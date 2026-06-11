package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	ProviderName   = "opencode"
	versionUnknown = "unknown"
)

var binaryCandidates = []string{"opencode", "opencode-desktop"}

func init() {
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
		panic(err)
	}
}

// Starter launches the opencode agent asynchronously. It defaults to
// runner.Start and is injectable so tests can substitute a fake supervisor.
type Starter func(ctx context.Context, request runner.StartRequest) (*runner.Session, error)

type Config struct {
	LookPath func(string) (string, error)
	// Runner performs the synchronous one-shot `opencode --version` probe.
	Runner Runner
	// Starter launches the async agent invocation; nil defaults to runner.Start.
	Starter Starter
	// Model overrides the opencode model; empty omits the --model flag.
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

	mu       sync.Mutex
	sessions map[string]*agentSession

	versionOnce sync.Once
	version     string
	versionErr  error
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

type Runner interface {
	Run(ctx context.Context, command CommandSpec) (CommandResult, error)
}

type CommandSpec struct {
	Path   string
	Args   []string
	Dir    string
	Stdin  string
	Stdout io.Writer
	Stderr io.Writer
}

type CommandResult struct {
	Stdout []byte
	Stderr []byte
}

func New(config Config) *Adapter {
	lookPath := config.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	r := config.Runner
	if r == nil {
		r = execRunner{}
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

// Start launches the opencode agent asynchronously and returns immediately with
// an ExecutionStateRunning execution. The terminal result is assembled later by
// PollOrCollect once the runner session completes.
func (a *Adapter) Start(ctx context.Context, request shared.StartRequest) (*shared.Execution, error) {
	if err := adapterutil.ValidateStartRequest(request); err != nil {
		return nil, err
	}

	binaryPath, err := a.binaryPath()
	if err != nil {
		return nil, err
	}

	version := a.binaryVersion(ctx, binaryPath)

	args := buildRunArgs(runArgsParams{
		WorkingDir:      commandDir(request.WorkingDir),
		Prompt:          agentPrompt(request),
		Model:           a.model,
		ResumeSessionID: nil,
	})

	session, startErr := a.starter(ctx, runner.StartRequest{
		Binary:        binaryPath,
		Args:          args,
		Dir:           commandDir(request.WorkingDir),
		Prompt:        agentPrompt(request),
		PromptOnStdin: false,
		LogPath:       a.logPath(request),
		ExtraSink:     a.liveSink,
	})
	if startErr != nil {
		return nil, adapterError(shared.ErrorCodeExecution, "start opencode session", startErr)
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
		Summary: "opencode session started",
	}, nil
}

// PollOrCollect blocks until the underlying runner session finishes, then
// parses the opencode output into a terminal Execution. The result is cached so
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

	return adapterutil.CloneExecution(execution), nil
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

	// Snapshot session + terminal together under the lock. cleanup nils the
	// session AFTER publishing record.terminal (both under a.mu via cleanup), so
	// observing session==nil here guarantees terminal is visible too.
	a.mu.Lock()
	session := record.session
	terminal := record.terminal
	a.mu.Unlock()

	// The live session was already released by cleanup: the process finished and
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
		return adapterutil.CloneExecution(execution), nil
	}

	interrupted := adapterutil.CloneExecution(execution)
	interrupted.State = shared.ExecutionStateInterrupted

	return interrupted, nil
}

// interruptFromTerminal resolves an Interrupt that raced a finished session: the
// runner already exited and cleanup released the session, so only the cached
// terminal remains. A terminal that already settled into Succeeded/Failed is
// returned verbatim (there is no work left to resume); a still-running terminal
// is flipped to Interrupted.
func interruptFromTerminal(terminal *shared.Execution) (*shared.Execution, error) {
	if terminal == nil {
		return nil, adapterError(shared.ErrorCodeExecution, "opencode execution session not found", nil)
	}

	clone := adapterutil.CloneExecution(terminal)
	if clone.State == shared.ExecutionStateRunning {
		clone.State = shared.ExecutionStateInterrupted
	}

	return clone, nil
}

// Resume re-attaches to a prior opencode session by appending `--session <sid>`
// before the prompt argv. It mirrors Start's async flow: launch the runner,
// store the session under the same ProviderSessionID, and return
// ExecutionStateRunning so the runtime poll loop collects via PollOrCollect.
func (a *Adapter) Resume(ctx context.Context, request shared.ResumeRequest) (*shared.Execution, error) {
	if err := a.DescribeCapabilities().Require(shared.CapabilityResume); err != nil {
		return nil, err
	}

	if err := adapterutil.ValidateHandle(request.Handle); err != nil {
		return nil, err
	}

	binaryPath, err := a.binaryPath()
	if err != nil {
		return nil, err
	}

	version := a.binaryVersion(ctx, binaryPath)

	resumeStart := a.resumeStartRequest(request)
	sessionID := request.Handle.ProviderSessionID

	args := buildRunArgs(runArgsParams{
		WorkingDir:      commandDir(resumeStart.WorkingDir),
		Prompt:          agentPrompt(resumeStart),
		Model:           a.model,
		ResumeSessionID: &sessionID,
	})

	session, startErr := a.starter(ctx, runner.StartRequest{
		Binary:        binaryPath,
		Args:          args,
		Dir:           commandDir(resumeStart.WorkingDir),
		Prompt:        agentPrompt(resumeStart),
		PromptOnStdin: false,
		LogPath:       a.logPath(resumeStart),
		ExtraSink:     a.liveSink,
	})
	if startErr != nil {
		return nil, adapterError(shared.ErrorCodeExecution, "resume opencode session", startErr)
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
		Summary: "opencode session resumed",
	}, nil
}

// resumeStartRequest reconstructs a StartRequest from the resume handle, pulling
// the original working directory (and prompt fallback) from the live session map
// when the resume happens in the same process.
func (a *Adapter) resumeStartRequest(request shared.ResumeRequest) shared.StartRequest {
	resume := shared.StartRequest{
		RunID:      request.Handle.RunID,
		StepID:     request.Handle.StepID,
		AttemptID:  request.Handle.AttemptID,
		Prompt:     request.Prompt,
		WorkingDir: strings.TrimSpace(request.WorkingDir),
	}

	// Prefer the WorkingDir threaded on the resume request (works across a fresh
	// `cogito resume` process); fall back to the live session map only for an
	// in-process resume that did not carry one.
	a.mu.Lock()
	if prior, ok := a.sessions[request.Handle.ProviderSessionID]; ok {
		if resume.WorkingDir == "" {
			resume.WorkingDir = prior.request.WorkingDir
		}

		if strings.TrimSpace(resume.Prompt) == "" {
			resume.Prompt = prior.request.Prompt
		}
	}
	a.mu.Unlock()

	return resume
}

func (a *Adapter) NormalizeResult(_ context.Context, request shared.NormalizeRequest) (*shared.StepResult, error) {
	if request.Execution == nil {
		return nil, adapterError(shared.ErrorCodeResult, "execution is required", nil)
	}

	if !request.Execution.State.Normalizable() {
		return nil, adapterError(shared.ErrorCodeResult, "execution state cannot be normalized", nil)
	}

	if request.RequireStructuredOutput {
		if err := a.DescribeCapabilities().Require(shared.CapabilityStructuredOutput); err != nil {
			return nil, err
		}
	}

	if request.RequireArtifactRefs {
		if err := a.DescribeCapabilities().Require(shared.CapabilityArtifactRefs); err != nil {
			return nil, err
		}
	}

	if request.RequireMachineReadableLogs {
		if err := a.DescribeCapabilities().Require(shared.CapabilityMachineReadableLogs); err != nil {
			return nil, err
		}
	}

	return &shared.StepResult{
		Handle:           request.Execution.Handle,
		Status:           request.Execution.State,
		Summary:          request.Execution.Summary,
		OutputText:       request.Execution.OutputText,
		StructuredOutput: adapterutil.CloneJSON(request.Execution.StructuredOutput),
		ArtifactRefs:     adapterutil.CloneArtifactRefs(request.Execution.ArtifactRefs),
		Logs:             adapterutil.CloneLogs(request.Execution.Logs),
	}, nil
}

func (a *Adapter) binaryPath() (string, error) {
	var lastErr error

	for _, name := range binaryCandidates {
		path, err := a.lookPath(name)
		if err == nil {
			return path, nil
		}

		if errors.Is(err, exec.ErrNotFound) {
			lastErr = err
			continue
		}

		return "", adapterError(shared.ErrorCodeExecution, fmt.Sprintf("locate %s binary", name), err)
	}

	if lastErr == nil {
		lastErr = exec.ErrNotFound
	}

	return "", adapterError(shared.ErrorCodeExecution, "opencode binary not found", lastErr)
}

func (a *Adapter) binaryVersion(ctx context.Context, binaryPath string) string {
	a.versionOnce.Do(func() {
		result, err := a.runner.Run(ctx, CommandSpec{Path: binaryPath, Args: []string{"--version"}})
		if err != nil {
			a.versionErr = err
			a.version = versionUnknown

			return
		}

		version := strings.TrimSpace(string(result.Stdout))
		if version == "" {
			version = strings.TrimSpace(string(result.Stderr))
		}

		if version == "" {
			version = versionUnknown
		}

		a.version = version
	})

	if a.version == "" {
		return versionUnknown
	}

	return a.version
}

// runArgsParams carries the opencode run argv inputs. Unlike codex and claude,
// opencode takes the prompt as the final positional argument (PromptOnStdin is
// false). ResumeSessionID is nil for fresh starts; L2 will pass a non-nil
// session id to append `--session <sid>`.
type runArgsParams struct {
	WorkingDir      string
	Prompt          string
	Model           string
	ResumeSessionID *string
}

// buildRunArgs assembles the opencode run argv. Shape:
//
//	run --dir <root> --dangerously-skip-permissions --print-logs
//	--output-format json [--model <m>] [--session <sid>] <prompt>
func buildRunArgs(params runArgsParams) []string {
	args := []string{
		"run",
		"--dir", params.WorkingDir,
		"--dangerously-skip-permissions",
		"--print-logs",
		"--output-format", "json",
	}

	if model := strings.TrimSpace(params.Model); model != "" {
		args = append(args, "--model", model)
	}

	if params.ResumeSessionID != nil {
		if sessionID := strings.TrimSpace(*params.ResumeSessionID); sessionID != "" {
			args = append(args, "--session", sessionID)
		}
	}

	return append(args, params.Prompt)
}

func agentPrompt(request shared.StartRequest) string {
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		prompt = fmt.Sprintf("run %s/%s", request.StepID, request.AttemptID)
	}

	return prompt
}

