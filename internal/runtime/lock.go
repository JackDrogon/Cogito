package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JackDrogon/Cogito/internal/gitutil"
	"github.com/JackDrogon/Cogito/internal/store"
)

// DefaultRepoLocksRoot is the path relative to the repository root where
// per-repository lock files are stored when no explicit RepoLocksRoot is given.
const (
	DefaultRepoLocksRoot = ".cogito/locks"

	runtimeFileMode = 0o600
	runtimeDirMode  = 0o700

	repoLockFileName = "repo.lock.json"
)

var errRepoLockHeld = errors.New("repo lock already held")

// AcquireOptions carries the parameters needed to acquire a repository lock
// for one run. RepoLocksRoot and RunsRoot default to package-level constants
// when left empty.
type AcquireOptions struct {
	RunID         string
	RepoPath      string
	RunsRoot      string
	RepoLocksRoot string
	AllowDirty    bool
}

// LockMetadata is the JSON payload written to both the repo-level and
// run-level lock files. It records enough identity information to detect
// stale locks: a lock is considered stale when the owning process is no
// longer running on the same host.
type LockMetadata struct {
	RunID       string `json:"run_id"`
	RepoRoot    string `json:"repo_root"`
	PID         int    `json:"pid"`
	Hostname    string `json:"hostname"`
	AcquiredAt  string `json:"acquired_at"`
	UpdatedAt   string `json:"updated_at"`
	RunLockPath string `json:"run_lock_path"`
}

// Dependencies carries the injectable collaborators for RepoLockManager.
// All fields are optional: nil/zero values fall back to production defaults
// (time.Now, os.Getpid, os.Hostname, and a syscall-based liveness check).
type Dependencies struct {
	Now            func() time.Time
	PID            int
	Hostname       string
	ProcessRunning func(pid int) bool
}

// RepoLockManager creates and releases repository locks. It is safe to reuse
// across multiple Acquire calls, but each call must be paired with a Release
// on the returned RepoLock.
type RepoLockManager struct {
	now            func() time.Time
	pid            int
	hostname       string
	processRunning func(pid int) bool
}

// RepoLock represents an acquired repository lock. It holds the paths to both
// the repo-level and run-level lock files so Release can remove both atomically.
// A nil RepoLock is safe to call Release on.
type RepoLock struct {
	metadata     LockMetadata
	repoLockPath string
	runLockPath  string
}

// NewRepoLockManager constructs a RepoLockManager from the provided
// Dependencies, substituting production defaults for any nil/zero fields.
func NewRepoLockManager(deps Dependencies) *RepoLockManager {
	now := deps.Now
	if now == nil {
		now = time.Now
	}

	pid := deps.PID
	if pid == 0 {
		pid = os.Getpid()
	}

	hostname := strings.TrimSpace(deps.Hostname)
	if hostname == "" {
		hostname, _ = os.Hostname() //nolint:errcheck // fallback to empty string is acceptable
	}

	processRunning := deps.ProcessRunning
	if processRunning == nil {
		processRunning = isProcessRunning
	}

	return &RepoLockManager{
		now:            now,
		pid:            pid,
		hostname:       hostname,
		processRunning: processRunning,
	}
}

// Acquire takes the repository lock for one run in two phases: prepare
// (resolve the lock root, gate on a clean worktree, create lock directories)
// and commit (exclusively claim the repo lock, then mirror the metadata into
// the run's own lock file).
func (m *RepoLockManager) Acquire(ctx context.Context, opts AcquireOptions) (*RepoLock, error) {
	if strings.TrimSpace(opts.RunID) == "" {
		return nil, newError(ErrorCodePath, "run id is required")
	}

	prepared, err := m.prepareAcquire(ctx, opts)
	if err != nil {
		return nil, err
	}

	return m.commitAcquire(prepared)
}

// preparedLock carries the resolved paths and metadata between Acquire's
// prepare and commit phases.
type preparedLock struct {
	metadata     LockMetadata
	repoLockPath string
	runLockPath  string
}

