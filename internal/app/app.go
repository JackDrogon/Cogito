package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/version"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type commandHandler func(args []string, stdout io.Writer) error

type sharedFlags struct {
	repo            string
	stateDir        string
	approval        string
	providerTimeout time.Duration
	allowDirty      bool
}

var rootCommands = map[string]commandHandler{
	"workflow": runWorkflowCommand,
	"run":      runRunCommand,
	"status":   runStatusCommand,
	"resume":   runResumeCommand,
	"replay":   runReplayCommand,
	"cancel":   runCancelCommand,
}

func printUsage(stdout io.Writer) {
	fmt.Fprintln(stdout, "Usage: cogito <command> [options]")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Commands:")
	fmt.Fprintln(stdout, "  workflow    Workflow operations (validate)")
	fmt.Fprintln(stdout, "  run         Execute a workflow")
	fmt.Fprintln(stdout, "  status      Show workflow run status")
	fmt.Fprintln(stdout, "  resume      Resume a paused workflow")
	fmt.Fprintln(stdout, "  replay      Replay workflow from event log")
	fmt.Fprintln(stdout, "  cancel      Cancel a running workflow")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Options:")
	fmt.Fprintln(stdout, "  --version   Show version information")
}

// Run executes the CLI application with the provided arguments.
func Run(args []string, stdout io.Writer) error {
	if len(args) > 0 && isSubcommandToken(args[0]) {
		handler, ok := rootCommands[args[0]]
		if !ok {
			return fmt.Errorf("unknown subcommand: %s", args[0])
		}

		return handler(args[1:], stdout)
	}

	fs := flag.NewFlagSet("Cogito", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	showVersion := fs.Bool("version", false, "Print version information")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	if *showVersion {
		_, err := fmt.Fprintln(stdout, version.Info())
		return err
	}

	printUsage(stdout)

	return nil
}

func runWorkflowCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("workflow subcommand is required")
	}

	if !isSubcommandToken(args[0]) {
		fs := flag.NewFlagSet("workflow", flag.ContinueOnError)
		fs.SetOutput(stdout)

		fs.Usage = func() {
			_, _ = fmt.Fprintln(stdout, "Usage: cogito workflow <subcommand>")
			_, _ = fmt.Fprintln(stdout, "Subcommands: validate")

			fs.PrintDefaults()
		}
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}

			return err
		}

		if len(fs.Args()) > 0 {
			return errors.New("workflow subcommand is required")
		}

		return errors.New("workflow subcommand is required")
	}

	switch args[0] {
	case "validate":
		return runWorkflowValidateCommand(args[1:], stdout)
	default:
		return fmt.Errorf("unknown workflow subcommand: %s", args[0])
	}
}

func runWorkflowValidateCommand(args []string, stdout io.Writer) error {
	_, remainingArgs, err := parseSharedFlags("workflow validate", args, stdout)
	if err != nil {
		return err
	}

	if remainingArgs == nil {
		return nil
	}

	if len(remainingArgs) != 1 {
		return errors.New("workflow validate expects exactly 1 file argument")
	}

	if _, err := workflow.LoadFile(remainingArgs[0]); err != nil {
		return err
	}

	_, err = fmt.Fprintln(stdout, "workflow valid")

	return err
}

func runRunCommand(args []string, stdout io.Writer) error {
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

		return executeWorkflowRun(args[0], flags, stdout)
	}

	flags, remainingArgs, err := parseSharedFlags("run", args, stdout)
	if err != nil {
		return err
	}

	if remainingArgs == nil {
		return nil
	}

	if len(remainingArgs) != 1 {
		return errors.New("run expects exactly 1 file argument")
	}

	return executeWorkflowRun(remainingArgs[0], flags, stdout)
}

func runStatusCommand(args []string, stdout io.Writer) error {
	flags, remainingArgs, err := parseSharedFlags("status", args, stdout)
	if err != nil {
		return err
	}

	if remainingArgs == nil {
		return nil
	}

	if len(remainingArgs) > 0 {
		return fmt.Errorf("status does not accept positional arguments: %v", remainingArgs)
	}

	stateDir, compiled, snapshot, err := loadRunStatus(flags.stateDir)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdout, "run_id=%s\nstate_dir=%s\nstate=%s\n", snapshot.RunID, stateDir, snapshot.State); err != nil {
		return err
	}

	for _, line := range formatStepSummaries(compiled, snapshot) {
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			return err
		}
	}

	return nil
}

func runResumeCommand(args []string, stdout io.Writer) error {
	flags, remainingArgs, err := parseSharedFlags("resume", args, stdout)
	if err != nil {
		return err
	}

	if remainingArgs == nil {
		return nil
	}

	if len(remainingArgs) > 0 {
		return fmt.Errorf("resume does not accept positional arguments: %v", remainingArgs)
	}

	return executeResumeRun(flags, stdout)
}

func runReplayCommand(args []string, stdout io.Writer) error {
	_, remainingArgs, err := parseSharedFlags("replay", args, stdout)
	if err != nil {
		return err
	}

	if remainingArgs == nil {
		return nil
	}

	if len(remainingArgs) != 1 {
		return errors.New("replay expects exactly 1 events file argument")
	}

	return executeReplay(remainingArgs[0], stdout)
}

func runCancelCommand(args []string, stdout io.Writer) error {
	flags, remainingArgs, err := parseSharedFlags("cancel", args, stdout)
	if err != nil {
		return err
	}

	if remainingArgs == nil {
		return nil
	}

	if len(remainingArgs) > 0 {
		return fmt.Errorf("cancel does not accept positional arguments: %v", remainingArgs)
	}

	return executeCancelRun(flags.stateDir, stdout)
}

func parseSharedFlags(commandName string, args []string, stdout io.Writer) (*sharedFlags, []string, error) {
	fs := flag.NewFlagSet(commandName, flag.ContinueOnError)
	fs.SetOutput(stdout)

	flags := sharedFlags{}
	fs.StringVar(&flags.repo, "repo", "", "Repository root for workflow execution")
	fs.StringVar(&flags.stateDir, "state-dir", "", "Run state directory (default: ref/tmp/runs/<generated-run-id>)")
	fs.StringVar(&flags.approval, "approval", "", "Approval mode")
	fs.DurationVar(&flags.providerTimeout, "provider-timeout", 0, "Provider timeout (for example: 30s, 2m)")
	fs.BoolVar(&flags.allowDirty, "allow-dirty", false, "Allow dirty repository state")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stdout, "Usage: cogito %s [flags]\n", commandName)

		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil, nil
		}

		return nil, nil, err
	}

	if strings.TrimSpace(flags.stateDir) == "" {
		flags.stateDir = defaultStateDir()
	}

	return &flags, fs.Args(), nil
}

func defaultStateDir() string {
	return filepath.Join(store.DefaultRunsRoot, generatedRunID())
}

func generatedRunID() string {
	return fmt.Sprintf("run-%d", time.Now().UTC().UnixNano())
}

func isSubcommandToken(token string) bool {
	return !strings.HasPrefix(token, "-")
}
