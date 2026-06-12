package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/provider"
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

	_, err = driver.Interrupt(ctx, provider.ExecutionHandle{
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

	attemptID := e.idGen.NewAttemptID(stepID)

	if step.Kind == workflow.StepKindApproval {
		return e.executeApprovalStep(ctx, step, attemptID)
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
		return e.failStepStart(failStepStartParams{
			StepID: stepID, AttemptID: attemptID,
			Err: err, Message: "driver setup failed",
		})
	}

	// An interrupted step is parked in StepStateQueued with Resumable=true and a
	// preserved provider session. Re-attach to that session instead of starting
	// a brand-new attempt.
	prior := e.snapshot.Steps[stepID]
	if prior.Resumable &&
		strings.TrimSpace(prior.ProviderSessionID) != "" &&
		strings.TrimSpace(prior.AttemptID) != "" {
		return e.resumeStep(ctx, resumeStepParams{
			Step:              step,
			Driver:            driver,
			AttemptID:         prior.AttemptID,
			ProviderSessionID: prior.ProviderSessionID,
		})
	}

	execution, err := driver.Start(ctx, stepStartRequest{
		Step:       step,
		AttemptID:  attemptID,
		Snapshot:   e.Snapshot(),
		WorkingDir: e.workingDir,
	})
	if err != nil {
		return e.failStepStart(failStepStartParams{
			StepID: stepID, AttemptID: attemptID,
			Err: err, Message: "step start failed",
		})
	}

	providerSessionID := strings.TrimSpace(execution.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.idGen.NewSyntheticSessionID(stepID)
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

// resumeStepParams groups the inputs for resuming an interrupted step. It keeps
// the prior AttemptID + ProviderSessionID so the resume re-attaches to the same
// provider session rather than minting a fresh attempt.
type resumeStepParams struct {
	Step              workflow.CompiledStep
	Driver            stepDriver
	AttemptID         string
	ProviderSessionID string
}

// resumeStep re-attaches to a previously interrupted provider session. It calls
// the driver's Resume path, then persists EventStepStarted (StepStateQueued →
// StepStateRunning) reusing the prior attempt + session before handing back to
// the normal poll/normalize loop.
func (e *Engine) resumeStep(ctx context.Context, params resumeStepParams) error {
	handle := provider.ExecutionHandle{
		RunID:             e.runID,
		StepID:            params.Step.ID,
		AttemptID:         params.AttemptID,
		ProviderSessionID: params.ProviderSessionID,
	}

	prior := e.snapshot.Steps[params.Step.ID]

	execution, err := params.Driver.Resume(ctx, stepResumeRequest{
		Step:           params.Step,
		Handle:         handle,
		Snapshot:       e.Snapshot(),
		WorkingDir:     e.workingDir,
		RecoveryPrompt: e.recoveryPromptOverride(ctx, params.Step, prior),
	})
	if err != nil {
		return e.failStepStart(failStepStartParams{
			StepID: params.Step.ID, AttemptID: params.AttemptID,
			Err: err, Message: "step resume failed",
		})
	}

	providerSessionID := strings.TrimSpace(execution.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = params.ProviderSessionID
		execution.Handle.ProviderSessionID = providerSessionID
	}

	if err := e.persistStepTransition(StepTransitionParams{
		EventType:         store.EventStepStarted,
		StepID:            params.Step.ID,
		From:              StepStateQueued,
		To:                StepStateRunning,
		AttemptID:         params.AttemptID,
		ProviderSessionID: providerSessionID,
		Summary:           normalizeSummary(execution.Summary, execution.State),
		NormalizedStatus:  "",
	}); err != nil {
		return err
	}

	return e.continueExecution(ctx, executionContinuationRequest{
		Step:      params.Step,
		AttemptID: params.AttemptID,
		Driver:    params.Driver,
		Execution: execution,
	})
}

func (e *Engine) executeApprovalStep(
	ctx context.Context,
	step workflow.CompiledStep,
	attemptID string,
) error {
	stepID := step.ID
	providerSessionID := e.idGen.NewSyntheticSessionID(stepID)
	summary := defaultApprovalSummary(step, provider.ExecutionStateWaitingApproval)

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

	return e.requestApproval(ctx, ApprovalRequestParams{
		Step:              step,
		AttemptID:         attemptID,
		ProviderSessionID: providerSessionID,
		Summary:           summary,
		Trigger:           ApprovalTriggerExplicit,
		Status:            provider.ExecutionStateWaitingApproval,
	})
}

// failStepStartParams carries the inputs for recording a failed step start;
// a struct keeps the call sites within the three-parameter rule.
type failStepStartParams struct {
	StepID    string
	AttemptID string
	Err       error
	Message   string
}

func (e *Engine) failStepStart(params failStepStartParams) error {
	stepID, attemptID := params.StepID, params.AttemptID
	executionErr, message := params.Err, params.Message
	providerSessionID := e.idGen.NewSyntheticSessionID(stepID)

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
	Execution *provider.Execution
}

func (e *Engine) continueExecution(ctx context.Context, request executionContinuationRequest) error {
	providerSessionID := strings.TrimSpace(request.Execution.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.idGen.NewSyntheticSessionID(request.Step.ID)
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
	Result    *provider.StepResult
}

func (e *Engine) applyResult(ctx context.Context, request executionResultRequest) error {
	if request.Result == nil {
		return newError(ErrorCodeExecution, "step result is required")
	}

	providerSessionID := strings.TrimSpace(request.Result.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.idGen.NewSyntheticSessionID(request.Step.ID)
	}

	summary := normalizeSummary(request.Result.Summary, request.Result.Status)

	switch request.Result.Status {
	case provider.ExecutionStateSucceeded:
		return e.persistStepTransition(StepTransitionParams{
			EventType:         store.EventStepSucceeded,
			StepID:            request.Step.ID,
			From:              StepStateRunning,
			To:                StepStateSucceeded,
			AttemptID:         request.AttemptID,
			ProviderSessionID: providerSessionID,
			Summary:           summary,
			NormalizedStatus:  string(request.Result.Status),
			StructuredOutput:  request.Result.StructuredOutput,
		})
	case provider.ExecutionStateFailed:
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
	case provider.ExecutionStateWaitingApproval:
		return e.requestApproval(
			ctx,
			ApprovalRequestParams{
				Step:              request.Step,
				AttemptID:         request.AttemptID,
				ProviderSessionID: providerSessionID,
				Summary:           summary,
				Trigger:           ApprovalTriggerAdapter,
				Status:            request.Result.Status,
			},
		)
	case provider.ExecutionStateInterrupted:
		if err := e.persistStepTransition(StepTransitionParams{
			EventType:         store.EventStepInterrupted,
			StepID:            request.Step.ID,
			From:              StepStateRunning,
			To:                StepStateQueued,
			AttemptID:         request.AttemptID,
			ProviderSessionID: providerSessionID,
			Summary:           summary,
			NormalizedStatus:  string(request.Result.Status),
			Resumable:         true,
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
	Start(ctx context.Context, request stepStartRequest) (*provider.Execution, error)
	Resume(ctx context.Context, request stepResumeRequest) (*provider.Execution, error)
	PollOrCollect(ctx context.Context, handle provider.ExecutionHandle) (*provider.Execution, error)
	Interrupt(ctx context.Context, handle provider.ExecutionHandle) (*provider.Execution, error)
	NormalizeResult(ctx context.Context, execution *provider.Execution) (*provider.StepResult, error)
}

type stepStartRequest struct {
	Step       workflow.CompiledStep
	AttemptID  string
	Snapshot   Snapshot
	WorkingDir string
}

type stepResumeRequest struct {
	Step     workflow.CompiledStep
	Handle   provider.ExecutionHandle
	Snapshot Snapshot
	// WorkingDir is the run's working directory, threaded so a cross-process
	// resume can hand the adapter the original directory instead of relying on
	// the now-empty in-process session map.
	WorkingDir string
	// RecoveryPrompt, when non-empty, overrides the agent step's main prompt on
	// resume. The engine computes it from the work-tree state (dirty worktree or
	// missing self-reported commits) so a post-interrupt resume finishes the
	// prior attempt instead of restarting it. Empty means resume verbatim.
	RecoveryPrompt string
}

type agentDriver struct {
	adapter provider.Provider
}

func (d agentDriver) Start(ctx context.Context, request stepStartRequest) (*provider.Execution, error) {
	if request.Step.Agent == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("agent config missing for step %q", request.Step.ID))
	}

	return d.adapter.Start(ctx, provider.StartRequest{
		RunID:      request.Snapshot.RunID,
		StepID:     request.Step.ID,
		AttemptID:  request.AttemptID,
		WorkingDir: request.WorkingDir,
		Prompt:     request.Step.Agent.Prompt,
	})
}

func (d agentDriver) PollOrCollect(ctx context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	return d.adapter.PollOrCollect(ctx, handle)
}

func (d agentDriver) Interrupt(ctx context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	if err := d.adapter.DescribeCapabilities().Require(provider.CapabilityInterrupt); err != nil {
		return nil, wrapError(ErrorCodeExecution, "interrupt agent step", err)
	}

	return d.adapter.Interrupt(ctx, handle)
}

func (d agentDriver) Resume(ctx context.Context, request stepResumeRequest) (*provider.Execution, error) {
	if request.Step.Agent == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("agent config missing for step %q", request.Step.ID))
	}

	if err := d.adapter.DescribeCapabilities().Require(provider.CapabilityResume); err != nil {
		return nil, wrapError(ErrorCodeExecution, "resume agent step", err)
	}

	// A recovery prompt (dirty worktree / missing commit) overrides the main
	// prompt on resume; an empty override resumes the original task verbatim.
	resumePrompt := request.Step.Agent.Prompt
	if strings.TrimSpace(request.RecoveryPrompt) != "" {
		resumePrompt = request.RecoveryPrompt
	}

	return d.adapter.Resume(ctx, provider.ResumeRequest{
		Handle:     request.Handle,
		Prompt:     resumePrompt,
		WorkingDir: request.WorkingDir,
	})
}

func (d agentDriver) NormalizeResult(ctx context.Context, execution *provider.Execution) (*provider.StepResult, error) {
	return d.adapter.NormalizeResult(ctx, provider.NormalizeRequest{Execution: execution})
}

type commandDriver struct {
	runner CommandRunner
}

func (d commandDriver) Start(ctx context.Context, request stepStartRequest) (*provider.Execution, error) {
	if request.Step.Command == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("command config missing for step %q", request.Step.ID))
	}

	workingDir := strings.TrimSpace(request.WorkingDir)
	if workingDir == "" {
		workingDir = "."
	}

	return d.runner.Start(ctx, CommandRequest{
		RunID:      request.Snapshot.RunID,
		StepID:     request.Step.ID,
		AttemptID:  request.AttemptID,
		Command:    request.Step.Command.Command,
		WorkingDir: workingDir,
	})
}

func (d commandDriver) PollOrCollect(
	ctx context.Context,
	handle provider.ExecutionHandle,
) (*provider.Execution, error) {
	return d.runner.PollOrCollect(ctx, handle)
}

func (d commandDriver) Interrupt(ctx context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	return d.runner.Interrupt(ctx, handle)
}

func (d commandDriver) Resume(_ context.Context, request stepResumeRequest) (*provider.Execution, error) {
	return nil, newError(
		ErrorCodeExecution,
		fmt.Sprintf("command step %q does not support approval resume", request.Step.ID),
	)
}

func (d commandDriver) NormalizeResult(
	ctx context.Context,
	execution *provider.Execution,
) (*provider.StepResult, error) {
	return d.runner.NormalizeResult(ctx, execution)
}

type approvalDriver struct {
	runID string
	ids   IDGenerator
}

func (d approvalDriver) Start(_ context.Context, request stepStartRequest) (*provider.Execution, error) {
	return &provider.Execution{
		Handle: provider.ExecutionHandle{
			RunID:             d.runID,
			StepID:            request.Step.ID,
			AttemptID:         request.AttemptID,
			ProviderSessionID: d.ids.NewSyntheticSessionID(request.Step.ID),
		},
		State:   provider.ExecutionStateWaitingApproval,
		Summary: defaultApprovalSummary(request.Step, provider.ExecutionStateWaitingApproval),
	}, nil
}

func (d approvalDriver) Resume(_ context.Context, request stepResumeRequest) (*provider.Execution, error) {
	return &provider.Execution{
		Handle:  request.Handle,
		State:   provider.ExecutionStateSucceeded,
		Summary: approvalDecisionSummary(ApprovalDecisionApprove, request.Step),
	}, nil
}

func (d approvalDriver) Interrupt(_ context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	return &provider.Execution{
		Handle:  handle,
		State:   provider.ExecutionStateInterrupted,
		Summary: "approval interrupted",
	}, nil
}

func (d approvalDriver) PollOrCollect(_ context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	return &provider.Execution{
		Handle:  handle,
		State:   provider.ExecutionStateWaitingApproval,
		Summary: "approval pending",
	}, nil
}

func (d approvalDriver) NormalizeResult(
	_ context.Context,
	execution *provider.Execution,
) (*provider.StepResult, error) {
	if execution == nil {
		return nil, newError(ErrorCodeExecution, "approval execution is required")
	}

	return &provider.StepResult{Handle: execution.Handle, Status: execution.State, Summary: execution.Summary}, nil
}
