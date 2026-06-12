// Package provider holds the provider-facing helpers shared by every
// provider implementations (codex, claude, opencode): the exec-based probe runner,
// prompt/working-dir resolution, session aliasing, and resume-request
// reconstruction. These were previously copy-pasted verbatim into each
// provider subpackage. Validation and clone helpers live on the parent SPI
// package; shared helper logic belongs here.
package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// VersionUnknown is the fallback version string used when a provider version
// probe returns no usable output or fails non-fatally.
const VersionUnknown = "unknown"

// CommandRunner executes a synchronous command for provider probes.
type CommandRunner interface {
	Run(ctx context.Context, command CommandSpec) (CommandResult, error)
}

// CommandSpec describes a synchronous command invocation.
type CommandSpec struct {
	Path   string
	Args   []string
	Dir    string
	Stdin  string
	Stdout io.Writer
	Stderr io.Writer
}

// CommandResult captures a synchronous command's buffered output.
type CommandResult struct {
	Stdout []byte
	Stderr []byte
}

// ExecRunner runs commands with os/exec while teeing stdout and stderr into
// optional caller-provided writers.
type ExecRunner struct{}

// Run executes command and returns buffered stdout and stderr.
func (ExecRunner) Run(ctx context.Context, command CommandSpec) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Dir = command.Dir

	var stdout bytes.Buffer

	var stderr bytes.Buffer

	stdoutWriter := command.Stdout
	stderrWriter := command.Stderr

	if stdoutWriter == nil {
		stdoutWriter = io.Discard
	}

	if stderrWriter == nil {
		stderrWriter = io.Discard
	}

	cmd.Stdout = io.MultiWriter(&stdout, stdoutWriter)
	cmd.Stderr = io.MultiWriter(&stderr, stderrWriter)

	if command.Stdin != "" {
		cmd.Stdin = strings.NewReader(command.Stdin)
	}

	err := cmd.Run()
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}

	if err != nil {
		return result, err
	}

	return result, nil
}

// NewError builds a provider error without changing provider-specific
// messages supplied by callers.
func NewError(code ErrorCode, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}

// AgentPrompt resolves the text sent to a provider, preserving the historical
// run/step fallback for empty prompts.
func AgentPrompt(request StartRequest) string {
	promptText := strings.TrimSpace(request.Prompt)
	if promptText == "" {
		promptText = fmt.Sprintf("run %s/%s", request.StepID, request.AttemptID)
	}

	return promptText
}

// CommandDir resolves a provider child process working directory.
func CommandDir(workingDir string) string {
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		return "."
	}

	return dir
}

// SanitizeID turns a run component into a filesystem/session-safe token.
func SanitizeID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return VersionUnknown
	}

	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, "/", "-")

	return value
}

// PIDFilePath returns the per-attempt pidfile path beside provider logs.
func PIDFilePath(logDir string, request StartRequest) string {
	if strings.TrimSpace(logDir) == "" {
		return ""
	}

	return filepath.Join(logDir, SanitizeID(request.AttemptID)+".pid.json")
}

// ProcessLabel formats run context for machine-local pidfile records.
func ProcessLabel(request StartRequest) string {
	return fmt.Sprintf("run=%s step=%s attempt=%s", request.RunID, request.StepID, request.AttemptID)
}

// FirstLine returns the trimmed first line of value.
func FirstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if line, _, ok := strings.Cut(value, "\n"); ok {
		return strings.TrimSpace(line)
	}

	return value
}

// ResumeStartRequest reconstructs a StartRequest for provider resume flows.
func ResumeStartRequest(request ResumeRequest, prior StartRequest) StartRequest {
	resume := StartRequest{
		RunID:      request.Handle.RunID,
		StepID:     request.Handle.StepID,
		AttemptID:  request.Handle.AttemptID,
		Prompt:     request.Prompt,
		WorkingDir: strings.TrimSpace(request.WorkingDir),
	}

	if resume.WorkingDir == "" {
		resume.WorkingDir = prior.WorkingDir
	}

	if strings.TrimSpace(resume.Prompt) == "" {
		resume.Prompt = prior.Prompt
	}

	return resume
}

// SessionMap gives provider adapters shared alias/release mechanics while each
// adapter keeps owning its private record type and lock.
type SessionMap[T any] struct {
	Mu       sync.Locker
	Sessions map[string]T
}

// Alias registers aliasID as another key for record.
func (m SessionMap[T]) Alias(record T, aliasID string) {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.Sessions[aliasID] = record
}

// Release applies release to a session record if sessionID is present.
func (m SessionMap[T]) Release(sessionID string, release func(T)) {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	if record, ok := m.Sessions[sessionID]; ok {
		release(record)
	}
}
