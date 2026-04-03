package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/JackDrogon/Cogito/internal/store"
)

type workflowValidateCommand struct{}

func (workflowValidateCommand) Name() string    { return "validate" }
func (workflowValidateCommand) Summary() string { return "Validate a workflow file" }
func (workflowValidateCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	parsed, err := parseSharedFlags("workflow validate", args, stdout)
	if isHelpRequested(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if parsed == nil {
		return nil
	}

	remainingArgs := parsed.remainingArgs
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
		return runWorkflowSubcommand(ctx, args[0], args[1:], stdout)
	}

	parsed, err := parseSharedFlags("run", args, stdout)
	if isHelpRequested(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if parsed == nil {
		return nil
	}

	flags := parsed.flags
	remainingArgs := parsed.remainingArgs

	workflowPath, err := requireExactlyOneArg("run", "file", remainingArgs)
	if err != nil || workflowPath == "" {
		return err
	}

	result, err := appsvc.RunWorkflow(ctx, RunWorkflowInput{WorkflowPath: workflowPath, Flags: flags})
	if verboseErr := presentVerboseRun(stdout, flags, result, err); verboseErr != nil {
		return verboseErr
	}

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
	if err != nil {
		return err
	}

	if request == nil {
		return nil
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
	if isHelpRequested(err) {
		return nil
	}

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
		if isHelpRequested(err) {
			return nil
		}

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
	if isHelpRequested(err) {
		return nil
	}

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
	if isHelpRequested(err) {
		return nil
	}

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
	parsed, err := parseSharedFlags(commandName, args, stdout)
	if isHelpRequested(err) {
		return nil, errHelpRequested
	}

	if err != nil || parsed == nil {
		return nil, err
	}

	flags := parsed.flags
	remainingArgs := parsed.remainingArgs

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
	if isHelpRequested(err) {
		return nil, errHelpRequested
	}

	if err != nil || flags == nil {
		return nil, err
	}

	return &statusRequest{StateDir: flags.stateDir}, nil
}

func parseReplayRequest(args []string, stdout io.Writer) (*replayInputRequest, error) {
	parsed, err := parseSharedFlags("replay", args, stdout)
	if isHelpRequested(err) {
		return nil, errHelpRequested
	}

	if err != nil || parsed == nil {
		return nil, err
	}

	remainingArgs := parsed.remainingArgs

	eventsPath, err := requireExactlyOneArg("replay", "events file", remainingArgs)
	if err != nil {
		return nil, err
	}

	return &replayInputRequest{EventsPath: eventsPath}, nil
}

func runWorkflowSubcommand(ctx context.Context, workflowPath string, args []string, stdout io.Writer) error {
	parsed, err := parseSharedFlags("run", args, stdout)
	if isHelpRequested(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if parsed == nil {
		return nil
	}

	flags := parsed.flags
	remainingArgs := parsed.remainingArgs

	if len(remainingArgs) > 0 {
		return fmt.Errorf("run does not accept extra positional arguments: %v", remainingArgs)
	}

	result, err := appsvc.RunWorkflow(ctx, RunWorkflowInput{WorkflowPath: workflowPath, Flags: flags})
	if verboseErr := presentVerboseRun(stdout, flags, result, err); verboseErr != nil {
		return verboseErr
	}

	if err != nil {
		return err
	}

	return presenter.PresentRunWorkflow(stdout, result)
}

func presentVerboseRun(stdout io.Writer, flags *sharedFlags, result RunWorkflowOutput, runErr error) error {
	if flags == nil || !flags.verbose {
		return nil
	}

	if runErr != nil && flags.stateDir != "" {
		return printVerboseEvents(stdout, flags.stateDir)
	}

	if runErr == nil {
		return printVerboseEvents(stdout, result.StateDir)
	}

	return nil
}

func printVerboseEvents(stdout io.Writer, stateDir string) error {
	events, err := store.ReadEventsFile(filepath.Join(stateDir, "events.jsonl"))
	if err != nil {
		return err
	}

	logger := newVerboseLogger(true, stdout)
	for _, event := range events {
		logger.logEvent(event)
	}

	return nil
}
