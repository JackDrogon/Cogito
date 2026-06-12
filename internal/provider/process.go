// Package provider is the shared async process supervisor used by every
// code-agent adapter (codex, claude, opencode). It launches the agent binary
// in its own process group, streams stdout/stderr to a log file, scrapes the
// session id from output, and exposes Cancel/Done so the caller can interrupt
// or await independently.
//
// Design contract (mirrors AgentLoop internal/runner/runcodex.go::runAttempt):
//
//   - StartProcess returns immediately with a Session. The actual exec happens in a
//     goroutine. Adapters surface ExecutionStateRunning to runtime right away;
//     PollOrCollect awaits Session.Done.
//   - Cancel sends SIGTERM to the whole process group (setsid), waits ExitGrace,
//     then SIGKILL. Natural exits skip signaling entirely.
//   - GIT_CEILING_DIRECTORIES caps git discovery at filepath.Dir(Dir) so an
//     agent inside a non-repo working dir cannot accidentally commit into an
//     enclosing parent repository.
//
// The process supervisor itself is stateless. Providers keep their own session maps keyed
// by ExecutionHandle.ProviderSessionID; this package only owns process
// lifecycle.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Default grace windows for the SIGTERM → SIGKILL escalation. Ported from
// AgentLoop AGENT_RESULT_EXIT_GRACE / AGENT_RESULT_KILL_GRACE defaults.
const (
	DefaultExitGrace = 5 * time.Second
	DefaultKillGrace = 3 * time.Second
)

// Tee buffer sizing for teeAndScan. The carry buffer accumulates output while
// scanning for the session id and is trimmed back to one chunk once it exceeds
// the limit, so a session line straddling the trim boundary is still matched.
const (
	teeChunkSize  = 4096
	teeCarryLimit = 2 * teeChunkSize
)

// SessionIDPattern matches "session id: <uuid>" case-insensitively in agent
// stdout. Ported verbatim from AgentLoop internal/config/regex.go.
var SessionIDPattern = regexp.MustCompile(
	`(?i)session id:\s*([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`,
)

// ProcessRequest configures one agent invocation.
type ProcessRequest struct {
	// Binary is the absolute or PATH-resolvable command to exec.
	Binary string

	// Args are the argv following Binary, with no shell interpretation.
	Args []string

	// Dir is the working directory for the child process. When non-empty it
	// is also used to set GIT_CEILING_DIRECTORIES to filepath.Dir(Dir).
	Dir string

	// Prompt is the agent prompt body. Delivered via stdin when
	// PromptOnStdin=true; otherwise the caller is responsible for placing it
	// inside Args.
	Prompt string

	// PromptOnStdin selects the prompt delivery channel. codex and claude
	// stream the prompt via stdin (PromptOnStdin=true); opencode takes it as
	// the final argv value (PromptOnStdin=false).
	PromptOnStdin bool

	// LogPath, when non-empty, receives a stdout+stderr merged copy as the
	// process runs. Parent directories are created with 0o700 if missing.
	LogPath string

	// PIDFile, when non-empty, receives a machine-local pid record for orphan
	// detection. Write/remove failures are warn-only and never affect execution.
	PIDFile string

	// Label is optional free-form context copied into PIDFile for operator output.
	Label string

	// ExtraSink, when non-nil, receives the same stdout+stderr byte stream as
	// LogPath in real time (AgentLoop-style live output). The runner writes
	// from two goroutines (stdout and stderr), so the writer must be safe for
	// concurrent use; write errors are ignored so a broken sink never affects
	// the run.
	ExtraSink io.Writer

	// WarningSink optionally receives runner-level warnings (currently the
	// post-SIGKILL "process did not exit" notice) when there is no log file to
	// write them to. When both the log file and this sink are nil the warning
	// falls back to stderr so it is never silently dropped.
	WarningSink func(string)

	// SessionPattern overrides SessionIDPattern. nil means use the default.
	SessionPattern *regexp.Regexp

	// Timeout is the maximum wall-clock duration from StartProcess until the
	// child must finish. Values <= 0 disable wall-clock enforcement.
	Timeout time.Duration

	// IdleTimeout is the maximum duration between stdout/stderr bytes from the
	// child. Values <= 0 disable no-output enforcement.
	IdleTimeout time.Duration

	// ExitGrace and KillGrace override the default SIGTERM → SIGKILL timing.
	// Values <= 0 fall back to the package defaults.
	ExitGrace time.Duration
	KillGrace time.Duration
}

