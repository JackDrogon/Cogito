package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

const (
	dataOccurredAt        = "occurred_at"
	dataFromState         = "from_state"
	dataToState           = "to_state"
	dataProviderSessionID = "provider_session_id"
	dataApprovalTrigger   = "approval_trigger"
	dataSummary           = "summary"
	dataNormalizedStatus  = "normalized_status"
)

type EventStore interface {
	AppendEvent(event store.Event) (store.Event, error)
	SaveCheckpoint(checkpoint *store.Checkpoint) error
	LoadCheckpoint() (*store.CheckpointLoadResult, error)
	ReadEvents() ([]store.Event, error)
}

type IDGenerator interface {
	NewAttemptID(stepID string) string
	NewSyntheticSessionID(stepID string) string
	NewApprovalID(stepID string) string
}

type AdapterLookup func(step workflow.CompiledStep) (adapters.Adapter, error)

type CommandRequest struct {
	RunID      string
	StepID     string
	AttemptID  string
	WorkingDir string
	Command    string
}

type CommandRunner interface {
	Start(ctx context.Context, request CommandRequest) (*adapters.Execution, error)
	PollOrCollect(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error)
	Interrupt(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error)
	NormalizeResult(ctx context.Context, execution *adapters.Execution) (*adapters.StepResult, error)
}

// MachineDependencies bundles collaborators required by Engine.
// This dependency injection boundary keeps the state machine testable without
// importing CLI-specific wiring. Clock and IDs default to production
// implementations if nil; other fields must be provided by the caller.
type MachineDependencies struct {
	Clock          func() time.Time
	IDs            IDGenerator
	Store          EventStore
	LookupAdapter  AdapterLookup
	DriverFactory  StepDriverFactory
	ApprovalPolicy ApprovalPolicy
	CommandRunner  CommandRunner
	RepoPath       string
	WorkingDir     string
}

// Engine orchestrates event-sourced execution of a compiled workflow.
// It advances the workflow by appending durable events to the EventStore and
// rebuilding an in-memory Snapshot. Each meaningful transition is persisted
// before state mutation, ensuring execution is auditable and resumable.
//
// Engine is designed for single-run orchestration and should be driven by one
// caller at a time. Concurrent access to the same Engine instance is not safe.
// For multi-run concurrency, use separate Engine instances with distinct
// EventStore backends and coordinate repository access via RepoLockManager.
//
// Lifecycle:
//  1. Create via NewEngine with a CompiledWorkflow and MachineDependencies.
//  2. Call Initialize to load checkpoint or start fresh.
//  3. Call ExecuteUntilWait repeatedly to advance execution.
//  4. Query Snapshot for current state.
//  5. Resume paused runs by calling Resume, then ExecuteUntilWait again.
type Engine struct {
	runID          string
	compiled       *workflow.CompiledWorkflow
	clock          func() time.Time
	ids            IDGenerator
	store          EventStore
	lookupAdapter  AdapterLookup
	driverFactory  StepDriverFactory
	approvalPolicy ApprovalPolicy
	commandRunner  CommandRunner
	repoPath       string
	workingDir     string

	snapshot    Snapshot
	transitions []Transition
}

