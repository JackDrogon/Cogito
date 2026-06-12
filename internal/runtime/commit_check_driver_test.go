package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/JackDrogon/Cogito/internal/gitutil"
	"github.com/JackDrogon/Cogito/internal/provider"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// initGitRepo creates a git repo under t.TempDir() with one commit and returns
// its root and HEAD SHA.
func initGitRepo(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	gitRun(t, root, "init")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	gitRun(t, root, "add", "README.md")
	gitRun(t, root, "commit", "-m", "initial commit")

	head, err := (gitutil.GitOps{Root: root}).HeadCommit(t.Context())
	if err != nil {
		t.Fatalf("HeadCommit() error = %v", err)
	}

	return root, head
}

func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(root))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v error = %v: %s", args, err, out)
	}
}

func TestEvaluateCommitCheckSucceedsWithValidCommitsCleanTree(t *testing.T) {
	root, head := initGitRepo(t)

	failure, err := evaluateCommitCheck(t.Context(), commitCheckParams{
		Spec:    workflow.CommitCheckStepSpec{From: "agent", RequireSome: true},
		Commits: []string{head},
		Git:     gitutil.GitOps{Root: root},
	})
	if err != nil {
		t.Fatalf("evaluateCommitCheck() error = %v", err)
	}
	if failure != "" {
		t.Fatalf("evaluateCommitCheck() = %q, want pass", failure)
	}
}

func TestEvaluateCommitCheckRequireSomeEmpty(t *testing.T) {
	root, _ := initGitRepo(t)

	failure, err := evaluateCommitCheck(t.Context(), commitCheckParams{
		Spec:    workflow.CommitCheckStepSpec{From: "agent", RequireSome: true},
		Commits: nil,
		Git:     gitutil.GitOps{Root: root},
	})
	if err != nil {
		t.Fatalf("evaluateCommitCheck() error = %v", err)
	}
	if failure != "no commits self-reported" {
		t.Fatalf("evaluateCommitCheck() = %q, want no-commits failure", failure)
	}
}

func TestEvaluateCommitCheckInvalidRef(t *testing.T) {
	root, _ := initGitRepo(t)

	failure, err := evaluateCommitCheck(t.Context(), commitCheckParams{
		Spec:    workflow.CommitCheckStepSpec{From: "agent"},
		Commits: []string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		Git:     gitutil.GitOps{Root: root},
	})
	if err != nil {
		t.Fatalf("evaluateCommitCheck() error = %v", err)
	}
	if failure == "" || failure[:len("invalid commit refs")] != "invalid commit refs" {
		t.Fatalf("evaluateCommitCheck() = %q, want invalid-refs failure", failure)
	}
}

func TestEvaluateCommitCheckDirtyWorktree(t *testing.T) {
	root, head := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	failure, err := evaluateCommitCheck(t.Context(), commitCheckParams{
		Spec:    workflow.CommitCheckStepSpec{From: "agent"},
		Commits: []string{head},
		Git:     gitutil.GitOps{Root: root},
	})
	if err != nil {
		t.Fatalf("evaluateCommitCheck() error = %v", err)
	}
	if failure != "worktree still dirty" {
		t.Fatalf("evaluateCommitCheck() = %q, want dirty failure", failure)
	}
}

func TestEvaluateCommitCheckAllowDirtySkipsWorktreeCheck(t *testing.T) {
	root, head := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	failure, err := evaluateCommitCheck(t.Context(), commitCheckParams{
		Spec:    workflow.CommitCheckStepSpec{From: "agent", AllowDirty: true},
		Commits: []string{head},
		Git:     gitutil.GitOps{Root: root},
	})
	if err != nil {
		t.Fatalf("evaluateCommitCheck() error = %v", err)
	}
	if failure != "" {
		t.Fatalf("evaluateCommitCheck() = %q, want pass with AllowDirty", failure)
	}
}

// agentVerifyCommitSpec builds the canonical agent → verify → commit_check chain.
func agentVerifyCommitSpec() *workflow.Spec {
	return &workflow.Spec{
		Metadata: workflow.Metadata{Name: "agent-verify-commit"},
		Steps: []workflow.StepSpec{
			{ID: "agent", Kind: workflow.StepKindAgent, Agent: &workflow.AgentStepSpec{Agent: "fake", Prompt: "do"}},
			{ID: "verify", Kind: workflow.StepKindVerify, Needs: []string{"agent"}, Verify: &workflow.VerifyStepSpec{From: "agent"}},
			{ID: "commit_check", Kind: workflow.StepKindCommitCheck, Needs: []string{"verify"}, CommitCheck: &workflow.CommitCheckStepSpec{From: "agent", RequireSome: true}},
		},
	}
}

