package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

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

type ApprovalRequestParams struct {
	Step              workflow.CompiledStep
	AttemptID         string
	ProviderSessionID string
	Summary           string
	Trigger           ApprovalTrigger
	Status            adapters.ExecutionState
	Decision          ApprovalDecisionResult
}

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

type pendingApproval struct {
	Step              workflow.CompiledStep
	AttemptID         string
	ProviderSessionID string
	ApprovalID        string
	Trigger           ApprovalTrigger
}

func (e *Engine) requestExceptionalApproval(
	ctx context.Context,
	step workflow.CompiledStep,
	attemptID string,
) (bool, error) {
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
		Status:            adapters.ExecutionStateWaitingApproval,
		Decision:          decision,
	})
}

func (e *Engine) requestApproval(
	ctx context.Context,
	step workflow.CompiledStep,
	attemptID, providerSessionID, summary string,
	trigger ApprovalTrigger,
	status adapters.ExecutionState,
) error {
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

	return e.requestApprovalWithDecision(ctx, ApprovalRequestParams{
		Step:              step,
		AttemptID:         attemptID,
		ProviderSessionID: providerSessionID,
		Summary:           summary,
		Trigger:           trigger,
		Status:            status,
		Decision:          decision,
	})
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

	if err := e.persistRunTransition(
		store.EventRunWaitingApproval,
		RunStateRunning,
		RunStateWaitingApproval,
		params.Summary,
	); err != nil {
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

	return handler.Handle(ctx, e, pending, resolveApprovalSummary(decision, pending.Step, message))
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

	handle := adapters.ExecutionHandle{
		RunID:             e.runID,
		StepID:            pending.Step.ID,
		AttemptID:         pending.AttemptID,
		ProviderSessionID: pending.ProviderSessionID,
	}

	strategy, err := lookupApprovalContinuationStrategy(pending.Trigger)
	if err != nil {
		return err
	}

	execution, err := strategy.Continue(ctx, e, pending, driver, handle)
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
		return e.persistRunTransition(
			store.EventRunSucceeded,
			RunStateRunning,
			RunStateSucceeded,
			"run succeeded",
		)
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
		summary = defaultApprovalSummary(request.Step, adapters.ExecutionStateWaitingApproval)
	}

	if decision != ApprovalDecisionWait {
		summary = approvalDecisionSummary(decision, request.Step)
	}

	return ApprovalDecisionResult{Decision: decision, Summary: summary}, nil
}

func (approvalModePolicy) EvaluateException(
	_ context.Context,
	_ ApprovalExceptionRequest,
) (ApprovalDecisionResult, bool, error) {
	return ApprovalDecisionResult{}, false, nil
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