// prepareAcquire resolves the lock root, enforces the clean-worktree gate for
// git repositories, ensures the lock directories exist, and assembles the
// lock metadata.
func (m *RepoLockManager) prepareAcquire(ctx context.Context, opts AcquireOptions) (preparedLock, error) {
	rootInfo, err := resolveRepoRoot(ctx, opts.RepoPath)
	if err != nil {
		return preparedLock{}, err
	}

	repoRoot := rootInfo.root

	// Worktree cleanliness is a git concept; in non-git mode (AgentLoop port)
	// there is nothing to check and the run proceeds with just the path lock.
	if rootInfo.isGit {
		if err := ensureCleanWorktree(ctx, repoRoot, opts.AllowDirty); err != nil {
			return preparedLock{}, err
		}
	}

	runsRoot := strings.TrimSpace(opts.RunsRoot)
	if runsRoot == "" {
		runsRoot = store.DefaultRunsRoot
	}

	repoLocksRoot := strings.TrimSpace(opts.RepoLocksRoot)
	if repoLocksRoot == "" {
		repoLocksRoot = DefaultRepoLocksRoot
	}

	layout := store.LayoutForRun(runsRoot, opts.RunID)
	if err := ensureDir(layout.LocksDir); err != nil {
		return preparedLock{}, err
	}

	if err := ensureDir(repoLocksRoot); err != nil {
		return preparedLock{}, err
	}

	now := m.now().UTC().Format(time.RFC3339Nano)
	runLockPath := filepath.Join(layout.LocksDir, repoLockFileName)

	return preparedLock{
		metadata: LockMetadata{
			RunID:       strings.TrimSpace(opts.RunID),
			RepoRoot:    repoRoot,
			PID:         m.pid,
			Hostname:    m.hostname,
			AcquiredAt:  now,
			UpdatedAt:   now,
			RunLockPath: runLockPath,
		},
		repoLockPath: filepath.Join(repoLocksRoot, repoLockFileNameForRepo(repoRoot)),
		runLockPath:  runLockPath,
	}, nil
}

// commitAcquire claims the repo lock exclusively and mirrors the metadata
// into the run lock file, rolling the repo lock back when the mirror fails.
func (m *RepoLockManager) commitAcquire(prepared preparedLock) (*RepoLock, error) {
	if err := m.acquireRepoLockFile(prepared.repoLockPath, prepared.metadata); err != nil {
		return nil, err
	}

	if err := writeAtomicJSON(prepared.runLockPath, prepared.metadata); err != nil {
		// Best-effort rollback of the repo lock we just took; if the rollback
		// itself fails the lock would linger until stale-lock reclaim, which
		// the operator should know about.
		if cleanupErr := removeFileIfMatches(prepared.repoLockPath, prepared.metadata); cleanupErr != nil {
			slog.Warn("lock: rollback of repo lock failed; will be reclaimed as stale",
				"path", prepared.repoLockPath, "err", cleanupErr)
		}

		return nil, err
	}

	return &RepoLock{
		metadata:     prepared.metadata,
		repoLockPath: prepared.repoLockPath,
		runLockPath:  prepared.runLockPath,
	}, nil
}

func (m *RepoLockManager) acquireRepoLockFile(repoLockPath string, metadata LockMetadata) error {
	for range 2 {
		if err := writeExclusiveJSON(repoLockPath, metadata); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrExist) {
			return wrapError(ErrorCodeLock, "acquire repo lock", err)
		}

		existing, err := readLockMetadata(repoLockPath)
		if err != nil {
			slog.Warn("lock: reclaiming unreadable repo lock", "path", repoLockPath, "err", err)

			if removeErr := removeStaleLock(repoLockPath, LockMetadata{}); removeErr != nil {
				return wrapError(ErrorCodeLock, "reclaim corrupt repo lock", removeErr)
			}

			continue
		}

		if !m.isStale(existing) {
			msg := fmt.Sprintf("repo lock already held for %s by run %s", existing.RepoRoot, existing.RunID)
			return wrapError(ErrorCodeLock, msg, errRepoLockHeld)
		}

		slog.Info("lock: reclaiming stale repo lock",
			"path", repoLockPath, "run", existing.RunID, "pid", existing.PID)

		if err := removeStaleLock(repoLockPath, existing); err != nil {
			return wrapError(ErrorCodeLock, "reclaim stale repo lock for run "+existing.RunID, err)
		}
	}

	return wrapError(ErrorCodeLock, "acquire repo lock", errRepoLockHeld)
}