// ProcessResult captures the outcome of one finished agent invocation.
type ProcessResult struct {
	// ExitCode is the process exit status. A non-zero ExitCode does NOT set
	// Err; adapters interpret status semantics themselves.
	ExitCode int

	// Stdout and Stderr hold the full captured byte streams, in addition to
	// whatever was tee'd to LogPath.
	Stdout []byte
	Stderr []byte

	// LogPath echoes the request value, for caller convenience.
	LogPath string

	// SessionID is the extracted real session id (matched against
	// SessionPattern), or empty if no match was found.
	SessionID string

	// Interrupted reports whether the runner actually delivered a cancellation
	// signal (SIGTERM/SIGKILL) to the process group. It stays false when the
	// process exited naturally before any cancel fired. Adapters use it to
	// avoid downgrading a naturally-succeeded execution to interrupted when an
	// interrupt arrives after the child already finished.
	Interrupted bool

	// TimeoutReason is empty unless the process watchdog fired. When set it is a
	// human-readable reason, Interrupted will typically also be true because the
	// watchdog cancels via the same SIGTERM→SIGKILL escalation, and adapters must
	// check TimeoutReason before Interrupted so timeouts become failed executions.
	TimeoutReason string

	// Err is reserved for spawn/wait/IO errors. Cancellation alone is NOT an
	// error: callers detect interruption via parent context state, not Err.
	Err error
}

// Session is an asynchronously running agent invocation. The zero value is
// not useful; sessions must come from StartProcess.
type Session struct {
	// Done resolves with the final ProcessResult once the process exits and IO
	// drains. The channel is closed after the value is sent.
	Done <-chan ProcessResult

	cancel    context.CancelFunc
	sessionID atomic.Value // string
}

// SessionID returns the extracted session id observed so far. Empty until the
// child emits a matching line; safe to call concurrently with Cancel/Done.
func (s *Session) SessionID() string {
	if s == nil {
		return ""
	}

	value, ok := s.sessionID.Load().(string)
	if !ok {
		return ""
	}

	return value
}

// Cancel triggers a graceful interruption: SIGTERM to the process group, wait
// ExitGrace, then SIGKILL. Idempotent. The caller should still drain Done.
func (s *Session) Cancel() {
	if s == nil || s.cancel == nil {
		return
	}

	s.cancel()
}

// StartProcess launches the binary asynchronously and returns the running Session.
// Errors here cover only spawn-time failures (missing binary, log dir
// unwriteable, pipe setup). Process exit and IO errors land in ProcessResult.
func StartProcess(parentCtx context.Context, req ProcessRequest) (*Session, error) {
	if strings.TrimSpace(req.Binary) == "" {
		return nil, errors.New("provider.StartProcess: Binary is required")
	}

	settings := applyDefaults(req)
	secretValues := collectSecretEnvValues(os.Environ())

	var logFile *os.File

	if req.LogPath != "" {
		file, err := openLogFile(req.LogPath)
		if err != nil {
			return nil, err
		}

		logFile = file
	}

	ctx, cancel := context.WithCancel(parentCtx)
	cmd := buildCommand(ctx, req)

	pipes, err := openPipes(cmd, req.PromptOnStdin)
	if err != nil {
		closeLogFile(logFile)
		cancel()

		return nil, err
	}

	if err := cmd.Start(); err != nil {
		closeLogFile(logFile)
		cancel()

		return nil, fmt.Errorf("provider.StartProcess: exec: %w", err)
	}

	writePIDFile(req, cmd)

	done := make(chan ProcessResult, 1)
	session := &Session{Done: done, cancel: cancel}
	session.sessionID.Store("")

	supervise(superviseParams{
		Cmd:         cmd,
		Ctx:         ctx,
		Cancel:      cancel,
		Stdin:       pipes.stdin,
		Stdout:      pipes.stdout,
		Stderr:      pipes.stderr,
		LogFile:     logFile,
		LogPath:     req.LogPath,
		PIDFile:     req.PIDFile,
		ExtraSink:   req.ExtraSink,
		Secrets:     secretValues,
		Prompt:      req.Prompt,
		Pattern:     settings.pattern,
		Timeout:     req.Timeout,
		IdleTimeout: req.IdleTimeout,
		ExitGrace:   settings.exitGrace,
		KillGrace:   settings.killGrace,
		WarningSink: req.WarningSink,
		Session:     session,
		Done:        done,
	})

	return session, nil
}

