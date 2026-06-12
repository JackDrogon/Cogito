package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/JackDrogon/Cogito/internal/runtime"
)

type textPresenter struct{}

func (textPresenter) PresentWorkflowValid(stdout io.Writer) error {
	_, err := fmt.Fprintln(stdout, "workflow valid")
	return err
}

func (textPresenter) PresentRunWorkflow(stdout io.Writer, output RunWorkflowOutput) error {
	_, err := fmt.Fprintf(stdout, "run_id=%s\nstate_dir=%s\nstate=%s\n", output.RunID, output.StateDir, output.State)
	return err
}

func (textPresenter) PresentStatusRun(stdout io.Writer, output StatusRunOutput) error {
	_, err := io.WriteString(stdout, renderStatusView(output))
	return err
}

func (textPresenter) PresentReplayRun(stdout io.Writer, output ReplayRunOutput) error {
	_, err := io.WriteString(stdout, renderReplayView(output.View))
	return err
}

func (textPresenter) PresentMessage(stdout io.Writer, message string) error {
	_, err := fmt.Fprintln(stdout, message)
	return err
}

func renderStatusView(output StatusRunOutput) string {
	var builder strings.Builder

	view := output.View

	_, _ = fmt.Fprintf(&builder, "run_id=%s\nstate_dir=%s\nstate=%s\n", view.RunID, output.StateDir, view.State)

	for _, step := range view.StepViews {
		_, _ = fmt.Fprintf(&builder, "step=%s state=%s", step.StepID, step.State)

		if strings.TrimSpace(step.Summary) != "" {
			_, _ = fmt.Fprintf(&builder, " summary=%q", step.Summary)
		}

		builder.WriteByte('\n')
	}

	for _, orphan := range output.Orphans {
		if !orphan.Alive || !orphan.IdentityMatched {
			continue
		}

		_, _ = fmt.Fprintf(&builder, "WARNING: orphan provider process pid %d (step %s) still running; resume or cancel will terminate it\n", orphan.Record.PID, orphan.StepID)
	}

	return builder.String()
}

func renderReplayView(view runtime.ReplayView) string {
	var builder strings.Builder

	builder.WriteString("replay OK\n")
	_, _ = fmt.Fprintf(&builder, "run_id=%s\nstate=%s\ntransitions=%d\n", view.RunID, view.State, len(view.Transitions))

	return builder.String()
}
