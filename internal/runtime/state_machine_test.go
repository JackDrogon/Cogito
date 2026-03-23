package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

func TestTransitionMatrix(t *testing.T) {
	runTests := []struct {
		name    string
		current RunState
		from    RunState
		to      RunState
		wantErr bool
	}{
		{name: "create pending", current: "", from: "", to: RunStatePending},
		{name: "start run", current: RunStatePending, from: RunStatePending, to: RunStateRunning},
		{name: "wait approval", current: RunStateRunning, from: RunStateRunning, to: RunStateWaitingApproval},
		{name: "pause run", current: RunStateRunning, from: RunStateRunning, to: RunStatePaused},
		{name: "resume run", current: RunStatePaused, from: RunStatePaused, to: RunStateRunning},
		{name: "disallow terminal restart", current: RunStateSucceeded, from: RunStateSucceeded, to: RunStateRunning, wantErr: true},
		{name: "disallow backward transition", current: RunStateRunning, from: RunStateRunning, to: RunStatePending, wantErr: true},
		{name: "reject stale current state", current: RunStateRunning, from: RunStatePending, to: RunStateSucceeded, wantErr: true},
	}

	for _, tt := range runTests {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureRunTransition(tt.current, tt.from, tt.to)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ensureRunTransition() error = nil, want error")
				}
				return
			}

			if err != nil {
				t.Fatalf("ensureRunTransition() error = %v", err)
			}
		})
	}

	stepTests := []struct {
		name    string
		current StepState
		from    StepState
		to      StepState
		wantErr bool
	}{
		{name: "queue pending step", current: StepStatePending, from: StepStatePending, to: StepStateQueued},
		{name: "start queued step", current: StepStateQueued, from: StepStateQueued, to: StepStateRunning},
		{name: "succeed running step", current: StepStateRunning, from: StepStateRunning, to: StepStateSucceeded},
		{name: "fail running step", current: StepStateRunning, from: StepStateRunning, to: StepStateFailed},
		{name: "request approval", current: StepStateRunning, from: StepStateRunning, to: StepStateWaitingApproval},
		{name: "retry failed step", current: StepStateFailed, from: StepStateFailed, to: StepStateQueued},
		{name: "approval requeues step", current: StepStateWaitingApproval, from: StepStateWaitingApproval, to: StepStateQueued},
		{name: "disallow pending to running", current: StepStatePending, from: StepStatePending, to: StepStateRunning, wantErr: true},
		{name: "disallow terminal retry", current: StepStateSucceeded, from: StepStateSucceeded, to: StepStateQueued, wantErr: true},
	}

	for _, tt := range stepTests {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureStepTransition(tt.current, tt.from, tt.to)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ensureStepTransition() error = nil, want error")
				}
				return
			}

			if err != nil {
				t.Fatalf("ensureStepTransition() error = %v", err)
			}
		})
	}
}

func TestDeterministicTransitions(t *testing.T) {
	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{Test: t, Spec: runtimeSpec(), CommandScripts: map[string]commandScript{
		"prepare": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "prepare started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "prepare ok"}},
		},
		"notify": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "notify started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "notify ok"}},
		},
	}, Adapter: adapters.NewFakeAdapter(adapters.FakeConfig{
		Scripts: map[string]adapters.FakeScript{
			"attempt-review-01": {
				Start: adapters.FakeSnapshot{State: adapters.ExecutionStateRunning, Summary: "review started"},
				Polls: []adapters.FakeSnapshot{{State: adapters.ExecutionStateSucceeded, Summary: "review ok"}},
			},
		},
	})})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	snapshot := fixture.engine.Snapshot()
	if snapshot.State != RunStateSucceeded {
		t.Fatalf("Snapshot().State = %q, want %q", snapshot.State, RunStateSucceeded)
	}

	for _, stepID := range []string{"prepare", "review", "notify"} {
		step := snapshot.Steps[stepID]
		if step.State != StepStateSucceeded {
			t.Fatalf("step %s state = %q, want %q", stepID, step.State, StepStateSucceeded)
		}

		if strings.TrimSpace(step.AttemptID) == "" {
			t.Fatalf("step %s attempt id is empty", stepID)
		}

		if strings.TrimSpace(step.ProviderSessionID) == "" {
			t.Fatalf("step %s provider session id is empty", stepID)
		}

		if strings.TrimSpace(step.Summary) == "" {
			t.Fatalf("step %s summary is empty", stepID)
		}
	}

	events, err := fixture.store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	gotTypes := make([]store.EventType, 0, len(events))
	for _, event := range events {
		gotTypes = append(gotTypes, event.Type)
	}

	wantTypes := []store.EventType{
		store.EventRunCreated,
		store.EventRunStarted,
		store.EventStepQueued,
		store.EventStepStarted,
		store.EventStepSucceeded,
		store.EventStepQueued,
		store.EventStepStarted,
		store.EventStepSucceeded,
		store.EventStepQueued,
		store.EventStepStarted,
		store.EventStepSucceeded,
		store.EventRunSucceeded,
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event types = %v, want %v", gotTypes, wantTypes)
	}

	t.Log("run reached succeeded")
}

func TestReplayProducesSameTransitions(t *testing.T) {
	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{Test: t, Spec: runtimeSpec(), CommandScripts: map[string]commandScript{
		"prepare": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "prepare started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "prepare ok"}},
		},
		"notify": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "notify started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "notify ok"}},
		},
	}, Adapter: adapters.NewFakeAdapter(adapters.FakeConfig{
		Scripts: map[string]adapters.FakeScript{
			"attempt-review-01": {
				Start: adapters.FakeSnapshot{State: adapters.ExecutionStateRunning, Summary: "review started"},
				Polls: []adapters.FakeSnapshot{{State: adapters.ExecutionStateSucceeded, Summary: "review ok"}},
			},
		},
	})})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	events, err := fixture.store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	replay, err := Replay(fixture.runID, fixture.compiled, events)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}

	if !reflect.DeepEqual(replay.Transitions, fixture.engine.Transitions()) {
		t.Fatalf("Replay().Transitions = %#v, want %#v", replay.Transitions, fixture.engine.Transitions())
	}

	if !reflect.DeepEqual(replay.Snapshot, fixture.engine.Snapshot()) {
		t.Fatalf("Replay().Snapshot = %#v, want %#v", replay.Snapshot, fixture.engine.Snapshot())
	}
}

