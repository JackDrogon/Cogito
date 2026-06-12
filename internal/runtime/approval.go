package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/JackDrogon/Cogito/internal/provider"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// ApprovalMode controls how the engine responds to approval gates at run time.
type ApprovalMode string

const (
	// ApprovalModeAuto leaves the gate open for manual resolution: the run
	// parks in waiting_approval until an external caller grants or denies it.
	ApprovalModeAuto ApprovalMode = "auto"
	// ApprovalModeApprove automatically grants every approval gate without
	// waiting for human input.
	ApprovalModeApprove ApprovalMode = "approve"
	// ApprovalModeDeny automatically denies every approval gate, causing the
	// run to fail immediately at the first gate it encounters.
	ApprovalModeDeny ApprovalMode = "deny"
)

// ApprovalTrigger identifies what caused a step to enter the waiting_approval
// state. The trigger determines which continuation strategy is used after the
// gate resolves.
type ApprovalTrigger string

const (
	// ApprovalTriggerExplicit means the workflow DSL declared an explicit
	// approval step; the step was already started and parked.
	ApprovalTriggerExplicit ApprovalTrigger = "explicit"
	// ApprovalTriggerAdapter means the provider itself requested approval
	// mid-execution; the step was already started and parked.
	ApprovalTriggerAdapter ApprovalTrigger = "adapter"
	// ApprovalTriggerPolicy means the approval policy intercepted the step
	// before it started; continuation resumes with a fresh Start.
	ApprovalTriggerPolicy ApprovalTrigger = "policy"
)

// ApprovalDecision is the outcome of an approval gate evaluation.
type ApprovalDecision string

// ApprovalRequestParams describes one approval request. Decision is empty
// when entering through requestApproval (the gate policy supplies it) and
// pre-filled when entering through requestApprovalWithDecision directly.
type ApprovalRequestParams struct {
	Step              workflow.CompiledStep
	AttemptID         string
	ProviderSessionID string
	Summary           string
	Trigger           ApprovalTrigger
	Status            provider.ExecutionState
	Decision          ApprovalDecisionResult
}

const (
	// ApprovalDecisionWait leaves the run parked in waiting_approval until an
	// external caller resolves it.
	ApprovalDecisionWait ApprovalDecision = "wait"
	// ApprovalDecisionApprove grants the pending gate and resumes execution.
	ApprovalDecisionApprove ApprovalDecision = "approve"
	// ApprovalDecisionDeny rejects the pending gate and fails the run.
	ApprovalDecisionDeny ApprovalDecision = "deny"
	// ApprovalDecisionTimeout treats the gate as expired and fails the run.
	ApprovalDecisionTimeout ApprovalDecision = "timeout"
)

// ApprovalDecisionResult pairs an ApprovalDecision with a human-readable
// summary that surfaces in the run's event log and CLI output.
type ApprovalDecisionResult struct {
	Decision ApprovalDecision
	Summary  string
}

// ApprovalGateRequest carries the context passed to ApprovalPolicy.DecideGate
// so the policy can inspect the current run state and step details before
// returning a decision.
type ApprovalGateRequest struct {
	Trigger           ApprovalTrigger
	Step              workflow.CompiledStep
	Snapshot          Snapshot
	AttemptID         string
	ProviderSessionID string
	Summary           string
	Status            provider.ExecutionState
}

// ApprovalExceptionRequest carries the context passed to
// ApprovalPolicy.EvaluateException so the policy can decide whether to
// intercept a step before it starts and inject a policy-triggered gate.
type ApprovalExceptionRequest struct {
	Step      workflow.CompiledStep
	Snapshot  Snapshot
	AttemptID string
	Summary   string
	Status    provider.ExecutionState
}

// ApprovalPolicy decides how the engine handles approval gates and policy
// exceptions. DecideGate is called for every explicit, adapter-raised, or
// policy-triggered gate; EvaluateException is called before a step starts to
// let the policy intercept it. Implementations must return errNoApprovalException
// (via errors.Is) from EvaluateException when no exception applies.
type ApprovalPolicy interface {
	DecideGate(ctx context.Context, request ApprovalGateRequest) (ApprovalDecisionResult, error)
	EvaluateException(ctx context.Context, request ApprovalExceptionRequest) (*ApprovalDecisionResult, error)
}

