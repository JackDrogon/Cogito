package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/provider"
	"github.com/JackDrogon/Cogito/internal/provider/opencode"
)

func TestOpenCodeAdapterContract(t *testing.T) {
	versionRunner := &opencodeVersionRunner{}
	starter := &opencodeFakeStarter{}
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
		Runner:  versionRunner,
		Starter: starter.start,
	})

	provider.RunContractSuite(t, []provider.ContractCase{{
		Name:             "opencode run terminal success path",
		Provider:         adapter,
		StartRequest:     provider.StartRequest{RunID: "run-345", StepID: "summarize", AttemptID: "attempt-3", WorkingDir: "/workspace/repo", Prompt: "Summarize the latest changes"},
		WantCapabilities: provider.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true, Resume: true, Interrupt: true},
		WantStartState:   provider.ExecutionStateRunning,
		WantPollStates:   []provider.ExecutionState{provider.ExecutionStateSucceeded},
		NormalizeRequest: provider.NormalizeRequest{RequireMachineReadableLogs: true},
		WantResult: provider.StepResult{
			// The terminal handle adopts the real opencode session_id surfaced
			// from the response, not the synthetic Start-time fallback.
			Handle:     provider.ExecutionHandle{RunID: "run-345", StepID: "summarize", AttemptID: "attempt-3", ProviderSessionID: "session-345"},
			Status:     provider.ExecutionStateSucceeded,
			Summary:    "opencode adapter passed",
			OutputText: "opencode adapter passed\nEverything looks stable.",
			Logs: []provider.LogEntry{
				{Level: "info", Message: "opencode binary resolved", Fields: map[string]string{"provider": "opencode", "version": "OpenCode 1.0.150"}},
				{Level: "info", Message: "opencode adapter passed", Fields: map[string]string{"event": "run.completed", "message_count": "1"}},
			},
		},
	}})

	if got := lookups; !reflect.DeepEqual(got, []string{"opencode", "opencode-desktop"}) {
		t.Fatalf("binary lookup sequence = %#v, want %#v", got, []string{"opencode", "opencode-desktop"})
	}

	if len(versionRunner.calls) != 1 {
		t.Fatalf("version runner call count = %d, want %d", len(versionRunner.calls), 1)
	}

	if got := versionRunner.calls[0]; !reflect.DeepEqual(got, []string{"--version"}) {
		t.Fatalf("version args = %#v, want %#v", got, []string{"--version"})
	}

	if starter.req.Binary != "/usr/local/bin/opencode-desktop" {
		t.Fatalf("starter binary = %q, want %q", starter.req.Binary, "/usr/local/bin/opencode-desktop")
	}
	if starter.req.Dir != "/workspace/repo" {
		t.Fatalf("starter dir = %q, want %q", starter.req.Dir, "/workspace/repo")
	}
	if starter.req.PromptOnStdin {
		t.Fatal("starter PromptOnStdin = true, want false (opencode takes prompt via argv)")
	}

	want := []string{
		"run", "--dir", "/workspace/repo", "--dangerously-skip-permissions",
		"--print-logs", "--output-format", "json", "Summarize the latest changes",
	}
	if got := starter.req.Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("run args = %#v, want %#v", got, want)
	}

	t.Log("opencode adapter passed")
}

func TestOpenCodeBinaryMissingIsExplicit(t *testing.T) {
	adapter := opencode.New(opencode.Config{
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		Runner: &opencodeVersionRunner{},
	})

	_, err := adapter.Start(t.Context(), provider.StartRequest{RunID: "run-345", StepID: "summarize", AttemptID: "attempt-3", Prompt: "Summarize"})
	if err == nil {
		t.Fatal("Start() error = nil, want error")
	}

	var adapterErr *provider.Error
	if !errors.As(err, &adapterErr) {
		t.Fatalf("Start() error type = %T, want *provider.Error", err)
	}
	if adapterErr.Code != provider.ErrorCodeExecution {
		t.Fatalf("Start() error code = %q, want %q", adapterErr.Code, provider.ErrorCodeExecution)
	}
	if !strings.Contains(err.Error(), "opencode binary not found") {
		t.Fatalf("Start() error = %v, want contains %q", err, "opencode binary not found")
	}

	t.Log("opencode binary not found")
}

func TestOpenCodeCapabilitiesRegistered(t *testing.T) {
	registration, ok := provider.Lookup(opencode.ProviderName)
	if !ok {
		t.Fatalf("Lookup(%q) found = false, want true", opencode.ProviderName)
	}

	want := provider.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true, Resume: true, Interrupt: true}
	if !reflect.DeepEqual(registration.Capabilities, want) {
		t.Fatalf("registered capabilities = %+v, want %+v", registration.Capabilities, want)
	}

	if registration.Capabilities.ArtifactRefs {
		t.Fatalf("unsupported capabilities must stay explicit: %+v", registration.Capabilities)
	}

	if registration.New == nil {
		t.Fatal("registered factory = nil, want non-nil")
	}
}