type startSettings struct {
	pattern   *regexp.Regexp
	exitGrace time.Duration
	killGrace time.Duration
}

func applyDefaults(req ProcessRequest) startSettings {
	pattern := req.SessionPattern
	if pattern == nil {
		pattern = SessionIDPattern
	}

	exitGrace := req.ExitGrace
	if exitGrace <= 0 {
		exitGrace = DefaultExitGrace
	}

	killGrace := req.KillGrace
	if killGrace <= 0 {
		killGrace = DefaultKillGrace
	}

	return startSettings{pattern: pattern, exitGrace: exitGrace, killGrace: killGrace}
}

func openLogFile(logPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, fmt.Errorf("provider.StartProcess: prepare log dir: %w", err)
	}

	file, err := os.Create(filepath.Clean(logPath))
	if err != nil {
		return nil, fmt.Errorf("provider.StartProcess: open log file: %w", err)
	}

	return file, nil
}

func closeLogFile(logFile *os.File) {
	if logFile == nil {
		return
	}

	_ = logFile.Close()
}

func writePIDFile(req ProcessRequest, cmd *exec.Cmd) {
	pidFile := strings.TrimSpace(req.PIDFile)
	if pidFile == "" || cmd == nil || cmd.Process == nil {
		return
	}

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		slog.Warn("runner: pidfile pgid lookup failed", "path", pidFile, "pid", cmd.Process.Pid, "err", err)
		pgid = cmd.Process.Pid
	}

	record := PIDRecord{
		PID:       cmd.Process.Pid,
		PGID:      pgid,
		Binary:    req.Binary,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Label:     req.Label,
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		slog.Warn("runner: pidfile encode failed", "path", pidFile, "err", err)

		return
	}

	if err := os.MkdirAll(filepath.Dir(pidFile), 0o700); err != nil {
		slog.Warn("runner: pidfile dir prepare failed", "path", pidFile, "err", err)

		return
	}

	if err := os.WriteFile(filepath.Clean(pidFile), append(data, '\n'), 0o600); err != nil {
		slog.Warn("runner: pidfile write failed", "path", pidFile, "err", err)
	}
}

