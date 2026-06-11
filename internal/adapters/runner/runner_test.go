package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStartReturnsBeforeProcessExits asserts the async contract: Start must
// return well before the child completes, even when the child sleeps.
func TestStartReturnsBeforeProcessExits(t *testing.T) {
	t.Parallel()

	const childSleep = 200 * time.Millisecond
	const startBudget = 50 * time.Millisecond

	begin := time.Now()
	session, err := Start(context.Background(), StartRequest{
		Binary: "/bin/sh",
		Args:   []string{"-c", "sleep 0.2; echo done"},
	})
	startElapsed := time.Since(begin)

	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if startElapsed > startBudget {
		t.Fatalf("Start blocked for %v (budget %v); expected immediate return", startElapsed, startBudget)
	}

	select {
	case result := <-session.Done:
		if result.Err != nil {
			t.Fatalf("Result.Err = %v, want nil", result.Err)
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

	session, err := Start(context.Background(), StartRequest{
		Binary:        "/bin/cat",
		PromptOnStdin: true,
		Prompt:        prompt,
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("Result.Err = %v, want nil", result.Err)
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

	session, err := Start(context.Background(), StartRequest{
		Binary:        "/bin/echo",
		Args:          []string{"-n", prompt},
		PromptOnStdin: false,
		Prompt:        prompt,
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("Result.Err = %v, want nil", result.Err)
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

	session, err := Start(context.Background(), StartRequest{
		Binary: "/bin/sh",
		Args:   []string{"-c", "printf '%s' " + shellQuote(stdoutBody)},
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("Result.Err = %v, want nil", result.Err)
	}

	if result.SessionID != wantID {
		t.Fatalf("Result.SessionID = %q, want %q", result.SessionID, wantID)
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

	session, err := Start(context.Background(), StartRequest{
		Binary:  "/bin/sh",
		Args:    []string{"-c", "printf '%s' " + shellQuote(body)},
		LogPath: logPath,
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("Result.Err = %v, want nil", result.Err)
	}

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}

	if !bytes.Equal(contents, []byte(body)) {
		t.Errorf("log file = %q, want %q", string(contents), body)
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

	session, err := Start(context.Background(), StartRequest{
		Binary:    "/bin/sh",
		Args:      []string{"-c", "printf '%s' " + shellQuote(body) + "; printf 'live stderr\\n' >&2"},
		ExtraSink: sink,
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("Result.Err = %v, want nil", result.Err)
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

	session, err := Start(context.Background(), StartRequest{
		Binary:    "/bin/sh",
		Args:      []string{"-c", "sleep 100"},
		ExitGrace: exitGrace,
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
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

	session, err := Start(context.Background(), StartRequest{
		Binary: "/bin/sh",
		Args:   []string{"-c", "printf '%s' \"$GIT_CEILING_DIRECTORIES\""},
		Dir:    subdir,
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)

	if result.Err != nil {
		t.Fatalf("Result.Err = %v, want nil", result.Err)
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

	if _, err := Start(context.Background(), StartRequest{Binary: "  "}); err == nil {
		t.Fatal("Start with blank Binary returned nil error")
	}
}

func awaitResult(t *testing.T, session *Session, timeout time.Duration) Result {
	t.Helper()

	select {
	case result := <-session.Done:
		return result
	case <-time.After(timeout):
		t.Fatalf("session.Done did not resolve within %v", timeout)
		return Result{}
	}
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
// Used for /bin/sh -c scripts where the test body needs to contain literal
// metacharacters.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
