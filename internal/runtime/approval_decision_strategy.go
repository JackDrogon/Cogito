package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type approvalDecisionHandler interface {
	Handle(ctx context.Context, engine *Engine, pending pendingApproval, summary string) error
}

type approvalDecisionHandlerFunc func(ctx context.Context, engine *Engine, pending pendingApproval, summary string) error

func (f approvalDecisionHandlerFunc) Handle(ctx context.Context, engine *Engine, pending pendingApproval, summary string) error {
	return f(ctx, engine, pending, summary)
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

func handleApprovalGranted(ctx context.Context, engine *Engine, pending pendingApproval, summary string) error {
	if err := engine.persistApprovalResolution(ApprovalResolutionParams{
		EventType: store.EventApprovalGranted,
		Pending:   pending,
		From:      StepStateWaitingApproval,
		To:        StepStateRunning,
		Summary:   summary,
	}); err != nil {
		return err
	}

	if err := engine.persistRunTransition(
		store.EventRunStarted,
		RunStateWaitingApproval,
		RunStateRunning,
		"run resumed",
	); err != nil {
		return err
	}

	if err := engine.continueApprovedStep(ctx, pending); err != nil {
		return err
	}

	return engine.finalizeRunningState()
}

func handleApprovalDenied(_ context.Context, engine *Engine, pending pendingApproval, summary string) error {
	if err := engine.persistApprovalResolution(ApprovalResolutionParams{
		EventType: store.EventApprovalDenied,
		Pending:   pending,
		From:      StepStateWaitingApproval,
		To:        StepStateFailed,
		Summary:   summary,
	}); err != nil {
		return err
	}

	if err := engine.persistRunTransition(
		store.EventRunFailed,
		RunStateWaitingApproval,
		RunStateFailed,
		summary,
	); err != nil {
		return err
	}

	return newError(ErrorCodeExecution, summary)
}

func handleApprovalTimedOut(_ context.Context, engine *Engine, pending pendingApproval, summary string) error {
	if err := engine.persistApprovalResolution(ApprovalResolutionParams{
		EventType: store.EventApprovalTimedOut,
		Pending:   pending,
		From:      StepStateWaitingApproval,
		To:        StepStateFailed,
		Summary:   summary,
	}); err != nil {
		return err
	}

	if err := engine.persistRunTransition(
		store.EventRunFailed,
		RunStateWaitingApproval,
		RunStateFailed,
		summary,
	); err != nil {
		return err
	}

	return newError(ErrorCodeExecution, summary)
}
