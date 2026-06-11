package runtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters/prompt"
	"github.com/JackDrogon/Cogito/internal/gitutil"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// recoveryPromptOverride decides whether a resumed agent step should receive a
// recovery-flavored prompt instead of its original main prompt. It returns the
// override prompt, or "" to fall back to step.Agent.Prompt.
//
// Only agent steps running inside a git work tree are eligible. The selection
// mirrors AgentLoop's recovery priority, where missing commits rank above a
// dirty tree because lost commits are the higher-severity failure (work that
// was reported as committed but no longer resolves), while a dirty tree is
// active work in progress:
//   - clean/dirty tree, missing commit -> BuildCommitRecovery (the prior
//     attempt self-reported commits that no longer resolve against HEAD)
//   - dirty tree, no commit gap        -> BuildDirtyWorktree (finish + commit
//     the WIP)
//   - clean tree, no commit gap        -> "" (resume verbatim, main prompt)
//
// A non-git working dir, an empty working dir, or any git read failure also
// yields "" so the resume stays on the original main prompt.
func (e *Engine) recoveryPromptOverride(ctx context.Context, step workflow.CompiledStep, prior StepSnapshot) string {
	if step.Kind != workflow.StepKindAgent || step.Agent == nil {
		return ""
	}

	workingDir := strings.TrimSpace(e.workingDir)
	if workingDir == "" {
		return ""
	}

	git := gitutil.GitOps{Root: workingDir}
	if !git.IsRepo(ctx) {
		return ""
	}

	input := prompt.Input{
		Root:  workingDir,
		Tasks: []prompt.TaskRef{{ID: step.ID, Text: step.Agent.Prompt}},
	}

	// Missing self-reported commits take precedence over a dirty worktree:
	// recovering lost commits is higher priority than finishing WIP.
	if missing := missingReportedCommits(ctx, git, prior.StructuredOutput); len(missing) > 0 {
		return prompt.BuildCommitRecovery(input, missing)
	}

	dirty, err := git.HasUncommittedChanges(ctx)
	if err != nil {
		// A git failure degrades to the main prompt, but silently treating it
		// as a clean tree would mask real problems (e.g. missing binary), so
		// record the degradation.
		slog.Warn("recovery: git status failed; resuming with main prompt", "step", step.ID, "err", err)

		return ""
	}

	if dirty {
		return prompt.BuildDirtyWorktree(input)
	}

	return ""
}

// missingReportedCommits decodes the self-reported commits from a prior agent
// StructuredOutput and returns those that no longer resolve to a real commit in
// the work tree. An empty/undecodable output, no reported commits, or a git
// failure all yield no missing commits so the caller falls back to the main
// prompt.
func missingReportedCommits(ctx context.Context, git gitutil.GitOps, structuredOutput json.RawMessage) []string {
	if len(structuredOutput) == 0 {
		return nil
	}

	var result prompt.AgentResult
	if err := json.Unmarshal(structuredOutput, &result); err != nil {
		return nil
	}

	if len(result.Commits) == 0 {
		return nil
	}

	validation, err := git.ValidateCommitRefs(ctx, result.Commits)
	if err != nil {
		// Degrading to "no missing commits" keeps the resume on the main
		// prompt; log so a broken git setup is not silently mistaken for a
		// healthy history.
		slog.Warn("recovery: commit validation failed; skipping commit recovery", "err", err)

		return nil
	}

	return validation.Invalid
}
