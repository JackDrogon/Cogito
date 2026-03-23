package runtime

import (
	"fmt"
	"strings"
	"time"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

func (e *Engine) ensureInitialized() error {
	if e.snapshot.State != "" {
		return nil
	}

	return e.persistRunTransition(RunTransitionParams{
		EventType: store.EventRunCreated,
		From:      RunState(""),
		To:        RunStatePending,
		Message:   "run created",
	})
}

func (e *Engine) queueReadySteps() error {
	if e.snapshot.State != RunStateRunning {
		return nil
	}

	for _, stepID := range e.selectReadyPendingStepIDs() {
		if err := e.persistStepTransition(StepTransitionParams{
			EventType:         store.EventStepQueued,
			StepID:            stepID,
			From:              StepStatePending,
			To:                StepStateQueued,
			AttemptID:         "",
			ProviderSessionID: "",
			Summary:           "step ready",
			NormalizedStatus:  "",
		}); err != nil {
			return err
		}
	}

	return nil
}

func (e *Engine) selectReadyPendingStepIDs() []string {
	ready := make([]string, 0)

	for _, stepID := range e.compiled.TopologicalOrder {
		stepSnapshot := e.snapshot.Steps[stepID]
		if stepSnapshot.State != StepStatePending {
			continue
		}

		step := e.compiled.Steps[e.compiled.StepIndex[stepID]]
		dependenciesReady := true

		for _, dependencyID := range step.Needs {
			if e.snapshot.Steps[dependencyID].State != StepStateSucceeded {
				dependenciesReady = false
				break
			}
		}

		if dependenciesReady {
			ready = append(ready, stepID)
		}
	}

	return ready
}

// ApprovalRequestedParams groups parameters for approval request events.
type ApprovalRequestedParams struct {
	StepID            string
	AttemptID         string
	ProviderSessionID string
	Summary           string
	Trigger           ApprovalTrigger
	Status            adapters.ExecutionState
}

func (e *Engine) persistApprovalRequested(params ApprovalRequestedParams) error {
	event := store.Event{
		Type:       store.EventApprovalRequested,
		StepID:     params.StepID,
		AttemptID:  params.AttemptID,
		ApprovalID: e.ids.NewApprovalID(params.StepID),
		Message:    params.Summary,
		Data: map[string]string{
			dataOccurredAt:        e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:         string(StepStateRunning),
			dataToState:           string(StepStateWaitingApproval),
			dataProviderSessionID: params.ProviderSessionID,
			dataApprovalTrigger:   string(params.Trigger),
			dataSummary:           params.Summary,
			dataNormalizedStatus:  string(params.Status),
		},
	}

	return e.persistEvent(event)
}

// ApprovalResolutionParams groups parameters for approval resolution events.
type ApprovalResolutionParams struct {
	EventType store.EventType
	Pending   pendingApproval
	From      StepState
	To        StepState
	Summary   string
}

func (e *Engine) persistApprovalResolution(params ApprovalResolutionParams) error {
	event := store.Event{
		Type:       params.EventType,
		StepID:     params.Pending.Step.ID,
		AttemptID:  params.Pending.AttemptID,
		ApprovalID: params.Pending.ApprovalID,
		Message:    params.Summary,
		Data: map[string]string{
			dataOccurredAt:        e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:         string(params.From),
			dataToState:           string(params.To),
			dataProviderSessionID: params.Pending.ProviderSessionID,
			dataApprovalTrigger:   string(params.Pending.Trigger),
			dataSummary:           params.Summary,
		},
	}

	return e.persistEvent(event)
}

type RunTransitionParams struct {
	EventType store.EventType
	From      RunState
	To        RunState
	Message   string
}

func (e *Engine) persistRunTransition(params RunTransitionParams) error {
	summary := normalizeSummary(params.Message, adapters.ExecutionStateRunning)
	event := store.Event{
		Type:    params.EventType,
		Message: summary,
		Data: map[string]string{
			dataOccurredAt: e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:  string(params.From),
			dataToState:    string(params.To),
			dataSummary:    summary,
		},
	}

	return e.persistEvent(event)
}

// StepTransitionParams groups parameters for step state transitions.
type StepTransitionParams struct {
	EventType         store.EventType
	StepID            string
	From              StepState
	To                StepState
	AttemptID         string
	ProviderSessionID string
	Summary           string
	NormalizedStatus  string
}

func (e *Engine) persistStepTransition(params StepTransitionParams) error {
	event := store.Event{
		Type:      params.EventType,
		StepID:    params.StepID,
		AttemptID: params.AttemptID,
		Message:   normalizeSummary(params.Summary, adapters.ExecutionStateRunning),
		Data: map[string]string{
			dataOccurredAt:        e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:         string(params.From),
			dataToState:           string(params.To),
			dataProviderSessionID: params.ProviderSessionID,
			dataSummary:           normalizeSummary(params.Summary, adapters.ExecutionStateRunning),
		},
	}

	if params.NormalizedStatus != "" {
		event.Data[dataNormalizedStatus] = params.NormalizedStatus
	}

	return e.persistEvent(event)
}

func (e *Engine) persistEvent(event store.Event) error {
	event.RunID = e.runID
	previewSnapshot := cloneSnapshot(e.snapshot)
	previewEvent := cloneEvent(event)
	previewEvent.Sequence = previewSnapshot.LastSequence + 1

	if err := applyEvent(applyEventParams{
		Compiled: e.compiled,
		Snapshot: &previewSnapshot,
		Event:    previewEvent,
		Code:     ErrorCodeState,
	}); err != nil {
		return err
	}

	appended, err := e.store.AppendEvent(event)
	if err != nil {
		return err
	}

	if err := applyEvent(applyEventParams{
		Compiled:    e.compiled,
		Snapshot:    &e.snapshot,
		Transitions: &e.transitions,
		Event:       appended,
		Code:        ErrorCodeState,
	}); err != nil {
		return err
	}

	return e.store.SaveCheckpoint(checkpointFromSnapshot(e.snapshot, e.repoPath, e.workingDir))
}

func (e *Engine) lookupStep(stepID string) (workflow.CompiledStep, error) {
	index, ok := e.compiled.StepIndex[stepID]
	if !ok {
		return workflow.CompiledStep{}, newError(ErrorCodeConfig, fmt.Sprintf("unknown step %q", stepID))
	}

	return e.compiled.Steps[index], nil
}

func (e *Engine) allStepsSucceeded() bool {
	if len(e.compiled.Steps) == 0 {
		return true
	}

	for _, step := range e.compiled.Steps {
		if e.snapshot.Steps[step.ID].State != StepStateSucceeded {
			return false
		}
	}

	return true
}

// FailRunParams groups parameters for run failure events.
type FailRunParams struct {
	StepID            string
	AttemptID         string
	ProviderSessionID string
	ExecutionErr      error
	Message           string
}

func (e *Engine) failRunForExecutionError(params FailRunParams) error {
	summary := normalizeSummary(params.ExecutionErr.Error(), adapters.ExecutionStateFailed)
	if err := e.persistStepTransition(StepTransitionParams{
		EventType:         store.EventStepFailed,
		StepID:            params.StepID,
		From:              StepStateRunning,
		To:                StepStateFailed,
		AttemptID:         params.AttemptID,
		ProviderSessionID: params.ProviderSessionID,
		Summary:           summary,
		NormalizedStatus:  string(adapters.ExecutionStateFailed),
	}); err != nil {
		return err
	}

	if err := e.persistRunTransition(RunTransitionParams{
		EventType: store.EventRunFailed,
		From:      RunStateRunning,
		To:        RunStateFailed,
		Message:   summary,
	}); err != nil {
		return err
	}

	return wrapError(ErrorCodeExecution, params.Message, params.ExecutionErr)
}

func latestEventSequence(events []store.Event) int64 {
	if len(events) == 0 {
		return 0
	}

	return events[len(events)-1].Sequence
}

func normalizeSummary(summary string, status adapters.ExecutionState) string {
	summary = strings.TrimSpace(summary)
	if summary != "" {
		return summary
	}

	if status != "" {
		return string(status)
	}

	return "transition recorded"
}
