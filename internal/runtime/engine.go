package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/JackDrogon/Cogito/internal/provider"
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
	dataResumable         = "resumable"
)

// EventStore is the persistence boundary for run events and checkpoints.
// AppendEvent durably records a transition; SaveCheckpoint writes an
// optimization snapshot so resumes avoid replaying the full event log.
type EventStore interface {
	AppendEvent(event store.Event) (store.Event, error)
	SaveCheckpoint(checkpoint *store.Checkpoint) error
	LoadCheckpoint() (*store.CheckpointLoadResult, error)
	ReadEvents() ([]store.Event, error)
}

// IDGenerator produces stable, human-readable identifiers for attempts,
// synthetic provider sessions, and approval gates. The default implementation
// uses per-step counters; custom implementations are not required to be
// thread-safe (see MachineDependencies).
type IDGenerator interface {
	NewAttemptID(stepID string) string
	NewSyntheticSessionID(stepID string) string
	NewApprovalID(stepID string) string
}

// ProviderLookup resolves the provider adapter for an agent step. It is called
// once per step execution and must return an error for unknown or misconfigured
// providers.
type ProviderLookup func(step workflow.CompiledStep) (provider.Provider, error)

// CommandRequest carries the parameters needed to start a local command step.
type CommandRequest struct {
	RunID      string
	StepID     string
	AttemptID  string
	WorkingDir string
	Command    string
}

// CommandRunner supervises local command steps. Start launches the command;
// PollOrCollect waits for or collects its result; Interrupt signals it to stop;
// NormalizeResult converts the raw execution into a structured StepResult.
type CommandRunner interface {
	Start(ctx context.Context, request CommandRequest) (*provider.Execution, error)
	PollOrCollect(ctx context.Context, handle provider.ExecutionHandle) (*provider.Execution, error)
	Interrupt(ctx context.Context, handle provider.ExecutionHandle) (*provider.Execution, error)
	NormalizeResult(ctx context.Context, execution *provider.Execution) (*provider.StepResult, error)
}

