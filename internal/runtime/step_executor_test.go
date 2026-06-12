package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/JackDrogon/Cogito/internal/provider"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// recordingStepDriver records whether Start or Resume was invoked so the
// executeStep dispatch logic can be asserted without a real adapter.
type recordingStepDriver struct {
	runID        string
	startCalled  bool
	resumeCalled bool
	resumeHandle provider.ExecutionHandle
}

func (d *recordingStepDriver) Start(_ context.Context, request stepStartRequest) (*provider.Execution, error) {
	d.startCalled = true

	return &provider.Execution{
		Handle: provider.ExecutionHandle{
			RunID:             d.runID,
			StepID:            request.Step.ID,
			AttemptID:         request.AttemptID,
			ProviderSessionID: "sess-start",
		},
		State:   provider.ExecutionStateRunning,
		Summary: "started",
	}, nil
}

func (d *recordingStepDriver) Resume(_ context.Context, request stepResumeRequest) (*provider.Execution, error) {
	d.resumeCalled = true
	d.resumeHandle = request.Handle

	return &provider.Execution{
		Handle:  request.Handle,
		State:   provider.ExecutionStateRunning,
		Summary: "resumed",
	}, nil
}

func (d *recordingStepDriver) PollOrCollect(_ context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	return &provider.Execution{Handle: handle, State: provider.ExecutionStateSucceeded, Summary: "ok"}, nil
}

func (d *recordingStepDriver) Interrupt(_ context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	return &provider.Execution{Handle: handle, State: provider.ExecutionStateInterrupted, Summary: "interrupted"}, nil
}

func (d *recordingStepDriver) NormalizeResult(_ context.Context, execution *provider.Execution) (*provider.StepResult, error) {
	return &provider.StepResult{Handle: execution.Handle, Status: execution.State, Summary: execution.Summary}, nil
}

func newResumeDispatchEngine(t *testing.T, driver stepDriver, step StepSnapshot) *Engine {
	t.Helper()

	compiled := compileSpec(t, &workflow.Spec{
		Metadata: workflow.Metadata{Name: "resume-dispatch"},
		Steps: []workflow.StepSpec{{
			ID:    "review",
			Kind:  workflow.StepKindAgent,
			Agent: &workflow.AgentStepSpec{Agent: "fake", Prompt: "review"},
		}},
	})

	runStore, err := store.Open(filepath.Join(t.TempDir(), "runs"), "run-123")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	engine, err := NewEngine("run-123", compiled, MachineDependencies{
		Store: runStore,
		DriverFactory: StepDriverFactoryFunc(func(_ *Engine, _ workflow.CompiledStep) (stepDriver, error) {
			return driver, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	engine.snapshot.State = RunStateRunning
	engine.snapshot.Steps["review"] = step

	return engine
}

// TestExecuteStepCallsResumeWhenResumable verifies that a queued step carrying
// Resumable=true plus a provider session reattaches via driver.Resume instead of
// minting a fresh driver.Start attempt.
func TestExecuteStepCallsResumeWhenResumable(t *testing.T) {
	driver := &recordingStepDriver{runID: "run-123"}
	engine := newResumeDispatchEngine(t, driver, StepSnapshot{
		State:             StepStateQueued,
		AttemptID:         "attempt-prior",
		ProviderSessionID: "sess-prior",
		Resumable:         true,
	})

	if err := engine.executeStep(t.Context(), "review"); err != nil {
		t.Fatalf("executeStep() error = %v", err)
	}

	if !driver.resumeCalled {
		t.Fatal("driver.Resume was not called for a resumable step")
	}

	if driver.startCalled {
		t.Fatal("driver.Start must not be called for a resumable step")
	}

	if driver.resumeHandle.AttemptID != "attempt-prior" {
		t.Fatalf("resume handle AttemptID = %q, want %q", driver.resumeHandle.AttemptID, "attempt-prior")
	}

	if driver.resumeHandle.ProviderSessionID != "sess-prior" {
		t.Fatalf("resume handle ProviderSessionID = %q, want %q", driver.resumeHandle.ProviderSessionID, "sess-prior")
	}
}

// TestExecuteStepFallsThroughToStartWhenNotResumable regression-protects the
// default path: a queued step without resume state performs a fresh Start.
func TestExecuteStepFallsThroughToStartWhenNotResumable(t *testing.T) {
	driver := &recordingStepDriver{runID: "run-123"}
	engine := newResumeDispatchEngine(t, driver, StepSnapshot{State: StepStateQueued})

	if err := engine.executeStep(t.Context(), "review"); err != nil {
		t.Fatalf("executeStep() error = %v", err)
	}

	if !driver.startCalled {
		t.Fatal("driver.Start was not called for a non-resumable step")
	}

	if driver.resumeCalled {
		t.Fatal("driver.Resume must not be called for a non-resumable step")
	}
}
