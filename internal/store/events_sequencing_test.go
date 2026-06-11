package store

import (
	"errors"
	"os"
	"testing"
)

// TestAppendEventOpenFailureKeepsSequenceUnconsumed locks the candidate-commit
// contract: a failure before any byte reaches the disk must not burn a
// sequence number, so the next successful append keeps the log contiguous and
// replayable.
func TestAppendEventOpenFailureKeepsSequenceUnconsumed(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)

	if _, err := store.AppendEvent(Event{Type: EventRunCreated}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	eventsPath := store.Layout().EventsPath
	if err := os.Chmod(eventsPath, 0o400); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	if _, err := store.AppendEvent(Event{Type: EventRunStarted}); err == nil {
		t.Fatal("AppendEvent() on read-only log: expected error, got nil")
	}

	if err := os.Chmod(eventsPath, 0o600); err != nil {
		t.Fatalf("Chmod() restore error = %v", err)
	}

	appended, err := store.AppendEvent(Event{Type: EventRunStarted})
	if err != nil {
		t.Fatalf("AppendEvent() after recovery error = %v", err)
	}

	if appended.Sequence != 2 {
		t.Fatalf("appended.Sequence = %d, want 2 (no sequence burned by failed open)", appended.Sequence)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	for i, event := range events {
		if event.Sequence != int64(i+1) {
			t.Fatalf("events[%d].Sequence = %d, want %d (contiguous)", i, event.Sequence, i+1)
		}
	}
}

// TestResyncLastSequenceTrustsTheFile locks the disk-is-source-of-truth
// contract: when the on-disk log advanced beyond the in-memory counter (a
// "failed" sync that actually persisted), the counter adopts the on-disk
// sequence so the next append cannot duplicate it.
func TestResyncLastSequenceTrustsTheFile(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)

	if _, err := store.AppendEvent(Event{Type: EventRunCreated}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	// Simulate an append whose error reached the caller although the data
	// became durable: the line exists on disk while lastSequence still says 1.
	outOfBand := `{"sequence":2,"run_id":"run-123","type":"run_started"}` + "\n"

	file, err := os.OpenFile(store.Layout().EventsPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}

	if _, err := file.WriteString(outOfBand); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}

	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store.resyncLastSequence(2)

	appended, err := store.AppendEvent(Event{Type: EventRunSucceeded})
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	if appended.Sequence != 3 {
		t.Fatalf("appended.Sequence = %d, want 3 (resync adopted on-disk sequence 2)", appended.Sequence)
	}
}

// TestResyncLastSequenceUnreadableLogPoisonsStore locks the fail-fast
// contract: when the log cannot even be read back, the sequence counter is no
// longer trustworthy, so the store refuses every further append with
// ErrEventLogUnreliable instead of risking a gap or duplicate. Reopening the
// store (after repairing the log) is the recovery path.
func TestResyncLastSequenceUnreadableLogPoisonsStore(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)

	if _, err := store.AppendEvent(Event{Type: EventRunCreated}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	// A torn line (invalid JSON) makes ReadEvents fail.
	file, err := os.OpenFile(store.Layout().EventsPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}

	if _, err := file.WriteString(`{"torn`); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}

	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store.resyncLastSequence(2)

	if !store.sequenceUnreliable {
		t.Fatal("sequenceUnreliable = false, want true after unreadable log")
	}

	if _, err := store.AppendEvent(Event{Type: EventRunStarted}); !errors.Is(err, ErrEventLogUnreliable) {
		t.Fatalf("AppendEvent() error = %v, want ErrEventLogUnreliable", err)
	}
}
