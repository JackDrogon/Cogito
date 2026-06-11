package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/adapters/prompt"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// dirtyWorktreeMarker and commitRecoveryMarker are template-unique phrases used
// to identify which recovery prompt recoveryPromptOverride selected.
const (
	dirtyWorktreeMarker  = "仓库里留下了未提交改动"
	commitRecoveryMarker = "没有创建 git commit"
)

// newRecoveryEngine builds a minimal single-agent-step engine with the given
// working dir so recoveryPromptOverride can be exercised against a real (or
// absent) git work tree.
func newRecoveryEngine(t *testing.T, workingDir string) *Engine {
	t.Helper()

	compiled := compileSpec(t, &workflow.Spec{
		Metadata: workflow.Metadata{Name: "recovery"},
		Steps: []workflow.StepSpec{{
			ID:    "agent",
			Kind:  workflow.StepKindAgent,
			Agent: &workflow.AgentStepSpec{Agent: "fake", Prompt: "do the task"},
		}},
	})

	runStore, err := store.Open(filepath.Join(t.TempDir(), "runs"), "run-rec")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	engine, err := NewEngine("run-rec", compiled, MachineDependencies{
		Store:      runStore,
		WorkingDir: workingDir,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	return engine
}

func recoveryStep(t *testing.T, engine *Engine) workflow.CompiledStep {
	t.Helper()

	step, err := engine.lookupStep("agent")
	if err != nil {
		t.Fatalf("lookupStep() error = %v", err)
	}

	return step
}

// TestRecoveryPromptOverrideNonRepo (Case A): a non-git working dir resumes
// verbatim with no override.
func TestRecoveryPromptOverrideNonRepo(t *testing.T) {
	engine := newRecoveryEngine(t, t.TempDir())

	got := engine.recoveryPromptOverride(t.Context(), recoveryStep(t, engine), StepSnapshot{})
	if got != "" {
		t.Fatalf("recoveryPromptOverride() = %q, want empty for non-repo", got)
	}
}

// TestRecoveryPromptOverrideCleanRepo (Case B): a clean git repo with no commit
// gap resumes verbatim with no override.
func TestRecoveryPromptOverrideCleanRepo(t *testing.T) {
	root, _ := initGitRepo(t)
	engine := newRecoveryEngine(t, root)

	got := engine.recoveryPromptOverride(t.Context(), recoveryStep(t, engine), StepSnapshot{})
	if got != "" {
		t.Fatalf("recoveryPromptOverride() = %q, want empty for clean repo", got)
	}
}

// TestRecoveryPromptOverrideDirtyRepo (Case C): an uncommitted change selects
// the dirty-worktree recovery prompt.
func TestRecoveryPromptOverrideDirtyRepo(t *testing.T) {
	root, _ := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(root, "wip.txt"), []byte("scratch\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	engine := newRecoveryEngine(t, root)

	got := engine.recoveryPromptOverride(t.Context(), recoveryStep(t, engine), StepSnapshot{})
	if !strings.Contains(got, dirtyWorktreeMarker) {
		t.Fatalf("recoveryPromptOverride() = %q, want dirty-worktree recovery prompt", got)
	}
}

// TestRecoveryPromptOverrideMissingCommits (Case D): a clean repo where the
// prior attempt self-reported commits that no longer resolve selects the
// commit-recovery prompt, listing the missing refs.
func TestRecoveryPromptOverrideMissingCommits(t *testing.T) {
	root, _ := initGitRepo(t)
	engine := newRecoveryEngine(t, root)

	missing := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	structured, err := json.Marshal(prompt.AgentResult{Commits: []string{missing}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	got := engine.recoveryPromptOverride(t.Context(), recoveryStep(t, engine), StepSnapshot{StructuredOutput: structured})
	if !strings.Contains(got, commitRecoveryMarker) {
		t.Fatalf("recoveryPromptOverride() = %q, want commit-recovery prompt", got)
	}
	if !strings.Contains(got, missing) {
		t.Fatalf("recoveryPromptOverride() = %q, want the missing commit ref %q", got, missing)
	}
}

// TestRecoveryPromptOverrideValidCommitsClean confirms self-reported commits
// that DO resolve leave the resume on the main prompt (no false positive).
func TestRecoveryPromptOverrideValidCommitsClean(t *testing.T) {
	root, head := initGitRepo(t)
	engine := newRecoveryEngine(t, root)

	structured, err := json.Marshal(prompt.AgentResult{Commits: []string{head}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	got := engine.recoveryPromptOverride(t.Context(), recoveryStep(t, engine), StepSnapshot{StructuredOutput: structured})
	if got != "" {
		t.Fatalf("recoveryPromptOverride() = %q, want empty when reported commits resolve", got)
	}
}

// recordingResumeAdapter captures the prompt passed to Resume so the
// agentDriver override plumbing can be asserted end to end.
type recordingResumeAdapter struct {
	resumePrompt string
}

func (a *recordingResumeAdapter) DescribeCapabilities() adapters.CapabilityMatrix {
	return adapters.CapabilityMatrix{Resume: true, Interrupt: true}
}

func (a *recordingResumeAdapter) Start(_ context.Context, _ adapters.StartRequest) (*adapters.Execution, error) {
	return nil, newError(ErrorCodeExecution, "start not used")
}

func (a *recordingResumeAdapter) PollOrCollect(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateSucceeded}, nil
}

func (a *recordingResumeAdapter) Interrupt(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateInterrupted}, nil
}

func (a *recordingResumeAdapter) Resume(_ context.Context, request adapters.ResumeRequest) (*adapters.Execution, error) {
	a.resumePrompt = request.Prompt

	return &adapters.Execution{Handle: request.Handle, State: adapters.ExecutionStateRunning, Summary: "resumed"}, nil
}

func (a *recordingResumeAdapter) NormalizeResult(_ context.Context, request adapters.NormalizeRequest) (*adapters.StepResult, error) {
	return &adapters.StepResult{Handle: request.Execution.Handle, Status: request.Execution.State}, nil
}

// TestAgentDriverResumeUsesRecoveryPrompt confirms the recovery override flows
// stepResumeRequest -> agentDriver.Resume -> adapter.Resume.
func TestAgentDriverResumeUsesRecoveryPrompt(t *testing.T) {
	adapter := &recordingResumeAdapter{}
	driver := agentDriver{adapter: adapter}

	step := workflow.CompiledStep{StepSpec: workflow.StepSpec{
		ID:    "agent",
		Kind:  workflow.StepKindAgent,
		Agent: &workflow.AgentStepSpec{Agent: "fake", Prompt: "main prompt"},
	}}

	if _, err := driver.Resume(t.Context(), stepResumeRequest{
		Step:           step,
		Handle:         adapters.ExecutionHandle{ProviderSessionID: "sess"},
		RecoveryPrompt: "recovery prompt",
	}); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	if adapter.resumePrompt != "recovery prompt" {
		t.Fatalf("adapter.resumePrompt = %q, want the recovery override", adapter.resumePrompt)
	}
}

// TestAgentDriverResumeFallsBackToMainPrompt confirms an empty override resumes
// the original main prompt verbatim.
func TestAgentDriverResumeFallsBackToMainPrompt(t *testing.T) {
	adapter := &recordingResumeAdapter{}
	driver := agentDriver{adapter: adapter}

	step := workflow.CompiledStep{StepSpec: workflow.StepSpec{
		ID:    "agent",
		Kind:  workflow.StepKindAgent,
		Agent: &workflow.AgentStepSpec{Agent: "fake", Prompt: "main prompt"},
	}}

	if _, err := driver.Resume(t.Context(), stepResumeRequest{
		Step:   step,
		Handle: adapters.ExecutionHandle{ProviderSessionID: "sess"},
	}); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	if adapter.resumePrompt != "main prompt" {
		t.Fatalf("adapter.resumePrompt = %q, want the main prompt", adapter.resumePrompt)
	}
}
