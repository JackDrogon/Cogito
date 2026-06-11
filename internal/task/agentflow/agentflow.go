// Package agentflow builds ephemeral in-memory agent workflows shared by CLI
// task sources. Both `cogito feishu run` (story-driven) and `cogito agents run`
// (ad hoc prompt) delegate here so the agent -> verify -> commit_check shape,
// agent-name validation, and workflow-name sanitization stay in one place.
package agentflow

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters/prompt"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// DefaultAgentName is used when SpecOptions.AgentName is empty. It mirrors the
// `--agent` / `-p` flag defaults on the CLI commands that build ephemeral runs.
const DefaultAgentName = "codex"

// AgentStepID/VerifyStepID/CommitCheckStepID are the fixed step identifiers of
// every ephemeral workflow. They are stable so the verify/commit_check steps
// can reference the agent step via their From field.
const (
	AgentStepID       = "agent"
	VerifyStepID      = "verify"
	CommitCheckStepID = "commit_check"
)

// validAgentNames is the closed set of code agents an ephemeral workflow may
// delegate to. It matches the adapters registered in internal/adapters.
var validAgentNames = map[string]struct{}{
	"codex":    {},
	"claude":   {},
	"opencode": {},
}

// invalidNameChars matches every character that is not allowed in a workflow
// metadata name. Matches are replaced with '-' to keep the identifier valid.
var invalidNameChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// IsValidAgentName reports whether name is a registered code agent.
func IsValidAgentName(name string) bool {
	_, ok := validAgentNames[name]
	return ok
}

// SpecOptions configures one ephemeral workflow build. WithVerify and
// WithCommitCheck are semantically defaulted to true by callers (CLI commands
// derive them from --no-verify / --no-commit-check); BuildSpec honors whatever
// values it is given.
type SpecOptions struct {
	Name            string           // workflow metadata name; sanitized to a valid identifier
	AgentName       string           // codex | claude | opencode; empty defaults to codex
	RepoPath        string           // required; the repository the agent operates on
	Tasks           []prompt.TaskRef // required; rendered into the main prompt contract
	WithVerify      bool             // append a verify step gated on the agent step
	WithCommitCheck bool             // append a commit_check step gated on the agent/verify step
}

// BuildSpec turns the options into a runnable workflow.Spec that delegates the
// tasks to a code agent, optionally followed by a verify step and a
// commit_check step. The returned spec is guaranteed to pass
// workflow.CompileWorkflow.
//
// The agent prompt is rendered with prompt.BuildMain so it carries the same
// shared execution contract used by every Cogito agent step.
func BuildSpec(opts SpecOptions) (*workflow.Spec, error) {
	agentName := strings.TrimSpace(opts.AgentName)
	if agentName == "" {
		agentName = DefaultAgentName
	}

	if !IsValidAgentName(agentName) {
		return nil, fmt.Errorf(
			"agentflow.BuildSpec: invalid agent %q; must be one of codex, claude, opencode", agentName)
	}

	repoPath := strings.TrimSpace(opts.RepoPath)
	if repoPath == "" {
		return nil, errors.New("agentflow.BuildSpec: repo path is required")
	}

	if len(opts.Tasks) == 0 {
		return nil, errors.New("agentflow.BuildSpec: at least one task is required")
	}

	mainPrompt := prompt.BuildMain(prompt.Input{
		Root:  repoPath,
		Tasks: opts.Tasks,
	})

	steps := []workflow.StepSpec{{
		ID:    AgentStepID,
		Kind:  workflow.StepKindAgent,
		Agent: &workflow.AgentStepSpec{Agent: agentName, Prompt: mainPrompt},
	}}

	lastStepID := AgentStepID

	if opts.WithVerify {
		steps = append(steps, workflow.StepSpec{
			ID:     VerifyStepID,
			Kind:   workflow.StepKindVerify,
			Needs:  []string{AgentStepID},
			Verify: &workflow.VerifyStepSpec{From: AgentStepID},
		})
		lastStepID = VerifyStepID
	}

	if opts.WithCommitCheck {
		steps = append(steps, workflow.StepSpec{
			ID:          CommitCheckStepID,
			Kind:        workflow.StepKindCommitCheck,
			Needs:       []string{lastStepID},
			CommitCheck: &workflow.CommitCheckStepSpec{From: AgentStepID},
		})
	}

	return &workflow.Spec{
		APIVersion: "cogito/v1alpha1",
		Kind:       "Workflow",
		Metadata:   workflow.Metadata{Name: SanitizeWorkflowName(opts.Name)},
		Steps:      steps,
	}, nil
}

// SanitizeWorkflowName replaces every character outside [A-Za-z0-9_-] with '-'
// so the generated metadata name is always a valid workflow identifier.
func SanitizeWorkflowName(name string) string {
	return invalidNameChars.ReplaceAllString(name, "-")
}
