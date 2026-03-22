package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JackDrogon/Cogito/internal/store"
)

const (
	DefaultRepoLocksRoot = "ref/tmp/locks"

	runtimeFileMode = 0o600
	runtimeDirMode  = 0o700

	repoLockFileName = "repo.lock.json"
)

var errRepoLockHeld = errors.New("repo lock already held")

type AcquireOptions struct {
	RunID         string
	RepoPath      string
	RunsRoot      string
	RepoLocksRoot string
	AllowDirty    bool
}

type LockMetadata struct {
	RunID       string `json:"run_id"`
	RepoRoot    string `json:"repo_root"`
	PID         int    `json:"pid"`
	Hostname    string `json:"hostname"`
	AcquiredAt  string `json:"acquired_at"`
	UpdatedAt   string `json:"updated_at"`
	RunLockPath string `json:"run_lock_path"`
}

type Dependencies struct {
	Now            func() time.Time
	PID            int
	Hostname       string
	ProcessRunning func(pid int) bool
}

type RepoLockManager struct {
	now            func() time.Time
	pid            int
	hostname       string
	processRunning func(pid int) bool
}

type RepoLock struct {
	metadata     LockMetadata
	repoLockPath string
	runLockPath  string
}

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

func (m *RepoLockManager) Acquire(opts AcquireOptions) (*RepoLock, error) {
	if strings.TrimSpace(opts.RunID) == "" {
		return nil, newError(ErrorCodePath, "run id is required")
	}

	repoRoot, err := resolveRepoRoot(opts.RepoPath)
	if err != nil {
		return nil, err
	}

	if err := ensureCleanWorktree(repoRoot, opts.AllowDirty); err != nil {
		return nil, err
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
		return nil, err
	}

	if err := ensureDir(repoLocksRoot); err != nil {
		return nil, err
	}

	now := m.now().UTC().Format(time.RFC3339Nano)
	runLockPath := filepath.Join(layout.LocksDir, repoLockFileName)
	metadata := LockMetadata{
		RunID:       strings.TrimSpace(opts.RunID),
		RepoRoot:    repoRoot,
		PID:         m.pid,
		Hostname:    m.hostname,
		AcquiredAt:  now,
		UpdatedAt:   now,
		RunLockPath: runLockPath,
	}

	repoLockPath := filepath.Join(repoLocksRoot, repoLockFileNameForRepo(repoRoot))
	if err := m.acquireRepoLockFile(repoLockPath, metadata); err != nil {
		return nil, err
	}

	if err := writeAtomicJSON(runLockPath, metadata); err != nil {
		_ = removeFileIfMatches(repoLockPath, metadata) //nolint:errcheck // best effort cleanup
		return nil, err
	}

	return &RepoLock{
		metadata:     metadata,
		repoLockPath: repoLockPath,
		runLockPath:  runLockPath,
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
			if removeErr := removeStaleLock(repoLockPath, LockMetadata{}); removeErr != nil {
				return wrapError(ErrorCodeLock, "reclaim corrupt repo lock", removeErr)
			}

			continue
		}

		if !m.isStale(existing) {
			msg := fmt.Sprintf("repo lock already held for %s by run %s", existing.RepoRoot, existing.RunID)
			return wrapError(ErrorCodeLock, msg, errRepoLockHeld)
		}

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

func (l *RepoLock) Metadata() LockMetadata {
	return l.metadata
}

func (l *RepoLock) RepoLockPath() string {
	return l.repoLockPath
}

func (l *RepoLock) RunLockPath() string {
	return l.runLockPath
}

func ensureCleanWorktree(repoRoot string, allowDirty bool) error {
	if allowDirty {
		return nil
	}

	output, err := runGit(repoRoot, "status", "--porcelain")
	if err != nil {
		return err
	}

	if strings.TrimSpace(output) != "" {
		return newError(ErrorCodeDirtyWorktree, "dirty worktree detected for "+repoRoot)
	}

	return nil
}

func resolveRepoRoot(repoPath string) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		repoPath = "."
	}

	output, err := runGit(repoPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}

	repoRoot := strings.TrimSpace(output)
	if repoRoot == "" {
		return "", newError(ErrorCodeGit, "resolve repo root")
	}

	return filepath.Clean(repoRoot), nil
}

func runGit(repoPath string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", cmdArgs...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}

		return "", wrapError(ErrorCodeGit, message, err)
	}

	return string(output), nil
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

func writeExclusiveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}

	data = append(data, '\n')

	file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, runtimeFileMode)
	if err != nil {
		return err
	}

	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}

	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	return syncDir(filepath.Dir(path))
}

func writeAtomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return wrapError(ErrorCodeLock, "marshal lock metadata", err)
	}

	data = append(data, '\n')
	tempPath := path + ".tmp"

	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, runtimeFileMode)
	if err != nil {
		return wrapError(ErrorCodeLock, "create temp lock file", err)
	}

	if _, err := file.Write(data); err != nil {
		file.Close()
		return wrapError(ErrorCodeLock, "write temp lock file", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		return wrapError(ErrorCodeLock, "sync temp lock file", err)
	}

	if err := file.Close(); err != nil {
		return wrapError(ErrorCodeLock, "close temp lock file", err)
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
