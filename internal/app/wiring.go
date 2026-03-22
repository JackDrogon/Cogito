package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/JackDrogon/Cogito/internal/adapters"
	_ "github.com/JackDrogon/Cogito/internal/adapters/claude"
	_ "github.com/JackDrogon/Cogito/internal/adapters/codex"
	_ "github.com/JackDrogon/Cogito/internal/adapters/opencode"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type runtimeWiring struct {
	LookupAdapter runtime.AdapterLookup
	CommandRunner runtime.CommandRunner
	RepoPath      string
	WorkingDir    string
}

func buildRuntimeWiring(runStore *store.Store, flags *sharedFlags) (runtimeWiring, error) {
	if runStore == nil {
		return runtimeWiring{}, fmt.Errorf("buildRuntimeWiring: run store is required")
	}

	repoPath, workingDir, err := resolveExecutionContext(runStore, flags)
	if err != nil {
		return runtimeWiring{}, err
	}

	return runtimeWiring{
		LookupAdapter: lookupRegisteredAdapter,
		CommandRunner: newSupervisorCommandRunner(runStore, workingDir, providerTimeout(flags)),
		RepoPath:      repoPath,
		WorkingDir:    workingDir,
	}, nil
}

func lookupRegisteredAdapter(step workflow.CompiledStep) (adapters.Adapter, error) {
	if step.Agent == nil {
		return nil, fmt.Errorf("agent config missing for step %q", step.ID)
	}

	provider := strings.TrimSpace(step.Agent.Agent)
	if adapter, ok := lookupBuiltinLocalAdapter(provider); ok {
		return adapter, nil
	}

	registration, ok := adapters.Lookup(provider)
	if !ok {
		return nil, fmt.Errorf("adapter %q is not registered", provider)
	}

	return registration.New(), nil
}

func resolveExecutionContext(runStore *store.Store, flags *sharedFlags) (string, string, error) {
	if runStore == nil {
		return "", "", fmt.Errorf("resolveExecutionContext: run store is required")
	}

	if flags != nil && strings.TrimSpace(flags.repo) != "" {
		repoPath, err := filepath.Abs(filepath.Clean(flags.repo))
		if err != nil {
			return "", "", err
		}

		return repoPath, repoPath, nil
	}

	checkpoint, _, err := runStore.LoadCheckpoint()
	if err == nil && checkpoint != nil {
		repoPath := strings.TrimSpace(checkpoint.RepoPath)
		workingDir := strings.TrimSpace(checkpoint.WorkingDir)

		if repoPath == "" {
			repoPath = workingDir
		}

		if workingDir == "" {
			workingDir = repoPath
		}

		if repoPath != "" || workingDir != "" {
			return repoPath, workingDir, nil
		}
	}

	workingDir, err := filepath.Abs(".")
	if err != nil {
		return "", "", err
	}

	return workingDir, workingDir, nil
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
