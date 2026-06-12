package store

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestAppendEventRejectsOversizedEvent locks the write side of the
// "anything appended stays readable" invariant: an event whose encoded form
// exceeds maxAppendEventBytes is rejected before any byte reaches the log,
// the sequence counter stays untouched, and the log remains readable.
func TestAppendEventRejectsOversizedEvent(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)

	if _, err := store.AppendEvent(Event{Type: EventRunCreated}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	huge := Event{
		Type:             EventStepSucceeded,
		StructuredOutput: json.RawMessage(`"` + strings.Repeat("x", maxAppendEventBytes) + `"`),
	}
	if _, err := store.AppendEvent(huge); err == nil {
		t.Fatal("AppendEvent(oversized) error = nil, want size-limit error")
	} else if !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("AppendEvent(oversized) error = %v, want size-limit error", err)
	}

	appended, err := store.AppendEvent(Event{Type: EventRunSucceeded})
	if err != nil {
		t.Fatalf("AppendEvent() after rejection error = %v", err)
	}

	if appended.Sequence != 2 {
		t.Fatalf("appended.Sequence = %d, want 2 (rejection must not consume a sequence)", appended.Sequence)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
}

// TestAppendEventLargeEventRoundTrips proves the scanner cap stays above the
// append cap: an event near (but under) the append limit is written and read
// back intact.
func TestAppendEventLargeEventRoundTrips(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)

	payload := json.RawMessage(`"` + strings.Repeat("y", maxAppendEventBytes-1024) + `"`)
	if _, err := store.AppendEvent(Event{Type: EventStepSucceeded, StructuredOutput: payload}); err != nil {
		t.Fatalf("AppendEvent(large) error = %v", err)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}

	if !bytes.Equal(events[0].StructuredOutput, payload) {
		t.Fatal("StructuredOutput did not round-trip intact")
	}
}
