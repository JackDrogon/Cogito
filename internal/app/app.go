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
	errHelpRequested = errors.New("help requested")

	workflowCommands = newCommandRegistry(
		workflowValidateCommand{},
	)
	rootCommands = newCommandRegistry(
		commandGroup{name: "workflow", summary: "Workflow operations", registry: workflowCommands},
		commandGroup{name: "feishu", summary: "Feishu Project (Meegle) task operations", registry: feishuCommandRegistry},
		commandGroup{name: "agents", summary: "Ad hoc code agent operations", registry: agentsCommandRegistry},
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
	registerSharedFlags(fs, &flags)

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stdout, "Usage: cogito %s [flags]\n", commandName)

		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, errHelpRequested
		}

		return nil, err
	}

	if strings.TrimSpace(flags.stateDir) == "" {
		flags.stateDir = defaultStateDir(flags.repo)
	}

	return &parsedSharedFlagsResult{flags: &flags, remainingArgs: fs.Args()}, nil
}

// registerSharedFlags binds the common execution flags onto fs. It is the
// single source of truth for the shared flag contract so commands that mix
// shared flags with command-specific flags (for example `cogito feishu run`)
// stay in sync with `parseSharedFlags` instead of duplicating definitions.
func registerSharedFlags(fs *flag.FlagSet, flags *sharedFlags) {
	fs.StringVar(&flags.repo, "repo", "", "Repository root for workflow execution")
	fs.StringVar(&flags.stateDir, "state-dir", "", "Run state directory (default: <repo>/.cogito/runs/<generated-run-id>)")
	fs.StringVar(&flags.approval, "approval", "", "Approval mode")
	fs.DurationVar(&flags.providerTimeout, "provider-timeout", 0, "Provider timeout (for example: 30s, 2m)")
	fs.BoolVar(&flags.allowDirty, "allow-dirty", false, "Allow dirty repository state")
	fs.BoolVar(&flags.verbose, "v", false, "Enable verbose logging")
}

func isHelpRequested(err error) bool {
	return errors.Is(err, errHelpRequested)
}

// defaultStateDir places new run state under <repo>/.cogito/runs so runs are
// anchored at the target repository root instead of the operator's cwd, and
// never scatter state across the rest of the worktree. An empty repo falls
// back to the current directory, matching the execution-context default.
func defaultStateDir(repo string) string {
	root := strings.TrimSpace(repo)
	if root == "" {
		root = "."
	}

	return filepath.Join(root, store.DefaultRunsRoot, generatedRunID())
}

func generatedRunID() string {
	return fmt.Sprintf("run-%d", time.Now().UTC().UnixNano())
}

func isSubcommandToken(token string) bool {
	return !strings.HasPrefix(token, "-")
}
