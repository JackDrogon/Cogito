package provider

import (
	"encoding/json"
	"strings"

	"github.com/JackDrogon/Cogito/internal/prompt"
)

// StructuredOutputFromLog recovers the agent's AGENT_RESULT_JSON payload from a
// finished invocation and returns it as the normalized StructuredOutput bytes.
//
// It prefers the streamed log file (a stdout+stderr superset) when logPath is
// set and falls back to the captured stdout otherwise. The parsed AgentResult is
// re-marshaled so downstream runtime drivers consume a stable, normalized JSON
// shape via json.Unmarshal rather than re-scanning raw logs.
//
// Outcomes (so callers never silently swallow a real fault, N2):
//   - (bytes, nil): a valid AGENT_RESULT_JSON marker decoded cleanly.
//   - prompt.ErrNoAgentResult: the agent emitted no marker at all (a clean
//     "no data" that downstream verify/commit_check drivers treat as missing
//     structured output, failing the gate rather than trivially passing it).
//   - any other error: a read fault (EACCES, etc.) or a present-but-corrupt
//     JSON payload. Adapters must surface this as a build error, not "empty".
func StructuredOutputFromLog(logPath string, stdout []byte) (json.RawMessage, error) {
	var (
		result prompt.AgentResult
		err    error
	)

	if strings.TrimSpace(logPath) != "" {
		result, err = prompt.ParseResult(logPath)
	} else {
		result, err = prompt.ParseResultBytes(stdout)
	}

	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}

	return data, nil
}
