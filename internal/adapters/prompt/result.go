// Package prompt ports AgentLoop's code-agent prompt templates and the
// AGENT_RESULT_JSON result protocol into Cogito. Adapters render BuildMain for
// agent steps and call ParseResult once at the end of an invocation to recover
// the structured AgentResult emitted on the agent's final result line.
package prompt

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoAgentResult reports that no AGENT_RESULT_JSON marker line was found.
// It is a sentinel, not a fault: callers distinguish "the agent reported
// nothing" (fall back or fail a gate) from real read/decode errors with
// errors.Is. This replaces the previous (value, bool, error) triple so every
// function stays within the project's two-return-value rule.
var ErrNoAgentResult = errors.New("no AGENT_RESULT_JSON marker found")

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
// AGENT_RESULT_JSON line:
//   - empty path or a missing file (os.IsNotExist) -> ErrNoAgentResult: the
//     agent simply produced no log, which callers treat as missing data.
//   - any other read error (EACCES, etc.) is returned as-is: a real I/O
//     fault must not be silently swallowed as "no result".
//   - an absent marker -> ErrNoAgentResult.
//   - a present-but-invalid JSON payload -> a decode error: the agent tried
//     to report but the payload is corrupt, which is a hard error rather than
//     a trivially-passing empty result.
func ParseResult(logPath string) (AgentResult, error) {
	logPath = strings.TrimSpace(logPath)
	if logPath == "" {
		return AgentResult{}, ErrNoAgentResult
	}

	data, err := os.ReadFile(filepath.Clean(logPath))
	if err != nil {
		if os.IsNotExist(err) {
			return AgentResult{}, ErrNoAgentResult
		}

		return AgentResult{}, err
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
//   - (bytes, nil) when a marker line decoded cleanly,
//   - ErrNoAgentResult when no marker line is present (caller may fall back),
//   - a decode error when a marker is present but its JSON is corrupt.
func StructuredOutputFromText(text string) (json.RawMessage, error) {
	result, err := ParseResultBytes([]byte(text))
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// ParseResultBytes scans stdout in reverse line order for the last line
// prefixed with ResultPrefix and unmarshals its JSON payload. The final marker
// wins because agents may emit intermediate retries. A missing marker yields
// ErrNoAgentResult; a present marker with an invalid payload yields a decode
// error so corrupt results are surfaced, not swallowed.
func ParseResultBytes(stdout []byte) (AgentResult, error) {
	lines := strings.Split(string(stdout), "\n")

	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !strings.HasPrefix(line, ResultPrefix) {
			continue
		}

		payload := strings.TrimSpace(line[len(ResultPrefix):])

		var result AgentResult
		if err := json.Unmarshal([]byte(payload), &result); err != nil {
			return AgentResult{}, fmt.Errorf("prompt.ParseResultBytes: decode %s payload: %w", ResultPrefix, err)
		}

		return result, nil
	}

	return AgentResult{}, ErrNoAgentResult
}
