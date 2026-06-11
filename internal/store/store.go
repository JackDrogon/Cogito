package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store provides append-only event persistence and atomic checkpoint writes for a single run.
// All event appends are serialized under a mutex to maintain stable sequence numbers.
type Store struct {
	layout       Layout
	eventMu      sync.Mutex
	lastSequence int64
	// sequenceUnreliable poisons the store once a failed append left the
	// on-disk log in a state that could not even be read back: any further
	// append could create a sequence gap or duplicate, so they fail fast
	// instead. Recovery is reopening the store, which re-primes the counter
	// from the (repaired) file.
	sequenceUnreliable bool
}

// Run-layout entry names. Exported as the single vocabulary for the on-disk
// layout so other layers (app, tooling) never re-spell these literals.
const (
	WorkflowFileName    = "workflow.json"
	EventsFileName      = "events.jsonl"
	CheckpointFileName  = "checkpoint.json"
	ArtifactsFileName   = "artifacts.json"
	ProviderLogsDirName = "provider-logs"
	LocksDirName        = "locks"
)

// tempFileSuffix marks the scratch file used by atomic write-then-rename;
// Layout.CheckpointTempPath and writeAtomicJSON must agree on it so crash
// recovery can find the orphaned temp file.
const tempFileSuffix = ".tmp"

func LayoutForRun(baseDir, runID string) Layout {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		baseDir = DefaultRunsRoot
	}

	runID = strings.TrimSpace(runID)
	runDir := filepath.Join(baseDir, runID)

	return Layout{
		BaseDir:            baseDir,
		RunID:              runID,
		RunDir:             runDir,
		WorkflowPath:       filepath.Join(runDir, WorkflowFileName),
		EventsPath:         filepath.Join(runDir, EventsFileName),
		CheckpointPath:     filepath.Join(runDir, CheckpointFileName),
		CheckpointTempPath: filepath.Join(runDir, CheckpointFileName+tempFileSuffix),
		ArtifactsPath:      filepath.Join(runDir, ArtifactsFileName),
		LocksDir:           filepath.Join(runDir, LocksDirName),
	}
}

// Open opens (creating if necessary) the run layout for runID under baseDir
// and primes the sequence counter from the persisted event log.
func Open(baseDir, runID string) (*Store, error) {
	return openWithLayoutCheck(baseDir, runID, ensureLayout)
}

// OpenExisting opens the run layout for runID but fails when any expected
// layout entry is missing, so commands that resume or inspect a run never
// silently create an empty one.
func OpenExisting(baseDir, runID string) (*Store, error) {
	return openWithLayoutCheck(baseDir, runID, validateExistingLayout)
}

// openWithLayoutCheck is the shared open path: validate the run id, let
// checkLayout either create or verify the on-disk layout, then prime
// lastSequence from the persisted events (the file is the source of truth).
func openWithLayoutCheck(baseDir, runID string, checkLayout func(Layout) error) (*Store, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, newError(ErrorCodePath, "run id is required")
	}

	layout := LayoutForRun(baseDir, runID)
	if err := checkLayout(layout); err != nil {
		return nil, err
	}

	store := &Store{layout: layout}

	events, err := store.ReadEvents()
	if err != nil {
		return nil, err
	}

	if len(events) > 0 {
		store.lastSequence = events[len(events)-1].Sequence
	}

	return store, nil
}

func (s *Store) Layout() Layout {
	return s.layout
}

func ensureLayout(layout Layout) error {
	if err := ensureDir(layout.RunDir); err != nil {
		return err
	}

	if err := ensureDir(layout.LocksDir); err != nil {
		return err
	}

	if err := ensureFile(layout.EventsPath, nil); err != nil {
		return wrapError(ErrorCodeEventLog, "create events log", err)
	}

	if err := ensureFile(layout.ArtifactsPath, []byte("[]\n")); err != nil {
		return wrapError(ErrorCodeArtifacts, "create artifact index", err)
	}

	return nil
}

func validateExistingLayout(layout Layout) error {
	paths := []string{
		layout.RunDir,
		layout.LocksDir,
		layout.EventsPath,
		layout.ArtifactsPath,
		layout.CheckpointPath,
		layout.WorkflowPath,
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return wrapError(ErrorCodePath, "open existing run layout", err)
			}

			return wrapError(ErrorCodePermission, "stat run layout", err)
		}
	}

	return nil
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, persistedDirMode); err != nil {
		return wrapError(ErrorCodePermission, "create directory", err)
	}

	if err := os.Chmod(path, persistedDirMode); err != nil {
		return wrapError(ErrorCodePermission, "set directory permissions", err)
	}

	return nil
}

func ensureFile(path string, content []byte) error {
	file, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, persistedFileMode)
	if err != nil {
		return err
	}

	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	if info.Size() == 0 && len(content) > 0 {
		if _, err := file.Write(content); err != nil {
			return err
		}

		if err := file.Sync(); err != nil {
			return err
		}
	}

	if err := file.Chmod(persistedFileMode); err != nil {
		return err
	}

	return nil
}

func writeAtomicJSON(path string, value any, code ErrorCode) (err error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return wrapError(code, "marshal JSON", err)
	}

	data = append(data, '\n')
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tempPath := filepath.Join(dir, base+tempFileSuffix)

	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, persistedFileMode)
	if err != nil {
		return wrapError(code, "create temp file", err)
	}

	closed := false

	defer func() {
		if closed {
			return
		}

		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = wrapError(code, "close temp file", closeErr)
		}
	}()

	if _, err = file.Write(data); err != nil {
		return wrapError(code, "write temp file", err)
	}

	if err = file.Sync(); err != nil {
		return wrapError(code, "sync temp file", err)
	}

	if err = file.Chmod(persistedFileMode); err != nil {
		return wrapError(code, "set temp file permissions", err)
	}

	if err = file.Close(); err != nil {
		return wrapError(code, "close temp file", err)
	}

	closed = true

	if err := os.Rename(tempPath, path); err != nil {
		return wrapError(code, "rename temp file", err)
	}

	if err := syncDir(dir); err != nil {
		return wrapError(code, "sync directory", err)
	}

	return nil
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
