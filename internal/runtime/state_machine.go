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

type eventPreconditionsParams struct {
	Compiled *workflow.CompiledWorkflow
	Snapshot *Snapshot
	Event    store.Event
	Code     ErrorCode
}

func validateEventPreconditions(params eventPreconditionsParams) error {
	if params.Compiled == nil {
		return newError(params.Code, "compiled workflow is required")
	}

	if params.Snapshot == nil {
		return newError(params.Code, "snapshot is required")
	}

	if strings.TrimSpace(params.Event.RunID) == "" {
		return newError(params.Code, "event run id is required")
	}

	if params.Snapshot.RunID == "" {
		params.Snapshot.RunID = params.Event.RunID
	}

	if params.Event.RunID != params.Snapshot.RunID {
		return newError(params.Code, fmt.Sprintf("event run id %q does not match snapshot %q", params.Event.RunID, params.Snapshot.RunID))
	}

	if params.Event.Sequence != params.Snapshot.LastSequence+1 {
		return newError(params.Code, fmt.Sprintf("invalid event sequence %d after %d", params.Event.Sequence, params.Snapshot.LastSequence))
	}

	return nil
}

func applyRunEvent(request stateMachineEventRequest) error {
	from := RunState(strings.TrimSpace(request.Data[dataFromState]))
	to := RunState(strings.TrimSpace(request.Data[dataToState]))
	summary := strings.TrimSpace(request.Data[dataSummary])

	if err := ensureRunTransition(request.Snapshot.State, from, to); err != nil {
		return wrapError(request.Code, "invalid transition order", err)
	}

	request.Snapshot.State = to

	if request.Event.Type == store.EventRunCreated {
		initializePendingSteps(request.Snapshot, request.Compiled)
	}

	if request.Event.Type == store.EventRunCanceled {
		cancelActiveSteps(request.Snapshot)
	}

	recordTransition(request.Transitions, Transition{
		Sequence:  request.Event.Sequence,
		EventType: request.Event.Type,
		Scope:     "run",
		From:      string(from),
		To:        string(to),
		Summary:   normalizeSummary(summary, adapters.ExecutionStateRunning),
	})

	return nil
}

func applyStepEvent(request stateMachineEventRequest) error {
	stepID := strings.TrimSpace(request.Event.StepID)
	if stepID == "" {
		return newError(request.Code, fmt.Sprintf("event %s missing step id", request.Event.Type))
	}

	if _, ok := request.Compiled.StepIndex[stepID]; !ok {
		return newError(request.Code, fmt.Sprintf("event references unknown step %q", stepID))
	}

	current := request.Snapshot.Steps[stepID]
	from := StepState(strings.TrimSpace(request.Data[dataFromState]))
	to := StepState(strings.TrimSpace(request.Data[dataToState]))
	summary := strings.TrimSpace(request.Data[dataSummary])
	providerSessionID := strings.TrimSpace(request.Data[dataProviderSessionID])

	if err := ensureStepTransition(current.State, from, to); err != nil {
		return wrapError(request.Code, "invalid transition order", err)
	}

	current.State = to
	if request.Event.AttemptID != "" {
		current.AttemptID = request.Event.AttemptID
	}

	if providerSessionID != "" {
		current.ProviderSessionID = providerSessionID
	}

	if summary != "" {
		current.Summary = summary
	}

	if to == StepStateQueued && request.Event.Type == store.EventStepRetried {
		current.AttemptID = ""
		current.ProviderSessionID = ""
	}

	request.Snapshot.Steps[stepID] = current
	recordTransition(request.Transitions, Transition{
		Sequence:          request.Event.Sequence,
		EventType:         request.Event.Type,
		Scope:             "step",
		StepID:            stepID,
		From:              string(from),
		To:                string(to),
		AttemptID:         request.Event.AttemptID,
		ProviderSessionID: current.ProviderSessionID,
		Summary:           normalizeSummary(current.Summary, adapters.ExecutionStateRunning),
	})

	return nil
}

func applyApprovalEvent(request stateMachineEventRequest) error {
	stepID := strings.TrimSpace(request.Event.StepID)
	if stepID == "" {
		return newError(request.Code, fmt.Sprintf("event %s missing step id", request.Event.Type))
	}

	if _, ok := request.Compiled.StepIndex[stepID]; !ok {
		return newError(request.Code, fmt.Sprintf("event references unknown step %q", stepID))
	}

	current := request.Snapshot.Steps[stepID]
	from := StepState(strings.TrimSpace(request.Data[dataFromState]))
	to := StepState(strings.TrimSpace(request.Data[dataToState]))
	summary := strings.TrimSpace(request.Data[dataSummary])
	providerSessionID := strings.TrimSpace(request.Data[dataProviderSessionID])
	approvalTrigger := ApprovalTrigger(strings.TrimSpace(request.Data[dataApprovalTrigger]))

	if err := ensureStepTransition(current.State, from, to); err != nil {
		return wrapError(request.Code, "invalid transition order", err)
	}

	current.State = to
	if request.Event.AttemptID != "" {
		current.AttemptID = request.Event.AttemptID
	}

	if providerSessionID != "" {
		current.ProviderSessionID = providerSessionID
	}

	if summary != "" {
		current.Summary = summary
	}

	if request.Event.Type == store.EventApprovalRequested {
		current.ApprovalID = request.Event.ApprovalID
		current.ApprovalTrigger = approvalTrigger
	} else {
		current.ApprovalID = ""
		current.ApprovalTrigger = ""
	}

	request.Snapshot.Steps[stepID] = current
	recordTransition(request.Transitions, Transition{
		Sequence:          request.Event.Sequence,
		EventType:         request.Event.Type,
		Scope:             "step",
		StepID:            stepID,
		ApprovalID:        request.Event.ApprovalID,
		From:              string(from),
		To:                string(to),
		AttemptID:         request.Event.AttemptID,
		ProviderSessionID: current.ProviderSessionID,
		Summary:           normalizeSummary(current.Summary, adapters.ExecutionStateRunning),
	})

	return nil
}

type applyEventParams struct {
	Compiled    *workflow.CompiledWorkflow
	Snapshot    *Snapshot
	Transitions *[]Transition
	Event       store.Event
	Code        ErrorCode
}

func applyEvent(params applyEventParams) error {
	if err := validateEventPreconditions(eventPreconditionsParams{
		Compiled: params.Compiled,
		Snapshot: params.Snapshot,
		Event:    params.Event,
		Code:     params.Code,
	}); err != nil {
		return err
	}

	data := cloneStringMap(params.Event.Data)
	occurredAt := strings.TrimSpace(data[dataOccurredAt])

	if occurredAt == "" {
		return newError(params.Code, fmt.Sprintf("event %s missing %s", params.Event.Type, dataOccurredAt))
	}

	handler, err := lookupStateMachineEventHandler(params.Event.Type)
	if err != nil {
		return newError(params.Code, err.Error())
	}

	if err := handler.Apply(stateMachineEventRequest{
		Compiled:    params.Compiled,
		Snapshot:    params.Snapshot,
		Transitions: params.Transitions,
		Event:       params.Event,
		Data:        data,
		Code:        params.Code,
	}); err != nil {
		return err
	}

	params.Snapshot.LastSequence = params.Event.Sequence
	params.Snapshot.UpdatedAt = occurredAt

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
