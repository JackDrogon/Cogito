package adapters_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/adapters/claude"
	"github.com/JackDrogon/Cogito/internal/adapters/runner"
)

const testWorkspaceRepo = "/workspace/repo"

func TestClaudeAdapterContract(t *testing.T) {
	versionRunner := &claudeVersionRunner{}
	starter := &claudeFakeStarter{}
	adapter := claude.New(claude.Config{
		LookPath: func(name string) (string, error) {
			if name != "claude" {
				t.Fatalf("LookPath() name = %q, want %q", name, "claude")
			}
			return "/usr/local/bin/claude", nil
		},
		Runner:  versionRunner,
		Starter: starter.start,
	})

	adapters.RunContractSuite(t, []adapters.ContractCase{{
		Name:             "claude print terminal success path",
		Adapter:          adapter,
		StartRequest:     adapters.StartRequest{RunID: "run-234", StepID: "summarize", AttemptID: "attempt-2", WorkingDir: testWorkspaceRepo, Prompt: "Summarize the latest changes"},
		WantCapabilities: adapters.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true, Resume: true, Interrupt: true},
		WantStartState:   adapters.ExecutionStateRunning,
		WantPollStates:   []adapters.ExecutionState{adapters.ExecutionStateSucceeded},
		NormalizeRequest: adapters.NormalizeRequest{RequireMachineReadableLogs: true},
		WantResult: adapters.StepResult{
			// The terminal handle adopts the real claude session_id surfaced from
			// the response, not the synthetic Start-time fallback.
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

	if len(versionRunner.calls) != 1 {
		t.Fatalf("version runner call count = %d, want %d", len(versionRunner.calls), 1)
	}

	if got := versionRunner.calls[0]; !reflect.DeepEqual(got, []string{"--version"}) {
		t.Fatalf("version args = %#v, want %#v", got, []string{"--version"})
	}

	if starter.req.Binary != "/usr/local/bin/claude" {
		t.Fatalf("starter binary = %q, want %q", starter.req.Binary, "/usr/local/bin/claude")
	}
	if starter.req.Dir != testWorkspaceRepo {
		t.Fatalf("starter dir = %q, want %q", starter.req.Dir, testWorkspaceRepo)
	}
	if !starter.req.PromptOnStdin {
		t.Fatal("starter PromptOnStdin = false, want true (claude feeds prompt via stdin)")
	}
	if starter.req.Prompt != "Summarize the latest changes" {
		t.Fatalf("starter prompt = %q, want %q", starter.req.Prompt, "Summarize the latest changes")
	}

	want := []string{"--print", "--permission-mode", "bypassPermissions", "--output-format", "json"}
	if got := starter.req.Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("print args = %#v, want %#v", got, want)
	}

	t.Log("claude adapter passed")
}

func TestClaudeBinaryMissingIsExplicit(t *testing.T) {
	adapter := claude.New(claude.Config{
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		Runner: &claudeVersionRunner{},
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

	want := adapters.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true, Resume: true, Interrupt: true}
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

func TestClaudeResumeAppendsResumeArgs(t *testing.T) {
	starter := &claudeRecordingStarter{}
	adapter := claude.New(claude.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/claude", nil },
		Runner:   &claudeVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, adapters.StartRequest{RunID: "run-1", StepID: "summarize", AttemptID: "attempt-2", WorkingDir: testWorkspaceRepo, Prompt: "do it"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	resume, err := adapter.Resume(ctx, adapters.ResumeRequest{Handle: start.Handle, Prompt: "do it"})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	if resume.State != adapters.ExecutionStateRunning {
		t.Fatalf("Resume().State = %q, want %q", resume.State, adapters.ExecutionStateRunning)
	}

	if len(starter.reqs) != 2 {
		t.Fatalf("starter call count = %d, want 2", len(starter.reqs))
	}

	for _, arg := range starter.reqs[0].Args {
		if arg == "--resume" {
			t.Fatalf("start args must not contain resume hint: %#v", starter.reqs[0].Args)
		}
	}

	resumeArgs := starter.reqs[1].Args
	assertArgSubsequence(t, resumeArgs, []string{"--resume", start.Handle.ProviderSessionID})
	if !starter.reqs[1].PromptOnStdin {
		t.Fatal("resume PromptOnStdin = false, want true (claude feeds prompt via stdin)")
	}
}

// TestClaudeResumeUsesRealSessionID is Oracle coverage gap #2 for claude: a
// resume keyed off the terminal handle must put the real session_id into the
// `--resume <sid>` argv, not the synthetic Start-time fallback.
func TestClaudeResumeUsesRealSessionID(t *testing.T) {
	const realSessionID = "real-session-claude"

	starter := &claudeRealSessionStarter{sessionID: realSessionID}
	adapter := claude.New(claude.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/claude", nil },
		Runner:   &claudeVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, adapters.StartRequest{RunID: "run-1", StepID: "summarize", AttemptID: "attempt-2", WorkingDir: testWorkspaceRepo, Prompt: "do it"})
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

	resume, err := adapter.Resume(ctx, adapters.ResumeRequest{Handle: terminal.Handle, Prompt: "do it", WorkingDir: testWorkspaceRepo})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resume.State != adapters.ExecutionStateRunning {
		t.Fatalf("Resume().State = %q, want %q", resume.State, adapters.ExecutionStateRunning)
	}

	if len(starter.reqs) != 2 {
		t.Fatalf("starter call count = %d, want 2", len(starter.reqs))
	}

	resumeArgs := starter.reqs[1].Args
	assertArgSubsequence(t, resumeArgs, []string{"--resume", realSessionID})
	for _, arg := range resumeArgs {
		if arg == syntheticSessionID {
			t.Fatalf("resume argv leaked synthetic session id %q: %#v", syntheticSessionID, resumeArgs)
		}
	}
}

// TestClaudeStructuredOutputFromResponseResult is v3.2 N1: a real claude run
// returns the AGENT_RESULT_JSON marker inside the parsed response.Result string,
// never as a standalone stdout line (the raw stdout is one JSON object with the
// marker escaped inside a string value). collectTerminal must scan the
// normalized response.Result so StructuredOutput is populated.
func TestClaudeStructuredOutputFromResponseResult(t *testing.T) {
	resultText := "Work complete.\nAGENT_RESULT_JSON: {\"commits\":[\"claude-commit\"],\"summary\":\"ok\"}"

	payload, err := json.Marshal(map[string]any{
		"type":       "result",
		"subtype":    "success",
		"is_error":   false,
		"result":     resultText,
		"session_id": "claude-session-n1",
	})
	if err != nil {
		t.Fatalf("marshal claude response: %v", err)
	}

	starter := &claudeStdoutStarter{stdout: payload}
	adapter := claude.New(claude.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/claude", nil },
		Runner:   &claudeVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, adapters.StartRequest{RunID: "run-1", StepID: "step", AttemptID: "attempt", WorkingDir: testWorkspaceRepo, Prompt: "do it"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	terminal, err := adapter.PollOrCollect(ctx, start.Handle)
	if err != nil {
		t.Fatalf("PollOrCollect() error = %v", err)
	}

	result := decodeAgentResult(t, terminal.StructuredOutput)
	if !reflect.DeepEqual(result.Commits, []string{"claude-commit"}) {
		t.Fatalf("StructuredOutput commits = %#v, want [claude-commit]", result.Commits)
	}
}

// claudeStdoutStarter resolves Done with a fixed stdout payload so a test can
// exercise the response-parsing path with an arbitrary envelope.
type claudeStdoutStarter struct {
	stdout []byte
}

func (s *claudeStdoutStarter) start(_ context.Context, _ runner.StartRequest) (*runner.Session, error) {
	done := make(chan runner.Result, 1)
	done <- runner.Result{Stdout: s.stdout}
	close(done)

	return &runner.Session{Done: done}, nil
}

// claudeRealSessionStarter resolves Done with a response carrying a configurable
// real session_id, recording every StartRequest so resume argv can be asserted.
type claudeRealSessionStarter struct {
	sessionID string
	reqs      []runner.StartRequest
}

func (s *claudeRealSessionStarter) start(_ context.Context, req runner.StartRequest) (*runner.Session, error) {
	s.reqs = append(s.reqs, req)

	done := make(chan runner.Result, 1)
	done <- runner.Result{Stdout: []byte(`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"` + s.sessionID + `"}`)}
	close(done)

	return &runner.Session{Done: done}, nil
}

// claudeRecordingStarter captures every async StartRequest and resolves Done
// immediately so Resume/Start return without blocking.
type claudeRecordingStarter struct {
	reqs []runner.StartRequest
}

func (s *claudeRecordingStarter) start(_ context.Context, req runner.StartRequest) (*runner.Session, error) {
	s.reqs = append(s.reqs, req)

	done := make(chan runner.Result, 1)
	done <- runner.Result{}
	close(done)

	return &runner.Session{Done: done}, nil
}

// claudeVersionRunner records the synchronous `claude --version` probe calls.
type claudeVersionRunner struct {
	calls [][]string
}

func (r *claudeVersionRunner) Run(_ context.Context, command claude.CommandSpec) (claude.CommandResult, error) {
	r.calls = append(r.calls, append([]string(nil), command.Args...))

	if reflect.DeepEqual(command.Args, []string{"--version"}) {
		return claude.CommandResult{Stdout: []byte("2.1.71 (Claude Code)\n")}, nil
	}

	return claude.CommandResult{}, nil
}

// claudeFakeStarter captures the async StartRequest and resolves Done with a
// canned single-object JSON response.
type claudeFakeStarter struct {
	req runner.StartRequest
}

func (s *claudeFakeStarter) start(_ context.Context, req runner.StartRequest) (*runner.Session, error) {
	s.req = req

	done := make(chan runner.Result, 1)
	done <- runner.Result{Stdout: []byte(strings.TrimSpace(`
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
}`))}
	close(done)

	return &runner.Session{Done: done}, nil
}
