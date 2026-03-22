package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

type RunState string

const (
	RunStatePending         RunState = "pending"
	RunStateRunning         RunState = "running"
	RunStateWaitingApproval RunState = "waiting_approval"
	RunStatePaused          RunState = "paused"
	RunStateSucceeded       RunState = "succeeded"
	RunStateFailed          RunState = "failed"
	RunStateCanceled        RunState = "canceled"
)

type StepState string

const (
	StepStatePending         StepState = "pending"
	StepStateQueued          StepState = "queued"
	StepStateRunning         StepState = "running"
	StepStateWaitingApproval StepState = "waiting_approval"
	StepStateSucceeded       StepState = "succeeded"
	StepStateFailed          StepState = "failed"
	StepStateCanceled        StepState = "canceled"
)

var allowedRunTransitions = map[RunState]map[RunState]struct{}{
	RunStatePending: {
		RunStateRunning:  {},
		RunStateCanceled: {},
	},
	RunStateRunning: {
		RunStateWaitingApproval: {},
		RunStatePaused:          {},
		RunStateSucceeded:       {},
		RunStateFailed:          {},
		RunStateCanceled:        {},
	},
	RunStateWaitingApproval: {
		RunStateRunning:  {},
		RunStatePaused:   {},
		RunStateFailed:   {},
		RunStateCanceled: {},
	},
	RunStatePaused: {
		RunStateRunning:  {},
		RunStateCanceled: {},
	},
}

var allowedStepTransitions = map[StepState]map[StepState]struct{}{
	StepStatePending: {
		StepStateQueued:   {},
		StepStateCanceled: {},
	},
	StepStateQueued: {
		StepStateRunning:  {},
		StepStateCanceled: {},
	},
	StepStateRunning: {
		StepStateWaitingApproval: {},
		StepStateQueued:          {},
		StepStateSucceeded:       {},
		StepStateFailed:          {},
		StepStateCanceled:        {},
	},
	StepStateWaitingApproval: {
		StepStateRunning:   {},
		StepStateQueued:    {},
		StepStateSucceeded: {},
		StepStateFailed:    {},
		StepStateCanceled:  {},
	},
	StepStateFailed: {
		StepStateQueued:   {},
		StepStateCanceled: {},
	},
}

type EventStore interface {
	AppendEvent(event store.Event) (store.Event, error)
	SaveCheckpoint(checkpoint *store.Checkpoint) error
	LoadCheckpoint() (*store.Checkpoint, bool, error)
	ReadEvents() ([]store.Event, error)
}

type IDGenerator interface {
	NewAttemptID(stepID string) string
	NewSyntheticSessionID(stepID string) string
	NewApprovalID(stepID string) string
}

type AdapterLookup func(step workflow.CompiledStep) (adapters.Adapter, error)

type ApprovalMode string

const (
	ApprovalModeAuto    ApprovalMode = "auto"
	ApprovalModeApprove ApprovalMode = "approve"
	ApprovalModeDeny    ApprovalMode = "deny"
)

type ApprovalTrigger string

const (
	ApprovalTriggerExplicit ApprovalTrigger = "explicit"
	ApprovalTriggerAdapter  ApprovalTrigger = "adapter"
	ApprovalTriggerPolicy   ApprovalTrigger = "policy"
)

type ApprovalDecision string

const (
	ApprovalDecisionWait    ApprovalDecision = "wait"
	ApprovalDecisionApprove ApprovalDecision = "approve"
	ApprovalDecisionDeny    ApprovalDecision = "deny"
	ApprovalDecisionTimeout ApprovalDecision = "timeout"
)

type ApprovalDecisionResult struct {
	Decision ApprovalDecision
	Summary  string
}

type ApprovalGateRequest struct {
	Trigger           ApprovalTrigger
	Step              workflow.CompiledStep
	Snapshot          Snapshot
	AttemptID         string
	ProviderSessionID string
	Summary           string
	Status            adapters.ExecutionState
}

type ApprovalExceptionRequest struct {
	Step      workflow.CompiledStep
	Snapshot  Snapshot
	AttemptID string
	Summary   string
	Status    adapters.ExecutionState
}

type ApprovalPolicy interface {
	DecideGate(ctx context.Context, request ApprovalGateRequest) (ApprovalDecisionResult, error)
	EvaluateException(ctx context.Context, request ApprovalExceptionRequest) (ApprovalDecisionResult, bool, error)
}

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

type MachineDependencies struct {
	Clock          func() time.Time
	IDs            IDGenerator
	Store          EventStore
	LookupAdapter  AdapterLookup
	ApprovalPolicy ApprovalPolicy
	CommandRunner  CommandRunner
	RepoPath       string
	WorkingDir     string
}

type StepSnapshot struct {
	State             StepState
	AttemptID         string
	ProviderSessionID string
	ApprovalID        string
	ApprovalTrigger   ApprovalTrigger
	Summary           string
}

type Snapshot struct {
	RunID        string
	State        RunState
	LastSequence int64
	UpdatedAt    string
	Steps        map[string]StepSnapshot
}

type Transition struct {
	Sequence          int64
	EventType         store.EventType
	Scope             string
	StepID            string
	ApprovalID        string
	From              string
	To                string
	AttemptID         string
	ProviderSessionID string
	Summary           string
}

type ReplayResult struct {
	Snapshot    Snapshot
	Transitions []Transition
}

type pendingApproval struct {
	Step              workflow.CompiledStep
	AttemptID         string
	ProviderSessionID string
	ApprovalID        string
	Trigger           ApprovalTrigger
}

