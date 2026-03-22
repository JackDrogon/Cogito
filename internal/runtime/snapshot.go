package runtime

import (
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type StepSnapshot struct {
	State             StepState
	AttemptID         string
	ProviderSessionID string
	ApprovalID        string
	ApprovalTrigger   ApprovalTrigger
	Summary           string
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

type ReplayResult struct {
	Snapshot    Snapshot
	Transitions []Transition
}

func checkpointFromSnapshot(snapshot Snapshot, repoPath, workingDir string) *store.Checkpoint {
	repoPath, workingDir = normalizeExecutionContext(repoPath, workingDir)

	steps := make(map[string]store.StepCheckpoint, len(snapshot.Steps))
	for stepID, step := range snapshot.Steps {
		steps[stepID] = store.StepCheckpoint{
			State:             string(step.State),
			AttemptID:         step.AttemptID,
			ProviderSessionID: step.ProviderSessionID,
			ApprovalID:        step.ApprovalID,
			ApprovalTrigger:   string(step.ApprovalTrigger),
			Summary:           step.Summary,
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

func snapshotFromCheckpoint(runID string, compiled *workflow.CompiledWorkflow, checkpoint *store.Checkpoint) (Snapshot, error) {
	if checkpoint == nil {
		return Snapshot{}, newError(ErrorCodeState, "checkpoint is required")
	}

	if strings.TrimSpace(checkpoint.RunID) != "" && strings.TrimSpace(checkpoint.RunID) != runID {
		return Snapshot{}, newError(ErrorCodeState, fmt.Sprintf("checkpoint run id %q does not match %q", checkpoint.RunID, runID))
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

	for _, step := range compiled.Steps {
		stored := checkpoint.Steps[step.ID]

		stepState := StepState(strings.TrimSpace(stored.State))
		if stepState == "" {
			stepState = StepStatePending
		}

		if !validStepState(stepState) {
			return Snapshot{}, newError(ErrorCodeState, fmt.Sprintf("unknown checkpoint step state %q for %s", stored.State, step.ID))
		}

		snapshot.Steps[step.ID] = StepSnapshot{
			State:             stepState,
			AttemptID:         stored.AttemptID,
			ProviderSessionID: stored.ProviderSessionID,
			ApprovalID:        stored.ApprovalID,
			ApprovalTrigger:   ApprovalTrigger(strings.TrimSpace(stored.ApprovalTrigger)),
			Summary:           stored.Summary,
		}
	}

	return snapshot, nil
}

func checkpointExecutionContext(checkpoint *store.Checkpoint, repoPath, workingDir string) (string, string) {
	if checkpoint == nil {
		return normalizeExecutionContext(repoPath, workingDir)
	}

	return normalizeExecutionContext(firstNonEmpty(strings.TrimSpace(checkpoint.RepoPath), repoPath), firstNonEmpty(strings.TrimSpace(checkpoint.WorkingDir), workingDir))
}

func normalizeExecutionContext(repoPath, workingDir string) (string, string) {
	repoPath = strings.TrimSpace(repoPath)
	workingDir = strings.TrimSpace(workingDir)

	if repoPath == "" {
		repoPath = workingDir
	}

	if workingDir == "" {
		workingDir = repoPath
	}

	return repoPath, workingDir
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

	for stepID, step := range snapshot.Steps {
		cloned.Steps[stepID] = step
	}

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
	for key, value := range values {
		cloned[key] = value
	}

	return cloned
}

func cloneEvent(event store.Event) store.Event {
	return store.Event{
		Sequence:   event.Sequence,
		Type:       event.Type,
		RunID:      event.RunID,
		StepID:     event.StepID,
		AttemptID:  event.AttemptID,
		ApprovalID: event.ApprovalID,
		Message:    event.Message,
		Data:       cloneStringMap(event.Data),
	}
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
