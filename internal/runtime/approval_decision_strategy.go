package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type approvalDecisionHandler interface {
	Handle(ctx context.Context, request approvalDecisionRequest) error
}

type approvalDecisionRequest struct {
	Engine  *Engine
	Pending pendingApproval
	Summary string
}

type approvalDecisionHandlerFunc func(ctx context.Context, request approvalDecisionRequest) error

func (f approvalDecisionHandlerFunc) Handle(ctx context.Context, request approvalDecisionRequest) error {
	return f(ctx, request)
}

// lookupApprovalDecisionHandler dispatches by decision via a switch rather
// than a package-level map: the granted path eventually re-enters this lookup
// (continue -> poll -> approval), and a map initializer would form an
// initialization cycle with its own handler functions.
func lookupApprovalDecisionHandler(decision ApprovalDecision) (approvalDecisionHandler, error) {
	switch decision {
	case ApprovalDecisionApprove:
		return approvalDecisionHandlerFunc(handleApprovalGranted), nil
	case ApprovalDecisionDeny:
		return approvalDecisionHandlerFunc(handleApprovalDenied), nil
	case ApprovalDecisionTimeout:
		return approvalDecisionHandlerFunc(handleApprovalTimedOut), nil
	case ApprovalDecisionWait:
		return nil, newError(ErrorCodeExecution, fmt.Sprintf("unsupported approval decision %q", decision))
	default:
		return nil, newError(ErrorCodeExecution, fmt.Sprintf("unsupported approval decision %q", decision))
	}
}

func resolveApprovalSummary(decision ApprovalDecision, step workflow.CompiledStep, message string) string {
	summary := strings.TrimSpace(message)
	if summary == "" {
		summary = approvalDecisionSummary(decision, step)
	}

	return summary
}

func handleApprovalGranted(ctx context.Context, request approvalDecisionRequest) error {
	if err := request.Engine.persistApprovalResolution(ApprovalResolutionParams{
		EventType: store.EventApprovalGranted,
		Pending:   request.Pending,
		From:      StepStateWaitingApproval,
		To:        StepStateRunning,
		Summary:   request.Summary,
	}); err != nil {
		return err
	}

	if err := request.Engine.persistRunTransition(RunTransitionParams{
		EventType: store.EventRunStarted,
		From:      RunStateWaitingApproval,
		To:        RunStateRunning,
		Message:   "run resumed",
	}); err != nil {
		return err
	}

	if err := request.Engine.continueApprovedStep(ctx, request.Pending); err != nil {
		return err
	}

	return request.Engine.finalizeRunningState()
}

func handleApprovalDenied(_ context.Context, request approvalDecisionRequest) error {
	return failApprovalTerminally(store.EventApprovalDenied, request)
}

func handleApprovalTimedOut(_ context.Context, request approvalDecisionRequest) error {
	return failApprovalTerminally(store.EventApprovalTimedOut, request)
}

// failApprovalTerminally is the shared terminal path for deny and timeout:
// the step fails, the run fails, and the decision summary surfaces as the
// command error. Only the resolution event type differs.
func failApprovalTerminally(eventType store.EventType, request approvalDecisionRequest) error {
	if err := request.Engine.persistApprovalResolution(ApprovalResolutionParams{
		EventType: eventType,
		Pending:   request.Pending,
		From:      StepStateWaitingApproval,
		To:        StepStateFailed,
		Summary:   request.Summary,
	}); err != nil {
		return err
	}

	if err := request.Engine.persistRunTransition(RunTransitionParams{
		EventType: store.EventRunFailed,
		From:      RunStateWaitingApproval,
		To:        RunStateFailed,
		Message:   request.Summary,
	}); err != nil {
		return err
	}

	return newError(ErrorCodeExecution, request.Summary)
}
