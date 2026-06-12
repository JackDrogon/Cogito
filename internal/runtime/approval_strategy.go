package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/provider"
)

type approvalContinuationStrategy interface {
	Continue(ctx context.Context, request approvalContinuationRequest) (*provider.Execution, error)
	FailureMessage() string
}

type approvalContinuationStrategyFunc struct {
	continueFn     func(ctx context.Context, request approvalContinuationRequest) (*provider.Execution, error)
	failureMessage string
}

type approvalContinuationRequest struct {
	Engine  *Engine
	Pending pendingApproval
	Driver  stepDriver
	Handle  provider.ExecutionHandle
}

func (s approvalContinuationStrategyFunc) Continue(
	ctx context.Context,
	request approvalContinuationRequest,
) (*provider.Execution, error) {
	return s.continueFn(ctx, request)
}

func (s approvalContinuationStrategyFunc) FailureMessage() string {
	return s.failureMessage
}

// resumeAfterApproval re-attaches to the paused provider session. Explicit
// gates and adapter-raised approvals share it: in both cases the step was
// already started and parked, so the continuation is a Resume, not a Start.
var resumeAfterApproval = approvalContinuationStrategyFunc{
	continueFn: func(ctx context.Context, request approvalContinuationRequest) (*provider.Execution, error) {
		return request.Driver.Resume(ctx, stepResumeRequest{
			Step:     request.Pending.Step,
			Handle:   request.Handle,
			Snapshot: request.Engine.Snapshot(),
		})
	},
	failureMessage: "step resume failed after approval",
}

// approvalContinuationStrategies is the read-only trigger dispatch table,
// built once at package init. Do not mutate after init.
var approvalContinuationStrategies = map[ApprovalTrigger]approvalContinuationStrategy{
	ApprovalTriggerExplicit: resumeAfterApproval,
	ApprovalTriggerAdapter:  resumeAfterApproval,
	// Policy exceptions pause the step BEFORE it ever starts, so the
	// continuation is a fresh Start rather than a Resume.
	ApprovalTriggerPolicy: approvalContinuationStrategyFunc{
		continueFn: func(ctx context.Context, request approvalContinuationRequest) (*provider.Execution, error) {
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

	return strategy, nil
}

func finalizeApprovedExecution(execution *provider.Execution, providerSessionID string) *provider.Execution {
	if execution == nil {
		return nil
	}

	if strings.TrimSpace(execution.Handle.ProviderSessionID) == "" {
		execution.Handle.ProviderSessionID = providerSessionID
	}

	return execution
}
