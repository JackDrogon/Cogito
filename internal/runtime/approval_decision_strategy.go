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

func lookupApprovalDecisionHandler(decision ApprovalDecision) (approvalDecisionHandler, error) {
	handlers := map[ApprovalDecision]approvalDecisionHandler{
		ApprovalDecisionApprove: approvalDecisionHandlerFunc(handleApprovalGranted),
		ApprovalDecisionDeny:    approvalDecisionHandlerFunc(handleApprovalDenied),
		ApprovalDecisionTimeout: approvalDecisionHandlerFunc(handleApprovalTimedOut),
	}

	handler, ok := handlers[decision]
	if !ok {
		return nil, newError(ErrorCodeExecution, fmt.Sprintf("unsupported approval decision %q", decision))
	}

	if handler == nil {
		return nil, newError(ErrorCodeExecution, fmt.Sprintf("approval decision handler is required for %q", decision))
	}

	return handler, nil
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
	if err := request.Engine.persistApprovalResolution(ApprovalResolutionParams{
		EventType: store.EventApprovalDenied,
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

func handleApprovalTimedOut(_ context.Context, request approvalDecisionRequest) error {
	if err := request.Engine.persistApprovalResolution(ApprovalResolutionParams{
		EventType: store.EventApprovalTimedOut,
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
