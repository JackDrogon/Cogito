package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Store struct {
	layout       Layout
	eventMu      sync.Mutex
	lastSequence int64
}

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
		WorkflowPath:       filepath.Join(runDir, "workflow.json"),
		EventsPath:         filepath.Join(runDir, "events.jsonl"),
		CheckpointPath:     filepath.Join(runDir, "checkpoint.json"),
		CheckpointTempPath: filepath.Join(runDir, "checkpoint.json.tmp"),
		ArtifactsPath:      filepath.Join(runDir, "artifacts.json"),
		LocksDir:           filepath.Join(runDir, "locks"),
	}
}

func Open(baseDir, runID string) (*Store, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, newError(ErrorCodePath, "run id is required")
	}

	layout := LayoutForRun(baseDir, runID)
	if err := ensureLayout(layout); err != nil {
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

func OpenExisting(baseDir, runID string) (*Store, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, newError(ErrorCodePath, "run id is required")
	}

	layout := LayoutForRun(baseDir, runID)
	if err := validateExistingLayout(layout); err != nil {
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
	for _, path := range []string{layout.RunDir, layout.LocksDir, layout.EventsPath, layout.ArtifactsPath, layout.CheckpointPath, layout.WorkflowPath} {
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

func writeAtomicJSON(path string, value any, code ErrorCode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return wrapError(code, "marshal JSON", err)
	}

	data = append(data, '\n')
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tempPath := filepath.Join(dir, base+".tmp")

	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, persistedFileMode)
	if err != nil {
		return wrapError(code, "create temp file", err)
	}

	if _, err := file.Write(data); err != nil {
		file.Close()
		return wrapError(code, "write temp file", err)
	}

	if err := file.Sync(); err != nil {
		file.Close()
		return wrapError(code, "sync temp file", err)
	}

	if err := file.Chmod(persistedFileMode); err != nil {
		file.Close()
		return wrapError(code, "set temp file permissions", err)
	}

	if err := file.Close(); err != nil {
		return wrapError(code, "close temp file", err)
	}

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
