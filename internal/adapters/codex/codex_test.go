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
	request := shared.StartRequest{StepID: "review", AttemptID: "attempt-1", WorkingDir: "/repo", Prompt: "Fix the failing test"}
	args := buildExecArgs(request, "/tmp/last.txt")
	want := []string{"exec", "--json", "--color", "never", "--output-last-message", "/tmp/last.txt", "--cd", "/repo", "Fix the failing test"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("buildExecArgs() = %#v, want %#v", args, want)
	}
}

func TestBinaryPathMissingIsExplicit(t *testing.T) {
	adapter := New(Config{LookPath: func(string) (string, error) { return "", exec.ErrNotFound }, Runner: scriptedRunner{}})
	_, err := adapter.Start(context.Background(), shared.StartRequest{RunID: "run-1", StepID: "review", AttemptID: "attempt-1", Prompt: "Fix it"})
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

type scriptedRunner struct{}

func (scriptedRunner) Run(context.Context, CommandSpec) (CommandResult, error) {
	return CommandResult{}, nil
}
