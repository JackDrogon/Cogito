package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/provider"
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
		Summary:   normalizeSummary(summary, provider.ExecutionStateRunning),
	})

	return nil
}

// stepEventFold is the shared decode/validate/fold prefix for every
// step-scoped event. Plain step transitions and approval transitions differ
// only in their event-specific fields, so both build on this common fold and
// finish with commit.
type stepEventFold struct {
	stepID string
	step   StepSnapshot
	from   StepState
	to     StepState
}

// foldStepEventCommon validates the step reference and transition order, then
// folds the fields every step-scoped event shares (state, attempt, provider
// session, summary) into a copy of the step snapshot.
func foldStepEventCommon(request stateMachineEventRequest) (stepEventFold, error) {
	stepID := strings.TrimSpace(request.Event.StepID)
	if stepID == "" {
		return stepEventFold{}, newError(request.Code, fmt.Sprintf("event %s missing step id", request.Event.Type))
	}

	if _, ok := request.Compiled.StepIndex[stepID]; !ok {
		return stepEventFold{}, newError(request.Code, fmt.Sprintf("event references unknown step %q", stepID))
	}

	current := request.Snapshot.Steps[stepID]
	from := StepState(strings.TrimSpace(request.Data[dataFromState]))
	to := StepState(strings.TrimSpace(request.Data[dataToState]))
	summary := strings.TrimSpace(request.Data[dataSummary])
	providerSessionID := strings.TrimSpace(request.Data[dataProviderSessionID])

	if err := ensureStepTransition(current.State, from, to); err != nil {
		return stepEventFold{}, wrapError(request.Code, "invalid transition order", err)
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

	return stepEventFold{stepID: stepID, step: current, from: from, to: to}, nil
}

// commit writes the folded step back into the snapshot and records the
// transition. Event.ApprovalID is empty for plain step events, so including it
// unconditionally keeps both event families on one code path.
func (f stepEventFold) commit(request stateMachineEventRequest) {
	request.Snapshot.Steps[f.stepID] = f.step
	recordTransition(request.Transitions, Transition{
		Sequence:          request.Event.Sequence,
		EventType:         request.Event.Type,
		Scope:             "step",
		StepID:            f.stepID,
		ApprovalID:        request.Event.ApprovalID,
		From:              string(f.from),
		To:                string(f.to),
		AttemptID:         request.Event.AttemptID,
		ProviderSessionID: f.step.ProviderSessionID,
		Summary:           normalizeSummary(f.step.Summary, provider.ExecutionStateRunning),
	})
}

func applyStepEvent(request stateMachineEventRequest) error {
	fold, err := foldStepEventCommon(request)
	if err != nil {
		return err
	}

	// Fold structured output whenever an event carries it (only successful
	// agent transitions do). The field is per-step, not per-attempt, so it is
	// preserved through retries until a later attempt overwrites it.
	if len(request.Event.StructuredOutput) > 0 {
		fold.step.StructuredOutput = append(json.RawMessage(nil), request.Event.StructuredOutput...)
	}

	switch request.Event.Type {
	case store.EventStepStarted:
		fold.step.Attempts++
		fold.step.Resumable = false
	case store.EventStepInterrupted:
		// EventStepInterrupted parks a running step back in the queued state
		// while preserving AttemptID + ProviderSessionID so executeStep can
		// resume the same provider session on the next pass.
		if fold.to == StepStateQueued {
			fold.step.Resumable = true
		}
	case store.EventStepRetried:
		// EventStepRetried is a fresh attempt: drop the prior session and resume
		// intent so the next pass performs a clean Start.
		if fold.to == StepStateQueued {
			fold.step.AttemptID = ""
			fold.step.ProviderSessionID = ""
			fold.step.Resumable = false
		}
	case store.EventStepSucceeded, store.EventStepFailed:
		// Resume intent is single-shot. Once the step re-enters running (the
		// resume actually fired) or reaches a terminal outcome, clear Resumable
		// so a stale "true" cannot persist in the checkpoint indefinitely. Only
		// EventStepInterrupted ever sets it back to true.
		fold.step.Resumable = false
	}

	fold.commit(request)

	return nil
}

func applyApprovalEvent(request stateMachineEventRequest) error {
	fold, err := foldStepEventCommon(request)
	if err != nil {
		return err
	}

	if request.Event.Type == store.EventApprovalRequested {
		fold.step.ApprovalID = request.Event.ApprovalID
		fold.step.ApprovalTrigger = ApprovalTrigger(strings.TrimSpace(request.Data[dataApprovalTrigger]))
	} else {
		fold.step.ApprovalID = ""
		fold.step.ApprovalTrigger = ""
	}

	fold.commit(request)

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

	for index := range compiled.Steps {
		step := &compiled.Steps[index]
		snapshot.Steps[step.ID] = StepSnapshot{State: StepStatePending}
	}
}

func cancelActiveSteps(snapshot *Snapshot) {
	for stepID, step := range snapshot.Steps { //nolint:gocritic // map values cannot be addressed; the copy is inherent
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
