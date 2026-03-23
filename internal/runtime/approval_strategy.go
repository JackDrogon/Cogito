package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters"
)

type approvalContinuationStrategy interface {
	Continue(ctx context.Context, request approvalContinuationRequest) (*adapters.Execution, error)
	FailureMessage() string
}

type approvalContinuationStrategyFunc struct {
	continueFn     func(ctx context.Context, request approvalContinuationRequest) (*adapters.Execution, error)
	failureMessage string
}

type approvalContinuationRequest struct {
	Engine  *Engine
	Pending pendingApproval
	Driver  stepDriver
	Handle  adapters.ExecutionHandle
}

func (s approvalContinuationStrategyFunc) Continue(
	ctx context.Context,
	request approvalContinuationRequest,
) (*adapters.Execution, error) {
	return s.continueFn(ctx, request)
}

func (s approvalContinuationStrategyFunc) FailureMessage() string {
	return s.failureMessage
}

var approvalContinuationStrategies = map[ApprovalTrigger]approvalContinuationStrategy{
	ApprovalTriggerExplicit: approvalContinuationStrategyFunc{
		continueFn: func(ctx context.Context, request approvalContinuationRequest) (*adapters.Execution, error) {
			return request.Driver.Resume(ctx, stepResumeRequest{
				Step:     request.Pending.Step,
				Handle:   request.Handle,
				Snapshot: request.Engine.Snapshot(),
			})
		},
		failureMessage: "step resume failed after approval",
	},
	ApprovalTriggerAdapter: approvalContinuationStrategyFunc{
		continueFn: func(ctx context.Context, request approvalContinuationRequest) (*adapters.Execution, error) {
			return request.Driver.Resume(ctx, stepResumeRequest{
				Step:     request.Pending.Step,
				Handle:   request.Handle,
				Snapshot: request.Engine.Snapshot(),
			})
		},
		failureMessage: "step resume failed after approval",
	},
	ApprovalTriggerPolicy: approvalContinuationStrategyFunc{
		continueFn: func(ctx context.Context, request approvalContinuationRequest) (*adapters.Execution, error) {
			return request.Driver.Start(ctx, stepStartRequest{
				Step:      request.Pending.Step,
				AttemptID: request.Pending.AttemptID,
				Snapshot:  request.Engine.Snapshot(),
			})
		},
		failureMessage: "step start failed after approval",
	},
}

func lookupApprovalContinuationStrategy(trigger ApprovalTrigger) (approvalContinuationStrategy, error) {
	strategy, ok := approvalContinuationStrategies[trigger]
	if !ok {
		return nil, newError(ErrorCodeExecution, fmt.Sprintf("unsupported approval trigger %q", trigger))
	}

	if strategy == nil {
		return nil, newError(
			ErrorCodeExecution,
			fmt.Sprintf("approval continuation strategy is required for %q", trigger),
		)
	}

	return strategy, nil
}

func finalizeApprovedExecution(execution *adapters.Execution, providerSessionID string) *adapters.Execution {
	if execution == nil {
		return nil
	}

	if strings.TrimSpace(execution.Handle.ProviderSessionID) == "" {
		execution.Handle.ProviderSessionID = providerSessionID
	}

	return execution
}
