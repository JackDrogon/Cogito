package app

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/JackDrogon/Cogito/internal/adapters/claude"
	_ "github.com/JackDrogon/Cogito/internal/adapters/codex"
	_ "github.com/JackDrogon/Cogito/internal/adapters/opencode"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
)

// codexSandboxEnv mirrors AgentLoop's CODEX_SANDBOX override; empty falls back
// to the codex adapter's danger-full-access default.
const codexSandboxEnv = "CODEX_SANDBOX"

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
		LookupAdapter: defaultAdapterLookup(adapterOptionDefaults{
			Sandbox:    codexSandbox(),
			Model:      "",
			LogDirRoot: runStore.Layout().RunDir,
			LiveSink:   liveOutputSink(flags),
		}),
		CommandRunner: newSupervisorCommandRunner(runStore, context.workingDir, providerTimeout(flags)),
		RepoPath:      context.repoPath,
		WorkingDir:    context.workingDir,
	}, nil
}

// codexSandbox resolves the codex sandbox mode from the environment, falling
// back to the adapter default when CODEX_SANDBOX is unset. Model is intentionally
// left empty: the workflow DSL has no per-step model field yet (L1 revisits).
func codexSandbox() string {
	if value := strings.TrimSpace(os.Getenv(codexSandboxEnv)); value != "" {
		return value
	}

	return "danger-full-access"
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
