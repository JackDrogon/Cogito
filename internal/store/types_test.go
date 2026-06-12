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
