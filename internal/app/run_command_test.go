package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
)

func TestSupervisorCommandRunnerWritesLogsAndArtifacts(t *testing.T) {
	runStore := openTestRunStore(t, "run-logs")
	runner := newSupervisorCommandRunner(runStore, ".", 0)

	execution, err := runner.Start(context.Background(), runtime.CommandRequest{
		RunID:     "run-logs",
		StepID:    "prepare",
		AttemptID: "attempt-01",
		Command:   "printf 'hello\\n'",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	collected, err := runner.PollOrCollect(context.Background(), execution.Handle)
	if err != nil {
		t.Fatalf("PollOrCollect() error = %v", err)
	}
	if collected.State != adapters.ExecutionStateSucceeded {
		t.Fatalf("collected.State = %q, want %q", collected.State, adapters.ExecutionStateSucceeded)
	}

	result, err := runner.NormalizeResult(context.Background(), collected)
	if err != nil {
		t.Fatalf("NormalizeResult() error = %v", err)
	}
	if strings.TrimSpace(result.OutputText) != "hello" {
		t.Fatalf("result.OutputText = %q, want %q", result.OutputText, "hello")
	}

	stdoutPath := filepath.Join(runStore.Layout().RunDir, providerLogsDir, "prepare", "attempt-01-stdout.log")
	stderrPath := filepath.Join(runStore.Layout().RunDir, providerLogsDir, "prepare", "attempt-01-stderr.log")
	stdout, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("ReadFile(stdout log) error = %v", err)
	}
	if strings.TrimSpace(string(stdout)) != "hello" {
		t.Fatalf("stdout log = %q, want %q", string(stdout), "hello\n")
	}
	if _, err := os.Stat(stderrPath); err != nil {
		t.Fatalf("Stat(stderr log) error = %v", err)
	}

	artifacts, err := runStore.LoadArtifacts()
	if err != nil {
		t.Fatalf("LoadArtifacts() error = %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("len(artifacts) = %d, want 2", len(artifacts))
	}
	if artifacts[0].Path != filepath.Join(providerLogsDir, "prepare", "attempt-01-stdout.log") {
		t.Fatalf("artifacts[0].Path = %q", artifacts[0].Path)
	}
	if artifacts[1].Path != filepath.Join(providerLogsDir, "prepare", "attempt-01-stderr.log") {
		t.Fatalf("artifacts[1].Path = %q", artifacts[1].Path)
	}
}

func TestSupervisorCommandRunnerInterruptsRunningCommand(t *testing.T) {
	runStore := openTestRunStore(t, "run-interrupt")
	runner := newSupervisorCommandRunner(runStore, ".", 0)

	execution, err := runner.Start(context.Background(), runtime.CommandRequest{
		RunID:     "run-interrupt",
		StepID:    "prepare",
		AttemptID: "attempt-01",
		Command:   "sleep 5",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	interrupted, err := runner.Interrupt(context.Background(), execution.Handle)
	if err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	if interrupted.State != adapters.ExecutionStateInterrupted {
		t.Fatalf("interrupted.State = %q, want %q", interrupted.State, adapters.ExecutionStateInterrupted)
	}

	result, err := runner.NormalizeResult(context.Background(), interrupted)
	if err != nil {
		t.Fatalf("NormalizeResult() error = %v", err)
	}
	if result.Status != adapters.ExecutionStateInterrupted {
		t.Fatalf("result.Status = %q, want %q", result.Status, adapters.ExecutionStateInterrupted)
	}
}

func TestSupervisorCommandRunnerHonorsTimeout(t *testing.T) {
	runStore := openTestRunStore(t, "run-timeout")
	runner := newSupervisorCommandRunner(runStore, ".", 50*time.Millisecond)

	execution, err := runner.Start(context.Background(), runtime.CommandRequest{
		RunID:     "run-timeout",
		StepID:    "prepare",
		AttemptID: "attempt-01",
		Command:   "sleep 1",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	collected, err := runner.PollOrCollect(context.Background(), execution.Handle)
	if err != nil {
		t.Fatalf("PollOrCollect() error = %v", err)
	}
	if collected.State != adapters.ExecutionStateFailed {
		t.Fatalf("collected.State = %q, want %q", collected.State, adapters.ExecutionStateFailed)
	}
	if !strings.Contains(collected.Summary, "timeout") {
		t.Fatalf("collected.Summary = %q, want timeout", collected.Summary)
	}
}

func TestTokenizeCommand(t *testing.T) {
	args, err := tokenizeCommand(`printf 'hello\n'`)
	if err != nil {
		t.Fatalf("tokenizeCommand() error = %v", err)
	}
	want := []string{"printf", "hello\\n"}
	if len(args) != len(want) {
		t.Fatalf("len(args) = %d, want %d (%v)", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestTokenizeCommandRejectsUnterminatedQuote(t *testing.T) {
	_, err := tokenizeCommand(`printf 'oops`)
	if err == nil {
		t.Fatal("tokenizeCommand() error = nil, want unterminated quote")
	}
	if !strings.Contains(err.Error(), "unterminated quoted string") {
		t.Fatalf("tokenizeCommand() error = %v, want unterminated quoted string", err)
	}
}

func TestAcquireRepoLockHonorsDirtyAndReleases(t *testing.T) {
	fixture := newAppRepoFixture(t)
	writeAppFile(t, filepath.Join(fixture.repoDir, "tracked.txt"), []byte("dirty now\n"))

	_, err := acquireRepoLock(&sharedFlags{repo: fixture.repoDir}, "run-dirty", fixture.runsRoot)
	if err == nil {
		t.Fatal("acquireRepoLock() error = nil, want dirty worktree rejection")
	}

	var runtimeErr *runtime.Error
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("error type = %T, want *runtime.Error", err)
	}
	if runtimeErr.Code != runtime.ErrorCodeDirtyWorktree {
		t.Fatalf("error code = %q, want %q", runtimeErr.Code, runtime.ErrorCodeDirtyWorktree)
	}

	lock, err := acquireRepoLock(&sharedFlags{repo: fixture.repoDir, allowDirty: true}, "run-dirty", fixture.runsRoot)
	if err != nil {
		t.Fatalf("acquireRepoLock() with allowDirty error = %v", err)
	}

	if _, err := os.Stat(lock.RepoLockPath()); err != nil {
		t.Fatalf("Stat(repo lock) error = %v", err)
	}
	if _, err := os.Stat(lock.RunLockPath()); err != nil {
		t.Fatalf("Stat(run lock) error = %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(lock.RepoLockPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(repo lock after release) error = %v, want not exist", err)
	}
}

func TestExecuteWorkflowRunUsesRepoLockAndDirtyGuard(t *testing.T) {
	fixture := newAppRepoFixture(t)
	workflowPath := writeWorkflowFile(t, fixture.repoDir, "lock.yaml", "sleep 1")
	stateDir := filepath.Join(fixture.runsRoot, "run-lock")
	runLockPath := filepath.Join(stateDir, "locks", "repo.lock.json")

	errCh := make(chan error, 1)
	go func() {
		errCh <- executeWorkflowRun(workflowPath, &sharedFlags{repo: fixture.repoDir, stateDir: stateDir, allowDirty: false}, ioDiscard{})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(runLockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run lock %q not observed before deadline", runLockPath)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("executeWorkflowRun() error = %v", err)
	}
	if _, err := os.Stat(runLockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(run lock after run) error = %v, want not exist", err)
	}

	writeAppFile(t, filepath.Join(fixture.repoDir, "tracked.txt"), []byte("dirty now\n"))
	err := executeWorkflowRun(workflowPath, &sharedFlags{repo: fixture.repoDir, stateDir: filepath.Join(fixture.runsRoot, "run-dirty")}, ioDiscard{})
	if err == nil {
		t.Fatal("executeWorkflowRun() dirty error = nil, want dirty worktree rejection")
	}
	if !strings.Contains(err.Error(), "dirty worktree") {
		t.Fatalf("executeWorkflowRun() error = %v, want dirty worktree", err)
	}

	if err := executeWorkflowRun(workflowPath, &sharedFlags{repo: fixture.repoDir, stateDir: filepath.Join(fixture.runsRoot, "run-dirty-allowed"), allowDirty: true}, ioDiscard{}); err != nil {
		t.Fatalf("executeWorkflowRun() with allowDirty error = %v", err)
	}
}

func TestBuildRuntimeWiringUsesPersistedWorkingDirWithoutFlags(t *testing.T) {
	runStore := openTestRunStore(t, "run-context")
	workingDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(workingDir) error = %v", err)
	}

	if err := runStore.SaveCheckpoint(&store.Checkpoint{
		RunID:        runStore.Layout().RunID,
		RepoPath:     workingDir,
		WorkingDir:   workingDir,
		State:        string(runtime.RunStatePaused),
		LastSequence: 1,
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	wiring, err := buildRuntimeWiring(runStore, nil)
	if err != nil {
		t.Fatalf("buildRuntimeWiring() error = %v", err)
	}
	if wiring.WorkingDir != workingDir {
		t.Fatalf("wiring.WorkingDir = %q, want %q", wiring.WorkingDir, workingDir)
	}
	if wiring.RepoPath != workingDir {
		t.Fatalf("wiring.RepoPath = %q, want %q", wiring.RepoPath, workingDir)
	}
}

func openTestRunStore(t *testing.T, runID string) *store.Store {
	t.Helper()

	runStore, err := store.Open(t.TempDir(), runID)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	return runStore
}

type appRepoFixture struct {
	repoDir  string
	runsRoot string
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func newAppRepoFixture(t *testing.T) appRepoFixture {
	t.Helper()

	baseDir := t.TempDir()
	repoDir := filepath.Join(baseDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(repo) error = %v", err)
	}

	runGitInRepo(t, repoDir, "init", "--quiet")
	writeAppFile(t, filepath.Join(repoDir, "tracked.txt"), []byte("clean\n"))
	runGitInRepo(t, repoDir, "add", "tracked.txt")
	runGitCommitInRepo(t, repoDir, "init")

	return appRepoFixture{
		repoDir:  repoDir,
		runsRoot: filepath.Join(baseDir, "ref", "tmp", "runs"),
	}
}

func runGitCommitInRepo(t *testing.T, repoDir, message string) {
	t.Helper()

	cmd := exec.Command(
		"git",
		"-C", repoDir,
		"-c", "user.name=App Test",
		"-c", "user.email=app-test@example.com",
		"commit", "--quiet", "-m", message,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit error = %v, output = %s", err, strings.TrimSpace(string(output)))
	}
}

func runGitInRepo(t *testing.T, repoDir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v error = %v, output = %s", args, err, strings.TrimSpace(string(output)))
	}
}

func writeAppFile(t *testing.T, path string, content []byte) {
	t.Helper()

	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func writeWorkflowFile(t *testing.T, repoDir, name, command string) string {
	t.Helper()

	path := filepath.Join(repoDir, name)
	content := fmt.Sprintf("apiVersion: cogito/v1alpha1\nkind: Workflow\nmetadata:\n  name: lock-check\nsteps:\n  - id: run\n    kind: command\n    command: %s\n", command)
	writeAppFile(t, path, []byte(content))
	runGitInRepo(t, repoDir, "add", name)
	runGitCommitInRepo(t, repoDir, "add workflow "+name)
	return path
}
