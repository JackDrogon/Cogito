package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters"
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
	ref, err := newRunStateRef(stateDir)
	if err != nil {
		return nil, "", err
	}

	runStore, err := store.OpenExisting(ref.baseDir, ref.runID)
	if err != nil {
		return nil, "", err
	}

	return runStore, ref.runID, nil
}

func loadReplayInput(eventsPath string) (string, *workflow.CompiledWorkflow, []store.Event, error) {
	request, err := newReplayRequest(eventsPath)
	if err != nil {
		return "", nil, nil, err
	}

	compiled, err := workflow.LoadResolvedFile(filepath.Join(request.runDir, "workflow.json"))
	if err != nil {
		return "", nil, nil, err
	}

	events, err := store.ReadEventsFile(request.eventsPath)
	if err != nil {
		return "", nil, nil, err
	}

	return request.runID, compiled, events, nil
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
