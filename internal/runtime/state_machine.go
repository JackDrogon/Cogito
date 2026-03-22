package runtime

import (
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// RunState tracks the lifecycle of an entire workflow run.
// Valid transitions are enforced by allowedRunTransitions to prevent invalid
// state corruption. Terminal states (succeeded, failed, canceled) have no
// outbound transitions.
type RunState string

const (
	RunStatePending         RunState = "pending"
	RunStateRunning         RunState = "running"
	RunStateWaitingApproval RunState = "waiting_approval"
	RunStatePaused          RunState = "paused"
	RunStateSucceeded       RunState = "succeeded"
	RunStateFailed          RunState = "failed"
	RunStateCanceled        RunState = "canceled"
)

// StepState tracks the lifecycle of an individual workflow step.
// Valid transitions are enforced by allowedStepTransitions. Failed steps may
// be retried (queued again), while other terminal states have no outbound
// transitions.
type StepState string

const (
	StepStatePending         StepState = "pending"
	StepStateQueued          StepState = "queued"
	StepStateRunning         StepState = "running"
	StepStateWaitingApproval StepState = "waiting_approval"
	StepStateSucceeded       StepState = "succeeded"
	StepStateFailed          StepState = "failed"
	StepStateCanceled        StepState = "canceled"
)

var allowedRunTransitions = map[RunState]map[RunState]struct{}{
	RunStatePending: {
		RunStateRunning:  {},
		RunStateCanceled: {},
	},
	RunStateRunning: {
		RunStateWaitingApproval: {},
		RunStatePaused:          {},
		RunStateSucceeded:       {},
		RunStateFailed:          {},
		RunStateCanceled:        {},
	},
	RunStateWaitingApproval: {
		RunStateRunning:  {},
		RunStatePaused:   {},
		RunStateFailed:   {},
		RunStateCanceled: {},
	},
	RunStatePaused: {
		RunStateRunning:  {},
		RunStateCanceled: {},
	},
}

var allowedStepTransitions = map[StepState]map[StepState]struct{}{
	StepStatePending: {
		StepStateQueued:   {},
		StepStateCanceled: {},
	},
	StepStateQueued: {
		StepStateRunning:  {},
		StepStateCanceled: {},
	},
	StepStateRunning: {
		StepStateWaitingApproval: {},
		StepStateQueued:          {},
		StepStateSucceeded:       {},
		StepStateFailed:          {},
		StepStateCanceled:        {},
	},
	StepStateWaitingApproval: {
		StepStateRunning:   {},
		StepStateQueued:    {},
		StepStateSucceeded: {},
		StepStateFailed:    {},
		StepStateCanceled:  {},
	},
	StepStateFailed: {
		StepStateQueued:   {},
		StepStateCanceled: {},
	},
}

func validateEventPreconditions(compiled *workflow.CompiledWorkflow, snapshot *Snapshot, event store.Event, code ErrorCode) error {
	if compiled == nil {
		return newError(code, "compiled workflow is required")
	}

	if snapshot == nil {
		return newError(code, "snapshot is required")
	}

	if strings.TrimSpace(event.RunID) == "" {
		return newError(code, "event run id is required")
	}

	if snapshot.RunID == "" {
		snapshot.RunID = event.RunID
	}

	if event.RunID != snapshot.RunID {
		return newError(code, fmt.Sprintf("event run id %q does not match snapshot %q", event.RunID, snapshot.RunID))
	}

	if event.Sequence != snapshot.LastSequence+1 {
		return newError(code, fmt.Sprintf("invalid event sequence %d after %d", event.Sequence, snapshot.LastSequence))
	}

	return nil
}

func applyRunEvent(compiled *workflow.CompiledWorkflow, snapshot *Snapshot, transitions *[]Transition, event store.Event, data map[string]string, code ErrorCode) error {
	from := RunState(strings.TrimSpace(data[dataFromState]))
	to := RunState(strings.TrimSpace(data[dataToState]))
	summary := strings.TrimSpace(data[dataSummary])

	if err := ensureRunTransition(snapshot.State, from, to); err != nil {
		return wrapError(code, "invalid transition order", err)
	}

	snapshot.State = to

	if event.Type == store.EventRunCreated {
		initializePendingSteps(snapshot, compiled)
	}

	if event.Type == store.EventRunCanceled {
		cancelActiveSteps(snapshot)
	}

	recordTransition(transitions, Transition{
		Sequence:  event.Sequence,
		EventType: event.Type,
		Scope:     "run",
		From:      string(from),
		To:        string(to),
		Summary:   normalizeSummary(summary, adapters.ExecutionStateRunning),
	})

	return nil
}

func applyStepEvent(compiled *workflow.CompiledWorkflow, snapshot *Snapshot, transitions *[]Transition, event store.Event, data map[string]string, code ErrorCode) error {
	stepID := strings.TrimSpace(event.StepID)
	if stepID == "" {
		return newError(code, fmt.Sprintf("event %s missing step id", event.Type))
	}

	if _, ok := compiled.StepIndex[stepID]; !ok {
		return newError(code, fmt.Sprintf("event references unknown step %q", stepID))
	}

	current := snapshot.Steps[stepID]
	from := StepState(strings.TrimSpace(data[dataFromState]))
	to := StepState(strings.TrimSpace(data[dataToState]))
	summary := strings.TrimSpace(data[dataSummary])
	providerSessionID := strings.TrimSpace(data[dataProviderSessionID])

	if err := ensureStepTransition(current.State, from, to); err != nil {
		return wrapError(code, "invalid transition order", err)
	}

	current.State = to
	if event.AttemptID != "" {
		current.AttemptID = event.AttemptID
	}

	if providerSessionID != "" {
		current.ProviderSessionID = providerSessionID
	}

	if summary != "" {
		current.Summary = summary
	}

	if to == StepStateQueued && event.Type == store.EventStepRetried {
		current.AttemptID = ""
		current.ProviderSessionID = ""
	}

	snapshot.Steps[stepID] = current
	recordTransition(transitions, Transition{
		Sequence:          event.Sequence,
		EventType:         event.Type,
		Scope:             "step",
		StepID:            stepID,
		From:              string(from),
		To:                string(to),
		AttemptID:         event.AttemptID,
		ProviderSessionID: current.ProviderSessionID,
		Summary:           normalizeSummary(current.Summary, adapters.ExecutionStateRunning),
	})

	return nil
}

func applyApprovalEvent(compiled *workflow.CompiledWorkflow, snapshot *Snapshot, transitions *[]Transition, event store.Event, data map[string]string, code ErrorCode) error {
	stepID := strings.TrimSpace(event.StepID)
	if stepID == "" {
		return newError(code, fmt.Sprintf("event %s missing step id", event.Type))
	}

	if _, ok := compiled.StepIndex[stepID]; !ok {
		return newError(code, fmt.Sprintf("event references unknown step %q", stepID))
	}

	current := snapshot.Steps[stepID]
	from := StepState(strings.TrimSpace(data[dataFromState]))
	to := StepState(strings.TrimSpace(data[dataToState]))
	summary := strings.TrimSpace(data[dataSummary])
	providerSessionID := strings.TrimSpace(data[dataProviderSessionID])
	approvalTrigger := ApprovalTrigger(strings.TrimSpace(data[dataApprovalTrigger]))

	if err := ensureStepTransition(current.State, from, to); err != nil {
		return wrapError(code, "invalid transition order", err)
	}

	current.State = to
	if event.AttemptID != "" {
		current.AttemptID = event.AttemptID
	}

	if providerSessionID != "" {
		current.ProviderSessionID = providerSessionID
	}

	if summary != "" {
		current.Summary = summary
	}

	if event.Type == store.EventApprovalRequested {
		current.ApprovalID = event.ApprovalID
		current.ApprovalTrigger = approvalTrigger
	} else {
		current.ApprovalID = ""
		current.ApprovalTrigger = ""
	}

	snapshot.Steps[stepID] = current
	recordTransition(transitions, Transition{
		Sequence:          event.Sequence,
		EventType:         event.Type,
		Scope:             "step",
		StepID:            stepID,
		ApprovalID:        event.ApprovalID,
		From:              string(from),
		To:                string(to),
		AttemptID:         event.AttemptID,
		ProviderSessionID: current.ProviderSessionID,
		Summary:           normalizeSummary(current.Summary, adapters.ExecutionStateRunning),
	})

	return nil
}

func applyEvent(compiled *workflow.CompiledWorkflow, snapshot *Snapshot, transitions *[]Transition, event store.Event, code ErrorCode) error {
	if err := validateEventPreconditions(compiled, snapshot, event, code); err != nil {
		return err
	}

	data := cloneStringMap(event.Data)
	occurredAt := strings.TrimSpace(data[dataOccurredAt])

	if occurredAt == "" {
		return newError(code, fmt.Sprintf("event %s missing %s", event.Type, dataOccurredAt))
	}

	handler, err := lookupStateMachineEventHandler(event.Type)
	if err != nil {
		return newError(code, err.Error())
	}

	if err := handler.Apply(compiled, snapshot, transitions, event, data, code); err != nil {
		return err
	}

	snapshot.LastSequence = event.Sequence
	snapshot.UpdatedAt = occurredAt

	return nil
}

func ensureRunTransition(current, from, to RunState) error {
	if from == "" && to == RunStatePending {
		if current != "" {
			return newError(ErrorCodeState, fmt.Sprintf("run state is %q, want empty before creation", current))
		}

		return nil
	}

	if current != from {
		return newError(ErrorCodeState, fmt.Sprintf("run state is %q, want %q", current, from))
	}

	allowed, ok := allowedRunTransitions[from]
	if !ok {
		return newError(ErrorCodeState, fmt.Sprintf("run state %q is terminal", from))
	}

	if _, ok := allowed[to]; !ok {
		return newError(ErrorCodeState, fmt.Sprintf("run state cannot transition from %q to %q", from, to))
	}

	return nil
}

func ensureStepTransition(current, from, to StepState) error {
	if current != from {
		return newError(ErrorCodeState, fmt.Sprintf("step state is %q, want %q", current, from))
	}

	allowed, ok := allowedStepTransitions[from]
	if !ok {
		return newError(ErrorCodeState, fmt.Sprintf("step state %q is terminal", from))
	}

	if _, ok := allowed[to]; !ok {
		return newError(ErrorCodeState, fmt.Sprintf("step state cannot transition from %q to %q", from, to))
	}

	return nil
}

func initializePendingSteps(snapshot *Snapshot, compiled *workflow.CompiledWorkflow) {
	if snapshot.Steps == nil {
		snapshot.Steps = make(map[string]StepSnapshot, len(compiled.Steps))
	}

	for _, step := range compiled.Steps {
		snapshot.Steps[step.ID] = StepSnapshot{State: StepStatePending}
	}
}

func cancelActiveSteps(snapshot *Snapshot) {
	for stepID, step := range snapshot.Steps {
		if terminalStepStates.Has(step.State) {
			continue
		}

		step.State = StepStateCanceled
		snapshot.Steps[stepID] = step
	}
}

func validRunState(state RunState) bool {
	return validRunStates.Has(state)
}

func validStepState(state StepState) bool {
	return validStepStates.Has(state)
}
