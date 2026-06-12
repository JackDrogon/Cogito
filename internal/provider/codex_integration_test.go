package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/prompt"
	"github.com/JackDrogon/Cogito/internal/provider"
	"github.com/JackDrogon/Cogito/internal/provider/codex"
)

func TestCodexAdapterContract(t *testing.T) {
	versionRunner := &codexVersionRunner{}
	starter := &codexFakeStarter{}
	adapter := codex.New(codex.Config{
		LookPath: func(name string) (string, error) {
			if name != "codex" {
				t.Fatalf("LookPath() name = %q, want %q", name, "codex")
			}
			return "/usr/local/bin/codex", nil
		},
		Runner:  versionRunner,
		Starter: starter.start,
	})

	provider.RunContractSuite(t, []provider.ContractCase{{
		Name:             "codex exec terminal success path",
		Provider:         adapter,
		StartRequest:     provider.StartRequest{RunID: "run-123", StepID: "review", AttemptID: "attempt-1", WorkingDir: "/workspace/repo", Prompt: "Summarize the repo changes"},
		WantCapabilities: provider.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true, Resume: true, Interrupt: true},
		WantStartState:   provider.ExecutionStateRunning,
		WantPollStates:   []provider.ExecutionState{provider.ExecutionStateSucceeded},
		NormalizeRequest: provider.NormalizeRequest{RequireMachineReadableLogs: true},
		WantResult: provider.StepResult{
			// The terminal handle adopts the real codex thread_id surfaced from
			// the event stream, not the synthetic Start-time fallback.
			Handle:     provider.ExecutionHandle{RunID: "run-123", StepID: "review", AttemptID: "attempt-1", ProviderSessionID: "thread-123"},
			Status:     provider.ExecutionStateSucceeded,
			Summary:    "All changes look good.",
			OutputText: "All changes look good.\nNothing else to add.",
			Usage:      &provider.Usage{InputTokens: 1200, OutputTokens: 345, TotalTokens: 1545},
			Logs: []provider.LogEntry{
				{Level: "info", Message: "codex binary resolved", Fields: map[string]string{"provider": "codex", "version": "codex-cli 0.66.0"}},
				{Level: "info", Message: "thread.started", Fields: map[string]string{"type": "thread.started", "thread_id": "thread-123"}},
				{Level: "info", Message: "turn.started", Fields: map[string]string{"type": "turn.started"}},
				{Level: "info", Message: "turn.completed", Fields: map[string]string{"type": "turn.completed"}},
			},
		},
	}})

	if len(versionRunner.calls) != 1 {
		t.Fatalf("version runner call count = %d, want %d", len(versionRunner.calls), 1)
	}

	if got := versionRunner.calls[0]; !reflect.DeepEqual(got, []string{"--version"}) {
		t.Fatalf("version args = %#v, want %#v", got, []string{"--version"})
	}

	if starter.req.Binary != "/usr/local/bin/codex" {
		t.Fatalf("starter binary = %q, want %q", starter.req.Binary, "/usr/local/bin/codex")
	}
	if starter.req.Dir != "/workspace/repo" {
		t.Fatalf("starter dir = %q, want %q", starter.req.Dir, "/workspace/repo")
	}
	if !starter.req.PromptOnStdin {
		t.Fatal("starter PromptOnStdin = false, want true (codex feeds prompt via stdin)")
	}
	if starter.req.Prompt != "Summarize the repo changes" {
		t.Fatalf("starter prompt = %q, want %q", starter.req.Prompt, "Summarize the repo changes")
	}

	execArgs := starter.req.Args
	wantPrefix := []string{
		"exec", "--cd", "/workspace/repo", "--sandbox", "danger-full-access",
		"--skip-git-repo-check", "--json", "--color", "never", "--output-last-message",
	}
	if len(execArgs) < len(wantPrefix)+2 {
		t.Fatalf("exec args too short: %#v", execArgs)
	}
	if !reflect.DeepEqual(execArgs[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("exec args prefix = %#v, want %#v", execArgs[:len(wantPrefix)], wantPrefix)
	}
	if execArgs[len(execArgs)-1] != "-" {
		t.Fatalf("exec args must end with stdin marker '-': %#v", execArgs)
	}

	t.Log("codex adapter passed")
}

func TestCodexBinaryMissingIsExplicit(t *testing.T) {
	adapter := codex.New(codex.Config{
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		Runner: &codexVersionRunner{},
	})

	_, err := adapter.Start(t.Context(), provider.StartRequest{RunID: "run-123", StepID: "review", AttemptID: "attempt-1", Prompt: "Summarize"})
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
	if !strings.Contains(err.Error(), "codex binary not found") {
		t.Fatalf("Start() error = %v, want contains %q", err, "codex binary not found")
	}

	t.Log("codex binary not found")
}

