package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/gitutil"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// commitCheckDriver validates the commits an upstream agent self-reported against
// real git history. It runs synchronously: Start reads the upstream
// AgentResult.Commits, validates them, optionally requires at least one, and
// (unless AllowDirty) asserts the worktree is clean, returning a settled
// Execution directly.
type commitCheckDriver struct {
	syncTerminalDriver
	engine *Engine
}

func (d commitCheckDriver) Start(_ context.Context, request stepStartRequest) (*adapters.Execution, error) {
	if request.Step.CommitCheck == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("commit_check config missing for step %q", request.Step.ID))
	}

	spec := request.Step.CommitCheck
	handle := syncStepHandle(d.engine, request.Step, request.AttemptID)

	result, err := readAgentResult(d.engine, spec.From)
	if err != nil {
		return terminalExecution(handle, adapters.ExecutionStateFailed, err.Error()), nil
	}

	workingDir := strings.TrimSpace(request.WorkingDir)
	if workingDir == "" {
		workingDir = "."
	}

	git := gitutil.GitOps{Root: workingDir}

	// Non-git mode (AgentLoop port): the main prompt instructs the agent to
	// leave commits empty outside a git repo, and there is no history or
	// worktree to validate, so the gate passes as an explicit no-op instead of
	// failing on git exit 128.
	if !git.IsRepo() {
		return terminalExecution(handle, adapters.ExecutionStateSucceeded, "commit_check skipped: not a git repository"), nil
	}

	failure, evalErr := evaluateCommitCheck(commitCheckParams{
		Spec:    *spec,
		Commits: result.Commits,
		Git:     git,
	})
	if evalErr != nil {
		return nil, evalErr
	}

	if failure != "" {
		return terminalExecution(handle, adapters.ExecutionStateFailed, failure), nil
	}

	return terminalExecution(handle, adapters.ExecutionStateSucceeded, fmt.Sprintf("commit_check passed: %d commit(s)", len(result.Commits))), nil
}

// commitCheckParams groups the inputs for evaluateCommitCheck so the validation
// logic stays a single pure function over the spec, the self-reported commits,
// and the git boundary.
type commitCheckParams struct {
	Spec    workflow.CommitCheckStepSpec
	Commits []string
	Git     gitutil.GitOps
}

// evaluateCommitCheck applies the commit_check policy and returns a non-empty
// failure summary when a gate is violated. The error return is reserved for
// unexpected git failures (e.g. a missing binary), not for ordinary policy
// failures, which are reported through the summary so the run folds normally.
func evaluateCommitCheck(params commitCheckParams) (string, error) {
	if params.Spec.RequireSome && len(params.Commits) == 0 {
		return "no commits self-reported", nil
	}

	if len(params.Commits) > 0 {
		validation, err := params.Git.ValidateCommitRefs(params.Commits)
		if err != nil {
			return "", wrapError(ErrorCodeGit, "validate commit refs", err)
		}

		if len(validation.Invalid) > 0 {
			return fmt.Sprintf("invalid commit refs: %s", strings.Join(validation.Invalid, ", ")), nil
		}
	}

	if !params.Spec.AllowDirty {
		dirty, err := params.Git.HasUncommittedChanges()
		if err != nil {
			return "", wrapError(ErrorCodeGit, "check worktree status", err)
		}

		if dirty {
			return "worktree still dirty", nil
		}
	}

	return "", nil
}
