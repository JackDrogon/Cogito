package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters"
)

type approvalContinuationStrategy interface {
	Continue(ctx context.Context, engine *Engine, pending pendingApproval, driver stepDriver, handle adapters.ExecutionHandle) (*adapters.Execution, error)
	FailureMessage() string
}

type approvalContinuationStrategyFunc struct {
	continueFn     func(ctx context.Context, engine *Engine, pending pendingApproval, driver stepDriver, handle adapters.ExecutionHandle) (*adapters.Execution, error)
	failureMessage string
}

func (s approvalContinuationStrategyFunc) Continue(ctx context.Context, engine *Engine, pending pendingApproval, driver stepDriver, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return s.continueFn(ctx, engine, pending, driver, handle)
}

func (s approvalContinuationStrategyFunc) FailureMessage() string {
	return s.failureMessage
}

var approvalContinuationStrategies = map[ApprovalTrigger]approvalContinuationStrategy{
	ApprovalTriggerExplicit: approvalContinuationStrategyFunc{
		continueFn: func(ctx context.Context, engine *Engine, pending pendingApproval, driver stepDriver, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
			return driver.Resume(ctx, pending.Step, handle, engine.Snapshot())
		},
		failureMessage: "step resume failed after approval",
	},
	ApprovalTriggerAdapter: approvalContinuationStrategyFunc{
		continueFn: func(ctx context.Context, engine *Engine, pending pendingApproval, driver stepDriver, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
			return driver.Resume(ctx, pending.Step, handle, engine.Snapshot())
		},
		failureMessage: "step resume failed after approval",
	},
	ApprovalTriggerPolicy: approvalContinuationStrategyFunc{
		continueFn: func(ctx context.Context, engine *Engine, pending pendingApproval, driver stepDriver, _ adapters.ExecutionHandle) (*adapters.Execution, error) {
			return driver.Start(ctx, pending.Step, pending.AttemptID, engine.Snapshot())
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
		return nil, newError(ErrorCodeExecution, fmt.Sprintf("approval continuation strategy is required for %q", trigger))
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