type Engine struct {
	runID          string
	compiled       *workflow.CompiledWorkflow
	clock          func() time.Time
	ids            IDGenerator
	store          EventStore
	lookupAdapter  AdapterLookup
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
		approvalPolicy: policy,
		commandRunner:  deps.CommandRunner,
		repoPath:       strings.TrimSpace(deps.RepoPath),
		workingDir:     strings.TrimSpace(deps.WorkingDir),
		snapshot:       newZeroSnapshot(runID),
	}

	checkpoint, _, _ := deps.Store.LoadCheckpoint()

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
			engine.repoPath, engine.workingDir = checkpointExecutionContext(checkpoint, engine.repoPath, engine.workingDir)
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
		if err := e.persistRunTransition(store.EventRunStarted, RunStatePending, RunStateRunning, "run started"); err != nil {
			return err
		}
	case RunStateRunning:
	case RunStatePaused:
		if err := e.persistRunTransition(store.EventRunStarted, RunStatePaused, RunStateRunning, "run resumed"); err != nil {
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
			if err := e.persistRunTransition(store.EventRunSucceeded, RunStateRunning, RunStateSucceeded, "run succeeded"); err != nil {
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

func (e *Engine) Cancel(message string) error {
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
			if err := e.interruptActiveExecution(context.Background()); err != nil {
				return err
			}
		}

		return e.persistRunTransition(store.EventRunCanceled, e.snapshot.State, RunStateCanceled, message)
	default:
		return newError(ErrorCodeState, fmt.Sprintf("cannot cancel run from %q", e.snapshot.State))
	}
}

func (e *Engine) interruptActiveExecution(ctx context.Context) error {
	activeStepID := ""
	for _, stepID := range e.compiled.TopologicalOrder {
		if e.snapshot.Steps[stepID].State != StepStateRunning {
			continue
		}

		if activeStepID != "" {
			return newError(ErrorCodeState, "multiple running steps are not supported")
		}

		activeStepID = stepID
	}

	if activeStepID == "" {
		return nil
	}

	stepSnapshot := e.snapshot.Steps[activeStepID]
	if strings.TrimSpace(stepSnapshot.AttemptID) == "" {
		return newError(ErrorCodeState, fmt.Sprintf("step %q missing attempt id", activeStepID))
	}
	if strings.TrimSpace(stepSnapshot.ProviderSessionID) == "" {
		return newError(ErrorCodeState, fmt.Sprintf("step %q missing provider session id", activeStepID))
	}

	step, err := e.lookupStep(activeStepID)
	if err != nil {
		return err
	}

	driver, err := e.buildDriver(step)
	if err != nil {
		return err
	}

	_, err = driver.Interrupt(ctx, adapters.ExecutionHandle{
		RunID:             e.runID,
		StepID:            activeStepID,
		AttemptID:         stepSnapshot.AttemptID,
		ProviderSessionID: stepSnapshot.ProviderSessionID,
	})
	if err != nil {
		return wrapError(ErrorCodeExecution, fmt.Sprintf("interrupt active step %q", activeStepID), err)
	}

	return nil
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

func (e *Engine) ensureInitialized() error {
	if e.snapshot.State != "" {
		return nil
	}

	return e.persistRunTransition(store.EventRunCreated, RunState(""), RunStatePending, "run created")
}

func (e *Engine) queueReadySteps() error {
	if e.snapshot.State != RunStateRunning {
		return nil
	}

	for _, stepID := range e.selectReadyPendingStepIDs() {
		if err := e.persistStepTransition(store.EventStepQueued, stepID, StepStatePending, StepStateQueued, "", "", "step ready", ""); err != nil {
			return err
		}
	}

	return nil
}

func (e *Engine) selectReadyPendingStepIDs() []string {
	ready := make([]string, 0)
	for _, stepID := range e.compiled.TopologicalOrder {
		stepSnapshot := e.snapshot.Steps[stepID]
		if stepSnapshot.State != StepStatePending {
			continue
		}

		step := e.compiled.Steps[e.compiled.StepIndex[stepID]]
		dependenciesReady := true
		for _, dependencyID := range step.Needs {
			if e.snapshot.Steps[dependencyID].State != StepStateSucceeded {
				dependenciesReady = false
				break
			}
		}

		if dependenciesReady {
			ready = append(ready, stepID)
		}
	}

	return ready
}

func (e *Engine) executeStep(ctx context.Context, stepID string) error {
	step, err := e.lookupStep(stepID)
	if err != nil {
		return err
	}

	attemptID := e.ids.NewAttemptID(stepID)
	if step.Kind == workflow.StepKindApproval {
		providerSessionID := e.ids.NewSyntheticSessionID(stepID)
		summary := defaultApprovalSummary(step, adapters.ExecutionStateWaitingApproval)
		if err := e.persistStepTransition(store.EventStepStarted, stepID, StepStateQueued, StepStateRunning, attemptID, providerSessionID, summary, ""); err != nil {
			return err
		}

		return e.requestApproval(ctx, step, attemptID, providerSessionID, summary, ApprovalTriggerExplicit, adapters.ExecutionStateWaitingApproval)
	}

	handled, err := e.requestExceptionalApproval(ctx, step, attemptID)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}

	driver, err := e.buildDriver(step)
	if err != nil {
		providerSessionID := e.ids.NewSyntheticSessionID(stepID)
		if startErr := e.persistStepTransition(store.EventStepStarted, stepID, StepStateQueued, StepStateRunning, attemptID, providerSessionID, "step started", ""); startErr != nil {
			return startErr
		}

		return e.failRunForExecutionError(stepID, attemptID, providerSessionID, err, "driver setup failed")
	}

	execution, err := driver.Start(ctx, step, attemptID, e.Snapshot())
	if err != nil {
		providerSessionID := e.ids.NewSyntheticSessionID(stepID)
		if startErr := e.persistStepTransition(store.EventStepStarted, stepID, StepStateQueued, StepStateRunning, attemptID, providerSessionID, "step started", ""); startErr != nil {
			return startErr
		}

		return e.failRunForExecutionError(stepID, attemptID, providerSessionID, err, "step start failed")
	}

	providerSessionID := strings.TrimSpace(execution.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.ids.NewSyntheticSessionID(stepID)
		execution.Handle.ProviderSessionID = providerSessionID
	}

	if err := e.persistStepTransition(store.EventStepStarted, stepID, StepStateQueued, StepStateRunning, attemptID, providerSessionID, normalizeSummary(execution.Summary, execution.State), ""); err != nil {
		return err
	}

	return e.continueExecution(ctx, step, attemptID, driver, execution)
}

