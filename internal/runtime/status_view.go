package runtime

import (
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/workflow"
)

type StepStatusView struct {
	StepID   string
	State    StepState
	Summary  string
	Rendered string
}

type RunStatusView struct {
	RunID     string
	State     RunState
	StepViews []StepStatusView
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
		rendered := fmt.Sprintf("step=%s state=%s", stepID, step.State)
		if summary := strings.TrimSpace(step.Summary); summary != "" {
			rendered += fmt.Sprintf(" summary=%q", summary)
		}

		view.StepViews = append(view.StepViews, StepStatusView{
			StepID:   stepID,
			State:    step.State,
			Summary:  step.Summary,
			Rendered: rendered,
		})
	}

	return view
}