func (m *RepoLockManager) isStale(metadata LockMetadata) bool {
	if !isValidLockMetadata(metadata) {
		return true
	}

	if m.hostname != "" && metadata.Hostname != "" && metadata.Hostname != m.hostname {
		return false
	}

	return !m.processRunning(metadata.PID)
}

// Release removes both the repo-level and run-level lock files if they still
// belong to this lock (matched by RunID, PID, and AcquiredAt). A nil receiver
// is a no-op. Callers should always defer Release immediately after a
// successful Acquire.
func (l *RepoLock) Release() error {
	if l == nil {
		return nil
	}

	if err := removeFileIfMatches(l.repoLockPath, l.metadata); err != nil {
		return err
	}

	if err := removeFileIfMatches(l.runLockPath, l.metadata); err != nil {
		return err
	}

	return nil
}

// Metadata returns the lock metadata recorded at acquisition time.
func (l *RepoLock) Metadata() LockMetadata {
	return l.metadata
}

// RepoLockPath returns the path to the shared repository-level lock file.
func (l *RepoLock) RepoLockPath() string {
	return l.repoLockPath
}

// RunLockPath returns the path to the run-scoped lock file mirrored inside
// the run's own state directory.
func (l *RepoLock) RunLockPath() string {
	return l.runLockPath
}

func ensureCleanWorktree(ctx context.Context, repoRoot string, allowDirty bool) error {
	if allowDirty {
		return nil
	}

	dirty, err := gitutil.GitOps{Root: repoRoot}.HasUncommittedChanges(ctx)
	if err != nil {
		return wrapError(ErrorCodeGit, "check worktree status for "+repoRoot, err)
	}

	if dirty {
		return newError(ErrorCodeDirtyWorktree, "dirty worktree detected for "+repoRoot)
	}

	return nil
}

// resolvedRepoRoot carries the resolved lock root plus whether it is a git work
// tree, so Acquire applies git-only gates (worktree cleanliness) selectively.
type resolvedRepoRoot struct {
	root  string
	isGit bool
}

// resolveRepoRoot resolves the lock root for repoPath. Inside a git work tree
// it locks on the repository top level so concurrent runs from different
// subdirectories contend on the same lock. When git cannot resolve a top level
// (most commonly: the directory is not a git repository), it degrades to
// non-git mode ported from AgentLoop instead of failing the run.
func resolveRepoRoot(ctx context.Context, repoPath string) (resolvedRepoRoot, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		repoPath = "."
	}

	repoRoot, err := gitutil.DiscoverToplevel(ctx, repoPath)
	if err != nil {
		return resolveNonGitRoot(repoPath, wrapError(ErrorCodeGit, "resolve repo toplevel for "+repoPath, err))
	}

	if repoRoot == "" {
		return resolvedRepoRoot{}, newError(ErrorCodeGit, "resolve repo root")
	}

	return resolvedRepoRoot{root: filepath.Clean(repoRoot), isGit: true}, nil
}

// resolveNonGitRoot is the non-git degradation path: a directory that is not a
// git repository can still host a run, with the absolute directory path acting
// as the lock root so concurrent runs in the same directory still contend on
// one lock. The original git error is surfaced only when repoPath is not a
// usable directory at all, keeping the diagnostic for genuinely broken --repo
// values (e.g. a path that does not exist).
func resolveNonGitRoot(repoPath string, gitErr error) (resolvedRepoRoot, error) {
	info, statErr := os.Stat(repoPath)
	if statErr != nil || !info.IsDir() {
		return resolvedRepoRoot{}, gitErr
	}

	abs, absErr := filepath.Abs(repoPath)
	if absErr != nil {
		return resolvedRepoRoot{}, wrapError(ErrorCodePath, "resolve repo path "+repoPath, absErr)
	}

	return resolvedRepoRoot{root: filepath.Clean(abs), isGit: false}, nil
}