func (a *Adapter) logPath(request shared.StartRequest) string {
	if a.logDir == "" {
		return ""
	}

	return filepath.Join(a.logDir, sanitizeID(request.AttemptID)+"-opencode.log")
}

func (a *Adapter) lookupSession(handle shared.ExecutionHandle) (*agentSession, error) {
	if err := adapterutil.ValidateHandle(handle); err != nil {
		return nil, err
	}

	a.mu.Lock()
	record, ok := a.sessions[handle.ProviderSessionID]
	a.mu.Unlock()

	if !ok {
		return nil, adapterError(shared.ErrorCodeExecution, "opencode execution session not found", nil)
	}

	if record.handle.RunID != handle.RunID || record.handle.StepID != handle.StepID || record.handle.AttemptID != handle.AttemptID {
		return nil, adapterError(shared.ErrorCodeExecution, "opencode execution handle does not match session", nil)
	}

	return record, nil
}

// collectTerminal awaits the runner session exactly once and folds the result
// into a terminal Execution using the existing response parsing.
//
// The terminal handle keeps the run/step/attempt identifiers from the stored
// handle but adopts the real provider session id when opencode reported one
// (the response session_id): that real id is what `--session <sid>` needs, so
// overwriting it with the synthetic Start-time fallback would make resume fail.
// Once the terminal is built the heavy runner session is released (see cleanup).
func (a *Adapter) collectTerminal(record *agentSession) (*shared.Execution, error) {
	record.once.Do(func() {
		result := <-record.session.Done
		record.terminalInterrupted = result.Interrupted

		response, parseErr := parseResponse(result.Stdout)
		if parseErr != nil {
			record.buildErr = adapterError(shared.ErrorCodeResult, "parse opencode json output", parseErr)
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

		structured, soutErr := a.structuredOutput(execution.OutputText, result)
		if soutErr != nil {
			record.buildErr = soutErr
			return
		}

		execution.StructuredOutput = structured

		record.terminal = execution

		// Release the live runner session + buffered stdout/stderr now that the
		// terminal Execution captured everything we need.
		a.cleanup(syntheticID)
	})

	if record.buildErr != nil {
		return nil, record.buildErr
	}

	if record.terminal == nil {
		return nil, adapterError(shared.ErrorCodeExecution, "opencode terminal execution unavailable", nil)
	}

	return record.terminal, nil
}

// commandDir resolves the child process working directory, defaulting an empty
// value to "." so the opencode argv never carries `--dir ""` (which a
// cross-process resume without a recovered directory would otherwise produce).
func commandDir(workingDir string) string {
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		return "."
	}

	return dir
}

