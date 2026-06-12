package store

import (
	"encoding/json"
	"fmt"
	"sync"
)

// DefaultStateRoot is the directory Cogito creates inside the target
// repository root to hold run state, so runs never pollute the rest of the
// user's worktree. DefaultRunsRoot is where new runs land beneath it.
const (
	DefaultStateRoot = ".cogito"
	DefaultRunsRoot  = DefaultStateRoot + "/runs"
)

const (
	persistedFileMode = 0o600
	persistedDirMode  = 0o700
)

// EventType identifies a durable state-transition event.
//
// Wire format: the JSON representation is always the string name (e.g.
// "StepStarted"), never the numeric value. Numeric values are purely
// in-memory and may change between versions; only the string names are
// stable on disk.
// The json.Unmarshaler contract forces a pointer receiver next to the enum's
// value receivers (String/Valid/MarshalJSON) — the stdlib idiom, not a smell.
// Upstream recvcheck v0.3.0 excludes *.UnmarshalJSON by default
// (raeperd/recvcheck#17), but golangci-lint still bundles v0.2.0 whose builtin
// excludes cover only the Marshal/Encode family. Drop the directive once it
// ships recvcheck >= v0.3.0.
type EventType uint32 //nolint:recvcheck // see comment above: stdlib (Un)Marshaler receiver split

const (
	// 0 is intentionally left as the invalid zero value.
	EventRunCreated EventType = iota + 1
	EventRunStarted
	EventRunPaused
	EventRunWaitingApproval
	EventRunSucceeded
	EventRunFailed
	EventRunCanceled
	EventStepQueued
	EventStepStarted
	// EventStepResumed re-attaches to an interrupted step's provider session;
	// counts as the SAME attempt (no Attempts increment).
	EventStepResumed
	EventStepSucceeded
	EventStepFailed
	EventStepRetried
	EventStepInterrupted
	EventApprovalRequested
	EventApprovalGranted
	EventApprovalDenied
	EventApprovalTimedOut
	EventReplayStarted
	EventReplaySucceeded
	EventReplayFailed
)

// eventTypeNames is the single source of truth mapping numeric EventType
// values to their stable on-disk string names.
var eventTypeNames = map[EventType]string{
	EventRunCreated:         "RunCreated",
	EventRunStarted:         "RunStarted",
	EventRunPaused:          "RunPaused",
	EventRunWaitingApproval: "RunWaitingApproval",
	EventRunSucceeded:       "RunSucceeded",
	EventRunFailed:          "RunFailed",
	EventRunCanceled:        "RunCanceled",
	EventStepQueued:         "StepQueued",
	EventStepStarted:        "StepStarted",
	EventStepResumed:        "StepResumed",
	EventStepSucceeded:      "StepSucceeded",
	EventStepFailed:         "StepFailed",
	EventStepRetried:        "StepRetried",
	EventStepInterrupted:    "StepInterrupted",
	EventApprovalRequested:  "ApprovalRequested",
	EventApprovalGranted:    "ApprovalGranted",
	EventApprovalDenied:     "ApprovalDenied",
	EventApprovalTimedOut:   "ApprovalTimedOut",
	EventReplayStarted:      "ReplayStarted",
	EventReplaySucceeded:    "ReplaySucceeded",
	EventReplayFailed:       "ReplayFailed",
}

// eventTypeByName is the lazily-built reverse map of eventTypeNames.
var (
	eventTypeByName     map[string]EventType
	eventTypeByNameOnce sync.Once
)

func reverseEventTypeNames() map[string]EventType {
	eventTypeByNameOnce.Do(func() {
		m := make(map[string]EventType, len(eventTypeNames))
		for k, v := range eventTypeNames {
			m[v] = k
		}

		eventTypeByName = m
	})

	return eventTypeByName
}

// String returns the stable string name of t, or "EventType(<n>)" for unknown values.
func (t EventType) String() string {
	if name, ok := eventTypeNames[t]; ok {
		return name
	}

	return fmt.Sprintf("EventType(%d)", uint32(t))
}

// Valid reports whether t is a known EventType constant.
func (t EventType) Valid() bool {
	_, ok := eventTypeNames[t]

	return ok
}

