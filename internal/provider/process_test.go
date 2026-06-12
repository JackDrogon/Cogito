package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStartReturnsBeforeProcessExits asserts the async contract: StartProcess must
// return well before the child completes, even when the child sleeps.
func TestStartReturnsBeforeProcessExits(t *testing.T) {
	t.Parallel()

	const childSleep = 200 * time.Millisecond
	const startBudget = 50 * time.Millisecond

	begin := time.Now()
	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary: "/bin/bash",
		Args:   []string{"-c", "sleep 0.2; echo done"},
	})
	startElapsed := time.Since(begin)

	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	if startElapsed > startBudget {
		t.Fatalf("StartProcess blocked for %v (budget %v); expected immediate return", startElapsed, startBudget)
	}

	select {
	case result := <-session.Done:
		if result.Err != nil {
			t.Fatalf("ProcessResult.Err = %v, want nil", result.Err)
		}
		if !strings.Contains(string(result.Stdout), "done") {
			t.Errorf("stdout = %q, want substring %q", string(result.Stdout), "done")
		}
	case <-time.After(childSleep + 2*time.Second):
		t.Fatal("session.Done did not resolve within child sleep + grace")
	}
}

// TestStartFeedsPromptViaStdin verifies PromptOnStdin=true delivers Prompt to
// the child's stdin (codex/claude path).
func TestStartFeedsPromptViaStdin(t *testing.T) {
	t.Parallel()

	const prompt = "hello-from-stdin"

	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:        "/bin/cat",
		PromptOnStdin: true,
		Prompt:        prompt,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("ProcessResult.Err = %v, want nil", result.Err)
	}

	if string(result.Stdout) != prompt {
		t.Errorf("stdout = %q, want %q", string(result.Stdout), prompt)
	}
}

// TestStartUsesArgvPromptWhenStdinDisabled verifies opencode's path: prompt
// stays in argv and stdin is never opened.
func TestStartUsesArgvPromptWhenStdinDisabled(t *testing.T) {
	t.Parallel()

	const prompt = "hello-from-argv"

	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:        "/bin/echo",
		Args:          []string{"-n", prompt},
		PromptOnStdin: false,
		Prompt:        prompt,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("ProcessResult.Err = %v, want nil", result.Err)
	}

	if string(result.Stdout) != prompt {
		t.Errorf("stdout = %q, want %q", string(result.Stdout), prompt)
	}
}

// TestStartExtractsSessionIDFromStdout verifies the SessionIDPattern matcher
// updates Session.SessionID() in real time.
func TestStartExtractsSessionIDFromStdout(t *testing.T) {
	t.Parallel()

	const wantID = "12345678-1234-4321-aaaa-abcdef012345"
	stdoutBody := "preamble\nsession id: " + wantID + "\nmore output\n"

	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary: "/bin/bash",
		Args:   []string{"-c", "printf '%s' " + shellQuote(stdoutBody)},
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("ProcessResult.Err = %v, want nil", result.Err)
	}

	if result.SessionID != wantID {
		t.Fatalf("ProcessResult.SessionID = %q, want %q", result.SessionID, wantID)
	}

	if got := session.SessionID(); got != wantID {
		t.Errorf("Session.SessionID() = %q, want %q", got, wantID)
	}
}

// TestStartLogPathReceivesStreamedOutput verifies the LogPath file accumulates
// stdout+stderr while the process runs (drained when Done resolves).
func TestStartLogPathReceivesStreamedOutput(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "stream.log")
	const body = "line one\nline two\n"

	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:  "/bin/bash",
		Args:    []string{"-c", "printf '%s' " + shellQuote(body)},
		LogPath: logPath,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("ProcessResult.Err = %v, want nil", result.Err)
	}

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}

	if !bytes.Equal(contents, []byte(body)) {
		t.Errorf("log file = %q, want %q", string(contents), body)
	}
}

