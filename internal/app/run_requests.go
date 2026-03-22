package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type runStateRef struct {
	stateDir string
	baseDir  string
	runID    string
}

type replayRequest struct {
	eventsPath string
	runDir     string
	runID      string
}

func newRunStateRef(stateDir string) (runStateRef, error) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return runStateRef{}, errors.New("parseRunStateDir: state dir is required")
	}

	runID := filepath.Base(stateDir)
	baseDir := filepath.Dir(stateDir)
	if runID == "." || runID == string(filepath.Separator) || strings.TrimSpace(runID) == "" {
		return runStateRef{}, fmt.Errorf("invalid state dir %q", stateDir)
	}

	return runStateRef{stateDir: stateDir, baseDir: baseDir, runID: runID}, nil
}

func newReplayRequest(eventsPath string) (replayRequest, error) {
	eventsPath = strings.TrimSpace(eventsPath)
	if eventsPath == "" {
		return replayRequest{}, errors.New("loadReplayInput: events file path is required")
	}

	runDir := filepath.Dir(eventsPath)
	runID := filepath.Base(runDir)
	if runID == "." || runID == string(filepath.Separator) || strings.TrimSpace(runID) == "" {
		return replayRequest{}, fmt.Errorf("invalid events file path %q", eventsPath)
	}

	return replayRequest{eventsPath: eventsPath, runDir: runDir, runID: runID}, nil
}
