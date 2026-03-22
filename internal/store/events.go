package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (s *Store) AppendEvent(event Event) (Event, error) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()

	s.lastSequence++
	event.Sequence = s.lastSequence
	event.RunID = s.layout.RunID

	encoded, err := json.Marshal(event)
	if err != nil {
		s.lastSequence--
		return Event{}, wrapError(ErrorCodeEventLog, "marshal event", err)
	}

	file, err := os.OpenFile(filepath.Clean(s.layout.EventsPath), os.O_WRONLY|os.O_APPEND, persistedFileMode)
	if err != nil {
		s.lastSequence--
		return Event{}, wrapError(ErrorCodeEventLog, "open events log", err)
	}
	defer file.Close()

	if _, err := file.Write(append(encoded, '\n')); err != nil {
		s.lastSequence--
		return Event{}, wrapError(ErrorCodeEventLog, "append event", err)
	}

	if err := file.Sync(); err != nil {
		s.lastSequence--
		return Event{}, wrapError(ErrorCodeEventLog, "sync events log", err)
	}

	return event, nil
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
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 1024*1024)

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
