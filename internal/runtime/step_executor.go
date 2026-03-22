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
		providerSessionID := e.ids.NewSyntheticSessionID(stepID)
		summary := defaultApprovalSummary(step, adapters.ExecutionStateWaitingApproval)

		if err := e.persistStepTransition(store.EventStepStarted, stepID, StepStateQueued, StepStateRunning, attemptID, providerSessionID, summary, ""); err != nil {
			return err
		}

		return e.requestApproval(ctx, step, attemptID, providerSessionID, summary, ApprovalTriggerExplicit, adapters.ExecutionStateWaitingApproval)
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
		providerSessionID := e.ids.NewSyntheticSessionID(stepID)
		if startErr := e.persistStepTransition(store.EventStepStarted, stepID, StepStateQueued, StepStateRunning, attemptID, providerSessionID, "step started", ""); startErr != nil {
			return startErr
		}

		return e.failRunForExecutionError(stepID, attemptID, providerSessionID, err, "driver setup failed")
	}

	execution, err := driver.Start(ctx, step, attemptID, e.Snapshot())
	if err != nil {
		providerSessionID := e.ids.NewSyntheticSessionID(stepID)
		if startErr := e.persistStepTransition(store.EventStepStarted, stepID, StepStateQueued, StepStateRunning, attemptID, providerSessionID, "step started", ""); startErr != nil {
			return startErr
		}

		return e.failRunForExecutionError(stepID, attemptID, providerSessionID, err, "step start failed")
	}

	providerSessionID := strings.TrimSpace(execution.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.ids.NewSyntheticSessionID(stepID)
		execution.Handle.ProviderSessionID = providerSessionID
	}

	if err := e.persistStepTransition(store.EventStepStarted, stepID, StepStateQueued, StepStateRunning, attemptID, providerSessionID, normalizeSummary(execution.Summary, execution.State), ""); err != nil {
		return err
	}

	return e.continueExecution(ctx, step, attemptID, driver, execution)
}

func (e *Engine) continueExecution(ctx context.Context, step workflow.CompiledStep, attemptID string, driver stepDriver, execution *adapters.Execution) error {
	providerSessionID := strings.TrimSpace(execution.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.ids.NewSyntheticSessionID(step.ID)
		execution.Handle.ProviderSessionID = providerSessionID
	}

	var err error
	for !execution.State.Normalizable() {
		execution, err = driver.PollOrCollect(ctx, execution.Handle)
		if err != nil {
			return e.failRunForExecutionError(step.ID, attemptID, providerSessionID, err, "step polling failed")
		}

		if strings.TrimSpace(execution.Handle.ProviderSessionID) == "" {
			execution.Handle.ProviderSessionID = providerSessionID
		}
	}

	result, err := driver.NormalizeResult(ctx, execution)
	if err != nil {
		return e.failRunForExecutionError(step.ID, attemptID, providerSessionID, err, "result normalization failed")
	}

	if strings.TrimSpace(result.Handle.ProviderSessionID) == "" {
		result.Handle.ProviderSessionID = providerSessionID
	}

	return e.applyResult(ctx, step, attemptID, result)
}

func (e *Engine) applyResult(ctx context.Context, step workflow.CompiledStep, attemptID string, result *adapters.StepResult) error {
	if result == nil {
		return newError(ErrorCodeExecution, "step result is required")
	}

	providerSessionID := strings.TrimSpace(result.Handle.ProviderSessionID)
	if providerSessionID == "" {
		providerSessionID = e.ids.NewSyntheticSessionID(step.ID)
	}

	summary := normalizeSummary(result.Summary, result.Status)

	switch result.Status {
	case adapters.ExecutionStateSucceeded:
		return e.persistStepTransition(store.EventStepSucceeded, step.ID, StepStateRunning, StepStateSucceeded, attemptID, providerSessionID, summary, string(result.Status))
	case adapters.ExecutionStateFailed:
		if err := e.persistStepTransition(store.EventStepFailed, step.ID, StepStateRunning, StepStateFailed, attemptID, providerSessionID, summary, string(result.Status)); err != nil {
			return err
		}

		return e.persistRunTransition(store.EventRunFailed, RunStateRunning, RunStateFailed, summary)
	case adapters.ExecutionStateWaitingApproval:
		return e.requestApproval(ctx, step, attemptID, providerSessionID, summary, ApprovalTriggerAdapter, result.Status)
	case adapters.ExecutionStateInterrupted:
		if err := e.persistStepTransition(store.EventStepRetried, step.ID, StepStateRunning, StepStateQueued, attemptID, providerSessionID, summary, string(result.Status)); err != nil {
			return err
		}

		return e.persistRunTransition(store.EventRunPaused, RunStateRunning, RunStatePaused, summary)
	default:
		return newError(ErrorCodeExecution, fmt.Sprintf("unsupported normalized step status %q", result.Status))
	}
}