func TestDuplicateResumeRejected(t *testing.T) {
	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{Test: t, Spec: runtimeSpec(), CommandScripts: map[string]commandScript{
		"prepare": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "prepare started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateInterrupted, Summary: "prepare interrupted"}},
		},
	}})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	if got := fixture.engine.Snapshot().State; got != RunStatePaused {
		t.Fatalf("Snapshot().State after pause = %q, want %q", got, RunStatePaused)
	}

	if err := fixture.engine.Resume(""); err != nil {
		t.Fatalf("Resume() first call error = %v", err)
	}

	err := fixture.engine.Resume("")
	if err == nil {
		t.Fatal("Resume() second call error = nil, want invalid resume state")
	}
	if !strings.Contains(err.Error(), "cannot resume run from") {
		t.Fatalf("Resume() second call error = %v, want contains cannot resume run from", err)
	}

	events := mustReadEvents(t, fixture.store)
	resumeEvents := 0
	for _, event := range events {
		if event.Type == store.EventRunStarted && event.Data[dataFromState] == string(RunStatePaused) && event.Data[dataToState] == string(RunStateRunning) {
			resumeEvents++
		}
	}
	if resumeEvents != 1 {
		t.Fatalf("paused resume events = %d, want 1", resumeEvents)
	}

	t.Log("invalid resume state")
}

func TestTopologicalSchedulingRespectsDependencies(t *testing.T) {
	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{Test: t, Spec: dependencySpec(), CommandScripts: map[string]commandScript{
		"build": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "build started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "build ok"}},
		},
		"docs": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "docs started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "docs ok"}},
		},
		"release": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "release started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "release ok"}},
		},
	}})

	if err := fixture.engine.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if got := fixture.engine.ReadyStepIDs(); !reflect.DeepEqual(got, []string{"build", "docs"}) {
		t.Fatalf("ReadyStepIDs() after Start = %v, want %v", got, []string{"build", "docs"})
	}

	if _, err := fixture.engine.ExecuteNext(t.Context()); err != nil {
		t.Fatalf("ExecuteNext() build error = %v", err)
	}

	if got := fixture.engine.ReadyStepIDs(); !reflect.DeepEqual(got, []string{"docs"}) {
		t.Fatalf("ReadyStepIDs() after build = %v, want %v", got, []string{"docs"})
	}

	if _, err := fixture.engine.ExecuteNext(t.Context()); err != nil {
		t.Fatalf("ExecuteNext() docs error = %v", err)
	}

	if got := fixture.engine.ReadyStepIDs(); !reflect.DeepEqual(got, []string{"release"}) {
		t.Fatalf("ReadyStepIDs() after docs = %v, want %v", got, []string{"release"})
	}
}

func TestReplayRejectsInvalidTransitionOrder(t *testing.T) {
	compiled := compileSpec(t, &workflow.Spec{
		Metadata: workflow.Metadata{Name: "invalid-replay"},
		Steps: []workflow.StepSpec{{
			ID:      "prepare",
			Kind:    workflow.StepKindCommand,
			Command: &workflow.CommandStepSpec{Command: "echo prepare"},
		}},
	})

	_, err := Replay("run-123", compiled, []store.Event{
		buildTestEvent(testEventParams{Sequence: 1, EventType: store.EventRunCreated, RunID: "run-123", Data: eventData(eventDataParams{From: "", To: string(RunStatePending), Summary: "run created"})}),
		buildTestEvent(testEventParams{Sequence: 2, EventType: store.EventStepStarted, RunID: "run-123", StepID: "prepare", AttemptID: "attempt-prepare-01", Data: eventData(eventDataParams{From: string(StepStateQueued), To: string(StepStateRunning), Summary: "prepare started", ProviderSessionID: "session-prepare-01"})}),
	})
	if err == nil {
		t.Fatal("Replay() error = nil, want invalid transition order")
	}

	if !strings.Contains(err.Error(), "invalid transition order") {
		t.Fatalf("Replay() error = %q, want invalid transition order", err)
	}

	t.Log("invalid transition order")
}

func TestResumeAfterApproval(t *testing.T) {
	commandScripts := map[string]commandScript{
		"draft": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "draft started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "draft ok"}},
		},
		"publish": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "publish started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "publish ok"}},
		},
	}
	fixture := newRuntimeMachineFixtureWithPolicy(runtimeMachineFixtureParams{Test: t, Spec: approvalWorkflowSpec(), CommandScripts: commandScripts, ApprovalPolicy: newApprovalModePolicy(ApprovalModeAuto)})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() before grant error = %v", err)
	}

	if got := fixture.engine.Snapshot().State; got != RunStateWaitingApproval {
		t.Fatalf("Snapshot().State before grant = %q, want %q", got, RunStateWaitingApproval)
	}

	if got := fixture.runner.StartCount("publish"); got != 0 {
		t.Fatalf("publish start count before grant = %d, want 0", got)
	}

	fixture = reloadRuntimeMachineFixtureWithPolicy(runtimeMachineFixtureParams{Test: t, CommandScripts: commandScripts, ApprovalPolicy: newApprovalModePolicy(ApprovalModeAuto)}, fixture)

	reloadedSnapshot := fixture.engine.Snapshot()
	if reloadedSnapshot.State != RunStateWaitingApproval {
		t.Fatalf("reloaded Snapshot().State before grant = %q, want %q", reloadedSnapshot.State, RunStateWaitingApproval)
	}
	if strings.TrimSpace(reloadedSnapshot.Steps["legal"].ApprovalID) == "" {
		t.Fatal("reloaded legal approval id is empty")
	}
	if got := reloadedSnapshot.Steps["legal"].ApprovalTrigger; got != ApprovalTriggerExplicit {
		t.Fatalf("reloaded legal approval trigger = %q, want %q", got, ApprovalTriggerExplicit)
	}

	if err := fixture.engine.GrantApproval(t.Context(), ""); err != nil {
		t.Fatalf("GrantApproval() error = %v", err)
	}

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() after grant error = %v", err)
	}

	snapshot := fixture.engine.Snapshot()
	if snapshot.State != RunStateSucceeded {
		t.Fatalf("Snapshot().State after grant = %q, want %q", snapshot.State, RunStateSucceeded)
	}
	if got := fixture.runner.StartCount("publish"); got != 1 {
		t.Fatalf("publish start count after grant = %d, want 1", got)
	}

	for _, stepID := range []string{"draft", "legal", "publish"} {
		if got := snapshot.Steps[stepID].State; got != StepStateSucceeded {
			t.Fatalf("step %s state = %q, want %q", stepID, got, StepStateSucceeded)
		}
	}

	events := mustReadEvents(t, fixture.store)
	assertEventOrder(t, events, []store.EventType{
		store.EventApprovalRequested,
		store.EventRunWaitingApproval,
		store.EventApprovalGranted,
		store.EventRunStarted,
		store.EventStepSucceeded,
	})

	t.Log("approval granted")
	t.Log("run resumed")
}