func (e *Engine) continueExecution(ctx context.Context, step workflow.CompiledStep, attemptID string, driver stepDriver, execution *adapters.Execution) error {
	providerSessionID := strings.TrimSpace(execution.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.ids.NewSyntheticSessionID(step.ID)
		execution.Handle.ProviderSessionID = providerSessionID
	}

	var err error
	for !execution.State.Normalizable() {
		execution, err = driver.PollOrCollect(ctx, execution.Handle)
		if err != nil {
			return e.failRunForExecutionError(step.ID, attemptID, providerSessionID, err, "step polling failed")
		}

		if strings.TrimSpace(execution.Handle.ProviderSessionID) == "" {
			execution.Handle.ProviderSessionID = providerSessionID
		}
	}

	result, err := driver.NormalizeResult(ctx, execution)
	if err != nil {
		return e.failRunForExecutionError(step.ID, attemptID, providerSessionID, err, "result normalization failed")
	}

	if strings.TrimSpace(result.Handle.ProviderSessionID) == "" {
		result.Handle.ProviderSessionID = providerSessionID
	}

	return e.applyResult(ctx, step, attemptID, result)
}

func (e *Engine) applyResult(ctx context.Context, step workflow.CompiledStep, attemptID string, result *adapters.StepResult) error {
	if result == nil {
		return newError(ErrorCodeExecution, "step result is required")
	}

	providerSessionID := strings.TrimSpace(result.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.ids.NewSyntheticSessionID(step.ID)
	}

	summary := normalizeSummary(result.Summary, result.Status)

	switch result.Status {
	case adapters.ExecutionStateSucceeded:
		return e.persistStepTransition(store.EventStepSucceeded, step.ID, StepStateRunning, StepStateSucceeded, attemptID, providerSessionID, summary, string(result.Status))
	case adapters.ExecutionStateFailed:
		if err := e.persistStepTransition(store.EventStepFailed, step.ID, StepStateRunning, StepStateFailed, attemptID, providerSessionID, summary, string(result.Status)); err != nil {
			return err
		}

		return e.persistRunTransition(store.EventRunFailed, RunStateRunning, RunStateFailed, summary)
	case adapters.ExecutionStateWaitingApproval:
		return e.requestApproval(ctx, step, attemptID, providerSessionID, summary, ApprovalTriggerAdapter, result.Status)
	case adapters.ExecutionStateInterrupted:
		if err := e.persistStepTransition(store.EventStepRetried, step.ID, StepStateRunning, StepStateQueued, attemptID, providerSessionID, summary, string(result.Status)); err != nil {
			return err
		}

		return e.persistRunTransition(store.EventRunPaused, RunStateRunning, RunStatePaused, summary)
	default:
		return newError(ErrorCodeExecution, fmt.Sprintf("unsupported normalized step status %q", result.Status))
	}
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

func (e *Engine) requestExceptionalApproval(ctx context.Context, step workflow.CompiledStep, attemptID string) (bool, error) {
	decision, required, err := e.approvalPolicy.EvaluateException(ctx, ApprovalExceptionRequest{
		Step:      step,
		Snapshot:  e.Snapshot(),
		AttemptID: attemptID,
		Summary:   normalizeSummary("approval required by policy", adapters.ExecutionStateWaitingApproval),
		Status:    adapters.ExecutionStateWaitingApproval,
	})
	if err != nil {
		return false, err
	}
	if !required {
		return false, nil
	}

	providerSessionID := e.ids.NewSyntheticSessionID(step.ID)
	summary := strings.TrimSpace(decision.Summary)
	if summary == "" {
		summary = "approval required by policy"
	}
	if err := e.persistStepTransition(store.EventStepStarted, step.ID, StepStateQueued, StepStateRunning, attemptID, providerSessionID, summary, ""); err != nil {
		return true, err
	}

	return true, e.requestApprovalWithDecision(ctx, step, attemptID, providerSessionID, summary, ApprovalTriggerPolicy, adapters.ExecutionStateWaitingApproval, decision)
}

func (e *Engine) requestApproval(ctx context.Context, step workflow.CompiledStep, attemptID, providerSessionID, summary string, trigger ApprovalTrigger, status adapters.ExecutionState) error {
	decision, err := e.approvalPolicy.DecideGate(ctx, ApprovalGateRequest{
		Trigger:           trigger,
		Step:              step,
		Snapshot:          e.Snapshot(),
		AttemptID:         attemptID,
		ProviderSessionID: providerSessionID,
		Summary:           summary,
		Status:            status,
	})
	if err != nil {
		return err
	}

	return e.requestApprovalWithDecision(ctx, step, attemptID, providerSessionID, summary, trigger, status, decision)
}

func (e *Engine) requestApprovalWithDecision(ctx context.Context, step workflow.CompiledStep, attemptID, providerSessionID, summary string, trigger ApprovalTrigger, status adapters.ExecutionState, decision ApprovalDecisionResult) error {
	if err := e.persistApprovalRequested(step.ID, attemptID, providerSessionID, summary, trigger, status); err != nil {
		return err
	}

	if err := e.persistRunTransition(store.EventRunWaitingApproval, RunStateRunning, RunStateWaitingApproval, summary); err != nil {
		return err
	}

	resolvedDecision := normalizeApprovalDecision(decision.Decision)
	switch resolvedDecision {
	case ApprovalDecisionWait:
		return nil
	case ApprovalDecisionApprove, ApprovalDecisionDeny, ApprovalDecisionTimeout:
		return e.resolvePendingApproval(ctx, resolvedDecision, decision.Summary)
	default:
		return newError(ErrorCodeExecution, fmt.Sprintf("unsupported approval decision %q", decision.Decision))
	}
}

func (e *Engine) resolvePendingApproval(ctx context.Context, decision ApprovalDecision, message string) error {
	if err := e.ensureInitialized(); err != nil {
		return err
	}

	pending, err := e.findPendingApproval()
	if err != nil {
		return err
	}

	summary := strings.TrimSpace(message)
	if summary == "" {
		summary = approvalDecisionSummary(decision, pending.Step)
	}

	switch decision {
	case ApprovalDecisionApprove:
		if err := e.persistApprovalResolution(store.EventApprovalGranted, pending, StepStateWaitingApproval, StepStateRunning, summary); err != nil {
			return err
		}

		if err := e.persistRunTransition(store.EventRunStarted, RunStateWaitingApproval, RunStateRunning, "run resumed"); err != nil {
			return err
		}

		if err := e.continueApprovedStep(ctx, pending); err != nil {
			return err
		}

		return e.finalizeRunningState()
	case ApprovalDecisionDeny:
		if err := e.persistApprovalResolution(store.EventApprovalDenied, pending, StepStateWaitingApproval, StepStateFailed, summary); err != nil {
			return err
		}

		if err := e.persistRunTransition(store.EventRunFailed, RunStateWaitingApproval, RunStateFailed, summary); err != nil {
			return err
		}

		return newError(ErrorCodeExecution, summary)
	case ApprovalDecisionTimeout:
		if err := e.persistApprovalResolution(store.EventApprovalTimedOut, pending, StepStateWaitingApproval, StepStateFailed, summary); err != nil {
			return err
		}

		if err := e.persistRunTransition(store.EventRunFailed, RunStateWaitingApproval, RunStateFailed, summary); err != nil {
			return err
		}

		return newError(ErrorCodeExecution, summary)
	default:
		return newError(ErrorCodeExecution, fmt.Sprintf("unsupported approval decision %q", decision))
	}
}

func (e *Engine) continueApprovedStep(ctx context.Context, pending pendingApproval) error {
	driver, err := e.buildDriver(pending.Step)
	if err != nil {
		return e.failRunForExecutionError(pending.Step.ID, pending.AttemptID, pending.ProviderSessionID, err, "driver setup failed after approval")
	}

	handle := adapters.ExecutionHandle{
		RunID:             e.runID,
		StepID:            pending.Step.ID,
		AttemptID:         pending.AttemptID,
		ProviderSessionID: pending.ProviderSessionID,
	}

	var execution *adapters.Execution
	switch pending.Trigger {
	case ApprovalTriggerExplicit, ApprovalTriggerAdapter:
		execution, err = driver.Resume(ctx, pending.Step, handle, e.Snapshot())
		if err != nil {
			return e.failRunForExecutionError(pending.Step.ID, pending.AttemptID, pending.ProviderSessionID, err, "step resume failed after approval")
		}
	case ApprovalTriggerPolicy:
		execution, err = driver.Start(ctx, pending.Step, pending.AttemptID, e.Snapshot())
		if err != nil {
			return e.failRunForExecutionError(pending.Step.ID, pending.AttemptID, pending.ProviderSessionID, err, "step start failed after approval")
		}
	default:
		return newError(ErrorCodeExecution, fmt.Sprintf("unsupported approval trigger %q", pending.Trigger))
	}

	if strings.TrimSpace(execution.Handle.ProviderSessionID) == "" {
		execution.Handle.ProviderSessionID = pending.ProviderSessionID
	}

	return e.continueExecution(ctx, pending.Step, pending.AttemptID, driver, execution)
}

func (e *Engine) finalizeRunningState() error {
	if e.snapshot.State != RunStateRunning {
		return nil
	}

	if err := e.queueReadySteps(); err != nil {
		return err
	}

	if e.allStepsSucceeded() {
		return e.persistRunTransition(store.EventRunSucceeded, RunStateRunning, RunStateSucceeded, "run succeeded")
	}

	return nil
}

func (e *Engine) findPendingApproval() (pendingApproval, error) {
	if e.snapshot.State != RunStateWaitingApproval {
		return pendingApproval{}, newError(ErrorCodeState, fmt.Sprintf("run is not waiting approval: %q", e.snapshot.State))
	}

	var pending pendingApproval
	found := false
	for _, stepID := range e.compiled.TopologicalOrder {
		stepSnapshot := e.snapshot.Steps[stepID]
		if stepSnapshot.State != StepStateWaitingApproval {
			continue
		}

		if found {
			return pendingApproval{}, newError(ErrorCodeState, "multiple pending approvals are not supported")
		}

		step, err := e.lookupStep(stepID)
		if err != nil {
			return pendingApproval{}, err
		}

		if strings.TrimSpace(stepSnapshot.ApprovalID) == "" {
			return pendingApproval{}, newError(ErrorCodeState, fmt.Sprintf("step %q missing approval id", stepID))
		}

		pending = pendingApproval{
			Step:              step,
			AttemptID:         stepSnapshot.AttemptID,
			ProviderSessionID: stepSnapshot.ProviderSessionID,
			ApprovalID:        stepSnapshot.ApprovalID,
			Trigger:           stepSnapshot.ApprovalTrigger,
		}
		found = true
	}

	if !found {
		return pendingApproval{}, newError(ErrorCodeState, "no pending approval found")
	}

	return pending, nil
}

func (e *Engine) failRunForExecutionError(stepID, attemptID, providerSessionID string, executionErr error, message string) error {
	summary := normalizeSummary(executionErr.Error(), adapters.ExecutionStateFailed)
	if err := e.persistStepTransition(store.EventStepFailed, stepID, StepStateRunning, StepStateFailed, attemptID, providerSessionID, summary, string(adapters.ExecutionStateFailed)); err != nil {
		return err
	}

	if err := e.persistRunTransition(store.EventRunFailed, RunStateRunning, RunStateFailed, summary); err != nil {
		return err
	}

	return wrapError(ErrorCodeExecution, message, executionErr)
}

func (e *Engine) persistApprovalRequested(stepID, attemptID, providerSessionID, summary string, trigger ApprovalTrigger, status adapters.ExecutionState) error {
	event := store.Event{
		Type:       store.EventApprovalRequested,
		StepID:     stepID,
		AttemptID:  attemptID,
		ApprovalID: e.ids.NewApprovalID(stepID),
		Message:    summary,
		Data: map[string]string{
			dataOccurredAt:        e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:         string(StepStateRunning),
			dataToState:           string(StepStateWaitingApproval),
			dataProviderSessionID: providerSessionID,
			dataApprovalTrigger:   string(trigger),
			dataSummary:           summary,
			dataNormalizedStatus:  string(status),
		},
	}

	return e.persistEvent(event)
}

func (e *Engine) persistApprovalResolution(eventType store.EventType, pending pendingApproval, from, to StepState, summary string) error {
	event := store.Event{
		Type:       eventType,
		StepID:     pending.Step.ID,
		AttemptID:  pending.AttemptID,
		ApprovalID: pending.ApprovalID,
		Message:    summary,
		Data: map[string]string{
			dataOccurredAt:        e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:         string(from),
			dataToState:           string(to),
			dataProviderSessionID: pending.ProviderSessionID,
			dataApprovalTrigger:   string(pending.Trigger),
			dataSummary:           summary,
		},
	}

	return e.persistEvent(event)
}

func (e *Engine) persistRunTransition(eventType store.EventType, from, to RunState, message string) error {
	event := store.Event{
		Type:    eventType,
		Message: normalizeSummary(message, adapters.ExecutionStateRunning),
		Data: map[string]string{
			dataOccurredAt: e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:  string(from),
			dataToState:    string(to),
			dataSummary:    normalizeSummary(message, adapters.ExecutionStateRunning),
		},
	}

	return e.persistEvent(event)
}

func (e *Engine) persistStepTransition(eventType store.EventType, stepID string, from, to StepState, attemptID, providerSessionID, summary, normalizedStatus string) error {
	event := store.Event{
		Type:      eventType,
		StepID:    stepID,
		AttemptID: attemptID,
		Message:   normalizeSummary(summary, adapters.ExecutionStateRunning),
		Data: map[string]string{
			dataOccurredAt:        e.clock().UTC().Format(time.RFC3339Nano),
			dataFromState:         string(from),
			dataToState:           string(to),
			dataProviderSessionID: providerSessionID,
			dataSummary:           normalizeSummary(summary, adapters.ExecutionStateRunning),
		},
	}

	if normalizedStatus != "" {
		event.Data[dataNormalizedStatus] = normalizedStatus
	}

	return e.persistEvent(event)
}

func (e *Engine) persistEvent(event store.Event) error {
	event.RunID = e.runID
	previewSnapshot := cloneSnapshot(e.snapshot)
	previewEvent := cloneEvent(event)
	previewEvent.Sequence = previewSnapshot.LastSequence + 1
	if err := applyEvent(e.compiled, &previewSnapshot, nil, previewEvent, ErrorCodeState); err != nil {
		return err
	}

	appended, err := e.store.AppendEvent(event)
	if err != nil {
		return err
	}

	if err := applyEvent(e.compiled, &e.snapshot, &e.transitions, appended, ErrorCodeState); err != nil {
		return err
	}

	return e.store.SaveCheckpoint(checkpointFromSnapshot(e.snapshot, e.repoPath, e.workingDir))
}

func (e *Engine) lookupStep(stepID string) (workflow.CompiledStep, error) {
	index, ok := e.compiled.StepIndex[stepID]
	if !ok {
		return workflow.CompiledStep{}, newError(ErrorCodeConfig, fmt.Sprintf("unknown step %q", stepID))
	}

	return e.compiled.Steps[index], nil
}

func (e *Engine) buildDriver(step workflow.CompiledStep) (stepDriver, error) {
	switch step.Kind {
	case workflow.StepKindAgent:
		if e.lookupAdapter == nil {
			return nil, newError(ErrorCodeConfig, "adapter lookup is required for agent steps")
		}

		adapter, err := e.lookupAdapter(step)
		if err != nil {
			return nil, err
		}

		return agentDriver{adapter: adapter}, nil
	case workflow.StepKindCommand:
		if e.commandRunner == nil {
			return nil, newError(ErrorCodeConfig, "command runner is required for command steps")
		}

		return commandDriver{runner: e.commandRunner}, nil
	case workflow.StepKindApproval:
		return approvalDriver{
			runID: e.runID,
			ids:   e.ids,
		}, nil
	default:
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("unsupported step kind %q", step.Kind))
	}
}

