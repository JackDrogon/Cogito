package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JackDrogon/Cogito/internal/adapters"
	_ "github.com/JackDrogon/Cogito/internal/adapters/claude"
	_ "github.com/JackDrogon/Cogito/internal/adapters/codex"
	_ "github.com/JackDrogon/Cogito/internal/adapters/opencode"
	"github.com/JackDrogon/Cogito/internal/executor"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

const providerLogsDir = "provider-logs"

func executeWorkflowRun(workflowPath string, flags *sharedFlags, stdout io.Writer) (err error) {
	if flags == nil {
		return fmt.Errorf("run flags are required")
	}

	approvalMode, err := runtime.ParseApprovalMode(flags.approval)
	if err != nil {
		return err
	}

	compiled, err := workflow.LoadFile(workflowPath)
	if err != nil {
		return err
	}

	baseDir, runID, err := parseRunStateDir(flags.stateDir)
	if err != nil {
		return err
	}

	repoLock, err := acquireRepoLock(flags, runID, baseDir)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := repoLock.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()

	runStore, err := store.Open(baseDir, runID)
	if err != nil {
		return err
	}

	if err := workflow.SaveResolvedFile(runStore.Layout().WorkflowPath, compiled); err != nil {
		return err
	}

	wiring, err := buildRuntimeWiring(runStore, flags)
	if err != nil {
		return err
	}

	engine, err := runtime.NewEngine(runID, compiled, runtime.MachineDependencies{
		Store:          runStore,
		ApprovalPolicy: runtime.NewApprovalModePolicy(approvalMode),
		LookupAdapter:  wiring.LookupAdapter,
		CommandRunner:  wiring.CommandRunner,
		RepoPath:       wiring.RepoPath,
		WorkingDir:     wiring.WorkingDir,
	})
	if err != nil {
		return err
	}

	if err := engine.ExecuteAll(context.Background()); err != nil {
		return err
	}

	snapshot := engine.Snapshot()
	if snapshot.State == runtime.RunStateFailed {
		return latestRunFailure(runStore)
	}

	_, err = fmt.Fprintf(stdout, "run_id=%s\nstate_dir=%s\nstate=%s\n", runID, runStore.Layout().RunDir, snapshot.State)
	return err
}

func executeResumeRun(flags *sharedFlags, stdout io.Writer) error {
	runStore, _, engine, err := loadExistingRunEngine(flags.stateDir, flags)
	if err != nil {
		return err
	}

	if err := engine.Resume(""); err != nil {
		return err
	}

	if err := engine.ExecuteAll(context.Background()); err != nil {
		return err
	}

	if engine.Snapshot().State == runtime.RunStateFailed {
		return latestRunFailure(runStore)
	}

	_, err = fmt.Fprintln(stdout, "run resumed")
	return err
}

func executeReplay(eventsPath string, stdout io.Writer) error {
	runID, compiled, events, err := loadReplayInput(eventsPath)
	if err != nil {
		return err
	}

	if _, err := runtime.Replay(runID, compiled, events); err != nil {
		return err
	}

	_, err = fmt.Fprintln(stdout, "replay OK")
	return err
}

func executeCancelRun(stateDir string, stdout io.Writer) error {
	_, _, engine, err := loadExistingRunEngine(stateDir, nil)
	if err != nil {
		return err
	}

	if err := engine.Cancel(""); err != nil {
		return err
	}

	_, err = fmt.Fprintln(stdout, "run canceled")
	return err
}

func latestRunFailure(runStore *store.Store) error {
	if runStore == nil {
		return fmt.Errorf("run failed")
	}

	events, err := runStore.ReadEvents()
	if err != nil {
		return err
	}

	for index := len(events) - 1; index >= 0; index-- {
		message := strings.TrimSpace(events[index].Message)
		if message != "" {
			return errors.New(message)
		}
	}

	return fmt.Errorf("run failed")
}

func openExistingRunStore(stateDir string) (*store.Store, string, error) {
	baseDir, runID, err := parseRunStateDir(stateDir)
	if err != nil {
		return nil, "", err
	}

	runStore, err := store.OpenExisting(baseDir, runID)
	if err != nil {
		return nil, "", err
	}

	return runStore, runID, nil
}

func parseRunStateDir(stateDir string) (string, string, error) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return "", "", fmt.Errorf("state dir is required")
	}

	runID := filepath.Base(stateDir)
	baseDir := filepath.Dir(stateDir)
	if runID == "." || runID == string(filepath.Separator) || strings.TrimSpace(runID) == "" {
		return "", "", fmt.Errorf("invalid state dir %q", stateDir)
	}

	return baseDir, runID, nil
}

func loadRunStatus(stateDir string) (string, *workflow.CompiledWorkflow, runtime.Snapshot, error) {
	runStore, compiled, engine, err := loadExistingRunEngine(stateDir, nil)
	if err != nil {
		return "", nil, runtime.Snapshot{}, err
	}

	return runStore.Layout().RunDir, compiled, engine.Snapshot(), nil
}