func agentChainAdapter(structuredOutput string) provider.Provider {
	return provider.NewFakeProvider(provider.FakeConfig{
		Capabilities: provider.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true},
		Scripts: map[string]provider.FakeScript{
			"attempt-agent-01": {
				Start: provider.FakeSnapshot{State: provider.ExecutionStateRunning, Summary: "agent started"},
				Polls: []provider.FakeSnapshot{{
					State:            provider.ExecutionStateSucceeded,
					Summary:          "agent ok",
					StructuredOutput: json.RawMessage(structuredOutput),
				}},
			},
		},
	})
}

func TestAgentVerifyCommitCheckChainSucceeds(t *testing.T) {
	root, head := initGitRepo(t)
	structured := fmt.Sprintf(`{"commits":[%q],"verification":["true"],"summary":"done"}`, head)

	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test:       t,
		Spec:       agentVerifyCommitSpec(),
		WorkingDir: root,
		Provider:   agentChainAdapter(structured),
	})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	snapshot := fixture.engine.Snapshot()
	for _, stepID := range []string{"agent", "verify", "commit_check"} {
		if state := snapshot.Steps[stepID].State; state != StepStateSucceeded {
			t.Fatalf("step %q state = %q, want %q", stepID, state, StepStateSucceeded)
		}
	}
	if snapshot.State != RunStateSucceeded {
		t.Fatalf("run state = %q, want %q", snapshot.State, RunStateSucceeded)
	}
}

func TestAgentVerifyCommitCheckChainFailsOnInvalidCommit(t *testing.T) {
	root, _ := initGitRepo(t)
	structured := `{"commits":["deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"],"verification":["true"],"summary":"done"}`

	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test:       t,
		Spec:       agentVerifyCommitSpec(),
		WorkingDir: root,
		Provider:   agentChainAdapter(structured),
	})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	snapshot := fixture.engine.Snapshot()
	if state := snapshot.Steps["commit_check"].State; state != StepStateFailed {
		t.Fatalf("commit_check state = %q, want %q", state, StepStateFailed)
	}
	if snapshot.State != RunStateFailed {
		t.Fatalf("run state = %q, want %q", snapshot.State, RunStateFailed)
	}
}

// TestAgentVerifyCommitCheckChainSucceedsInNonGitDirectory covers the
// AgentLoop-ported non-git mode end to end: in a plain directory the agent
// reports empty commits (per the main prompt contract) and commit_check must
// pass as an explicit no-op instead of failing on git exit 128 — even with
// RequireSome set, since there is no git history to require commits in.
func TestAgentVerifyCommitCheckChainSucceedsInNonGitDirectory(t *testing.T) {
	root := t.TempDir()
	structured := `{"commits":[],"verification":["true"],"summary":"done without git"}`

	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test:       t,
		Spec:       agentVerifyCommitSpec(),
		WorkingDir: root,
		Provider:   agentChainAdapter(structured),
	})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	snapshot := fixture.engine.Snapshot()
	for _, stepID := range []string{"agent", "verify", "commit_check"} {
		if state := snapshot.Steps[stepID].State; state != StepStateSucceeded {
			t.Fatalf("step %q state = %q, want %q", stepID, state, StepStateSucceeded)
		}
	}
	if snapshot.State != RunStateSucceeded {
		t.Fatalf("run state = %q, want %q", snapshot.State, RunStateSucceeded)
	}
}

func TestStepDriverRegistryResolvesAllStepKinds(t *testing.T) {
	root, head := initGitRepo(t)
	structured := fmt.Sprintf(`{"commits":[%q],"verification":["true"],"summary":"done"}`, head)

	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test:       t,
		Spec:       agentVerifyCommitSpec(),
		WorkingDir: root,
		Provider:   agentChainAdapter(structured),
	})

	registry := NewStepDriverRegistry()
	for _, step := range fixture.compiled.Steps {
		driver, err := registry.Build(fixture.engine, step)
		if err != nil {
			t.Fatalf("registry.Build(%q) error = %v", step.ID, err)
		}
		if driver == nil {
			t.Fatalf("registry.Build(%q) = nil driver", step.ID)
		}
	}
}
