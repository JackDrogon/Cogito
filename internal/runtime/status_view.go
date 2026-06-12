package runtime

import "github.com/JackDrogon/Cogito/internal/workflow"

// StepStatusView is a read-only projection of a single step's current state
// and summary, suitable for display by CLI presenters.
type StepStatusView struct {
	StepID  string
	State   StepState
	Summary string
}

// RunStatusView is a read-only projection of a run's current state and the
// ordered list of step statuses, suitable for display by CLI presenters.
type RunStatusView struct {
	RunID     string
	State     RunState
	StepViews []StepStatusView
}

// TransitionView is a read-only projection of one persisted state transition,
// used by the replay presenter to show the ordered event history.
type TransitionView struct {
	Sequence  int64
	EventType string
	Scope     string
	StepID    string
	From      string
	To        string
	Summary   string
}

// ReplayView is a read-only projection of a full replay result, combining the
// ordered transition history with the final per-step statuses. It is consumed
// by the replay presenter to render the complete event log.
type ReplayView struct {
	RunID        string
	State        RunState
	Transitions  []TransitionView
	StepStatuses []StepStatusView
}

// BuildRunStatusView constructs a RunStatusView from a compiled workflow and
// the current snapshot. Steps are ordered by the workflow's topological order.
// A nil compiled workflow returns a view with only the run-level fields set.
func BuildRunStatusView(compiled *workflow.CompiledWorkflow, snapshot Snapshot) RunStatusView {
	view := RunStatusView{
		RunID: snapshot.RunID,
		State: snapshot.State,
	}

	if compiled == nil {
		return view
	}

	view.StepViews = make([]StepStatusView, 0, len(compiled.TopologicalOrder))

	for _, stepID := range compiled.TopologicalOrder {
		step := snapshot.Steps[stepID]
		view.StepViews = append(view.StepViews, StepStatusView{
			StepID:  stepID,
			State:   step.State,
			Summary: step.Summary,
		})
	}

	return view
}

// BuildReplayView constructs a ReplayView from a compiled workflow and a
// ReplayResult, combining the ordered transition history with the final
// per-step statuses derived from the replay snapshot.
func BuildReplayView(compiled *workflow.CompiledWorkflow, replay ReplayResult) ReplayView {
	statusView := BuildRunStatusView(compiled, replay.Snapshot)
	view := ReplayView{
		RunID:        replay.Snapshot.RunID,
		State:        replay.Snapshot.State,
		StepStatuses: append([]StepStatusView(nil), statusView.StepViews...),
		Transitions:  make([]TransitionView, 0, len(replay.Transitions)),
	}

	for i := range replay.Transitions {
		transition := replay.Transitions[i]
		view.Transitions = append(view.Transitions, TransitionView{
			Sequence:  transition.Sequence,
			EventType: transition.EventType.String(),
			Scope:     transition.Scope,
			StepID:    transition.StepID,
			From:      transition.From,
			To:        transition.To,
			Summary:   transition.Summary,
		})
	}

	return view
}