func NewEngine(runID string, compiled *workflow.CompiledWorkflow, deps MachineDependencies) (*Engine, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, newError(ErrorCodePath, "run id is required")
	}

	if compiled == nil {
		return nil, newError(ErrorCodeConfig, "compiled workflow is required")
	}

	if deps.Store == nil {
		return nil, newError(ErrorCodeConfig, "store is required")
	}

	clock := deps.Clock
	if clock == nil {
		clock = time.Now
	}

	ids := deps.IDs
	if ids == nil {
		ids = newDefaultIDGenerator()
	}

	policy := deps.ApprovalPolicy
	if policy == nil {
		policy = newApprovalModePolicy(ApprovalModeAuto)
	}

	engine := &Engine{
		runID:          runID,
		compiled:       compiled,
		clock:          clock,
		ids:            ids,
		store:          deps.Store,
		lookupAdapter:  deps.LookupAdapter,
		driverFactory:  deps.DriverFactory,
		approvalPolicy: policy,
		commandRunner:  deps.CommandRunner,
		repoPath:       strings.TrimSpace(deps.RepoPath),
		workingDir:     strings.TrimSpace(deps.WorkingDir),
		snapshot:       newZeroSnapshot(runID),
	}

	if engine.driverFactory == nil {
		engine.driverFactory = NewStepDriverRegistry()
	}

	checkpointResult, _ := deps.Store.LoadCheckpoint() //nolint:errcheck // checkpoint is optional
	var checkpoint *store.Checkpoint
	if checkpointResult != nil {
		checkpoint = checkpointResult.Checkpoint
	}

	events, readErr := deps.Store.ReadEvents()
	if readErr != nil {
		return nil, wrapError(ErrorCodeReplay, "load runtime history", readErr)
	}

	if checkpoint != nil && latestEventSequence(events) <= checkpoint.LastSequence {
		snapshot, snapshotErr := snapshotFromCheckpoint(runID, compiled, checkpoint)
		if snapshotErr != nil {
			return nil, snapshotErr
		}

		engine.repoPath, engine.workingDir = checkpointExecutionContext(checkpoint, engine.repoPath, engine.workingDir)
		engine.snapshot = snapshot

		return engine, nil
	}

	if len(events) == 0 {
		if checkpoint != nil {
			engine.repoPath, engine.workingDir = checkpointExecutionContext(
				checkpoint,
				engine.repoPath,
				engine.workingDir,
			)
		}

		return engine, nil
	}

	replay, replayErr := Replay(runID, compiled, events)
	if replayErr != nil {
		return nil, replayErr
	}

	if checkpoint != nil {
		engine.repoPath, engine.workingDir = checkpointExecutionContext(checkpoint, engine.repoPath, engine.workingDir)
	}

	engine.snapshot = replay.Snapshot
	engine.transitions = replay.Transitions

	return engine, nil
}

func Replay(runID string, compiled *workflow.CompiledWorkflow, events []store.Event) (*ReplayResult, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, newError(ErrorCodePath, "run id is required")
	}

	if compiled == nil {
		return nil, newError(ErrorCodeConfig, "compiled workflow is required")
	}

	snapshot := newZeroSnapshot(runID)
	transitions := make([]Transition, 0, len(events))

	for _, event := range events {
		if err := applyEvent(compiled, &snapshot, &transitions, event, ErrorCodeReplay); err != nil {
			return nil, err
		}
	}

	return &ReplayResult{
		Snapshot:    cloneSnapshot(snapshot),
		Transitions: cloneTransitions(transitions),
	}, nil
}

func (e *Engine) Snapshot() Snapshot {
	return cloneSnapshot(e.snapshot)
}

func (e *Engine) Transitions() []Transition {
	return cloneTransitions(e.transitions)
}

func (e *Engine) Start(_ context.Context) error {
	if err := e.ensureInitialized(); err != nil {
		return err
	}

	switch e.snapshot.State {
	case RunStatePending:
		if err := e.persistRunTransition(
			store.EventRunStarted,
			RunStatePending,
			RunStateRunning,
			"run started",
		); err != nil {
			return err
		}
	case RunStateRunning:
	case RunStatePaused:
		if err := e.persistRunTransition(
			store.EventRunStarted,
			RunStatePaused,
			RunStateRunning,
			"run resumed",
		); err != nil {
			return err
		}
	case RunStateWaitingApproval:
		return newError(ErrorCodeState, "run is waiting approval")
	case RunStateSucceeded, RunStateFailed, RunStateCanceled:
		return nil
	default:
		return newError(ErrorCodeState, fmt.Sprintf("unknown run state %q", e.snapshot.State))
	}

	return e.queueReadySteps()
}

func (e *Engine) ExecuteAll(ctx context.Context) error {
	for {
		progressed, err := e.ExecuteNext(ctx)
		if err != nil {
			return err
		}

		if !progressed {
			return nil
		}
	}
}

