package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// resolvedFileMode matches the store layer's persisted-file permissions so the
// resolved workflow is no more readable than the rest of the run state.
const resolvedFileMode = 0o600

// SaveResolvedFile persists the compiled workflow spec as pretty-printed JSON.
// The write is atomic (temp file + fsync + rename) so a crash mid-write can
// never leave a truncated workflow.json behind: readers observe either the
// previous content or the complete new content.
func SaveResolvedFile(path string, compiled *CompiledWorkflow) error {
	if compiled == nil || compiled.Spec == nil {
		return newError(ErrorCodeSchema, "compiled workflow is required")
	}

	data, err := json.MarshalIndent(compiled.Spec, "", "  ")
	if err != nil {
		return wrapError(ErrorCodeSchema, "marshal resolved workflow", err)
	}

	data = append(data, '\n')
	if err := writeFileAtomic(filepath.Clean(path), data); err != nil {
		return wrapError(ErrorCodePersist, "write resolved workflow", err)
	}

	return nil
}

// LoadResolvedFile reads a previously saved resolved workflow and re-compiles
// it into runtime-ready form.
func LoadResolvedFile(path string) (*CompiledWorkflow, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, wrapError(ErrorCodePersist, "read resolved workflow", err)
	}

	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, wrapError(ErrorCodeParse, "decode resolved workflow", err)
	}

	return CompileWorkflow(&spec)
}

// writeFileAtomic writes data to path via temp-file + fsync + rename in the
// same directory, mirroring the store layer's checkpoint durability story.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}

	tmpPath := tmp.Name()

	cleanup := func(cause error) error {
		closeErr := tmp.Close()
		removeErr := os.Remove(tmpPath)

		return errors.Join(cause, closeErr, removeErr)
	}

	if err := tmp.Chmod(resolvedFileMode); err != nil {
		return cleanup(err)
	}

	if _, err := tmp.Write(data); err != nil {
		return cleanup(err)
	}

	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}

	if err := tmp.Close(); err != nil {
		return errors.Join(err, os.Remove(tmpPath))
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return errors.Join(err, os.Remove(tmpPath))
	}

	return syncDir(dir)
}

// syncDir fsyncs a directory so a completed rename survives a crash. Some
// platforms reject directory fsync (os.ErrInvalid); treat that as success,
// mirroring the store layer's behavior.
func syncDir(path string) error {
	dir, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}

	defer func() { _ = dir.Close() }()

	if err := dir.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}

	return nil
}