func loadExistingRunEngine(stateDir string, flags *sharedFlags) (*store.Store, *workflow.CompiledWorkflow, *runtime.Engine, error) {
	runStore, runID, err := openExistingRunStore(stateDir)
	if err != nil {
		if isMissingRunStateError(err) {
			return nil, nil, nil, fmt.Errorf("run state not found: %s", stateDir)
		}
		return nil, nil, nil, err
	}

	compiled, err := workflow.LoadResolvedFile(runStore.Layout().WorkflowPath)
	if err != nil {
		if isMissingRunStateError(err) {
			return nil, nil, nil, fmt.Errorf("run state not found: %s", stateDir)
		}
		return nil, nil, nil, err
	}

	wiring, err := buildRuntimeWiring(runStore, flags)
	if err != nil {
		return nil, nil, nil, err
	}

	engine, err := runtime.NewEngine(runID, compiled, runtime.MachineDependencies{
		Store:         runStore,
		LookupAdapter: wiring.LookupAdapter,
		CommandRunner: wiring.CommandRunner,
		RepoPath:      wiring.RepoPath,
		WorkingDir:    wiring.WorkingDir,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	return runStore, compiled, engine, nil
}

func loadReplayInput(eventsPath string) (string, *workflow.CompiledWorkflow, []store.Event, error) {
	eventsPath = strings.TrimSpace(eventsPath)
	if eventsPath == "" {
		return "", nil, nil, fmt.Errorf("events file is required")
	}

	runDir := filepath.Dir(eventsPath)
	runID := filepath.Base(runDir)
	if runID == "." || runID == string(filepath.Separator) || strings.TrimSpace(runID) == "" {
		return "", nil, nil, fmt.Errorf("invalid events file path %q", eventsPath)
	}

	compiled, err := workflow.LoadResolvedFile(filepath.Join(runDir, "workflow.json"))
	if err != nil {
		return "", nil, nil, err
	}

	events, err := store.ReadEventsFile(eventsPath)
	if err != nil {
		return "", nil, nil, err
	}

	return runID, compiled, events, nil
}

func isMissingRunStateError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, os.ErrNotExist) {
		return true
	}

	var storeErr *store.Error
	return errors.As(err, &storeErr) && storeErr.Code == store.ErrorCodePath && errors.Is(storeErr.Err, os.ErrNotExist)
}

func formatStepSummaries(compiled *workflow.CompiledWorkflow, snapshot runtime.Snapshot) []string {
	if compiled == nil {
		return nil
	}

	lines := make([]string, 0, len(compiled.TopologicalOrder))
	for _, stepID := range compiled.TopologicalOrder {
		step := snapshot.Steps[stepID]
		line := fmt.Sprintf("step=%s state=%s", stepID, step.State)
		if summary := strings.TrimSpace(step.Summary); summary != "" {
			line += fmt.Sprintf(" summary=%q", summary)
		}
		lines = append(lines, line)
	}

	return lines
}

type runtimeWiring struct {
	LookupAdapter runtime.AdapterLookup
	CommandRunner runtime.CommandRunner
	RepoPath      string
	WorkingDir    string
}

func buildRuntimeWiring(runStore *store.Store, flags *sharedFlags) (runtimeWiring, error) {
	if runStore == nil {
		return runtimeWiring{}, fmt.Errorf("run store is required")
	}

	repoPath, workingDir, err := resolveExecutionContext(runStore, flags)
	if err != nil {
		return runtimeWiring{}, err
	}

	return runtimeWiring{
		LookupAdapter: lookupRegisteredAdapter,
		CommandRunner: newSupervisorCommandRunner(runStore, workingDir, providerTimeout(flags)),
		RepoPath:      repoPath,
		WorkingDir:    workingDir,
	}, nil
}

func lookupRegisteredAdapter(step workflow.CompiledStep) (adapters.Adapter, error) {
	if step.Agent == nil {
		return nil, fmt.Errorf("agent config missing for step %q", step.ID)
	}

	provider := strings.TrimSpace(step.Agent.Agent)
	if adapter, ok := lookupBuiltinLocalAdapter(provider); ok {
		return adapter, nil
	}

	registration, ok := adapters.Lookup(provider)
	if !ok {
		return nil, fmt.Errorf("adapter %q is not registered", provider)
	}

	return registration.New(), nil
}

