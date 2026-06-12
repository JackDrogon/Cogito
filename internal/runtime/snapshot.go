package runtime

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type StepSnapshot struct {
	State             StepState
	Attempts          int
	AttemptID         string
	ProviderSessionID string
	ApprovalID        string
	ApprovalTrigger   ApprovalTrigger
	Summary           string
	// StructuredOutput holds the normalized AgentResult JSON produced by a
	// succeeded agent step. It is persisted through events and checkpoints so
	// downstream steps can read upstream agent results across resumes.
	StructuredOutput json.RawMessage
	// Resumable marks a step that was interrupted while running but still has a
	// provider session id to resume from. executeStep dispatches to resumeStep
	// instead of Start while a queued step carries this flag.
	Resumable bool
}

// Snapshot represents the in-memory state of a workflow run at a point in time.
// It is rebuilt by folding events from the EventStore and serves as the source
// of truth for scheduling decisions, status queries, and checkpoint persistence.
type Snapshot struct {
	RunID        string
	State        RunState
	LastSequence int64
	UpdatedAt    string
	Steps        map[string]StepSnapshot
}

// Transition records one persisted state change folded from an event. It is
// accumulated during replay and exposed via Engine.Transitions so callers can
// render the full ordered history without re-reading the event log.
type Transition struct {
	Sequence          int64
	EventType         store.EventType
	Scope             string
	StepID            string
	ApprovalID        string
	From              string
	To                string
	AttemptID         string
	ProviderSessionID string
	Summary           string
}

// ReplayResult is the output of a full event-log replay. It pairs the final
// Snapshot with the ordered list of Transitions so callers can inspect both
// the current state and the complete history in one pass.
type ReplayResult struct {
	Snapshot    Snapshot
	Transitions []Transition
}

func checkpointFromSnapshot(snapshot Snapshot, execContext executionContext) *store.Checkpoint {
	execContext = normalizeExecutionContext(execContext)
	repoPath, workingDir := execContext.repoPath, execContext.workingDir

	steps := make(map[string]store.StepCheckpoint, len(snapshot.Steps))
	for stepID, step := range snapshot.Steps { //nolint:gocritic // map values cannot be addressed; the copy is inherent
		steps[stepID] = store.StepCheckpoint{
			State:             string(step.State),
			Attempts:          step.Attempts,
			AttemptID:         step.AttemptID,
			ProviderSessionID: step.ProviderSessionID,
			ApprovalID:        step.ApprovalID,
			ApprovalTrigger:   string(step.ApprovalTrigger),
			Summary:           step.Summary,
			StructuredOutput:  cloneRawMessage(step.StructuredOutput),
			Resumable:         step.Resumable,
		}
	}

	return &store.Checkpoint{
		RunID:        snapshot.RunID,
		RepoPath:     repoPath,
		WorkingDir:   workingDir,
		State:        string(snapshot.State),
		LastSequence: snapshot.LastSequence,
		UpdatedAt:    snapshot.UpdatedAt,
		Steps:        steps,
	}
}

func snapshotFromCheckpoint(
	runID string,
	compiled *workflow.CompiledWorkflow,
	checkpoint *store.Checkpoint,
) (Snapshot, error) {
	if checkpoint == nil {
		return Snapshot{}, newError(ErrorCodeState, "checkpoint is required")
	}

	if strings.TrimSpace(checkpoint.RunID) != "" && strings.TrimSpace(checkpoint.RunID) != runID {
		return Snapshot{}, newError(
			ErrorCodeState,
			fmt.Sprintf("checkpoint run id %q does not match %q", checkpoint.RunID, runID),
		)
	}

	state := RunState(strings.TrimSpace(checkpoint.State))
	if !validRunState(state) {
		return Snapshot{}, newError(ErrorCodeState, fmt.Sprintf("unknown checkpoint run state %q", checkpoint.State))
	}

	snapshot := Snapshot{
		RunID:        runID,
		State:        state,
		LastSequence: checkpoint.LastSequence,
		UpdatedAt:    checkpoint.UpdatedAt,
		Steps:        make(map[string]StepSnapshot, len(compiled.Steps)),
	}

	for stepID := range checkpoint.Steps {
		if _, ok := compiled.StepIndex[stepID]; !ok {
			return Snapshot{}, newError(ErrorCodeState, fmt.Sprintf("checkpoint references unknown step %q", stepID))
		}
	}

	for index := range compiled.Steps {
		step := &compiled.Steps[index]
		stored := checkpoint.Steps[step.ID]

		stepState := StepState(strings.TrimSpace(stored.State))
		if stepState == "" {
			stepState = StepStatePending
		}

		if !validStepState(stepState) {
			return Snapshot{}, newError(
				ErrorCodeState,
				fmt.Sprintf("unknown checkpoint step state %q for %s", stored.State, step.ID),
			)
		}

		snapshot.Steps[step.ID] = StepSnapshot{
			State:             stepState,
			Attempts:          stored.Attempts,
			AttemptID:         stored.AttemptID,
			ProviderSessionID: stored.ProviderSessionID,
			ApprovalID:        stored.ApprovalID,
			ApprovalTrigger:   ApprovalTrigger(strings.TrimSpace(stored.ApprovalTrigger)),
			Summary:           stored.Summary,
			StructuredOutput:  cloneRawMessage(stored.StructuredOutput),
			Resumable:         stored.Resumable,
		}
	}

	return snapshot, nil
}

