package provider

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
)

type Capability string

const (
	CapabilityStructuredOutput    Capability = "structured_output"
	CapabilityResume              Capability = "resume"
	CapabilityInterrupt           Capability = "interrupt"
	CapabilityArtifactRefs        Capability = "artifact_refs"
	CapabilityMachineReadableLogs Capability = "machine_readable_logs"
)

// CapabilityMatrix declares which optional features an adapter implementation supports.
// Runtime uses this to validate workflow requirements before execution and to skip
// unsupported operations gracefully.
type CapabilityMatrix struct {
	StructuredOutput    bool `json:"structured_output"`
	Resume              bool `json:"resume"`
	Interrupt           bool `json:"interrupt"`
	ArtifactRefs        bool `json:"artifact_refs"`
	MachineReadableLogs bool `json:"machine_readable_logs"`
}

func (m CapabilityMatrix) Supports(capability Capability) bool {
	switch capability {
	case CapabilityStructuredOutput:
		return m.StructuredOutput
	case CapabilityResume:
		return m.Resume
	case CapabilityInterrupt:
		return m.Interrupt
	case CapabilityArtifactRefs:
		return m.ArtifactRefs
	case CapabilityMachineReadableLogs:
		return m.MachineReadableLogs
	default:
		return false
	}
}

func (m CapabilityMatrix) Require(capability Capability) error {
	if m.Supports(capability) {
		return nil
	}

	return unsupportedCapabilityError(capability)
}

// Provider is the service provider interface for AI tool integrations.
//
// Each adapter wraps a concrete provider (Codex, Claude, OpenCode) and exposes
// a staged lifecycle so runtime can persist intermediate states and resume work
// after interruptions or failures.
//
// Lifecycle stages:
//  1. Start: initiate provider session and return initial Execution
//  2. PollOrCollect: advance or observe remote/local session state
//  3. Interrupt/Resume: control flow when capabilities allow
//  4. NormalizeResult: convert finished Execution into workflow-safe StepResult
//
// Implementations must report capabilities via DescribeCapabilities so runtime
// can validate workflow requirements before execution begins.
type Provider interface {
	DescribeCapabilities() CapabilityMatrix
	Start(ctx context.Context, request StartRequest) (*Execution, error)
	PollOrCollect(ctx context.Context, handle ExecutionHandle) (*Execution, error)
	Interrupt(ctx context.Context, handle ExecutionHandle) (*Execution, error)
	Resume(ctx context.Context, request ResumeRequest) (*Execution, error)
	NormalizeResult(ctx context.Context, request NormalizeRequest) (*StepResult, error)
}

type StartRequest struct {
	RunID      string
	StepID     string
	AttemptID  string
	WorkingDir string
	Prompt     string
}

type ResumeRequest struct {
	Handle ExecutionHandle
	Prompt string
	// WorkingDir is the directory the resumed invocation must run in. A fresh
	// `cogito resume` process has an empty in-memory session map, so without
	// this the adapter would lose the original directory and build `--cd ""` /
	// `--dir ""`. Adapters prefer this value and fall back to their session map
	// only for backward compatibility within a single process.
	WorkingDir string
}

type NormalizeRequest struct {
	Execution                  *Execution
	RequireStructuredOutput    bool
	RequireArtifactRefs        bool
	RequireMachineReadableLogs bool
}

type ExecutionHandle struct {
	RunID             string
	StepID            string
	AttemptID         string
	ProviderSessionID string
}

type ExecutionState string

const (
	ExecutionStateRunning         ExecutionState = "running"
	ExecutionStateSucceeded       ExecutionState = "succeeded"
	ExecutionStateFailed          ExecutionState = "failed"
	ExecutionStateInterrupted     ExecutionState = "interrupted"
	ExecutionStateWaitingApproval ExecutionState = "waiting_approval"
)

func (s ExecutionState) Normalizable() bool {
	switch s {
	case ExecutionStateSucceeded, ExecutionStateFailed, ExecutionStateInterrupted, ExecutionStateWaitingApproval:
		return true
	case ExecutionStateRunning:
		return false
	default:
		return false
	}
}

type ArtifactRef struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Summary string `json:"summary,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

type LogEntry struct {
	Level   string            `json:"level,omitempty"`
	Message string            `json:"message,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// Usage captures provider-reported token and billing usage for one agent step.
type Usage struct {
	// InputTokens is the number of prompt/input tokens reported by the provider.
	InputTokens int64 `json:"input_tokens"`
	// OutputTokens is the number of completion/output tokens reported by the provider.
	OutputTokens int64 `json:"output_tokens"`
	// TotalTokens is the total token count reported by the provider, or input plus output when only those parts are reported.
	TotalTokens int64 `json:"total_tokens"`
	// CostUSD is the total provider-reported cost in US dollars, when available.
	CostUSD float64 `json:"cost_usd,omitempty"`
}

