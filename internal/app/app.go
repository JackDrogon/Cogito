package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/version"
)

type sharedFlags struct {
	repo            string
	stateDir        string
	approval        string
	providerTimeout time.Duration
	allowDirty      bool
	verbose         bool
}

type parsedSharedFlagsResult struct {
	flags         *sharedFlags
	remainingArgs []string
}

var (
	workflowCommands = newCommandRegistry(
		workflowValidateCommand{},
	)
	rootCommands = newCommandRegistry(
		commandGroup{name: "workflow", summary: "Workflow operations", registry: workflowCommands},
		workflowRunCommand{},
		statusCommand{},
		resumeCommand{},
		replayCommand{},
		cancelCommand{},
		approveCommand{},
	)
)

func printUsage(stdout io.Writer, commands *commandRegistry) {
	fmt.Fprintln(stdout, "Usage: cogito <command> [options]")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Commands:")
	commands.printEntries(stdout)
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Options:")
	fmt.Fprintln(stdout, "  --version   Show version information")
}

// Run executes the CLI application with the provided arguments.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 && isSubcommandToken(args[0]) {
		cmd, ok := rootCommands.Lookup(args[0])
		if !ok {
			return fmt.Errorf("unknown subcommand: %s", args[0])
		}

		return cmd.Run(ctx, args[1:], stdout)
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

	printUsage(stdout, rootCommands)

	return nil
}
func parseSharedFlags(commandName string, args []string, stdout io.Writer) (*parsedSharedFlagsResult, error) {
	fs := flag.NewFlagSet(commandName, flag.ContinueOnError)
	fs.SetOutput(stdout)

	flags := sharedFlags{}
	fs.StringVar(&flags.repo, "repo", "", "Repository root for workflow execution")
	fs.StringVar(&flags.stateDir, "state-dir", "", "Run state directory (default: ref/tmp/runs/<generated-run-id>)")
	fs.StringVar(&flags.approval, "approval", "", "Approval mode")
	fs.DurationVar(&flags.providerTimeout, "provider-timeout", 0, "Provider timeout (for example: 30s, 2m)")
	fs.BoolVar(&flags.allowDirty, "allow-dirty", false, "Allow dirty repository state")
	fs.BoolVar(&flags.verbose, "v", false, "Enable verbose logging")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stdout, "Usage: cogito %s [flags]\n", commandName)

		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil
		}

		return nil, err
	}

	if strings.TrimSpace(flags.stateDir) == "" {
		flags.stateDir = defaultStateDir()
	}

	return &parsedSharedFlagsResult{flags: &flags, remainingArgs: fs.Args()}, nil
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