func (e *Engine) allStepsSucceeded() bool {
	if len(e.compiled.Steps) == 0 {
		return true
	}

	for _, step := range e.compiled.Steps {
		if e.snapshot.Steps[step.ID].State != StepStateSucceeded {
			return false
		}
	}

	return true
}

type stepDriver interface {
	Start(ctx context.Context, step workflow.CompiledStep, attemptID string, snapshot Snapshot) (*adapters.Execution, error)
	Resume(ctx context.Context, step workflow.CompiledStep, handle adapters.ExecutionHandle, snapshot Snapshot) (*adapters.Execution, error)
	PollOrCollect(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error)
	Interrupt(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error)
	NormalizeResult(ctx context.Context, execution *adapters.Execution) (*adapters.StepResult, error)
}

type agentDriver struct {
	adapter adapters.Adapter
}

func (d agentDriver) Start(ctx context.Context, step workflow.CompiledStep, attemptID string, snapshot Snapshot) (*adapters.Execution, error) {
	if step.Agent == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("agent config missing for step %q", step.ID))
	}

	return d.adapter.Start(ctx, adapters.StartRequest{
		RunID:     snapshot.RunID,
		StepID:    step.ID,
		AttemptID: attemptID,
		Prompt:    step.Agent.Prompt,
	})
}

