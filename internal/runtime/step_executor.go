package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

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

func (e *Engine) executeStep(ctx context.Context, stepID string) error {
	step, err := e.lookupStep(stepID)
	if err != nil {
		return err
	}

	attemptID := e.ids.NewAttemptID(stepID)

	if step.Kind == workflow.StepKindApproval {
		return e.executeApprovalStep(ctx, stepID, step, attemptID)
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
		return e.failStepStart(stepID, attemptID, err, "driver setup failed")
	}

	execution, err := driver.Start(ctx, stepStartRequest{
		Step:      step,
		AttemptID: attemptID,
		Snapshot:  e.Snapshot(),
	})
	if err != nil {
		return e.failStepStart(stepID, attemptID, err, "step start failed")
	}

	providerSessionID := strings.TrimSpace(execution.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.ids.NewSyntheticSessionID(stepID)
		execution.Handle.ProviderSessionID = providerSessionID
	}

	if err := e.persistStepTransition(StepTransitionParams{
		EventType:         store.EventStepStarted,
		StepID:            stepID,
		From:              StepStateQueued,
		To:                StepStateRunning,
		AttemptID:         attemptID,
		ProviderSessionID: providerSessionID,
		Summary:           normalizeSummary(execution.Summary, execution.State),
		NormalizedStatus:  "",
	}); err != nil {
		return err
	}

	return e.continueExecution(ctx, executionContinuationRequest{
		Step:      step,
		AttemptID: attemptID,
		Driver:    driver,
		Execution: execution,
	})
}

func (e *Engine) executeApprovalStep(
	ctx context.Context,
	stepID string,
	step workflow.CompiledStep,
	attemptID string,
) error {
	providerSessionID := e.ids.NewSyntheticSessionID(stepID)
	summary := defaultApprovalSummary(step, adapters.ExecutionStateWaitingApproval)

	if err := e.persistStepTransition(StepTransitionParams{
		EventType:         store.EventStepStarted,
		StepID:            stepID,
		From:              StepStateQueued,
		To:                StepStateRunning,
		AttemptID:         attemptID,
		ProviderSessionID: providerSessionID,
		Summary:           summary,
		NormalizedStatus:  "",
	}); err != nil {
		return err
	}

	return e.requestApproval(ctx, approvalGateParams{
		Step:              step,
		AttemptID:         attemptID,
		ProviderSessionID: providerSessionID,
		Summary:           summary,
		Trigger:           ApprovalTriggerExplicit,
		Status:            adapters.ExecutionStateWaitingApproval,
	})
}

func (e *Engine) failStepStart(stepID, attemptID string, executionErr error, message string) error {
	providerSessionID := e.ids.NewSyntheticSessionID(stepID)

	if startErr := e.persistStepTransition(StepTransitionParams{
		EventType:         store.EventStepStarted,
		StepID:            stepID,
		From:              StepStateQueued,
		To:                StepStateRunning,
		AttemptID:         attemptID,
		ProviderSessionID: providerSessionID,
		Summary:           "step started",
		NormalizedStatus:  "",
	}); startErr != nil {
		return startErr
	}

	return e.failRunForExecutionError(FailRunParams{
		StepID:            stepID,
		AttemptID:         attemptID,
		ProviderSessionID: providerSessionID,
		ExecutionErr:      executionErr,
		Message:           message,
	})
}

type executionContinuationRequest struct {
	Step      workflow.CompiledStep
	AttemptID string
	Driver    stepDriver
	Execution *adapters.Execution
}

func (e *Engine) continueExecution(ctx context.Context, request executionContinuationRequest) error {
	providerSessionID := strings.TrimSpace(request.Execution.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.ids.NewSyntheticSessionID(request.Step.ID)
		request.Execution.Handle.ProviderSessionID = providerSessionID
	}

	var err error
	for !request.Execution.State.Normalizable() {
		request.Execution, err = request.Driver.PollOrCollect(ctx, request.Execution.Handle)
		if err != nil {
			return e.failRunForExecutionError(FailRunParams{
				StepID:            request.Step.ID,
				AttemptID:         request.AttemptID,
				ProviderSessionID: providerSessionID,
				ExecutionErr:      err,
				Message:           "step polling failed",
			})
		}

		if strings.TrimSpace(request.Execution.Handle.ProviderSessionID) == "" {
			request.Execution.Handle.ProviderSessionID = providerSessionID
		}
	}

	result, err := request.Driver.NormalizeResult(ctx, request.Execution)
	if err != nil {
		return e.failRunForExecutionError(FailRunParams{
			StepID:            request.Step.ID,
			AttemptID:         request.AttemptID,
			ProviderSessionID: providerSessionID,
			ExecutionErr:      err,
			Message:           "result normalization failed",
		})
	}

	if strings.TrimSpace(result.Handle.ProviderSessionID) == "" {
		result.Handle.ProviderSessionID = providerSessionID
	}

	return e.applyResult(ctx, executionResultRequest{
		Step:      request.Step,
		AttemptID: request.AttemptID,
		Result:    result,
	})
}

