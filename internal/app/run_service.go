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

type runEngineResult struct {
	engine *runtime.Engine
	wiring runtimeWiring
}

type newRunEngineInput struct {
	RunID          string
	Compiled       *workflow.CompiledWorkflow
	RunStore       *store.Store
	Flags          *sharedFlags
	ApprovalPolicy runtime.ApprovalPolicy
}

func (runService) newRunEngine(input newRunEngineInput) (*runEngineResult, error) {
	if input.RunStore == nil {
		return nil, errors.New("runService.newRunEngine: run store is required")
	}
	if input.Compiled == nil {
		return nil, errors.New("runService.newRunEngine: compiled workflow is required")
	}

	wiring, err := buildRuntimeWiring(input.RunStore, input.Flags)
	if err != nil {
		return nil, err
	}

	engine, err := runtime.NewEngine(input.RunID, input.Compiled, runtime.MachineDependencies{
		Store:          input.RunStore,
		ApprovalPolicy: input.ApprovalPolicy,
		LookupAdapter:  wiring.LookupAdapter,
		CommandRunner:  wiring.CommandRunner,
		RepoPath:       wiring.RepoPath,
		WorkingDir:     wiring.WorkingDir,
	})
	if err != nil {
		return nil, err
	}

	return &runEngineResult{engine: engine, wiring: wiring}, nil
}

func (s runService) openExistingRunSession(stateDir string, flags *sharedFlags) (existingRunSession, error) {
	runStoreResult, err := openExistingRunStore(stateDir)
	if err != nil {
		if isMissingRunStateError(err) {
			return existingRunSession{}, errors.New("run state not found: " + stateDir)
		}

		return existingRunSession{}, err
	}
	runStore := runStoreResult.store
	runID := runStoreResult.runID

	compiled, err := workflow.LoadResolvedFile(runStore.Layout().WorkflowPath)
	if err != nil {
		if isMissingRunStateError(err) {
			return existingRunSession{}, errors.New("run state not found: " + stateDir)
		}

		return existingRunSession{}, err
	}

	runEngine, err := s.newRunEngine(newRunEngineInput{
		RunID:    runID,
		Compiled: compiled,
		RunStore: runStore,
		Flags:    flags,
	})
	if err != nil {
		return existingRunSession{}, err
	}

	return existingRunSession{store: runStore, compiled: compiled, engine: runEngine.engine}, nil
}

func (runService) executeUntilSettled(ctx context.Context, engine *runtime.Engine) error {
	if engine == nil {
		return errors.New("runService.executeUntilSettled: engine is required")
	}

	return engine.ExecuteAll(ctx)
}