func (d agentDriver) PollOrCollect(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return d.adapter.PollOrCollect(ctx, handle)
}

func (d agentDriver) Interrupt(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	if err := d.adapter.DescribeCapabilities().Require(adapters.CapabilityInterrupt); err != nil {
		return nil, wrapError(ErrorCodeExecution, "interrupt agent step", err)
	}

	return d.adapter.Interrupt(ctx, handle)
}

func (d agentDriver) Resume(ctx context.Context, step workflow.CompiledStep, handle adapters.ExecutionHandle, _ Snapshot) (*adapters.Execution, error) {
	if step.Agent == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("agent config missing for step %q", step.ID))
	}

	if err := d.adapter.DescribeCapabilities().Require(adapters.CapabilityResume); err != nil {
		return nil, wrapError(ErrorCodeExecution, "resume agent step", err)
	}

	return d.adapter.Resume(ctx, adapters.ResumeRequest{Handle: handle, Prompt: step.Agent.Prompt})
}

func (d agentDriver) NormalizeResult(ctx context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	return d.adapter.NormalizeResult(ctx, adapters.NormalizeRequest{Execution: execution})
}

type commandDriver struct {
	runner CommandRunner
}

func (d commandDriver) Start(ctx context.Context, step workflow.CompiledStep, attemptID string, snapshot Snapshot) (*adapters.Execution, error) {
	if step.Command == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("command config missing for step %q", step.ID))
	}

	return d.runner.Start(ctx, CommandRequest{
		RunID:      snapshot.RunID,
		StepID:     step.ID,
		AttemptID:  attemptID,
		Command:    step.Command.Command,
		WorkingDir: ".",
	})
}

