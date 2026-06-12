package store

import (
	"encoding/json"
	"testing"
)

// TestEventDecodesLegacyRowWithoutStructuredOutput verifies that an events.jsonl
// row written before the StructuredOutput field existed still decodes cleanly,
// leaving StructuredOutput nil.
func TestEventDecodesLegacyRowWithoutStructuredOutput(t *testing.T) {
	legacy := `{"sequence":7,"type":"StepSucceeded","run_id":"run-123","step_id":"review","attempt_id":"attempt-review-01","message":"review ok","data":{"to_state":"succeeded"}}`

	var event Event
	if err := json.Unmarshal([]byte(legacy), &event); err != nil {
		t.Fatalf("Unmarshal legacy event error = %v", err)
	}

	if event.StructuredOutput != nil {
		t.Fatalf("legacy event StructuredOutput = %s, want nil", string(event.StructuredOutput))
	}
	if event.Usage != nil {
		t.Fatalf("legacy event Usage = %+v, want nil", event.Usage)
	}

	if event.Sequence != 7 || event.StepID != "review" {
		t.Fatalf("legacy event decoded incorrectly: %+v", event)
	}
}

func TestEventUsageRoundTrip(t *testing.T) {
	usage := &Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: 0.001}
	event := Event{Sequence: 9, Type: EventStepSucceeded, RunID: "run-123", StepID: "review", Usage: usage}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal event error = %v", err)
	}

	var decoded Event
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal event error = %v", err)
	}

	if decoded.Usage == nil || *decoded.Usage != *usage {
		t.Fatalf("round-tripped Usage = %+v, want %+v", decoded.Usage, usage)
	}
}

func TestEventOmitsEmptyUsage(t *testing.T) {
	encoded, err := json.Marshal(Event{Sequence: 1, Type: EventRunStarted, RunID: "run-123"})
	if err != nil {
		t.Fatalf("Marshal event error = %v", err)
	}

	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatalf("Unmarshal generic error = %v", err)
	}

	if _, ok := generic["usage"]; ok {
		t.Fatalf("usage must be omitted when empty: %s", string(encoded))
	}
}

// TestStepCheckpointDecodesLegacyRowWithoutStructuredOutput verifies the same
// backward compatibility for checkpoint.json step entries.
func TestStepCheckpointDecodesLegacyRowWithoutStructuredOutput(t *testing.T) {
	legacy := `{"state":"succeeded","attempt_id":"attempt-review-01","provider_session_id":"session-review-01","summary":"review ok"}`

	var step StepCheckpoint
	if err := json.Unmarshal([]byte(legacy), &step); err != nil {
		t.Fatalf("Unmarshal legacy checkpoint error = %v", err)
	}

	if step.StructuredOutput != nil {
		t.Fatalf("legacy checkpoint StructuredOutput = %s, want nil", string(step.StructuredOutput))
	}

	if step.State != "succeeded" {
		t.Fatalf("legacy checkpoint decoded incorrectly: %+v", step)
	}
}

// TestEventStructuredOutputRoundTrip confirms the new field survives a
// marshal/unmarshal cycle and is omitted when empty.
func TestEventStructuredOutputRoundTrip(t *testing.T) {
	payload := json.RawMessage(`{"commits":["abc1234"],"summary":"done"}`)
	event := Event{
		Sequence:         9,
		Type:             EventStepSucceeded,
		RunID:            "run-123",
		StepID:           "review",
		StructuredOutput: payload,
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal event error = %v", err)
	}

	var decoded Event
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal event error = %v", err)
	}

	if string(decoded.StructuredOutput) != string(payload) {
		t.Fatalf("round-tripped StructuredOutput = %s, want %s", string(decoded.StructuredOutput), string(payload))
	}
}

// TestEventOmitsEmptyStructuredOutput confirms omitempty keeps legacy-shaped
// output for events without structured output.
func TestEventOmitsEmptyStructuredOutput(t *testing.T) {
	encoded, err := json.Marshal(Event{Sequence: 1, Type: EventRunStarted, RunID: "run-123"})
	if err != nil {
		t.Fatalf("Marshal event error = %v", err)
	}

	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatalf("Unmarshal generic error = %v", err)
	}

	if _, ok := generic["structured_output"]; ok {
		t.Fatalf("structured_output must be omitted when empty: %s", string(encoded))
	}
}

// TestEventTypeJSONRoundTrip verifies that EventType marshals to its string
// name and unmarshals back to the same value.
func TestEventTypeJSONRoundTrip(t *testing.T) {
	event := Event{
		Sequence: 3,
		Type:     EventStepResumed,
		RunID:    "run-abc",
		StepID:   "step-1",
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}

	// Wire format must contain the string name, not a number.
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("Unmarshal raw error = %v", err)
	}
	if raw["type"] != "StepResumed" {
		t.Fatalf("wire type = %v, want \"StepResumed\"", raw["type"])
	}

	var decoded Event
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if decoded.Type != EventStepResumed {
		t.Fatalf("round-tripped Type = %v, want EventStepResumed", decoded.Type)
	}
}

// TestEventTypeUnmarshalUnknownName verifies that an unknown string name
// produces a descriptive error.
func TestEventTypeUnmarshalUnknownName(t *testing.T) {
	raw := `{"sequence":1,"type":"Bogus","run_id":"run-x"}`
	var event Event
	if err := json.Unmarshal([]byte(raw), &event); err == nil {
		t.Fatal("Unmarshal unknown type name: want error, got nil")
	}
}

// TestEventTypeMarshalZero verifies that marshaling a zero EventType returns
// an error (zero is the invalid sentinel value).
func TestEventTypeMarshalZero(t *testing.T) {
	var zero EventType
	_, err := json.Marshal(zero)
	if err == nil {
		t.Fatal("Marshal zero EventType: want error, got nil")
	}
}

// TestEventTypeString verifies String() on known and unknown values.
func TestEventTypeString(t *testing.T) {
	if got := EventStepStarted.String(); got != "StepStarted" {
		t.Fatalf("EventStepStarted.String() = %q, want \"StepStarted\"", got)
	}
	unknown := EventType(9999)
	if got := unknown.String(); got != "EventType(9999)" {
		t.Fatalf("unknown.String() = %q, want \"EventType(9999)\"", got)
	}
}