func removePIDFile(pidFile string) {
	pidFile = strings.TrimSpace(pidFile)
	if pidFile == "" {
		return
	}

	if err := os.Remove(filepath.Clean(pidFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("runner: pidfile remove failed", "path", pidFile, "err", err)
	}
}

// flushLogFile forces buffered writes to disk before the caller observes the
// log path. Without this, a fast caller reading the log right after Done
// resolves can see partial content even though our writer goroutines completed.
func flushLogFile(logFile *os.File) {
	if logFile == nil {
		return
	}

	if err := logFile.Sync(); err != nil {
		slog.Warn("runner: log file sync failed", "path", logFile.Name(), "err", err)
	}
}

func buildCommand(_ context.Context, req ProcessRequest) *exec.Cmd {
	// Intentionally NOT exec.CommandContext: we own all cancellation via the
	// supervise goroutine so ctx never escalates to Process.Kill behind our
	// back. The ctx parameter is kept in the signature for future propagation
	// (env injection, otel, etc.).
	cmd := exec.Command(req.Binary, req.Args...) //nolint:noctx // cancellation is owned by signalOnCancel (SIGTERM→SIGKILL on the process group)

	dir := strings.TrimSpace(req.Dir)
	if dir != "" {
		cmd.Dir = dir
		// Cap git discovery at the parent so an agent inside a non-repo Dir
		// cannot accidentally commit into an enclosing repository.
		cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	} else {
		cmd.Env = os.Environ()
	}

	// setsid puts the child in its own process group so we can signal the
	// whole tree via -pgid.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	return cmd
}

// pipeSet bundles the child-process IO pipes so openPipes stays within the
// project's two-return-value rule. Stdin is nil unless the prompt is delivered
// on stdin.
type pipeSet struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func openPipes(cmd *exec.Cmd, promptOnStdin bool) (pipeSet, error) {
	var pipes pipeSet

	if promptOnStdin {
		pipe, err := cmd.StdinPipe()
		if err != nil {
			return pipeSet{}, fmt.Errorf("provider.StartProcess: stdin pipe: %w", err)
		}

		pipes.stdin = pipe
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return pipeSet{}, fmt.Errorf("provider.StartProcess: stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return pipeSet{}, fmt.Errorf("provider.StartProcess: stderr pipe: %w", err)
	}

	pipes.stdout = stdout
	pipes.stderr = stderr

	return pipes, nil
}

type superviseParams struct {
	Cmd *exec.Cmd
	// Ctx is carried in the params struct because supervise fans it out to
	// the signal goroutine; the struct is a one-shot argument bag, not a
	// stored field on a long-lived object.
	Ctx         context.Context //nolint:containedctx // one-shot goroutine argument bag
	Cancel      context.CancelFunc
	Stdin       io.WriteCloser
	Stdout      io.ReadCloser
	Stderr      io.ReadCloser
	LogFile     *os.File
	LogPath     string
	PIDFile     string
	ExtraSink   io.Writer
	Secrets     []string
	Prompt      string
	Pattern     *regexp.Regexp
	Timeout     time.Duration
	IdleTimeout time.Duration
	ExitGrace   time.Duration
	KillGrace   time.Duration
	WarningSink func(string)
	Session     *Session
	Done        chan<- ProcessResult
}

func supervise(params superviseParams) {
	var stdoutBuf, stderrBuf bytes.Buffer

	var (
		logMu      sync.Mutex
		lastOutput atomic.Int64
	)

	lastOutput.Store(time.Now().UnixNano())

	if params.Stdin != nil {
		go writePrompt(params.Stdin, params.Prompt)
	}

	var ioWG sync.WaitGroup

	ioWG.Add(2)

	go func() {
		defer ioWG.Done()

		teeAndScan(params.Stdout, teeParams{
			Buffer:     &stdoutBuf,
			LogFile:    params.LogFile,
			LogMu:      &logMu,
			ExtraSink:  params.ExtraSink,
			Secrets:    params.Secrets,
			Pattern:    params.Pattern,
			Session:    params.Session,
			LastOutput: &lastOutput,
		})
	}()

	go func() {
		defer ioWG.Done()

		teeAndScan(params.Stderr, teeParams{
			Buffer:     &stderrBuf,
			LogFile:    params.LogFile,
			LogMu:      &logMu,
			ExtraSink:  params.ExtraSink,
			Secrets:    params.Secrets,
			Session:    params.Session,
			LastOutput: &lastOutput,
		})
	}()

	go func() {
		readersDone := make(chan struct{})

		var (
			interrupted   atomic.Bool
			timeoutReason atomic.Value
			timeoutOnce   sync.Once
		)

		// signalOnCancel watches for parent cancellation vs natural child
		// exit. Natural exit is detected by readers draining the stdout/
		// stderr pipes, NOT by Cmd.Wait — see the next comment.
		go signalOnCancel(signalParams{
			Ctx:         params.Ctx,
			Process:     params.Cmd.Process,
			ExitGrace:   params.ExitGrace,
			KillGrace:   params.KillGrace,
			ReadersDone: readersDone,
			Interrupted: &interrupted,
			WarnStuck:   func() { warnKillTimeout(params.LogFile, &logMu, params.WarningSink) },
		})

		startWatchdog(watchdogParams{
			Timeout:       params.Timeout,
			IdleTimeout:   params.IdleTimeout,
			LastOutput:    &lastOutput,
			ReadersDone:   readersDone,
			Cancel:        params.Cancel,
			Reason:        &timeoutReason,
			RecordTimeout: &timeoutOnce,
		})

		// Per Go exec docs (StdoutPipe / StderrPipe): "It is incorrect to
		// call Wait before all reads from the pipe have completed." Cmd.Wait
		// closes the parent-side read fds when the child exits; if a reader
		// goroutine is mid-read at that moment it can observe truncated
		// output. The race manifests reliably for fast-exit children
		// (/bin/echo, /bin/sh -c printf) under heavy parallel test load.
		// Wait for ioWG (readers naturally finish on EOF after the child's
		// write ends close at exit) BEFORE invoking Cmd.Wait.
		ioWG.Wait()
		close(readersDone)

		waitErr := params.Cmd.Wait()

		// Flush and close the log file BEFORE delivering ProcessResult. Otherwise
		// the caller's os.ReadFile races our buffered writes / fd close and
		// observes an empty / truncated log even though the process emitted
		// everything.
		flushLogFile(params.LogFile)
		closeLogFile(params.LogFile)
		removePIDFile(params.PIDFile)

		exitCode, propagated := classifyWaitError(waitErr, params.Cmd.ProcessState)

		params.Done <- ProcessResult{
			ExitCode:      exitCode,
			Stdout:        bytes.Clone(stdoutBuf.Bytes()),
			Stderr:        bytes.Clone(stderrBuf.Bytes()),
			LogPath:       params.LogPath,
			SessionID:     params.Session.SessionID(),
			Interrupted:   interrupted.Load(),
			TimeoutReason: loadTimeoutReason(&timeoutReason),
			Err:           propagated,
		}

		close(params.Done)

		// Cancel the supervising context AFTER Done resolves so any in-flight
		// signal goroutine exits cleanly; safe because readersDone already
		// closed and signalOnCancel returned.
		params.Cancel()
	}()
}

func writePrompt(stdin io.WriteCloser, prompt string) {
	defer func() { _ = stdin.Close() }()

	if prompt == "" {
		return
	}

	// A failed write means the agent ran with a truncated or empty prompt —
	// the run will likely produce garbage, so the cause must be visible.
	if _, err := io.WriteString(stdin, prompt); err != nil {
		slog.Warn("runner: prompt delivery to stdin failed", "err", err)
	}
}

// teeParams groups the per-stream destinations for teeAndScan. Stdout and
// stderr each get their own Buffer but share LogFile/LogMu/ExtraSink; only the
// stdout stream carries a Pattern for session-id scraping.
type teeParams struct {
	Buffer     *bytes.Buffer
	LogFile    *os.File
	LogMu      *sync.Mutex
	ExtraSink  io.Writer
	Secrets    []string
	Pattern    *regexp.Regexp
	Session    *Session
	LastOutput *atomic.Int64
}

// teeAndScan reads src in 4KB chunks, appends to Buf, LogFile (under LogMu),
// and ExtraSink (live output, errors ignored), and matches against Pattern
// (when non-nil) to update Session.sessionID. Returns when src returns any
// error including io.EOF.
func teeAndScan(src io.Reader, params teeParams) {
	buf, pattern, session := params.Buffer, params.Pattern, params.Session
	logWriter := redactingLogWriter(params.LogFile, params.LogMu, params.Secrets)
	sinkWriter := redactingSinkWriter(params.ExtraSink, params.Secrets)
	chunk := make([]byte, teeChunkSize)

	defer closeRedactor(logWriter)
	defer closeRedactor(sinkWriter)

	var carry []byte

	for {
		bytesRead, err := src.Read(chunk)
		if bytesRead > 0 {
			if params.LastOutput != nil {
				params.LastOutput.Store(time.Now().UnixNano())
			}

			data := chunk[:bytesRead]
			buf.Write(data)

			if params.LogFile != nil {
				_, _ = logWriter.Write(data)
			}

			if params.ExtraSink != nil {
				_, _ = sinkWriter.Write(data)
			}

			if pattern != nil && session.SessionID() == "" {
				carry = append(carry, data...)
				if match := pattern.FindSubmatch(carry); len(match) >= 2 {
					session.sessionID.Store(string(match[1]))

					carry = nil
				} else if len(carry) > teeCarryLimit {
					// Keep a one-chunk tail in case a session line straddles
					// the trim boundary; avoid unbounded growth on long
					// outputs.
					carry = bytes.Clone(carry[len(carry)-teeChunkSize:])
				}
			}
		}

		if err != nil {
			return
		}
	}
}

func redactingLogWriter(logFile *os.File, logMu *sync.Mutex, secrets []string) io.WriteCloser {
	if logFile == nil {
		return nil
	}

	return newRedactingWriter(lockedWriter{Writer: logFile, Mu: logMu}, secrets)
}

func redactingSinkWriter(sink io.Writer, secrets []string) io.WriteCloser {
	if sink == nil {
		return nil
	}

	return newRedactingWriter(sink, secrets)
}

func closeRedactor(writer io.WriteCloser) {
	if writer == nil {
		return
	}

	_ = writer.Close()
}

type lockedWriter struct {
	Writer io.Writer
	Mu     *sync.Mutex
}

func (w lockedWriter) Write(data []byte) (int, error) {
	if w.Mu == nil {
		return w.Writer.Write(data)
	}

	w.Mu.Lock()
	defer w.Mu.Unlock()

	return w.Writer.Write(data)
}

// watchdogParams configures the optional process timeout watchdog. It is a
// struct because the watchdog needs several shared signals and state handles.
type watchdogParams struct {
	Timeout       time.Duration
	IdleTimeout   time.Duration
	LastOutput    *atomic.Int64
	ReadersDone   <-chan struct{}
	Cancel        context.CancelFunc
	Reason        *atomic.Value
	RecordTimeout *sync.Once
}

func startWatchdog(params watchdogParams) {
	if params.Timeout <= 0 && params.IdleTimeout <= 0 {
		return
	}

	go runWatchdog(params)
}

func runWatchdog(params watchdogParams) {
	var wallClock <-chan time.Time

	var wallTimer *time.Timer

	if params.Timeout > 0 {
		wallTimer = time.NewTimer(params.Timeout)
		defer wallTimer.Stop()

		wallClock = wallTimer.C
	}

	var idleTicks <-chan time.Time

	var idleTicker *time.Ticker

	if params.IdleTimeout > 0 {
		idleTicker = time.NewTicker(idleCheckInterval(params.IdleTimeout))
		defer idleTicker.Stop()

		idleTicks = idleTicker.C
	}

	for {
		select {
		case <-params.ReadersDone:
			return
		case <-wallClock:
			recordTimeout(params, fmt.Sprintf("agent process timed out after %s", params.Timeout))

			return
		case <-idleTicks:
			if params.LastOutput == nil {
				continue
			}

			last := time.Unix(0, params.LastOutput.Load())
			if time.Since(last) < params.IdleTimeout {
				continue
			}

			recordTimeout(params, fmt.Sprintf("agent process produced no output for %s", params.IdleTimeout))

			return
		}
	}
}

func idleCheckInterval(idleTimeout time.Duration) time.Duration {
	interval := idleTimeout / 4
	if interval < 100*time.Millisecond {
		return 100 * time.Millisecond
	}

	if interval > 10*time.Second {
		return 10 * time.Second
	}

	return interval
}

func recordTimeout(params watchdogParams, reason string) {
	params.RecordTimeout.Do(func() {
		params.Reason.Store(reason)
		params.Cancel()
	})
}

func loadTimeoutReason(reason *atomic.Value) string {
	value, ok := reason.Load().(string)
	if !ok {
		return ""
	}

	return value
}

// signalParams configures one signalOnCancel escalation. It is a struct because
// the escalation needs more than three inputs (the project caps positional
// params at three).
type signalParams struct {
	// Ctx mirrors superviseParams.Ctx: a one-shot argument bag for the
	// escalation goroutine, not long-lived state.
	Ctx       context.Context //nolint:containedctx // one-shot goroutine argument bag
	Process   *os.Process
	ExitGrace time.Duration
	KillGrace time.Duration
	// ReadersDone closes when the stdout/stderr reader goroutines drain (which
	// happens at natural child exit, before Cmd.Wait). It is NOT the Cmd.Wait
	// signal: the main-session race fix deliberately moved ioWG.Wait() ahead of
	// Cmd.Wait, so this channel must track reader completion, not process exit.
	ReadersDone <-chan struct{}
	Interrupted *atomic.Bool
	WarnStuck   func()
}

// signalOnCancel watches ctx for cancellation and escalates the process group
// SIGTERM → (ExitGrace) → SIGKILL → (KillGrace) → give up. Natural process
// exits short-circuit every wait via ReadersDone. When a signal is actually
// sent it flips Interrupted so the caller can tell a real cancel from a natural
// finish. If the post-SIGKILL grace expires the process is truly stuck and
// WarnStuck records that we gave up.
func signalOnCancel(params signalParams) {
	if params.Process == nil {
		return
	}

	select {
	case <-params.ReadersDone:
		return
	case <-params.Ctx.Done():
	}

	params.Interrupted.Store(true)

	pgid, pgidErr := syscall.Getpgid(params.Process.Pid)
	if pgidErr != nil {
		logSignalError(params.Process.Signal(syscall.SIGTERM), "SIGTERM", params.Process.Pid)
	} else {
		logSignalError(syscall.Kill(-pgid, syscall.SIGTERM), "SIGTERM", -pgid)
	}

	termTimer := time.NewTimer(params.ExitGrace)
	defer termTimer.Stop()

	select {
	case <-params.ReadersDone:
		return
	case <-termTimer.C:
	}

	if pgidErr != nil {
		logSignalError(params.Process.Kill(), "SIGKILL", params.Process.Pid)
	} else {
		logSignalError(syscall.Kill(-pgid, syscall.SIGKILL), "SIGKILL", -pgid)
	}

	// SIGKILL is uncatchable, so the process should die promptly. Wait KillGrace
	// for that to land; if it does not (e.g. uninterruptible D-state sleep), the
	// package contract (SIGTERM → grace → SIGKILL → grace → give up) is honored
	// by warning and returning instead of blocking forever.
	killTimer := time.NewTimer(params.KillGrace)
	defer killTimer.Stop()

	select {
	case <-params.ReadersDone:
		return
	case <-killTimer.C:
		if params.WarnStuck != nil {
			params.WarnStuck()
		}
	}
}

// warnKillTimeout records that a process survived the post-SIGKILL grace window.
// When a log file is present it writes there under logMu (the same lock the IO
// tee goroutines use); the file is guaranteed open because supervise closes it
// only after the IO pipes drain, which cannot happen until the process exits.
//
// When there is no log file the warning is routed to the caller-provided sink,
// and when that is also nil it falls back to stderr — so a stuck process is
// never silently ignored just because the caller passed an empty LogPath.
func warnKillTimeout(logFile *os.File, logMu *sync.Mutex, sink func(string)) {
	const message = "[runner] WARNING: process did not exit after SIGKILL grace; giving up"

	if logFile != nil {
		logMu.Lock()
		defer logMu.Unlock()

		_, _ = logFile.WriteString("\n" + message + "\n")

		return
	}

	if sink != nil {
		sink(message)

		return
	}

	_, _ = fmt.Fprintln(os.Stderr, message)
}

// logSignalError records a failed signal delivery. A vanished process
// (ESRCH / ErrProcessDone) is the expected race between natural exit and the
// escalation path and is not worth reporting; anything else means the child
// may still be alive, which the operator should know about.
func logSignalError(err error, signal string, target int) {
	if err == nil || errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
		return
	}

	slog.Warn("runner: signal delivery failed", "signal", signal, "target", target, "err", err)
}

// classifyWaitError extracts ExitCode and decides whether the wait error is a
// runtime-level failure to propagate. *exec.ExitError yields its ExitCode
// without setting Err; any other error type passes through.
func classifyWaitError(waitErr error, state *os.ProcessState) (int, error) {
	if waitErr == nil {
		if state != nil {
			return state.ExitCode(), nil
		}

		return 0, nil
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok {
		return exitErr.ExitCode(), nil
	}

	return -1, waitErr
}