func (d commandDriver) PollOrCollect(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return d.runner.PollOrCollect(ctx, handle)
}

func (d commandDriver) Interrupt(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return d.runner.Interrupt(ctx, handle)
}

func (d commandDriver) Resume(_ context.Context, step workflow.CompiledStep, _ adapters.ExecutionHandle, _ Snapshot) (*adapters.Execution, error) {
	return nil, newError(ErrorCodeExecution, fmt.Sprintf("command step %q does not support approval resume", step.ID))
}

func (d commandDriver) NormalizeResult(ctx context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	return d.runner.NormalizeResult(ctx, execution)
}

type approvalDriver struct {
	runID string
	ids   IDGenerator
}

func (d approvalDriver) Start(_ context.Context, step workflow.CompiledStep, attemptID string, _ Snapshot) (*adapters.Execution, error) {
	return &adapters.Execution{
		Handle: adapters.ExecutionHandle{
			RunID:             d.runID,
			StepID:            step.ID,
			AttemptID:         attemptID,
			ProviderSessionID: d.ids.NewSyntheticSessionID(step.ID),
		},
		State:   adapters.ExecutionStateWaitingApproval,
		Summary: defaultApprovalSummary(step, adapters.ExecutionStateWaitingApproval),
	}, nil
}

func (d approvalDriver) Resume(_ context.Context, step workflow.CompiledStep, handle adapters.ExecutionHandle, _ Snapshot) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateSucceeded, Summary: approvalDecisionSummary(ApprovalDecisionApprove, step)}, nil
}

func (d approvalDriver) Interrupt(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateInterrupted, Summary: "approval interrupted"}, nil
}

func (d approvalDriver) PollOrCollect(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateWaitingApproval, Summary: "approval pending"}, nil
}

func (d approvalDriver) NormalizeResult(_ context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	if execution == nil {
		return nil, newError(ErrorCodeExecution, "approval execution is required")
	}

	return &adapters.StepResult{
		Handle:  execution.Handle,
		Status:  execution.State,
		Summary: execution.Summary,
	}, nil
}

type approvalModePolicy struct {
	mode ApprovalMode
}

func ParseApprovalMode(value string) (ApprovalMode, error) {
	mode := ApprovalMode(strings.TrimSpace(value))
	if mode == "" {
		return ApprovalModeAuto, nil
	}

	switch mode {
	case ApprovalModeAuto, ApprovalModeApprove, ApprovalModeDeny:
		return mode, nil
	default:
		return "", newError(ErrorCodeConfig, fmt.Sprintf("unsupported approval mode %q", value))
	}
}

func newApprovalModePolicy(mode ApprovalMode) ApprovalPolicy {
	return approvalModePolicy{mode: mode}
}

func NewApprovalModePolicy(mode ApprovalMode) ApprovalPolicy {
	return newApprovalModePolicy(mode)
}

func (p approvalModePolicy) DecideGate(_ context.Context, request ApprovalGateRequest) (ApprovalDecisionResult, error) {
	decision := ApprovalDecisionWait
	switch p.mode {
	case ApprovalModeApprove:
		decision = ApprovalDecisionApprove
	case ApprovalModeDeny:
		decision = ApprovalDecisionDeny
	case ApprovalModeAuto:
		decision = ApprovalDecisionWait
	default:
		return ApprovalDecisionResult{}, newError(ErrorCodeConfig, fmt.Sprintf("unsupported approval mode %q", p.mode))
	}

	summary := strings.TrimSpace(request.Summary)
	if summary == "" {
		summary = defaultApprovalSummary(request.Step, adapters.ExecutionStateWaitingApproval)
	}
	if decision != ApprovalDecisionWait {
		summary = approvalDecisionSummary(decision, request.Step)
	}

	return ApprovalDecisionResult{Decision: decision, Summary: summary}, nil
}

func (approvalModePolicy) EvaluateException(_ context.Context, _ ApprovalExceptionRequest) (ApprovalDecisionResult, bool, error) {
	return ApprovalDecisionResult{}, false, nil
}

type defaultIDGenerator struct {
	mu        sync.Mutex
	attempts  map[string]int
	sessions  map[string]int
	approvals map[string]int
}

func newDefaultIDGenerator() *defaultIDGenerator {
	return &defaultIDGenerator{
		attempts:  map[string]int{},
		sessions:  map[string]int{},
		approvals: map[string]int{},
	}
}

func (g *defaultIDGenerator) NewAttemptID(stepID string) string {
	return g.next("attempt", stepID, g.attempts)
}

func (g *defaultIDGenerator) NewSyntheticSessionID(stepID string) string {
	return g.next("session", stepID, g.sessions)
}

func (g *defaultIDGenerator) NewApprovalID(stepID string) string {
	return g.next("approval", stepID, g.approvals)
}

func (g *defaultIDGenerator) next(prefix, stepID string, bucket map[string]int) string {
	stepID = sanitizeIDPart(stepID)

	g.mu.Lock()
	defer g.mu.Unlock()

	bucket[stepID]++
	return fmt.Sprintf("%s-%s-%02d", prefix, stepID, bucket[stepID])
}