// MarshalJSON encodes the stable string name, rejecting unknown or zero
// values so a bad EventType can never corrupt the log.
func (t EventType) MarshalJSON() ([]byte, error) {
	name, ok := eventTypeNames[t]
	if !ok {
		return nil, fmt.Errorf("cannot marshal unknown EventType(%d)", uint32(t))
	}

	return json.Marshal(name)
}

// UnmarshalJSON decodes a string name, rejecting unknown names so replay
// fails loudly at the read boundary instead of inside the state machine.
func (t *EventType) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("event type must be a JSON string: %w", err)
	}

	v, ok := reverseEventTypeNames()[name]
	if !ok {
		return fmt.Errorf("unknown event type name %q", name)
	}

	*t = v

	return nil
}

// Layout holds canonical file paths for a single run under DefaultRunsRoot.
// Use LayoutForRun to construct instances rather than assembling paths manually.
type Layout struct {
	BaseDir            string
	RunID              string
	RunDir             string
	WorkflowPath       string
	EventsPath         string
	CheckpointPath     string
	CheckpointTempPath string
	ArtifactsPath      string
	LocksDir           string
}

// Event represents one durable state transition in the append-only event log.
// Sequence numbers are assigned atomically by Store and increase monotonically.
type Event struct {
	Sequence         int64             `json:"sequence"`
	Type             EventType         `json:"type"`
	RunID            string            `json:"run_id"`
	StepID           string            `json:"step_id,omitempty"`
	AttemptID        string            `json:"attempt_id,omitempty"`
	ApprovalID       string            `json:"approval_id,omitempty"`
	Message          string            `json:"message,omitempty"`
	Data             map[string]string `json:"data,omitempty"`
	StructuredOutput json.RawMessage   `json:"structured_output,omitempty"`
	Usage            *Usage            `json:"usage,omitempty"`
}

// Usage is the durable event-log representation of provider token and cost usage.
type Usage struct {
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

type StepCheckpoint struct {
	State             string          `json:"state"`
	Attempts          int             `json:"attempts,omitempty"`
	AttemptID         string          `json:"attempt_id,omitempty"`
	ProviderSessionID string          `json:"provider_session_id,omitempty"`
	ApprovalID        string          `json:"approval_id,omitempty"`
	ApprovalTrigger   string          `json:"approval_trigger,omitempty"`
	Summary           string          `json:"summary,omitempty"`
	StructuredOutput  json.RawMessage `json:"structured_output,omitempty"`
	// Resumable marks a step that was interrupted with its provider session
	// preserved. Cleared by any transition back to Running, terminal
	// Succeeded/Failed, or fresh retry.
	Resumable bool `json:"resumable,omitempty"`
}

// Checkpoint is a coarse-grained snapshot of run state used for resume after interruption.
// Written atomically with temp-file-plus-rename to avoid exposing partial state.
//
// Field contracts (the store is a dumb persistence layer; semantics live in
// internal/runtime, which validates on load):
//   - State holds a runtime RunState literal ("pending", "running", "paused",
//     "waiting_approval", "succeeded", "failed", "canceled").
//   - UpdatedAt is an RFC 3339 timestamp string.
type Checkpoint struct {
	RunID        string                    `json:"run_id"`
	RepoPath     string                    `json:"repo_path,omitempty"`
	WorkingDir   string                    `json:"working_dir,omitempty"`
	State        string                    `json:"state"`
	LastSequence int64                     `json:"last_sequence"`
	UpdatedAt    string                    `json:"updated_at,omitempty"`
	Steps        map[string]StepCheckpoint `json:"steps,omitempty"`
}

// ArtifactKind classifies an artifact record. The constants below are the
// only kinds Cogito itself writes; the field stays open for forward
// compatibility with externally produced records.
type ArtifactKind = string

const (
	// ArtifactKindLog marks captured process output (stdout/stderr logs).
	ArtifactKindLog ArtifactKind = "log"
)

// ArtifactRecord describes one file produced by a run, addressed relative to
// the run directory. CreatedAt is an RFC 3339 timestamp string.
type ArtifactRecord struct {
	Path      string       `json:"path"`
	Kind      ArtifactKind `json:"kind"`
	StepID    string       `json:"step_id,omitempty"`
	Digest    string       `json:"digest,omitempty"`
	Summary   string       `json:"summary,omitempty"`
	CreatedAt string       `json:"created_at,omitempty"`
}
