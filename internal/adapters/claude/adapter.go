package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	shared "github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/adapters/adapterutil"
	"github.com/JackDrogon/Cogito/internal/adapters/prompt"
	"github.com/JackDrogon/Cogito/internal/adapters/runner"
)

const (
	ProviderName = "claude"
	binaryName   = "claude"
)

func init() {
	// Registration can only fail on deterministic composition bugs
	// (duplicate or invalid registration), never on runtime conditions.
	// Panicking at init makes such a bug unmissable in any test or first
	// launch, mirroring regexp.MustCompile semantics.
	if err := shared.Register(shared.Registration{
		Name:         ProviderName,
		Capabilities: Capabilities(),
		New: func() shared.Adapter {
			return New(Config{})
		},
		NewWithOptions: func(options shared.AdapterOptions) shared.Adapter {
			return New(Config{
				Model:    options.Model,
				LogDir:   options.LogDir,
				LiveSink: options.LiveSink,
			})
		},
	}); err != nil {
		panic(fmt.Sprintf("%s: register adapter: %v", ProviderName, err))
	}
}

// Starter launches the claude agent asynchronously. It defaults to runner.Start
// and is injectable so tests can substitute a fake process supervisor.
type Starter func(ctx context.Context, request runner.StartRequest) (*runner.Session, error)

type Config struct {
	LookPath func(string) (string, error)
	// Runner performs the synchronous one-shot `claude --version` probe.
	Runner Runner
	// Starter launches the async agent invocation; nil defaults to runner.Start.
	Starter Starter
	// Model overrides the claude model; empty omits the --model flag.
	Model string
	// LogDir is the directory for streamed process logs; empty skips logging.
	LogDir string
	// LiveSink optionally receives the process output stream in real time;
	// nil disables live streaming. Must be safe for concurrent writes.
	LiveSink io.Writer
}

type Adapter struct {
	lookPath func(string) (string, error)
	runner   Runner
	starter  Starter
	model    string
	logDir   string
	liveSink io.Writer

	mu       sync.RWMutex
	sessions map[string]*agentSession

	versionOnce sync.Once
	version     string
}

// agentSession holds the live runner session plus the metadata needed to
// assemble the terminal Execution once the process exits.
type agentSession struct {
	handle  shared.ExecutionHandle
	request shared.StartRequest
	version string
	session *runner.Session

	once sync.Once
	// terminalInterrupted records whether the runner actually fired a cancel
	// signal before the process exited. Interrupt uses it to avoid downgrading
	// a naturally-succeeded execution.
	terminalInterrupted bool
	terminal            *shared.Execution
	buildErr            error
}

type Runner = adapterutil.Runner

type CommandSpec = adapterutil.CommandSpec

type CommandResult = adapterutil.CommandResult

func New(config Config) *Adapter {
	lookPath := config.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	r := config.Runner
	if r == nil {
		r = adapterutil.ExecRunner{}
	}

	starter := config.Starter
	if starter == nil {
		starter = runner.Start
	}

	return &Adapter{
		lookPath: lookPath,
		runner:   r,
		starter:  starter,
		model:    strings.TrimSpace(config.Model),
		logDir:   strings.TrimSpace(config.LogDir),
		liveSink: config.LiveSink,
		sessions: map[string]*agentSession{},
	}
}

func Capabilities() shared.CapabilityMatrix {
	return shared.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true, Resume: true, Interrupt: true}
}

func (a *Adapter) DescribeCapabilities() shared.CapabilityMatrix {
	return Capabilities()
}

// Start launches the claude agent asynchronously and returns immediately with
// an ExecutionStateRunning execution. The terminal result is assembled later by
// PollOrCollect once the runner session completes.
func (a *Adapter) Start(ctx context.Context, request shared.StartRequest) (*shared.Execution, error) {
	if err := shared.ValidateStartRequest(request); err != nil {
		return nil, err
	}

	binaryPath, err := a.binaryPath()
	if err != nil {
		return nil, err
	}

	version := a.binaryVersion(ctx, binaryPath)

	args := buildCLIArgs(cliArgsParams{Model: a.model, ResumeSessionID: nil})

	session, startErr := a.starter(ctx, runner.StartRequest{
		Binary:        binaryPath,
		Args:          args,
		Dir:           adapterutil.CommandDir(request.WorkingDir),
		Prompt:        adapterutil.AgentPrompt(request),
		PromptOnStdin: true,
		LogPath:       a.logPath(request),
		ExtraSink:     a.liveSink,
	})
	if startErr != nil {
		return nil, adapterutil.AdapterError(shared.ErrorCodeExecution, "start claude print", startErr)
	}

	handle := shared.ExecutionHandle{
		RunID:             request.RunID,
		StepID:            request.StepID,
		AttemptID:         request.AttemptID,
		ProviderSessionID: providerSessionID(request, nil),
	}

	a.mu.Lock()
	a.sessions[handle.ProviderSessionID] = &agentSession{
		handle:  handle,
		request: request,
		version: version,
		session: session,
	}
	a.mu.Unlock()

	return &shared.Execution{
		Handle:  handle,
		State:   shared.ExecutionStateRunning,
		Summary: "claude print started",
	}, nil
}

