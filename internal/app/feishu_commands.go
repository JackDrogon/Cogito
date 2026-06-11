package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JackDrogon/Cogito/internal/task/feishuproject"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// feishuCommandRegistry is the subcommand table for `cogito feishu ...`.
// Kept private to this package so the feishu surface stays decoupled from
// workflow commands.
var feishuCommandRegistry = newCommandRegistry(
	feishuPullCommand{},
	feishuWatchCommand{},
	feishuRunCommand{},
)

type feishuPullCommand struct{}

func (feishuPullCommand) Name() string { return "pull" }
func (feishuPullCommand) Summary() string {
	return "Pull stories from a Feishu Project (Meegle) space"
}

func (feishuPullCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	flags, err := parseFeishuFlags("feishu pull", args, stdout)
	if isHelpRequested(err) {
		return nil
	}
	if err != nil {
		return err
	}

	return runFeishuPull(ctx, flags, stdout)
}

type feishuWatchCommand struct{}

func (feishuWatchCommand) Name() string { return "watch" }
func (feishuWatchCommand) Summary() string {
	return "Poll a Feishu Project space on an interval and emit diffs"
}

func (feishuWatchCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	flags, err := parseFeishuFlags("feishu watch", args, stdout)
	if isHelpRequested(err) {
		return nil
	}
	if err != nil {
		return err
	}

	return runFeishuWatch(ctx, flags, stdout)
}

type feishuRunCommand struct{}

func (feishuRunCommand) Name() string { return "run" }
func (feishuRunCommand) Summary() string {
	return "Delegate a Feishu Project story to a code agent workflow"
}

func (feishuRunCommand) Run(ctx context.Context, args []string, stdout io.Writer) error {
	flags, err := parseFeishuRunFlags(args, stdout)
	if isHelpRequested(err) {
		return nil
	}
	if err != nil {
		return err
	}

	return runFeishuRun(ctx, flags, stdout)
}

// feishuRunFlags holds the parsed `cogito feishu run` flags. The shared
// execution flags are embedded by value and threaded into RunCompiledWorkflow
// so the ephemeral run honors --state-dir/--approval/--repo just like
// `cogito run`.
type feishuRunFlags struct {
	storyID       int64
	configPath    string
	agentName     string
	noVerify      bool
	noCommitCheck bool
	shared        sharedFlags
}

// parseFeishuRunFlags parses the feishu run command line. The story id is a
// required positional argument; it may appear either before the flags
// (`feishu run 7004 -c cfg.toml`) or after them (`feishu run -c cfg.toml 7004`),
// so the leading positional is split off before the flag set runs.
func parseFeishuRunFlags(args []string, stdout io.Writer) (feishuRunFlags, error) {
	var (
		storyToken string
		flagArgs   []string
	)
	if len(args) > 0 && isSubcommandToken(args[0]) {
		storyToken = args[0]
		flagArgs = args[1:]
	} else {
		flagArgs = args
	}

	fs := flag.NewFlagSet("feishu run", flag.ContinueOnError)
	fs.SetOutput(stdout)

	flags := feishuRunFlags{}
	fs.StringVar(&flags.configPath, "c", "", "Path to Cogito TOML config containing [meegle] section")
	fs.StringVar(&flags.configPath, "config", "", "Path to Cogito TOML config containing [meegle] section")
	fs.StringVar(&flags.agentName, "agent", "codex", "Code agent to delegate to (codex|claude|opencode)")
	fs.BoolVar(&flags.noVerify, "no-verify", false, "Skip the verify step")
	fs.BoolVar(&flags.noCommitCheck, "no-commit-check", false, "Skip the commit_check step")
	registerSharedFlags(fs, &flags.shared)

	fs.Usage = func() {
		_, _ = fmt.Fprintln(stdout, "Usage: cogito feishu run <story-id> -c <config.toml> [--agent codex|claude|opencode] [--no-verify] [--no-commit-check] [flags]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return feishuRunFlags{}, errHelpRequested
		}
		return feishuRunFlags{}, err
	}

	rest := fs.Args()
	if storyToken == "" {
		if len(rest) == 0 {
			return feishuRunFlags{}, errors.New("feishu run: <story-id> is required")
		}
		storyToken = rest[0]
		rest = rest[1:]
	}
	if len(rest) > 0 {
		return feishuRunFlags{}, fmt.Errorf("feishu run: unexpected positional arguments: %v", rest)
	}

	storyID, err := strconv.ParseInt(storyToken, 10, 64)
	if err != nil {
		return feishuRunFlags{}, fmt.Errorf("feishu run: invalid story-id %q: must be an integer", storyToken)
	}
	flags.storyID = storyID

	if strings.TrimSpace(flags.configPath) == "" {
		return feishuRunFlags{}, errors.New("feishu run: --config (or -c) is required")
	}

	flags.agentName = strings.TrimSpace(flags.agentName)
	if flags.agentName == "" {
		flags.agentName = "codex"
	}
	if !isValidAgentName(flags.agentName) {
		return feishuRunFlags{}, fmt.Errorf("feishu run: invalid --agent %q; must be one of codex, claude, opencode", flags.agentName)
	}

	if strings.TrimSpace(flags.shared.stateDir) == "" {
		flags.shared.stateDir = defaultStateDir()
	}

	return flags, nil
}

