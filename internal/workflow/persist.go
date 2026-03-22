package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func SaveResolvedFile(path string, compiled *CompiledWorkflow) error {
	if compiled == nil || compiled.Spec == nil {
		return newError(ErrorCodeSchema, "compiled workflow is required")
	}

	data, err := json.MarshalIndent(compiled.Spec, "", "  ")
	if err != nil {
		return wrapError(ErrorCodeSchema, "marshal resolved workflow", err)
	}

	data = append(data, '\n')
	if err := os.WriteFile(filepath.Clean(path), data, 0o600); err != nil {
		return fmt.Errorf("write resolved workflow: %w", err)
	}

	return nil
}

func LoadResolvedFile(path string) (*CompiledWorkflow, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read resolved workflow: %w", err)
	}

	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, wrapError(ErrorCodeParse, "decode resolved workflow", err)
	}

	return CompileWorkflow(&spec)
}
