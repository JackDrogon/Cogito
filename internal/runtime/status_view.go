package runtime

import "github.com/JackDrogon/Cogito/internal/workflow"

type StepStatusView struct {
	StepID  string
	State   StepState
	Summary string
}

type RunStatusView struct {
	RunID     string
	State     RunState
	StepViews []StepStatusView
}

type TransitionView struct {
	Sequence  int64
	EventType string
	Scope     string
	StepID    string
	From      string
	To        string
	Summary   string
}

type ReplayView struct {
	RunID        string
	State        RunState
	Transitions  []TransitionView
	StepStatuses []StepStatusView
}

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
			EventType: string(transition.EventType),
			Scope:     transition.Scope,
			StepID:    transition.StepID,
			From:      transition.From,
			To:        transition.To,
			Summary:   transition.Summary,
		})
	}

	return view
}