type pendingApproval struct {
	Step              workflow.CompiledStep
	AttemptID         string
	ProviderSessionID string
	ApprovalID        string
	Trigger           ApprovalTrigger
}

var errNoApprovalException = errors.New("no approval exception")

func (e *Engine) requestExceptionalApproval(
	ctx context.Context,
	step workflow.CompiledStep,
	attemptID string,
) (bool, error) {
	decision, err := e.approvalPolicy.EvaluateException(ctx, ApprovalExceptionRequest{
		Step:      step,
		Snapshot:  e.Snapshot(),
		AttemptID: attemptID,
		Summary:   normalizeSummary("approval required by policy", provider.ExecutionStateWaitingApproval),
		Status:    provider.ExecutionStateWaitingApproval,
	})
	if err != nil {
		if errors.Is(err, errNoApprovalException) {
			return false, nil
		}

		return false, err
	}

	if decision == nil {
		return false, nil
	}

	providerSessionID := e.idGen.NewSyntheticSessionID(step.ID)

	summary := strings.TrimSpace(decision.Summary)
	if summary == "" {
		summary = "approval required by policy"
	}

	if err := e.persistStepTransition(StepTransitionParams{
		EventType:         store.EventStepStarted,
		StepID:            step.ID,
		From:              StepStateQueued,
		To:                StepStateRunning,
		AttemptID:         attemptID,
		ProviderSessionID: providerSessionID,
		Summary:           summary,
		NormalizedStatus:  "",
	}); err != nil {
		return true, err
	}

	return true, e.requestApprovalWithDecision(ctx, ApprovalRequestParams{
		Step:              step,
		AttemptID:         attemptID,
		ProviderSessionID: providerSessionID,
		Summary:           summary,
		Trigger:           ApprovalTriggerPolicy,
		Status:            provider.ExecutionStateWaitingApproval,
		Decision:          *decision,
	})
}

// requestApproval consults the gate policy for a decision and then runs the
// shared approval flow. Whatever params.Decision holds on entry is ignored:
// the policy's decision overwrites it.
func (e *Engine) requestApproval(
	ctx context.Context,
	params ApprovalRequestParams,
) error {
	decision, err := e.approvalPolicy.DecideGate(ctx, ApprovalGateRequest{
		Trigger:           params.Trigger,
		Step:              params.Step,
		Snapshot:          e.Snapshot(),
		AttemptID:         params.AttemptID,
		ProviderSessionID: params.ProviderSessionID,
		Summary:           params.Summary,
		Status:            params.Status,
	})
	if err != nil {
		return err
	}

	params.Decision = decision

	return e.requestApprovalWithDecision(ctx, params)
}

