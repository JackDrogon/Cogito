package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JackDrogon/Cogito/internal/prompt"
	"github.com/JackDrogon/Cogito/internal/provider"
)

const (
	ProviderName = "codex"
	binaryName   = "codex"
	// DefaultSandbox mirrors AgentLoop's CODEX_SANDBOX default. It grants the
	// agent full access because Cogito already isolates the working directory
	// via the runner's GIT_CEILING_DIRECTORIES guard.
	DefaultSandbox = "danger-full-access"
)

func init() {
	// Registration can only fail on deterministic composition bugs
	// (duplicate or invalid registration), never on runtime conditions.
	// Panicking at init makes such a bug unmissable in any test or first
	// launch, mirroring regexp.MustCompile semantics.
	if err := provider.Register(provider.Registration{
		Name:         ProviderName,
		Capabilities: Capabilities(),
		New: func() provider.Provider {
			return New(Config{})
		},
		NewWithOptions: func(options provider.Options) provider.Provider {
			return New(Config{
				Sandbox:     options.Sandbox,
				Model:       options.Model,
				LogDir:      options.LogDir,
				LiveSink:    options.LiveSink,
				Timeout:     options.Timeout,
				IdleTimeout: options.IdleTimeout,
			})
		},
	}); err != nil {
		panic(fmt.Sprintf("%s: register adapter: %v", ProviderName, err))
	}
}

// Starter launches the codex agent asynchronously. It defaults to provider.StartProcess
// and is injectable so tests can substitute a fake process supervisor.
type Starter func(ctx context.Context, request provider.ProcessRequest) (*provider.Session, error)

type Config struct {
	LookPath func(string) (string, error)
	// Runner performs the synchronous one-shot `codex --version` probe.
	Runner Runner
	// Starter launches the async agent invocation; nil defaults to provider.StartProcess.
	Starter Starter
	// Sandbox selects the codex sandbox mode; empty defaults to DefaultSandbox.
	Sandbox string
	// Model overrides the codex model; empty omits the --model flag.
	Model string
	// LogDir is the directory for streamed process logs; empty skips logging.
	LogDir string
	// LiveSink optionally receives the process output stream in real time;
	// nil disables live streaming. Must be safe for concurrent writes.
	LiveSink io.Writer
	// Timeout is the maximum agent process wall-clock duration; <=0 disables it.
	Timeout time.Duration
	// IdleTimeout is the maximum no-output duration; <=0 disables it.
	IdleTimeout time.Duration
}

type Adapter struct {
	lookPath func(string) (string, error)
	runner   Runner
	starter  Starter
	sandbox  string
	model    string
	logDir   string
	liveSink io.Writer
	timeout  time.Duration
	idle     time.Duration

	mu       sync.RWMutex
	sessions map[string]*agentSession

	versionOnce sync.Once
	version     string
}

// agentSession holds the live runner session plus the metadata needed to
// assemble the terminal Execution once the process exits.
type agentSession struct {
	handle      provider.ExecutionHandle
	request     provider.StartRequest
	version     string
	session     *provider.Session
	lastMsgPath string
	// removeLastMsgDir deletes the temp dir holding codex's --output-last-message
	// file. Distinct from Adapter.releaseSession, which drops the heavy runner
	// session reference from the session map.
	removeLastMsgDir func()

	once sync.Once
	// terminalInterrupted records whether the runner actually fired a cancel
	// signal before the process exited. Interrupt uses it to avoid downgrading
	// a naturally-succeeded execution.
	terminalInterrupted bool
	terminal            *provider.Execution
	buildErr            error
}

type Runner = provider.CommandRunner

type CommandSpec = provider.CommandSpec

type CommandResult = provider.CommandResult

type lastMessagePathResult struct {
	path   string
	remove func()
}

func New(config Config) *Adapter {
	lookPath := config.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	r := config.Runner
	if r == nil {
		r = provider.ExecRunner{}
	}

	starter := config.Starter
	if starter == nil {
		starter = provider.StartProcess
	}

	sandbox := strings.TrimSpace(config.Sandbox)
	if sandbox == "" {
		sandbox = DefaultSandbox
	}

	return &Adapter{
		lookPath: lookPath,
		runner:   r,
		starter:  starter,
		sandbox:  sandbox,
		model:    strings.TrimSpace(config.Model),
		logDir:   strings.TrimSpace(config.LogDir),
		liveSink: config.LiveSink,
		timeout:  config.Timeout,
		idle:     config.IdleTimeout,
		sessions: map[string]*agentSession{},
	}
}