func applyEvent(compiled *workflow.CompiledWorkflow, snapshot *Snapshot, transitions *[]Transition, event store.Event, code ErrorCode) error {
	if compiled == nil {
		return newError(code, "compiled workflow is required")
	}

	if snapshot == nil {
		return newError(code, "snapshot is required")
	}

	if strings.TrimSpace(event.RunID) == "" {
		return newError(code, "event run id is required")
	}

	if snapshot.RunID == "" {
		snapshot.RunID = event.RunID
	}

	if event.RunID != snapshot.RunID {
		return newError(code, fmt.Sprintf("event run id %q does not match snapshot %q", event.RunID, snapshot.RunID))
	}

	if event.Sequence != snapshot.LastSequence+1 {
		return newError(code, fmt.Sprintf("invalid event sequence %d after %d", event.Sequence, snapshot.LastSequence))
	}

	data := cloneStringMap(event.Data)
	fromState := strings.TrimSpace(data[dataFromState])
	toState := strings.TrimSpace(data[dataToState])
	occurredAt := strings.TrimSpace(data[dataOccurredAt])
	if occurredAt == "" {
		return newError(code, fmt.Sprintf("event %s missing %s", event.Type, dataOccurredAt))
	}

	summary := strings.TrimSpace(data[dataSummary])
	providerSessionID := strings.TrimSpace(data[dataProviderSessionID])
	approvalTrigger := ApprovalTrigger(strings.TrimSpace(data[dataApprovalTrigger]))

	switch event.Type {
	case store.EventRunCreated, store.EventRunStarted, store.EventRunPaused, store.EventRunWaitingApproval, store.EventRunSucceeded, store.EventRunFailed, store.EventRunCanceled:
		from := RunState(fromState)
		to := RunState(toState)
		if err := ensureRunTransition(snapshot.State, from, to); err != nil {
			return wrapError(code, "invalid transition order", err)
		}

		snapshot.State = to
		if event.Type == store.EventRunCreated {
			initializePendingSteps(snapshot, compiled)
		}

		if event.Type == store.EventRunCanceled {
			cancelActiveSteps(snapshot)
		}

		recordTransition(transitions, Transition{
			Sequence:  event.Sequence,
			EventType: event.Type,
			Scope:     "run",
			From:      string(from),
			To:        string(to),
			Summary:   normalizeSummary(summary, adapters.ExecutionStateRunning),
		})
	case store.EventStepQueued, store.EventStepStarted, store.EventStepSucceeded, store.EventStepFailed, store.EventStepRetried, store.EventApprovalRequested, store.EventApprovalGranted, store.EventApprovalDenied, store.EventApprovalTimedOut:
		stepID := strings.TrimSpace(event.StepID)
		if stepID == "" {
			return newError(code, fmt.Sprintf("event %s missing step id", event.Type))
		}

		if _, ok := compiled.StepIndex[stepID]; !ok {
			return newError(code, fmt.Sprintf("event references unknown step %q", stepID))
		}

		current := snapshot.Steps[stepID]
		from := StepState(fromState)
		to := StepState(toState)
		if err := ensureStepTransition(current.State, from, to); err != nil {
			return wrapError(code, "invalid transition order", err)
		}

		current.State = to
		if event.AttemptID != "" {
			current.AttemptID = event.AttemptID
		}

		if providerSessionID != "" {
			current.ProviderSessionID = providerSessionID
		}

		if summary != "" {
			current.Summary = summary
		}

		switch event.Type {
		case store.EventApprovalRequested:
			current.ApprovalID = event.ApprovalID
			current.ApprovalTrigger = approvalTrigger
		case store.EventApprovalGranted, store.EventApprovalDenied, store.EventApprovalTimedOut:
			current.ApprovalID = ""
			current.ApprovalTrigger = ""
		}

		if to == StepStateQueued && event.Type == store.EventStepRetried {
			current.AttemptID = ""
			current.ProviderSessionID = ""
		}

		snapshot.Steps[stepID] = current

		recordTransition(transitions, Transition{
			Sequence:          event.Sequence,
			EventType:         event.Type,
			Scope:             "step",
			StepID:            stepID,
			ApprovalID:        event.ApprovalID,
			From:              string(from),
			To:                string(to),
			AttemptID:         event.AttemptID,
			ProviderSessionID: current.ProviderSessionID,
			Summary:           normalizeSummary(current.Summary, adapters.ExecutionStateRunning),
		})
	default:
		return newError(code, fmt.Sprintf("unsupported event type %q", event.Type))
	}

	snapshot.LastSequence = event.Sequence
	snapshot.UpdatedAt = occurredAt
	return nil
}

func ensureRunTransition(current, from, to RunState) error {
	if from == "" && to == RunStatePending {
		if current != "" {
			return newError(ErrorCodeState, fmt.Sprintf("run state is %q, want empty before creation", current))
		}

		return nil
	}

	if current != from {
		return newError(ErrorCodeState, fmt.Sprintf("run state is %q, want %q", current, from))
	}

	allowed, ok := allowedRunTransitions[from]
	if !ok {
		return newError(ErrorCodeState, fmt.Sprintf("run state %q is terminal", from))
	}

	if _, ok := allowed[to]; !ok {
		return newError(ErrorCodeState, fmt.Sprintf("run state cannot transition from %q to %q", from, to))
	}

	return nil
}

func ensureStepTransition(current, from, to StepState) error {
	if current != from {
		return newError(ErrorCodeState, fmt.Sprintf("step state is %q, want %q", current, from))
	}

	allowed, ok := allowedStepTransitions[from]
	if !ok {
		return newError(ErrorCodeState, fmt.Sprintf("step state %q is terminal", from))
	}

	if _, ok := allowed[to]; !ok {
		return newError(ErrorCodeState, fmt.Sprintf("step state cannot transition from %q to %q", from, to))
	}

	return nil
}

func initializePendingSteps(snapshot *Snapshot, compiled *workflow.CompiledWorkflow) {
	if snapshot.Steps == nil {
		snapshot.Steps = make(map[string]StepSnapshot, len(compiled.Steps))
	}

	for _, step := range compiled.Steps {
		snapshot.Steps[step.ID] = StepSnapshot{State: StepStatePending}
	}
}

func cancelActiveSteps(snapshot *Snapshot) {
	for stepID, step := range snapshot.Steps {
		switch step.State {
		case StepStateSucceeded, StepStateFailed, StepStateCanceled:
			continue
		default:
			step.State = StepStateCanceled
			snapshot.Steps[stepID] = step
		}
	}
}

func checkpointFromSnapshot(snapshot Snapshot, repoPath, workingDir string) *store.Checkpoint {
	repoPath, workingDir = normalizeExecutionContext(repoPath, workingDir)
	steps := make(map[string]store.StepCheckpoint, len(snapshot.Steps))
	for stepID, step := range snapshot.Steps {
		steps[stepID] = store.StepCheckpoint{
			State:             string(step.State),
			AttemptID:         step.AttemptID,
			ProviderSessionID: step.ProviderSessionID,
			ApprovalID:        step.ApprovalID,
			ApprovalTrigger:   string(step.ApprovalTrigger),
			Summary:           step.Summary,
		}
	}

	return &store.Checkpoint{
		RunID:        snapshot.RunID,
		RepoPath:     repoPath,
		WorkingDir:   workingDir,
		State:        string(snapshot.State),
		LastSequence: snapshot.LastSequence,
		UpdatedAt:    snapshot.UpdatedAt,
		Steps:        steps,
	}
}