type executionResultRequest struct {
	Step      workflow.CompiledStep
	AttemptID string
	Result    *adapters.StepResult
}

func (e *Engine) applyResult(ctx context.Context, request executionResultRequest) error {
	if request.Result == nil {
		return newError(ErrorCodeExecution, "step result is required")
	}

	providerSessionID := strings.TrimSpace(request.Result.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.ids.NewSyntheticSessionID(request.Step.ID)
	}

	summary := normalizeSummary(request.Result.Summary, request.Result.Status)

	switch request.Result.Status {
	case adapters.ExecutionStateSucceeded:
		return e.persistStepTransition(StepTransitionParams{
			EventType:         store.EventStepSucceeded,
			StepID:            request.Step.ID,
			From:              StepStateRunning,
			To:                StepStateSucceeded,
			AttemptID:         request.AttemptID,
			ProviderSessionID: providerSessionID,
			Summary:           summary,
			NormalizedStatus:  string(request.Result.Status),
		})
	case adapters.ExecutionStateFailed:
		if err := e.persistStepTransition(StepTransitionParams{
			EventType:         store.EventStepFailed,
			StepID:            request.Step.ID,
			From:              StepStateRunning,
			To:                StepStateFailed,
			AttemptID:         request.AttemptID,
			ProviderSessionID: providerSessionID,
			Summary:           summary,
			NormalizedStatus:  string(request.Result.Status),
		}); err != nil {
			return err
		}

		return e.persistRunTransition(RunTransitionParams{
			EventType: store.EventRunFailed,
			From:      RunStateRunning,
			To:        RunStateFailed,
			Message:   summary,
		})
	case adapters.ExecutionStateWaitingApproval:
		return e.requestApproval(
			ctx,
			approvalGateParams{
				Step:              request.Step,
				AttemptID:         request.AttemptID,
				ProviderSessionID: providerSessionID,
				Summary:           summary,
				Trigger:           ApprovalTriggerAdapter,
				Status:            request.Result.Status,
			},
		)
	case adapters.ExecutionStateInterrupted:
		if err := e.persistStepTransition(StepTransitionParams{
			EventType:         store.EventStepRetried,
			StepID:            request.Step.ID,
			From:              StepStateRunning,
			To:                StepStateQueued,
			AttemptID:         request.AttemptID,
			ProviderSessionID: providerSessionID,
			Summary:           summary,
			NormalizedStatus:  string(request.Result.Status),
		}); err != nil {
			return err
		}

		return e.persistRunTransition(RunTransitionParams{
			EventType: store.EventRunPaused,
			From:      RunStateRunning,
			To:        RunStatePaused,
			Message:   summary,
		})
	default:
		return newError(ErrorCodeExecution, fmt.Sprintf("unsupported normalized step status %q", request.Result.Status))
	}
}

func (e *Engine) buildDriver(step workflow.CompiledStep) (stepDriver, error) {
	if e.driverFactory == nil {
		return nil, newError(ErrorCodeConfig, "step driver factory is required")
	}

	return e.driverFactory.Build(e, step)
}

type stepDriver interface {
	Start(ctx context.Context, request stepStartRequest) (*adapters.Execution, error)
	Resume(ctx context.Context, request stepResumeRequest) (*adapters.Execution, error)
	PollOrCollect(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error)
	Interrupt(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error)
	NormalizeResult(ctx context.Context, execution *adapters.Execution) (*adapters.StepResult, error)
}

type stepStartRequest struct {
	Step      workflow.CompiledStep
	AttemptID string
	Snapshot  Snapshot
}