func Capabilities() provider.CapabilityMatrix {
	return provider.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true, Resume: true, Interrupt: true}
}

func (a *Adapter) DescribeCapabilities() provider.CapabilityMatrix {
	return Capabilities()
}

// Start launches the codex agent asynchronously and returns immediately with
// an ExecutionStateRunning execution. The terminal result is assembled later
// by PollOrCollect once the runner session completes.
func (a *Adapter) Start(ctx context.Context, request provider.StartRequest) (*provider.Execution, error) {
	if err := provider.ValidateStartRequest(request); err != nil {
		return nil, err
	}

	binaryPath, err := a.binaryPath()
	if err != nil {
		return nil, err
	}

	version := a.binaryVersion(ctx, binaryPath)

	lastMessageResult, err := makeLastMessagePath()
	if err != nil {
		return nil, provider.NewError(provider.ErrorCodeExecution, "prepare codex output path", err)
	}

	args := buildExecArgs(execArgsParams{
		WorkingDir:      provider.CommandDir(request.WorkingDir),
		LastMessagePath: lastMessageResult.path,
		Sandbox:         a.sandbox,
		Model:           a.model,
		ResumeSessionID: nil,
	})

	emitPromptBanner(a.liveSink, provider.AgentPrompt(request))

	session, startErr := a.starter(ctx, provider.ProcessRequest{
		Binary:        binaryPath,
		Args:          args,
		Dir:           provider.CommandDir(request.WorkingDir),
		Prompt:        provider.AgentPrompt(request),
		PromptOnStdin: true,
		LogPath:       a.logPath(request),
		ExtraSink:     newLiveRenderer(a.liveSink),
		Timeout:       a.timeout,
		IdleTimeout:   a.idle,
	})
	if startErr != nil {
		lastMessageResult.remove()
		return nil, provider.NewError(provider.ErrorCodeExecution, "start codex exec", startErr)
	}

	handle := provider.ExecutionHandle{
		RunID:             request.RunID,
		StepID:            request.StepID,
		AttemptID:         request.AttemptID,
		ProviderSessionID: providerSessionID(request, nil),
	}

	a.mu.Lock()
	a.sessions[handle.ProviderSessionID] = &agentSession{
		handle:           handle,
		request:          request,
		version:          version,
		session:          session,
		lastMsgPath:      lastMessageResult.path,
		removeLastMsgDir: lastMessageResult.remove,
	}
	a.mu.Unlock()

	return &provider.Execution{
		Handle:  handle,
		State:   provider.ExecutionStateRunning,
		Summary: "codex exec started",
	}, nil
}

// PollOrCollect blocks until the underlying runner session finishes, then
// parses the codex output into a terminal Execution. The result is cached so
// repeated polls are cheap and idempotent.
func (a *Adapter) PollOrCollect(_ context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	record, err := a.lookupSession(handle)
	if err != nil {
		return nil, err
	}

	execution, err := a.collectTerminal(record)
	if err != nil {
		return nil, err
	}

	return provider.CloneExecution(execution), nil
}

// Interrupt cancels the live runner session (SIGTERM → grace → SIGKILL) and
// returns the eventual terminal Execution marked as interrupted. The runtime
// then persists EventStepInterrupted so the step can be resumed.
//
// If the child process exited successfully before the cancel signal actually
// fired, the natural success is returned verbatim instead of being downgraded
// to interrupted: there is no work left to resume.
func (a *Adapter) Interrupt(_ context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	record, err := a.lookupSession(handle)
	if err != nil {
		return nil, err
	}

	// Snapshot session + terminal together under the lock. releaseSession nils the
	// session AFTER publishing record.terminal (both under a.mu via releaseSession), so
	// observing session==nil here guarantees terminal is visible too.
	a.mu.RLock()

	session := record.session
	terminal := record.terminal

	a.mu.RUnlock()

	// The live session was already released by releaseSession: the process finished and
	// the terminal is cached. There is nothing to cancel, so resolve from the
	// cached terminal instead of dereferencing a nil session (N3).
	if session == nil {
		return interruptFromTerminal(terminal)
	}

	session.Cancel()

	execution, err := a.collectTerminal(record)
	if err != nil {
		return nil, err
	}

	if execution.State == provider.ExecutionStateSucceeded && !record.terminalInterrupted {
		return provider.CloneExecution(execution), nil
	}

	interrupted := provider.CloneExecution(execution)
	interrupted.State = provider.ExecutionStateInterrupted

	return interrupted, nil
}

