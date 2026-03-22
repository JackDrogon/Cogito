package adapters

import (
	"context"
	"encoding/json"
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

// Adapter is the service provider interface for AI tool integrations.
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
type Adapter interface {
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

type Execution struct {
	Handle           ExecutionHandle `json:"handle"`
	State            ExecutionState  `json:"state"`
	Summary          string          `json:"summary,omitempty"`
	OutputText       string          `json:"output_text,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	ArtifactRefs     []ArtifactRef   `json:"artifact_refs,omitempty"`
	Logs             []LogEntry      `json:"logs,omitempty"`
}

type StepResult struct {
	Handle           ExecutionHandle `json:"handle"`
	Status           ExecutionState  `json:"status"`
	Summary          string          `json:"summary,omitempty"`
	OutputText       string          `json:"output_text,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	ArtifactRefs     []ArtifactRef   `json:"artifact_refs,omitempty"`
	Logs             []LogEntry      `json:"logs,omitempty"`
}

func validateStartRequest(request StartRequest) error {
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

func validateHandle(handle ExecutionHandle) error {
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