type stepResumeRequest struct {
	Step     workflow.CompiledStep
	Handle   adapters.ExecutionHandle
	Snapshot Snapshot
}

type agentDriver struct {
	adapter adapters.Adapter
}

func (d agentDriver) Start(ctx context.Context, request stepStartRequest) (*adapters.Execution, error) {
	if request.Step.Agent == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("agent config missing for step %q", request.Step.ID))
	}

	return d.adapter.Start(ctx, adapters.StartRequest{
		RunID:     request.Snapshot.RunID,
		StepID:    request.Step.ID,
		AttemptID: request.AttemptID,
		Prompt:    request.Step.Agent.Prompt,
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

func (d agentDriver) Resume(ctx context.Context, request stepResumeRequest) (*adapters.Execution, error) {
	if request.Step.Agent == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("agent config missing for step %q", request.Step.ID))
	}

	if err := d.adapter.DescribeCapabilities().Require(adapters.CapabilityResume); err != nil {
		return nil, wrapError(ErrorCodeExecution, "resume agent step", err)
	}

	return d.adapter.Resume(ctx, adapters.ResumeRequest{
		Handle: request.Handle,
		Prompt: request.Step.Agent.Prompt,
	})
}

func (d agentDriver) NormalizeResult(ctx context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	return d.adapter.NormalizeResult(ctx, adapters.NormalizeRequest{Execution: execution})
}

type commandDriver struct {
	runner CommandRunner
}

func (d commandDriver) Start(ctx context.Context, request stepStartRequest) (*adapters.Execution, error) {
	if request.Step.Command == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("command config missing for step %q", request.Step.ID))
	}

	return d.runner.Start(ctx, CommandRequest{
		RunID:      request.Snapshot.RunID,
		StepID:     request.Step.ID,
		AttemptID:  request.AttemptID,
		Command:    request.Step.Command.Command,
		WorkingDir: ".",
	})
}

func (d commandDriver) PollOrCollect(
	ctx context.Context,
	handle adapters.ExecutionHandle,
) (*adapters.Execution, error) {
	return d.runner.PollOrCollect(ctx, handle)
}

func (d commandDriver) Interrupt(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return d.runner.Interrupt(ctx, handle)
}

func (d commandDriver) Resume(_ context.Context, request stepResumeRequest) (*adapters.Execution, error) {
	return nil, newError(
		ErrorCodeExecution,
		fmt.Sprintf("command step %q does not support approval resume", request.Step.ID),
	)
}

func (d commandDriver) NormalizeResult(
	ctx context.Context,
	execution *adapters.Execution,
) (*adapters.StepResult, error) {
	return d.runner.NormalizeResult(ctx, execution)
}

type approvalDriver struct {
	runID string
	ids   IDGenerator
}

func (d approvalDriver) Start(_ context.Context, request stepStartRequest) (*adapters.Execution, error) {
	return &adapters.Execution{
		Handle: adapters.ExecutionHandle{
			RunID:             d.runID,
			StepID:            request.Step.ID,
			AttemptID:         request.AttemptID,
			ProviderSessionID: d.ids.NewSyntheticSessionID(request.Step.ID),
		},
		State:   adapters.ExecutionStateWaitingApproval,
		Summary: defaultApprovalSummary(request.Step, adapters.ExecutionStateWaitingApproval),
	}, nil
}

func (d approvalDriver) Resume(_ context.Context, request stepResumeRequest) (*adapters.Execution, error) {
	return &adapters.Execution{
		Handle:  request.Handle,
		State:   adapters.ExecutionStateSucceeded,
		Summary: approvalDecisionSummary(ApprovalDecisionApprove, request.Step),
	}, nil
}

func (d approvalDriver) Interrupt(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{
		Handle:  handle,
		State:   adapters.ExecutionStateInterrupted,
		Summary: "approval interrupted",
	}, nil
}

func (d approvalDriver) PollOrCollect(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{
		Handle:  handle,
		State:   adapters.ExecutionStateWaitingApproval,
		Summary: "approval pending",
	}, nil
}

func (d approvalDriver) NormalizeResult(
	_ context.Context,
	execution *adapters.Execution,
) (*adapters.StepResult, error) {
	if execution == nil {
		return nil, newError(ErrorCodeExecution, "approval execution is required")
	}

	return &adapters.StepResult{Handle: execution.Handle, Status: execution.State, Summary: execution.Summary}, nil
}
