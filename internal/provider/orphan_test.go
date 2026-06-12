package provider

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReapOrphansKillsMatchingDetachedProcessAndRemovesPIDFile(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	pidFile := filepath.Join(rootDir, "provider-logs", "build", "attempt.pid.json")
	cmd, record := startDetachedBashSleep(t)

	writePIDRecord(t, pidFile, record)

	report, err := ReapOrphans(rootDir)
	if err != nil {
		t.Fatalf("ReapOrphans() error = %v", err)
	}
	if len(report.Reaped) != 1 {
		t.Fatalf("ReapOrphans() reaped = %d, want 1", len(report.Reaped))
	}
	if report.Reaped[0].StepID != "build" {
		t.Fatalf("reaped StepID = %q, want build", report.Reaped[0].StepID)
	}

	waitForCommandExit(t, cmd)
	if _, err := os.Stat(pidFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pidfile stat after reap err = %v, want not exist", err)
	}
}

func TestReapOrphansCleansDeadPIDFile(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	pidFile := filepath.Join(rootDir, "provider-logs", "dead", "attempt.pid.json")
	writePIDRecord(t, pidFile, PIDRecord{PID: 99999999, PGID: 99999999, Binary: "/bin/bash"})

	report, err := ReapOrphans(rootDir)
	if err != nil {
		t.Fatalf("ReapOrphans() error = %v", err)
	}
	if len(report.Reaped) != 0 {
		t.Fatalf("ReapOrphans() reaped = %d, want 0", len(report.Reaped))
	}
	if len(report.Cleaned) != 1 {
		t.Fatalf("ReapOrphans() cleaned = %d, want 1", len(report.Cleaned))
	}
	if report.Cleaned[0].Alive {
		t.Fatal("dead pidfile classified Alive=true, want false")
	}
	if _, err := os.Stat(pidFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pidfile stat after cleanup err = %v, want not exist", err)
	}
}

func TestReapOrphansCleansMismatchedLivePIDWithoutKilling(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	pidFile := filepath.Join(rootDir, "provider-logs", "mismatch", "attempt.pid.json")
	writePIDRecord(t, pidFile, PIDRecord{PID: os.Getpid(), PGID: os.Getpid(), Binary: "/bin/bash"})

	report, err := ReapOrphans(rootDir)
	if err != nil {
		t.Fatalf("ReapOrphans() error = %v", err)
	}
	if len(report.Reaped) != 0 {
		t.Fatalf("ReapOrphans() reaped = %d, want 0", len(report.Reaped))
	}
	if len(report.Cleaned) != 1 {
		t.Fatalf("ReapOrphans() cleaned = %d, want 1", len(report.Cleaned))
	}
	if !report.Cleaned[0].Alive || report.Cleaned[0].IdentityMatched {
		t.Fatalf("mismatch classification Alive=%v IdentityMatched=%v, want alive mismatch", report.Cleaned[0].Alive, report.Cleaned[0].IdentityMatched)
	}
	if _, err := os.Stat(pidFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pidfile stat after cleanup err = %v, want not exist", err)
	}
}

func TestFindOrphansClassifiesAliveMismatchAndDeadReadOnly(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	alivePath := filepath.Join(rootDir, "provider-logs", "alive", "attempt.pid.json")
	deadPath := filepath.Join(rootDir, "provider-logs", "dead", "attempt.pid.json")
	mismatchPath := filepath.Join(rootDir, "provider-logs", "mismatch", "attempt.pid.json")
	cmd, record := startDetachedBashSleep(t)
	defer cleanupDetachedCommand(cmd, record.PGID)

	writePIDRecord(t, alivePath, record)
	writePIDRecord(t, deadPath, PIDRecord{PID: 99999999, PGID: 99999999, Binary: "/bin/bash"})
	writePIDRecord(t, mismatchPath, PIDRecord{PID: os.Getpid(), PGID: os.Getpid(), Binary: "/bin/bash"})

	orphans, err := FindOrphans(rootDir)
	if err != nil {
		t.Fatalf("FindOrphans() error = %v", err)
	}
	if len(orphans) != 3 {
		t.Fatalf("FindOrphans() returned %d records, want 3", len(orphans))
	}

	byStep := map[string]OrphanProcess{}
	for _, orphan := range orphans {
		byStep[orphan.StepID] = orphan
	}
	if !byStep["alive"].Alive || !byStep["alive"].IdentityMatched {
		t.Fatalf("alive classification = %#v, want alive+match", byStep["alive"])
	}
	if byStep["dead"].Alive || byStep["dead"].IdentityMatched {
		t.Fatalf("dead classification = %#v, want dead", byStep["dead"])
	}
	if !byStep["mismatch"].Alive || byStep["mismatch"].IdentityMatched {
		t.Fatalf("mismatch classification = %#v, want alive+mismatch", byStep["mismatch"])
	}

	for _, path := range []string{alivePath, deadPath, mismatchPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("FindOrphans mutated pidfile %q: %v", path, err)
		}
	}
}

func startDetachedBashSleep(t *testing.T) (*exec.Cmd, PIDRecord) {
	t.Helper()

	cmd := exec.Command("/bin/bash", "-c", "sleep 30 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start detached bash: %v", err)
	}

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		cleanupDetachedCommand(cmd, cmd.Process.Pid)
		t.Fatalf("Getpgid(%d): %v", cmd.Process.Pid, err)
	}

	record := PIDRecord{PID: cmd.Process.Pid, PGID: pgid, Binary: "/bin/bash", StartedAt: time.Now().UTC().Format(time.RFC3339)}

	return cmd, record
}

func writePIDRecord(t *testing.T, path string, record PIDRecord) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir pidfile dir: %v", err)
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal pid record: %v", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
}

func cleanupDetachedCommand(cmd *exec.Cmd, pgid int) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	if pgid > 1 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}

	_ = cmd.Wait()
}

func waitForCommandExit(t *testing.T, cmd *exec.Cmd) {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
		t.Fatal("detached command did not exit after ReapOrphans")
	}
}