func (e *Engine) ExecuteNext(ctx context.Context) (bool, error) {
	if err := e.ensureInitialized(); err != nil {
		return false, err
	}

	if e.snapshot.State == RunStatePending {
		if err := e.Start(ctx); err != nil {
			return false, err
		}
	}

	switch e.snapshot.State {
	case RunStatePaused, RunStateWaitingApproval, RunStateSucceeded, RunStateFailed, RunStateCanceled:
		return false, nil
	case RunStateRunning:
	default:
		return false, newError(ErrorCodeState, fmt.Sprintf("unknown run state %q", e.snapshot.State))
	}

	if err := e.queueReadySteps(); err != nil {
		return false, err
	}

	ready := e.ReadyStepIDs()
	if len(ready) == 0 {
		if e.allStepsSucceeded() {
			if err := e.persistRunTransition(
				store.EventRunSucceeded,
				RunStateRunning,
				RunStateSucceeded,
				"run succeeded",
			); err != nil {
				return false, err
			}

			return true, nil
		}

		return false, nil
	}

	if err := e.executeStep(ctx, ready[0]); err != nil {
		return false, err
	}

	if e.snapshot.State == RunStateRunning {
		if err := e.finalizeRunningState(); err != nil {
			return false, err
		}
	}

	return true, nil
}

func (e *Engine) Pause(message string) error {
	if err := e.ensureInitialized(); err != nil {
		return err
	}

	message = strings.TrimSpace(message)
	if message == "" {
		message = "run paused"
	}

	switch e.snapshot.State {
	case RunStateRunning, RunStateWaitingApproval:
		return e.persistRunTransition(store.EventRunPaused, e.snapshot.State, RunStatePaused, message)
	default:
		return newError(ErrorCodeState, fmt.Sprintf("cannot pause run from %q", e.snapshot.State))
	}
}

func (e *Engine) Resume(message string) error {
	if err := e.ensureInitialized(); err != nil {
		return err
	}

	message = strings.TrimSpace(message)
	if message == "" {
		message = "run resumed"
	}

	if e.snapshot.State != RunStatePaused {
		return newError(ErrorCodeState, fmt.Sprintf("cannot resume run from %q", e.snapshot.State))
	}

	if err := e.persistRunTransition(store.EventRunStarted, RunStatePaused, RunStateRunning, message); err != nil {
		return err
	}

	return e.queueReadySteps()
}

func (e *Engine) Cancel(ctx context.Context, message string) error {
	if err := e.ensureInitialized(); err != nil {
		return err
	}

	message = strings.TrimSpace(message)
	if message == "" {
		message = "run canceled"
	}

	switch e.snapshot.State {
	case RunStatePending, RunStateRunning, RunStateWaitingApproval, RunStatePaused:
		if e.snapshot.State == RunStateRunning {
			if err := e.interruptActiveExecution(ctx); err != nil {
				return err
			}
		}

		return e.persistRunTransition(store.EventRunCanceled, e.snapshot.State, RunStateCanceled, message)
	default:
		return newError(ErrorCodeState, fmt.Sprintf("cannot cancel run from %q", e.snapshot.State))
	}
}

func (e *Engine) ReadyStepIDs() []string {
	ready := make([]string, 0)

	for _, stepID := range e.compiled.TopologicalOrder {
		if e.snapshot.Steps[stepID].State == StepStateQueued {
			ready = append(ready, stepID)
		}
	}

	return ready
}

func (e *Engine) GrantApproval(ctx context.Context, message string) error {
	return e.resolvePendingApproval(ctx, ApprovalDecisionApprove, message)
}

func (e *Engine) DenyApproval(ctx context.Context, message string) error {
	return e.resolvePendingApproval(ctx, ApprovalDecisionDeny, message)
}

func (e *Engine) TimeoutApproval(ctx context.Context, message string) error {
	return e.resolvePendingApproval(ctx, ApprovalDecisionTimeout, message)
}
