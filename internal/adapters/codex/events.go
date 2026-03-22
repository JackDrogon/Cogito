package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	shared "github.com/JackDrogon/Cogito/internal/adapters"
)

const eventTypeError = "error"

type event struct {
	Type     string         `json:"type"`
	ThreadID string         `json:"thread_id"`
	Message  string         `json:"message"`
	Error    *eventError    `json:"error"`
	Raw      map[string]any `json:"-"`
}

type eventError struct {
	Message string `json:"message"`
}

func parseEvents(payload []byte) ([]event, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, fmt.Errorf("codex.parseEvents: empty event stream payload")
	}

	events := make([]event, 0, 8)
	reader := bufio.NewReader(bytes.NewReader(payload))

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				var raw map[string]any
				if err := json.Unmarshal(trimmed, &raw); err != nil {
					return nil, err
				}

				var parsed event
				if err := json.Unmarshal(trimmed, &parsed); err != nil {
					return nil, err
				}

				parsed.Raw = raw
				events = append(events, parsed)
			}
		}

		if err == nil {
			continue
		}

		if err == io.EOF {
			break
		}

		return nil, err
	}

	if len(events) == 0 {
		return nil, fmt.Errorf("codex.parseEvents: no valid events parsed")
	}

	return events, nil
}

func buildExecution(request shared.StartRequest, version string, events []event, lastMessage, stderr []byte) *shared.Execution {
	handle := shared.ExecutionHandle{
		RunID:             request.RunID,
		StepID:            request.StepID,
		AttemptID:         request.AttemptID,
		ProviderSessionID: providerSessionID(request, events),
	}

	messageText := strings.TrimSpace(string(lastMessage))
	errorMessage := eventErrorMessage(events)
	outputText := messageText

	if outputText == "" {
		outputText = strings.TrimSpace(errorMessage)
	}

	if outputText == "" {
		outputText = strings.TrimSpace(string(stderr))
	}

	state := shared.ExecutionStateSucceeded

	summary := strings.TrimSpace(firstLine(messageText))
	if summary == "" {
		summary = "codex execution succeeded"
	}

	if errorMessage != "" {
		state = shared.ExecutionStateFailed
		summary = errorMessage
	}

	return &shared.Execution{
		Handle:     handle,
		State:      state,
		Summary:    summary,
		OutputText: outputText,
		Logs:       buildLogs(version, events, stderr),
	}
}

func providerSessionID(request shared.StartRequest, events []event) string {
	for _, event := range events {
		if strings.TrimSpace(event.ThreadID) != "" {
			return strings.TrimSpace(event.ThreadID)
		}
	}

	return fmt.Sprintf("codex-%s-%s", sanitizeID(request.StepID), sanitizeID(request.AttemptID))
}

func sanitizeID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}

	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, "/", "-")

	return value
}

func eventErrorMessage(events []event) string {
	for _, event := range events {
		if event.Error != nil && strings.TrimSpace(event.Error.Message) != "" {
			return strings.TrimSpace(event.Error.Message)
		}

		if event.Type == eventTypeError && strings.TrimSpace(event.Message) != "" {
			return strings.TrimSpace(event.Message)
		}
	}

	return ""
}

func buildLogs(version string, events []event, stderr []byte) []shared.LogEntry {
	logs := make([]shared.LogEntry, 0, len(events)+2)
	logs = append(logs, shared.LogEntry{
		Level:   "info",
		Message: "codex binary resolved",
		Fields: map[string]string{
			"provider": ProviderName,
			"version":  strings.TrimSpace(version),
		},
	})

	for _, event := range events {
		fields := map[string]string{"type": event.Type}
		if strings.TrimSpace(event.ThreadID) != "" {
			fields["thread_id"] = strings.TrimSpace(event.ThreadID)
		}

		level := "info"

		message := strings.TrimSpace(event.Message)
		if message == "" {
			message = event.Type
		}

		if event.Error != nil && strings.TrimSpace(event.Error.Message) != "" {
			level = "error"
			message = strings.TrimSpace(event.Error.Message)
		}

		if event.Type == eventTypeError {
			level = eventTypeError
		}

		logs = append(logs, shared.LogEntry{Level: level, Message: message, Fields: fields})
	}

	stderrText := strings.TrimSpace(string(stderr))
	if stderrText != "" {
		logs = append(logs, shared.LogEntry{Level: "info", Message: "codex stderr captured", Fields: map[string]string{"text": stderrText}})
	}

	return logs
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		return strings.TrimSpace(value[:idx])
	}

	return value
}
