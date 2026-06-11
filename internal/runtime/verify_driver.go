package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/JackDrogon/Cogito/internal/adapters"
)

const (
	verifyStderrSnippetLimit = 200
	// verifyCommandTimeout caps how long a single verification command may run
	// before it is killed. Without it a hung command stalls the whole run until
	// external ctx cancellation. Mirrors the runner's SIGTERM/SIGKILL story but
	// for the synchronous verify gate.
	verifyCommandTimeout = 60 * time.Second
)

// verifyDriver runs a verify step synchronously: it resolves the command list
// (explicit or pulled from an upstream agent's AgentResult.Verification), replays
// each command with `bash -lc`, and returns a settled Execution from Start. It is
// a local gate, not a long-running provider session, so there is no poll/await.
type verifyDriver struct {
	syncTerminalDriver
	engine *Engine
}

func (d verifyDriver) Start(ctx context.Context, request stepStartRequest) (*adapters.Execution, error) {
	if request.Step.Verify == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("verify config missing for step %q", request.Step.ID))
	}

	spec := request.Step.Verify
	handle := syncStepHandle(d.engine, request.Step, request.AttemptID)

	commands := spec.Commands
	if len(commands) == 0 {
		if strings.TrimSpace(spec.From) == "" {
			return nil, newError(ErrorCodeConfig, fmt.Sprintf("verify step %q has neither commands nor from", request.Step.ID))
		}

		result, err := readAgentResult(d.engine, spec.From)
		if err != nil {
			return terminalExecution(handle, adapters.ExecutionStateFailed, err.Error()), nil
		}

		commands = result.Verification

		// A verify step that pulls commands from an upstream agent must fail
		// when that agent reported no verification commands. Otherwise an agent
		// that forgot to emit verification (or its AGENT_RESULT_JSON entirely)
		// would let the gate trivially pass with zero commands run. (v1 has no
		// allow_empty DSL escape hatch.)
		if countNonEmpty(commands) == 0 {
			return terminalExecution(handle, adapters.ExecutionStateFailed,
				fmt.Sprintf("no verification commands reported by step %q", spec.From)), nil
		}
	}

	workingDir := strings.TrimSpace(request.WorkingDir)
	if workingDir == "" {
		workingDir = "."
	}

	if failure := runVerifyCommands(ctx, workingDir, commands); failure != "" {
		return terminalExecution(handle, adapters.ExecutionStateFailed, failure), nil
	}

	return terminalExecution(handle, adapters.ExecutionStateSucceeded, fmt.Sprintf("verify passed: %d command(s)", countNonEmpty(commands))), nil
}

// runVerifyCommands replays each command with `bash -lc` in workingDir, stopping
// at the first non-zero exit. It returns a failure summary ("" means every
// command succeeded) including a stderr snippet for the offending command.
func runVerifyCommands(ctx context.Context, workingDir string, commands []string) string {
	for _, command := range commands {
		trimmed := strings.TrimSpace(command)
		if trimmed == "" {
			continue
		}

		if failure := runVerifyCommand(ctx, workingDir, trimmed); failure != "" {
			return failure
		}
	}

	return ""
}

// runVerifyCommand runs a single `bash -lc` command in its own process group
// with a per-command timeout. On timeout or parent-context cancellation it
// SIGKILLs the whole group (-pgid) so child processes spawned by the script do
// not leak, mirroring the runner's escalation. Go's default CommandContext kill
// only signals the shell, not its children, which is exactly the gap this
// closes.
func runVerifyCommand(ctx context.Context, workingDir, command string) string {
	cmdCtx, cancel := context.WithTimeout(ctx, verifyCommandTimeout)
	defer cancel()

	var stderr bytes.Buffer

	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = workingDir
	cmd.Stderr = &stderr
	// Setpgid isolates the command (and its children) in a dedicated process
	// group so we can signal the whole tree on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("verify failed: %s: %s", command, err.Error())
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case <-cmdCtx.Done():
		killProcessGroup(cmd.Process)
		<-waitErr // reap the killed process

		return fmt.Sprintf("verify failed: %s: %s", command, verifyCancelReason(cmdCtx, stderr.String()))
	case err := <-waitErr:
		if err != nil {
			return fmt.Sprintf("verify failed: %s: %s", command, stderrSnippet(stderr.String(), err))
		}

		return ""
	}
}

// killProcessGroup SIGKILLs the process group led by process, falling back to a
// direct process kill when the pgid cannot be resolved.
func killProcessGroup(process *os.Process) {
	if process == nil {
		return
	}

	if pgid, err := syscall.Getpgid(process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)

		return
	}

	_ = process.Kill()
}

// verifyCancelReason explains why a command was killed: a deadline overrun
// reports the timeout window, while a parent cancel reports the context error.
// A captured stderr snippet is prepended when present for extra diagnostics.
func verifyCancelReason(ctx context.Context, stderr string) string {
	reason := ctx.Err().Error()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		reason = fmt.Sprintf("timed out after %s", verifyCommandTimeout)
	}

	if snippet := strings.TrimSpace(stderr); snippet != "" {
		if len(snippet) > verifyStderrSnippetLimit {
			snippet = snippet[:verifyStderrSnippetLimit]
		}

		return snippet + ": " + reason
	}

	return reason
}

func stderrSnippet(stderr string, runErr error) string {
	snippet := strings.TrimSpace(stderr)
	if snippet == "" {
		snippet = runErr.Error()
	}

	if len(snippet) > verifyStderrSnippetLimit {
		snippet = snippet[:verifyStderrSnippetLimit]
	}

	return snippet
}

func countNonEmpty(values []string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}

	return count
}
