package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/JackDrogon/Cogito/internal/provider"
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
	TotalCostUSD  float64        `json:"total_cost_usd"`
	Usage         *usage         `json:"usage"`
	Raw           map[string]any `json:"-"`
}

type usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type executionParams struct {
	Request  provider.StartRequest
	Version  string
	Response *response
	Stderr   []byte
}

func parseResponse(payload []byte) (*response, error) {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return nil, errors.New("claude.parseResponse: empty response payload")
	}

	// Two decodes of the same payload are intentional: the typed struct gives
	// the fields the adapter consumes, while the map preserves EVERY field
	// (including ones the struct does not model) for diagnostics in Raw. A
	// single decode cannot produce both views.
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

func buildExecution(params executionParams) *provider.Execution {
	handle := provider.ExecutionHandle{
		RunID:             params.Request.RunID,
		StepID:            params.Request.StepID,
		AttemptID:         params.Request.AttemptID,
		ProviderSessionID: providerSessionID(params.Request, params.Response),
	}

	outputText := strings.TrimSpace(params.Response.Result)
	if outputText == "" {
		outputText = strings.TrimSpace(string(params.Stderr))
	}

	state := provider.ExecutionStateSucceeded

	summary := strings.TrimSpace(provider.FirstLine(outputText))
	if summary == "" {
		summary = "claude execution succeeded"
	}

	if params.Response.IsError {
		state = provider.ExecutionStateFailed

		if summary == "" {
			summary = "claude execution failed"
		}
	}

	return &provider.Execution{
		Handle:     handle,
		State:      state,
		Summary:    summary,
		OutputText: outputText,
		Logs:       buildLogs(params.Version, params.Response, params.Stderr),
		Usage:      responseUsage(params.Response),
	}
}

func responseUsage(response *response) *provider.Usage {
	if response == nil || response.Usage == nil {
		return nil
	}

	totalTokens := response.Usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = response.Usage.InputTokens + response.Usage.OutputTokens
	}

	return &provider.Usage{
		InputTokens:  response.Usage.InputTokens,
		OutputTokens: response.Usage.OutputTokens,
		TotalTokens:  totalTokens,
		CostUSD:      response.TotalCostUSD,
	}
}

func providerSessionID(request provider.StartRequest, response *response) string {
	if response != nil && strings.TrimSpace(response.SessionID) != "" {
		return strings.TrimSpace(response.SessionID)
	}

	return fmt.Sprintf("claude-%s-%s", provider.SanitizeID(request.StepID), provider.SanitizeID(request.AttemptID))
}

func buildLogs(version string, response *response, stderr []byte) []provider.LogEntry {
	logs := make([]provider.LogEntry, 0, 3)
	logs = append(logs, provider.LogEntry{
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

		message := strings.TrimSpace(provider.FirstLine(response.Result))
		if message == "" {
			message = "claude response captured"
		}

		if response.IsError {
			level = "error"
		}

		logs = append(logs, provider.LogEntry{Level: level, Message: message, Fields: fields})
	}

	stderrText := strings.TrimSpace(string(stderr))
	if stderrText != "" {
		logs = append(logs, provider.LogEntry{Level: "info", Message: "claude stderr captured", Fields: map[string]string{"text": stderrText}})
	}

	return logs
}
