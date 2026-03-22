package adapters_test

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/adapters/claude"
)

const testWorkspaceRepo = "/workspace/repo"

func TestClaudeAdapterContract(t *testing.T) {
	runner := &claudeContractRunner{}
	adapter := claude.New(claude.Config{
		LookPath: func(name string) (string, error) {
			if name != "claude" {
				t.Fatalf("LookPath() name = %q, want %q", name, "claude")
			}
			return "/usr/local/bin/claude", nil
		},
		Runner: runner,
	})

	adapters.RunContractSuite(t, []adapters.ContractCase{{
		Name:             "claude print terminal success path",
		Adapter:          adapter,
		StartRequest:     adapters.StartRequest{RunID: "run-234", StepID: "summarize", AttemptID: "attempt-2", WorkingDir: testWorkspaceRepo, Prompt: "Summarize the latest changes"},
		WantCapabilities: adapters.CapabilityMatrix{MachineReadableLogs: true},
		WantStartState:   adapters.ExecutionStateSucceeded,
		NormalizeRequest: adapters.NormalizeRequest{RequireMachineReadableLogs: true},
		WantResult: adapters.StepResult{
			Handle:     adapters.ExecutionHandle{RunID: "run-234", StepID: "summarize", AttemptID: "attempt-2", ProviderSessionID: "session-234"},
			Status:     adapters.ExecutionStateSucceeded,
			Summary:    "claude adapter passed",
			OutputText: "claude adapter passed\nEverything looks stable.",
			Logs: []adapters.LogEntry{
				{Level: "info", Message: "claude binary resolved", Fields: map[string]string{"provider": "claude", "version": "2.1.71 (Claude Code)"}},
				{Level: "info", Message: "claude adapter passed", Fields: map[string]string{"type": "result", "subtype": "success", "stop_reason": "end_turn", "session_id": "session-234", "duration_ms": "1532", "duration_api_ms": "1200", "num_turns": "1"}},
			},
		},
	}})

	if len(runner.calls) != 2 {
		t.Fatalf("runner call count = %d, want %d", len(runner.calls), 2)
	}

	if got := runner.calls[0].Args; !reflect.DeepEqual(got, []string{"--version"}) {
		t.Fatalf("version args = %#v, want %#v", got, []string{"--version"})
	}

	execCall := runner.calls[1]
	if execCall.Path != "/usr/local/bin/claude" {
		t.Fatalf("exec path = %q, want %q", execCall.Path, "/usr/local/bin/claude")
	}
	if execCall.Dir != testWorkspaceRepo {
		t.Fatalf("exec dir = %q, want %q", execCall.Dir, testWorkspaceRepo)
	}
	if got := execCall.Args; !reflect.DeepEqual(got, []string{"--print", "--output-format", "json", "Summarize the latest changes"}) {
		t.Fatalf("exec args = %#v, want %#v", got, []string{"--print", "--output-format", "json", "Summarize the latest changes"})
	}

	t.Log("claude adapter passed")
}

func TestClaudeBinaryMissingIsExplicit(t *testing.T) {
	adapter := claude.New(claude.Config{
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		Runner: &claudeContractRunner{},
	})

	_, err := adapter.Start(t.Context(), adapters.StartRequest{RunID: "run-234", StepID: "summarize", AttemptID: "attempt-2", Prompt: "Summarize"})
	if err == nil {
		t.Fatal("Start() error = nil, want error")
	}

	var adapterErr *adapters.Error
	if !errors.As(err, &adapterErr) {
		t.Fatalf("Start() error type = %T, want *adapters.Error", err)
	}
	if adapterErr.Code != adapters.ErrorCodeExecution {
		t.Fatalf("Start() error code = %q, want %q", adapterErr.Code, adapters.ErrorCodeExecution)
	}
	if !strings.Contains(err.Error(), "claude binary not found") {
		t.Fatalf("Start() error = %v, want contains %q", err, "claude binary not found")
	}

	t.Log("claude binary not found")
}

func TestClaudeCapabilitiesRegistered(t *testing.T) {
	registration, ok := adapters.Lookup(claude.ProviderName)
	if !ok {
		t.Fatalf("Lookup(%q) found = false, want true", claude.ProviderName)
	}

	want := adapters.CapabilityMatrix{MachineReadableLogs: true}
	if !reflect.DeepEqual(registration.Capabilities, want) {
		t.Fatalf("registered capabilities = %+v, want %+v", registration.Capabilities, want)
	}

	if registration.Capabilities.StructuredOutput || registration.Capabilities.Resume || registration.Capabilities.Interrupt || registration.Capabilities.ArtifactRefs {
		t.Fatalf("unsupported capabilities must stay explicit: %+v", registration.Capabilities)
	}

	if registration.New == nil {
		t.Fatal("registered factory = nil, want non-nil")
	}
}

type claudeContractRunner struct {
	calls []claudeCommandSpec
}

type claudeCommandSpec struct {
	Path   string
	Args   []string
	Dir    string
	Stdin  string
	Stdout bool
	Stderr bool
}

func (r *claudeContractRunner) Run(_ context.Context, command claude.CommandSpec) (claude.CommandResult, error) {
	r.calls = append(r.calls, claudeCommandSpec{
		Path:   command.Path,
		Args:   append([]string(nil), command.Args...),
		Dir:    command.Dir,
		Stdin:  command.Stdin,
		Stdout: command.Stdout != nil,
		Stderr: command.Stderr != nil,
	})

	if reflect.DeepEqual(command.Args, []string{"--version"}) {
		return claude.CommandResult{Stdout: []byte("2.1.71 (Claude Code)\n")}, nil
	}

	return claude.CommandResult{Stdout: []byte(strings.TrimSpace(`
{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "duration_ms": 1532,
  "duration_api_ms": 1200,
  "num_turns": 1,
  "result": "claude adapter passed\nEverything looks stable.",
  "stop_reason": "end_turn",
  "session_id": "session-234"
}`))}, nil
}