// interruptFromTerminal resolves an Interrupt that raced a finished session: the
// runner already exited and releaseSession released the session, so only the cached
// terminal remains. A terminal that already settled into Succeeded/Failed is
// returned verbatim (there is no work left to resume); a still-running terminal
// is flipped to Interrupted.
func interruptFromTerminal(terminal *provider.Execution) (*provider.Execution, error) {
	if terminal == nil {
		return nil, provider.NewError(provider.ErrorCodeExecution, "codex execution session not found", nil)
	}

	clone := provider.CloneExecution(terminal)
	if clone.State == provider.ExecutionStateRunning {
		clone.State = provider.ExecutionStateInterrupted
	}

	return clone, nil
}

// Resume re-attaches to a prior codex session by appending `resume <sid>` to the
// exec argv. It mirrors Start's async flow: launch the runner, store the session
// under the same ProviderSessionID, and return ExecutionStateRunning so the
// runtime poll loop collects the terminal result via PollOrCollect.
func (a *Adapter) Resume(ctx context.Context, request provider.ResumeRequest) (*provider.Execution, error) {
	if err := a.DescribeCapabilities().Require(provider.CapabilityResume); err != nil {
		return nil, err
	}

	if err := provider.ValidateHandle(request.Handle); err != nil {
		return nil, err
	}

	binaryPath, err := a.binaryPath()
	if err != nil {
		return nil, err
	}

	version := a.binaryVersion(ctx, binaryPath)

	lastMessageResult, err := makeLastMessagePath()
	if err != nil {
		return nil, provider.NewError(provider.ErrorCodeExecution, "prepare codex output path", err)
	}

	resumeStart := a.resumeStartRequest(request)
	sessionID := request.Handle.ProviderSessionID

	args := buildExecArgs(execArgsParams{
		WorkingDir:      provider.CommandDir(resumeStart.WorkingDir),
		LastMessagePath: lastMessageResult.path,
		Sandbox:         a.sandbox,
		Model:           a.model,
		ResumeSessionID: &sessionID,
	})

	emitPromptBanner(a.liveSink, provider.AgentPrompt(resumeStart))

	session, startErr := a.starter(ctx, provider.ProcessRequest{
		Binary:        binaryPath,
		Args:          args,
		Dir:           provider.CommandDir(resumeStart.WorkingDir),
		Prompt:        provider.AgentPrompt(resumeStart),
		PromptOnStdin: true,
		LogPath:       a.logPath(resumeStart),
		ExtraSink:     newLiveRenderer(a.liveSink),
		Timeout:       a.timeout,
		IdleTimeout:   a.idle,
	})
	if startErr != nil {
		lastMessageResult.remove()
		return nil, provider.NewError(provider.ErrorCodeExecution, "resume codex exec", startErr)
	}

	a.mu.Lock()
	a.sessions[request.Handle.ProviderSessionID] = &agentSession{
		handle:           request.Handle,
		request:          resumeStart,
		version:          version,
		session:          session,
		lastMsgPath:      lastMessageResult.path,
		removeLastMsgDir: lastMessageResult.remove,
	}
	a.mu.Unlock()

	return &provider.Execution{
		Handle:  request.Handle,
		State:   provider.ExecutionStateRunning,
		Summary: "codex exec resumed",
	}, nil
}

// resumeStartRequest reconstructs a StartRequest from the resume handle, pulling
// the original working directory (and prompt fallback) from the live session map
// when the resume happens in the same process.
func (a *Adapter) resumeStartRequest(request provider.ResumeRequest) provider.StartRequest {
	// Prefer the WorkingDir threaded on the resume request (works across a fresh
	// `cogito resume` process); fall back to the live session map only for an
	// in-process resume that did not carry one.
	var priorRequest provider.StartRequest

	a.mu.Lock()
	if prior, ok := a.sessions[request.Handle.ProviderSessionID]; ok {
		priorRequest = prior.request
	}
	a.mu.Unlock()

	return provider.ResumeStartRequest(request, priorRequest)
}

func (a *Adapter) NormalizeResult(_ context.Context, request provider.NormalizeRequest) (*provider.StepResult, error) {
	return provider.NormalizeResult(request, a.DescribeCapabilities())
}