// PollOrCollect blocks until the underlying runner session finishes, then
// parses the claude output into a terminal Execution. The result is cached so
// repeated polls are cheap and idempotent.
func (a *Adapter) PollOrCollect(_ context.Context, handle shared.ExecutionHandle) (*shared.Execution, error) {
	record, err := a.lookupSession(handle)
	if err != nil {
		return nil, err
	}

	execution, err := a.collectTerminal(record)
	if err != nil {
		return nil, err
	}

	return shared.CloneExecution(execution), nil
}

// Interrupt cancels the live runner session (SIGTERM → grace → SIGKILL) and
// returns the eventual terminal Execution marked as interrupted. The runtime
// then persists EventStepInterrupted so the step can be resumed.
//
// If the child process exited successfully before the cancel signal actually
// fired, the natural success is returned verbatim instead of being downgraded
// to interrupted: there is no work left to resume.
func (a *Adapter) Interrupt(_ context.Context, handle shared.ExecutionHandle) (*shared.Execution, error) {
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

	if execution.State == shared.ExecutionStateSucceeded && !record.terminalInterrupted {
		return shared.CloneExecution(execution), nil
	}

	interrupted := shared.CloneExecution(execution)
	interrupted.State = shared.ExecutionStateInterrupted

	return interrupted, nil
}

// interruptFromTerminal resolves an Interrupt that raced a finished session: the
// runner already exited and releaseSession released the session, so only the cached
// terminal remains. A terminal that already settled into Succeeded/Failed is
// returned verbatim (there is no work left to resume); a still-running terminal
// is flipped to Interrupted.
func interruptFromTerminal(terminal *shared.Execution) (*shared.Execution, error) {
	if terminal == nil {
		return nil, adapterutil.AdapterError(shared.ErrorCodeExecution, "claude execution session not found", nil)
	}

	clone := shared.CloneExecution(terminal)
	if clone.State == shared.ExecutionStateRunning {
		clone.State = shared.ExecutionStateInterrupted
	}

	return clone, nil
}

// Resume re-attaches to a prior claude session by appending `--resume <sid>` to
// the print argv. It mirrors Start's async flow: launch the runner, store the
// session under the same ProviderSessionID, and return ExecutionStateRunning so
// the runtime poll loop collects the terminal result via PollOrCollect.
func (a *Adapter) Resume(ctx context.Context, request shared.ResumeRequest) (*shared.Execution, error) {
	if err := a.DescribeCapabilities().Require(shared.CapabilityResume); err != nil {
		return nil, err
	}

	if err := shared.ValidateHandle(request.Handle); err != nil {
		return nil, err
	}

	binaryPath, err := a.binaryPath()
	if err != nil {
		return nil, err
	}

	version := a.binaryVersion(ctx, binaryPath)

	resumeStart := a.resumeStartRequest(request)
	sessionID := request.Handle.ProviderSessionID

	args := buildCLIArgs(cliArgsParams{Model: a.model, ResumeSessionID: &sessionID})

	session, startErr := a.starter(ctx, runner.StartRequest{
		Binary:        binaryPath,
		Args:          args,
		Dir:           adapterutil.CommandDir(resumeStart.WorkingDir),
		Prompt:        adapterutil.AgentPrompt(resumeStart),
		PromptOnStdin: true,
		LogPath:       a.logPath(resumeStart),
		ExtraSink:     a.liveSink,
	})
	if startErr != nil {
		return nil, adapterutil.AdapterError(shared.ErrorCodeExecution, "resume claude print", startErr)
	}

	a.mu.Lock()
	a.sessions[request.Handle.ProviderSessionID] = &agentSession{
		handle:  request.Handle,
		request: resumeStart,
		version: version,
		session: session,
	}
	a.mu.Unlock()

	return &shared.Execution{
		Handle:  request.Handle,
		State:   shared.ExecutionStateRunning,
		Summary: "claude print resumed",
	}, nil
}

