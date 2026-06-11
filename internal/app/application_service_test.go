package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

func TestRunCompiledWorkflowRequiresCompiled(t *testing.T) {
	_, err := appService.RunCompiledWorkflow(t.Context(), RunCompiledWorkflowInput{Flags: &sharedFlags{}})
	if err == nil {
		t.Fatal("RunCompiledWorkflow() error = nil, want compiled workflow required")
	}
	if !strings.Contains(err.Error(), "compiled workflow is required") {
		t.Fatalf("RunCompiledWorkflow() error = %v, want compiled required", err)
	}
}

func TestRunCompiledWorkflowRunsCompiledWorkflow(t *testing.T) {
	fixture := newAppRepoFixture(t)
	workflowPath := writeWorkflowFile(workflowFileParams{Test: t, RepoDir: fixture.repoDir, Name: "compiled.yaml", Command: "printf 'ok\\n'"})

	compiled, err := workflow.LoadFile(workflowPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}

	stateDir := filepath.Join(fixture.runsRoot, "run-compiled")
	output, err := appService.RunCompiledWorkflow(t.Context(), RunCompiledWorkflowInput{
		Compiled: compiled,
		Flags:    &sharedFlags{repo: fixture.repoDir, stateDir: stateDir},
	})
	if err != nil {
		t.Fatalf("RunCompiledWorkflow() error = %v", err)
	}
	if output.State != runtime.RunStateSucceeded {
		t.Fatalf("output.State = %q, want %q", output.State, runtime.RunStateSucceeded)
	}
	if output.StateDir != stateDir {
		t.Fatalf("output.StateDir = %q, want %q", output.StateDir, stateDir)
	}

	// The kernel must persist the resolved workflow so `cogito resume` can
	// reload the compiled graph from disk, exactly like a YAML-sourced run.
	resolved, err := workflow.LoadResolvedFile(filepath.Join(stateDir, "workflow.json"))
	if err != nil {
		t.Fatalf("LoadResolvedFile() error = %v", err)
	}
	if len(resolved.Steps) != len(compiled.Steps) {
		t.Fatalf("resolved steps = %d, want %d", len(resolved.Steps), len(compiled.Steps))
	}
}

// TestRunCompiledWorkflowParityWithRunWorkflow asserts the two public entry
// points produce the same terminal state for the same workflow, confirming the
// RunWorkflow thin-wrapper refactor preserved behavior.
func TestRunCompiledWorkflowParityWithRunWorkflow(t *testing.T) {
	fixture := newAppRepoFixture(t)
	workflowPath := writeWorkflowFile(workflowFileParams{Test: t, RepoDir: fixture.repoDir, Name: "parity.yaml", Command: "printf 'parity\\n'"})

	loadedOutput, err := appService.RunWorkflow(t.Context(), RunWorkflowInput{
		WorkflowPath: workflowPath,
		Flags:        &sharedFlags{repo: fixture.repoDir, stateDir: filepath.Join(fixture.runsRoot, "run-loaded")},
	})
	if err != nil {
		t.Fatalf("RunWorkflow() error = %v", err)
	}

	compiled, err := workflow.LoadFile(workflowPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	compiledOutput, err := appService.RunCompiledWorkflow(t.Context(), RunCompiledWorkflowInput{
		Compiled: compiled,
		Flags:    &sharedFlags{repo: fixture.repoDir, stateDir: filepath.Join(fixture.runsRoot, "run-compiled-parity")},
	})
	if err != nil {
		t.Fatalf("RunCompiledWorkflow() error = %v", err)
	}

	if loadedOutput.State != compiledOutput.State {
		t.Fatalf("state mismatch: RunWorkflow=%q RunCompiledWorkflow=%q", loadedOutput.State, compiledOutput.State)
	}
}
