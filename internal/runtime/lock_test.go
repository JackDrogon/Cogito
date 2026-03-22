package runtime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepoLockSingleRunPolicy(t *testing.T) {
	fixture := newRuntimeFixture(t)

	manager1 := NewRepoLockManager(Dependencies{
		Now:      fixture.fixedNow,
		PID:      111,
		Hostname: "test-host",
		ProcessRunning: func(pid int) bool {
			return pid == 111
		},
	})

	lock1, err := manager1.Acquire(AcquireOptions{
		RunID:         "run-1",
		RepoPath:      fixture.repoDir,
		RunsRoot:      fixture.runsRoot,
		RepoLocksRoot: fixture.repoLocksRoot,
	})
	if err != nil {
		fatalfWithOutput(t, "Acquire(run-1) error = %v", err)
	}

	assertExists(t, lock1.RepoLockPath())
	assertExists(t, lock1.RunLockPath())

	manager2 := NewRepoLockManager(Dependencies{
		Now:      fixture.fixedNow,
		PID:      222,
		Hostname: "test-host",
		ProcessRunning: func(pid int) bool {
			return pid == 111
		},
	})

	_, err = manager2.Acquire(AcquireOptions{
		RunID:         "run-2",
		RepoPath:      fixture.repoDir,
		RunsRoot:      fixture.runsRoot,
		RepoLocksRoot: fixture.repoLocksRoot,
	})
	if err == nil {
		t.Fatal("Acquire(run-2) error = nil, want repo lock conflict")
	}

	var runtimeErr *Error
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("error type = %T, want *runtime.Error", err)
	}

	if runtimeErr.Code != ErrorCodeLock {
		t.Fatalf("error code = %q, want %q", runtimeErr.Code, ErrorCodeLock)
	}

	if !strings.Contains(err.Error(), "repo lock already held") {
		t.Fatalf("error = %q, want repo lock conflict", err)
	}

	if err := lock1.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	assertMissing(t, lock1.RepoLockPath())
	assertMissing(t, lock1.RunLockPath())
	assertMissing(t, filepath.Join(fixture.runsRoot, "run-2", "locks", repoLockFileName))
}

func TestStaleLockRecovery(t *testing.T) {
	fixture := newRuntimeFixture(t)

	lock1, err := NewRepoLockManager(Dependencies{
		Now:      fixture.fixedNow,
		PID:      111,
		Hostname: "test-host",
		ProcessRunning: func(pid int) bool {
			return pid == 111
		},
	}).Acquire(AcquireOptions{
		RunID:         "run-stale",
		RepoPath:      fixture.repoDir,
		RunsRoot:      fixture.runsRoot,
		RepoLocksRoot: fixture.repoLocksRoot,
	})
	if err != nil {
		t.Fatalf("Acquire(run-stale) error = %v", err)
	}

	manager2 := NewRepoLockManager(Dependencies{
		Now:      func() time.Time { return fixture.fixedNow().Add(5 * time.Minute) },
		PID:      222,
		Hostname: "test-host",
		ProcessRunning: func(pid int) bool {
			return false
		},
	})

	lock2, err := manager2.Acquire(AcquireOptions{
		RunID:         "run-recovered",
		RepoPath:      fixture.repoDir,
		RunsRoot:      fixture.runsRoot,
		RepoLocksRoot: fixture.repoLocksRoot,
	})
	if err != nil {
		t.Fatalf("Acquire(run-recovered) error = %v", err)
	}

	assertMissing(t, lock1.RunLockPath())
	assertExists(t, lock2.RepoLockPath())
	assertExists(t, lock2.RunLockPath())

	metadata, err := readLockMetadata(lock2.RepoLockPath())
	if err != nil {
		t.Fatalf("readLockMetadata() error = %v", err)
	}

	if metadata.RunID != "run-recovered" {
		t.Fatalf("metadata.RunID = %q, want %q", metadata.RunID, "run-recovered")
	}

	t.Log("stale lock reclaimed")

	if err := lock2.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
}

func TestDirtyWorktreeRejected(t *testing.T) {
	fixture := newRuntimeFixture(t)

	writeFile(t, filepath.Join(fixture.repoDir, "tracked.txt"), []byte("dirty now\n"))

	manager := NewRepoLockManager(Dependencies{
		Now:      fixture.fixedNow,
		PID:      333,
		Hostname: "test-host",
		ProcessRunning: func(pid int) bool {
			return pid == 333
		},
	})

	_, err := manager.Acquire(AcquireOptions{
		RunID:         "run-dirty",
		RepoPath:      fixture.repoDir,
		RunsRoot:      fixture.runsRoot,
		RepoLocksRoot: fixture.repoLocksRoot,
	})
	if err == nil {
		t.Fatal("Acquire() error = nil, want dirty worktree rejection")
	}

	var runtimeErr *Error
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("error type = %T, want *runtime.Error", err)
	}

	if runtimeErr.Code != ErrorCodeDirtyWorktree {
		t.Fatalf("error code = %q, want %q", runtimeErr.Code, ErrorCodeDirtyWorktree)
	}

	t.Log("dirty worktree")

	lock, err := manager.Acquire(AcquireOptions{
		RunID:         "run-dirty-override",
		RepoPath:      fixture.repoDir,
		RunsRoot:      fixture.runsRoot,
		RepoLocksRoot: fixture.repoLocksRoot,
		AllowDirty:    true,
	})
	if err != nil {
		t.Fatalf("Acquire() with AllowDirty error = %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
}

type runtimeFixture struct {
	repoDir       string
	runsRoot      string
	repoLocksRoot string
	baseDir       string
	fixedNow      func() time.Time
}

func newRuntimeFixture(t *testing.T) runtimeFixture {
	t.Helper()

	baseDir := t.TempDir()
	repoDir := filepath.Join(baseDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(repo) error = %v", err)
	}

	runGitCommand(t, repoDir, "init", "--quiet")
	writeFile(t, filepath.Join(repoDir, "tracked.txt"), []byte("clean\n"))
	runGitCommand(t, repoDir, "add", "tracked.txt")
	runGitCommit(t, repoDir, "init")

	return runtimeFixture{
		repoDir:       repoDir,
		runsRoot:      filepath.Join(baseDir, "ref", "tmp", "runs"),
		repoLocksRoot: filepath.Join(baseDir, "ref", "tmp", "locks"),
		baseDir:       baseDir,
		fixedNow: func() time.Time {
			return time.Date(2026, time.March, 22, 12, 0, 0, 0, time.UTC)
		},
	}
}

func runGitCommit(t *testing.T, repoDir, message string) {
	t.Helper()

	cmd := exec.Command(
		"git",
		"-C", repoDir,
		"-c", "user.name=Runtime Test",
		"-c", "user.email=runtime-test@example.com",
		"commit", "--quiet", "-m", message,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit error = %v, output = %s", err, strings.TrimSpace(string(output)))
	}
}

func runGitCommand(t *testing.T, repoDir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v error = %v, output = %s", args, err, strings.TrimSpace(string(output)))
	}
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()

	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(%q) error = %v, want existing file", path, err)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(%q) error = %v, want not exist", path, err)
	}
}

func fatalfWithOutput(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format, args...)
}
