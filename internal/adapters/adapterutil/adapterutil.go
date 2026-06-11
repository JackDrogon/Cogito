// Package adapterutil holds the request validation and deep-copy helpers shared
// by every provider adapter (codex, claude, opencode). These were previously
// copy-pasted verbatim into each provider subpackage, so any protocol field
// addition required three identical edits. Centralizing them here keeps the
// provider subpackages focused on their own argv/parse/event logic.
package adapterutil

import (
	"encoding/json"
	"strings"

	shared "github.com/JackDrogon/Cogito/internal/adapters"
)

func requestError(message string) *shared.Error {
	return &shared.Error{Code: shared.ErrorCodeRequest, Message: message}
}

// ValidateStartRequest checks that a StartRequest carries the run/step/attempt
// identifiers every adapter needs before launching a provider invocation.
func ValidateStartRequest(request shared.StartRequest) error {
	if strings.TrimSpace(request.RunID) == "" {
		return requestError("run id is required")
	}

	if strings.TrimSpace(request.StepID) == "" {
		return requestError("step id is required")
	}

	if strings.TrimSpace(request.AttemptID) == "" {
		return requestError("attempt id is required")
	}

	return nil
}

// ValidateHandle checks that an ExecutionHandle is fully populated, including
// the provider session id used to look up a live or resumable session.
func ValidateHandle(handle shared.ExecutionHandle) error {
	if strings.TrimSpace(handle.RunID) == "" {
		return requestError("run id is required")
	}

	if strings.TrimSpace(handle.StepID) == "" {
		return requestError("step id is required")
	}

	if strings.TrimSpace(handle.AttemptID) == "" {
		return requestError("attempt id is required")
	}

	if strings.TrimSpace(handle.ProviderSessionID) == "" {
		return requestError("provider session id is required")
	}

	return nil
}

// CloneExecution returns a deep copy of an Execution so callers can hand out a
// terminal result without exposing the adapter's cached copy to mutation.
func CloneExecution(execution *shared.Execution) *shared.Execution {
	if execution == nil {
		return nil
	}

	return &shared.Execution{
		Handle:           execution.Handle,
		State:            execution.State,
		Summary:          execution.Summary,
		OutputText:       execution.OutputText,
		StructuredOutput: CloneJSON(execution.StructuredOutput),
		ArtifactRefs:     CloneArtifactRefs(execution.ArtifactRefs),
		Logs:             CloneLogs(execution.Logs),
	}
}

// CloneJSON deep-copies a json.RawMessage, preserving nil.
func CloneJSON(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}

	cloned := make(json.RawMessage, len(value))
	copy(cloned, value)

	return cloned
}

// CloneArtifactRefs deep-copies an ArtifactRef slice, preserving nil.
func CloneArtifactRefs(artifacts []shared.ArtifactRef) []shared.ArtifactRef {
	if artifacts == nil {
		return nil
	}

	cloned := make([]shared.ArtifactRef, 0, len(artifacts))
	cloned = append(cloned, artifacts...)

	return cloned
}

// CloneLogs deep-copies a LogEntry slice including each entry's Fields map.
func CloneLogs(logs []shared.LogEntry) []shared.LogEntry {
	if logs == nil {
		return nil
	}

	cloned := make([]shared.LogEntry, 0, len(logs))

	for _, entry := range logs {
		clonedEntry := shared.LogEntry{Level: entry.Level, Message: entry.Message}
		if entry.Fields != nil {
			clonedEntry.Fields = make(map[string]string, len(entry.Fields))
			for key, value := range entry.Fields {
				clonedEntry.Fields[key] = value
			}
		}

		cloned = append(cloned, clonedEntry)
	}

	return cloned
}
