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

	remainingArgs := parsed.remainingArgs
	if len(remainingArgs) != 1 {
		return errors.New("workflow.validate: expects exactly 1 file argument")
	}

	if err := appService.ValidateWorkflow(ctx, ValidateWorkflowInput{WorkflowPath: remainingArgs[0]}); err != nil {
		return err
	}

	return presenter.PresentWorkflowValid(stdout)
}

// runCommandName is the shared subcommand token for `cogito run` and
// `cogito agents run`.
const runCommandName = "run"

type workflowRunCommand struct{}

func (workflowRunCommand) Name() string    { return runCommandName }
func (workflowRunCommand) Summary() string { return "Execute a workflow" }
func (workflowRunCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 && isSubcommandToken(args[0]) {
		return runWorkflowSubcommand(ctx, args[0], args[1:], stdout)
	}

	parsed, err := parseSharedFlags(runCommandName, args, stdout)
	if isHelpRequested(err) {
		return nil
	}

	if err != nil {
		return err
	}

	flags := parsed.flags
	remainingArgs := parsed.remainingArgs

	workflowPath, err := requireExactlyOneArg(runCommandName, "file", remainingArgs)
	if err != nil {
		return err
	}

	return executeRunWorkflow(ctx, runWorkflowAction{workflowPath: workflowPath, flags: flags, stdout: stdout})
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

	result, err := appService.StatusRun(ctx, StatusRunInput{StateDir: request.StateDir})
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

	result, err := appService.ResumeRun(ctx, ResumeRunInput{Flags: flags})
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

	result, err := appService.ReplayRun(ctx, ReplayRunInput{EventsPath: request.EventsPath})
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

	result, err := appService.CancelRun(ctx, CancelRunInput{StateDir: flags.stateDir})
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

	result, err := appService.ApproveRun(ctx, ApproveRunInput{Flags: flags})
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

	if err != nil {
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

	if err != nil {
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
	parsed, err := parseSharedFlags(runCommandName, args, stdout)
	if isHelpRequested(err) {
		return nil
	}

	if err != nil {
		return err
	}

	flags := parsed.flags
	remainingArgs := parsed.remainingArgs

	if len(remainingArgs) > 0 {
		return fmt.Errorf("run does not accept extra positional arguments: %v", remainingArgs)
	}

	return executeRunWorkflow(ctx, runWorkflowAction{workflowPath: workflowPath, flags: flags, stdout: stdout})
}

// runWorkflowAction bundles the resolved inputs of a `cogito run` invocation;
// both argument shapes (positional-first and flags-only) funnel into
// executeRunWorkflow with it.
type runWorkflowAction struct {
	workflowPath string
	flags        *sharedFlags
	stdout       io.Writer
}

// executeRunWorkflow is the shared tail of every `cogito run` spelling:
// execute, replay verbose events when asked, then present the outcome.
func executeRunWorkflow(ctx context.Context, action runWorkflowAction) error {
	result, err := appService.RunWorkflow(ctx, RunWorkflowInput{WorkflowPath: action.workflowPath, Flags: action.flags})
	if verboseErr := presentVerboseRun(action.stdout, action.flags, result, err); verboseErr != nil {
		return verboseErr
	}

	if err != nil {
		return err
	}

	return presenter.PresentRunWorkflow(action.stdout, result)
}

func presentVerboseRun(stdout io.Writer, flags *sharedFlags, result RunWorkflowOutput, runErr error) error {
	if flags == nil || !flags.verbose {
		return nil
	}

	// On failure the result may not carry a state dir, so fall back to the
	// user-provided one; without it there is no event log to replay.
	if runErr != nil {
		if flags.stateDir == "" {
			return nil
		}

		return printVerboseEvents(stdout, flags.stateDir)
	}

	return printVerboseEvents(stdout, result.StateDir)
}

func printVerboseEvents(stdout io.Writer, stateDir string) error {
	events, err := store.ReadEventsFile(filepath.Join(stateDir, store.EventsFileName))
	if err != nil {
		return err
	}

	logger := newVerboseLogger(true, stdout)
	for i := range events {
		logger.logEvent(events[i])
	}

	return nil
}