func isValidAgentName(name string) bool {
	switch name {
	case "codex", "claude", "opencode":
		return true
	default:
		return false
	}
}

func runFeishuRun(ctx context.Context, flags feishuRunFlags, stdout io.Writer) error {
	cfg, err := feishuproject.LoadConfig(flags.configPath)
	if err != nil {
		return err
	}

	svc := feishuproject.NewService(cfg)
	result, err := svc.Pull(ctx)
	if err != nil {
		return err
	}
	reportWorkflowErrors(stdout, result.WorkflowErrors)

	compiled, repoPath, err := buildFeishuRunPlan(result.Stories, flags)
	if err != nil {
		return err
	}

	// The agent prompt declares story.RepoPath as the project root, so the
	// runtime wiring (runner Dir, repo lock, verify/commit_check workingDir)
	// must target that repo rather than the cogito process cwd. Thread it
	// through the shared --repo flag the run kernel already honors.
	sharedCopy := flags.shared
	sharedCopy.repo = repoPath

	output, err := appsvc.RunCompiledWorkflow(ctx, RunCompiledWorkflowInput{Compiled: compiled, Flags: &sharedCopy})
	if err != nil {
		return err
	}

	return presenter.PresentRunWorkflow(stdout, output)
}

// buildFeishuRunPlan resolves the story, validates its repo mapping, builds the
// ephemeral spec, and compiles it. It returns the resolved repo path alongside
// the compiled workflow so the caller can target the runtime wiring at the
// story repo. It is split out from runFeishuRun so the post-pull logic is
// unit-testable without hitting the Feishu API.
func buildFeishuRunPlan(stories []feishuproject.Story, flags feishuRunFlags) (*workflow.CompiledWorkflow, string, error) {
	story, ok := findStory(stories, flags.storyID)
	if !ok {
		return nil, "", fmt.Errorf("feishu run: story %d not found", flags.storyID)
	}

	rawRepoPath := strings.TrimSpace(story.RepoPath)
	if rawRepoPath == "" {
		return nil, "", fmt.Errorf("feishu run: story %d has no repo_path mapping; configure [repos] in TOML", flags.storyID)
	}

	repoPath, err := canonicalRepoPath(rawRepoPath)
	if err != nil {
		return nil, "", fmt.Errorf("feishu run: resolve story %d repo_path %q: %w", flags.storyID, rawRepoPath, err)
	}

	// A user-supplied --repo that disagrees with the story mapping is almost
	// certainly a mistake: the prompt and the runtime would target different
	// trees. Compare canonical absolute paths so "./repo" vs "/abs/repo" or a
	// trailing-slash variant of the same directory is not a false conflict.
	if userRepo := strings.TrimSpace(flags.shared.repo); userRepo != "" {
		match, matchErr := repoMatches(rawRepoPath, userRepo)
		if matchErr != nil {
			return nil, "", fmt.Errorf("feishu run: resolve --repo %q: %w", userRepo, matchErr)
		}

		if !match {
			return nil, "", fmt.Errorf("feishu run: --repo %q conflicts with story %d repo_path %q", userRepo, flags.storyID, rawRepoPath)
		}
	}

	spec, err := feishuproject.BuildEphemeralSpec(feishuproject.EphemeralOptions{
		Story:           story,
		AgentName:       flags.agentName,
		RepoPath:        repoPath,
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

// canonicalRepoPath normalizes a repo path to an absolute, slash-trimmed,
// Clean'd form so equivalent spellings ("./repo", "repo/", "/abs/repo")
// canonicalize identically. An empty input returns an empty string.
func canonicalRepoPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}

	// Drop trailing slashes so "repo/" and "repo" agree, but keep a bare root.
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == "" {
		trimmed = "/"
	}

	// filepath.Abs resolves against the process cwd and also Clean's the result.
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}

	return abs, nil
}

