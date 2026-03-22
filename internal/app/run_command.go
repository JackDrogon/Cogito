package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

func executeWorkflowRun(ctx context.Context, workflowPath string, flags *sharedFlags, stdout io.Writer) (err error) {
	if flags == nil {
		return errors.New("executeWorkflowRun: flags are required")
	}

	approvalMode, err := runtime.ParseApprovalMode(flags.approval)
	if err != nil {
		return err
	}

	compiled, err := workflow.LoadFile(workflowPath)
	if err != nil {
		return err
	}

	baseDir, runID, err := parseRunStateDir(flags.stateDir)
	if err != nil {
		return err
	}

	repoLock, err := acquireRepoLock(flags, runID, baseDir)
	if err != nil {
		return err
	}

	defer func() {
		if releaseErr := repoLock.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()

	runStore, err := store.Open(baseDir, runID)
	if err != nil {
		return err
	}

	if err := workflow.SaveResolvedFile(runStore.Layout().WorkflowPath, compiled); err != nil {
		return err
	}

	wiring, err := buildRuntimeWiring(runStore, flags)
	if err != nil {
		return err
	}

	engine, err := runtime.NewEngine(runID, compiled, runtime.MachineDependencies{
		Store:          runStore,
		ApprovalPolicy: runtime.NewApprovalModePolicy(approvalMode),
		LookupAdapter:  wiring.LookupAdapter,
		CommandRunner:  wiring.CommandRunner,
		RepoPath:       wiring.RepoPath,
		WorkingDir:     wiring.WorkingDir,
	})
	if err != nil {
		return err
	}

	if err := engine.ExecuteAll(ctx); err != nil {
		return err
	}

	snapshot := engine.Snapshot()
	if snapshot.State == runtime.RunStateFailed {
		return latestRunFailure(runStore)
	}

	_, err = fmt.Fprintf(stdout, "run_id=%s\nstate_dir=%s\nstate=%s\n", runID, runStore.Layout().RunDir, snapshot.State)

	return err
}

func executeResumeRun(ctx context.Context, flags *sharedFlags, stdout io.Writer) error {
	runStore, _, engine, err := loadExistingRunEngine(flags.stateDir, flags)
	if err != nil {
		return err
	}

	if err := engine.Resume(""); err != nil {
		return err
	}

	if err := engine.ExecuteAll(ctx); err != nil {
		return err
	}

	if engine.Snapshot().State == runtime.RunStateFailed {
		return latestRunFailure(runStore)
	}

	_, err = fmt.Fprintln(stdout, "run resumed")

	return err
}

func executeReplay(eventsPath string, stdout io.Writer) error {
	runID, compiled, events, err := loadReplayInput(eventsPath)
	if err != nil {
		return err
	}

	if _, err := runtime.Replay(runID, compiled, events); err != nil {
		return err
	}

	_, err = fmt.Fprintln(stdout, "replay OK")

	return err
}

func executeCancelRun(ctx context.Context, stateDir string, stdout io.Writer) error {
	_, _, engine, err := loadExistingRunEngine(stateDir, nil)
	if err != nil {
		return err
	}

	if err := engine.Cancel(ctx, ""); err != nil {
		return err
	}

	_, err = fmt.Fprintln(stdout, "run canceled")

	return err
}

func executeApproveRun(ctx context.Context, flags *sharedFlags, stdout io.Writer) error {
	_, _, engine, err := loadExistingRunEngine(flags.stateDir, flags)
	if err != nil {
		return err
	}

	if err := engine.GrantApproval(ctx, "approved via CLI"); err != nil {
		return err
	}

	if err := engine.ExecuteAll(ctx); err != nil {
		return err
	}

	_, err = fmt.Fprintln(stdout, "approval granted")

	return err
}