func TestOpenCodeResumeAppendsSessionArgs(t *testing.T) {
	starter := &opencodeRecordingStarter{}
	adapter := opencode.New(opencode.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/opencode", nil },
		Runner:   &opencodeVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, provider.StartRequest{RunID: "run-1", StepID: "summarize", AttemptID: "attempt-3", WorkingDir: "/workspace/repo", Prompt: "do it"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	resume, err := adapter.Resume(ctx, provider.ResumeRequest{Handle: start.Handle, Prompt: "do it"})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	if resume.State != provider.ExecutionStateRunning {
		t.Fatalf("Resume().State = %q, want %q", resume.State, provider.ExecutionStateRunning)
	}

	if len(starter.reqs) != 2 {
		t.Fatalf("starter call count = %d, want 2", len(starter.reqs))
	}

	for _, arg := range starter.reqs[0].Args {
		if arg == "--session" {
			t.Fatalf("start args must not contain session hint: %#v", starter.reqs[0].Args)
		}
	}

	resumeArgs := starter.reqs[1].Args
	assertArgSubsequence(t, resumeArgs, []string{"--session", start.Handle.ProviderSessionID})
	assertArgSubsequence(t, resumeArgs, []string{"--dir", "/workspace/repo"})
	if resumeArgs[len(resumeArgs)-1] != "do it" {
		t.Fatalf("resume args must end with prompt: %#v", resumeArgs)
	}
}

// TestOpenCodeResumeUsesRealSessionID is Oracle coverage gap #2 for opencode: a
// resume keyed off the terminal handle must put the real session_id into the
// `--session <sid>` argv, not the synthetic Start-time fallback.
func TestOpenCodeResumeUsesRealSessionID(t *testing.T) {
	const realSessionID = "real-session-opencode"

	starter := &opencodeRealSessionStarter{sessionID: realSessionID}
	adapter := opencode.New(opencode.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/opencode", nil },
		Runner:   &opencodeVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, provider.StartRequest{RunID: "run-1", StepID: "summarize", AttemptID: "attempt-3", WorkingDir: "/workspace/repo", Prompt: "do it"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	syntheticSessionID := start.Handle.ProviderSessionID
	if syntheticSessionID == realSessionID {
		t.Fatal("Start handle already carries the real session id, want synthetic fallback")
	}

	terminal, err := adapter.PollOrCollect(ctx, start.Handle)
	if err != nil {
		t.Fatalf("PollOrCollect() error = %v", err)
	}
	if terminal.Handle.ProviderSessionID != realSessionID {
		t.Fatalf("terminal ProviderSessionID = %q, want real id %q", terminal.Handle.ProviderSessionID, realSessionID)
	}

	resume, err := adapter.Resume(ctx, provider.ResumeRequest{Handle: terminal.Handle, Prompt: "do it", WorkingDir: "/workspace/repo"})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resume.State != provider.ExecutionStateRunning {
		t.Fatalf("Resume().State = %q, want %q", resume.State, provider.ExecutionStateRunning)
	}

	if len(starter.reqs) != 2 {
		t.Fatalf("starter call count = %d, want 2", len(starter.reqs))
	}

	resumeArgs := starter.reqs[1].Args
	assertArgSubsequence(t, resumeArgs, []string{"--session", realSessionID})
	for _, arg := range resumeArgs {
		if arg == syntheticSessionID {
			t.Fatalf("resume argv leaked synthetic session id %q: %#v", syntheticSessionID, resumeArgs)
		}
	}
	assertArgSubsequence(t, resumeArgs, []string{"--dir", "/workspace/repo"})
}

func TestOpenCodeTimeoutResultNormalizesAsFailed(t *testing.T) {
	const reason = "agent process timed out after 100ms"

	adapter := opencode.New(opencode.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/opencode", nil },
		Runner:   &opencodeVersionRunner{},
		Starter:  (&opencodeTimeoutStarter{reason: reason}).start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, provider.StartRequest{RunID: "run-1", StepID: "step", AttemptID: "attempt", WorkingDir: "/workspace/repo", Prompt: "do it"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	terminal, err := adapter.PollOrCollect(ctx, start.Handle)
	if err != nil {
		t.Fatalf("PollOrCollect() error = %v", err)
	}

	result, err := adapter.NormalizeResult(ctx, provider.NormalizeRequest{Execution: terminal})
	if err != nil {
		t.Fatalf("NormalizeResult() error = %v", err)
	}
	if result.Status != provider.ExecutionStateFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if !strings.Contains(result.Summary, reason) {
		t.Fatalf("Summary = %q, want contains %q", result.Summary, reason)
	}
}

type opencodeTimeoutStarter struct {
	reason string
}

func (s *opencodeTimeoutStarter) start(_ context.Context, _ provider.ProcessRequest) (*provider.Session, error) {
	done := make(chan provider.ProcessResult, 1)
	done <- provider.ProcessResult{TimeoutReason: s.reason, Interrupted: true}
	close(done)

	return &provider.Session{Done: done}, nil
}

// TestOpenCodeStructuredOutputFromOutputText is v3.2 N1: a real opencode run
// returns the AGENT_RESULT_JSON marker inside the normalized output_text message
// body, never as a standalone stdout line (the raw stdout is one JSON object
// with the marker escaped inside a string value). CollectTerminal must scan the
// normalized OutputText so StructuredOutput is populated.
func TestOpenCodeStructuredOutputFromOutputText(t *testing.T) {
	outputText := "Finished.\nAGENT_RESULT_JSON: {\"commits\":[\"oc-commit\"],\"summary\":\"ok\"}"

	payload, err := json.Marshal(map[string]any{
		"session_id":  "oc-session-n1",
		"success":     true,
		"output_text": outputText,
	})
	if err != nil {
		t.Fatalf("marshal opencode response: %v", err)
	}

	starter := &opencodeStdoutStarter{stdout: payload}
	adapter := opencode.New(opencode.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/opencode", nil },
		Runner:   &opencodeVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, provider.StartRequest{RunID: "run-1", StepID: "step", AttemptID: "attempt", WorkingDir: "/workspace/repo", Prompt: "do it"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	terminal, err := adapter.PollOrCollect(ctx, start.Handle)
	if err != nil {
		t.Fatalf("PollOrCollect() error = %v", err)
	}

	result := decodeAgentResult(t, terminal.StructuredOutput)
	if !reflect.DeepEqual(result.Commits, []string{"oc-commit"}) {
		t.Fatalf("StructuredOutput commits = %#v, want [oc-commit]", result.Commits)
	}
}

// opencodeStdoutStarter resolves Done with a fixed stdout payload so a test can
// exercise the response-parsing path with an arbitrary envelope.
type opencodeStdoutStarter struct {
	stdout []byte
}

func (s *opencodeStdoutStarter) start(_ context.Context, _ provider.ProcessRequest) (*provider.Session, error) {
	done := make(chan provider.ProcessResult, 1)
	done <- provider.ProcessResult{Stdout: s.stdout}
	close(done)

	return &provider.Session{Done: done}, nil
}

// opencodeRealSessionStarter resolves Done with a response carrying a
// configurable real session_id, recording every StartRequest so resume argv can
// be asserted.
type opencodeRealSessionStarter struct {
	sessionID string
	reqs      []provider.ProcessRequest
}

func (s *opencodeRealSessionStarter) start(_ context.Context, req provider.ProcessRequest) (*provider.Session, error) {
	s.reqs = append(s.reqs, req)

	done := make(chan provider.ProcessResult, 1)
	done <- provider.ProcessResult{Stdout: []byte(`{"session_id":"` + s.sessionID + `","success":true,"summary":"ok","output_text":"ok"}`)}
	close(done)

	return &provider.Session{Done: done}, nil
}

// opencodeRecordingStarter captures every async StartRequest and resolves Done
// immediately so Resume/Start return without blocking.
type opencodeRecordingStarter struct {
	reqs []provider.ProcessRequest
}

func (s *opencodeRecordingStarter) start(_ context.Context, req provider.ProcessRequest) (*provider.Session, error) {
	s.reqs = append(s.reqs, req)

	done := make(chan provider.ProcessResult, 1)
	done <- provider.ProcessResult{}
	close(done)

	return &provider.Session{Done: done}, nil
}

// opencodeVersionRunner records the synchronous `opencode --version` probe calls.
type opencodeVersionRunner struct {
	calls [][]string
}

func (r *opencodeVersionRunner) Run(_ context.Context, command opencode.CommandSpec) (opencode.CommandResult, error) {
	r.calls = append(r.calls, append([]string(nil), command.Args...))

	if reflect.DeepEqual(command.Args, []string{"--version"}) {
		return opencode.CommandResult{Stdout: []byte("OpenCode 1.0.150\n")}, nil
	}

	return opencode.CommandResult{}, nil
}

// opencodeFakeStarter captures the async StartRequest and resolves Done with a
// canned JSON response.
type opencodeFakeStarter struct {
	req provider.ProcessRequest
}

func (s *opencodeFakeStarter) start(_ context.Context, req provider.ProcessRequest) (*provider.Session, error) {
	s.req = req

	done := make(chan provider.ProcessResult, 1)
	done <- provider.ProcessResult{Stdout: []byte(strings.TrimSpace(`
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
}`))}
	close(done)

	return &provider.Session{Done: done}, nil
}