func (a *Adapter) binaryPath() (string, error) {
	path, err := a.lookPath(binaryName)
	if err == nil {
		return path, nil
	}

	if errors.Is(err, exec.ErrNotFound) {
		return "", provider.NewError(provider.ErrorCodeExecution, "codex binary not found", err)
	}

	return "", provider.NewError(provider.ErrorCodeExecution, "locate codex binary", err)
}

func (a *Adapter) binaryVersion(ctx context.Context, binaryPath string) string {
	a.versionOnce.Do(func() {
		result, err := a.runner.Run(ctx, provider.CommandSpec{Path: binaryPath, Args: []string{"--version"}})
		if err != nil {
			// The probe failing is non-fatal (version is cosmetic), but the
			// cause should not vanish into an unread struct field.
			slog.Warn("codex: version probe failed", "err", err)

			a.version = provider.VersionUnknown

			return
		}

		version := strings.TrimSpace(string(result.Stdout))
		if version == "" {
			version = strings.TrimSpace(string(result.Stderr))
		}

		if version == "" {
			version = provider.VersionUnknown
		}

		a.version = version
	})

	if a.version == "" {
		return provider.VersionUnknown
	}

	return a.version
}

// execArgsParams carries the codex exec argv inputs. ResumeSessionID is nil for
// fresh starts; L2 will pass a non-nil session id to append `resume <sid>`.
type execArgsParams struct {
	WorkingDir      string
	LastMessagePath string
	Sandbox         string
	Model           string
	ResumeSessionID *string
}

// buildExecArgs assembles the codex exec argv. The prompt is delivered on stdin
// (trailing `-`), so it never appears here. Shape:
//
//	exec --cd <dir> --sandbox <sandbox> --skip-git-repo-check --json
//	--color never --output-last-message <path> [--model <m>] [resume <sid>] -
func buildExecArgs(params execArgsParams) []string {
	args := []string{
		"exec",
		"--cd", params.WorkingDir,
		"--sandbox", params.Sandbox,
		"--skip-git-repo-check",
		"--json",
		"--color", "never",
		"--output-last-message", params.LastMessagePath,
	}

	if model := strings.TrimSpace(params.Model); model != "" {
		args = append(args, "--model", model)
	}

	if params.ResumeSessionID != nil {
		if sessionID := strings.TrimSpace(*params.ResumeSessionID); sessionID != "" {
			args = append(args, "resume", sessionID)
		}
	}

	return append(args, "-")
}

func (a *Adapter) logPath(request provider.StartRequest) string {
	if a.logDir == "" {
		return ""
	}

	return filepath.Join(a.logDir, provider.SanitizeID(request.AttemptID)+"-codex.log")
}

func (a *Adapter) lookupSession(handle provider.ExecutionHandle) (*agentSession, error) {
	if err := provider.ValidateHandle(handle); err != nil {
		return nil, err
	}

	a.mu.RLock()
	record, ok := a.sessions[handle.ProviderSessionID]
	a.mu.RUnlock()

	if !ok {
		return nil, provider.NewError(provider.ErrorCodeExecution, "codex execution session not found", nil)
	}

	if record.handle.RunID != handle.RunID || record.handle.StepID != handle.StepID || record.handle.AttemptID != handle.AttemptID {
		return nil, provider.NewError(provider.ErrorCodeExecution, "codex execution handle does not match session", nil)
	}

	return record, nil
}

