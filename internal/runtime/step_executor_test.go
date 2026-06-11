package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// recordingStepDriver records whether Start or Resume was invoked so the
// executeStep dispatch logic can be asserted without a real adapter.
type recordingStepDriver struct {
	runID        string
	startCalled  bool
	resumeCalled bool
	resumeHandle adapters.ExecutionHandle
}

func (d *recordingStepDriver) Start(_ context.Context, request stepStartRequest) (*adapters.Execution, error) {
	d.startCalled = true

	return &adapters.Execution{
		Handle: adapters.ExecutionHandle{
			RunID:             d.runID,
			StepID:            request.Step.ID,
			AttemptID:         request.AttemptID,
			ProviderSessionID: "sess-start",
		},
		State:   adapters.ExecutionStateRunning,
		Summary: "started",
	}, nil
}

func (d *recordingStepDriver) Resume(_ context.Context, request stepResumeRequest) (*adapters.Execution, error) {
	d.resumeCalled = true
	d.resumeHandle = request.Handle

	return &adapters.Execution{
		Handle:  request.Handle,
		State:   adapters.ExecutionStateRunning,
		Summary: "resumed",
	}, nil
}

func (d *recordingStepDriver) PollOrCollect(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateSucceeded, Summary: "ok"}, nil
}

func (d *recordingStepDriver) Interrupt(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateInterrupted, Summary: "interrupted"}, nil
}

func (d *recordingStepDriver) NormalizeResult(_ context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	return &adapters.StepResult{Handle: execution.Handle, Status: execution.State, Summary: execution.Summary}, nil
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