func TestApprovalDenialStopsSideEffects(t *testing.T) {
	fixture := newRuntimeMachineFixtureWithPolicy(runtimeMachineFixtureParams{Test: t, Spec: approvalWorkflowSpec(), CommandScripts: map[string]commandScript{
		"draft": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "draft started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "draft ok"}},
		},
		"publish": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "publish started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "publish ok"}},
		},
	}, ApprovalPolicy: newApprovalModePolicy(ApprovalModeDeny)})

	err := fixture.engine.ExecuteAll(t.Context())
	if err == nil {
		t.Fatal("ExecuteAll() error = nil, want approval denied")
	}
	if !strings.Contains(err.Error(), "approval denied") {
		t.Fatalf("ExecuteAll() error = %v, want contains approval denied", err)
	}

	snapshot := fixture.engine.Snapshot()
	if snapshot.State != RunStateFailed {
		t.Fatalf("Snapshot().State = %q, want %q", snapshot.State, RunStateFailed)
	}
	if got := snapshot.Steps["legal"].State; got != StepStateFailed {
		t.Fatalf("legal state = %q, want %q", got, StepStateFailed)
	}
	if got := snapshot.Steps["publish"].State; got != StepStatePending {
		t.Fatalf("publish state = %q, want %q", got, StepStatePending)
	}
	if got := fixture.runner.StartCount("publish"); got != 0 {
		t.Fatalf("publish start count = %d, want 0", got)
	}

	events := mustReadEvents(t, fixture.store)
	assertEventOrder(t, events, []store.EventType{
		store.EventApprovalRequested,
		store.EventRunWaitingApproval,
		store.EventApprovalDenied,
		store.EventRunFailed,
	})
	assertNoStepEvents(t, events, "publish")
}

func TestApprovalResolutionBranches(t *testing.T) {
	tests := []struct {
		name       string
		resolve    func(context.Context, *Engine) error
		wantState  RunState
		wantStep   StepState
		wantErr    string
		wantEvent  store.EventType
		publishRun int
	}{
		{
			name: "grant",
			resolve: func(ctx context.Context, engine *Engine) error {
				return engine.GrantApproval(ctx, "")
			},
			wantState:  RunStateRunning,
			wantStep:   StepStateSucceeded,
			wantEvent:  store.EventApprovalGranted,
			publishRun: 0,
		},
		{
			name: "deny",
			resolve: func(ctx context.Context, engine *Engine) error {
				return engine.DenyApproval(ctx, "")
			},
			wantState:  RunStateFailed,
			wantStep:   StepStateFailed,
			wantErr:    "approval denied",
			wantEvent:  store.EventApprovalDenied,
			publishRun: 0,
		},
		{
			name: "timeout",
			resolve: func(ctx context.Context, engine *Engine) error {
				return engine.TimeoutApproval(ctx, "")
			},
			wantState:  RunStateFailed,
			wantStep:   StepStateFailed,
			wantErr:    "approval timed out",
			wantEvent:  store.EventApprovalTimedOut,
			publishRun: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRuntimeMachineFixtureWithPolicy(runtimeMachineFixtureParams{Test: t, Spec: approvalWorkflowSpec(), CommandScripts: map[string]commandScript{
				"draft": {
					Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "draft started"},
					Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "draft ok"}},
				},
				"publish": {
					Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "publish started"},
					Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "publish ok"}},
				},
			}, ApprovalPolicy: newApprovalModePolicy(ApprovalModeAuto)})

			if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
				t.Fatalf("ExecuteAll() before resolution error = %v", err)
			}

			err := tc.resolve(t.Context(), fixture.engine)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("resolve approval error = %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolve approval error = %v, want contains %q", err, tc.wantErr)
				}
			}

			snapshot := fixture.engine.Snapshot()
			if snapshot.State != tc.wantState {
				t.Fatalf("Snapshot().State = %q, want %q", snapshot.State, tc.wantState)
			}
			if got := snapshot.Steps["legal"].State; got != tc.wantStep {
				t.Fatalf("legal state = %q, want %q", got, tc.wantStep)
			}
			if got := fixture.runner.StartCount("publish"); got != tc.publishRun {
				t.Fatalf("publish start count = %d, want %d", got, tc.publishRun)
			}

			events := mustReadEvents(t, fixture.store)
			assertContainsEventType(t, events, tc.wantEvent)
		})
	}
}

