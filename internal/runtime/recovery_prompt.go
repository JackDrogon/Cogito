package runtime

import (
	"encoding/json"
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
func (e *Engine) recoveryPromptOverride(step workflow.CompiledStep, prior StepSnapshot) string {
	if step.Kind != workflow.StepKindAgent || step.Agent == nil {
		return ""
	}

	workingDir := strings.TrimSpace(e.workingDir)
	if workingDir == "" {
		return ""
	}

	git := gitutil.GitOps{Root: workingDir}
	if !git.IsRepo() {
		return ""
	}

	input := prompt.PromptInput{
		Root:  workingDir,
		Tasks: []prompt.TaskRef{{ID: step.ID, Text: step.Agent.Prompt}},
	}

	// Missing self-reported commits take precedence over a dirty worktree:
	// recovering lost commits is higher priority than finishing WIP.
	if missing := missingReportedCommits(git, prior.StructuredOutput); len(missing) > 0 {
		return prompt.BuildCommitRecovery(input, missing)
	}

	if dirty, err := git.HasUncommittedChanges(); err == nil && dirty {
		return prompt.BuildDirtyWorktree(input)
	}

	return ""
}

// missingReportedCommits decodes the self-reported commits from a prior agent
// StructuredOutput and returns those that no longer resolve to a real commit in
// the work tree. An empty/undecodable output, no reported commits, or a git
// failure all yield no missing commits so the caller falls back to the main
// prompt.
func missingReportedCommits(git gitutil.GitOps, structuredOutput json.RawMessage) []string {
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

	validation, err := git.ValidateCommitRefs(result.Commits)
	if err != nil {
		return nil
	}

	return validation.Invalid
}
