package prompt

import (
	"encoding/json"
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

	got, found, err := ParseResultBytes([]byte(log))
	if err != nil {
		t.Fatalf("ParseResultBytes error = %v", err)
	}
	if !found {
		t.Fatal("ParseResultBytes found = false, want true")
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
// (nil/false/nil) from "corrupt marker" (nil/false/err).
func TestStructuredOutputFromText(t *testing.T) {
	t.Run("marker found", func(t *testing.T) {
		text := "Work complete.\n" + ResultPrefix + ` {"commits":["abc1234"],"summary":"done"}`

		raw, found, err := StructuredOutputFromText(text)
		if err != nil {
			t.Fatalf("StructuredOutputFromText error = %v", err)
		}
		if !found {
			t.Fatal("StructuredOutputFromText found = false, want true")
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
		raw, found, err := StructuredOutputFromText("just normalized prose, no marker")
		if err != nil || found || raw != nil {
			t.Fatalf("StructuredOutputFromText = (%s, %v, %v), want (nil, false, nil)", string(raw), found, err)
		}
	})

	t.Run("corrupt marker", func(t *testing.T) {
		raw, found, err := StructuredOutputFromText(ResultPrefix + `{not json`)
		if err == nil {
			t.Fatal("StructuredOutputFromText error = nil, want decode error")
		}
		if found || raw != nil {
			t.Fatalf("StructuredOutputFromText = (%s, %v), want (nil, false) on corrupt marker", string(raw), found)
		}
	})
}

func TestParseResultBytesMissingMarker(t *testing.T) {
	got, found, err := ParseResultBytes([]byte("no result line here\njust logs\n"))
	if err != nil {
		t.Fatalf("ParseResultBytes error = %v", err)
	}
	if found {
		t.Fatal("ParseResultBytes found = true, want false on missing marker")
	}

	if !reflect.DeepEqual(got, AgentResult{}) {
		t.Fatalf("ParseResultBytes = %+v, want zero value", got)
	}
}

// TestParseResultBytesInvalidJSONErrors verifies a present-but-corrupt payload
// surfaces an error and found=false rather than being swallowed as an empty
// result (Oracle finding #4).
func TestParseResultBytesInvalidJSONErrors(t *testing.T) {
	log := ResultPrefix + `{not valid json`

	got, found, err := ParseResultBytes([]byte(log))
	if err == nil {
		t.Fatal("ParseResultBytes error = nil, want decode error on invalid JSON")
	}
	if found {
		t.Fatal("ParseResultBytes found = true, want false on invalid JSON")
	}

	if !reflect.DeepEqual(got, AgentResult{}) {
		t.Fatalf("ParseResultBytes = %+v, want zero value on invalid JSON", got)
	}
}

func TestParseResultBytesEmptyArrays(t *testing.T) {
	log := ResultPrefix + `{"completed":[],"blocked":[],"deferred":[],"toolchain_bugs":[],"commits":[],"verification":[],"summary":""}`

	got, found, err := ParseResultBytes([]byte(log))
	if err != nil {
		t.Fatalf("ParseResultBytes error = %v", err)
	}
	if !found {
		t.Fatal("ParseResultBytes found = false, want true")
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

	got, found, err := ParseResult(logPath)
	if err != nil {
		t.Fatalf("ParseResult error = %v", err)
	}
	if !found {
		t.Fatal("ParseResult found = false, want true")
	}

	want := AgentResult{Commits: []string{"deadbeef"}, Summary: "ok"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseResult = %+v, want %+v", got, want)
	}
}

func TestParseResultMissingFileIsNotFound(t *testing.T) {
	got, found, err := ParseResult(filepath.Join(t.TempDir(), "does-not-exist.log"))
	if err != nil {
		t.Fatalf("ParseResult error = %v, want nil for missing file", err)
	}
	if found {
		t.Fatal("ParseResult found = true, want false for missing file")
	}

	if !reflect.DeepEqual(got, AgentResult{}) {
		t.Fatalf("ParseResult = %+v, want zero value", got)
	}
}

func TestParseResultEmptyPathIsNotFound(t *testing.T) {
	got, found, err := ParseResult("")
	if err != nil {
		t.Fatalf("ParseResult error = %v", err)
	}
	if found {
		t.Fatal("ParseResult found = true, want false for empty path")
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
	if _, _, err := ParseResult(dir); err == nil {
		t.Fatal("ParseResult error = nil, want read error for directory path")
	}
}
