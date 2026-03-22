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

	return e.persistRunTransition(store.EventRunCreated, RunState(""), RunStatePending, "run created")
}

func (e *Engine) queueReadySteps() error {
	if e.snapshot.State != RunStateRunning {
		return nil
	}

	for _, stepID := range e.selectReadyPendingStepIDs() {
		if err := e.persistStepTransition(store.EventStepQueued, stepID, StepStatePending, StepStateQueued, "", "", "step ready", ""); err != nil {
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

func (e *Engine) persistApprovalRequested(stepID, attemptID, providerSessionID, summary string, trigger ApprovalTrigger, status adapters.ExecutionState) error {
	event := store.Event{
		Type:       store.EventApprovalRequested,
		StepID:     stepID,
		AttemptID:  attemptID,
		ApprovalID: e.ids.NewApprovalID(stepID),
		Message:    summary,
		Data: map[string]string{
			dataOccurredAt:        e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:         string(StepStateRunning),
			dataToState:           string(StepStateWaitingApproval),
			dataProviderSessionID: providerSessionID,
			dataApprovalTrigger:   string(trigger),
			dataSummary:           summary,
			dataNormalizedStatus:  string(status),
		},
	}

	return e.persistEvent(event)
}

func (e *Engine) persistApprovalResolution(eventType store.EventType, pending pendingApproval, from, to StepState, summary string) error {
	event := store.Event{
		Type:       eventType,
		StepID:     pending.Step.ID,
		AttemptID:  pending.AttemptID,
		ApprovalID: pending.ApprovalID,
		Message:    summary,
		Data: map[string]string{
			dataOccurredAt:        e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:         string(from),
			dataToState:           string(to),
			dataProviderSessionID: pending.ProviderSessionID,
			dataApprovalTrigger:   string(pending.Trigger),
			dataSummary:           summary,
		},
	}

	return e.persistEvent(event)
}

func (e *Engine) persistRunTransition(eventType store.EventType, from, to RunState, message string) error {
	summary := normalizeSummary(message, adapters.ExecutionStateRunning)
	event := store.Event{
		Type:    eventType,
		Message: summary,
		Data: map[string]string{
			dataOccurredAt: e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:  string(from),
			dataToState:    string(to),
			dataSummary:    summary,
		},
	}

	return e.persistEvent(event)
}

func (e *Engine) persistStepTransition(eventType store.EventType, stepID string, from, to StepState, attemptID, providerSessionID, summary, normalizedStatus string) error {
	event := store.Event{
		Type:      eventType,
		StepID:    stepID,
		AttemptID: attemptID,
		Message:   normalizeSummary(summary, adapters.ExecutionStateRunning),
		Data: map[string]string{
			dataOccurredAt:        e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:         string(from),
			dataToState:           string(to),
			dataProviderSessionID: providerSessionID,
			dataSummary:           normalizeSummary(summary, adapters.ExecutionStateRunning),
		},
	}

	if normalizedStatus != "" {
		event.Data[dataNormalizedStatus] = normalizedStatus
	}

	return e.persistEvent(event)
}

func (e *Engine) persistEvent(event store.Event) error {
	event.RunID = e.runID
	previewSnapshot := cloneSnapshot(e.snapshot)
	previewEvent := cloneEvent(event)
	previewEvent.Sequence = previewSnapshot.LastSequence + 1

	if err := applyEvent(e.compiled, &previewSnapshot, nil, previewEvent, ErrorCodeState); err != nil {
		return err
	}

	appended, err := e.store.AppendEvent(event)
	if err != nil {
		return err
	}

	if err := applyEvent(e.compiled, &e.snapshot, &e.transitions, appended, ErrorCodeState); err != nil {
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

func (e *Engine) failRunForExecutionError(stepID, attemptID, providerSessionID string, executionErr error, message string) error {
	summary := normalizeSummary(executionErr.Error(), adapters.ExecutionStateFailed)
	if err := e.persistStepTransition(store.EventStepFailed, stepID, StepStateRunning, StepStateFailed, attemptID, providerSessionID, summary, string(adapters.ExecutionStateFailed)); err != nil {
		return err
	}

	if err := e.persistRunTransition(store.EventRunFailed, RunStateRunning, RunStateFailed, summary); err != nil {
		return err
	}

	return wrapError(ErrorCodeExecution, message, executionErr)
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
