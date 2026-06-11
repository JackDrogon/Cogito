package runtime

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

const reviewStructuredOutput = `{"completed":["task a"],"blocked":[],"deferred":[],"toolchain_bugs":[],"commits":["abc1234"],"verification":["go test ./..."],"summary":"done"}`

// structuredOutputFixture runs the prepare→review→notify workflow to completion
// with the review agent step emitting a structured AgentResult payload.
func structuredOutputFixture(t *testing.T) runtimeMachineFixture {
	t.Helper()

	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test:           t,
		Spec:           runtimeSpec(),
		CommandScripts: succeedingCommandScripts(),
		Adapter: adapters.NewFakeAdapter(adapters.FakeConfig{
			Capabilities: adapters.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true},
			Scripts: map[string]adapters.FakeScript{
				"attempt-review-01": {
					Start: adapters.FakeSnapshot{State: adapters.ExecutionStateRunning, Summary: "review started"},
					Polls: []adapters.FakeSnapshot{{
						State:            adapters.ExecutionStateSucceeded,
						Summary:          "review ok",
						StructuredOutput: json.RawMessage(reviewStructuredOutput),
					}},
				},
			},
		}),
	})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	return fixture
}

func succeedingCommandScripts() map[string]commandScript {
	return map[string]commandScript{
		"prepare": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "prepare started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "prepare ok"}},
		},
		"notify": {
			Start: snapshotSpec{State: adapters.ExecutionStateRunning, Summary: "notify started"},
			Polls: []snapshotSpec{{State: adapters.ExecutionStateSucceeded, Summary: "notify ok"}},
		},
	}
}

func TestEngineStructuredOutputPersistedToSnapshot(t *testing.T) {
	fixture := structuredOutputFixture(t)

	got := fixture.engine.Snapshot().Steps["review"].StructuredOutput
	if !equalJSON(t, got, json.RawMessage(reviewStructuredOutput)) {
		t.Fatalf("review StructuredOutput = %s, want %s", string(got), reviewStructuredOutput)
	}
}

func TestEngineStepStructuredOutputReturnsResult(t *testing.T) {
	fixture := structuredOutputFixture(t)

	got, err := fixture.engine.StepStructuredOutput("review")
	if err != nil {
		t.Fatalf("StepStructuredOutput(review) error = %v", err)
	}

	if !equalJSON(t, got, json.RawMessage(reviewStructuredOutput)) {
		t.Fatalf("StepStructuredOutput(review) = %s, want %s", string(got), reviewStructuredOutput)
	}
}

func TestEngineStepStructuredOutputNilForCommandStep(t *testing.T) {
	fixture := structuredOutputFixture(t)

	got, err := fixture.engine.StepStructuredOutput("notify")
	if err != nil {
		t.Fatalf("StepStructuredOutput(notify) error = %v", err)
	}

	if got != nil {
		t.Fatalf("StepStructuredOutput(notify) = %s, want nil", string(got))
	}
}

func TestEngineStepStructuredOutputUnknownStepErrors(t *testing.T) {
	fixture := structuredOutputFixture(t)

	_, err := fixture.engine.StepStructuredOutput("missing")
	assertStateError(t, err)
}

func TestEngineStepStructuredOutputNotSucceededErrors(t *testing.T) {
	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test:           t,
		Spec:           runtimeSpec(),
		CommandScripts: succeedingCommandScripts(),
		Adapter: adapters.NewFakeAdapter(adapters.FakeConfig{
			Capabilities: adapters.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true},
			Scripts:      map[string]adapters.FakeScript{},
		}),
	})

	// review never runs, so it stays pending and must error.
	_, err := fixture.engine.StepStructuredOutput("review")
	assertStateError(t, err)
}

func TestEngineStructuredOutputEventAndCheckpointRoundTrip(t *testing.T) {
	fixture := structuredOutputFixture(t)

	events := mustReadEvents(t, fixture.store)
	succeeded := findStepSucceededEvent(t, events, "review")
	if !equalJSON(t, succeeded.StructuredOutput, json.RawMessage(reviewStructuredOutput)) {
		t.Fatalf("StepSucceeded event StructuredOutput = %s, want %s", string(succeeded.StructuredOutput), reviewStructuredOutput)
	}

	loaded, err := fixture.store.LoadCheckpoint()
	if err != nil {
		t.Fatalf("LoadCheckpoint() error = %v", err)
	}

	stored := loaded.Checkpoint.Steps["review"]
	if !equalJSON(t, stored.StructuredOutput, json.RawMessage(reviewStructuredOutput)) {
		t.Fatalf("checkpoint review StructuredOutput = %s, want %s", string(stored.StructuredOutput), reviewStructuredOutput)
	}
}

