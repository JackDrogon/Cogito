package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Event-log line sizing. Single events can be large because step transitions
// may embed an agent's full structured output. The append cap and the scanner
// cap together guarantee the invariant "anything AppendEvent wrote, ReadEvents
// can read back": an event that would exceed the append cap is rejected up
// front (failing the append is better than writing a line that breaks every
// later status/resume/replay), and the scanner cap stays well above the append
// cap so historical logs near the old limit remain readable.
const (
	eventScanInitialBufferSize = 64 * 1024
	maxAppendEventBytes        = 2 * 1024 * 1024
	eventScanMaxLineSize       = 4 * 1024 * 1024
)

// AppendEvent durably appends one event with the next sequence number.
//
// Sequence accounting follows a candidate-commit scheme because the runtime's
// replay requires strictly contiguous sequences: the counter is only advanced
// once the on-disk outcome is known. Failures before any byte can reach the
// file (marshal, open) leave the counter untouched; failures after the write
// started (write, sync) leave the disk state UNKNOWN, so the counter is
// re-synced from the file itself — the file is the source of truth — to
// guarantee a later append can never duplicate a sequence that already became
// durable.
// ErrEventLogUnreliable reports that a previous failed append left the event
// log unreadable, so the in-memory sequence counter can no longer be trusted.
// The store refuses further appends; reopen it after repairing the log.
var ErrEventLogUnreliable = errors.New("event log in unknown state after failed append; reopen the store")

func (s *Store) AppendEvent(event Event) (Event, error) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()

	if s.sequenceUnreliable {
		return Event{}, wrapError(ErrorCodeEventLog, "append event", ErrEventLogUnreliable)
	}

	candidate := s.lastSequence + 1
	event.Sequence = candidate
	event.RunID = s.layout.RunID

	encoded, err := json.Marshal(event)
	if err != nil {
		return Event{}, wrapError(ErrorCodeEventLog, "marshal event", err)
	}

	if len(encoded) > maxAppendEventBytes {
		return Event{}, wrapError(ErrorCodeEventLog, "append event",
			fmt.Errorf("event of %d bytes exceeds the %d-byte limit", len(encoded), maxAppendEventBytes))
	}

	file, err := os.OpenFile(filepath.Clean(s.layout.EventsPath), os.O_WRONLY|os.O_APPEND, persistedFileMode)
	if err != nil {
		return Event{}, wrapError(ErrorCodeEventLog, "open events log", err)
	}
	defer file.Close()

	if _, err := file.Write(append(encoded, '\n')); err != nil {
		s.resyncLastSequence(candidate)

		return Event{}, wrapError(ErrorCodeEventLog, "append event", err)
	}

	if err := file.Sync(); err != nil {
		s.resyncLastSequence(candidate)

		return Event{}, wrapError(ErrorCodeEventLog, "sync events log", err)
	}

	s.lastSequence = candidate

	return event, nil
}

// resyncLastSequence reconciles the in-memory sequence counter with the events
// file after a failed append left the on-disk state unknown (a failed sync may
// still have persisted the data). When even reading the log fails, the counter
// can no longer be trusted at all, so the store poisons itself: further
// appends fail fast with ErrEventLogUnreliable instead of risking a sequence
// gap or duplicate that replay would only discover much later.
func (s *Store) resyncLastSequence(candidate int64) {
	events, err := s.ReadEvents()
	if err != nil {
		slog.Warn("store: events log unreadable after failed append; refusing further appends",
			"run", s.layout.RunID, "sequence", candidate, "err", err)

		s.sequenceUnreliable = true

		return
	}

	recovered := int64(0)
	if len(events) > 0 {
		recovered = events[len(events)-1].Sequence
	}

	if recovered != s.lastSequence {
		slog.Warn("store: events log diverged from in-memory sequence after failed append",
			"run", s.layout.RunID, "in_memory", s.lastSequence, "on_disk", recovered)
	}

	s.lastSequence = recovered
}

func (s *Store) ReadEvents() ([]Event, error) {
	return ReadEventsFile(s.layout.EventsPath)
}

func ReadEventsFile(path string) ([]Event, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, wrapError(ErrorCodeEventLog, "open events log", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 0, eventScanInitialBufferSize)
	scanner.Buffer(buffer, eventScanMaxLineSize)

	events := make([]Event, 0)

	lineNumber := 0
	for scanner.Scan() {
		lineNumber++

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, wrapError(ErrorCodeEventLog, fmt.Sprintf("decode event line %d", lineNumber), err)
		}

		events = append(events, event)
	}

	if err := scanner.Err(); err != nil {
		return nil, wrapError(ErrorCodeEventLog, "scan events log", err)
	}

	return events, nil
}