func resolveExecutionContext(runStore *store.Store, flags *sharedFlags) (string, string, error) {
	if runStore == nil {
		return "", "", fmt.Errorf("run store is required")
	}

	if flags != nil && strings.TrimSpace(flags.repo) != "" {
		repoPath, err := filepath.Abs(filepath.Clean(flags.repo))
		if err != nil {
			return "", "", err
		}

		return repoPath, repoPath, nil
	}

	checkpoint, _, err := runStore.LoadCheckpoint()
	if err == nil && checkpoint != nil {
		repoPath := strings.TrimSpace(checkpoint.RepoPath)
		workingDir := strings.TrimSpace(checkpoint.WorkingDir)
		if repoPath == "" {
			repoPath = workingDir
		}
		if workingDir == "" {
			workingDir = repoPath
		}
		if repoPath != "" || workingDir != "" {
			return repoPath, workingDir, nil
		}
	}

	workingDir, err := filepath.Abs(".")
	if err != nil {
		return "", "", err
	}

	return workingDir, workingDir, nil
}

func providerTimeout(flags *sharedFlags) time.Duration {
	if flags == nil {
		return 0
	}

	return flags.providerTimeout
}

func acquireRepoLock(flags *sharedFlags, runID, runsRoot string) (*runtime.RepoLock, error) {
	manager := runtime.NewRepoLockManager(runtime.Dependencies{})
	return manager.Acquire(runtime.AcquireOptions{
		RunID:         runID,
		RepoPath:      repoPath(flags),
		RunsRoot:      runsRoot,
		RepoLocksRoot: repoLocksRoot(runsRoot),
		AllowDirty:    flags != nil && flags.allowDirty,
	})
}

func repoPath(flags *sharedFlags) string {
	if flags == nil || strings.TrimSpace(flags.repo) == "" {
		return "."
	}

	return flags.repo
}

func repoLocksRoot(runsRoot string) string {
	runsRoot = strings.TrimSpace(runsRoot)
	if runsRoot == "" {
		return runtime.DefaultRepoLocksRoot
	}

	return filepath.Join(filepath.Dir(runsRoot), "locks")
}

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
		return nil, fmt.Errorf("run id is required")
	}
	if strings.TrimSpace(request.StepID) == "" {
		return nil, fmt.Errorf("step id is required")
	}
	if strings.TrimSpace(request.AttemptID) == "" {
		return nil, fmt.Errorf("attempt id is required")
	}
	if strings.TrimSpace(request.Command) == "" {
		return nil, fmt.Errorf("command is required")
	}

	commandSpec, err := parseCommandSpec(request.Command, r.commandWorkingDir(request))
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
		return nil, fmt.Errorf("execution is required")
	}
	if !execution.State.Normalizable() {
		return nil, fmt.Errorf("execution state cannot be normalized")
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

func executionFromStepResult(result *adapters.StepResult) *adapters.Execution {
	if result == nil {
		return nil
	}

	return &adapters.Execution{
		Handle:           result.Handle,
		State:            result.Status,
		Summary:          result.Summary,
		OutputText:       result.OutputText,
		StructuredOutput: result.StructuredOutput,
		ArtifactRefs:     result.ArtifactRefs,
		Logs:             result.Logs,
	}
}

func cloneExecution(execution *adapters.Execution) *adapters.Execution {
	if execution == nil {
		return nil
	}

	cloned := *execution
	cloned.ArtifactRefs = append([]adapters.ArtifactRef(nil), execution.ArtifactRefs...)
	cloned.Logs = append([]adapters.LogEntry(nil), execution.Logs...)
	return &cloned
}

func sanitizePathToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "step"
	}

	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
	return replacer.Replace(value)
}

func parseCommandSpec(command, dir string) (executor.CommandSpec, error) {
	argv, err := tokenizeCommand(command)
	if err != nil {
		return executor.CommandSpec{}, err
	}
	if len(argv) == 0 {
		return executor.CommandSpec{}, fmt.Errorf("command is required")
	}

	return executor.CommandSpec{
		Path: argv[0],
		Args: argv[1:],
		Dir:  dir,
	}, nil
}

func tokenizeCommand(command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}

	args := make([]string, 0, 4)
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	tokenStarted := false

	flush := func() {
		args = append(args, current.String())
		current.Reset()
		tokenStarted = false
	}

	for _, r := range command {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
			tokenStarted = true
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				current.WriteRune(r)
			}
			tokenStarted = true
		case inDouble:
			switch r {
			case '\\':
				escaped = true
				tokenStarted = true
			case '"':
				inDouble = false
			default:
				current.WriteRune(r)
				tokenStarted = true
			}
		default:
			switch r {
			case '\\':
				escaped = true
				tokenStarted = true
			case '\'':
				inSingle = true
				tokenStarted = true
			case '"':
				inDouble = true
				tokenStarted = true
			case ' ', '\t', '\n', '\r':
				if tokenStarted {
					flush()
				}
			default:
				current.WriteRune(r)
				tokenStarted = true
			}
		}
	}

	if escaped {
		current.WriteRune('\\')
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quoted string in command %s", strconv.Quote(command))
	}
	if tokenStarted {
		flush()
	}

	return args, nil
}
