package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/executor"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
)

const providerLogsDir = "provider-logs"

type supervisorCommandRunner struct {
	store      *store.Store
	supervisor *executor.Supervisor
	workingDir string
	timeout    time.Duration
	now        func() time.Time

	mu       sync.Mutex
	sessions map[string]*commandSession
}

type commandSession struct {
	cancel context.CancelFunc
	done   chan commandSessionResult

	mu      sync.Mutex
	settled bool
	result  *adapters.Execution
	err     error
	stdout  string
	stderr  string
}

type commandSessionResult struct {
	result *adapters.Execution
	err    error
}

func newSupervisorCommandRunner(runStore *store.Store, workingDir string, timeout time.Duration) *supervisorCommandRunner {
	return &supervisorCommandRunner{
		store:      runStore,
		supervisor: executor.NewSupervisor(),
		workingDir: strings.TrimSpace(workingDir),
		timeout:    timeout,
		now:        time.Now,
		sessions:   map[string]*commandSession{},
	}
}

func (r *supervisorCommandRunner) Start(ctx context.Context, request runtime.CommandRequest) (*adapters.Execution, error) {
	if strings.TrimSpace(request.RunID) == "" {
		return nil, errors.New("supervisorCommandRunner.Start: run id is required")
	}

	if strings.TrimSpace(request.StepID) == "" {
		return nil, errors.New("supervisorCommandRunner.Start: step id is required")
	}

	if strings.TrimSpace(request.AttemptID) == "" {
		return nil, errors.New("supervisorCommandRunner.Start: attempt id is required")
	}

	if strings.TrimSpace(request.Command) == "" {
		return nil, errors.New("supervisorCommandRunner.Start: command is required")
	}

	commandSpec, err := executor.ParseCommand(request.Command, r.commandWorkingDir(request))
	if err != nil {
		return nil, err
	}

	handle := adapters.ExecutionHandle{
		RunID:             request.RunID,
		StepID:            request.StepID,
		AttemptID:         request.AttemptID,
		ProviderSessionID: fmt.Sprintf("command-%s-%s", sanitizePathToken(request.StepID), sanitizePathToken(request.AttemptID)),
	}

	stdoutPath, stderrPath := r.logPaths(request.StepID, request.AttemptID)
	runCtx, cancel := context.WithCancel(ctx)
	session := &commandSession{
		cancel: cancel,
		done:   make(chan commandSessionResult, 1),
		stdout: stdoutPath,
		stderr: stderrPath,
	}

	r.mu.Lock()
	r.sessions[handle.ProviderSessionID] = session
	r.mu.Unlock()

	go func() {
		result, err := r.runCommand(runCtx, handle, commandSpec, request.StepID, stdoutPath, stderrPath)
		session.complete(result, err)
	}()

	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateRunning, Summary: "command started"}, nil
}

func (r *supervisorCommandRunner) PollOrCollect(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	session, err := r.lookupSession(handle)
	if err != nil {
		return nil, err
	}

	return session.await()
}

func (r *supervisorCommandRunner) Interrupt(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	session, err := r.lookupSession(handle)
	if err != nil {
		return nil, err
	}

	session.cancel()

	return session.await()
}

func (r *supervisorCommandRunner) NormalizeResult(_ context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	if execution == nil {
		return nil, errors.New("supervisorCommandRunner.NormalizeResult: execution is required")
	}

	if !execution.State.Normalizable() {
		return nil, fmt.Errorf("supervisorCommandRunner.NormalizeResult: execution state %s cannot be normalized", execution.State)
	}

	return &adapters.StepResult{
		Handle:           execution.Handle,
		Status:           execution.State,
		Summary:          execution.Summary,
		OutputText:       execution.OutputText,
		StructuredOutput: execution.StructuredOutput,
		ArtifactRefs:     execution.ArtifactRefs,
		Logs:             execution.Logs,
	}, nil
}

func (r *supervisorCommandRunner) runCommand(ctx context.Context, handle adapters.ExecutionHandle, commandSpec executor.CommandSpec, stepID, stdoutPath, stderrPath string) (*adapters.Execution, error) {
	result, err := r.supervisor.Run(ctx, executor.RunRequest{
		Handle:     handle,
		Command:    commandSpec,
		Timeout:    r.timeout,
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
		Normalizer: executor.DefaultNormalizer(),
	})
	if err != nil {
		return nil, err
	}

	if err := r.saveArtifacts(stepID, stdoutPath, stderrPath); err != nil {
		return nil, err
	}

	return executionFromStepResult(result), nil
}

func (r *supervisorCommandRunner) commandWorkingDir(request runtime.CommandRequest) string {
	if dir := strings.TrimSpace(r.workingDir); dir != "" {
		return dir
	}

	if dir := strings.TrimSpace(request.WorkingDir); dir != "" {
		return dir
	}

	return "."
}

func (r *supervisorCommandRunner) logPaths(stepID, attemptID string) (string, string) {
	base := filepath.Join(r.store.Layout().RunDir, providerLogsDir, sanitizePathToken(stepID))
	prefix := sanitizePathToken(attemptID)

	return filepath.Join(base, prefix+"-stdout.log"), filepath.Join(base, prefix+"-stderr.log")
}

func (r *supervisorCommandRunner) saveArtifacts(stepID, stdoutPath, stderrPath string) error {
	records, err := r.store.LoadArtifacts()
	if err != nil {
		return err
	}

	createdAt := r.now().UTC().Format(time.RFC3339Nano)

	relStdout, err := filepath.Rel(r.store.Layout().RunDir, stdoutPath)
	if err != nil {
		return err
	}

	relStderr, err := filepath.Rel(r.store.Layout().RunDir, stderrPath)
	if err != nil {
		return err
	}

	records = append(records,
		store.ArtifactRecord{Path: relStdout, Kind: "log", StepID: stepID, Summary: "command stdout log", CreatedAt: createdAt},
		store.ArtifactRecord{Path: relStderr, Kind: "log", StepID: stepID, Summary: "command stderr log", CreatedAt: createdAt},
	)

	return r.store.SaveArtifacts(records)
}

func (r *supervisorCommandRunner) lookupSession(handle adapters.ExecutionHandle) (*commandSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	session, ok := r.sessions[handle.ProviderSessionID]
	if !ok {
		return nil, fmt.Errorf("command session %q not found", handle.ProviderSessionID)
	}

	return session, nil
}

func (s *commandSession) complete(result *adapters.Execution, err error) {
	s.done <- commandSessionResult{result: cloneExecution(result), err: err}
}

func (s *commandSession) await() (*adapters.Execution, error) {
	s.mu.Lock()
	if s.settled {
		result := cloneExecution(s.result)
		err := s.err
		s.mu.Unlock()

		return result, err
	}

	ch := s.done
	s.mu.Unlock()

	resolved := <-ch

	s.mu.Lock()
	if !s.settled {
		s.settled = true
		s.result = cloneExecution(resolved.result)
		s.err = resolved.err
	}

	result := cloneExecution(s.result)
	err := s.err
	s.mu.Unlock()

	return result, err
}

func sanitizePathToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "step"
	}

	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")

	return replacer.Replace(value)
}
