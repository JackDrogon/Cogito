package store

const DefaultRunsRoot = "ref/tmp/runs"

const (
	persistedFileMode = 0o600
	persistedDirMode  = 0o700
)

type EventType string

const (
	EventRunCreated         EventType = "RunCreated"
	EventRunStarted         EventType = "RunStarted"
	EventRunPaused          EventType = "RunPaused"
	EventRunWaitingApproval EventType = "RunWaitingApproval"
	EventRunSucceeded       EventType = "RunSucceeded"
	EventRunFailed          EventType = "RunFailed"
	EventRunCanceled        EventType = "RunCanceled"
	EventStepQueued         EventType = "StepQueued"
	EventStepStarted        EventType = "StepStarted"
	EventStepSucceeded      EventType = "StepSucceeded"
	EventStepFailed         EventType = "StepFailed"
	EventStepRetried        EventType = "StepRetried"
	EventApprovalRequested  EventType = "ApprovalRequested"
	EventApprovalGranted    EventType = "ApprovalGranted"
	EventApprovalDenied     EventType = "ApprovalDenied"
	EventApprovalTimedOut   EventType = "ApprovalTimedOut"
	EventReplayStarted      EventType = "ReplayStarted"
	EventReplaySucceeded    EventType = "ReplaySucceeded"
	EventReplayFailed       EventType = "ReplayFailed"
)

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

type Event struct {
	Sequence   int64             `json:"sequence"`
	Type       EventType         `json:"type"`
	RunID      string            `json:"run_id"`
	StepID     string            `json:"step_id,omitempty"`
	AttemptID  string            `json:"attempt_id,omitempty"`
	ApprovalID string            `json:"approval_id,omitempty"`
	Message    string            `json:"message,omitempty"`
	Data       map[string]string `json:"data,omitempty"`
}

type StepCheckpoint struct {
	State             string `json:"state"`
	AttemptID         string `json:"attempt_id,omitempty"`
	ProviderSessionID string `json:"provider_session_id,omitempty"`
	ApprovalID        string `json:"approval_id,omitempty"`
	ApprovalTrigger   string `json:"approval_trigger,omitempty"`
	Summary           string `json:"summary,omitempty"`
}

type Checkpoint struct {
	RunID        string                    `json:"run_id"`
	RepoPath     string                    `json:"repo_path,omitempty"`
	WorkingDir   string                    `json:"working_dir,omitempty"`
	State        string                    `json:"state"`
	LastSequence int64                     `json:"last_sequence"`
	UpdatedAt    string                    `json:"updated_at,omitempty"`
	Steps        map[string]StepCheckpoint `json:"steps,omitempty"`
}

type ArtifactRecord struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	StepID    string `json:"step_id,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Summary   string `json:"summary,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type artifactPathError string

func (e artifactPathError) Error() string {
	return string(e)
}
