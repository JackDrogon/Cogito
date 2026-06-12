package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/JackDrogon/Cogito/internal/provider"
)

const eventTypeError = "error"

type event struct {
	Type     string         `json:"type"`
	ThreadID string         `json:"thread_id"`
	Message  string         `json:"message"`
	Error    *eventError    `json:"error"`
	Usage    *eventUsage    `json:"usage"`
	Raw      map[string]any `json:"-"`
}

type eventError struct {
	Message string `json:"message"`
}

type eventUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
}

type executionParams struct {
	Request     provider.StartRequest
	Version     string
	Events      []event
	LastMessage []byte
	Stderr      []byte
}

func parseEvents(payload []byte) ([]event, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, errors.New("codex.parseEvents: empty event stream payload")
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

		if errors.Is(err, io.EOF) {
			break
		}

		return nil, err
	}

	if len(events) == 0 {
		return nil, errors.New("codex.parseEvents: no valid events parsed")
	}

	return events, nil
}

func buildExecution(params executionParams) *provider.Execution {
	handle := provider.ExecutionHandle{
		RunID:             params.Request.RunID,
		StepID:            params.Request.StepID,
		AttemptID:         params.Request.AttemptID,
		ProviderSessionID: providerSessionID(params.Request, params.Events),
	}

	messageText := strings.TrimSpace(string(params.LastMessage))
	errorMessage := eventErrorMessage(params.Events)
	outputText := messageText

	if outputText == "" {
		outputText = strings.TrimSpace(errorMessage)
	}

	if outputText == "" {
		outputText = strings.TrimSpace(string(params.Stderr))
	}

	state := provider.ExecutionStateSucceeded

	summary := strings.TrimSpace(provider.FirstLine(messageText))
	if summary == "" {
		summary = "codex execution succeeded"
	}

	if errorMessage != "" {
		state = provider.ExecutionStateFailed
		summary = errorMessage
	}

	return &provider.Execution{
		Handle:     handle,
		State:      state,
		Summary:    summary,
		OutputText: outputText,
		Logs:       buildLogs(params.Version, params.Events, params.Stderr),
		Usage:      usageFromEvents(params.Events),
	}
}

func usageFromEvents(events []event) *provider.Usage {
	var latest *eventUsage

	for index := range events {
		if events[index].Usage != nil {
			latest = events[index].Usage
		}
	}

	if latest == nil {
		return nil
	}

	return &provider.Usage{
		InputTokens:  latest.InputTokens,
		OutputTokens: latest.OutputTokens,
		TotalTokens:  latest.InputTokens + latest.OutputTokens,
	}
}

func providerSessionID(request provider.StartRequest, events []event) string {
	// Walk every event and keep the LAST non-empty thread_id. When logs are
	// concatenated or the session was rotated mid-run, the most recent thread
	// id is the one `resume <sid>` must target; returning the first match would
	// re-attach to a stale session.
	latest := ""

	for _, event := range events {
		if threadID := strings.TrimSpace(event.ThreadID); threadID != "" {
			latest = threadID
		}
	}

	if latest != "" {
		return latest
	}

	return fmt.Sprintf("codex-%s-%s", provider.SanitizeID(request.StepID), provider.SanitizeID(request.AttemptID))
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

func buildLogs(version string, events []event, stderr []byte) []provider.LogEntry {
	logs := make([]provider.LogEntry, 0, len(events)+2)
	logs = append(logs, provider.LogEntry{
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

		logs = append(logs, provider.LogEntry{Level: level, Message: message, Fields: fields})
	}

	stderrText := strings.TrimSpace(string(stderr))
	if stderrText != "" {
		logs = append(logs, provider.LogEntry{Level: "info", Message: "codex stderr captured", Fields: map[string]string{"text": stderrText}})
	}

	return logs
}
