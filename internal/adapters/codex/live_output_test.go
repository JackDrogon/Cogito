package codex

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestLiveRendererRendersHumanReadableLines(t *testing.T) {
	var sink bytes.Buffer
	renderer := newLiveRenderer(&sink)

	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"ls -la"}}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"ls -la","aggregated_output":"total 0\n","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"reasoning","text":"**Planning the change**"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"All done."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}}`,
	}, "\n") + "\n"

	if _, err := renderer.Write([]byte(stream)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := sink.String()
	for _, want := range []string{
		"[codex] thread thread-1",
		"[codex] $ ls -la",
		"total 0",
		"[codex] exit 0",
		"[codex] thinking: **Planning the change**",
		"[codex] agent:\nAll done.",
		"[codex] tokens: input=100 (cached 40) output=7",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q:\n%s", want, got)
		}
	}

	if strings.Contains(got, `{"type":`) {
		t.Errorf("rendered output leaked raw JSON:\n%s", got)
	}
	if strings.Contains(got, "turn.started") {
		t.Errorf("rendered output should skip progress noise:\n%s", got)
	}
}

func TestLiveRendererHandlesChunkSplitLines(t *testing.T) {
	var sink bytes.Buffer
	renderer := newLiveRenderer(&sink)

	line := `{"type":"item.completed","item":{"type":"agent_message","text":"split across chunks"}}` + "\n"
	half := len(line) / 2

	for _, chunk := range []string{line[:half], line[half:]} {
		if _, err := renderer.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	if got := sink.String(); !strings.Contains(got, "split across chunks") {
		t.Errorf("rendered output = %q, want agent message text", got)
	}
}

func TestLiveRendererPassesThroughNonJSON(t *testing.T) {
	var sink bytes.Buffer
	renderer := newLiveRenderer(&sink)

	if _, err := renderer.Write([]byte("plain stderr warning\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := sink.String(); !strings.Contains(got, "[codex] plain stderr warning\n") {
		t.Errorf("passthrough = %q, want timestamped marker line", got)
	}
}

// timestampPrefixPattern matches the "YYYY-MM-DD HH:MM:SS [codex] " prefix
// every marker line must carry.
var timestampPrefixPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \[codex\] `)

func TestLiveRendererMarkerLinesCarryDateTimestamp(t *testing.T) {
	var sink bytes.Buffer
	renderer := newLiveRenderer(&sink)

	stream := `{"type":"thread.started","thread_id":"thread-1"}` + "\n" +
		`{"type":"item.completed","item":{"type":"agent_message","text":"content line"}}` + "\n"

	if _, err := renderer.Write([]byte(stream)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	lines := strings.Split(strings.TrimRight(sink.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3 (thread, agent marker, content):\n%s", len(lines), sink.String())
	}

	for _, markerLine := range []string{lines[0], lines[1]} {
		if !timestampPrefixPattern.MatchString(markerLine) {
			t.Errorf("marker line %q missing date timestamp prefix", markerLine)
		}
	}

	// Content lines stay raw so multi-line payloads remain copy-pasteable.
	if lines[2] != "content line" {
		t.Errorf("content line = %q, want raw text without prefix", lines[2])
	}
}

func TestLiveRendererRendersErrors(t *testing.T) {
	var sink bytes.Buffer
	renderer := newLiveRenderer(&sink)

	stream := `{"type":"turn.failed","error":{"message":"rate limited"}}` + "\n" +
		`{"type":"error","message":"stream disconnected"}` + "\n"

	if _, err := renderer.Write([]byte(stream)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := sink.String()
	if !strings.Contains(got, "[codex] error: rate limited") {
		t.Errorf("rendered output missing turn.failed message:\n%s", got)
	}
	if !strings.Contains(got, "[codex] error: stream disconnected") {
		t.Errorf("rendered output missing error message:\n%s", got)
	}
}

func TestNewLiveRendererNilSinkYieldsNil(t *testing.T) {
	if got := newLiveRenderer(nil); got != nil {
		t.Fatalf("newLiveRenderer(nil) = %v, want nil", got)
	}
}
