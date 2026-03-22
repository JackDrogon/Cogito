package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

var errCheckpointNotFound = errors.New("checkpoint not found")

func (s *Store) SaveCheckpoint(checkpoint *Checkpoint) error {
	if checkpoint == nil {
		return newError(ErrorCodeCheckpoint, "checkpoint is required")
	}

	sanitized := sanitizeCheckpoint(checkpoint)
	sanitized.RunID = s.layout.RunID
	return writeAtomicJSON(s.layout.CheckpointPath, sanitized, ErrorCodeCheckpoint)
}

func (s *Store) LoadCheckpoint() (*Checkpoint, bool, error) {
	checkpoint, err := readCheckpointFile(s.layout.CheckpointPath)
	if err == nil {
		return checkpoint, false, nil
	}

	if !errors.Is(err, errCheckpointNotFound) {
		fallback, recovered, fallbackErr := s.tryRecoverCheckpoint(err)
		if fallbackErr == nil {
			return fallback, recovered, nil
		}

		return nil, false, fallbackErr
	}

	return s.tryRecoverCheckpoint(err)
}

func (s *Store) SaveArtifacts(artifacts []ArtifactRecord) error {
	if artifacts == nil {
		artifacts = []ArtifactRecord{}
	}

	sanitized, err := sanitizeArtifacts(s.layout.RunDir, artifacts)
	if err != nil {
		return err
	}

	return writeAtomicJSON(s.layout.ArtifactsPath, sanitized, ErrorCodeArtifacts)
}

func (s *Store) LoadArtifacts() ([]ArtifactRecord, error) {
	data, err := os.ReadFile(filepath.Clean(s.layout.ArtifactsPath))
	if err != nil {
		return nil, wrapError(ErrorCodeArtifacts, "read artifact index", err)
	}

	var artifacts []ArtifactRecord
	if err := json.Unmarshal(data, &artifacts); err != nil {
		return nil, wrapError(ErrorCodeArtifacts, "decode artifact index", err)
	}

	if artifacts == nil {
		return []ArtifactRecord{}, nil
	}

	return artifacts, nil
}

func (s *Store) tryRecoverCheckpoint(primaryErr error) (*Checkpoint, bool, error) {
	tempCheckpoint, tempErr := readCheckpointFile(s.layout.CheckpointTempPath)
	if tempErr == nil {
		if err := os.Rename(s.layout.CheckpointTempPath, s.layout.CheckpointPath); err != nil {
			return nil, false, wrapError(ErrorCodeCheckpoint, "recover checkpoint from temp file", err)
		}

		if err := syncDir(filepath.Dir(s.layout.CheckpointPath)); err != nil {
			return nil, false, wrapError(ErrorCodeCheckpoint, "sync checkpoint directory", err)
		}

		return tempCheckpoint, true, nil
	}

	if primaryErr != nil && !errors.Is(primaryErr, errCheckpointNotFound) {
		return nil, false, primaryErr
	}

	if errors.Is(tempErr, errCheckpointNotFound) {
		return nil, false, wrapError(ErrorCodeCheckpoint, "load checkpoint", errCheckpointNotFound)
	}

	return nil, false, tempErr
}

func readCheckpointFile(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errCheckpointNotFound
		}

		return nil, wrapError(ErrorCodeCheckpoint, "read checkpoint", err)
	}

	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, wrapError(ErrorCodeCheckpoint, "decode checkpoint", err)
	}

	return &checkpoint, nil
}
