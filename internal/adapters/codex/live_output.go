package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// liveTimeLayout is the console timestamp prefix for live output lines; it
// matches the [verbose] event replay format for visual alignment.
const liveTimeLayout = "2006-01-02 15:04:05"

// liveRenderer turns the codex --json NDJSON event stream into human-readable
// console lines for verbose live output. The durable provider log keeps the
// raw NDJSON (written separately by the runner); this renderer only feeds
// human eyes, so unknown shapes degrade to quiet no-ops and non-JSON lines
// (e.g. stderr noise) pass through unchanged.
//
// It implements io.Writer over a byte stream: chunks are buffered until a
// full line is available, then each line is rendered. Writes are serialized
// because the runner tees stdout and stderr from two goroutines.
type liveRenderer struct {
	mu   sync.Mutex
	sink io.Writer
	buf  bytes.Buffer
	now  func() time.Time
}

// newLiveRenderer wraps sink with NDJSON-to-text rendering. A nil sink yields
// a nil writer so callers can pass the result straight to the runner.
func newLiveRenderer(sink io.Writer) io.Writer {
	if sink == nil {
		return nil
	}

	return &liveRenderer{sink: sink, now: time.Now}
}

func (r *liveRenderer) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.buf.Write(data)

	for {
		line, err := r.buf.ReadString('\n')
		if err != nil {
			// Incomplete line: keep the tail buffered for the next chunk.
			r.buf.WriteString(line)
			break
		}

		r.renderLine(strings.TrimRight(line, "\r\n"))
	}

	return len(data), nil
}

// liveEvent mirrors the subset of the codex --json event schema worth showing
// to a human. Fields outside this subset are ignored on purpose.
type liveEvent struct {
	Type     string      `json:"type"`
	ThreadID string      `json:"thread_id"`
	Message  string      `json:"message"`
	Error    *eventError `json:"error"`
	Item     *liveItem   `json:"item"`
	Usage    *liveUsage  `json:"usage"`
}

type liveItem struct {
	Type             string           `json:"type"`
	Text             string           `json:"text"`
	Command          string           `json:"command"`
	AggregatedOutput string           `json:"aggregated_output"`
	ExitCode         *int             `json:"exit_code"`
	Status           string           `json:"status"`
	Server           string           `json:"server"`
	Tool             string           `json:"tool"`
	Query            string           `json:"query"`
	Changes          []liveFileChange `json:"changes"`
}

type liveFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type liveUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
}

func (r *liveRenderer) renderLine(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}

	var event liveEvent
	if !strings.HasPrefix(trimmed, "{") || json.Unmarshal([]byte(trimmed), &event) != nil || event.Type == "" {
		// Not a codex event (stderr noise, plain text): surface as a marker
		// line so it still carries a timestamp.
		r.markf("%s", line)
		return
	}

	r.renderEvent(event)
}

// renderEvent renders the event kinds a human reader cares about;
// turn.started, item.updated, and future event types are progress noise and
// fall through the switch unrendered.
func (r *liveRenderer) renderEvent(event liveEvent) {
	switch event.Type {
	case "thread.started":
		r.markf("thread %s", event.ThreadID)
	case "item.started":
		if event.Item != nil && event.Item.Type == "command_execution" {
			r.markf("$ %s", event.Item.Command)
		}
	case "item.completed":
		r.renderItem(event.Item)
	case "turn.completed":
		if event.Usage != nil {
			r.markf("tokens: input=%d (cached %d) output=%d",
				event.Usage.InputTokens, event.Usage.CachedInputTokens, event.Usage.OutputTokens)
		}
	case "turn.failed", eventTypeError:
		r.markf("error: %s", eventFailureMessage(event))
	}
}

// renderItem renders completed items; task-list updates and unknown item
// kinds add little for a live reader and fall through the switch unrendered.
func (r *liveRenderer) renderItem(item *liveItem) {
	if item == nil {
		return
	}

	switch item.Type {
	case "agent_message":
		r.markf("agent:")
		r.raw(strings.TrimRight(item.Text, "\n"))
	case "reasoning":
		r.markf("thinking: %s", strings.TrimSpace(item.Text))
	case "command_execution":
		if output := strings.TrimRight(item.AggregatedOutput, "\n"); output != "" {
			r.raw(output)
		}

		r.markf("exit %s", formatExit(item))
	case "file_change":
		for _, change := range item.Changes {
			r.markf("file %s: %s", change.Kind, change.Path)
		}
	case "mcp_tool_call":
		r.markf("tool: %s.%s (%s)", item.Server, item.Tool, item.Status)
	case "web_search":
		r.markf("search: %s", item.Query)
	}
}

// markf emits one timestamped "[codex]" marker line. Markers bracket the raw
// content blocks, so a reader can always tell when each step of the agent's
// activity happened.
func (r *liveRenderer) markf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.sink, "%s [codex] %s\n", r.now().Format(liveTimeLayout), fmt.Sprintf(format, args...))
}

// raw emits content (agent message text, command output) untouched, without
// timestamp or prefix, so multi-line payloads stay copy-pasteable.
func (r *liveRenderer) raw(text string) {
	_, _ = fmt.Fprintf(r.sink, "%s\n", text)
}

func formatExit(item *liveItem) string {
	if item.ExitCode != nil {
		return strconv.Itoa(*item.ExitCode)
	}

	if item.Status != "" {
		return item.Status
	}

	return "unknown"
}

func eventFailureMessage(event liveEvent) string {
	if event.Error != nil && strings.TrimSpace(event.Error.Message) != "" {
		return strings.TrimSpace(event.Error.Message)
	}

	if strings.TrimSpace(event.Message) != "" {
		return strings.TrimSpace(event.Message)
	}

	return event.Type
}

// emitPromptBanner writes the rendered agent prompt to the live sink before
// the provider starts, mirroring AgentLoop where the prompt is part of the
// visible run transcript. The marker lines around the prompt carry timestamps
// like every other live message.
func emitPromptBanner(sink io.Writer, prompt string) {
	if sink == nil {
		return
	}

	timestamp := time.Now().Format(liveTimeLayout)
	_, _ = fmt.Fprintf(sink, "%s [codex] ──── prompt ────\n%s\n%s [codex] ──── live output ────\n",
		timestamp, strings.TrimRight(prompt, "\n"), timestamp)
}
