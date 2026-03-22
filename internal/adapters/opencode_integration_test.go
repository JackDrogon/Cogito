package adapters_test

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/adapters/opencode"
)

func TestOpenCodeAdapterContract(t *testing.T) {
	runner := &opencodeContractRunner{}
	lookups := make([]string, 0, 2)
	adapter := opencode.New(opencode.Config{
		LookPath: func(name string) (string, error) {
			lookups = append(lookups, name)
			switch name {
			case "opencode":
				return "", exec.ErrNotFound
			case "opencode-desktop":
				return "/usr/local/bin/opencode-desktop", nil
			default:
				t.Fatalf("LookPath() unexpected name %q", name)
				return "", nil
			}
		},
		Runner: runner,
	})

	adapters.RunContractSuite(t, []adapters.ContractCase{{
		Name:             "opencode run terminal success path",
		Adapter:          adapter,
		StartRequest:     adapters.StartRequest{RunID: "run-345", StepID: "summarize", AttemptID: "attempt-3", WorkingDir: "/workspace/repo", Prompt: "Summarize the latest changes"},
		WantCapabilities: adapters.CapabilityMatrix{MachineReadableLogs: true},
		WantStartState:   adapters.ExecutionStateSucceeded,
		NormalizeRequest: adapters.NormalizeRequest{RequireMachineReadableLogs: true},
		WantResult: adapters.StepResult{
			Handle:     adapters.ExecutionHandle{RunID: "run-345", StepID: "summarize", AttemptID: "attempt-3", ProviderSessionID: "session-345"},
			Status:     adapters.ExecutionStateSucceeded,
			Summary:    "opencode adapter passed",
			OutputText: "opencode adapter passed\nEverything looks stable.",
			Logs: []adapters.LogEntry{
				{Level: "info", Message: "opencode binary resolved", Fields: map[string]string{"provider": "opencode", "version": "OpenCode 1.0.150"}},
				{Level: "info", Message: "opencode adapter passed", Fields: map[string]string{"event": "run.completed", "message_count": "1"}},
			},
		},
	}})

	if got := lookups; !reflect.DeepEqual(got, []string{"opencode", "opencode-desktop"}) {
		t.Fatalf("binary lookup sequence = %#v, want %#v", got, []string{"opencode", "opencode-desktop"})
	}

	if len(runner.calls) != 2 {
		t.Fatalf("runner call count = %d, want %d", len(runner.calls), 2)
	}

	if got := runner.calls[0].Args; !reflect.DeepEqual(got, []string{"--version"}) {
		t.Fatalf("version args = %#v, want %#v", got, []string{"--version"})
	}

	runCall := runner.calls[1]
	if runCall.Path != "/usr/local/bin/opencode-desktop" {
		t.Fatalf("run path = %q, want %q", runCall.Path, "/usr/local/bin/opencode-desktop")
	}
	if runCall.Dir != "/workspace/repo" {
		t.Fatalf("run dir = %q, want %q", runCall.Dir, "/workspace/repo")
	}
	if got := runCall.Args; !reflect.DeepEqual(got, []string{"run", "--json", "Summarize the latest changes"}) {
		t.Fatalf("run args = %#v, want %#v", got, []string{"run", "--json", "Summarize the latest changes"})
	}

	t.Log("opencode adapter passed")
}

func TestOpenCodeBinaryMissingIsExplicit(t *testing.T) {
	adapter := opencode.New(opencode.Config{
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		Runner: &opencodeContractRunner{},
	})

	_, err := adapter.Start(context.Background(), adapters.StartRequest{RunID: "run-345", StepID: "summarize", AttemptID: "attempt-3", Prompt: "Summarize"})
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
	if !strings.Contains(err.Error(), "opencode binary not found") {
		t.Fatalf("Start() error = %v, want contains %q", err, "opencode binary not found")
	}

	t.Log("opencode binary not found")
}

func TestOpenCodeCapabilitiesRegistered(t *testing.T) {
	registration, ok := adapters.Lookup(opencode.ProviderName)
	if !ok {
		t.Fatalf("Lookup(%q) found = false, want true", opencode.ProviderName)
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

type opencodeContractRunner struct {
	calls []opencodeCommandSpec
}

type opencodeCommandSpec struct {
	Path   string
	Args   []string
	Dir    string
	Stdin  string
	Stdout bool
	Stderr bool
}

func (r *opencodeContractRunner) Run(_ context.Context, command opencode.CommandSpec) (opencode.CommandResult, error) {
	r.calls = append(r.calls, opencodeCommandSpec{
		Path:   command.Path,
		Args:   append([]string(nil), command.Args...),
		Dir:    command.Dir,
		Stdin:  command.Stdin,
		Stdout: command.Stdout != nil,
		Stderr: command.Stderr != nil,
	})

	if reflect.DeepEqual(command.Args, []string{"--version"}) {
		return opencode.CommandResult{Stdout: []byte("OpenCode 1.0.150\n")}, nil
	}

	return opencode.CommandResult{Stdout: []byte(strings.TrimSpace(`
{
  "session_id": "session-345",
  "success": true,
  "summary": "opencode adapter passed",
  "output_text": "opencode adapter passed\nEverything looks stable.",
  "message_count": 1,
  "logs": [
    {
      "level": "info",
      "message": "opencode adapter passed",
      "fields": {
        "event": "run.completed",
        "message_count": 1
      }
    }
  ]
}`))}, nil
}