func snapshotFromCheckpoint(runID string, compiled *workflow.CompiledWorkflow, checkpoint *store.Checkpoint) (Snapshot, error) {
	if checkpoint == nil {
		return Snapshot{}, newError(ErrorCodeState, "checkpoint is required")
	}

	if strings.TrimSpace(checkpoint.RunID) != "" && strings.TrimSpace(checkpoint.RunID) != runID {
		return Snapshot{}, newError(ErrorCodeState, fmt.Sprintf("checkpoint run id %q does not match %q", checkpoint.RunID, runID))
	}

	state := RunState(strings.TrimSpace(checkpoint.State))
	if !validRunState(state) {
		return Snapshot{}, newError(ErrorCodeState, fmt.Sprintf("unknown checkpoint run state %q", checkpoint.State))
	}

	snapshot := Snapshot{
		RunID:        runID,
		State:        state,
		LastSequence: checkpoint.LastSequence,
		UpdatedAt:    checkpoint.UpdatedAt,
		Steps:        make(map[string]StepSnapshot, len(compiled.Steps)),
	}

	for stepID := range checkpoint.Steps {
		if _, ok := compiled.StepIndex[stepID]; !ok {
			return Snapshot{}, newError(ErrorCodeState, fmt.Sprintf("checkpoint references unknown step %q", stepID))
		}
	}

	for _, step := range compiled.Steps {
		stored := checkpoint.Steps[step.ID]
		stepState := StepState(strings.TrimSpace(stored.State))
		if stepState == "" {
			stepState = StepStatePending
		}

		if !validStepState(stepState) {
			return Snapshot{}, newError(ErrorCodeState, fmt.Sprintf("unknown checkpoint step state %q for %s", stored.State, step.ID))
		}

		snapshot.Steps[step.ID] = StepSnapshot{
			State:             stepState,
			AttemptID:         stored.AttemptID,
			ProviderSessionID: stored.ProviderSessionID,
			ApprovalID:        stored.ApprovalID,
			ApprovalTrigger:   ApprovalTrigger(strings.TrimSpace(stored.ApprovalTrigger)),
			Summary:           stored.Summary,
		}
	}

	return snapshot, nil
}

func latestEventSequence(events []store.Event) int64 {
	if len(events) == 0 {
		return 0
	}

	return events[len(events)-1].Sequence
}

func checkpointExecutionContext(checkpoint *store.Checkpoint, repoPath, workingDir string) (string, string) {
	if checkpoint == nil {
		return normalizeExecutionContext(repoPath, workingDir)
	}

	return normalizeExecutionContext(firstNonEmpty(strings.TrimSpace(checkpoint.RepoPath), repoPath), firstNonEmpty(strings.TrimSpace(checkpoint.WorkingDir), workingDir))
}

func normalizeExecutionContext(repoPath, workingDir string) (string, string) {
	repoPath = strings.TrimSpace(repoPath)
	workingDir = strings.TrimSpace(workingDir)
	if repoPath == "" {
		repoPath = workingDir
	}
	if workingDir == "" {
		workingDir = repoPath
	}

	return repoPath, workingDir
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}

func validRunState(state RunState) bool {
	switch state {
	case RunStatePending, RunStateRunning, RunStateWaitingApproval, RunStatePaused, RunStateSucceeded, RunStateFailed, RunStateCanceled:
		return true
	default:
		return false
	}
}

func validStepState(state StepState) bool {
	switch state {
	case StepStatePending, StepStateQueued, StepStateRunning, StepStateWaitingApproval, StepStateSucceeded, StepStateFailed, StepStateCanceled:
		return true
	default:
		return false
	}
}

func normalizeSummary(summary string, status adapters.ExecutionState) string {
	summary = strings.TrimSpace(summary)
	if summary != "" {
		return summary
	}

	if status != "" {
		return string(status)
	}

	return "transition recorded"
}

func defaultApprovalSummary(step workflow.CompiledStep, status adapters.ExecutionState) string {
	if step.Approval != nil && strings.TrimSpace(step.Approval.Message) != "" {
		return step.Approval.Message
	}

	return normalizeSummary("approval "+string(status), status)
}

func normalizeApprovalDecision(decision ApprovalDecision) ApprovalDecision {
	if decision == "" {
		return ApprovalDecisionWait
	}

	return decision
}

func approvalDecisionSummary(decision ApprovalDecision, step workflow.CompiledStep) string {
	switch decision {
	case ApprovalDecisionApprove:
		return "approval granted"
	case ApprovalDecisionDeny:
		return "approval denied"
	case ApprovalDecisionTimeout:
		return "approval timed out"
	default:
		return defaultApprovalSummary(step, adapters.ExecutionStateWaitingApproval)
	}
}

func sanitizeIDPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "step"
	}

	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-")
	return replacer.Replace(value)
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	cloned := Snapshot{
		RunID:        snapshot.RunID,
		State:        snapshot.State,
		LastSequence: snapshot.LastSequence,
		UpdatedAt:    snapshot.UpdatedAt,
		Steps:        make(map[string]StepSnapshot, len(snapshot.Steps)),
	}

	for stepID, step := range snapshot.Steps {
		cloned.Steps[stepID] = step
	}

	return cloned
}

func cloneTransitions(transitions []Transition) []Transition {
	if transitions == nil {
		return nil
	}

	cloned := make([]Transition, len(transitions))
	copy(cloned, transitions)
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}

	return cloned
}

func cloneEvent(event store.Event) store.Event {
	return store.Event{
		Sequence:   event.Sequence,
		Type:       event.Type,
		RunID:      event.RunID,
		StepID:     event.StepID,
		AttemptID:  event.AttemptID,
		ApprovalID: event.ApprovalID,
		Message:    event.Message,
		Data:       cloneStringMap(event.Data),
	}
}

func newZeroSnapshot(runID string) Snapshot {
	return Snapshot{
		RunID: runID,
		Steps: map[string]StepSnapshot{},
	}
}

func recordTransition(transitions *[]Transition, transition Transition) {
	if transitions == nil {
		return
	}

	*transitions = append(*transitions, transition)
}
