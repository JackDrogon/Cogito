// Package prompt ports AgentLoop's code-agent prompt templates and the
// AGENT_RESULT_JSON result protocol into Cogito. Adapters render BuildMain for
// agent steps and call ParseResult once at the end of an invocation to recover
// the structured AgentResult emitted on the agent's final result line.
package prompt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResultPrefix is the single-line marker the agent must emit immediately before
// its JSON result payload. Ported verbatim from AgentLoop config.ResultPrefix.
const ResultPrefix = "AGENT_RESULT_JSON:"

// AgentResult is the structured result an agent emits on the AGENT_RESULT_JSON
// line. It mirrors AgentLoop's runner.AgentResult shape and is the normalized
// payload that adapters marshal into Execution.StructuredOutput. Downstream
// runtime drivers consume it via json.Unmarshal of the persisted bytes, never
// by re-scanning logs.
type AgentResult struct {
	Completed     []string `json:"completed"`
	Blocked       []string `json:"blocked"`
	Deferred      []string `json:"deferred"`
	ToolchainBugs []string `json:"toolchain_bugs"`
	Commits       []string `json:"commits"`
	Verification  []string `json:"verification"`
	Summary       string   `json:"summary"`
}

// ParseResult reads logPath and returns the AgentResult parsed from the last
// AGENT_RESULT_JSON line. The bool reports whether a valid marker line was
// found and decoded:
//   - empty path or a missing file (os.IsNotExist) -> (zero, false, nil): the
//     agent simply produced no log, which callers treat as missing data.
//   - any other read error (EACCES, etc.) -> (zero, false, err): a real I/O
//     fault must not be silently swallowed as "no result".
//   - an absent marker -> (zero, false, nil).
//   - a present-but-invalid JSON payload -> (zero, false, err): the agent tried
//     to report but the payload is corrupt, which is a hard error rather than a
//     trivially-passing empty result.
func ParseResult(logPath string) (AgentResult, bool, error) {
	logPath = strings.TrimSpace(logPath)
	if logPath == "" {
		return AgentResult{}, false, nil
	}

	data, err := os.ReadFile(filepath.Clean(logPath))
	if err != nil {
		if os.IsNotExist(err) {
			return AgentResult{}, false, nil
		}

		return AgentResult{}, false, err
	}

	return ParseResultBytes(data)
}

// StructuredOutputFromText scans normalized assistant text (the provider's
// already-parsed final message — codex lastMessage, claude response.Result,
// opencode OutputText) for the last AGENT_RESULT_JSON marker and returns the
// AgentResult re-marshaled as normalized StructuredOutput bytes.
//
// Real providers wrap the marker inside their own JSON envelope, so the marker
// surfaces in the normalized text rather than the raw log/stdout stream that
// StructuredOutputFromLog scans. Returns:
//   - (bytes, true, nil) when a marker line decoded cleanly,
//   - (nil, false, nil) when no marker line is present (caller may fall back),
//   - (nil, false, err) when a marker is present but its JSON is corrupt.
func StructuredOutputFromText(text string) (json.RawMessage, bool, error) {
	result, found, err := ParseResultBytes([]byte(text))
	if err != nil {
		return nil, false, err
	}

	if !found {
		return nil, false, nil
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, false, err
	}

	return data, true, nil
}

// ParseResultBytes scans stdout in reverse line order for the last line
// prefixed with ResultPrefix and unmarshals its JSON payload. The final marker
// wins because agents may emit intermediate retries. A missing marker yields
// (zero, false, nil); a present marker with an invalid payload yields
// (zero, false, err) so corrupt results are surfaced, not swallowed.
func ParseResultBytes(stdout []byte) (AgentResult, bool, error) {
	lines := strings.Split(string(stdout), "\n")

	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !strings.HasPrefix(line, ResultPrefix) {
			continue
		}

		payload := strings.TrimSpace(line[len(ResultPrefix):])

		var result AgentResult
		if err := json.Unmarshal([]byte(payload), &result); err != nil {
			return AgentResult{}, false, fmt.Errorf("prompt.ParseResultBytes: decode %s payload: %w", ResultPrefix, err)
		}

		return result, true, nil
	}

	return AgentResult{}, false, nil
}
