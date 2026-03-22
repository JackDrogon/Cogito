package app

import (
	"fmt"
	"io"
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
	_, err := io.WriteString(stdout, formatRunStatus(output))
	return err
}

func (textPresenter) PresentMessage(stdout io.Writer, message string) error {
	_, err := fmt.Fprintln(stdout, message)
	return err
}
