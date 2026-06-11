package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters/prompt"
	"github.com/JackDrogon/Cogito/internal/task/agentflow"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// adhocTaskID is the synthetic task identifier rendered into the main prompt
// for `cogito agents run`; ad hoc prompts have no external story id.
const adhocTaskID = "adhoc"

// agentsCommandRegistry is the subcommand table for `cogito agents ...`.
var agentsCommandRegistry = newCommandRegistry(
	agentsRunCommand{},
)

type agentsRunCommand struct{}

func (agentsRunCommand) Name() string { return "run" }
func (agentsRunCommand) Summary() string {
	return "Run an ad hoc prompt through a code agent workflow"
}

func (agentsRunCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	flags, err := parseAgentsRunFlags(args, stdout)
	if isHelpRequested(err) {
		return nil
	}
	if err != nil {
		return err
	}

	return runAgentsRun(ctx, flags, stdout)
}

// agentsRunFlags holds the parsed `cogito agents run` flags. The shared
// execution flags are embedded by value and threaded into RunCompiledWorkflow
// so the ephemeral run honors --state-dir/--approval/--repo just like
// `cogito run`.
type agentsRunFlags struct {
	prompt        string
	agentName     string
	noVerify      bool
	noCommitCheck bool
	shared        sharedFlags
}

// parseAgentsRunFlags parses the agents run command line. The prompt is a
// required positional argument; it may appear either before the flags
// (`agents run "task" -p claude`) or after them (`agents run -p claude "task"`),
// so the leading positional is split off before the flag set runs.
func parseAgentsRunFlags(args []string, stdout io.Writer) (agentsRunFlags, error) {
	var (
		promptToken string
		flagArgs    []string
	)
	if len(args) > 0 && isSubcommandToken(args[0]) {
		promptToken = args[0]
		flagArgs = args[1:]
	} else {
		flagArgs = args
	}

	fs := flag.NewFlagSet("agents run", flag.ContinueOnError)
	fs.SetOutput(stdout)

	flags := agentsRunFlags{}
	fs.StringVar(&flags.agentName, "p", agentflow.DefaultAgentName, "Code agent to delegate to (codex|claude|opencode)")
	fs.StringVar(&flags.agentName, "agent", agentflow.DefaultAgentName, "Code agent to delegate to (codex|claude|opencode)")
	fs.BoolVar(&flags.noVerify, "no-verify", false, "Skip the verify step")
	fs.BoolVar(&flags.noCommitCheck, "no-commit-check", false, "Skip the commit_check step")
	registerSharedFlags(fs, &flags.shared)

	fs.Usage = func() {
		_, _ = fmt.Fprintln(stdout, "Usage: cogito agents run <prompt> [-p codex|claude|opencode] [--no-verify] [--no-commit-check] [flags]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return agentsRunFlags{}, errHelpRequested
		}
		return agentsRunFlags{}, err
	}

	rest := fs.Args()
	if promptToken == "" {
		if len(rest) == 0 {
			return agentsRunFlags{}, errors.New("agents run: <prompt> is required")
		}
		promptToken = rest[0]
		rest = rest[1:]
	}
	if len(rest) > 0 {
		return agentsRunFlags{}, fmt.Errorf("agents run: unexpected positional arguments: %v (quote the prompt as a single argument)", rest)
	}

	flags.prompt = strings.TrimSpace(promptToken)
	if flags.prompt == "" {
		return agentsRunFlags{}, errors.New("agents run: <prompt> is required")
	}

	flags.agentName = strings.TrimSpace(flags.agentName)
	if flags.agentName == "" {
		flags.agentName = agentflow.DefaultAgentName
	}
	if !agentflow.IsValidAgentName(flags.agentName) {
		return agentsRunFlags{}, fmt.Errorf("agents run: invalid -p/--agent %q; must be one of codex, claude, opencode", flags.agentName)
	}

	if strings.TrimSpace(flags.shared.stateDir) == "" {
		flags.shared.stateDir = defaultStateDir(flags.shared.repo)
	}

	return flags, nil
}

func runAgentsRun(ctx context.Context, flags agentsRunFlags, stdout io.Writer) error {
	compiled, repoPath, err := buildAgentsRunPlan(flags)
	if err != nil {
		return err
	}

	// The agent prompt declares repoPath as the project root, so the runtime
	// wiring (runner Dir, repo lock, verify/commit_check workingDir) must
	// target the same canonical path. Thread it through the shared --repo
	// flag the run kernel already honors.
	sharedCopy := flags.shared
	sharedCopy.repo = repoPath

	output, err := appsvc.RunCompiledWorkflow(ctx, RunCompiledWorkflowInput{Compiled: compiled, Flags: &sharedCopy})
	if err != nil {
		return err
	}

	return presenter.PresentRunWorkflow(stdout, output)
}

// buildAgentsRunPlan resolves the target repo, builds the ephemeral spec from
// the ad hoc prompt, and compiles it. It returns the canonical repo path
// alongside the compiled workflow so the caller can target the runtime wiring
// at that repo. It is split out from runAgentsRun so the plan logic is
// unit-testable without executing a run.
func buildAgentsRunPlan(flags agentsRunFlags) (*workflow.CompiledWorkflow, string, error) {
	// An empty --repo means "the current directory", matching `cogito run`.
	// Canonicalize so the prompt and the runtime agree on one absolute root.
	rawRepoPath := strings.TrimSpace(flags.shared.repo)
	if rawRepoPath == "" {
		rawRepoPath = "."
	}

	repoPath, err := canonicalRepoPath(rawRepoPath)
	if err != nil {
		return nil, "", fmt.Errorf("agents run: resolve --repo %q: %w", rawRepoPath, err)
	}

	spec, err := agentflow.BuildSpec(agentflow.SpecOptions{
		Name:            fmt.Sprintf("agents-%s", flags.agentName),
		AgentName:       flags.agentName,
		RepoPath:        repoPath,
		Tasks:           []prompt.TaskRef{{ID: adhocTaskID, Text: flags.prompt}},
		WithVerify:      !flags.noVerify,
		WithCommitCheck: !flags.noCommitCheck,
	})
	if err != nil {
		return nil, "", err
	}

	compiled, err := workflow.CompileWorkflow(spec)
	if err != nil {
		return nil, "", err
	}

	return compiled, repoPath, nil
}
