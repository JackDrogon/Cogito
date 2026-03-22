package opencode

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	shared "github.com/JackDrogon/Cogito/internal/adapters"
)

type response struct {
	Raw map[string]any
}

func parseResponse(payload []byte) (*response, error) {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return nil, fmt.Errorf("empty opencode response")
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, err
	}

	return &response{Raw: raw}, nil
}

func buildExecution(request shared.StartRequest, version string, response *response, stderr []byte) *shared.Execution {
	handle := shared.ExecutionHandle{
		RunID:             request.RunID,
		StepID:            request.StepID,
		AttemptID:         request.AttemptID,
		ProviderSessionID: providerSessionID(request, response),
	}

	outputText := strings.TrimSpace(firstNonEmpty(
		response.string("output_text"),
		response.string("outputText"),
		response.string("text"),
		response.string("message"),
		response.string("summary"),
		strings.TrimSpace(string(stderr)),
	))

	failedMessage := strings.TrimSpace(firstNonEmpty(
		response.string("error"),
		response.nestedString("error", "message"),
	))

	state := shared.ExecutionStateSucceeded
	summary := strings.TrimSpace(firstNonEmpty(response.string("summary"), firstLine(outputText), "opencode adapter passed"))
	if response.failed() || failedMessage != "" {
		state = shared.ExecutionStateFailed
		if failedMessage != "" {
			summary = failedMessage
		} else if summary == "" {
			summary = "opencode execution failed"
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
	if response != nil {
		if sessionID := strings.TrimSpace(firstNonEmpty(response.string("session_id"), response.string("sessionId"))); sessionID != "" {
			return sessionID
		}
	}

	return fmt.Sprintf("opencode-%s-%s", sanitizeID(request.StepID), sanitizeID(request.AttemptID))
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
	logs := make([]shared.LogEntry, 0, 4)
	logs = append(logs, shared.LogEntry{
		Level:   "info",
		Message: "opencode binary resolved",
		Fields: map[string]string{
			"provider": ProviderName,
			"version":  strings.TrimSpace(version),
		},
	})

	if response != nil {
		for _, entry := range response.logEntries() {
			logs = append(logs, entry)
		}
		if len(logs) == 1 {
			fields := map[string]string{}
			if sessionID := strings.TrimSpace(firstNonEmpty(response.string("session_id"), response.string("sessionId"))); sessionID != "" {
				fields["session_id"] = sessionID
			}
			if messageCount := response.intString("message_count"); messageCount != "" {
				fields["message_count"] = messageCount
			}

			message := strings.TrimSpace(firstNonEmpty(response.string("summary"), firstLine(response.string("output_text")), "opencode response captured"))
			logs = append(logs, shared.LogEntry{Level: "info", Message: message, Fields: fields})
		}
	}

	if stderrText := strings.TrimSpace(string(stderr)); stderrText != "" {
		logs = append(logs, shared.LogEntry{Level: "info", Message: "opencode stderr captured", Fields: map[string]string{"text": stderrText}})
	}

	return logs
}

func (r *response) string(key string) string {
	if r == nil || r.Raw == nil {
		return ""
	}

	value, ok := r.Raw[key]
	if !ok {
		return ""
	}

	text, ok := value.(string)
	if !ok {
		return ""
	}

	return strings.TrimSpace(text)
}

func (r *response) nestedString(key string, nested string) string {
	if r == nil || r.Raw == nil {
		return ""
	}

	value, ok := r.Raw[key]
	if !ok {
		return ""
	}

	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}

	text, ok := object[nested].(string)
	if !ok {
		return ""
	}

	return strings.TrimSpace(text)
}

func (r *response) intString(key string) string {
	if r == nil || r.Raw == nil {
		return ""
	}

	value, ok := r.Raw[key]
	if !ok {
		return ""
	}

	switch typed := value.(type) {
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func (r *response) failed() bool {
	if r == nil || r.Raw == nil {
		return false
	}

	if value, ok := r.Raw["success"]; ok {
		if success, ok := value.(bool); ok {
			return !success
		}
	}

	status := strings.ToLower(strings.TrimSpace(firstNonEmpty(r.string("status"), r.string("state"))))
	return status == "failed" || status == "error"
}

func (r *response) logEntries() []shared.LogEntry {
	if r == nil || r.Raw == nil {
		return nil
	}

	value, ok := r.Raw["logs"]
	if !ok {
		return nil
	}

	rawEntries, ok := value.([]any)
	if !ok {
		return nil
	}

	entries := make([]shared.LogEntry, 0, len(rawEntries))
	for _, rawEntry := range rawEntries {
		object, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}

		entry := shared.LogEntry{
			Level:   strings.TrimSpace(stringValue(object["level"])),
			Message: strings.TrimSpace(stringValue(object["message"])),
		}
		if entry.Level == "" {
			entry.Level = "info"
		}
		if entry.Message == "" {
			entry.Message = "opencode log captured"
		}

		if fields, ok := object["fields"].(map[string]any); ok {
			entry.Fields = make(map[string]string, len(fields))
			for key, value := range fields {
				if text := stringValue(value); text != "" {
					entry.Fields[key] = text
				}
			}
		}

		entries = append(entries, entry)
	}

	return entries
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}

	return ""
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
