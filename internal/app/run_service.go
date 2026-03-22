package app

import (
	"context"
	"errors"

	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type runService struct{}

type existingRunSession struct {
	store    *store.Store
	compiled *workflow.CompiledWorkflow
	engine   *runtime.Engine
}

func (runService) newRunEngine(runID string, compiled *workflow.CompiledWorkflow, runStore *store.Store, flags *sharedFlags, approvalPolicy runtime.ApprovalPolicy) (*runtime.Engine, runtimeWiring, error) {
	if runStore == nil {
		return nil, runtimeWiring{}, errors.New("runService.newRunEngine: run store is required")
	}
	if compiled == nil {
		return nil, runtimeWiring{}, errors.New("runService.newRunEngine: compiled workflow is required")
	}

	wiring, err := buildRuntimeWiring(runStore, flags)
	if err != nil {
		return nil, runtimeWiring{}, err
	}

	engine, err := runtime.NewEngine(runID, compiled, runtime.MachineDependencies{
		Store:          runStore,
		ApprovalPolicy: approvalPolicy,
		LookupAdapter:  wiring.LookupAdapter,
		CommandRunner:  wiring.CommandRunner,
		RepoPath:       wiring.RepoPath,
		WorkingDir:     wiring.WorkingDir,
	})
	if err != nil {
		return nil, runtimeWiring{}, err
	}

	return engine, wiring, nil
}

func (s runService) openExistingRunSession(stateDir string, flags *sharedFlags) (existingRunSession, error) {
	runStore, runID, err := openExistingRunStore(stateDir)
	if err != nil {
		if isMissingRunStateError(err) {
			return existingRunSession{}, errors.New("run state not found: " + stateDir)
		}

		return existingRunSession{}, err
	}

	compiled, err := workflow.LoadResolvedFile(runStore.Layout().WorkflowPath)
	if err != nil {
		if isMissingRunStateError(err) {
			return existingRunSession{}, errors.New("run state not found: " + stateDir)
		}

		return existingRunSession{}, err
	}

	engine, _, err := s.newRunEngine(runID, compiled, runStore, flags, nil)
	if err != nil {
		return existingRunSession{}, err
	}

	return existingRunSession{store: runStore, compiled: compiled, engine: engine}, nil
}

func (runService) executeUntilSettled(ctx context.Context, engine *runtime.Engine) error {
	if engine == nil {
		return errors.New("runService.executeUntilSettled: engine is required")
	}

	return engine.ExecuteAll(ctx)
}
