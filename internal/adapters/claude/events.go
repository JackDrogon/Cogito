package claude

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	shared "github.com/JackDrogon/Cogito/internal/adapters"
)

type response struct {
	Type          string         `json:"type"`
	Subtype       string         `json:"subtype"`
	Result        string         `json:"result"`
	IsError       bool           `json:"is_error"`
	StopReason    string         `json:"stop_reason"`
	SessionID     string         `json:"session_id"`
	DurationMS    int64          `json:"duration_ms"`
	DurationAPIMS int64          `json:"duration_api_ms"`
	NumTurns      int64          `json:"num_turns"`
	Raw           map[string]any `json:"-"`
}

func parseResponse(payload []byte) (*response, error) {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return nil, fmt.Errorf("empty claude response")
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, err
	}

	var parsed response
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, err
	}
	parsed.Raw = raw
	return &parsed, nil
}

func buildExecution(request shared.StartRequest, version string, response *response, stderr []byte) *shared.Execution {
	handle := shared.ExecutionHandle{
		RunID:             request.RunID,
		StepID:            request.StepID,
		AttemptID:         request.AttemptID,
		ProviderSessionID: providerSessionID(request, response),
	}

	outputText := strings.TrimSpace(response.Result)
	if outputText == "" {
		outputText = strings.TrimSpace(string(stderr))
	}

	state := shared.ExecutionStateSucceeded
	summary := strings.TrimSpace(firstLine(outputText))
	if summary == "" {
		summary = "claude execution succeeded"
	}
	if response.IsError {
		state = shared.ExecutionStateFailed
		if summary == "" {
			summary = "claude execution failed"
		}
	}

	return &shared.Execution{
		Handle:     handle,
		State:      state,
		Summary:    summary,
		OutputText: outputText,
		Logs:       buildLogs(version, response, stderr),
	}
}

func providerSessionID(request shared.StartRequest, response *response) string {
	if response != nil && strings.TrimSpace(response.SessionID) != "" {
		return strings.TrimSpace(response.SessionID)
	}

	return fmt.Sprintf("claude-%s-%s", sanitizeID(request.StepID), sanitizeID(request.AttemptID))
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

func buildLogs(version string, response *response, stderr []byte) []shared.LogEntry {
	logs := make([]shared.LogEntry, 0, 3)
	logs = append(logs, shared.LogEntry{
		Level:   "info",
		Message: "claude binary resolved",
		Fields: map[string]string{
			"provider": ProviderName,
			"version":  strings.TrimSpace(version),
		},
	})

	if response != nil {
		fields := map[string]string{}
		if strings.TrimSpace(response.Type) != "" {
			fields["type"] = strings.TrimSpace(response.Type)
		}
		if strings.TrimSpace(response.Subtype) != "" {
			fields["subtype"] = strings.TrimSpace(response.Subtype)
		}
		if strings.TrimSpace(response.StopReason) != "" {
			fields["stop_reason"] = strings.TrimSpace(response.StopReason)
		}
		if strings.TrimSpace(response.SessionID) != "" {
			fields["session_id"] = strings.TrimSpace(response.SessionID)
		}
		if response.DurationMS > 0 {
			fields["duration_ms"] = strconv.FormatInt(response.DurationMS, 10)
		}
		if response.DurationAPIMS > 0 {
			fields["duration_api_ms"] = strconv.FormatInt(response.DurationAPIMS, 10)
		}
		if response.NumTurns > 0 {
			fields["num_turns"] = strconv.FormatInt(response.NumTurns, 10)
		}

		level := "info"
		message := strings.TrimSpace(firstLine(response.Result))
		if message == "" {
			message = "claude response captured"
		}
		if response.IsError {
			level = "error"
		}

		logs = append(logs, shared.LogEntry{Level: level, Message: message, Fields: fields})
	}

	stderrText := strings.TrimSpace(string(stderr))
	if stderrText != "" {
		logs = append(logs, shared.LogEntry{Level: "info", Message: "claude stderr captured", Fields: map[string]string{"text": stderrText}})
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
