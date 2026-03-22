package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

func latestRunFailure(runStore *store.Store) error {
	if runStore == nil {
		return errors.New("latestRunFailure: run store is required")
	}

	events, err := runStore.ReadEvents()
	if err != nil {
		return err
	}

	for index := len(events) - 1; index >= 0; index-- {
		message := strings.TrimSpace(events[index].Message)
		if message != "" {
			return fmt.Errorf("run failed: %s", message)
		}
	}

	return errors.New("latestRunFailure: run failed with no error message")
}

func openExistingRunStore(stateDir string) (*store.Store, string, error) {
	baseDir, runID, err := parseRunStateDir(stateDir)
	if err != nil {
		return nil, "", err
	}

	runStore, err := store.OpenExisting(baseDir, runID)
	if err != nil {
		return nil, "", err
	}

	return runStore, runID, nil
}

func parseRunStateDir(stateDir string) (string, string, error) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return "", "", errors.New("parseRunStateDir: state dir is required")
	}

	runID := filepath.Base(stateDir)
	baseDir := filepath.Dir(stateDir)

	if runID == "." || runID == string(filepath.Separator) || strings.TrimSpace(runID) == "" {
		return "", "", fmt.Errorf("invalid state dir %q", stateDir)
	}

	return baseDir, runID, nil
}

func loadRunStatus(stateDir string) (string, *workflow.CompiledWorkflow, runtime.Snapshot, error) {
	runStore, compiled, engine, err := loadExistingRunEngine(stateDir, nil)
	if err != nil {
		return "", nil, runtime.Snapshot{}, err
	}

	return runStore.Layout().RunDir, compiled, engine.Snapshot(), nil
}

func loadExistingRunEngine(stateDir string, flags *sharedFlags) (*store.Store, *workflow.CompiledWorkflow, *runtime.Engine, error) {
	runStore, runID, err := openExistingRunStore(stateDir)
	if err != nil {
		if isMissingRunStateError(err) {
			return nil, nil, nil, fmt.Errorf("run state not found: %s", stateDir)
		}

		return nil, nil, nil, err
	}

	compiled, err := workflow.LoadResolvedFile(runStore.Layout().WorkflowPath)
	if err != nil {
		if isMissingRunStateError(err) {
			return nil, nil, nil, fmt.Errorf("run state not found: %s", stateDir)
		}

		return nil, nil, nil, err
	}

	wiring, err := buildRuntimeWiring(runStore, flags)
	if err != nil {
		return nil, nil, nil, err
	}

	engine, err := runtime.NewEngine(runID, compiled, runtime.MachineDependencies{
		Store:         runStore,
		LookupAdapter: wiring.LookupAdapter,
		CommandRunner: wiring.CommandRunner,
		RepoPath:      wiring.RepoPath,
		WorkingDir:    wiring.WorkingDir,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	return runStore, compiled, engine, nil
}

func loadReplayInput(eventsPath string) (string, *workflow.CompiledWorkflow, []store.Event, error) {
	eventsPath = strings.TrimSpace(eventsPath)
	if eventsPath == "" {
		return "", nil, nil, errors.New("loadReplayInput: events file path is required")
	}

	runDir := filepath.Dir(eventsPath)

	runID := filepath.Base(runDir)
	if runID == "." || runID == string(filepath.Separator) || strings.TrimSpace(runID) == "" {
		return "", nil, nil, fmt.Errorf("invalid events file path %q", eventsPath)
	}

	compiled, err := workflow.LoadResolvedFile(filepath.Join(runDir, "workflow.json"))
	if err != nil {
		return "", nil, nil, err
	}

	events, err := store.ReadEventsFile(eventsPath)
	if err != nil {
		return "", nil, nil, err
	}

	return runID, compiled, events, nil
}

func isMissingRunStateError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, os.ErrNotExist) {
		return true
	}

	var storeErr *store.Error

	return errors.As(err, &storeErr) && storeErr.Code == store.ErrorCodePath && errors.Is(storeErr.Err, os.ErrNotExist)
}

func formatStepSummaries(compiled *workflow.CompiledWorkflow, snapshot runtime.Snapshot) []string {
	if compiled == nil {
		return nil
	}

	lines := make([]string, 0, len(compiled.TopologicalOrder))

	for _, stepID := range compiled.TopologicalOrder {
		step := snapshot.Steps[stepID]
		line := fmt.Sprintf("step=%s state=%s", stepID, step.State)

		if summary := strings.TrimSpace(step.Summary); summary != "" {
			line += fmt.Sprintf(" summary=%q", summary)
		}

		lines = append(lines, line)
	}

	return lines
}

func executionFromStepResult(result *adapters.StepResult) *adapters.Execution {
	if result == nil {
		return nil
	}

	return &adapters.Execution{
		Handle:           result.Handle,
		State:            result.Status,
		Summary:          result.Summary,
		OutputText:       result.OutputText,
		StructuredOutput: result.StructuredOutput,
		ArtifactRefs:     result.ArtifactRefs,
		Logs:             result.Logs,
	}
}

func cloneExecution(execution *adapters.Execution) *adapters.Execution {
	if execution == nil {
		return nil
	}

	cloned := *execution
	cloned.ArtifactRefs = append([]adapters.ArtifactRef(nil), execution.ArtifactRefs...)
	cloned.Logs = append([]adapters.LogEntry(nil), execution.Logs...)

	return &cloned
}