func (e *Engine) buildDriver(step workflow.CompiledStep) (stepDriver, error) {
	switch step.Kind {
	case workflow.StepKindAgent:
		if e.lookupAdapter == nil {
			return nil, newError(ErrorCodeConfig, "adapter lookup is required for agent steps")
		}

		adapter, err := e.lookupAdapter(step)
		if err != nil {
			return nil, err
		}

		return agentDriver{adapter: adapter}, nil
	case workflow.StepKindCommand:
		if e.commandRunner == nil {
			return nil, newError(ErrorCodeConfig, "command runner is required for command steps")
		}

		return commandDriver{runner: e.commandRunner}, nil
	case workflow.StepKindApproval:
		return approvalDriver{runID: e.runID, ids: e.ids}, nil
	default:
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("unsupported step kind %q", step.Kind))
	}
}

type stepDriver interface {
	Start(ctx context.Context, step workflow.CompiledStep, attemptID string, snapshot Snapshot) (*adapters.Execution, error)
	Resume(ctx context.Context, step workflow.CompiledStep, handle adapters.ExecutionHandle, snapshot Snapshot) (*adapters.Execution, error)
	PollOrCollect(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error)
	Interrupt(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error)
	NormalizeResult(ctx context.Context, execution *adapters.Execution) (*adapters.StepResult, error)
}

type agentDriver struct {
	adapter adapters.Adapter
}

func (d agentDriver) Start(ctx context.Context, step workflow.CompiledStep, attemptID string, snapshot Snapshot) (*adapters.Execution, error) {
	if step.Agent == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("agent config missing for step %q", step.ID))
	}

	return d.adapter.Start(ctx, adapters.StartRequest{
		RunID:     snapshot.RunID,
		StepID:    step.ID,
		AttemptID: attemptID,
		Prompt:    step.Agent.Prompt,
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

func (d agentDriver) Resume(ctx context.Context, step workflow.CompiledStep, handle adapters.ExecutionHandle, _ Snapshot) (*adapters.Execution, error) {
	if step.Agent == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("agent config missing for step %q", step.ID))
	}

	if err := d.adapter.DescribeCapabilities().Require(adapters.CapabilityResume); err != nil {
		return nil, wrapError(ErrorCodeExecution, "resume agent step", err)
	}

	return d.adapter.Resume(ctx, adapters.ResumeRequest{Handle: handle, Prompt: step.Agent.Prompt})
}

func (d agentDriver) NormalizeResult(ctx context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	return d.adapter.NormalizeResult(ctx, adapters.NormalizeRequest{Execution: execution})
}

type commandDriver struct {
	runner CommandRunner
}

func (d commandDriver) Start(ctx context.Context, step workflow.CompiledStep, attemptID string, snapshot Snapshot) (*adapters.Execution, error) {
	if step.Command == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("command config missing for step %q", step.ID))
	}

	return d.runner.Start(ctx, CommandRequest{
		RunID:      snapshot.RunID,
		StepID:     step.ID,
		AttemptID:  attemptID,
		Command:    step.Command.Command,
		WorkingDir: ".",
	})
}

func (d commandDriver) PollOrCollect(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return d.runner.PollOrCollect(ctx, handle)
}

func (d commandDriver) Interrupt(ctx context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return d.runner.Interrupt(ctx, handle)
}

func (d commandDriver) Resume(_ context.Context, step workflow.CompiledStep, _ adapters.ExecutionHandle, _ Snapshot) (*adapters.Execution, error) {
	return nil, newError(ErrorCodeExecution, fmt.Sprintf("command step %q does not support approval resume", step.ID))
}

func (d commandDriver) NormalizeResult(ctx context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	return d.runner.NormalizeResult(ctx, execution)
}

type approvalDriver struct {
	runID string
	ids   IDGenerator
}

func (d approvalDriver) Start(_ context.Context, step workflow.CompiledStep, attemptID string, _ Snapshot) (*adapters.Execution, error) {
	return &adapters.Execution{
		Handle: adapters.ExecutionHandle{
			RunID:             d.runID,
			StepID:            step.ID,
			AttemptID:         attemptID,
			ProviderSessionID: d.ids.NewSyntheticSessionID(step.ID),
		},
		State:   adapters.ExecutionStateWaitingApproval,
		Summary: defaultApprovalSummary(step, adapters.ExecutionStateWaitingApproval),
	}, nil
}

func (d approvalDriver) Resume(_ context.Context, step workflow.CompiledStep, handle adapters.ExecutionHandle, _ Snapshot) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateSucceeded, Summary: approvalDecisionSummary(ApprovalDecisionApprove, step)}, nil
}

func (d approvalDriver) Interrupt(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateInterrupted, Summary: "approval interrupted"}, nil
}

func (d approvalDriver) PollOrCollect(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateWaitingApproval, Summary: "approval pending"}, nil
}

func (d approvalDriver) NormalizeResult(_ context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	if execution == nil {
		return nil, newError(ErrorCodeExecution, "approval execution is required")
	}

	return &adapters.StepResult{Handle: execution.Handle, Status: execution.State, Summary: execution.Summary}, nil
}
