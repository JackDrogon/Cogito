package app

import (
	"context"
	"fmt"
	"io"
)

var runs runService
var appsvc = newApplicationService()

func executeWorkflowRun(ctx context.Context, workflowPath string, flags *sharedFlags, stdout io.Writer) error {
	result, err := appsvc.RunWorkflow(ctx, RunWorkflowInput{WorkflowPath: workflowPath, Flags: flags})
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "run_id=%s\nstate_dir=%s\nstate=%s\n", result.RunID, result.StateDir, result.State)
	return err
}

func executeResumeRun(ctx context.Context, flags *sharedFlags, stdout io.Writer) error {
	result, err := appsvc.ResumeRun(ctx, ResumeRunInput{Flags: flags})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, result.Message)
	return err
}

func executeCancelRun(ctx context.Context, stateDir string, stdout io.Writer) error {
	result, err := appsvc.CancelRun(ctx, CancelRunInput{StateDir: stateDir})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, result.Message)
	return err
}

func executeApproveRun(ctx context.Context, flags *sharedFlags, stdout io.Writer) error {
	result, err := appsvc.ApproveRun(ctx, ApproveRunInput{Flags: flags})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, result.Message)
	return err
}
