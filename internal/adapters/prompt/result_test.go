package prompt

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseResultBytesTakesLastMarker(t *testing.T) {
	log := strings.Join([]string{
		"some preamble",
		ResultPrefix + `{"completed":["old"],"summary":"first"}`,
		"intermediate noise",
		ResultPrefix + `{"completed":["a","b"],"blocked":["c"],"commits":["abc1234"],"verification":["go test ./..."],"summary":"done"}`,
		"trailing line",
	}, "\n")

	got, err := ParseResultBytes([]byte(log))
	if err != nil {
		t.Fatalf("ParseResultBytes error = %v", err)
	}

	want := AgentResult{
		Completed:    []string{"a", "b"},
		Blocked:      []string{"c"},
		Commits:      []string{"abc1234"},
		Verification: []string{"go test ./..."},
		Summary:      "done",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseResultBytes = %+v, want %+v", got, want)
	}
}

// TestStructuredOutputFromText is v3.2 N1: the helper extracts and re-marshals
// the AgentResult from normalized assistant text, distinguishing "no marker"
// (ErrNoAgentResult) from "corrupt marker" (decode error).
func TestStructuredOutputFromText(t *testing.T) {
	t.Run("marker found", func(t *testing.T) {
		text := "Work complete.\n" + ResultPrefix + ` {"commits":["abc1234"],"summary":"done"}`

		raw, err := StructuredOutputFromText(text)
		if err != nil {
			t.Fatalf("StructuredOutputFromText error = %v", err)
		}

		var got AgentResult
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("re-decode structured output: %v", err)
		}
		if !reflect.DeepEqual(got, AgentResult{Commits: []string{"abc1234"}, Summary: "done"}) {
			t.Fatalf("StructuredOutputFromText = %+v", got)
		}
	})

	t.Run("no marker", func(t *testing.T) {
		raw, err := StructuredOutputFromText("just normalized prose, no marker")
		if !errors.Is(err, ErrNoAgentResult) || raw != nil {
			t.Fatalf("StructuredOutputFromText = (%s, %v), want (nil, ErrNoAgentResult)", string(raw), err)
		}
	})

	t.Run("corrupt marker", func(t *testing.T) {
		raw, err := StructuredOutputFromText(ResultPrefix + `{not json`)
		if err == nil {
			t.Fatal("StructuredOutputFromText error = nil, want decode error")
		}
		if errors.Is(err, ErrNoAgentResult) {
			t.Fatalf("StructuredOutputFromText error = %v, want a decode error, not ErrNoAgentResult", err)
		}
		if raw != nil {
			t.Fatalf("StructuredOutputFromText = %s, want nil on corrupt marker", string(raw))
		}
	})
}

func TestParseResultBytesMissingMarker(t *testing.T) {
	got, err := ParseResultBytes([]byte("no result line here\njust logs\n"))
	if !errors.Is(err, ErrNoAgentResult) {
		t.Fatalf("ParseResultBytes error = %v, want ErrNoAgentResult on missing marker", err)
	}

	if !reflect.DeepEqual(got, AgentResult{}) {
		t.Fatalf("ParseResultBytes = %+v, want zero value", got)
	}
}

// TestParseResultBytesInvalidJSONErrors verifies a present-but-corrupt payload
// surfaces a decode error (distinct from ErrNoAgentResult) rather than being
// swallowed as an empty result (Oracle finding #4).
func TestParseResultBytesInvalidJSONErrors(t *testing.T) {
	log := ResultPrefix + `{not valid json`

	got, err := ParseResultBytes([]byte(log))
	if err == nil {
		t.Fatal("ParseResultBytes error = nil, want decode error on invalid JSON")
	}
	if errors.Is(err, ErrNoAgentResult) {
		t.Fatalf("ParseResultBytes error = %v, want a decode error, not ErrNoAgentResult", err)
	}

	if !reflect.DeepEqual(got, AgentResult{}) {
		t.Fatalf("ParseResultBytes = %+v, want zero value on invalid JSON", got)
	}
}

func TestParseResultBytesEmptyArrays(t *testing.T) {
	log := ResultPrefix + `{"completed":[],"blocked":[],"deferred":[],"toolchain_bugs":[],"commits":[],"verification":[],"summary":""}`

	got, err := ParseResultBytes([]byte(log))
	if err != nil {
		t.Fatalf("ParseResultBytes error = %v", err)
	}

	want := AgentResult{
		Completed:     []string{},
		Blocked:       []string{},
		Deferred:      []string{},
		ToolchainBugs: []string{},
		Commits:       []string{},
		Verification:  []string{},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseResultBytes = %+v, want %+v", got, want)
	}
}

func TestParseResultReadsFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	content := "preamble\n" + ResultPrefix + `{"commits":["deadbeef"],"summary":"ok"}` + "\n"

	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	got, err := ParseResult(logPath)
	if err != nil {
		t.Fatalf("ParseResult error = %v", err)
	}

	want := AgentResult{Commits: []string{"deadbeef"}, Summary: "ok"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseResult = %+v, want %+v", got, want)
	}
}

func TestParseResultMissingFileIsNotFound(t *testing.T) {
	got, err := ParseResult(filepath.Join(t.TempDir(), "does-not-exist.log"))
	if !errors.Is(err, ErrNoAgentResult) {
		t.Fatalf("ParseResult error = %v, want ErrNoAgentResult for missing file", err)
	}

	if !reflect.DeepEqual(got, AgentResult{}) {
		t.Fatalf("ParseResult = %+v, want zero value", got)
	}
}

func TestParseResultEmptyPathIsNotFound(t *testing.T) {
	got, err := ParseResult("")
	if !errors.Is(err, ErrNoAgentResult) {
		t.Fatalf("ParseResult error = %v, want ErrNoAgentResult for empty path", err)
	}

	if !reflect.DeepEqual(got, AgentResult{}) {
		t.Fatalf("ParseResult = %+v, want zero value", got)
	}
}

// TestParseResultUnreadableFileErrors verifies a real I/O fault (here: a
// directory in place of a file) surfaces an error rather than being swallowed
// as "no result" (Oracle finding #4).
func TestParseResultUnreadableFileErrors(t *testing.T) {
	dir := t.TempDir()

	_, err := ParseResult(dir)
	if err == nil {
		t.Fatal("ParseResult error = nil, want read error for directory path")
	}

	if errors.Is(err, ErrNoAgentResult) {
		t.Fatalf("ParseResult error = %v, want a real I/O error, not ErrNoAgentResult", err)
	}
}