// MachineDependencies bundles collaborators required by Engine.
// This dependency injection boundary keeps the state machine testable without
// importing CLI-specific wiring. Clock and IDGen default to production
// implementations if nil; other fields must be provided by the caller.
//
// Concurrency model: the Engine itself is single-caller (see Engine doc), so
// dependencies are only ever invoked from that one caller's goroutine. The
// default IDGenerator happens to be internally synchronized, but that is an
// implementation detail — custom IDGenerator implementations are NOT required
// to be thread-safe, and none of these dependencies may be shared across
// engines unless their own documentation says so.
type MachineDependencies struct {
	Clock          func() time.Time
	IDGen          IDGenerator
	Store          EventStore
	LookupProvider ProviderLookup
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
//  1. Create via NewEngine with a CompiledWorkflow and MachineDependencies;
//     construction restores state from the checkpoint or the event history.
//  2. Call ExecuteAll (or ExecuteNext repeatedly) to advance execution.
//  3. Query Snapshot for current state.
//  4. Resume paused runs by calling Resume, then ExecuteAll again.
type Engine struct {
	runID          string
	compiled       *workflow.CompiledWorkflow
	clock          func() time.Time
	idGen          IDGenerator
	store          EventStore
	lookupProvider ProviderLookup
	driverFactory  StepDriverFactory
	approvalPolicy ApprovalPolicy
	commandRunner  CommandRunner
	repoPath       string
	workingDir     string

	snapshot    Snapshot
	transitions []Transition
}

// NewEngine constructs an Engine for the given run, restoring state from the
// EventStore (checkpoint first, full replay as fallback). Returns an error if
// runID is empty, compiled is nil, or deps.Store is nil.
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

	ids := deps.IDGen
	if ids == nil {
		ids = newDefaultIDGenerator()
	}

	policy := deps.ApprovalPolicy
	if policy == nil {
		policy = NewApprovalModePolicy(ApprovalModeAuto)
	}

	engine := &Engine{
		runID:          runID,
		compiled:       compiled,
		clock:          clock,
		idGen:          ids,
		store:          deps.Store,
		lookupProvider: deps.LookupProvider,
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

	if err := engine.restoreState(); err != nil {
		return nil, err
	}

	return engine, nil
}

// restoreState rebuilds the engine's snapshot from persisted run history.
// The checkpoint is an optimization, never the source of truth: when the
// event log advanced past it, the full replay wins; the checkpoint's
// execution context (repo/working dir) is still adopted either way so a
// resume targets the original repository.
func (e *Engine) restoreState() error {
	// A missing or unreadable checkpoint is not fatal — the engine falls back
	// to a full event replay — but an unreadable one (disk corruption, partial
	// write) must not degrade silently or the operator can never learn the
	// checkpoint pipeline is broken.
	checkpointResult, checkpointErr := e.store.LoadCheckpoint()
	if checkpointErr != nil && !errors.Is(checkpointErr, store.ErrCheckpointNotFound) {
		slog.Warn("engine: checkpoint unavailable; replaying full event history",
			"run", e.runID, "err", checkpointErr)
	}

	var checkpoint *store.Checkpoint

	if checkpointResult != nil {
		checkpoint = checkpointResult.Checkpoint
	}

	events, readErr := e.store.ReadEvents()
	if readErr != nil {
		return wrapError(ErrorCodeReplay, "load runtime history", readErr)
	}

	if checkpoint != nil {
		e.applyExecutionContext(checkpointExecutionContext(checkpoint, e.executionContext()))
	}

	if checkpoint != nil && latestEventSequence(events) <= checkpoint.LastSequence {
		return e.restoreFromCheckpoint(checkpoint)
	}

	return e.restoreFromEvents(events)
}

// restoreFromCheckpoint installs the snapshot decoded from a checkpoint that
// is at least as new as the event log.
func (e *Engine) restoreFromCheckpoint(checkpoint *store.Checkpoint) error {
	snapshot, err := snapshotFromCheckpoint(e.runID, e.compiled, checkpoint)
	if err != nil {
		return err
	}

	e.snapshot = snapshot

	return nil
}

// restoreFromEvents folds the full event history into a fresh snapshot. An
// empty history leaves the zero snapshot from construction in place.
func (e *Engine) restoreFromEvents(events []store.Event) error {
	if len(events) == 0 {
		return nil
	}

	replay, err := Replay(e.runID, e.compiled, events)
	if err != nil {
		return err
	}

	e.snapshot = replay.Snapshot
	e.transitions = replay.Transitions

	return nil
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

	for i := range events {
		if err := applyEvent(applyEventParams{
			Compiled:    compiled,
			Snapshot:    &snapshot,
			Transitions: &transitions,
			Event:       events[i],
			Code:        ErrorCodeReplay,
		}); err != nil {
			return nil, err
		}
	}

	return &ReplayResult{
		Snapshot:    cloneSnapshot(snapshot),
		Transitions: cloneTransitions(transitions),
	}, nil
}

// Snapshot returns a deep copy of the engine's current in-memory run state.
// Callers may inspect the returned value freely without affecting engine state.
func (e *Engine) Snapshot() Snapshot {
	return cloneSnapshot(e.snapshot)
}

// Transitions returns a deep copy of the ordered list of state transitions
// accumulated since the engine was constructed or last restored from events.
func (e *Engine) Transitions() []Transition {
	return cloneTransitions(e.transitions)
}

// Start transitions the run from pending (or paused) to running and queues
// the first batch of ready steps. It is a no-op when the run has already
// reached a terminal state.
func (e *Engine) Start(_ context.Context) error {
	if err := e.ensureInitialized(); err != nil {
		return err
	}

	switch e.snapshot.State {
	case RunStatePending:
		if err := e.persistRunTransition(RunTransitionParams{
			EventType: store.EventRunStarted,
			From:      RunStatePending,
			To:        RunStateRunning,
			Message:   "run started",
		}); err != nil {
			return err
		}
	case RunStateRunning:
	case RunStatePaused:
		if err := e.persistRunTransition(RunTransitionParams{
			EventType: store.EventRunStarted,
			From:      RunStatePaused,
			To:        RunStateRunning,
			Message:   "run resumed",
		}); err != nil {
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

// ExecuteAll drives the run to completion by calling ExecuteNext in a loop
// until no further progress is possible (terminal state or waiting for
// approval/pause).
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

// ExecuteNext starts the run if pending, then executes one queued step.
// Returns (true, nil) when a step was executed, (false, nil) when the run
// has reached a terminal or waiting state with no further work to do.
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
			if err := e.persistRunTransition(RunTransitionParams{
				EventType: store.EventRunSucceeded,
				From:      RunStateRunning,
				To:        RunStateSucceeded,
				Message:   "run succeeded",
			}); err != nil {
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

// Pause transitions the run from running or waiting_approval to paused.
// An empty message defaults to "run paused".
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
		return e.persistRunTransition(RunTransitionParams{
			EventType: store.EventRunPaused,
			From:      e.snapshot.State,
			To:        RunStatePaused,
			Message:   message,
		})
	default:
		return newError(ErrorCodeState, fmt.Sprintf("cannot pause run from %q", e.snapshot.State))
	}
}

// Resume transitions the run from paused to running and re-queues any steps
// that were ready before the pause. An empty message defaults to "run resumed".
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

	if err := e.persistRunTransition(RunTransitionParams{
		EventType: store.EventRunStarted,
		From:      RunStatePaused,
		To:        RunStateRunning,
		Message:   message,
	}); err != nil {
		return err
	}

	return e.queueReadySteps()
}

// Cancel transitions the run to canceled from any non-terminal state. When
// the run is actively executing a step, it interrupts that step first.
// An empty message defaults to "run canceled".
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

		return e.persistRunTransition(RunTransitionParams{
			EventType: store.EventRunCanceled,
			From:      e.snapshot.State,
			To:        RunStateCanceled,
			Message:   message,
		})
	default:
		return newError(ErrorCodeState, fmt.Sprintf("cannot cancel run from %q", e.snapshot.State))
	}
}

