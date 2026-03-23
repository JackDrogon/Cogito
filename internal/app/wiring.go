package app

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/JackDrogon/Cogito/internal/adapters/claude"
	_ "github.com/JackDrogon/Cogito/internal/adapters/codex"
	_ "github.com/JackDrogon/Cogito/internal/adapters/opencode"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
)

type runtimeWiring struct {
	LookupAdapter runtime.AdapterLookup
	CommandRunner runtime.CommandRunner
	RepoPath      string
	WorkingDir    string
}

type executionContext struct {
	repoPath   string
	workingDir string
}

func buildRuntimeWiring(runStore *store.Store, flags *sharedFlags) (runtimeWiring, error) {
	if runStore == nil {
		return runtimeWiring{}, errors.New("buildRuntimeWiring: run store is required")
	}

	context, err := resolveExecutionContext(runStore, flags)
	if err != nil {
		return runtimeWiring{}, err
	}

	return runtimeWiring{
		LookupAdapter: defaultAdapterLookup(),
		CommandRunner: newSupervisorCommandRunner(runStore, context.workingDir, providerTimeout(flags)),
		RepoPath:      context.repoPath,
		WorkingDir:    context.workingDir,
	}, nil
}

func resolveExecutionContext(runStore *store.Store, flags *sharedFlags) (*executionContext, error) {
	if runStore == nil {
		return nil, errors.New("resolveExecutionContext: run store is required")
	}

	if flags != nil && strings.TrimSpace(flags.repo) != "" {
		repoPath, err := filepath.Abs(filepath.Clean(flags.repo))
		if err != nil {
			return nil, err
		}

		return &executionContext{repoPath: repoPath, workingDir: repoPath}, nil
	}

	checkpointResult, err := runStore.LoadCheckpoint()
	if err == nil && checkpointResult.Checkpoint != nil {
		checkpoint := checkpointResult.Checkpoint
		repoPath := strings.TrimSpace(checkpoint.RepoPath)
		workingDir := strings.TrimSpace(checkpoint.WorkingDir)

		if repoPath == "" {
			repoPath = workingDir
		}

		if workingDir == "" {
			workingDir = repoPath
		}

		if repoPath != "" || workingDir != "" {
			return &executionContext{repoPath: repoPath, workingDir: workingDir}, nil
		}
	}

	workingDir, err := filepath.Abs(".")
	if err != nil {
		return nil, err
	}

	return &executionContext{repoPath: workingDir, workingDir: workingDir}, nil
}

func providerTimeout(flags *sharedFlags) time.Duration {
	if flags == nil {
		return 0
	}

	return flags.providerTimeout
}

func acquireRepoLock(flags *sharedFlags, runID, runsRoot string) (*runtime.RepoLock, error) {
	manager := runtime.NewRepoLockManager(runtime.Dependencies{})

	return manager.Acquire(runtime.AcquireOptions{
		RunID:         runID,
		RepoPath:      repoPath(flags),
		RunsRoot:      runsRoot,
		RepoLocksRoot: repoLocksRoot(runsRoot),
		AllowDirty:    flags != nil && flags.allowDirty,
	})
}

func repoPath(flags *sharedFlags) string {
	if flags == nil || strings.TrimSpace(flags.repo) == "" {
		return "."
	}

	return flags.repo
}

func repoLocksRoot(runsRoot string) string {
	runsRoot = strings.TrimSpace(runsRoot)
	if runsRoot == "" {
		return runtime.DefaultRepoLocksRoot
	}

	return filepath.Join(filepath.Dir(runsRoot), "locks")
}