// resumeStartRequest reconstructs a StartRequest from the resume handle, pulling
// the original working directory (and prompt fallback) from the live session map
// when the resume happens in the same process.
func (a *Adapter) resumeStartRequest(request shared.ResumeRequest) shared.StartRequest {
	// Prefer the WorkingDir threaded on the resume request (works across a fresh
	// `cogito resume` process); fall back to the live session map only for an
	// in-process resume that did not carry one.
	var priorRequest shared.StartRequest

	a.mu.Lock()
	if prior, ok := a.sessions[request.Handle.ProviderSessionID]; ok {
		priorRequest = prior.request
	}
	a.mu.Unlock()

	return adapterutil.ResumeStartRequest(request, priorRequest)
}

func (a *Adapter) NormalizeResult(_ context.Context, request shared.NormalizeRequest) (*shared.StepResult, error) {
	return shared.NormalizeResult(request, a.DescribeCapabilities())
}

func (a *Adapter) binaryPath() (string, error) {
	path, err := a.lookPath(binaryName)
	if err == nil {
		return path, nil
	}

	if errors.Is(err, exec.ErrNotFound) {
		return "", adapterutil.AdapterError(shared.ErrorCodeExecution, "claude binary not found", err)
	}

	return "", adapterutil.AdapterError(shared.ErrorCodeExecution, "locate claude binary", err)
}

func (a *Adapter) binaryVersion(ctx context.Context, binaryPath string) string {
	a.versionOnce.Do(func() {
		result, err := a.runner.Run(ctx, adapterutil.CommandSpec{Path: binaryPath, Args: []string{"--version"}})
		if err != nil {
			// The probe failing is non-fatal (version is cosmetic), but the
			// cause should not vanish into an unread struct field.
			slog.Warn("claude: version probe failed", "err", err)

			a.version = adapterutil.VersionUnknown

			return
		}

		version := strings.TrimSpace(string(result.Stdout))
		if version == "" {
			version = strings.TrimSpace(string(result.Stderr))
		}

		if version == "" {
			version = adapterutil.VersionUnknown
		}

		a.version = version
	})

	if a.version == "" {
		return adapterutil.VersionUnknown
	}

	return a.version
}

// cliArgsParams carries the claude print argv inputs. ResumeSessionID is nil
// for fresh starts; L2 will pass a non-nil session id to append `--resume <sid>`.
type cliArgsParams struct {
	Model           string
	ResumeSessionID *string
}

// buildCLIArgs assembles the claude print argv. The prompt is delivered on
// stdin, so it never appears here. Shape:
//
//	--print --permission-mode bypassPermissions --output-format json
//	[--model <m>] [--resume <sid>]
func buildCLIArgs(params cliArgsParams) []string {
	args := []string{"--print", "--permission-mode", "bypassPermissions", "--output-format", "json"}

	if model := strings.TrimSpace(params.Model); model != "" {
		args = append(args, "--model", model)
	}

	if params.ResumeSessionID != nil {
		if sessionID := strings.TrimSpace(*params.ResumeSessionID); sessionID != "" {
			args = append(args, "--resume", sessionID)
		}
	}

	return args
}

func (a *Adapter) logPath(request shared.StartRequest) string {
	if a.logDir == "" {
		return ""
	}

	return filepath.Join(a.logDir, adapterutil.SanitizeID(request.AttemptID)+"-claude.log")
}

func (a *Adapter) lookupSession(handle shared.ExecutionHandle) (*agentSession, error) {
	if err := shared.ValidateHandle(handle); err != nil {
		return nil, err
	}

	a.mu.RLock()
	record, ok := a.sessions[handle.ProviderSessionID]
	a.mu.RUnlock()

	if !ok {
		return nil, adapterutil.AdapterError(shared.ErrorCodeExecution, "claude execution session not found", nil)
	}

	if record.handle.RunID != handle.RunID || record.handle.StepID != handle.StepID || record.handle.AttemptID != handle.AttemptID {
		return nil, adapterutil.AdapterError(shared.ErrorCodeExecution, "claude execution handle does not match session", nil)
	}

	return record, nil
}