func TestApprovalTriggerSources(t *testing.T) {
	tests := []struct {
		name         string
		fixture      func(t *testing.T) runtimeMachineFixture
		stepID       string
		wantTrigger  ApprovalTrigger
		beforeAssert func(t *testing.T, fixture runtimeMachineFixture)
		afterAssert  func(t *testing.T, fixture runtimeMachineFixture)
	}{
		{
			name: "explicit step",
			fixture: func(t *testing.T) runtimeMachineFixture {
				t.Helper()
				return newRuntimeMachineFixtureWithPolicy(runtimeMachineFixtureParams{Test: t, Spec: approvalWorkflowSpec(), CommandScripts: map[string]commandScript{
					"draft": {
						Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "draft started"},
						Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "draft ok"}},
					},
					"publish": {
						Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "publish started"},
						Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "publish ok"}},
					},
				}, ApprovalPolicy: newApprovalModePolicy(ApprovalModeAuto)})
			},
			stepID:      "legal",
			wantTrigger: ApprovalTriggerExplicit,
		},
		{
			name: "adapter requested",
			fixture: func(t *testing.T) runtimeMachineFixture {
				t.Helper()
				return newRuntimeMachineFixtureWithPolicy(runtimeMachineFixtureParams{Test: t, Spec: adapterApprovalWorkflowSpec(), Adapter: adapters.NewFakeAdapter(adapters.FakeConfig{
					Capabilities: adapters.CapabilityMatrix{Resume: true},
					Scripts: map[string]adapters.FakeScript{
						"attempt-review-01": {
							Start:       adapters.FakeSnapshot{State: adapters.ExecutionStateRunning, Summary: "review started"},
							Polls:       []adapters.FakeSnapshot{{State: adapters.ExecutionStateWaitingApproval, Summary: "approval required"}},
							Resume:      &adapters.FakeSnapshot{State: adapters.ExecutionStateRunning, Summary: "review resumed"},
							ResumePolls: []adapters.FakeSnapshot{{State: adapters.ExecutionStateSucceeded, Summary: "review ok"}},
						},
					},
				}), ApprovalPolicy: newApprovalModePolicy(ApprovalModeAuto)})
			},
			stepID:      "review",
			wantTrigger: ApprovalTriggerAdapter,
		},
		{
			name: "policy exception",
			fixture: func(t *testing.T) runtimeMachineFixture {
				t.Helper()
				return newRuntimeMachineFixtureWithPolicy(runtimeMachineFixtureParams{Test: t, Spec: policyApprovalWorkflowSpec(), CommandScripts: map[string]commandScript{
					"ship": {
						Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "ship started"},
						Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "ship ok"}},
					},
				}, ApprovalPolicy: testApprovalPolicy{
					decideGate: func(_ context.Context, request ApprovalGateRequest) (ApprovalDecisionResult, error) {
						return ApprovalDecisionResult{Decision: ApprovalDecisionWait, Summary: request.Summary}, nil
					},
					evaluateException: func(_ context.Context, request ApprovalExceptionRequest) (*ApprovalDecisionResult, error) {
						if request.Step.ID == "ship" {
							return &ApprovalDecisionResult{Decision: ApprovalDecisionWait, Summary: "policy approval required"}, nil
						}
						return nil, nil
					},
				}})
			},
			stepID:      "ship",
			wantTrigger: ApprovalTriggerPolicy,
			beforeAssert: func(t *testing.T, fixture runtimeMachineFixture) {
				t.Helper()
				if got := fixture.runner.StartCount("ship"); got != 0 {
					t.Fatalf("ship start count before grant = %d, want 0", got)
				}
			},
			afterAssert: func(t *testing.T, fixture runtimeMachineFixture) {
				t.Helper()
				if got := fixture.runner.StartCount("ship"); got != 1 {
					t.Fatalf("ship start count after grant = %d, want 1", got)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := tc.fixture(t)

			if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
				t.Fatalf("ExecuteAll() before grant error = %v", err)
			}
			if tc.beforeAssert != nil {
				tc.beforeAssert(t, fixture)
			}

			snapshot := fixture.engine.Snapshot()
			if snapshot.State != RunStateWaitingApproval {
				t.Fatalf("Snapshot().State = %q, want %q", snapshot.State, RunStateWaitingApproval)
			}
			if got := snapshot.Steps[tc.stepID].ApprovalTrigger; got != tc.wantTrigger {
				t.Fatalf("step %s approval trigger = %q, want %q", tc.stepID, got, tc.wantTrigger)
			}

			events := mustReadEvents(t, fixture.store)
			approvalEvent := findApprovalRequestedEvent(t, events, tc.stepID)
			if got := approvalEvent.Data[dataApprovalTrigger]; got != string(tc.wantTrigger) {
				t.Fatalf("approval trigger data = %q, want %q", got, tc.wantTrigger)
			}

			if err := fixture.engine.GrantApproval(t.Context(), ""); err != nil {
				t.Fatalf("GrantApproval() error = %v", err)
			}
			if tc.afterAssert != nil {
				tc.afterAssert(t, fixture)
			}
		})
	}
}

func TestApprovalContinuationStrategiesCoverBuiltins(t *testing.T) {
	for _, trigger := range []ApprovalTrigger{ApprovalTriggerExplicit, ApprovalTriggerAdapter, ApprovalTriggerPolicy} {
		strategy, err := lookupApprovalContinuationStrategy(trigger)
		if err != nil {
			t.Fatalf("lookupApprovalContinuationStrategy(%q) error = %v", trigger, err)
		}
		if strategy == nil {
			t.Fatalf("lookupApprovalContinuationStrategy(%q) returned nil strategy", trigger)
		}
		if strings.TrimSpace(strategy.FailureMessage()) == "" {
			t.Fatalf("strategy failure message for %q is empty", trigger)
		}
	}
}

func TestApprovalContinuationStrategyRejectsUnknownTrigger(t *testing.T) {
	strategy, err := lookupApprovalContinuationStrategy(ApprovalTrigger("unknown"))
	if err == nil {
		t.Fatal("lookupApprovalContinuationStrategy() error = nil, want unsupported trigger")
	}
	if strategy != nil {
		t.Fatalf("lookupApprovalContinuationStrategy() strategy = %#v, want nil", strategy)
	}
	if !strings.Contains(err.Error(), "unsupported approval trigger") {
		t.Fatalf("lookupApprovalContinuationStrategy() error = %v", err)
	}
}

func TestApprovalDecisionHandlersCoverBuiltins(t *testing.T) {
	for _, decision := range []ApprovalDecision{ApprovalDecisionApprove, ApprovalDecisionDeny, ApprovalDecisionTimeout} {
		handler, err := lookupApprovalDecisionHandler(decision)
		if err != nil {
			t.Fatalf("lookupApprovalDecisionHandler(%q) error = %v", decision, err)
		}
		if handler == nil {
			t.Fatalf("lookupApprovalDecisionHandler(%q) returned nil handler", decision)
		}
	}
}

func TestApprovalDecisionHandlerRejectsUnknownDecision(t *testing.T) {
	handler, err := lookupApprovalDecisionHandler(ApprovalDecision("unknown"))
	if err == nil {
		t.Fatal("lookupApprovalDecisionHandler() error = nil, want unsupported decision")
	}
	if handler != nil {
		t.Fatalf("lookupApprovalDecisionHandler() handler = %#v, want nil", handler)
	}
	if !strings.Contains(err.Error(), "unsupported approval decision") {
		t.Fatalf("lookupApprovalDecisionHandler() error = %v", err)
	}
}

func TestStateMachineEventHandlersCoverBuiltins(t *testing.T) {
	for _, eventType := range []store.EventType{
		store.EventRunCreated,
		store.EventRunStarted,
		store.EventRunPaused,
		store.EventRunWaitingApproval,
		store.EventRunSucceeded,
		store.EventRunFailed,
		store.EventRunCanceled,
		store.EventStepQueued,
		store.EventStepStarted,
		store.EventStepSucceeded,
		store.EventStepFailed,
		store.EventStepRetried,
		store.EventApprovalRequested,
		store.EventApprovalGranted,
		store.EventApprovalDenied,
		store.EventApprovalTimedOut,
	} {
		handler, err := lookupStateMachineEventHandler(eventType)
		if err != nil {
			t.Fatalf("lookupStateMachineEventHandler(%q) error = %v", eventType, err)
		}
		if handler == nil {
			t.Fatalf("lookupStateMachineEventHandler(%q) returned nil handler", eventType)
		}
	}
}

