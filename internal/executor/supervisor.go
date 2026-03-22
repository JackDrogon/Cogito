package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JackDrogon/Cogito/internal/adapters"
)

const defaultTerminationGracePeriod = 250 * time.Millisecond

type CommandSpec struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

type NormalizerInput struct {
	Handle      adapters.ExecutionHandle
	ExitCode    int
	StdoutPath  string
	StderrPath  string
	Stdout      []byte
	Stderr      []byte
	TimedOut    bool
	Interrupted bool
}

type ResultNormalizer interface {
	Normalize(ctx context.Context, input NormalizerInput) (*adapters.StepResult, error)
}

type ResultNormalizerFunc func(ctx context.Context, input NormalizerInput) (*adapters.StepResult, error)

func (f ResultNormalizerFunc) Normalize(ctx context.Context, input NormalizerInput) (*adapters.StepResult, error) {
	return f(ctx, input)
}

type RunRequest struct {
	Handle     adapters.ExecutionHandle
	Command    CommandSpec
	Timeout    time.Duration
	StdoutPath string
	StderrPath string
	Normalizer ResultNormalizer
}

type Supervisor struct {
	terminationGracePeriod time.Duration

	mu      sync.Mutex
	running map[string]*exec.Cmd
}

func NewSupervisor() *Supervisor {
	return &Supervisor{
		terminationGracePeriod: defaultTerminationGracePeriod,
		running:                map[string]*exec.Cmd{},
	}
}

func (s *Supervisor) SetTerminationGracePeriod(duration time.Duration) {
	if duration <= 0 {
		duration = defaultTerminationGracePeriod
	}

	s.terminationGracePeriod = duration
}

func (s *Supervisor) Run(ctx context.Context, request RunRequest) (*adapters.StepResult, error) {
	if err := validateRunRequest(request); err != nil {
		return nil, err
	}

	stdoutFile, err := openOutputFile(request.StdoutPath)
	if err != nil {
		return nil, err
	}
	defer stdoutFile.Close()

	stderrFile, err := openOutputFile(request.StderrPath)
	if err != nil {
		return nil, err
	}
	defer stderrFile.Close()

	cmd, handleID, err := s.setupCommand(request, stdoutFile, stderrFile)
	if err != nil {
		return nil, err
	}

	s.track(handleID, cmd)
	defer s.untrack(handleID)

	waitErr, timedOut, interrupted := s.monitorProcess(ctx, cmd, request.Timeout)

	input, err := s.collectOutput(request, waitErr, timedOut, interrupted, stdoutFile, stderrFile)
	if err != nil {
		return nil, err
	}

	return request.Normalizer.Normalize(ctx, input)
}

func (s *Supervisor) setupCommand(request RunRequest, stdoutFile, stderrFile *os.File) (*exec.Cmd, string, error) {
	cmd := exec.Command(request.Command.Path, request.Command.Args...)
	cmd.Dir = request.Command.Dir
	cmd.Env = append(os.Environ(), request.Command.Env...)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, "", wrapError(ErrorCodeExecution, fmt.Sprintf("start provider command %q", request.Command.Path), err)
	}

	handleID := request.Handle.ProviderSessionID
	if handleID == "" {
		handleID = fmt.Sprintf("pid-%d", cmd.Process.Pid)
	}

	return cmd, handleID, nil
}