// executionContext pairs the repo root with the step working directory; the
// two values always travel together (engine state, checkpoint persistence)
// and fall back to each other when one is missing.
type executionContext struct {
	repoPath   string
	workingDir string
}

func checkpointExecutionContext(checkpoint *store.Checkpoint, fallback executionContext) executionContext {
	if checkpoint == nil {
		return normalizeExecutionContext(fallback)
	}

	return normalizeExecutionContext(executionContext{
		repoPath:   firstNonEmpty(strings.TrimSpace(checkpoint.RepoPath), fallback.repoPath),
		workingDir: firstNonEmpty(strings.TrimSpace(checkpoint.WorkingDir), fallback.workingDir),
	})
}

// normalizeExecutionContext trims both paths and lets each fall back to the
// other so a context with only one of the two still yields a usable pair.
func normalizeExecutionContext(ctx executionContext) executionContext {
	ctx.repoPath = strings.TrimSpace(ctx.repoPath)
	ctx.workingDir = strings.TrimSpace(ctx.workingDir)

	if ctx.repoPath == "" {
		ctx.repoPath = ctx.workingDir
	}

	if ctx.workingDir == "" {
		ctx.workingDir = ctx.repoPath
	}

	return ctx
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	cloned := Snapshot{
		RunID:        snapshot.RunID,
		State:        snapshot.State,
		LastSequence: snapshot.LastSequence,
		UpdatedAt:    snapshot.UpdatedAt,
		Steps:        make(map[string]StepSnapshot, len(snapshot.Steps)),
	}

	for stepID, step := range snapshot.Steps { //nolint:gocritic // map values cannot be addressed; the copy is inherent
		step.StructuredOutput = cloneRawMessage(step.StructuredOutput)
		cloned.Steps[stepID] = step
	}

	return cloned
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}

	cloned := make(json.RawMessage, len(value))
	copy(cloned, value)

	return cloned
}

func cloneTransitions(transitions []Transition) []Transition {
	if transitions == nil {
		return nil
	}

	cloned := make([]Transition, len(transitions))
	copy(cloned, transitions)

	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}

	cloned := make(map[string]string, len(values))
	maps.Copy(cloned, values)

	return cloned
}

func cloneEvent(event store.Event) store.Event {
	return store.Event{
		Sequence:         event.Sequence,
		Type:             event.Type,
		RunID:            event.RunID,
		StepID:           event.StepID,
		AttemptID:        event.AttemptID,
		ApprovalID:       event.ApprovalID,
		Message:          event.Message,
		Data:             cloneStringMap(event.Data),
		StructuredOutput: cloneRawMessage(event.StructuredOutput),
		Usage:            cloneStoreUsage(event.Usage),
	}
}

func cloneStoreUsage(usage *store.Usage) *store.Usage {
	if usage == nil {
		return nil
	}

	cloned := *usage

	return &cloned
}

func newZeroSnapshot(runID string) Snapshot {
	return Snapshot{
		RunID: runID,
		Steps: map[string]StepSnapshot{},
	}
}

func recordTransition(transitions *[]Transition, transition Transition) {
	if transitions == nil {
		return
	}

	*transitions = append(*transitions, transition)
}

// executionContext returns the engine's current repo/working-dir pair.
func (e *Engine) executionContext() executionContext {
	return executionContext{repoPath: e.repoPath, workingDir: e.workingDir}
}

// applyExecutionContext installs a resolved repo/working-dir pair on the
// engine.
func (e *Engine) applyExecutionContext(ctx executionContext) {
	e.repoPath = ctx.repoPath
	e.workingDir = ctx.workingDir
}
