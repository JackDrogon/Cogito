package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/JackDrogon/Cogito/internal/provider"
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

// ApprovalRequestedParams groups the parameters needed to persist an approval
// request event when a step enters the waiting_approval state.
type ApprovalRequestedParams struct {
	StepID            string
	AttemptID         string
	ProviderSessionID string
	Summary           string
	Trigger           ApprovalTrigger
	Status            provider.ExecutionState
}

func (e *Engine) persistApprovalRequested(params ApprovalRequestedParams) error {
	event := store.Event{
		Type:       store.EventApprovalRequested,
		StepID:     params.StepID,
		AttemptID:  params.AttemptID,
		ApprovalID: e.idGen.NewApprovalID(params.StepID),
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

// ApprovalResolutionParams groups the parameters needed to persist an approval
// resolution event (granted, denied, or timed out) for a pending gate.
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

// RunTransitionParams groups the parameters needed to persist a run-level
// state transition event.
type RunTransitionParams struct {
	EventType store.EventType
	From      RunState
	To        RunState
	Message   string
}

func (e *Engine) persistRunTransition(params RunTransitionParams) error {
	summary := normalizeSummary(params.Message, provider.ExecutionStateRunning)
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

// StepTransitionParams groups the parameters needed to persist a step-level
// state transition event.
type StepTransitionParams struct {
	EventType         store.EventType
	StepID            string
	From              StepState
	To                StepState
	AttemptID         string
	ProviderSessionID string
	Summary           string
	NormalizedStatus  string
	// StructuredOutput carries the normalized AgentResult JSON for a succeeded
	// agent step. It is nil for every other transition.
	StructuredOutput json.RawMessage
	// Usage carries provider-reported token and cost usage for terminal step
	// transitions. It remains nil when the provider reports no usage.
	Usage *provider.Usage
	// Resumable records that an interrupted step keeps a resumable provider
	// session. It is encoded into event.Data as "true" so replay and downstream
	// readers can audit the resume intent; applyStepEvent still derives the
	// snapshot flag from the event type.
	Resumable bool
}

func (e *Engine) persistStepTransition(params StepTransitionParams) error {
	event := store.Event{
		Type:      params.EventType,
		StepID:    params.StepID,
		AttemptID: params.AttemptID,
		Message:   normalizeSummary(params.Summary, provider.ExecutionStateRunning),
		Data: map[string]string{
			dataOccurredAt:        e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:         string(params.From),
			dataToState:           string(params.To),
			dataProviderSessionID: params.ProviderSessionID,
			dataSummary:           normalizeSummary(params.Summary, provider.ExecutionStateRunning),
		},
		StructuredOutput: params.StructuredOutput,
		Usage:            storeUsage(params.Usage),
	}

	if params.NormalizedStatus != "" {
		event.Data[dataNormalizedStatus] = params.NormalizedStatus
	}

	if params.Resumable {
		event.Data[dataResumable] = "true"
	}

	return e.persistEvent(event)
}

func storeUsage(usage *provider.Usage) *store.Usage {
	if usage == nil {
		return nil
	}

	return &store.Usage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.TotalTokens,
		CostUSD:      usage.CostUSD,
	}
}

func (e *Engine) persistEvent(event store.Event) error {
	event.RunID = e.runID
	redactEventPayload(&event)

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

	return e.store.SaveCheckpoint(checkpointFromSnapshot(e.snapshot, e.executionContext()))
}

// redactEventPayload scrubs the human-facing event fields (Message, summary)
// before they become durable. StructuredOutput is deliberately NOT redacted:
// it is the machine channel — verify and commit_check consume the agent's
// normalized result (commits, verification commands) straight from the event
// log, and rewriting those bytes would silently change later step behavior
// whenever a value merely looks secret-shaped. The human-readable copies of
// the same output (provider log files, live sink, summaries) remain redacted.
func redactEventPayload(event *store.Event) {
	event.Message = provider.RedactSecretString(event.Message)
	if event.Data != nil {
		event.Data[dataSummary] = provider.RedactSecretString(event.Data[dataSummary])
	}
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

	for index := range e.compiled.Steps {
		step := &e.compiled.Steps[index]
		if e.snapshot.Steps[step.ID].State != StepStateSucceeded {
			return false
		}
	}

	return true
}

// FailRunParams groups the parameters needed to persist a step failure event
// followed by a run failure event when an execution error terminates a step.
type FailRunParams struct {
	StepID            string
	AttemptID         string
	ProviderSessionID string
	ExecutionErr      error
	Message           string
	SkipRetry         bool
}

func (e *Engine) failRunForExecutionError(params FailRunParams) error {
	summary := normalizeSummary(params.ExecutionErr.Error(), provider.ExecutionStateFailed)
	if !params.SkipRetry {
		step, err := e.lookupStep(params.StepID)
		if err != nil {
			return err
		}

		retried, err := e.maybeRetryStep(stepRetryParams{
			Step:              step,
			AttemptID:         params.AttemptID,
			ProviderSessionID: params.ProviderSessionID,
			FailureSummary:    summary,
		})
		if err != nil {
			return err
		}

		if retried {
			return nil
		}
	}

	if err := e.persistStepTransition(StepTransitionParams{
		EventType:         store.EventStepFailed,
		StepID:            params.StepID,
		From:              StepStateRunning,
		To:                StepStateFailed,
		AttemptID:         params.AttemptID,
		ProviderSessionID: params.ProviderSessionID,
		Summary:           summary,
		NormalizedStatus:  string(provider.ExecutionStateFailed),
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

func normalizeSummary(summary string, status provider.ExecutionState) string {
	summary = strings.TrimSpace(summary)
	if summary != "" {
		return summary
	}

	if status != "" {
		return string(status)
	}

	return "transition recorded"
}