func (e *Engine) requestApprovalWithDecision(
	ctx context.Context,
	params ApprovalRequestParams,
) error {
	if err := e.persistApprovalRequested(ApprovalRequestedParams{
		StepID:            params.Step.ID,
		AttemptID:         params.AttemptID,
		ProviderSessionID: params.ProviderSessionID,
		Summary:           params.Summary,
		Trigger:           params.Trigger,
		Status:            params.Status,
	}); err != nil {
		return err
	}

	if err := e.persistRunTransition(RunTransitionParams{
		EventType: store.EventRunWaitingApproval,
		From:      RunStateRunning,
		To:        RunStateWaitingApproval,
		Message:   params.Summary,
	}); err != nil {
		return err
	}

	resolvedDecision := normalizeApprovalDecision(params.Decision.Decision)
	switch resolvedDecision {
	case ApprovalDecisionWait:
		return nil
	case ApprovalDecisionApprove, ApprovalDecisionDeny, ApprovalDecisionTimeout:
		return e.resolvePendingApproval(ctx, resolvedDecision, params.Decision.Summary)
	default:
		return newError(
			ErrorCodeExecution,
			fmt.Sprintf("unsupported approval decision %q", params.Decision.Decision),
		)
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

	handler, err := lookupApprovalDecisionHandler(decision)
	if err != nil {
		return err
	}

	return handler.Handle(ctx, approvalDecisionRequest{
		Engine:  e,
		Pending: pending,
		Summary: resolveApprovalSummary(decision, pending.Step, message),
	})
}

func (e *Engine) continueApprovedStep(ctx context.Context, pending pendingApproval) error {
	driver, err := e.buildDriver(pending.Step)
	if err != nil {
		return e.failRunForExecutionError(FailRunParams{
			StepID:            pending.Step.ID,
			AttemptID:         pending.AttemptID,
			ProviderSessionID: pending.ProviderSessionID,
			ExecutionErr:      err,
			Message:           "driver setup failed after approval",
		})
	}

	handle := provider.ExecutionHandle{
		RunID:             e.runID,
		StepID:            pending.Step.ID,
		AttemptID:         pending.AttemptID,
		ProviderSessionID: pending.ProviderSessionID,
	}

	strategy, err := lookupApprovalContinuationStrategy(pending.Trigger)
	if err != nil {
		return err
	}

	execution, err := strategy.Continue(ctx, approvalContinuationRequest{
		Engine:  e,
		Pending: pending,
		Driver:  driver,
		Handle:  handle,
	})
	if err != nil {
		return e.failRunForExecutionError(FailRunParams{
			StepID:            pending.Step.ID,
			AttemptID:         pending.AttemptID,
			ProviderSessionID: pending.ProviderSessionID,
			ExecutionErr:      err,
			Message:           strategy.FailureMessage(),
		})
	}

	execution = finalizeApprovedExecution(execution, pending.ProviderSessionID)

	return e.continueExecution(ctx, executionContinuationRequest{
		Step:      pending.Step,
		AttemptID: pending.AttemptID,
		Driver:    driver,
		Execution: execution,
	})
}

func (e *Engine) finalizeRunningState() error {
	if e.snapshot.State != RunStateRunning {
		return nil
	}

	if err := e.queueReadySteps(); err != nil {
		return err
	}

	if e.allStepsSucceeded() {
		return e.persistRunTransition(RunTransitionParams{
			EventType: store.EventRunSucceeded,
			From:      RunStateRunning,
			To:        RunStateSucceeded,
			Message:   "run succeeded",
		})
	}

	return nil
}

func (e *Engine) findPendingApproval() (pendingApproval, error) {
	if e.snapshot.State != RunStateWaitingApproval {
		return pendingApproval{}, newError(
			ErrorCodeState,
			fmt.Sprintf("run is not waiting approval: %q", e.snapshot.State),
		)
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

type approvalModePolicy struct {
	mode ApprovalMode
}

// ParseApprovalMode parses a string into an ApprovalMode. An empty string
// returns ApprovalModeAuto. Unrecognized values return an ErrorCodeConfig error.
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

// NewApprovalModePolicy builds the standard mode-driven gate policy
// (approve / deny / wait-for-manual).
func NewApprovalModePolicy(mode ApprovalMode) ApprovalPolicy {
	return approvalModePolicy{mode: mode}
}

func (p approvalModePolicy) DecideGate(_ context.Context, request ApprovalGateRequest) (ApprovalDecisionResult, error) {
	var decision ApprovalDecision

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
		summary = defaultApprovalSummary(request.Step, provider.ExecutionStateWaitingApproval)
	}

	if decision != ApprovalDecisionWait {
		summary = approvalDecisionSummary(decision, request.Step)
	}

	return ApprovalDecisionResult{Decision: decision, Summary: summary}, nil
}

func (approvalModePolicy) EvaluateException(
	_ context.Context,
	_ ApprovalExceptionRequest,
) (*ApprovalDecisionResult, error) {
	return nil, errNoApprovalException
}

func defaultApprovalSummary(step workflow.CompiledStep, status provider.ExecutionState) string {
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
	case ApprovalDecisionWait:
		return defaultApprovalSummary(step, provider.ExecutionStateWaitingApproval)
	default:
		return defaultApprovalSummary(step, provider.ExecutionStateWaitingApproval)
	}
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

func sanitizeIDPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "step"
	}

	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-")

	return replacer.Replace(value)
}
