package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/JackDrogon/Cogito/internal/task/feishuproject"
)

// feishuCommandRegistry is the subcommand table for `cogito feishu ...`.
// Kept private to this package so the feishu surface stays decoupled from
// workflow commands.
var feishuCommandRegistry = newCommandRegistry(
	feishuPullCommand{},
	feishuWatchCommand{},
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
