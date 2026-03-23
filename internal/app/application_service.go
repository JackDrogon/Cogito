package app

import (
	"context"
	"errors"

	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type applicationService struct {
	runs runService
}

type ValidateWorkflowInput struct {
	WorkflowPath string
}

type RunWorkflowInput struct {
	WorkflowPath string
	Flags        *sharedFlags
}

type RunWorkflowOutput struct {
	RunID    string
	StateDir string
	State    runtime.RunState
}

type StatusRunInput struct {
	StateDir string
}

type StatusRunOutput struct {
	StateDir string
	View     runtime.RunStatusView
}

type ResumeRunInput struct {
	Flags *sharedFlags
}

type ResumeRunOutput struct {
	Message string
}

type ReplayRunInput struct {
	EventsPath string
}

type ReplayRunOutput struct {
	View runtime.ReplayView
}

type CancelRunInput struct {
	StateDir string
}

type CancelRunOutput struct {
	Message string
}

type ApproveRunInput struct {
	Flags *sharedFlags
}

type ApproveRunOutput struct {
	Message string
}

func newApplicationService() applicationService {
	return applicationService{runs: runs}
}

func (applicationService) ValidateWorkflow(_ context.Context, input ValidateWorkflowInput) error {
	if _, err := workflow.LoadFile(input.WorkflowPath); err != nil {
		return err
	}

	return nil
}

func (s applicationService) RunWorkflow(ctx context.Context, input RunWorkflowInput) (output RunWorkflowOutput, err error) {
	if input.Flags == nil {
		return RunWorkflowOutput{}, errors.New("applicationService.RunWorkflow: flags are required")
	}

	approvalMode, err := runtime.ParseApprovalMode(input.Flags.approval)
	if err != nil {
		return RunWorkflowOutput{}, err
	}

	compiled, err := workflow.LoadFile(input.WorkflowPath)
	if err != nil {
		return RunWorkflowOutput{}, err
	}

	stateRef, err := newRunStateRef(input.Flags.stateDir)
	if err != nil {
		return RunWorkflowOutput{}, err
	}

	repoLock, err := acquireRepoLock(input.Flags, stateRef.runID, stateRef.baseDir)
	if err != nil {
		return RunWorkflowOutput{}, err
	}
	defer func() {
		if releaseErr := repoLock.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()

	runStore, err := store.Open(stateRef.baseDir, stateRef.runID)
	if err != nil {
		return RunWorkflowOutput{}, err
	}

	if err := workflow.SaveResolvedFile(runStore.Layout().WorkflowPath, compiled); err != nil {
		return RunWorkflowOutput{}, err
	}

	runEngine, err := s.runs.newRunEngine(newRunEngineInput{
		RunID:          stateRef.runID,
		Compiled:       compiled,
		RunStore:       runStore,
		Flags:          input.Flags,
		ApprovalPolicy: runtime.NewApprovalModePolicy(approvalMode),
	})
	if err != nil {
		return RunWorkflowOutput{}, err
	}
	engine := runEngine.engine

	if err := s.runs.executeUntilSettled(ctx, engine); err != nil {
		return RunWorkflowOutput{}, err
	}

	snapshot := engine.Snapshot()
	if snapshot.State == runtime.RunStateFailed {
		return RunWorkflowOutput{}, latestRunFailure(runStore)
	}

	return RunWorkflowOutput{RunID: stateRef.runID, StateDir: runStore.Layout().RunDir, State: snapshot.State}, nil
}

func (s applicationService) StatusRun(_ context.Context, input StatusRunInput) (StatusRunOutput, error) {
	session, err := s.runs.openExistingRunSession(input.StateDir, nil)
	if err != nil {
		return StatusRunOutput{}, err
	}

	snapshot := session.engine.Snapshot()
	statusView := runtime.BuildRunStatusView(session.compiled, snapshot)
	return StatusRunOutput{
		StateDir: session.store.Layout().RunDir,
		View:     statusView,
	}, nil
}

func (s applicationService) ResumeRun(ctx context.Context, input ResumeRunInput) (ResumeRunOutput, error) {
	if input.Flags == nil {
		return ResumeRunOutput{}, errors.New("applicationService.ResumeRun: flags are required")
	}

	session, err := s.runs.openExistingRunSession(input.Flags.stateDir, input.Flags)
	if err != nil {
		return ResumeRunOutput{}, err
	}

	if err := session.engine.Resume(""); err != nil {
		return ResumeRunOutput{}, err
	}

	if err := s.runs.executeUntilSettled(ctx, session.engine); err != nil {
		return ResumeRunOutput{}, err
	}

	if session.engine.Snapshot().State == runtime.RunStateFailed {
		return ResumeRunOutput{}, latestRunFailure(session.store)
	}

	return ResumeRunOutput{Message: "run resumed"}, nil
}

func (applicationService) ReplayRun(_ context.Context, input ReplayRunInput) (ReplayRunOutput, error) {
	replayInput, err := loadReplayInput(input.EventsPath)
	if err != nil {
		return ReplayRunOutput{}, err
	}

	replay, err := runtime.Replay(replayInput.runID, replayInput.compiled, replayInput.events)
	if err != nil {
		return ReplayRunOutput{}, err
	}

	return ReplayRunOutput{View: runtime.BuildReplayView(replayInput.compiled, *replay)}, nil
}

func (s applicationService) CancelRun(ctx context.Context, input CancelRunInput) (CancelRunOutput, error) {
	session, err := s.runs.openExistingRunSession(input.StateDir, nil)
	if err != nil {
		return CancelRunOutput{}, err
	}

	if err := session.engine.Cancel(ctx, ""); err != nil {
		return CancelRunOutput{}, err
	}

	return CancelRunOutput{Message: "run canceled"}, nil
}

func (s applicationService) ApproveRun(ctx context.Context, input ApproveRunInput) (ApproveRunOutput, error) {
	if input.Flags == nil {
		return ApproveRunOutput{}, errors.New("applicationService.ApproveRun: flags are required")
	}

	session, err := s.runs.openExistingRunSession(input.Flags.stateDir, input.Flags)
	if err != nil {
		return ApproveRunOutput{}, err
	}

	if err := session.engine.GrantApproval(ctx, "approved via CLI"); err != nil {
		return ApproveRunOutput{}, err
	}

	if err := s.runs.executeUntilSettled(ctx, session.engine); err != nil {
		return ApproveRunOutput{}, err
	}

	return ApproveRunOutput{Message: "approval granted"}, nil
}
