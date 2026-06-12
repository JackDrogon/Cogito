package runtime

import (
	"context"
	"encoding/json"
	"errors"
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
	usage        *provider.Usage
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
	return &provider.Execution{Handle: handle, State: provider.ExecutionStateSucceeded, Summary: "ok", Usage: d.usage}, nil
}

func (d *recordingStepDriver) Interrupt(_ context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	return &provider.Execution{Handle: handle, State: provider.ExecutionStateInterrupted, Summary: "interrupted"}, nil
}

func (d *recordingStepDriver) NormalizeResult(_ context.Context, execution *provider.Execution) (*provider.StepResult, error) {
	return &provider.StepResult{Handle: execution.Handle, Status: execution.State, Summary: execution.Summary, Usage: execution.Usage}, nil
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

func TestExecuteStepPersistsUsageOnSucceededEvent(t *testing.T) {
	usage := &provider.Usage{InputTokens: 111, OutputTokens: 22, TotalTokens: 133, CostUSD: 0.0123}
	driver := &recordingStepDriver{runID: "run-123", usage: usage}
	engine := newResumeDispatchEngine(t, driver, StepSnapshot{State: StepStateQueued})

	if err := engine.executeStep(t.Context(), "review"); err != nil {
		t.Fatalf("executeStep() error = %v", err)
	}

	events, err := engine.store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	var succeeded store.Event
	for _, event := range events {
		if event.Type == store.EventStepSucceeded {
			succeeded = event
		}
	}

	if succeeded.Usage == nil {
		t.Fatal("StepSucceeded Usage = nil, want persisted usage")
	}
	if succeeded.Usage.InputTokens != usage.InputTokens || succeeded.Usage.OutputTokens != usage.OutputTokens || succeeded.Usage.TotalTokens != usage.TotalTokens || succeeded.Usage.CostUSD != usage.CostUSD {
		t.Fatalf("StepSucceeded Usage = %+v, want %+v", succeeded.Usage, usage)
	}

	encoded, err := json.Marshal(succeeded)
	if err != nil {
		t.Fatalf("Marshal StepSucceeded event: %v", err)
	}
	if !json.Valid(encoded) || !containsJSONField(encoded, "usage") {
		t.Fatalf("StepSucceeded event JSON missing usage: %s", string(encoded))
	}
}

func TestDriverSetupErrorDoesNotRetry(t *testing.T) {
	compiled := compileSpec(t, &workflow.Spec{
		Metadata: workflow.Metadata{Name: "driver-setup-retry"},
		Steps: []workflow.StepSpec{{
			ID:      "review",
			Kind:    workflow.StepKindAgent,
			Retries: 2,
			Agent:   &workflow.AgentStepSpec{Agent: "fake", Prompt: "review"},
		}},
	})

	runStore, err := store.Open(filepath.Join(t.TempDir(), "runs"), "run-123")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	engine, err := NewEngine("run-123", compiled, MachineDependencies{
		Store: runStore,
		DriverFactory: StepDriverFactoryFunc(func(_ *Engine, _ workflow.CompiledStep) (stepDriver, error) {
			return nil, errors.New("bad driver config")
		}),
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	if err := engine.ExecuteAll(t.Context()); err == nil {
		t.Fatal("ExecuteAll() error = nil, want driver setup failure")
	}

	events, err := runStore.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	assertEventTypeCount(t, events, store.EventStepRetried, 0)
	assertEventTypeCount(t, events, store.EventStepFailed, 1)
	assertEventTypeCount(t, events, store.EventRunFailed, 1)
}

func containsJSONField(encoded []byte, field string) bool {
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return false
	}

	_, ok := object[field]

	return ok
}
