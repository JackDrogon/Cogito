package adapters_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/adapters/codex"
)

func TestCodexAdapterContract(t *testing.T) {
	runner := &codexContractRunner{}
	adapter := codex.New(codex.Config{
		LookPath: func(name string) (string, error) {
			if name != "codex" {
				t.Fatalf("LookPath() name = %q, want %q", name, "codex")
			}
			return "/usr/local/bin/codex", nil
		},
		Runner: runner,
	})

	adapters.RunContractSuite(t, []adapters.ContractCase{{
		Name:             "codex exec terminal success path",
		Adapter:          adapter,
		StartRequest:     adapters.StartRequest{RunID: "run-123", StepID: "review", AttemptID: "attempt-1", WorkingDir: "/workspace/repo", Prompt: "Summarize the repo changes"},
		WantCapabilities: adapters.CapabilityMatrix{MachineReadableLogs: true},
		WantStartState:   adapters.ExecutionStateSucceeded,
		NormalizeRequest: adapters.NormalizeRequest{RequireMachineReadableLogs: true},
		WantResult: adapters.StepResult{
			Handle:     adapters.ExecutionHandle{RunID: "run-123", StepID: "review", AttemptID: "attempt-1", ProviderSessionID: "thread-123"},
			Status:     adapters.ExecutionStateSucceeded,
			Summary:    "All changes look good.",
			OutputText: "All changes look good.\nNothing else to add.",
			Logs: []adapters.LogEntry{
				{Level: "info", Message: "codex binary resolved", Fields: map[string]string{"provider": "codex", "version": "codex-cli 0.66.0"}},
				{Level: "info", Message: "thread.started", Fields: map[string]string{"type": "thread.started", "thread_id": "thread-123"}},
				{Level: "info", Message: "turn.started", Fields: map[string]string{"type": "turn.started"}},
				{Level: "info", Message: "turn.completed", Fields: map[string]string{"type": "turn.completed"}},
			},
		},
	}})

	if len(runner.calls) != 2 {
		t.Fatalf("runner call count = %d, want %d", len(runner.calls), 2)
	}

	if got := runner.calls[0].Args; !reflect.DeepEqual(got, []string{"--version"}) {
		t.Fatalf("version args = %#v, want %#v", got, []string{"--version"})
	}

	execArgs := runner.calls[1].Args
	if len(execArgs) < 9 {
		t.Fatalf("exec args too short: %#v", execArgs)
	}
	if execArgs[0] != "exec" || execArgs[1] != "--json" || execArgs[2] != "--color" || execArgs[3] != "never" {
		t.Fatalf("exec args prefix = %#v, want exec json color-never", execArgs[:4])
	}
	if execArgs[4] != "--output-last-message" {
		t.Fatalf("exec args missing output-last-message flag: %#v", execArgs)
	}
	if execArgs[6] != "--cd" || execArgs[7] != "/workspace/repo" {
		t.Fatalf("exec args working dir = %#v, want --cd /workspace/repo", execArgs)
	}
	if execArgs[8] != "Summarize the repo changes" {
		t.Fatalf("exec prompt arg = %q, want %q", execArgs[8], "Summarize the repo changes")
	}

	t.Log("codex adapter passed")
}

func TestCodexBinaryMissingIsExplicit(t *testing.T) {
	adapter := codex.New(codex.Config{
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		Runner: &codexContractRunner{},
	})

	_, err := adapter.Start(context.Background(), adapters.StartRequest{RunID: "run-123", StepID: "review", AttemptID: "attempt-1", Prompt: "Summarize"})
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
	if !strings.Contains(err.Error(), "codex binary not found") {
		t.Fatalf("Start() error = %v, want contains %q", err, "codex binary not found")
	}

	t.Log("codex binary not found")
}

func TestCodexCapabilitiesRegistered(t *testing.T) {
	registration, ok := adapters.Lookup(codex.ProviderName)
	if !ok {
		t.Fatalf("Lookup(%q) found = false, want true", codex.ProviderName)
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

type codexContractRunner struct {
	calls []codexCommandSpec
}

type codexCommandSpec struct {
	Path   string
	Args   []string
	Dir    string
	Stdin  string
	Stdout bool
	Stderr bool
}

func (r *codexContractRunner) Run(_ context.Context, command codex.CommandSpec) (codex.CommandResult, error) {
	r.calls = append(r.calls, codexCommandSpec{
		Path:   command.Path,
		Args:   append([]string(nil), command.Args...),
		Dir:    command.Dir,
		Stdin:  command.Stdin,
		Stdout: command.Stdout != nil,
		Stderr: command.Stderr != nil,
	})

	if reflect.DeepEqual(command.Args, []string{"--version"}) {
		return codex.CommandResult{Stdout: []byte("codex-cli 0.66.0\n")}, nil
	}

	if len(command.Args) > 5 && command.Args[4] == "--output-last-message" {
		if err := os.WriteFile(command.Args[5], []byte("All changes look good.\nNothing else to add.\n"), 0o600); err != nil {
			return codex.CommandResult{}, err
		}
	}

	return codex.CommandResult{Stdout: []byte(strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-123"}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.completed"}`,
	}, "\n"))}, nil
}