func TestEngineStructuredOutputSurvivesResume(t *testing.T) {
	params := runtimeMachineFixtureParams{
		Test:           t,
		Spec:           runtimeSpec(),
		CommandScripts: succeedingCommandScripts(),
		Adapter: adapters.NewFakeAdapter(adapters.FakeConfig{
			Capabilities: adapters.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true},
			Scripts: map[string]adapters.FakeScript{
				"attempt-review-01": {
					Start: adapters.FakeSnapshot{State: adapters.ExecutionStateRunning, Summary: "review started"},
					Polls: []adapters.FakeSnapshot{{
						State:            adapters.ExecutionStateSucceeded,
						Summary:          "review ok",
						StructuredOutput: json.RawMessage(reviewStructuredOutput),
					}},
				},
			},
		}),
	}

	fixture := newRuntimeMachineFixture(params)
	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	reloaded := reloadRuntimeMachineFixtureWithPolicy(params, fixture)

	got, err := reloaded.engine.StepStructuredOutput("review")
	if err != nil {
		t.Fatalf("StepStructuredOutput(review) after resume error = %v", err)
	}

	if !equalJSON(t, got, json.RawMessage(reviewStructuredOutput)) {
		t.Fatalf("StepStructuredOutput(review) after resume = %s, want %s", string(got), reviewStructuredOutput)
	}
}

func TestApplyStepEventFoldsStructuredOutputOnReplay(t *testing.T) {
	compiled := compileSpec(t, &workflow.Spec{
		Metadata: workflow.Metadata{Name: "structured-replay"},
		Steps: []workflow.StepSpec{{
			ID:    "review",
			Kind:  workflow.StepKindAgent,
			Agent: &workflow.AgentStepSpec{Agent: "fake", Prompt: "review"},
		}},
	})

	succeeded := buildTestEvent(testEventParams{
		Sequence:  5,
		EventType: store.EventStepSucceeded,
		RunID:     "run-123",
		StepID:    "review",
		AttemptID: "attempt-review-01",
		Data: eventData(eventDataParams{
			From:              string(StepStateRunning),
			To:                string(StepStateSucceeded),
			Summary:           "review ok",
			ProviderSessionID: "session-review-01",
			NormalizedStatus:  string(adapters.ExecutionStateSucceeded),
		}),
	})
	succeeded.StructuredOutput = json.RawMessage(reviewStructuredOutput)

	events := []store.Event{
		buildTestEvent(testEventParams{Sequence: 1, EventType: store.EventRunCreated, RunID: "run-123", Data: eventData(eventDataParams{From: "", To: string(RunStatePending), Summary: "run created"})}),
		buildTestEvent(testEventParams{Sequence: 2, EventType: store.EventRunStarted, RunID: "run-123", Data: eventData(eventDataParams{From: string(RunStatePending), To: string(RunStateRunning), Summary: "run started"})}),
		buildTestEvent(testEventParams{Sequence: 3, EventType: store.EventStepQueued, RunID: "run-123", StepID: "review", Data: eventData(eventDataParams{From: string(StepStatePending), To: string(StepStateQueued), Summary: "step ready"})}),
		buildTestEvent(testEventParams{Sequence: 4, EventType: store.EventStepStarted, RunID: "run-123", StepID: "review", AttemptID: "attempt-review-01", Data: eventData(eventDataParams{From: string(StepStateQueued), To: string(StepStateRunning), Summary: "review started", ProviderSessionID: "session-review-01"})}),
		succeeded,
	}

	replay, err := Replay("run-123", compiled, events)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}

	got := replay.Snapshot.Steps["review"].StructuredOutput
	if !equalJSON(t, got, json.RawMessage(reviewStructuredOutput)) {
		t.Fatalf("replayed review StructuredOutput = %s, want %s", string(got), reviewStructuredOutput)
	}
}

func TestCloneSnapshotDeepCopiesStructuredOutput(t *testing.T) {
	original := Snapshot{
		RunID: "run-123",
		Steps: map[string]StepSnapshot{
			"review": {
				State:            StepStateSucceeded,
				StructuredOutput: json.RawMessage(`{"commits":["abc"]}`),
			},
		},
	}

	cloned := cloneSnapshot(original)

	// Mutate the original backing array; the clone must be unaffected.
	original.Steps["review"].StructuredOutput[0] = 'X'

	want := `{"commits":["abc"]}`
	if got := string(cloned.Steps["review"].StructuredOutput); got != want {
		t.Fatalf("cloned StructuredOutput = %s, want %s", got, want)
	}
}

func assertStateError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("error = nil, want ErrorCodeState error")
	}

	var runtimeErr *Error
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("error type = %T, want *runtime.Error", err)
	}

	if runtimeErr.Code != ErrorCodeState {
		t.Fatalf("error code = %q, want %q", runtimeErr.Code, ErrorCodeState)
	}
}

func findStepSucceededEvent(t *testing.T, events []store.Event, stepID string) store.Event {
	t.Helper()

	for _, event := range events {
		if event.Type == store.EventStepSucceeded && event.StepID == stepID {
			return event
		}
	}

	t.Fatalf("StepSucceeded event for %s not found", stepID)

	return store.Event{}
}

func equalJSON(t *testing.T, got, want json.RawMessage) bool {
	t.Helper()

	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		return false
	}

	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("want is not valid JSON: %v", err)
	}

	return reflect.DeepEqual(gotValue, wantValue)
}