// collectTerminal awaits the runner session exactly once and folds the result
// into a terminal Execution using the existing response parsing.
//
// The terminal handle keeps the run/step/attempt identifiers from the stored
// handle but adopts the real provider session id when claude reported one (the
// response session_id): that real id is what `--resume <sid>` needs, so
// overwriting it with the synthetic Start-time fallback would make resume fail.
// Once the terminal is built the heavy runner session is released (see releaseSession).
func (a *Adapter) collectTerminal(record *agentSession) (*shared.Execution, error) {
	record.once.Do(func() {
		result := <-record.session.Done
		record.terminalInterrupted = result.Interrupted

		response, parseErr := parseResponse(result.Stdout)
		if parseErr != nil {
			record.buildErr = adapterutil.AdapterError(shared.ErrorCodeResult, "parse claude json output", parseErr)
			return
		}

		execution := buildExecution(executionParams{
			Request:  record.request,
			Version:  record.version,
			Response: response,
			Stderr:   result.Stderr,
		})

		// buildExecution already extracted the real session id into the handle.
		// Restore run/step/attempt from the stored handle, but keep the parsed
		// id whenever it differs from the synthetic fallback.
		syntheticID := record.handle.ProviderSessionID
		parsedSessionID := execution.Handle.ProviderSessionID
		execution.Handle = record.handle

		if parsedSessionID != "" && parsedSessionID != syntheticID {
			execution.Handle.ProviderSessionID = parsedSessionID
			a.aliasSession(record, parsedSessionID)
		}

		structured, soutErr := a.structuredOutput(response, result)
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
		return nil, adapterutil.AdapterError(shared.ErrorCodeExecution, "claude terminal execution unavailable", nil)
	}

	return record.terminal, nil
}

// structuredOutput recovers the normalized AgentResult JSON for a finished
// claude invocation. It prefers the normalized assistant text (the parsed
// response.Result field), where a real provider embeds the AGENT_RESULT_JSON
// marker, and only falls back to scanning the raw log/stdout for fake binaries
// that place the marker directly on stdout (N1). A corrupt marker or read fault
// is surfaced as a build error rather than silently dropped (N2).
func (a *Adapter) structuredOutput(response *response, result runner.Result) (json.RawMessage, error) {
	var normalized string
	if response != nil {
		normalized = response.Result
	}

	structured, err := prompt.StructuredOutputFromText(normalized)
	if err == nil {
		return structured, nil
	}

	if !errors.Is(err, prompt.ErrNoAgentResult) {
		return nil, adapterutil.AdapterError(shared.ErrorCodeResult, "parse claude structured output", err)
	}

	structured, err = shared.StructuredOutputFromLog(result.LogPath, result.Stdout)
	if err != nil {
		// No marker anywhere is clean "no data": downstream gates treat the
		// absent structured output explicitly instead of failing the build.
		if errors.Is(err, prompt.ErrNoAgentResult) {
			return nil, nil
		}

		return nil, adapterutil.AdapterError(shared.ErrorCodeResult, "read claude structured output", err)
	}

	return structured, nil
}

// aliasSession registers an additional sessions-map key pointing at the same
// record so a PollOrCollect/Interrupt/Resume keyed off the real provider session
// id (which the terminal handle now carries and which the runtime persists)
// still resolves. Both the synthetic and real keys alias one record (N4).
func (a *Adapter) aliasSession(record *agentSession, aliasID string) {
	adapterutil.SessionMap[*agentSession]{Mu: &a.mu, Sessions: a.sessions}.Alias(record, aliasID)
}

// releaseSession releases the live runner session a finished invocation no longer
// needs. The terminal Execution and terminalInterrupted flag stay on the record
// for idempotent re-polls; only the heavy runner.Session reference (and the
// stdout/stderr it buffered) is dropped, so long-running engines do not
// accumulate finished sessions.
//
// Both the synthetic and real (aliased) session-map keys point at the same
// record, so nilling record.session here clears the heavy reference for every
// key under which the record is reachable. Safe to call repeatedly.
func (a *Adapter) releaseSession(sessionID string) {
	adapterutil.SessionMap[*agentSession]{Mu: &a.mu, Sessions: a.sessions}.Release(sessionID, func(record *agentSession) {
		record.session = nil
	})
}