func TestStartPIDFileExistsWhileRunningAndRemovedAfterDone(t *testing.T) {
	t.Parallel()

	pidFile := filepath.Join(t.TempDir(), "attempt.pid.json")
	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:  "/bin/bash",
		Args:    []string{"-c", "sleep 0.3"},
		PIDFile: pidFile,
		Label:   "run=run-1 step=build attempt=attempt-1",
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	record := readPIDFileEventually(t, pidFile)
	if record.PID <= 1 {
		t.Fatalf("pidfile PID = %d, want real child pid", record.PID)
	}
	if record.PGID <= 1 {
		t.Fatalf("pidfile PGID = %d, want real process group", record.PGID)
	}
	if record.Binary != "/bin/bash" {
		t.Fatalf("pidfile Binary = %q, want /bin/bash", record.Binary)
	}
	if record.Label != "run=run-1 step=build attempt=attempt-1" {
		t.Fatalf("pidfile Label = %q", record.Label)
	}

	result := awaitResult(t, session, 2*time.Second)
	if result.Err != nil {
		t.Fatalf("ProcessResult.Err = %v, want nil", result.Err)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("pidfile stat after Done err = %v, want not exist", err)
	}
}

func TestCancelRemovesPIDFile(t *testing.T) {
	t.Parallel()

	pidFile := filepath.Join(t.TempDir(), "attempt.pid.json")
	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:    "/bin/bash",
		Args:      []string{"-c", "sleep 30"},
		PIDFile:   pidFile,
		ExitGrace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	_ = readPIDFileEventually(t, pidFile)
	session.Cancel()
	_ = awaitResult(t, session, 3*time.Second)

	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("pidfile stat after Cancel err = %v, want not exist", err)
	}
}

func TestPIDFileWriteFailureIsTolerated(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	notDir := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(notDir, []byte("file"), 0o600); err != nil {
		t.Fatalf("write not-dir fixture: %v", err)
	}

	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:  "/bin/bash",
		Args:    []string{"-c", "printf ok"},
		PIDFile: filepath.Join(notDir, "attempt.pid.json"),
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)
	if result.Err != nil {
		t.Fatalf("ProcessResult.Err = %v, want nil", result.Err)
	}
	if string(result.Stdout) != "ok" {
		t.Fatalf("stdout = %q, want ok", string(result.Stdout))
	}
}

// syncBuffer is a concurrency-safe bytes.Buffer satisfying the ExtraSink
// contract (the runner writes from two tee goroutines).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(data)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// TestStartExtraSinkReceivesLiveOutput verifies the AgentLoop-ported live
// output path: ExtraSink must receive the same byte stream that lands in the
// log file, including stderr.
func TestStartExtraSinkReceivesLiveOutput(t *testing.T) {
	t.Parallel()

	const body = "live stdout\n"
	sink := &syncBuffer{}

	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:    "/bin/bash",
		Args:      []string{"-c", "printf '%s' " + shellQuote(body) + "; printf 'live stderr\\n' >&2"},
		ExtraSink: sink,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("ProcessResult.Err = %v, want nil", result.Err)
	}

	got := sink.String()
	if !strings.Contains(got, "live stdout") {
		t.Errorf("ExtraSink = %q, want stdout content", got)
	}
	if !strings.Contains(got, "live stderr") {
		t.Errorf("ExtraSink = %q, want stderr content", got)
	}
}

// TestCancelInterruptsLongRunningChild verifies the SIGTERM escalation path:
// cancellation must terminate the child within the configured ExitGrace +
// small overhead, regardless of the child's nominal duration.
func TestCancelInterruptsLongRunningChild(t *testing.T) {
	t.Parallel()

	const childSleep = 100 * time.Second
	const exitGrace = 100 * time.Millisecond
	const interruptBudget = exitGrace + 2*time.Second

	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:    "/bin/bash",
		Args:      []string{"-c", "sleep 100"},
		ExitGrace: exitGrace,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	// Give the child a moment to actually exec sleep so signaling has a
	// real target. 20ms is well under the 100s sleep.
	time.Sleep(20 * time.Millisecond)

	begin := time.Now()
	session.Cancel()

	select {
	case result := <-session.Done:
		elapsed := time.Since(begin)
		if elapsed > interruptBudget {
			t.Errorf("interrupt took %v, want < %v", elapsed, interruptBudget)
		}
		if result.ExitCode == 0 {
			t.Errorf("ExitCode = 0, want non-zero after SIGTERM/SIGKILL")
		}
		_ = childSleep
	case <-time.After(interruptBudget + 2*time.Second):
		t.Fatal("session.Done did not resolve after Cancel within budget")
	}
}