func TestStateMachineEventHandlerRejectsUnknownType(t *testing.T) {
	handler, err := lookupStateMachineEventHandler(store.EventType("unknown"))
	if err == nil {
		t.Fatal("lookupStateMachineEventHandler() error = nil, want unsupported event type")
	}
	if handler != nil {
		t.Fatalf("lookupStateMachineEventHandler() handler = %#v, want nil", handler)
	}
	if !strings.Contains(err.Error(), "unsupported event type") {
		t.Fatalf("lookupStateMachineEventHandler() error = %v", err)
	}
}

func TestValidStateSetsCoverBuiltins(t *testing.T) {
	for _, state := range []RunState{RunStatePending, RunStateRunning, RunStateWaitingApproval, RunStatePaused, RunStateSucceeded, RunStateFailed, RunStateCanceled} {
		if !validRunState(state) {
			t.Fatalf("validRunState(%q) = false, want true", state)
		}
	}
	for _, state := range []StepState{StepStatePending, StepStateQueued, StepStateRunning, StepStateWaitingApproval, StepStateSucceeded, StepStateFailed, StepStateCanceled} {
		if !validStepState(state) {
			t.Fatalf("validStepState(%q) = false, want true", state)
		}
	}
	if validRunState(RunState("unknown")) {
		t.Fatal("validRunState(unknown) = true, want false")
	}
	if validStepState(StepState("unknown")) {
		t.Fatal("validStepState(unknown) = true, want false")
	}
}

func TestBuildRunStatusViewFollowsTopologicalOrder(t *testing.T) {
	compiled := compileSpec(t, &workflow.Spec{
		Metadata: workflow.Metadata{Name: "status-view"},
		Steps: []workflow.StepSpec{
			{ID: "prepare", Kind: workflow.StepKindCommand, Command: &workflow.CommandStepSpec{Command: "echo prepare"}},
			{ID: "review", Kind: workflow.StepKindCommand, Needs: []string{"prepare"}, Command: &workflow.CommandStepSpec{Command: "echo review"}},
		},
	})

	view := BuildRunStatusView(compiled, Snapshot{
		RunID: "run-status-view",
		State: RunStateSucceeded,
		Steps: map[string]StepSnapshot{
			"prepare": {State: StepStateSucceeded, Summary: "prepare ok"},
			"review":  {State: StepStateSucceeded, Summary: "review ok"},
		},
	})

	if view.RunID != "run-status-view" {
		t.Fatalf("view.RunID = %q, want %q", view.RunID, "run-status-view")
	}
	if len(view.StepViews) != 2 {
		t.Fatalf("len(view.StepViews) = %d, want 2", len(view.StepViews))
	}
	if view.StepViews[0].StepID != "prepare" || view.StepViews[1].StepID != "review" {
		t.Fatalf("step order = [%s %s], want [prepare review]", view.StepViews[0].StepID, view.StepViews[1].StepID)
	}
	if view.StepViews[0].Summary != "prepare ok" {
		t.Fatalf("view.StepViews[0].Summary = %q, want %q", view.StepViews[0].Summary, "prepare ok")
	}
}

