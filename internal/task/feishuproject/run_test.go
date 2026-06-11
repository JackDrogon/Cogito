package feishuproject

import (
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/workflow"
)

func TestBuildEphemeralSpecDefaultBuildsThreeSteps(t *testing.T) {
	spec, err := BuildEphemeralSpec(EphemeralOptions{
		Story:           Story{ID: 7004653782, Description: "implement hello test"},
		RepoPath:        "/data/Work/repo",
		WithVerify:      true,
		WithCommitCheck: true,
	})
	if err != nil {
		t.Fatalf("BuildEphemeralSpec() error = %v", err)
	}

	if len(spec.Steps) != 3 {
		t.Fatalf("len(spec.Steps) = %d, want 3", len(spec.Steps))
	}

	wantKinds := []workflow.StepKind{workflow.StepKindAgent, workflow.StepKindVerify, workflow.StepKindCommitCheck}
	for index, want := range wantKinds {
		if spec.Steps[index].Kind != want {
			t.Fatalf("spec.Steps[%d].Kind = %q, want %q", index, spec.Steps[index].Kind, want)
		}
	}

	agent := spec.Steps[0]
	if agent.Agent == nil || agent.Agent.Agent != "codex" {
		t.Fatalf("agent step agent = %#v, want codex", agent.Agent)
	}

	verify := spec.Steps[1]
	if verify.Verify == nil || verify.Verify.From != agentStepID {
		t.Fatalf("verify step = %#v, want From=%q", verify.Verify, agentStepID)
	}
	if len(verify.Needs) != 1 || verify.Needs[0] != agentStepID {
		t.Fatalf("verify step needs = %v, want [%q]", verify.Needs, agentStepID)
	}

	commit := spec.Steps[2]
	if commit.CommitCheck == nil || commit.CommitCheck.From != agentStepID {
		t.Fatalf("commit_check step = %#v, want From=%q", commit.CommitCheck, agentStepID)
	}
	if len(commit.Needs) != 1 || commit.Needs[0] != verifyStepID {
		t.Fatalf("commit_check step needs = %v, want [%q]", commit.Needs, verifyStepID)
	}

	if spec.Metadata.Name != "feishu-story-7004653782" {
		t.Fatalf("spec.Metadata.Name = %q, want feishu-story-7004653782", spec.Metadata.Name)
	}
}

func TestBuildEphemeralSpecCommitCheckGatesOnAgentWhenVerifySkipped(t *testing.T) {
	spec, err := BuildEphemeralSpec(EphemeralOptions{
		Story:           Story{ID: 42, Description: "task"},
		RepoPath:        "/repo",
		WithVerify:      false,
		WithCommitCheck: true,
	})
	if err != nil {
		t.Fatalf("BuildEphemeralSpec() error = %v", err)
	}

	if len(spec.Steps) != 2 {
		t.Fatalf("len(spec.Steps) = %d, want 2", len(spec.Steps))
	}

	commit := spec.Steps[1]
	if commit.Kind != workflow.StepKindCommitCheck {
		t.Fatalf("spec.Steps[1].Kind = %q, want commit_check", commit.Kind)
	}
	if len(commit.Needs) != 1 || commit.Needs[0] != agentStepID {
		t.Fatalf("commit_check step needs = %v, want [%q]", commit.Needs, agentStepID)
	}
}

func TestBuildEphemeralSpecNoVerifyNoCommitBuildsSingleStep(t *testing.T) {
	spec, err := BuildEphemeralSpec(EphemeralOptions{
		Story:    Story{ID: 1, Description: "solo"},
		RepoPath: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildEphemeralSpec() error = %v", err)
	}

	if len(spec.Steps) != 1 {
		t.Fatalf("len(spec.Steps) = %d, want 1", len(spec.Steps))
	}
	if spec.Steps[0].Kind != workflow.StepKindAgent {
		t.Fatalf("spec.Steps[0].Kind = %q, want agent", spec.Steps[0].Kind)
	}
}

func TestBuildEphemeralSpecDefaultsAgentToCodex(t *testing.T) {
	spec, err := BuildEphemeralSpec(EphemeralOptions{
		Story:    Story{ID: 1, Description: "x"},
		RepoPath: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildEphemeralSpec() error = %v", err)
	}
	if spec.Steps[0].Agent.Agent != "codex" {
		t.Fatalf("agent = %q, want codex", spec.Steps[0].Agent.Agent)
	}
}

func TestBuildEphemeralSpecEmptyRepoPathErrors(t *testing.T) {
	_, err := BuildEphemeralSpec(EphemeralOptions{
		Story:    Story{ID: 1, Description: "x"},
		RepoPath: "   ",
	})
	if err == nil {
		t.Fatal("BuildEphemeralSpec() error = nil, want repo path required")
	}
	if !strings.Contains(err.Error(), "repo path is required") {
		t.Fatalf("BuildEphemeralSpec() error = %v, want repo path required", err)
	}
}

func TestBuildEphemeralSpecInvalidAgentErrors(t *testing.T) {
	_, err := BuildEphemeralSpec(EphemeralOptions{
		Story:     Story{ID: 1, Description: "x"},
		AgentName: "gemini",
		RepoPath:  "/repo",
	})
	if err == nil {
		t.Fatal("BuildEphemeralSpec() error = nil, want invalid agent")
	}
	if !strings.Contains(err.Error(), "invalid agent") {
		t.Fatalf("BuildEphemeralSpec() error = %v, want invalid agent", err)
	}
}

func TestBuildEphemeralSpecCompiles(t *testing.T) {
	spec, err := BuildEphemeralSpec(EphemeralOptions{
		Story:           Story{ID: 7, Description: "compile me"},
		AgentName:       "claude",
		RepoPath:        "/repo",
		WithVerify:      true,
		WithCommitCheck: true,
	})
	if err != nil {
		t.Fatalf("BuildEphemeralSpec() error = %v", err)
	}

	compiled, err := workflow.CompileWorkflow(spec)
	if err != nil {
		t.Fatalf("CompileWorkflow() error = %v", err)
	}
	if len(compiled.TopologicalOrder) != 3 {
		t.Fatalf("len(compiled.TopologicalOrder) = %d, want 3", len(compiled.TopologicalOrder))
	}
	if compiled.TopologicalOrder[0] != agentStepID {
		t.Fatalf("compiled.TopologicalOrder[0] = %q, want %q", compiled.TopologicalOrder[0], agentStepID)
	}
}

func TestBuildEphemeralSpecPromptContainsDescription(t *testing.T) {
	description := "implement the login endpoint with TDD"
	spec, err := BuildEphemeralSpec(EphemeralOptions{
		Story:    Story{ID: 99, Description: description},
		RepoPath: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildEphemeralSpec() error = %v", err)
	}

	gotPrompt := spec.Steps[0].Agent.Prompt
	if !strings.Contains(gotPrompt, description) {
		t.Fatalf("agent prompt does not contain story description %q", description)
	}
}
