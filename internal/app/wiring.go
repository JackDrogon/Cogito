package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/JackDrogon/Cogito/internal/provider/claude"
	_ "github.com/JackDrogon/Cogito/internal/provider/codex"
	_ "github.com/JackDrogon/Cogito/internal/provider/opencode"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
)

// codexSandboxEnv mirrors AgentLoop's CODEX_SANDBOX override; empty falls back
// to the codex adapter's danger-full-access default.
const codexSandboxEnv = "CODEX_SANDBOX"

type runtimeWiring struct {
	LookupProvider runtime.ProviderLookup
	CommandRunner  runtime.CommandRunner
	RepoPath       string
	WorkingDir     string
}

type executionContext struct {
	repoPath   string
	workingDir string
}

func buildRuntimeWiring(runStore *store.Store, flags *sharedFlags) (runtimeWiring, error) {
	if runStore == nil {
		return runtimeWiring{}, errors.New("buildRuntimeWiring: run store is required")
	}

	execContext, err := resolveExecutionContext(runStore, flags)
	if err != nil {
		return runtimeWiring{}, err
	}

	return runtimeWiring{
		LookupProvider: defaultProviderLookup(providerOptionDefaults{
			Sandbox:    codexSandbox(),
			Model:      "",
			LogDirRoot: runStore.Layout().RunDir,
			LiveSink:   liveOutputSink(flags),
		}),
		CommandRunner: newSupervisorCommandRunner(runStore, execContext.workingDir, providerTimeout(flags)),
		RepoPath:      execContext.repoPath,
		WorkingDir:    execContext.workingDir,
	}, nil
}

// codexSandbox resolves the codex sandbox mode from the environment. An empty
// return means "unset": the codex adapter applies its own DefaultSandbox, so
// the app layer never needs a provider import beyond the wiring-time blank
// registration imports (see internal/app/AGENTS.md). Model is intentionally
// left empty: the workflow DSL has no per-step model field yet (L1 revisits).
func codexSandbox() string {
	return strings.TrimSpace(os.Getenv(codexSandboxEnv))
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

// liveOutputSink returns the console sink for streaming provider output in
// real time, or nil when -v is off. It mirrors AgentLoop's transparent output
// pump: raw provider bytes go to the terminal while the runner keeps writing
// the durable provider-logs copy. The writer is mutex-locked because the
// runner tees stdout and stderr from two goroutines.
//
// The sink targets os.Stdout directly rather than the command's stdout writer:
// live streaming is a console-only concern, and wiring is shared by every
// engine-executing command (run, resume, approve, feishu run, agents run).
func liveOutputSink(flags *sharedFlags) io.Writer {
	if flags == nil || !flags.verbose {
		return nil
	}

	return &lockedWriter{target: os.Stdout}
}

// lockedWriter serializes concurrent writes from the runner's stdout/stderr
// tee goroutines so interleaved chunks never split mid-write.
type lockedWriter struct {
	mu     sync.Mutex
	target io.Writer
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.target.Write(data)
}

func providerTimeout(flags *sharedFlags) time.Duration {
	if flags == nil {
		return 0
	}

	return flags.providerTimeout
}

// acquireRepoLockInput bundles the lock inputs so the function stays within
// the three-parameter rule with ctx threaded through.
type acquireRepoLockInput struct {
	flags    *sharedFlags
	runID    string
	runsRoot string
}

func acquireRepoLock(ctx context.Context, input acquireRepoLockInput) (*runtime.RepoLock, error) {
	manager := runtime.NewRepoLockManager(runtime.Dependencies{})
	flags, runID, runsRoot := input.flags, input.runID, input.runsRoot

	return manager.Acquire(ctx, runtime.AcquireOptions{
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
