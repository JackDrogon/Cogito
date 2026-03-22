package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	shared "github.com/JackDrogon/Cogito/internal/adapters"
)

const (
	ProviderName   = "claude"
	binaryName     = "claude"
	versionUnknown = "unknown"
)

func init() {
	shared.MustRegister(shared.Registration{
		Name:         ProviderName,
		Capabilities: Capabilities(),
		New: func() shared.Adapter {
			return New(Config{})
		},
	})
}

type Config struct {
	LookPath func(string) (string, error)
	Runner   Runner
}

type Adapter struct {
	lookPath func(string) (string, error)
	runner   Runner

	mu       sync.Mutex
	sessions map[string]*shared.Execution

	versionOnce sync.Once
	version     string
	versionErr  error
}

type Runner interface {
	Run(ctx context.Context, command CommandSpec) (CommandResult, error)
}

type CommandSpec struct {
	Path   string
	Args   []string
	Dir    string
	Stdin  string
	Stdout io.Writer
	Stderr io.Writer
}

type CommandResult struct {
	Stdout []byte
	Stderr []byte
}

func New(config Config) *Adapter {
	lookPath := config.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	r := config.Runner
	if r == nil {
		r = execRunner{}
	}

	return &Adapter{
		lookPath: lookPath,
		runner:   r,
		sessions: map[string]*shared.Execution{},
	}
}

func Capabilities() shared.CapabilityMatrix {
	return shared.CapabilityMatrix{MachineReadableLogs: true}
}

func (a *Adapter) DescribeCapabilities() shared.CapabilityMatrix {
	return Capabilities()
}

func (a *Adapter) Start(ctx context.Context, request shared.StartRequest) (*shared.Execution, error) {
	if err := validateStartRequest(request); err != nil {
		return nil, err
	}

	binaryPath, err := a.binaryPath()
	if err != nil {
		return nil, err
	}

	version := a.binaryVersion(ctx, binaryPath)

	result, err := a.runner.Run(ctx, CommandSpec{
		Path: binaryPath,
		Args: buildPrintArgs(request),
		Dir:  commandDir(request.WorkingDir),
	})
	if err != nil {
		return nil, adapterError(shared.ErrorCodeExecution, "run claude print", err)
	}

	response, parseErr := parseResponse(result.Stdout)
	if parseErr != nil {
		return nil, adapterError(shared.ErrorCodeResult, "parse claude json output", parseErr)
	}

	execution := buildExecution(request, version, response, result.Stderr)

	a.mu.Lock()
	a.sessions[execution.Handle.ProviderSessionID] = cloneExecution(execution)
	a.mu.Unlock()

	return cloneExecution(execution), nil
}

func (a *Adapter) PollOrCollect(_ context.Context, handle shared.ExecutionHandle) (*shared.Execution, error) {
	if err := validateHandle(handle); err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	execution, ok := a.sessions[handle.ProviderSessionID]
	if !ok {
		return nil, adapterError(shared.ErrorCodeExecution, "claude execution session not found", nil)
	}

	if execution.Handle.RunID != handle.RunID || execution.Handle.StepID != handle.StepID || execution.Handle.AttemptID != handle.AttemptID {
		return nil, adapterError(shared.ErrorCodeExecution, "claude execution handle does not match session", nil)
	}

	return cloneExecution(execution), nil
}

func (a *Adapter) Interrupt(_ context.Context, _ shared.ExecutionHandle) (*shared.Execution, error) {
	if err := a.DescribeCapabilities().Require(shared.CapabilityInterrupt); err != nil {
		return nil, err
	}

	return nil, adapterError(shared.ErrorCodeExecution, "claude interrupt is not implemented", nil)
}

func (a *Adapter) Resume(_ context.Context, _ shared.ResumeRequest) (*shared.Execution, error) {
	if err := a.DescribeCapabilities().Require(shared.CapabilityResume); err != nil {
		return nil, err
	}

	return nil, adapterError(shared.ErrorCodeExecution, "claude resume is not implemented", nil)
}

func (a *Adapter) NormalizeResult(_ context.Context, request shared.NormalizeRequest) (*shared.StepResult, error) {
	if request.Execution == nil {
		return nil, adapterError(shared.ErrorCodeResult, "execution is required", nil)
	}

	if !request.Execution.State.Normalizable() {
		return nil, adapterError(shared.ErrorCodeResult, "execution state cannot be normalized", nil)
	}

	if request.RequireStructuredOutput {
		if err := a.DescribeCapabilities().Require(shared.CapabilityStructuredOutput); err != nil {
			return nil, err
		}
	}

	if request.RequireArtifactRefs {
		if err := a.DescribeCapabilities().Require(shared.CapabilityArtifactRefs); err != nil {
			return nil, err
		}
	}

	if request.RequireMachineReadableLogs {
		if err := a.DescribeCapabilities().Require(shared.CapabilityMachineReadableLogs); err != nil {
			return nil, err
		}
	}

	return &shared.StepResult{
		Handle:           request.Execution.Handle,
		Status:           request.Execution.State,
		Summary:          request.Execution.Summary,
		OutputText:       request.Execution.OutputText,
		StructuredOutput: cloneJSON(request.Execution.StructuredOutput),
		ArtifactRefs:     cloneArtifactRefs(request.Execution.ArtifactRefs),
		Logs:             cloneLogs(request.Execution.Logs),
	}, nil
}