func TestCodexCapabilitiesRegistered(t *testing.T) {
	registration, ok := provider.Lookup(codex.ProviderName)
	if !ok {
		t.Fatalf("Lookup(%q) found = false, want true", codex.ProviderName)
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

func TestCodexResumeAppendsResumeArgs(t *testing.T) {
	starter := &codexRecordingStarter{}
	adapter := codex.New(codex.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/codex", nil },
		Runner:   &codexVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, provider.StartRequest{RunID: "run-1", StepID: "review", AttemptID: "attempt-1", WorkingDir: "/workspace/repo", Prompt: "do it"})
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
		if arg == "resume" {
			t.Fatalf("start args must not contain resume hint: %#v", starter.reqs[0].Args)
		}
	}

	resumeArgs := starter.reqs[1].Args
	assertArgSubsequence(t, resumeArgs, []string{"resume", start.Handle.ProviderSessionID})
	assertArgSubsequence(t, resumeArgs, []string{"--cd", "/workspace/repo"})
	if resumeArgs[len(resumeArgs)-1] != "-" {
		t.Fatalf("resume args must end with stdin marker '-': %#v", resumeArgs)
	}
}

// TestCodexResumeUsesRealSessionID is Oracle coverage gap #2: when the runner
// emits a real session id (codex thread_id), collectTerminal must surface it on
// the terminal handle, and a Resume keyed off that handle must put the REAL id
// into the `resume <sid>` argv rather than the synthetic Start-time fallback.
func TestCodexResumeUsesRealSessionID(t *testing.T) {
	const realSessionID = "real-thread-xyz"

	starter := &codexRealSessionStarter{sessionID: realSessionID}
	adapter := codex.New(codex.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/codex", nil },
		Runner:   &codexVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, provider.StartRequest{RunID: "run-1", StepID: "step", AttemptID: "attempt", WorkingDir: "/workspace/repo", Prompt: "do it"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Start returns the synthetic fallback; the real id only emerges after the
	// runner output is collected.
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
	assertArgSubsequence(t, resumeArgs, []string{"resume", realSessionID})
	// The synthetic id must NOT appear in the resume argv.
	for _, arg := range resumeArgs {
		if arg == syntheticSessionID {
			t.Fatalf("resume argv leaked synthetic session id %q: %#v", syntheticSessionID, resumeArgs)
		}
	}
	// WorkingDir must come through the resume request even though the session
	// map entry was cleaned up after collectTerminal.
	assertArgSubsequence(t, resumeArgs, []string{"--cd", "/workspace/repo"})
}

func TestCodexTimeoutResultNormalizesAsFailed(t *testing.T) {
	const reason = "agent process timed out after 100ms"

	adapter := codex.New(codex.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/codex", nil },
		Runner:   &codexVersionRunner{},
		Starter:  (&codexTimeoutStarter{reason: reason}).start,
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

type codexTimeoutStarter struct {
	reason string
}

func (s *codexTimeoutStarter) start(_ context.Context, _ provider.ProcessRequest) (*provider.Session, error) {
	done := make(chan provider.ProcessResult, 1)
	done <- provider.ProcessResult{TimeoutReason: s.reason, Interrupted: true}
	close(done)

	return &provider.Session{Done: done}, nil
}

// codexRealSessionStarter writes the codex last-message file and resolves Done
// with an event stream carrying a configurable real thread_id, recording every
// StartRequest so resume argv can be asserted.
type codexRealSessionStarter struct {
	sessionID string
	reqs      []provider.ProcessRequest
}

func (s *codexRealSessionStarter) start(_ context.Context, req provider.ProcessRequest) (*provider.Session, error) {
	s.reqs = append(s.reqs, req)

	for i := 0; i+1 < len(req.Args); i++ {
		if req.Args[i] == "--output-last-message" {
			if err := os.WriteFile(req.Args[i+1], []byte("done\n"), 0o600); err != nil {
				return nil, err
			}
		}
	}

	done := make(chan provider.ProcessResult, 1)
	done <- provider.ProcessResult{Stdout: []byte(strings.Join([]string{
		`{"type":"thread.started","thread_id":"` + s.sessionID + `"}`,
		`{"type":"turn.completed","usage":{"input_tokens":1200,"cached_input_tokens":300,"output_tokens":345}}`,
	}, "\n"))}
	close(done)

	return &provider.Session{Done: done}, nil
}

// assertArgSubsequence fails unless want appears as a contiguous run inside got.
func assertArgSubsequence(t *testing.T, got, want []string) {
	t.Helper()

	for start := 0; start+len(want) <= len(got); start++ {
		if reflect.DeepEqual(got[start:start+len(want)], want) {
			return
		}
	}

	t.Fatalf("args %#v missing contiguous subsequence %#v", got, want)
}

// codexRecordingStarter captures every async StartRequest and resolves Done
// immediately so Resume/Start return without blocking.
type codexRecordingStarter struct {
	reqs []provider.ProcessRequest
}

func (s *codexRecordingStarter) start(_ context.Context, req provider.ProcessRequest) (*provider.Session, error) {
	s.reqs = append(s.reqs, req)

	done := make(chan provider.ProcessResult, 1)
	done <- provider.ProcessResult{}
	close(done)

	return &provider.Session{Done: done}, nil
}

// TestCodexStructuredOutputFromLastMessage is v3.2 N1: a real codex run wraps
// the AGENT_RESULT_JSON marker inside its last-message file, NOT the raw event
// stream. CollectTerminal must scan the normalized last-message text so the
// StructuredOutput is populated for downstream verify/commit_check steps, even
// when the marker never appears on stdout.
func TestCodexStructuredOutputFromLastMessage(t *testing.T) {
	const marker = `AGENT_RESULT_JSON: {"completed":["task-1"],"commits":["codex-commit"],"verification":["go test ./..."],"summary":"done"}`

	starter := &codexLastMessageStarter{lastMessage: "All changes applied.\n" + marker + "\n"}
	adapter := codex.New(codex.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/codex", nil },
		Runner:   &codexVersionRunner{},
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
	if !reflect.DeepEqual(result.Commits, []string{"codex-commit"}) {
		t.Fatalf("StructuredOutput commits = %#v, want [codex-commit]", result.Commits)
	}

	if !reflect.DeepEqual(result.Verification, []string{"go test ./..."}) {
		t.Fatalf("StructuredOutput verification = %#v, want [go test ./...]", result.Verification)
	}
}

// TestCodexStructuredOutputCorruptMarkerFails is v3.2 N2: a present-but-corrupt
// AGENT_RESULT_JSON marker in the normalized last-message text must surface as a
// build error from PollOrCollect, not be swallowed as empty structured output.
func TestCodexStructuredOutputCorruptMarkerFails(t *testing.T) {
	starter := &codexLastMessageStarter{lastMessage: "AGENT_RESULT_JSON: {not valid json\n"}
	adapter := codex.New(codex.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/codex", nil },
		Runner:   &codexVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, provider.StartRequest{RunID: "run-1", StepID: "step", AttemptID: "attempt", WorkingDir: "/workspace/repo", Prompt: "do it"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	_, err = adapter.PollOrCollect(ctx, start.Handle)
	if err == nil {
		t.Fatal("PollOrCollect() error = nil, want build error on corrupt structured output marker")
	}

	var adapterErr *provider.Error
	if !errors.As(err, &adapterErr) {
		t.Fatalf("PollOrCollect() error type = %T, want *provider.Error", err)
	}

	if adapterErr.Code != provider.ErrorCodeResult {
		t.Fatalf("PollOrCollect() error code = %q, want %q", adapterErr.Code, provider.ErrorCodeResult)
	}
}

// codexLastMessageStarter writes a configurable codex last-message file (where a
// real provider embeds AGENT_RESULT_JSON) while emitting an event stream that
// carries NO marker, proving the structured output came from the normalized
// last-message text rather than the raw stdout scan.
type codexLastMessageStarter struct {
	lastMessage string
}

func (s *codexLastMessageStarter) start(_ context.Context, req provider.ProcessRequest) (*provider.Session, error) {
	for i := 0; i+1 < len(req.Args); i++ {
		if req.Args[i] == "--output-last-message" {
			if err := os.WriteFile(req.Args[i+1], []byte(s.lastMessage), 0o600); err != nil {
				return nil, err
			}
		}
	}

	done := make(chan provider.ProcessResult, 1)
	done <- provider.ProcessResult{Stdout: []byte(strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-n1"}`,
		`{"type":"turn.completed"}`,
	}, "\n"))}
	close(done)

	return &provider.Session{Done: done}, nil
}

// TestCodexInterruptAfterTerminalDoesNotPanic is v3.2 N3: once a session has
// been collected and its heavy provider.Session released (record.session==nil), a
// late/racy Interrupt must not dereference the nil session. A naturally
// succeeded terminal is returned verbatim, never downgraded to interrupted. It
// also exercises N4 because the Interrupt is keyed off the REAL thread_id.
func TestCodexInterruptAfterTerminalDoesNotPanic(t *testing.T) {
	starter := &codexFakeStarter{}
	adapter := codex.New(codex.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/codex", nil },
		Runner:   &codexVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, provider.StartRequest{RunID: "run-1", StepID: "review", AttemptID: "attempt-1", WorkingDir: "/workspace/repo", Prompt: "do it"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	terminal, err := adapter.PollOrCollect(ctx, start.Handle)
	if err != nil {
		t.Fatalf("PollOrCollect() error = %v", err)
	}

	if terminal.State != provider.ExecutionStateSucceeded {
		t.Fatalf("PollOrCollect().State = %q, want succeeded", terminal.State)
	}

	interrupted, err := adapter.Interrupt(ctx, terminal.Handle)
	if err != nil {
		t.Fatalf("Interrupt() after terminal error = %v", err)
	}

	if interrupted.State != provider.ExecutionStateSucceeded {
		t.Fatalf("Interrupt() after natural success State = %q, want succeeded verbatim", interrupted.State)
	}
}

// TestCodexPollByRealSessionIDUsesAlias is v3.2 N4: after collectTerminal adopts
// the real thread_id, a PollOrCollect keyed off that real id must resolve via
// the alias entry instead of failing the session lookup.
func TestCodexPollByRealSessionIDUsesAlias(t *testing.T) {
	starter := &codexFakeStarter{}
	adapter := codex.New(codex.Config{
		LookPath: func(string) (string, error) { return "/usr/local/bin/codex", nil },
		Runner:   &codexVersionRunner{},
		Starter:  starter.start,
	})

	ctx := t.Context()
	start, err := adapter.Start(ctx, provider.StartRequest{RunID: "run-1", StepID: "review", AttemptID: "attempt-1", WorkingDir: "/workspace/repo", Prompt: "do it"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	terminal, err := adapter.PollOrCollect(ctx, start.Handle)
	if err != nil {
		t.Fatalf("PollOrCollect() error = %v", err)
	}

	if terminal.Handle.ProviderSessionID != "thread-123" {
		t.Fatalf("terminal ProviderSessionID = %q, want real id thread-123", terminal.Handle.ProviderSessionID)
	}

	again, err := adapter.PollOrCollect(ctx, terminal.Handle)
	if err != nil {
		t.Fatalf("PollOrCollect() by real id error = %v (alias entry missing)", err)
	}

	if again.State != provider.ExecutionStateSucceeded || again.Handle.ProviderSessionID != "thread-123" {
		t.Fatalf("re-poll by real id = %+v, want succeeded with real id", again.Handle)
	}
}

// decodeAgentResult unmarshals a normalized StructuredOutput payload into the
// shared AgentResult shape, failing the test when the payload is empty (which
// would mean the adapter dropped the marker instead of extracting it).
func decodeAgentResult(t *testing.T, raw json.RawMessage) prompt.AgentResult {
	t.Helper()

	if len(raw) == 0 {
		t.Fatal("StructuredOutput is empty, want normalized AgentResult JSON")
	}

	var result prompt.AgentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode StructuredOutput %s: %v", string(raw), err)
	}

	return result
}

// codexVersionRunner records the synchronous `codex --version` probe calls.
type codexVersionRunner struct {
	calls [][]string
}

func (r *codexVersionRunner) Run(_ context.Context, command codex.CommandSpec) (codex.CommandResult, error) {
	r.calls = append(r.calls, append([]string(nil), command.Args...))

	if reflect.DeepEqual(command.Args, []string{"--version"}) {
		return codex.CommandResult{Stdout: []byte("codex-cli 0.66.0\n")}, nil
	}

	return codex.CommandResult{}, nil
}

// codexFakeStarter captures the async StartRequest, writes the codex
// last-message file, and resolves Done with a canned JSON event stream.
type codexFakeStarter struct {
	req provider.ProcessRequest
}

func (s *codexFakeStarter) start(_ context.Context, req provider.ProcessRequest) (*provider.Session, error) {
	s.req = req

	for i := 0; i+1 < len(req.Args); i++ {
		if req.Args[i] == "--output-last-message" {
			if err := os.WriteFile(req.Args[i+1], []byte("All changes look good.\nNothing else to add.\n"), 0o600); err != nil {
				return nil, err
			}
		}
	}

	done := make(chan provider.ProcessResult, 1)
	done <- provider.ProcessResult{Stdout: []byte(strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-123"}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.completed"}`,
	}, "\n"))}
	close(done)

	return &provider.Session{Done: done}, nil
}