type Execution struct {
	Handle           ExecutionHandle `json:"handle"`
	State            ExecutionState  `json:"state"`
	Summary          string          `json:"summary,omitempty"`
	OutputText       string          `json:"output_text,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	ArtifactRefs     []ArtifactRef   `json:"artifact_refs,omitempty"`
	Logs             []LogEntry      `json:"logs,omitempty"`
	Usage            *Usage          `json:"usage,omitempty"`
}

type StepResult struct {
	Handle           ExecutionHandle `json:"handle"`
	Status           ExecutionState  `json:"status"`
	Summary          string          `json:"summary,omitempty"`
	OutputText       string          `json:"output_text,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	ArtifactRefs     []ArtifactRef   `json:"artifact_refs,omitempty"`
	Logs             []LogEntry      `json:"logs,omitempty"`
	Usage            *Usage          `json:"usage,omitempty"`
}

// NormalizeResult converts an Execution into a StepResult after checking the
// requested optional capabilities.
func NormalizeResult(request NormalizeRequest, capabilities CapabilityMatrix) (*StepResult, error) {
	if request.Execution == nil {
		return nil, newError(ErrorCodeResult, "execution is required")
	}

	if !request.Execution.State.Normalizable() {
		return nil, newError(ErrorCodeResult, "execution state cannot be normalized")
	}

	if request.RequireStructuredOutput {
		if err := capabilities.Require(CapabilityStructuredOutput); err != nil {
			return nil, err
		}
	}

	if request.RequireArtifactRefs {
		if err := capabilities.Require(CapabilityArtifactRefs); err != nil {
			return nil, err
		}
	}

	if request.RequireMachineReadableLogs {
		if err := capabilities.Require(CapabilityMachineReadableLogs); err != nil {
			return nil, err
		}
	}

	return &StepResult{
		Handle:           request.Execution.Handle,
		Status:           request.Execution.State,
		Summary:          request.Execution.Summary,
		OutputText:       request.Execution.OutputText,
		StructuredOutput: CloneJSON(request.Execution.StructuredOutput),
		ArtifactRefs:     CloneArtifactRefs(request.Execution.ArtifactRefs),
		Logs:             CloneLogs(request.Execution.Logs),
		Usage:            CloneUsage(request.Execution.Usage),
	}, nil
}

// ValidateStartRequest checks that a StartRequest carries the run/step/attempt
// identifiers every adapter needs before launching a provider invocation.
func ValidateStartRequest(request StartRequest) error {
	if strings.TrimSpace(request.RunID) == "" {
		return newError(ErrorCodeRequest, "run id is required")
	}

	if strings.TrimSpace(request.StepID) == "" {
		return newError(ErrorCodeRequest, "step id is required")
	}

	if strings.TrimSpace(request.AttemptID) == "" {
		return newError(ErrorCodeRequest, "attempt id is required")
	}

	return nil
}

// ValidateHandle checks that an ExecutionHandle is fully populated, including
// the provider session id used to look up a live or resumable session.
func ValidateHandle(handle ExecutionHandle) error {
	if strings.TrimSpace(handle.RunID) == "" {
		return newError(ErrorCodeRequest, "run id is required")
	}

	if strings.TrimSpace(handle.StepID) == "" {
		return newError(ErrorCodeRequest, "step id is required")
	}

	if strings.TrimSpace(handle.AttemptID) == "" {
		return newError(ErrorCodeRequest, "attempt id is required")
	}

	if strings.TrimSpace(handle.ProviderSessionID) == "" {
		return newError(ErrorCodeRequest, "provider session id is required")
	}

	return nil
}

// CloneExecution returns a deep copy of an Execution so callers can hand out a
// terminal result without exposing the adapter's cached copy to mutation.
func CloneExecution(execution *Execution) *Execution {
	if execution == nil {
		return nil
	}

	return &Execution{
		Handle:           execution.Handle,
		State:            execution.State,
		Summary:          execution.Summary,
		OutputText:       execution.OutputText,
		StructuredOutput: CloneJSON(execution.StructuredOutput),
		ArtifactRefs:     CloneArtifactRefs(execution.ArtifactRefs),
		Logs:             CloneLogs(execution.Logs),
		Usage:            CloneUsage(execution.Usage),
	}
}

// CloneUsage deep-copies usage, preserving nil.
func CloneUsage(usage *Usage) *Usage {
	if usage == nil {
		return nil
	}

	cloned := *usage

	return &cloned
}

// CloneJSON deep-copies a json.RawMessage, preserving nil.
func CloneJSON(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}

	cloned := make(json.RawMessage, len(value))
	copy(cloned, value)

	return cloned
}

// CloneArtifactRefs deep-copies an ArtifactRef slice, preserving nil.
func CloneArtifactRefs(artifacts []ArtifactRef) []ArtifactRef {
	if artifacts == nil {
		return nil
	}

	cloned := make([]ArtifactRef, 0, len(artifacts))
	cloned = append(cloned, artifacts...)

	return cloned
}

// CloneLogs deep-copies a LogEntry slice including each entry's Fields map.
func CloneLogs(logs []LogEntry) []LogEntry {
	if logs == nil {
		return nil
	}

	cloned := make([]LogEntry, 0, len(logs))

	for _, entry := range logs {
		clonedEntry := LogEntry{Level: entry.Level, Message: entry.Message}
		if entry.Fields != nil {
			clonedEntry.Fields = make(map[string]string, len(entry.Fields))
			maps.Copy(clonedEntry.Fields, entry.Fields)
		}

		cloned = append(cloned, clonedEntry)
	}

	return cloned
}