func repoLockFileNameForRepo(repoRoot string) string {
	replacer := strings.NewReplacer(
		string(filepath.Separator), "-",
		":", "-",
		" ", "-",
	)

	sanitized := strings.Trim(replacer.Replace(filepath.Clean(repoRoot)), "-")
	if sanitized == "" {
		sanitized = "repo"
	}

	return sanitized + ".lock.json"
}

func isValidLockMetadata(metadata LockMetadata) bool {
	return strings.TrimSpace(metadata.RunID) != "" &&
		strings.TrimSpace(metadata.RepoRoot) != "" &&
		strings.TrimSpace(metadata.RunLockPath) != "" &&
		strings.TrimSpace(metadata.AcquiredAt) != "" &&
		strings.TrimSpace(metadata.UpdatedAt) != "" &&
		metadata.PID > 0
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, runtimeDirMode); err != nil {
		return wrapError(ErrorCodePermission, "create directory", err)
	}

	if err := os.Chmod(path, runtimeDirMode); err != nil {
		return wrapError(ErrorCodePermission, "set directory permissions", err)
	}

	return nil
}

// writeJSONFile marshals value and durably writes it (write + fsync + close)
// to path using O_WRONLY|O_CREATE plus extraFlags. Raw errors are returned so
// flag-specific sentinels stay matchable — acquireRepoLockFile relies on
// errors.Is(err, os.ErrExist) from the O_EXCL variant.
func writeJSONFile(path string, value any, extraFlags int) (err error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}

	data = append(data, '\n')

	file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|extraFlags, runtimeFileMode)
	if err != nil {
		return err
	}

	// closed guards the deferred close so the happy path's explicit Close is
	// not followed by a second close on an already-closed fd.
	closed := false

	defer func() {
		if closed {
			return
		}

		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	if _, err = file.Write(data); err != nil {
		return err
	}

	if syncErr := file.Sync(); syncErr != nil {
		return syncErr
	}

	// Mark closed before the explicit Close so the deferred close never
	// double-fires, even when this Close itself fails.
	closed = true

	if closeErr := file.Close(); closeErr != nil {
		return closeErr
	}

	return nil
}

// writeExclusiveJSON creates path with O_EXCL semantics: it fails with
// os.ErrExist when the lock file is already held.
func writeExclusiveJSON(path string, value any) error {
	if err := writeJSONFile(path, value, os.O_EXCL); err != nil {
		return err
	}

	return syncDir(filepath.Dir(path))
}

// writeAtomicJSON writes via temp file + rename so readers observe either the
// old or the complete new lock metadata, never a torn write.
func writeAtomicJSON(path string, value any) error {
	tempPath := path + ".tmp"

	if err := writeJSONFile(tempPath, value, os.O_TRUNC); err != nil {
		return wrapError(ErrorCodeLock, "write temp lock file "+tempPath, err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		return wrapError(ErrorCodeLock, "rename temp lock file", err)
	}

	return syncDir(filepath.Dir(path))
}

func readLockMetadata(path string) (LockMetadata, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return LockMetadata{}, err
	}

	var metadata LockMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return LockMetadata{}, err
	}

	return metadata, nil
}

func removeStaleLock(repoLockPath string, metadata LockMetadata) error {
	if err := os.Remove(repoLockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if strings.TrimSpace(metadata.RunLockPath) != "" {
		if err := os.Remove(metadata.RunLockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	return syncDir(filepath.Dir(repoLockPath))
}

func removeFileIfMatches(path string, expected LockMetadata) error {
	metadata, err := readLockMetadata(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return wrapError(ErrorCodeLock, "read lock metadata "+path, err)
	}

	if metadata.RunID != expected.RunID || metadata.PID != expected.PID || metadata.AcquiredAt != expected.AcquiredAt {
		return nil
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return wrapError(ErrorCodeLock, "remove lock file "+path, err)
	}

	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	dir, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer dir.Close()

	if err := dir.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}

	return nil
}

func isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}

	err := syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}