func TestStartTimeoutInterruptsLongRunningChild(t *testing.T) {
	t.Parallel()

	const timeout = 100 * time.Millisecond
	const doneBudget = 2 * time.Second

	begin := time.Now()
	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:    "/bin/bash",
		Args:      []string{"-c", "sleep 30"},
		Timeout:   timeout,
		ExitGrace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, doneBudget)
	if elapsed := time.Since(begin); elapsed > doneBudget {
		t.Errorf("timeout completed after %v, want < %v", elapsed, doneBudget)
	}
	if result.TimeoutReason == "" {
		t.Fatal("TimeoutReason is empty, want timeout reason")
	}
	if !result.Interrupted {
		t.Fatal("Interrupted = false, want true after timeout cancellation")
	}
}

func TestStartIdleTimeoutInterruptsSilentChild(t *testing.T) {
	t.Parallel()

	const idleTimeout = 100 * time.Millisecond
	const doneBudget = 2 * time.Second

	begin := time.Now()
	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:      "/bin/bash",
		Args:        []string{"-c", "sleep 30"},
		IdleTimeout: idleTimeout,
		ExitGrace:   100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, doneBudget)
	if elapsed := time.Since(begin); elapsed > doneBudget {
		t.Errorf("idle timeout completed after %v, want < %v", elapsed, doneBudget)
	}
	if result.TimeoutReason == "" {
		t.Fatal("TimeoutReason is empty, want idle timeout reason")
	}
	if !result.Interrupted {
		t.Fatal("Interrupted = false, want true after idle timeout cancellation")
	}
}

func TestStartTimeoutsStayEmptyWhenChildFinishes(t *testing.T) {
	t.Parallel()

	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:      "/bin/bash",
		Args:        []string{"-c", "printf done"},
		Timeout:     time.Second,
		IdleTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)
	if result.TimeoutReason != "" {
		t.Fatalf("TimeoutReason = %q, want empty", result.TimeoutReason)
	}
	if result.Interrupted {
		t.Fatal("Interrupted = true, want false for natural exit")
	}
}

// TestStartGitCeilingDirectories asserts the env var is set to the parent of
// Dir so an agent in a non-repo working directory cannot leak commits into an
// enclosing repository.
func TestStartGitCeilingDirectories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	subdir := filepath.Join(dir, "work")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary: "/bin/bash",
		Args:   []string{"-c", "printf '%s' \"$GIT_CEILING_DIRECTORIES\""},
		Dir:    subdir,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("ProcessResult.Err = %v, want nil", result.Err)
	}

	got := string(result.Stdout)
	if got != dir {
		t.Errorf("GIT_CEILING_DIRECTORIES = %q, want %q (parent of %q)", got, dir, subdir)
	}
}

// TestStartRejectsEmptyBinary verifies a precondition guard so we get a
// useful error instead of an exec.Cmd panic.
func TestStartRejectsEmptyBinary(t *testing.T) {
	t.Parallel()

	if _, err := StartProcess(context.Background(), ProcessRequest{Binary: "  "}); err == nil {
		t.Fatal("StartProcess with blank Binary returned nil error")
	}
}

func awaitResult(t *testing.T, session *Session, timeout time.Duration) ProcessResult {
	t.Helper()

	select {
	case result := <-session.Done:
		return result
	case <-time.After(timeout):
		t.Fatalf("session.Done did not resolve within %v", timeout)
		return ProcessResult{}
	}
}

func readPIDFileEventually(t *testing.T, path string) PIDRecord {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			record := PIDRecord{}
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatalf("decode pidfile: %v", err)
			}

			return record
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("pidfile %q did not appear", path)

	return PIDRecord{}
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
// Used for /bin/bash -c scripts where the test body needs to contain literal
// metacharacters.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