func TestCancelInterruptsActiveProviderBeforePersistingRunCanceled(t *testing.T) {
	compiled := compileSpec(t, &workflow.Spec{
		Metadata: workflow.Metadata{Name: "cancel-running"},
		Steps: []workflow.StepSpec{{
			ID:      "prepare",
			Kind:    workflow.StepKindCommand,
			Command: &workflow.CommandStepSpec{Command: "echo prepare"},
		}},
	})

	runStore, err := store.Open(filepath.Join(t.TempDir(), "runs"), "run-cancel")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	for _, event := range []store.Event{
		buildTestEvent(testEventParams{Sequence: 1, EventType: store.EventRunCreated, RunID: "run-cancel", Data: eventData(eventDataParams{From: "", To: string(RunStatePending), Summary: "run created"})}),
		buildTestEvent(testEventParams{Sequence: 2, EventType: store.EventRunStarted, RunID: "run-cancel", Data: eventData(eventDataParams{From: string(RunStatePending), To: string(RunStateRunning), Summary: "run started"})}),
		buildTestEvent(testEventParams{Sequence: 3, EventType: store.EventStepQueued, RunID: "run-cancel", StepID: "prepare", Data: eventData(eventDataParams{From: string(StepStatePending), To: string(StepStateQueued), Summary: "step ready"})}),
		buildTestEvent(testEventParams{Sequence: 4, EventType: store.EventStepStarted, RunID: "run-cancel", StepID: "prepare", AttemptID: "attempt-prepare-01", Data: eventData(eventDataParams{From: string(StepStateQueued), To: string(StepStateRunning), Summary: "prepare started", ProviderSessionID: "command-prepare-01"})}),
	} {
		if _, err := runStore.AppendEvent(event); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	runner := &interruptRecordingCommandRunner{store: runStore}
	engine, err := NewEngine("run-cancel", compiled, MachineDependencies{Store: runStore, CommandRunner: runner})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	if err := engine.Cancel(t.Context(), ""); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	if !runner.interrupted {
		t.Fatal("Interrupt() was not called before cancel")
	}
	if got := runner.handle.ProviderSessionID; got != "command-prepare-01" {
		t.Fatalf("Interrupt() provider session id = %q, want %q", got, "command-prepare-01")
	}
	for _, eventType := range runner.eventTypesAtInterrupt {
		if eventType == store.EventRunCanceled {
			t.Fatal("RunCanceled was already persisted before Interrupt()")
		}
	}

	if got := engine.Snapshot().State; got != RunStateCanceled {
		t.Fatalf("Snapshot().State = %q, want %q", got, RunStateCanceled)
	}
	if got := engine.Snapshot().Steps["prepare"].State; got != StepStateCanceled {
		t.Fatalf("prepare state = %q, want %q", got, StepStateCanceled)
	}

	events := mustReadEvents(t, runStore)
	if got := events[len(events)-1].Type; got != store.EventRunCanceled {
		t.Fatalf("last event type = %q, want %q", got, store.EventRunCanceled)
	}

	t.Log("run canceled")
}

func TestNewEngineReplaysEventsWhenCheckpointSequenceIsStale(t *testing.T) {
	compiled := compileSpec(t, &workflow.Spec{
		Metadata: workflow.Metadata{Name: "stale-checkpoint"},
		Steps: []workflow.StepSpec{{
			ID:      "prepare",
			Kind:    workflow.StepKindCommand,
			Command: &workflow.CommandStepSpec{Command: "echo prepare"},
		}},
	})

	runStore, err := store.Open(filepath.Join(t.TempDir(), "runs"), "run-stale")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	for _, event := range []store.Event{
		buildTestEvent(testEventParams{Sequence: 1, EventType: store.EventRunCreated, RunID: "run-stale", Data: eventData(eventDataParams{From: "", To: string(RunStatePending), Summary: "run created"})}),
		buildTestEvent(testEventParams{Sequence: 2, EventType: store.EventRunStarted, RunID: "run-stale", Data: eventData(eventDataParams{From: string(RunStatePending), To: string(RunStateRunning), Summary: "run started"})}),
		buildTestEvent(testEventParams{Sequence: 3, EventType: store.EventRunPaused, RunID: "run-stale", Data: eventData(eventDataParams{From: string(RunStateRunning), To: string(RunStatePaused), Summary: "operator pause"})}),
	} {
		if _, err := runStore.AppendEvent(event); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	if err := runStore.SaveCheckpoint(&store.Checkpoint{
		RunID:        "run-stale",
		State:        string(RunStateRunning),
		LastSequence: 2,
		UpdatedAt:    eventData(eventDataParams{})[dataOccurredAt],
		Steps: map[string]store.StepCheckpoint{
			"prepare": {State: string(StepStatePending)},
		},
	}); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	engine, err := NewEngine("run-stale", compiled, MachineDependencies{Store: runStore})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	snapshot := engine.Snapshot()
	if snapshot.State != RunStatePaused {
		t.Fatalf("Snapshot().State = %q, want %q", snapshot.State, RunStatePaused)
	}
	if snapshot.LastSequence != 3 {
		t.Fatalf("Snapshot().LastSequence = %d, want %d", snapshot.LastSequence, 3)
	}
	if got := len(engine.Transitions()); got != 3 {
		t.Fatalf("len(Transitions()) = %d, want %d", got, 3)
	}
}

type runtimeMachineFixture struct {
	runID    string
	compiled *workflow.CompiledWorkflow
	store    *store.Store
	engine   *Engine
	runner   *testCommandRunner
}

type runtimeMachineFixtureParams struct {
	Test           *testing.T
	Spec           *workflow.Spec
	CommandScripts map[string]commandScript
	Adapter        adapters.Adapter
	ApprovalPolicy ApprovalPolicy
}

type testEventParams struct {
	Sequence   int64
	EventType  store.EventType
	RunID      string
	StepID     string
	AttemptID  string
	ApprovalID string
	Data       map[string]string
}

type eventDataParams struct {
	From              string
	To                string
	Summary           string
	ProviderSessionID string
	NormalizedStatus  string
}

func newRuntimeMachineFixture(params runtimeMachineFixtureParams) runtimeMachineFixture {
	params.Test.Helper()
	return newRuntimeMachineFixtureWithPolicy(params)
}

func newRuntimeMachineFixtureWithPolicy(params runtimeMachineFixtureParams) runtimeMachineFixture {
	params.Test.Helper()

	compiled := compileSpec(params.Test, params.Spec)
	runID := "run-123"
	runStore, err := store.Open(filepath.Join(params.Test.TempDir(), "runs"), runID)
	if err != nil {
		params.Test.Fatalf("store.Open() error = %v", err)
	}

	ids := newTestIDGenerator()
	runner := newTestCommandRunner(params.CommandScripts)
	engine, err := NewEngine(runID, compiled, MachineDependencies{
		Clock: func() time.Time {
			return time.Date(2026, time.March, 22, 15, 4, 5, 0, time.UTC)
		},
		IDs:            ids,
		Store:          runStore,
		ApprovalPolicy: params.ApprovalPolicy,
		CommandRunner:  runner,
		LookupAdapter: func(step workflow.CompiledStep) (adapters.Adapter, error) {
			if params.Adapter == nil {
				return nil, fmt.Errorf("unexpected agent step %s", step.ID)
			}

			return params.Adapter, nil
		},
	})
	if err != nil {
		params.Test.Fatalf("NewEngine() error = %v", err)
	}

	return runtimeMachineFixture{
		runID:    runID,
		compiled: compiled,
		store:    runStore,
		engine:   engine,
		runner:   runner,
	}
}

func TestNewEngineUsesInjectedDriverFactory(t *testing.T) {
	compiled := compileSpec(t, &workflow.Spec{
		Metadata: workflow.Metadata{Name: "custom-driver-factory"},
		Steps: []workflow.StepSpec{{
			ID:      "prepare",
			Kind:    workflow.StepKindCommand,
			Command: &workflow.CommandStepSpec{Command: "echo prepare"},
		}},
	})

	runStore, err := store.Open(filepath.Join(t.TempDir(), "runs"), "run-custom-driver")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	runner := newTestCommandRunner(map[string]commandScript{
		"prepare": {Start: snapshotSpec{State: adapters.ExecutionStateSucceeded, Summary: "done"}},
	})

	buildCount := 0
	engine, err := NewEngine("run-custom-driver", compiled, MachineDependencies{
		Store:         runStore,
		CommandRunner: runner,
		DriverFactory: StepDriverFactoryFunc(func(_ *Engine, step workflow.CompiledStep) (stepDriver, error) {
			buildCount++
			if step.ID != "prepare" {
				return nil, fmt.Errorf("unexpected step %s", step.ID)
			}

			return commandDriver{runner: runner}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	if err := engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	if buildCount == 0 {
		t.Fatal("DriverFactory was not used")
	}
	if runner.StartCount("prepare") != 1 {
		t.Fatalf("runner.StartCount(prepare) = %d, want 1", runner.StartCount("prepare"))
	}
}

func reloadRuntimeMachineFixtureWithPolicy(params runtimeMachineFixtureParams, fixture runtimeMachineFixture) runtimeMachineFixture {
	params.Test.Helper()

	runStore, err := store.Open(fixture.store.Layout().BaseDir, fixture.store.Layout().RunID)
	if err != nil {
		params.Test.Fatalf("store.Open() reload error = %v", err)
	}

	runner := newTestCommandRunner(params.CommandScripts)
	engine, err := NewEngine(fixture.runID, fixture.compiled, MachineDependencies{
		Clock: func() time.Time {
			return time.Date(2026, time.March, 22, 15, 4, 5, 0, time.UTC)
		},
		IDs:            newTestIDGenerator(),
		Store:          runStore,
		ApprovalPolicy: params.ApprovalPolicy,
		CommandRunner:  runner,
		LookupAdapter: func(step workflow.CompiledStep) (adapters.Adapter, error) {
			if params.Adapter == nil {
				return nil, fmt.Errorf("unexpected agent step %s", step.ID)
			}

			return params.Adapter, nil
		},
	})
	if err != nil {
		params.Test.Fatalf("NewEngine() reload error = %v", err)
	}

	fixture.store = runStore
	fixture.engine = engine
	fixture.runner = runner
	return fixture
}

func runtimeSpec() *workflow.Spec {
	return &workflow.Spec{
		Metadata: workflow.Metadata{Name: "runtime"},
		Steps: []workflow.StepSpec{
			{
				ID:      "prepare",
				Kind:    workflow.StepKindCommand,
				Command: &workflow.CommandStepSpec{Command: "echo prepare"},
			},
			{
				ID:    "review",
				Kind:  workflow.StepKindAgent,
				Needs: []string{"prepare"},
				Agent: &workflow.AgentStepSpec{Agent: "fake", Prompt: "review"},
			},
			{
				ID:      "notify",
				Kind:    workflow.StepKindCommand,
				Needs:   []string{"review"},
				Command: &workflow.CommandStepSpec{Command: "echo notify"},
			},
		},
	}
}

func approvalWorkflowSpec() *workflow.Spec {
	return &workflow.Spec{
		Metadata: workflow.Metadata{Name: "approval"},
		Steps: []workflow.StepSpec{
			{ID: "draft", Kind: workflow.StepKindCommand, Command: &workflow.CommandStepSpec{Command: "echo draft"}},
			{ID: "legal", Kind: workflow.StepKindApproval, Needs: []string{"draft"}, Approval: &workflow.ApprovalStepSpec{Message: "Legal approval required before publish"}},
			{ID: "publish", Kind: workflow.StepKindCommand, Needs: []string{"legal"}, Command: &workflow.CommandStepSpec{Command: "./publish.sh"}},
		},
	}
}

func adapterApprovalWorkflowSpec() *workflow.Spec {
	return &workflow.Spec{
		Metadata: workflow.Metadata{Name: "adapter-approval"},
		Steps: []workflow.StepSpec{{
			ID:    "review",
			Kind:  workflow.StepKindAgent,
			Agent: &workflow.AgentStepSpec{Agent: "fake", Prompt: "review"},
		}},
	}
}

func policyApprovalWorkflowSpec() *workflow.Spec {
	return &workflow.Spec{
		Metadata: workflow.Metadata{Name: "policy-approval"},
		Steps: []workflow.StepSpec{{
			ID:      "ship",
			Kind:    workflow.StepKindCommand,
			Command: &workflow.CommandStepSpec{Command: "./ship.sh"},
		}},
	}
}

func dependencySpec() *workflow.Spec {
	return &workflow.Spec{
		Metadata: workflow.Metadata{Name: "dependencies"},
		Steps: []workflow.StepSpec{
			{ID: "build", Kind: workflow.StepKindCommand, Command: &workflow.CommandStepSpec{Command: "echo build"}},
			{ID: "docs", Kind: workflow.StepKindCommand, Command: &workflow.CommandStepSpec{Command: "echo docs"}},
			{ID: "release", Kind: workflow.StepKindCommand, Needs: []string{"build", "docs"}, Command: &workflow.CommandStepSpec{Command: "echo release"}},
		},
	}
}

func compileSpec(t *testing.T, spec *workflow.Spec) *workflow.CompiledWorkflow {
	t.Helper()

	compiled, err := workflow.CompileWorkflow(spec)
	if err != nil {
		t.Fatalf("CompileWorkflow() error = %v", err)
	}

	return compiled
}

type testIDGenerator struct {
	attempts  map[string]int
	sessions  map[string]int
	approvals map[string]int
}

func newTestIDGenerator() *testIDGenerator {
	return &testIDGenerator{
		attempts:  map[string]int{},
		sessions:  map[string]int{},
		approvals: map[string]int{},
	}
}

func (g *testIDGenerator) NewAttemptID(stepID string) string {
	return g.next("attempt", stepID, g.attempts)
}

func (g *testIDGenerator) NewSyntheticSessionID(stepID string) string {
	return g.next("session", stepID, g.sessions)
}

func (g *testIDGenerator) NewApprovalID(stepID string) string {
	return g.next("approval", stepID, g.approvals)
}

func (g *testIDGenerator) next(prefix, stepID string, bucket map[string]int) string {
	bucket[stepID]++
	return fmt.Sprintf("%s-%s-%02d", prefix, stepID, bucket[stepID])
}

type snapshotSpec struct {
	State   adapters.ExecutionState
	Summary string
}

type testApprovalPolicy struct {
	decideGate        func(context.Context, ApprovalGateRequest) (ApprovalDecisionResult, error)
	evaluateException func(context.Context, ApprovalExceptionRequest) (*ApprovalDecisionResult, error)
}

func (p testApprovalPolicy) DecideGate(ctx context.Context, request ApprovalGateRequest) (ApprovalDecisionResult, error) {
	if p.decideGate != nil {
		return p.decideGate(ctx, request)
	}

	return ApprovalDecisionResult{Decision: ApprovalDecisionWait, Summary: request.Summary}, nil
}

func (p testApprovalPolicy) EvaluateException(ctx context.Context, request ApprovalExceptionRequest) (*ApprovalDecisionResult, error) {
	if p.evaluateException != nil {
		return p.evaluateException(ctx, request)
	}

	return nil, nil
}

type commandScript struct {
	Start     snapshotSpec
	Polls     []snapshotSpec
	Interrupt snapshotSpec
}

type commandSession struct {
	handle adapters.ExecutionHandle
	script commandScript
	index  int
	state  snapshotSpec
}

type testCommandRunner struct {
	scripts       map[string]commandScript
	sessions      map[string]*commandSession
	providerIndex map[string]int
	startCount    map[string]int
}

func newTestCommandRunner(scripts map[string]commandScript) *testCommandRunner {
	cloned := make(map[string]commandScript, len(scripts))
	for stepID, script := range scripts {
		cloned[stepID] = script
	}

	return &testCommandRunner{
		scripts:       cloned,
		sessions:      map[string]*commandSession{},
		providerIndex: map[string]int{},
		startCount:    map[string]int{},
	}
}

func (r *testCommandRunner) Start(_ context.Context, request CommandRequest) (*adapters.Execution, error) {
	script, ok := r.scripts[request.StepID]
	if !ok {
		return nil, fmt.Errorf("command script not found for %s", request.StepID)
	}

	r.startCount[request.StepID]++
	r.providerIndex[request.StepID]++
	providerSessionID := fmt.Sprintf("command-%s-%02d", request.StepID, r.providerIndex[request.StepID])
	handle := adapters.ExecutionHandle{
		RunID:             request.RunID,
		StepID:            request.StepID,
		AttemptID:         request.AttemptID,
		ProviderSessionID: providerSessionID,
	}

	session := &commandSession{handle: handle, script: script, state: script.Start}
	r.sessions[providerSessionID] = session
	return buildCommandExecution(handle, script.Start), nil
}

func (r *testCommandRunner) PollOrCollect(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	session, ok := r.sessions[handle.ProviderSessionID]
	if !ok {
		return nil, fmt.Errorf("command session not found for %s", handle.ProviderSessionID)
	}

	if session.index < len(session.script.Polls) {
		session.state = session.script.Polls[session.index]
		session.index++
	}

	return buildCommandExecution(session.handle, session.state), nil
}

func (r *testCommandRunner) Interrupt(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	session, ok := r.sessions[handle.ProviderSessionID]
	if !ok {
		return nil, fmt.Errorf("command session not found for %s", handle.ProviderSessionID)
	}

	snapshot := snapshotSpec{State: adapters.ExecutionStateInterrupted, Summary: "interrupted"}
	if session.script.Interrupt.State != "" {
		snapshot = session.script.Interrupt
	}

	session.state = snapshot
	return buildCommandExecution(session.handle, session.state), nil
}

func (r *testCommandRunner) NormalizeResult(_ context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	if execution == nil {
		return nil, errors.New("execution is required")
	}

	return &adapters.StepResult{
		Handle:  execution.Handle,
		Status:  execution.State,
		Summary: execution.Summary,
	}, nil
}

func (r *testCommandRunner) StartCount(stepID string) int {
	return r.startCount[stepID]
}

func buildCommandExecution(handle adapters.ExecutionHandle, snapshot snapshotSpec) *adapters.Execution {
	return &adapters.Execution{
		Handle:  handle,
		State:   snapshot.State,
		Summary: snapshot.Summary,
	}
}

type interruptRecordingCommandRunner struct {
	store                 *store.Store
	handle                adapters.ExecutionHandle
	interrupted           bool
	eventTypesAtInterrupt []store.EventType
}

func (r *interruptRecordingCommandRunner) Start(_ context.Context, request CommandRequest) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: adapters.ExecutionHandle{RunID: request.RunID, StepID: request.StepID, AttemptID: request.AttemptID, ProviderSessionID: "command-start"}, State: adapters.ExecutionStateRunning, Summary: "started"}, nil
}

func (r *interruptRecordingCommandRunner) PollOrCollect(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateRunning, Summary: "running"}, nil
}

func (r *interruptRecordingCommandRunner) Interrupt(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	r.interrupted = true
	r.handle = handle

	events, err := r.store.ReadEvents()
	if err != nil {
		return nil, err
	}

	r.eventTypesAtInterrupt = r.eventTypesAtInterrupt[:0]
	for _, event := range events {
		r.eventTypesAtInterrupt = append(r.eventTypesAtInterrupt, event.Type)
	}

	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateInterrupted, Summary: "interrupted"}, nil
}

func (r *interruptRecordingCommandRunner) NormalizeResult(_ context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	if execution == nil {
		return nil, errors.New("execution is required")
	}

	return &adapters.StepResult{Handle: execution.Handle, Status: execution.State, Summary: execution.Summary}, nil
}

func buildTestEvent(params testEventParams) store.Event {
	return store.Event{
		Sequence:   params.Sequence,
		Type:       params.EventType,
		RunID:      params.RunID,
		StepID:     params.StepID,
		AttemptID:  params.AttemptID,
		ApprovalID: params.ApprovalID,
		Message:    params.Data[dataSummary],
		Data:       params.Data,
	}
}

func eventData(params eventDataParams) map[string]string {
	data := map[string]string{
		dataOccurredAt: time.Date(2026, time.March, 22, 15, 4, 5, 0, time.UTC).Format(time.RFC3339Nano),
		dataFromState:  params.From,
		dataToState:    params.To,
		dataSummary:    params.Summary,
	}

	if params.ProviderSessionID != "" {
		data[dataProviderSessionID] = params.ProviderSessionID
	}

	if params.NormalizedStatus != "" {
		data[dataNormalizedStatus] = params.NormalizedStatus
	}

	return data
}

func mustReadEvents(t *testing.T, runStore *store.Store) []store.Event {
	t.Helper()

	events, err := runStore.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	return events
}

func assertEventOrder(t *testing.T, events []store.Event, want []store.EventType) {
	t.Helper()

	position := 0
	for _, event := range events {
		if position < len(want) && event.Type == want[position] {
			position++
		}
	}

	if position != len(want) {
		got := make([]store.EventType, 0, len(events))
		for _, event := range events {
			got = append(got, event.Type)
		}
		t.Fatalf("event order = %v, want subsequence %v", got, want)
	}
}

func assertContainsEventType(t *testing.T, events []store.Event, want store.EventType) {
	t.Helper()

	for _, event := range events {
		if event.Type == want {
			return
		}
	}

	t.Fatalf("event type %q not found", want)
}

func assertNoStepEvents(t *testing.T, events []store.Event, stepID string) {
	t.Helper()

	for _, event := range events {
		if event.StepID == stepID {
			t.Fatalf("unexpected event for step %s: %s", stepID, event.Type)
		}
	}
}

func findApprovalRequestedEvent(t *testing.T, events []store.Event, stepID string) store.Event {
	t.Helper()

	for _, event := range events {
		if event.Type == store.EventApprovalRequested && event.StepID == stepID {
			return event
		}
	}

	t.Fatalf("approval requested event for %s not found", stepID)
	return store.Event{}
}