// structuredOutput recovers the normalized AgentResult JSON for a finished
// opencode invocation. It prefers the normalized assistant text (the response's
// OutputText assembled by buildExecution), where a real provider embeds the
// AGENT_RESULT_JSON marker, and only falls back to scanning the raw log/stdout
// for fake binaries that place the marker directly on stdout (N1). A corrupt
// marker or read fault is surfaced as a build error rather than silently
// dropped (N2).
func (a *Adapter) structuredOutput(outputText string, result runner.Result) (json.RawMessage, error) {
	structured, found, err := prompt.StructuredOutputFromText(outputText)
	if err != nil {
		return nil, adapterError(shared.ErrorCodeResult, "parse opencode structured output", err)
	}

	if found {
		return structured, nil
	}

	structured, _, err = shared.StructuredOutputFromLog(result.LogPath, result.Stdout)
	if err != nil {
		return nil, adapterError(shared.ErrorCodeResult, "read opencode structured output", err)
	}

	return structured, nil
}

// aliasSession registers an additional sessions-map key pointing at the same
// record so a PollOrCollect/Interrupt/Resume keyed off the real provider session
// id (which the terminal handle now carries and which the runtime persists)
// still resolves. Both the synthetic and real keys alias one record (N4).
func (a *Adapter) aliasSession(record *agentSession, aliasID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.sessions[aliasID] = record
}