// collectTerminal awaits the runner session exactly once and folds the result
// into a terminal Execution using the existing event/last-message parsing.
//
// The terminal handle keeps the run/step/attempt identifiers from the stored
// handle but adopts the real provider session id when codex reported one (a
// thread_id in the event stream): that real id is what `resume <sid>` needs, so
// overwriting it with the synthetic Start-time fallback would make resume fail.
// Once the terminal is built the heavy runner session is released (see releaseSession).
func (a *Adapter) collectTerminal(record *agentSession) (*provider.Execution, error) {
	record.once.Do(func() {
		defer record.removeLastMsgDir()

		result := <-record.session.Done

		record.terminalInterrupted = result.Interrupted
		if result.TimeoutReason != "" {
			record.terminal = timeoutExecution(record.handle, result.TimeoutReason)
			a.releaseSession(record.handle.ProviderSessionID)

			return
		}

		events, parseErr := parseEvents(result.Stdout)
		if parseErr != nil {
			record.buildErr = provider.NewError(provider.ErrorCodeResult, "parse codex json output", parseErr)
			return
		}

		lastMessage, readErr := os.ReadFile(filepath.Clean(record.lastMsgPath))
		if readErr != nil {
			record.buildErr = provider.NewError(provider.ErrorCodeExecution, "read codex output message", readErr)
			return
		}

		execution := buildExecution(executionParams{
			Request:     record.request,
			Version:     record.version,
			Events:      events,
			LastMessage: lastMessage,
			Stderr:      result.Stderr,
		})

		// buildExecution already extracted the real session id (thread_id) into
		// the handle. Restore run/step/attempt from the stored handle, but keep
		// the parsed id whenever it differs from the synthetic fallback.
		syntheticID := record.handle.ProviderSessionID
		parsedSessionID := execution.Handle.ProviderSessionID
		execution.Handle = record.handle

		if parsedSessionID != "" && parsedSessionID != syntheticID {
			execution.Handle.ProviderSessionID = parsedSessionID
			a.aliasSession(record, parsedSessionID)
		}

		structured, soutErr := a.structuredOutput(lastMessage, result)
		if soutErr != nil {
			record.buildErr = soutErr
			return
		}

		execution.StructuredOutput = structured

		record.terminal = execution

		// Release the live runner session + buffered stdout/stderr now that the
		// terminal Execution captured everything we need.
		a.releaseSession(syntheticID)
	})

	if record.buildErr != nil {
		return nil, record.buildErr
	}

	if record.terminal == nil {
		return nil, provider.NewError(provider.ErrorCodeExecution, "codex terminal execution unavailable", nil)
	}

	return record.terminal, nil
}

func timeoutExecution(handle provider.ExecutionHandle, reason string) *provider.Execution {
	return &provider.Execution{
		Handle:  handle,
		State:   provider.ExecutionStateFailed,
		Summary: reason,
	}
}

// structuredOutput recovers the normalized AgentResult JSON for a finished codex
// invocation. It prefers the normalized assistant text (codex's last-message
// file), where a real provider embeds the AGENT_RESULT_JSON marker, and only
// falls back to scanning the raw event log/stdout for fake binaries that place
// the marker directly on stdout (N1). A corrupt marker or read fault is surfaced
// as a build error rather than silently dropped (N2).
func (a *Adapter) structuredOutput(lastMessage []byte, result provider.ProcessResult) (json.RawMessage, error) {
	structured, err := prompt.StructuredOutputFromText(string(lastMessage))
	if err == nil {
		return structured, nil
	}

	if !errors.Is(err, prompt.ErrNoAgentResult) {
		return nil, provider.NewError(provider.ErrorCodeResult, "parse codex structured output", err)
	}

	structured, err = provider.StructuredOutputFromLog(result.LogPath, result.Stdout)
	if err != nil {
		// No marker anywhere is clean "no data": downstream gates treat the
		// absent structured output explicitly instead of failing the build.
		if errors.Is(err, prompt.ErrNoAgentResult) {
			return nil, nil
		}

		return nil, provider.NewError(provider.ErrorCodeResult, "read codex structured output", err)
	}

	return structured, nil
}

// aliasSession registers an additional sessions-map key pointing at the same
// record so a PollOrCollect/Interrupt/Resume keyed off the real provider session
// id (which the terminal handle now carries and which the runtime persists)
// still resolves. Both the synthetic and real keys alias one record (N4).
func (a *Adapter) aliasSession(record *agentSession, aliasID string) {
	provider.SessionMap[*agentSession]{Mu: &a.mu, Sessions: a.sessions}.Alias(record, aliasID)
}

// releaseSession releases the live runner session a finished invocation no longer
// needs. The terminal Execution and terminalInterrupted flag stay on the record
// for idempotent re-polls; only the heavy provider.Session reference (and the
// stdout/stderr it buffered) is dropped, so long-running engines do not
// accumulate finished sessions.
//
// Both the synthetic and real (aliased) session-map keys point at the same
// record, so nilling record.session here clears the heavy reference for every
// key under which the record is reachable. Safe to call repeatedly.
func (a *Adapter) releaseSession(sessionID string) {
	provider.SessionMap[*agentSession]{Mu: &a.mu, Sessions: a.sessions}.Release(sessionID, func(record *agentSession) {
		record.session = nil
	})
}

func makeLastMessagePath() (*lastMessagePathResult, error) {
	dir, err := os.MkdirTemp("", "cogito-codex-")
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, "last-message.txt")
	remove := func() {
		_ = os.RemoveAll(dir)
	}

	return &lastMessagePathResult{path: path, remove: remove}, nil
}
