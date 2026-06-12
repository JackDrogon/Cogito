package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/provider"
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

type RunCompiledWorkflowInput struct {
	Compiled *workflow.CompiledWorkflow
	Flags    *sharedFlags
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
	Orphans  []provider.OrphanProcess
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

func (s applicationService) RunWorkflow(ctx context.Context, input RunWorkflowInput) (RunWorkflowOutput, error) {
	if input.Flags == nil {
		return RunWorkflowOutput{}, errors.New("applicationService.RunWorkflow: flags are required")
	}

	compiled, err := workflow.LoadFile(input.WorkflowPath)
	if err != nil {
		return RunWorkflowOutput{}, err
	}

	return s.runCompiled(ctx, compiled, input.Flags)
}

// RunCompiledWorkflow runs an already-compiled workflow that was built in
// memory (for example by `cogito feishu run`) rather than loaded from a YAML
// file. It shares the same execution kernel as RunWorkflow, so the produced run
// directory (events.jsonl + checkpoint.json + persisted workflow.json) stays
// resume-compatible with the standard `cogito run` flow.
func (s applicationService) RunCompiledWorkflow(ctx context.Context, input RunCompiledWorkflowInput) (RunWorkflowOutput, error) {
	if input.Compiled == nil {
		return RunWorkflowOutput{}, errors.New("applicationService.RunCompiledWorkflow: compiled workflow is required")
	}

	return s.runCompiled(ctx, input.Compiled, input.Flags)
}

// runCompiled is the shared execution kernel for both RunWorkflow and
// RunCompiledWorkflow. It owns approval parsing, run-state resolution, repo
// locking, durable workflow persistence, and engine execution. The only
// difference between the two public entry points is the source of compiled:
// a YAML file vs an in-memory build.
//
//nolint:nonamedreturns // the deferred lock release below must mutate err to surface release failures
func (s applicationService) runCompiled(ctx context.Context, compiled *workflow.CompiledWorkflow, flags *sharedFlags) (output RunWorkflowOutput, err error) {
	if flags == nil {
		return RunWorkflowOutput{}, errors.New("applicationService.runCompiled: flags are required")
	}

	approvalMode, err := runtime.ParseApprovalMode(flags.approval)
	if err != nil {
		return RunWorkflowOutput{}, err
	}

	stateRef, err := newRunStateRef(flags.stateDir)
	if err != nil {
		return RunWorkflowOutput{}, err
	}

	// Self-ignore the .cogito state root before the lock's dirty-worktree
	// check runs, so run state never shows up as untracked changes.
	if err := ensureStateRootIgnored(stateRef); err != nil {
		return RunWorkflowOutput{}, err
	}

	repoLock, err := acquireRepoLock(ctx, acquireRepoLockInput{
		flags:    flags,
		runID:    stateRef.runID,
		runsRoot: stateRef.baseDir,
	})
	if err != nil {
		return RunWorkflowOutput{}, err
	}

	// Named returns are required here: this defer surfaces a repo-lock release
	// failure to the caller by writing the function's actual return value. With
	// bare returns the assignment would target a shadow variable and the stale
	// lock would go unreported.
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
		Flags:          flags,
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

	orphans, err := provider.FindOrphans(session.store.Layout().RunDir)
	if err != nil {
		return StatusRunOutput{}, err
	}

	return StatusRunOutput{
		StateDir: session.store.Layout().RunDir,
		View:     statusView,
		Orphans:  orphans,
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

	reapReport, err := recoverCrashedRun(session)
	if err != nil {
		return ResumeRunOutput{}, err
	}

	// Resuming a run that already settled successfully is a no-op, not an
	// error: the user asked to make progress and there is none left to make.
	// Failed/canceled runs still fall through to engine.Resume, which reports
	// the appropriate "cannot resume from <state>" error.
	if session.engine.Snapshot().State == runtime.RunStateSucceeded {
		return ResumeRunOutput{Message: renderRunMessage(reapReport, "run already succeeded")}, nil
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

	return ResumeRunOutput{Message: renderRunMessage(reapReport, "run resumed")}, nil
}

func (applicationService) ReplayRun(_ context.Context, input ReplayRunInput) (ReplayRunOutput, error) {
	replay, err := loadReplayInput(input.EventsPath)
	if err != nil {
		return ReplayRunOutput{}, err
	}

	replayResult, err := runtime.Replay(replay.runID, replay.compiled, replay.events)
	if err != nil {
		return ReplayRunOutput{}, err
	}

	return ReplayRunOutput{View: runtime.BuildReplayView(replay.compiled, *replayResult)}, nil
}

func (s applicationService) CancelRun(ctx context.Context, input CancelRunInput) (CancelRunOutput, error) {
	session, err := s.runs.openExistingRunSession(input.StateDir, nil)
	if err != nil {
		return CancelRunOutput{}, err
	}

	reapReport, err := recoverCrashedRun(session)
	if err != nil {
		return CancelRunOutput{}, err
	}

	if err := session.engine.Cancel(ctx, ""); err != nil {
		return CancelRunOutput{}, err
	}

	return CancelRunOutput{Message: renderRunMessage(reapReport, "run canceled")}, nil
}

// recoverCrashedRun prepares a run for resume/cancel. Orphan reaping is
// unconditional defense in depth: no engine action may proceed while a stray
// provider process from an earlier attempt is still mutating the repository
// (resume would otherwise re-attach and run TWO agents concurrently). When
// the snapshot shows a crashed run (step still Running with no live engine),
// the reap outcome is additionally recorded through the engine's durable
// RecoverFromCrash transitions (StepInterrupted/StepRetried + RunPaused) so
// the recovery never bypasses the event log and the run becomes operable
// again.
func recoverCrashedRun(session existingRunSession) (provider.ReapReport, error) {
	report, err := provider.ReapOrphans(session.store.Layout().RunDir)
	if err != nil {
		return provider.ReapReport{}, err
	}

	if !session.engine.NeedsCrashRecovery() {
		return report, nil
	}

	if err := session.engine.RecoverFromCrash(crashEvidence(report)); err != nil {
		return provider.ReapReport{}, err
	}

	return report, nil
}

// crashEvidence summarizes the reap outcome for the durable recovery events.
func crashEvidence(report provider.ReapReport) string {
	if len(report.Reaped) == 0 {
		return ""
	}

	parts := make([]string, 0, len(report.Reaped))
	for _, orphan := range report.Reaped {
		parts = append(parts, fmt.Sprintf("terminated orphan provider process pid %d (step %s)", orphan.Record.PID, orphan.StepID))
	}

	return strings.Join(parts, "; ")
}

func renderRunMessage(report provider.ReapReport, final string) string {
	lines := make([]string, 0, len(report.Reaped)+1)
	for _, orphan := range report.Reaped {
		lines = append(lines, fmt.Sprintf("reaped orphan provider process pid %d (step %s)", orphan.Record.PID, orphan.StepID))
	}

	lines = append(lines, final)

	return strings.Join(lines, "\n")
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
