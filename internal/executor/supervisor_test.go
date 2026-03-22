package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JackDrogon/Cogito/internal/adapters"
)

const helperProcessEnv = "EXECUTOR_HELPER_PROCESS"

func TestProcessTimeoutKillsChildren(t *testing.T) {
	t.Parallel()

	t.Log("child process terminated")

	tempDir := t.TempDir()
	childPIDPath := filepath.Join(tempDir, "child.pid")
	childTerminatedPath := filepath.Join(tempDir, "child.terminated")
	stdoutPath := filepath.Join(tempDir, "stdout.log")
	stderrPath := filepath.Join(tempDir, "stderr.log")

	supervisor := NewSupervisor()
	result, err := supervisor.Run(t.Context(), RunRequest{
		Handle: newHandle("timeout-step", "timeout-session"),
		Command: CommandSpec{
			Path: os.Args[0],
			Args: []string{"-test.run=TestHelperProcess", "--", "parent-with-child", childPIDPath, childTerminatedPath},
			Env:  []string{helperProcessEnv + "=1"},
		},
		Timeout:    300 * time.Millisecond,
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
		Normalizer: DefaultNormalizer(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Status != adapters.ExecutionStateFailed {
		t.Fatalf("Run().Status = %q, want %q", result.Status, adapters.ExecutionStateFailed)
	}

	if !strings.Contains(result.Summary, "child process terminated") {
		t.Fatalf("Run().Summary = %q, want child process terminated text", result.Summary)
	}

	if err := waitForFile(childTerminatedPath, 2*time.Second); err != nil {
		t.Fatalf("waitForFile(childTerminatedPath) error = %v", err)
	}

	childPID, err := readPID(childPIDPath)
	if err != nil {
		t.Fatalf("readPID() error = %v", err)
	}

	if err := waitForProcessExit(childPID, 2*time.Second); err != nil {
		t.Fatalf("waitForProcessExit() error = %v", err)
	}

	assertFileExists(t, stdoutPath)
	assertFileExists(t, stderrPath)
}

func TestMalformedStructuredOutputFailsExplicitly(t *testing.T) {
	t.Parallel()

	t.Log("malformed structured output")

	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.log")
	stderrPath := filepath.Join(tempDir, "stderr.log")

	supervisor := NewSupervisor()
	result, err := supervisor.Run(t.Context(), RunRequest{
		Handle: newHandle("structured-step", "structured-session"),
		Command: CommandSpec{
			Path: os.Args[0],
			Args: []string{"-test.run=TestHelperProcess", "--", "invalid-json"},
			Env:  []string{helperProcessEnv + "=1"},
		},
		Timeout:    time.Second,
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
		Normalizer: JSONOutputNormalizer(),
	})
	if err == nil {
		t.Fatalf("Run() error = nil, want malformed structured output")
	}

	if result != nil {
		t.Fatalf("Run() result = %#v, want nil on malformed structured output", result)
	}

	if !strings.Contains(err.Error(), "malformed structured output") {
		t.Fatalf("Run() error = %v, want malformed structured output text", err)
	}

	stdoutData, readErr := os.ReadFile(stdoutPath)
	if readErr != nil {
		t.Fatalf("ReadFile(stdoutPath) error = %v", readErr)
	}

	if strings.TrimSpace(string(stdoutData)) != "{" {
		t.Fatalf("stdout = %q, want malformed JSON payload", string(stdoutData))
	}
	assertFileExists(t, stderrPath)
}

func TestInterruptStopsRunningProcess(t *testing.T) {
	t.Parallel()

	t.Log("child process terminated")

	tempDir := t.TempDir()
	readyPath := filepath.Join(tempDir, "ready")
	stdoutPath := filepath.Join(tempDir, "stdout.log")
	stderrPath := filepath.Join(tempDir, "stderr.log")

	supervisor := NewSupervisor()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	type runResult struct {
		result *adapters.StepResult
		err    error
	}

	done := make(chan runResult, 1)
	go func() {
		result, err := supervisor.Run(ctx, RunRequest{
			Handle: newHandle("interrupt-step", "interrupt-session"),
			Command: CommandSpec{
				Path: os.Args[0],
				Args: []string{"-test.run=TestHelperProcess", "--", "ready-sleep", readyPath},
				Env:  []string{helperProcessEnv + "=1"},
			},
			Timeout:    5 * time.Second,
			StdoutPath: stdoutPath,
			StderrPath: stderrPath,
			Normalizer: DefaultNormalizer(),
		})
		done <- runResult{result: result, err: err}
	}()

	if err := waitForFile(readyPath, 2*time.Second); err != nil {
		t.Fatalf("waitForFile(readyPath) error = %v", err)
	}

	cancel()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Run() error = %v", got.err)
		}

		if got.result == nil {
			t.Fatal("Run() result = nil, want interrupted result")
		}

		if got.result.Status != adapters.ExecutionStateInterrupted {
			t.Fatalf("Run().Status = %q, want %q", got.result.Status, adapters.ExecutionStateInterrupted)
		}

		if !strings.Contains(got.result.Summary, "child process terminated") {
			t.Fatalf("Run().Summary = %q, want child process terminated text", got.result.Summary)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not return after interrupt")
	}

	assertFileExists(t, stdoutPath)
	assertFileExists(t, stderrPath)
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperProcessEnv) != "1" {
		return
	}

	args := os.Args
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator == -1 || separator+1 >= len(args) {
		os.Exit(2)
	}

	mode := args[separator+1]
	modeArgs := args[separator+2:]

	switch mode {
	case "invalid-json":
		_, _ = os.Stdout.WriteString("{\n")
		os.Exit(0)
	case "ready-sleep":
		if len(modeArgs) != 1 {
			os.Exit(2)
		}
		if err := os.WriteFile(modeArgs[0], []byte("ready"), 0o600); err != nil {
			os.Exit(3)
		}
		waitForSignalAndExit("")
	case "parent-with-child":
		if len(modeArgs) != 2 {
			os.Exit(2)
		}

		childPIDPath := modeArgs[0]
		childTerminatedPath := modeArgs[1]
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "child-sleep", childTerminatedPath)
		cmd.Env = append(os.Environ(), helperProcessEnv+"=1")
		if err := cmd.Start(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "start child: %v\n", err)
			os.Exit(4)
		}
		if err := os.WriteFile(childPIDPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "write child pid: %v\n", err)
			os.Exit(5)
		}
		waitForSignalAndExit("")
	case "child-sleep":
		if len(modeArgs) != 1 {
			os.Exit(2)
		}
		waitForSignalAndExit(modeArgs[0])
	default:
		os.Exit(2)
	}
}

func waitForSignalAndExit(terminatedPath string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(ch)

	select {
	case <-ch:
		if terminatedPath != "" {
			_ = os.WriteFile(terminatedPath, []byte("terminated"), 0o600)
		}
		os.Exit(0)
	case <-time.After(30 * time.Second):
		os.Exit(0)
	}
}

func newHandle(stepID, sessionID string) adapters.ExecutionHandle {
	return adapters.ExecutionHandle{
		RunID:             "run-123",
		StepID:            stepID,
		AttemptID:         "attempt-1",
		ProviderSessionID: sessionID,
	}
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}

	return fmt.Errorf("file %s not observed before timeout", path)
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}

	return pid, nil
}

func waitForProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return nil
		}
		if err != nil && err != syscall.EPERM {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}

	return fmt.Errorf("process %d still running after timeout", pid)
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
}