// repoMatches reports whether the story repo path and a user-supplied --repo
// point at the same directory once both are canonicalized.
func repoMatches(story, user string) (bool, error) {
	storyCanonical, err := canonicalRepoPath(story)
	if err != nil {
		return false, err
	}

	userCanonical, err := canonicalRepoPath(user)
	if err != nil {
		return false, err
	}

	return storyCanonical == userCanonical, nil
}

func findStory(stories []feishuproject.Story, id int64) (feishuproject.Story, bool) {
	for _, story := range stories {
		if story.ID == id {
			return story, true
		}
	}

	return feishuproject.Story{}, false
}

type feishuFlags struct {
	configPath string
}

func parseFeishuFlags(commandName string, args []string, stdout io.Writer) (feishuFlags, error) {
	fs := flag.NewFlagSet(commandName, flag.ContinueOnError)
	fs.SetOutput(stdout)

	flags := feishuFlags{}
	fs.StringVar(&flags.configPath, "c", "", "Path to Cogito TOML config containing [meegle] section")
	fs.StringVar(&flags.configPath, "config", "", "Path to Cogito TOML config containing [meegle] section")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stdout, "Usage: cogito %s -c <config.toml>\n", commandName)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return feishuFlags{}, errHelpRequested
		}
		return feishuFlags{}, err
	}

	if len(fs.Args()) > 0 {
		return feishuFlags{}, fmt.Errorf("%s: unexpected positional arguments: %v", commandName, fs.Args())
	}
	if strings.TrimSpace(flags.configPath) == "" {
		return feishuFlags{}, fmt.Errorf("%s: --config (or -c) is required", commandName)
	}

	return flags, nil
}

func runFeishuPull(ctx context.Context, flags feishuFlags, stdout io.Writer) error {
	cfg, err := feishuproject.LoadConfig(flags.configPath)
	if err != nil {
		return err
	}

	svc := feishuproject.NewService(cfg)
	result, err := svc.Pull(ctx)
	if err != nil {
		return err
	}
	if err := svc.Persist(result); err != nil {
		return err
	}

	feishuproject.WriteSummary(stdout, result)
	reportWorkflowErrors(stdout, result.WorkflowErrors)

	return nil
}

func runFeishuWatch(ctx context.Context, flags feishuFlags, stdout io.Writer) error {
	cfg, err := feishuproject.LoadConfig(flags.configPath)
	if err != nil {
		return err
	}

	svc := feishuproject.NewService(cfg)
	observer := feishuproject.FuncObserver{
		Tick: func(result feishuproject.PullResult) {
			feishuproject.WriteSummary(stdout, result)
			reportWorkflowErrors(stdout, result.WorkflowErrors)
		},
		Error: func(err error) {
			_, _ = fmt.Fprintf(stdout, "feishu watch: tick failed: %v\n", err)
		},
	}

	if err := svc.Watch(ctx, observer); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}

	return nil
}

// reportWorkflowErrors surfaces per-story workflow fetch warnings without
// failing the command. The story list and snapshot are already persisted at
// this point, so these are diagnostics only.
func reportWorkflowErrors(out io.Writer, errs []error) {
	if len(errs) == 0 {
		return
	}
	_, _ = fmt.Fprintf(out, "feishu pull: %d workflow fetch(es) failed:\n", len(errs))
	for _, err := range errs {
		_, _ = fmt.Fprintf(out, "  - %v\n", err)
	}
}