// ReadyStepIDs returns the IDs of steps currently in the queued state,
// in topological order. The engine executes only the first one per
// ExecuteNext call to preserve deterministic single-step scheduling.
func (e *Engine) ReadyStepIDs() []string {
	ready := make([]string, 0)

	for _, stepID := range e.compiled.TopologicalOrder {
		if e.snapshot.Steps[stepID].State == StepStateQueued {
			ready = append(ready, stepID)
		}
	}

	return ready
}

// StepStructuredOutput returns the normalized AgentResult JSON persisted for a
// succeeded step. It returns an ErrorCodeState error when the step is unknown
// or has not succeeded, so downstream verify/commit_check drivers report a
// missing structured output rather than silently consuming nil. The step kind
// is intentionally not validated (plan OQ #7): command/approval steps simply
// carry a nil StructuredOutput.
func (e *Engine) StepStructuredOutput(stepID string) (json.RawMessage, error) {
	step, ok := e.snapshot.Steps[stepID]
	if !ok {
		return nil, newError(ErrorCodeState, fmt.Sprintf("unknown step %q", stepID))
	}

	if step.State != StepStateSucceeded {
		return nil, newError(ErrorCodeState, fmt.Sprintf("step %q has not succeeded", stepID))
	}

	return cloneRawMessage(step.StructuredOutput), nil
}

// GrantApproval resolves the pending approval gate with an approve decision,
// then continues execution from where the step was paused.
func (e *Engine) GrantApproval(ctx context.Context, message string) error {
	return e.resolvePendingApproval(ctx, ApprovalDecisionApprove, message)
}

// DenyApproval resolves the pending approval gate with a deny decision,
// failing the step and the run.
func (e *Engine) DenyApproval(ctx context.Context, message string) error {
	return e.resolvePendingApproval(ctx, ApprovalDecisionDeny, message)
}

// TimeoutApproval resolves the pending approval gate with a timeout decision,
// failing the step and the run.
func (e *Engine) TimeoutApproval(ctx context.Context, message string) error {
	return e.resolvePendingApproval(ctx, ApprovalDecisionTimeout, message)
}