func (a *Adapter) binaryPath() (string, error) {
	path, err := a.lookPath(binaryName)
	if err == nil {
		return path, nil
	}

	if errors.Is(err, exec.ErrNotFound) {
		return "", adapterError(shared.ErrorCodeExecution, "claude binary not found", err)
	}

	return "", adapterError(shared.ErrorCodeExecution, "locate claude binary", err)
}

func (a *Adapter) binaryVersion(ctx context.Context, binaryPath string) string {
	a.versionOnce.Do(func() {
		result, err := a.runner.Run(ctx, CommandSpec{Path: binaryPath, Args: []string{"--version"}})
		if err != nil {
			a.versionErr = err
			a.version = versionUnknown

			return
		}

		version := strings.TrimSpace(string(result.Stdout))
		if version == "" {
			version = strings.TrimSpace(string(result.Stderr))
		}

		if version == "" {
			version = versionUnknown
		}

		a.version = version
	})

	if a.version == "" {
		return versionUnknown
	}

	return a.version
}

func buildPrintArgs(request shared.StartRequest) []string {
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		prompt = fmt.Sprintf("run %s/%s", request.StepID, request.AttemptID)
	}

	return []string{"--print", "--output-format", "json", prompt}
}

func commandDir(workingDir string) string {
	return strings.TrimSpace(workingDir)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, command CommandSpec) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Dir = command.Dir

	var stdout bytes.Buffer

	var stderr bytes.Buffer

	cmd.Stdout = io.MultiWriter(&stdout, command.Stdout)
	cmd.Stderr = io.MultiWriter(&stderr, command.Stderr)

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

func adapterError(code shared.ErrorCode, message string, err error) *shared.Error {
	return &shared.Error{Code: code, Message: message, Err: err}
}

func validateStartRequest(request shared.StartRequest) error {
	if strings.TrimSpace(request.RunID) == "" {
		return adapterError(shared.ErrorCodeRequest, "run id is required", nil)
	}

	if strings.TrimSpace(request.StepID) == "" {
		return adapterError(shared.ErrorCodeRequest, "step id is required", nil)
	}

	if strings.TrimSpace(request.AttemptID) == "" {
		return adapterError(shared.ErrorCodeRequest, "attempt id is required", nil)
	}

	return nil
}

func validateHandle(handle shared.ExecutionHandle) error {
	if strings.TrimSpace(handle.RunID) == "" {
		return adapterError(shared.ErrorCodeRequest, "run id is required", nil)
	}

	if strings.TrimSpace(handle.StepID) == "" {
		return adapterError(shared.ErrorCodeRequest, "step id is required", nil)
	}

	if strings.TrimSpace(handle.AttemptID) == "" {
		return adapterError(shared.ErrorCodeRequest, "attempt id is required", nil)
	}

	if strings.TrimSpace(handle.ProviderSessionID) == "" {
		return adapterError(shared.ErrorCodeRequest, "provider session id is required", nil)
	}

	return nil
}

func cloneExecution(execution *shared.Execution) *shared.Execution {
	if execution == nil {
		return nil
	}

	return &shared.Execution{
		Handle:           execution.Handle,
		State:            execution.State,
		Summary:          execution.Summary,
		OutputText:       execution.OutputText,
		StructuredOutput: cloneJSON(execution.StructuredOutput),
		ArtifactRefs:     cloneArtifactRefs(execution.ArtifactRefs),
		Logs:             cloneLogs(execution.Logs),
	}
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}

	cloned := make(json.RawMessage, len(value))
	copy(cloned, value)

	return cloned
}

func cloneArtifactRefs(artifacts []shared.ArtifactRef) []shared.ArtifactRef {
	if artifacts == nil {
		return nil
	}

	cloned := make([]shared.ArtifactRef, 0, len(artifacts))
	cloned = append(cloned, artifacts...)

	return cloned
}

func cloneLogs(logs []shared.LogEntry) []shared.LogEntry {
	if logs == nil {
		return nil
	}

	cloned := make([]shared.LogEntry, 0, len(logs))

	for _, entry := range logs {
		clonedEntry := shared.LogEntry{Level: entry.Level, Message: entry.Message}
		if entry.Fields != nil {
			clonedEntry.Fields = make(map[string]string, len(entry.Fields))
			for key, value := range entry.Fields {
				clonedEntry.Fields[key] = value
			}
		}

		cloned = append(cloned, clonedEntry)
	}

	return cloned
}
