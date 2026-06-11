package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// ErrCheckpointNotFound reports that neither the checkpoint file nor its
// recovery temp file exists. Exported so callers (the runtime engine) can
// distinguish the benign fresh-run case from real checkpoint corruption.
var ErrCheckpointNotFound = errors.New("checkpoint not found")

type CheckpointLoadResult struct {
	Checkpoint *Checkpoint
	Recovered  bool
}

func (s *Store) SaveCheckpoint(checkpoint *Checkpoint) error {
	if checkpoint == nil {
		return newError(ErrorCodeCheckpoint, "checkpoint is required")
	}

	sanitized := sanitizeCheckpoint(checkpoint)
	sanitized.RunID = s.layout.RunID

	return writeAtomicJSON(s.layout.CheckpointPath, sanitized, ErrorCodeCheckpoint)
}

// LoadCheckpoint reads the primary checkpoint file, falling back to the
// crash-recovery temp file when the primary is missing or corrupt.
func (s *Store) LoadCheckpoint() (*CheckpointLoadResult, error) {
	checkpoint, err := readCheckpointFile(s.layout.CheckpointPath)
	if err == nil {
		return &CheckpointLoadResult{Checkpoint: checkpoint}, nil
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

// tryRecoverCheckpoint promotes a complete temp checkpoint (left behind by a
// crash between write and rename) to the primary path.
//
// Error priority when the temp file does not help: a corrupt PRIMARY file
// outranks any temp-file error because it names the real problem the operator
// must fix; only when the primary was merely absent does the temp error (or
// the not-found sentinel) surface instead.
func (s *Store) tryRecoverCheckpoint(primaryErr error) (*CheckpointLoadResult, error) {
	tempCheckpoint, tempErr := readCheckpointFile(s.layout.CheckpointTempPath)
	if tempErr == nil {
		if err := os.Rename(s.layout.CheckpointTempPath, s.layout.CheckpointPath); err != nil {
			return nil, wrapError(ErrorCodeCheckpoint, "recover checkpoint from temp file", err)
		}

		if err := syncDir(filepath.Dir(s.layout.CheckpointPath)); err != nil {
			return nil, wrapError(ErrorCodeCheckpoint, "sync checkpoint directory", err)
		}

		return &CheckpointLoadResult{Checkpoint: tempCheckpoint, Recovered: true}, nil
	}

	if primaryErr != nil && !errors.Is(primaryErr, ErrCheckpointNotFound) {
		return nil, primaryErr
	}

	if errors.Is(tempErr, ErrCheckpointNotFound) {
		return nil, wrapError(ErrorCodeCheckpoint, "load checkpoint", ErrCheckpointNotFound)
	}

	return nil, tempErr
}

func readCheckpointFile(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrCheckpointNotFound
		}

		return nil, wrapError(ErrorCodeCheckpoint, "read checkpoint", err)
	}

	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, wrapError(ErrorCodeCheckpoint, "decode checkpoint", err)
	}

	return &checkpoint, nil
}
