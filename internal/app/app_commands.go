package app

import (
	"context"
	"errors"
	"fmt"
	"io"
)

type workflowValidateCommand struct{}

func (workflowValidateCommand) Name() string    { return "validate" }
func (workflowValidateCommand) Summary() string { return "Validate a workflow file" }
func (workflowValidateCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	_, remainingArgs, err := parseSharedFlags("workflow validate", args, stdout)
	if err != nil {
		return err
	}

	if remainingArgs == nil {
		return nil
	}

	if len(remainingArgs) != 1 {
		return errors.New("workflow.validate: expects exactly 1 file argument")
	}

	if err := appsvc.ValidateWorkflow(ctx, ValidateWorkflowInput{WorkflowPath: remainingArgs[0]}); err != nil {
		return err
	}

	return presenter.PresentWorkflowValid(stdout)
}

type workflowRunCommand struct{}

func (workflowRunCommand) Name() string    { return "run" }
func (workflowRunCommand) Summary() string { return "Execute a workflow" }
func (workflowRunCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 && isSubcommandToken(args[0]) {
		flags, remainingArgs, err := parseSharedFlags("run", args[1:], stdout)
		if err != nil {
			return err
		}

		if remainingArgs == nil {
			return nil
		}

		if len(remainingArgs) > 0 {
			return fmt.Errorf("run does not accept extra positional arguments: %v", remainingArgs)
		}

		result, err := appsvc.RunWorkflow(ctx, RunWorkflowInput{WorkflowPath: args[0], Flags: flags})
		if err != nil {
			return err
		}

		return presenter.PresentRunWorkflow(stdout, result)
	}

	flags, remainingArgs, err := parseSharedFlags("run", args, stdout)
	if err != nil {
		return err
	}

	workflowPath, err := requireExactlyOneArg("run", "file", remainingArgs)
	if err != nil || workflowPath == "" {
		return err
	}

	result, err := appsvc.RunWorkflow(ctx, RunWorkflowInput{WorkflowPath: workflowPath, Flags: flags})
	if err != nil {
		return err
	}

	return presenter.PresentRunWorkflow(stdout, result)
}

type statusCommand struct{}

func (statusCommand) Name() string    { return "status" }
func (statusCommand) Summary() string { return "Show workflow run status" }
func (statusCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	request, err := parseStatusRequest(args, stdout)
	if err != nil || request == nil {
		return err
	}

	result, err := appsvc.StatusRun(ctx, StatusRunInput{StateDir: request.StateDir})
	if err != nil {
		return err
	}

	return presenter.PresentStatusRun(stdout, result)
}

type resumeCommand struct{}

func (resumeCommand) Name() string    { return "resume" }
func (resumeCommand) Summary() string { return "Resume a paused workflow" }
func (resumeCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	flags, err := parseSharedFlagsWithoutArgs("resume", args, stdout)
	if err != nil || flags == nil {
		return err
	}

	result, err := appsvc.ResumeRun(ctx, ResumeRunInput{Flags: flags})
	if err != nil {
		return err
	}

	return presenter.PresentMessage(stdout, result.Message)
}

type replayCommand struct{}

func (replayCommand) Name() string    { return "replay" }
func (replayCommand) Summary() string { return "Replay workflow from event log" }
func (replayCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	request, err := parseReplayRequest(args, stdout)
	if err != nil {
		return err
	}
	if request == nil {
		return nil
	}

	result, err := appsvc.ReplayRun(ctx, ReplayRunInput{EventsPath: request.EventsPath})
	if err != nil {
		return err
	}

	return presenter.PresentReplayRun(stdout, result)
}

type cancelCommand struct{}

func (cancelCommand) Name() string    { return "cancel" }
func (cancelCommand) Summary() string { return "Cancel a running workflow" }
func (cancelCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	flags, err := parseSharedFlagsWithoutArgs("cancel", args, stdout)
	if err != nil || flags == nil {
		return err
	}

	result, err := appsvc.CancelRun(ctx, CancelRunInput{StateDir: flags.stateDir})
	if err != nil {
		return err
	}

	return presenter.PresentMessage(stdout, result.Message)
}

type approveCommand struct{}

func (approveCommand) Name() string    { return "approve" }
func (approveCommand) Summary() string { return "Approve a waiting workflow" }
func (approveCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	flags, err := parseSharedFlagsWithoutArgs("approve", args, stdout)
	if err != nil || flags == nil {
		return err
	}

	result, err := appsvc.ApproveRun(ctx, ApproveRunInput{Flags: flags})
	if err != nil {
		return err
	}

	return presenter.PresentMessage(stdout, result.Message)
}

func parseSharedFlagsWithoutArgs(commandName string, args []string, stdout io.Writer) (*sharedFlags, error) {
	flags, remainingArgs, err := parseSharedFlags(commandName, args, stdout)
	if err != nil || remainingArgs == nil {
		return flags, err
	}

	if len(remainingArgs) > 0 {
		return nil, fmt.Errorf("%s does not accept positional arguments: %v", commandName, remainingArgs)
	}

	return flags, nil
}

func requireExactlyOneArg(commandName, argLabel string, args []string) (string, error) {
	if args == nil {
		return "", nil
	}

	if len(args) != 1 {
		return "", fmt.Errorf("%s: expects exactly 1 %s argument", commandName, argLabel)
	}

	return args[0], nil
}

type statusRequest struct {
	StateDir string
}

type replayInputRequest struct {
	EventsPath string
}

func parseStatusRequest(args []string, stdout io.Writer) (*statusRequest, error) {
	flags, err := parseSharedFlagsWithoutArgs("status", args, stdout)
	if err != nil || flags == nil {
		return nil, err
	}

	return &statusRequest{StateDir: flags.stateDir}, nil
}

func parseReplayRequest(args []string, stdout io.Writer) (*replayInputRequest, error) {
	_, remainingArgs, err := parseSharedFlags("replay", args, stdout)
	if err != nil || remainingArgs == nil {
		return nil, err
	}

	eventsPath, err := requireExactlyOneArg("replay", "events file", remainingArgs)
	if err != nil {
		return nil, err
	}

	return &replayInputRequest{EventsPath: eventsPath}, nil
}
