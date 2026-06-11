package feishuproject

import (
	"fmt"

	"github.com/JackDrogon/Cogito/internal/adapters/prompt"
	"github.com/JackDrogon/Cogito/internal/task/agentflow"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// agentStepID/verifyStepID/commitCheckStepID alias the shared ephemeral step
// identifiers so story-specific code keeps referring to them locally.
const (
	agentStepID       = agentflow.AgentStepID
	verifyStepID      = agentflow.VerifyStepID
	commitCheckStepID = agentflow.CommitCheckStepID
)

// EphemeralOptions configures the in-memory workflow built from a single Feishu
// Project story. WithVerify and WithCommitCheck are semantically defaulted to
// true by the caller (the `cogito feishu run` command derives them from
// --no-verify / --no-commit-check); BuildEphemeralSpec honors whatever values
// it is given.
type EphemeralOptions struct {
	Story           Story
	AgentName       string // codex | claude | opencode; empty defaults to codex
	RepoPath        string // required; the resolved story.RepoPath
	WithVerify      bool   // append a verify step gated on the agent step
	WithCommitCheck bool   // append a commit_check step gated on the agent/verify step
}

// BuildEphemeralSpec turns a single story into a runnable workflow.Spec that
// delegates the story description to a code agent, optionally followed by a
// verify step and a commit_check step. The returned spec is guaranteed to pass
// workflow.CompileWorkflow.
//
// The story description becomes the single inline task body; the shared
// agentflow builder renders it through prompt.BuildMain so it carries the same
// shared execution contract used by every Cogito agent step.
func BuildEphemeralSpec(opts EphemeralOptions) (*workflow.Spec, error) {
	return agentflow.BuildSpec(agentflow.SpecOptions{
		Name:      fmt.Sprintf("feishu-story-%d", opts.Story.ID),
		AgentName: opts.AgentName,
		RepoPath:  opts.RepoPath,
		Tasks: []prompt.TaskRef{{
			ID:   fmt.Sprintf("%d", opts.Story.ID),
			Text: opts.Story.Description,
		}},
		WithVerify:      opts.WithVerify,
		WithCommitCheck: opts.WithCommitCheck,
	})
}