func (s *Supervisor) monitorProcess(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (error, bool, bool) {
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	select {
	case waitErr := <-waitCh:
		return waitErr, false, false
	case <-timeoutCh:
		return s.terminate(cmd, waitCh, "timeout"), true, false
	case <-ctx.Done():
		return s.terminate(cmd, waitCh, "interrupt"), false, true
	}
}

func (s *Supervisor) collectOutput(request RunRequest, waitErr error, timedOut, interrupted bool, stdoutFile, stderrFile *os.File) (NormalizerInput, error) {
	if syncErr := stdoutFile.Sync(); syncErr != nil {
		return NormalizerInput{}, wrapError(ErrorCodeExecution, "sync stdout log", syncErr)
	}

	if syncErr := stderrFile.Sync(); syncErr != nil {
		return NormalizerInput{}, wrapError(ErrorCodeExecution, "sync stderr log", syncErr)
	}

	stdout, err := os.ReadFile(request.StdoutPath)
	if err != nil {
		return NormalizerInput{}, wrapError(ErrorCodeExecution, "read stdout log", err)
	}

	stderr, err := os.ReadFile(request.StderrPath)
	if err != nil {
		return NormalizerInput{}, wrapError(ErrorCodeExecution, "read stderr log", err)
	}

	return NormalizerInput{
		Handle:      request.Handle,
		ExitCode:    exitCode(waitErr),
		StdoutPath:  request.StdoutPath,
		StderrPath:  request.StderrPath,
		Stdout:      stdout,
		Stderr:      stderr,
		TimedOut:    timedOut,
		Interrupted: interrupted,
	}, nil
}

func (s *Supervisor) Interrupt(handle adapters.ExecutionHandle) error {
	if err := validateHandle(handle); err != nil {
		return wrapError(ErrorCodeRequest, "interrupt provider command", err)
	}

	s.mu.Lock()
	cmd, ok := s.running[handle.ProviderSessionID]
	s.mu.Unlock()

	if !ok {
		return newError(ErrorCodeExecution, fmt.Sprintf("provider process %q is not running", handle.ProviderSessionID))
	}

	return s.killProcessGroup(cmd.Process.Pid)
}

func DefaultNormalizer() ResultNormalizer {
	return ResultNormalizerFunc(func(_ context.Context, input NormalizerInput) (*adapters.StepResult, error) {
		status, summary := statusAndSummary(input)

		return &adapters.StepResult{
			Handle:     input.Handle,
			Status:     status,
			Summary:    summary,
			OutputText: selectOutputText(input.Stdout, input.Stderr),
			Logs: []adapters.LogEntry{
				{Level: "info", Message: "stdout captured", Fields: map[string]string{"path": input.StdoutPath}},
				{Level: "info", Message: "stderr captured", Fields: map[string]string{"path": input.StderrPath}},
			},
		}, nil
	})
}

func validateRunRequest(request RunRequest) error {
	if err := validateHandle(request.Handle); err != nil {
		return wrapError(ErrorCodeRequest, "validate execution handle", err)
	}

	if strings.TrimSpace(request.Command.Path) == "" {
		return newError(ErrorCodeRequest, "command path is required")
	}

	if request.StdoutPath == "" {
		return newError(ErrorCodeRequest, "stdout path is required")
	}

	if request.StderrPath == "" {
		return newError(ErrorCodeRequest, "stderr path is required")
	}

	if request.Normalizer == nil {
		return newError(ErrorCodeRequest, "result normalizer is required")
	}

	return nil
}

func openOutputFile(path string) (*os.File, error) {
	cleanPath := filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return nil, wrapError(ErrorCodeExecution, "create log directory for "+cleanPath, err)
	}

	file, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, wrapError(ErrorCodeExecution, "open log file "+cleanPath, err)
	}

	return file, nil
}

func (s *Supervisor) terminate(cmd *exec.Cmd, waitCh <-chan error, reason string) error {
	if err := s.killProcessGroup(cmd.Process.Pid); err != nil {
		return err
	}

	graceTimer := time.NewTimer(s.terminationGracePeriod)
	defer graceTimer.Stop()

	select {
	case err := <-waitCh:
		return err
	case <-graceTimer.C:
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !isMissingProcess(err) {
			return wrapError(ErrorCodeExecution, "force kill provider process after "+reason, err)
		}

		return <-waitCh
	}
}

func (s *Supervisor) killProcessGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !isMissingProcess(err) {
		return wrapError(ErrorCodeExecution, "terminate child process group", err)
	}

	return nil
}

func (s *Supervisor) track(handleID string, cmd *exec.Cmd) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running[handleID] = cmd
}

func (s *Supervisor) untrack(handleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, handleID)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}

	var exitErr *exec.ExitError

	if !strings.Contains(err.Error(), "signal") && !strings.Contains(err.Error(), "killed") {
		if !strings.Contains(err.Error(), "exit status") {
			return -1
		}
	}

	if ok := errorAs(err, &exitErr); ok {
		return exitErr.ExitCode()
	}

	return -1
}

func statusAndSummary(input NormalizerInput) (adapters.ExecutionState, string) {
	if input.TimedOut {
		return adapters.ExecutionStateFailed, "child process terminated after timeout"
	}

	if input.Interrupted {
		return adapters.ExecutionStateInterrupted, "child process terminated after interrupt"
	}

	if input.ExitCode == 0 {
		return adapters.ExecutionStateSucceeded, "command succeeded"
	}

	if input.ExitCode > 0 {
		return adapters.ExecutionStateFailed, fmt.Sprintf("command exited with code %d", input.ExitCode)
	}

	return adapters.ExecutionStateFailed, "command failed"
}

func selectOutputText(stdout, stderr []byte) string {
	trimmedStdout := strings.TrimSpace(string(stdout))
	if trimmedStdout != "" {
		return trimmedStdout
	}

	return strings.TrimSpace(string(stderr))
}

func isMissingProcess(err error) bool {
	return errors.Is(err, syscall.ESRCH)
}

func validateHandle(handle adapters.ExecutionHandle) error {
	if strings.TrimSpace(handle.RunID) == "" {
		return newError(ErrorCodeRequest, "run id is required")
	}

	if strings.TrimSpace(handle.StepID) == "" {
		return newError(ErrorCodeRequest, "step id is required")
	}

	if strings.TrimSpace(handle.AttemptID) == "" {
		return newError(ErrorCodeRequest, "attempt id is required")
	}

	if strings.TrimSpace(handle.ProviderSessionID) == "" {
		return newError(ErrorCodeRequest, "provider session id is required")
	}

	return nil
}

func errorAs(err error, target any) bool { return errors.As(err, target) }