// cleanup releases the live runner session a finished invocation no longer
// needs. The terminal Execution and terminalInterrupted flag stay on the record
// for idempotent re-polls; only the heavy runner.Session reference (and the
// stdout/stderr it buffered) is dropped, so long-running engines do not
// accumulate finished sessions.
//
// Both the synthetic and real (aliased) session-map keys point at the same
// record, so nilling record.session here clears the heavy reference for every
// key under which the record is reachable. Safe to call repeatedly.
func (a *Adapter) cleanup(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if record, ok := a.sessions[sessionID]; ok {
		record.session = nil
	}
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, command CommandSpec) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Dir = command.Dir

	var stdout bytes.Buffer

	var stderr bytes.Buffer

	stdoutWriter := command.Stdout
	stderrWriter := command.Stderr

	if stdoutWriter == nil {
		stdoutWriter = io.Discard
	}

	if stderrWriter == nil {
		stderrWriter = io.Discard
	}

	cmd.Stdout = io.MultiWriter(&stdout, stdoutWriter)
	cmd.Stderr = io.MultiWriter(&stderr, stderrWriter)

	if command.Stdin != "" {
		cmd.Stdin = strings.NewReader(command.Stdin)
	}

	err := cmd.Run()
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}

	if err != nil {
		return result, err
	}

	return result, nil
}

func adapterError(code shared.ErrorCode, message string, err error) *shared.Error {
	return &shared.Error{Code: code, Message: message, Err: err}
}
