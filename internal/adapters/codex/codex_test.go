package codex

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	shared "github.com/JackDrogon/Cogito/internal/adapters"
)

func TestBuildExecArgs(t *testing.T) {
	sessionID := "11111111-2222-3333-4444-555555555555"

	tests := []struct {
		name   string
		params execArgsParams
		want   []string
	}{
		{
			name:   "no model and no resume",
			params: execArgsParams{WorkingDir: "/repo", LastMessagePath: "/tmp/last.txt", Sandbox: "danger-full-access"},
			want: []string{
				"exec", "--cd", "/repo", "--sandbox", "danger-full-access",
				"--skip-git-repo-check", "--json", "--color", "never",
				"--output-last-message", "/tmp/last.txt", "-",
			},
		},
		{
			name:   "with model",
			params: execArgsParams{WorkingDir: "/repo", LastMessagePath: "/tmp/last.txt", Sandbox: "danger-full-access", Model: "gpt-5-codex"},
			want: []string{
				"exec", "--cd", "/repo", "--sandbox", "danger-full-access",
				"--skip-git-repo-check", "--json", "--color", "never",
				"--output-last-message", "/tmp/last.txt", "--model", "gpt-5-codex", "-",
			},
		},
		{
			name:   "with resume",
			params: execArgsParams{WorkingDir: "/repo", LastMessagePath: "/tmp/last.txt", Sandbox: "danger-full-access", ResumeSessionID: &sessionID},
			want: []string{
				"exec", "--cd", "/repo", "--sandbox", "danger-full-access",
				"--skip-git-repo-check", "--json", "--color", "never",
				"--output-last-message", "/tmp/last.txt", "resume", sessionID, "-",
			},
		},
		{
			name:   "with model and resume",
			params: execArgsParams{WorkingDir: "/repo", LastMessagePath: "/tmp/last.txt", Sandbox: "danger-full-access", Model: "gpt-5-codex", ResumeSessionID: &sessionID},
			want: []string{
				"exec", "--cd", "/repo", "--sandbox", "danger-full-access",
				"--skip-git-repo-check", "--json", "--color", "never",
				"--output-last-message", "/tmp/last.txt", "--model", "gpt-5-codex",
				"resume", sessionID, "-",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildExecArgs(tt.params)
			if !reflect.DeepEqual(args, tt.want) {
				t.Fatalf("buildExecArgs() = %#v, want %#v", args, tt.want)
			}
		})
	}
}

func TestBinaryPathMissingIsExplicit(t *testing.T) {
	adapter := New(Config{LookPath: func(string) (string, error) { return "", exec.ErrNotFound }, Runner: scriptedRunner{}})
	_, err := adapter.Start(t.Context(), shared.StartRequest{RunID: "run-1", StepID: "review", AttemptID: "attempt-1", Prompt: "Fix it"})
	if err == nil {
		t.Fatal("Start() error = nil, want error")
	}

	var adapterErr *shared.Error
	if !errors.As(err, &adapterErr) {
		t.Fatalf("Start() error type = %T, want *adapters.Error", err)
	}

	if adapterErr.Code != shared.ErrorCodeExecution {
		t.Fatalf("Start() error code = %q, want %q", adapterErr.Code, shared.ErrorCodeExecution)
	}

	if adapterErr.Message != "codex binary not found" {
		t.Fatalf("Start() error message = %q, want %q", adapterErr.Message, "codex binary not found")
	}
}

func TestParseEventsHandlesLargeJSONLines(t *testing.T) {
	largeMessage := strings.Repeat("x", 256*1024)
	payload := []byte("{\"type\":\"message\",\"thread_id\":\"thread-123\",\"message\":\"" + largeMessage + "\"}\n")

	events, err := parseEvents(payload)
	if err != nil {
		t.Fatalf("parseEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want %d", len(events), 1)
	}
	if events[0].Message != largeMessage {
		t.Fatalf("events[0].Message length = %d, want %d", len(events[0].Message), len(largeMessage))
	}
}

// TestProviderSessionIDReturnsLastThreadID is v3.2 N7: when the event stream
// carries multiple thread ids (concatenated logs or a rotated session), the
// LAST non-empty thread_id is the correct resume target, not the first.
func TestProviderSessionIDReturnsLastThreadID(t *testing.T) {
	request := shared.StartRequest{StepID: "step", AttemptID: "attempt"}
	events := []event{
		{Type: "thread.started", ThreadID: "thread-first"},
		{Type: "turn.completed"},
		{Type: "thread.started", ThreadID: "thread-second"},
	}

	if got := providerSessionID(request, events); got != "thread-second" {
		t.Fatalf("providerSessionID() = %q, want %q", got, "thread-second")
	}
}

// TestProviderSessionIDFallsBackToSynthetic verifies the synthetic fallback when
// no event carries a thread id.
func TestProviderSessionIDFallsBackToSynthetic(t *testing.T) {
	request := shared.StartRequest{StepID: "review", AttemptID: "attempt-1"}
	events := []event{{Type: "turn.completed"}}

	if got := providerSessionID(request, events); got != "codex-review-attempt-1" {
		t.Fatalf("providerSessionID() = %q, want synthetic fallback", got)
	}
}

type scriptedRunner struct{}

func (scriptedRunner) Run(context.Context, CommandSpec) (CommandResult, error) {
	return CommandResult{}, nil
}
