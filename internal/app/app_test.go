package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/version"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

func TestRun(t *testing.T) {
	t.Run("version output", func(t *testing.T) {
		oldVersion := version.Version
		version.Version = "v1.2.3"
		defer func() {
			version.Version = oldVersion
		}()

		var out bytes.Buffer
		if err := Run(t.Context(), []string{"--version"}, &out); err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if out.String() != "v1.2.3\n" {
			t.Fatalf("Run() output = %q", out.String())
		}
	})

	t.Run("unexpected args", func(t *testing.T) {
		var out bytes.Buffer
		err := Run(t.Context(), []string{"extra"}, &out)
		if err == nil {
			t.Fatal("Run() expected error, got nil")
		}
		if !strings.Contains(err.Error(), "unknown subcommand") {
			t.Fatalf("Run() error = %v", err)
		}
	})

	t.Run("workflow validate", func(t *testing.T) {
		var out bytes.Buffer
		path := filepath.Join("..", "workflow", "testdata", "simple.yaml")

		if err := Run(t.Context(), []string{"workflow", "validate", path}, &out); err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if out.String() != "workflow valid\n" {
			t.Fatalf("Run() output = %q", out.String())
		}
	})

	t.Run("workflow validate rejects invalid workflow", func(t *testing.T) {
		var out bytes.Buffer
		path := filepath.Join("..", "workflow", "testdata", "unsupported-kind.yaml")

		err := Run(t.Context(), []string{"workflow", "validate", path}, &out)
		if err == nil {
			t.Fatal("Run() error = nil, want workflow validation failure")
		}

		if !strings.Contains(err.Error(), "unsupported step kind") {
			t.Fatalf("Run() error = %v, want contains unsupported step kind", err)
		}

		if out.Len() != 0 {
			t.Fatalf("Run() output = %q, want empty on validation failure", out.String())
		}
	})
}

func TestSubcommandRouting(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name: "workflow validate help",
			args: []string{"workflow", "validate", "--help"},
		},
		{
			name:    "status shared flags with state dir",
			args:    []string{"status", "unexpected"},
			wantErr: "status does not accept positional arguments",
		},
		{
			name:    "run rejects invalid approval mode",
			args:    []string{"run", "--approval", "manual", filepath.Join("..", "workflow", "testdata", "simple.yaml")},
			wantErr: "unsupported approval mode",
		},
		{
			name:    "unknown top-level subcommand",
			args:    []string{"does-not-exist"},
			wantErr: "unknown subcommand",
		},
		{
			name:    "unknown workflow subcommand",
			args:    []string{"workflow", "does-not-exist"},
			wantErr: "unknown workflow subcommand",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := Run(t.Context(), tc.args, &out)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("Run() expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Run() error = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseSharedFlagsWithoutArgsRejectsPositionals(t *testing.T) {
	var out bytes.Buffer
	flags, err := parseSharedFlagsWithoutArgs("status", []string{"unexpected"}, &out)
	if err == nil {
		t.Fatal("parseSharedFlagsWithoutArgs() error = nil, want positional-argument rejection")
	}
	if flags != nil {
		t.Fatalf("parseSharedFlagsWithoutArgs() flags = %#v, want nil", flags)
	}
	if !strings.Contains(err.Error(), "status does not accept positional arguments") {
		t.Fatalf("parseSharedFlagsWithoutArgs() error = %v", err)
	}
}

func TestRequireExactlyOneArgUsesLabel(t *testing.T) {
	arg, err := requireExactlyOneArg("replay", "events file", []string{"one", "two"})
	if err == nil {
		t.Fatal("requireExactlyOneArg() error = nil, want too-many-args failure")
	}
	if arg != "" {
		t.Fatalf("requireExactlyOneArg() arg = %q, want empty", arg)
	}
	if !strings.Contains(err.Error(), "replay: expects exactly 1 events file argument") {
		t.Fatalf("requireExactlyOneArg() error = %v", err)
	}
}

func TestParseStatusRequestUsesStateDir(t *testing.T) {
	request, err := parseStatusRequest([]string{"--state-dir", filepath.Join("ref", "tmp", "runs", "run-1")}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseStatusRequest() error = %v", err)
	}
	if request == nil {
		t.Fatal("parseStatusRequest() request = nil")
	}
	if !strings.Contains(request.StateDir, "run-1") {
		t.Fatalf("request.StateDir = %q, want contains run-1", request.StateDir)
	}
}

func TestParseReplayRequestRequiresEventsPath(t *testing.T) {
	request, err := parseReplayRequest([]string{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseReplayRequest() error = nil, want missing events file")
	}
	if request != nil {
		t.Fatalf("parseReplayRequest() request = %#v, want nil", request)
	}
	if !strings.Contains(err.Error(), "replay: expects exactly 1 events file argument") {
		t.Fatalf("parseReplayRequest() error = %v", err)
	}
}

func TestFormatRunStatusIncludesStepLines(t *testing.T) {
	output := renderStatusView("ref/tmp/runs/run-1", runtime.RunStatusView{
		RunID: "run-1",
		State: runtime.RunStateSucceeded,
		StepViews: []runtime.StepStatusView{
			{StepID: "prepare", State: runtime.StepStateSucceeded},
			{StepID: "review", State: runtime.StepStateSucceeded},
		},
	})

	for _, want := range []string{
		"run_id=run-1",
		"state_dir=ref/tmp/runs/run-1",
		"state=succeeded",
		"step=prepare state=succeeded",
		"step=review state=succeeded",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("formatRunStatus() = %q, want contains %q", output, want)
		}
	}
}

func TestTextPresenterRendersRunAndMessages(t *testing.T) {
	var runOut bytes.Buffer
	if err := presenter.PresentRunWorkflow(&runOut, RunWorkflowOutput{RunID: "run-1", StateDir: "ref/tmp/runs/run-1", State: runtime.RunStateSucceeded}); err != nil {
		t.Fatalf("PresentRunWorkflow() error = %v", err)
	}
	for _, want := range []string{"run_id=run-1", "state_dir=ref/tmp/runs/run-1", "state=succeeded"} {
		if !strings.Contains(runOut.String(), want) {
			t.Fatalf("PresentRunWorkflow() output = %q, want contains %q", runOut.String(), want)
		}
	}

	var msgOut bytes.Buffer
	if err := presenter.PresentMessage(&msgOut, "approval granted"); err != nil {
		t.Fatalf("PresentMessage() error = %v", err)
	}
	if msgOut.String() != "approval granted\n" {
		t.Fatalf("PresentMessage() output = %q", msgOut.String())
	}

	var replayOut bytes.Buffer
	if err := presenter.PresentReplayRun(&replayOut, ReplayRunOutput{View: runtime.ReplayView{RunID: "run-1", State: runtime.RunStateSucceeded, Transitions: []runtime.TransitionView{{Sequence: 1}}}}); err != nil {
		t.Fatalf("PresentReplayRun() error = %v", err)
	}
	for _, want := range []string{"replay OK", "run_id=run-1", "state=succeeded", "transitions=1"} {
		if !strings.Contains(replayOut.String(), want) {
			t.Fatalf("PresentReplayRun() output = %q, want contains %q", replayOut.String(), want)
		}
	}
}

func TestRunSharedFlagsOnIsolatedRepo(t *testing.T) {
	fixture := newAppRepoFixture(t)
	workflowPath := writeWorkflowFile(t, fixture.repoDir, "simple.yaml", "printf 'hello\\n'")
	stateDir := filepath.Join(fixture.runsRoot, "run-shared-flags")

	var out bytes.Buffer
	err := Run(t.Context(), []string{"run", "--repo", fixture.repoDir, "--approval", "auto", "--provider-timeout", "30s", "--state-dir", stateDir, workflowPath}, &out)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := out.String()
	for _, want := range []string{"run_id=run-shared-flags", "state_dir=" + stateDir, "state=succeeded"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output = %q, want contains %q", output, want)
		}
	}
}

func TestRunCommandHonorsApprovalDenyAfterWorkflowPath(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "run-approval")
	workflowPath := filepath.Join("..", "workflow", "testdata", "approval.yaml")

	for attempt := 1; attempt <= 2; attempt++ {
		var out bytes.Buffer
		err := Run(t.Context(), []string{"run", workflowPath, "--state-dir", stateDir, "--approval=deny", "--allow-dirty"}, &out)
		if err == nil {
			t.Fatalf("Run() attempt %d error = nil, want approval denied", attempt)
		}
		if !strings.Contains(err.Error(), "approval denied") {
			t.Fatalf("Run() attempt %d error = %v, want contains approval denied", attempt, err)
		}
		if out.Len() != 0 {
			t.Fatalf("Run() attempt %d output = %q, want empty output on error", attempt, out.String())
		}
	}
}

func TestRunAndStatusUsePersistedWorkflowAndCheckpoint(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "run-simple")
	workflowPath := filepath.Join("..", "workflow", "testdata", "simple.yaml")

	var runOut bytes.Buffer
	if err := Run(t.Context(), []string{"run", workflowPath, "--state-dir", stateDir, "--approval=auto", "--allow-dirty"}, &runOut); err != nil {
		t.Fatalf("Run(run) error = %v", err)
	}

	runOutput := runOut.String()
	for _, want := range []string{"run_id=run-simple", "state_dir=" + stateDir, "state=succeeded"} {
		if !strings.Contains(runOutput, want) {
			t.Fatalf("Run(run) output = %q, want contains %q", runOutput, want)
		}
	}

	runStore, err := store.OpenExisting(filepath.Dir(stateDir), filepath.Base(stateDir))
	if err != nil {
		t.Fatalf("store.OpenExisting() error = %v", err)
	}

	checkpointBefore, err := os.ReadFile(runStore.Layout().CheckpointPath)
	if err != nil {
		t.Fatalf("ReadFile(checkpoint) error = %v", err)
	}

	eventsBefore, err := os.ReadFile(runStore.Layout().EventsPath)
	if err != nil {
		t.Fatalf("ReadFile(events) error = %v", err)
	}

	workflowBefore, err := os.ReadFile(runStore.Layout().WorkflowPath)
	if err != nil {
		t.Fatalf("ReadFile(workflow) error = %v", err)
	}

	var statusOut bytes.Buffer
	if err := Run(t.Context(), []string{"status", "--state-dir", stateDir}, &statusOut); err != nil {
		t.Fatalf("Run(status) error = %v", err)
	}

	statusOutput := statusOut.String()
	for _, want := range []string{
		"run_id=run-simple",
		"state_dir=" + stateDir,
		"state=succeeded",
		"step=prepare state=succeeded",
		"step=review state=succeeded",
		"step=notify state=succeeded",
	} {
		if !strings.Contains(statusOutput, want) {
			t.Fatalf("Run(status) output = %q, want contains %q", statusOutput, want)
		}
	}

	checkpointAfter, err := os.ReadFile(runStore.Layout().CheckpointPath)
	if err != nil {
		t.Fatalf("ReadFile(checkpoint after status) error = %v", err)
	}

	eventsAfter, err := os.ReadFile(runStore.Layout().EventsPath)
	if err != nil {
		t.Fatalf("ReadFile(events after status) error = %v", err)
	}

	workflowAfter, err := os.ReadFile(runStore.Layout().WorkflowPath)
	if err != nil {
		t.Fatalf("ReadFile(workflow after status) error = %v", err)
	}

	if !bytes.Equal(checkpointBefore, checkpointAfter) {
		t.Fatalf("checkpoint changed after status\nbefore=%q\nafter=%q", string(checkpointBefore), string(checkpointAfter))
	}

	if !bytes.Equal(eventsBefore, eventsAfter) {
		t.Fatalf("events changed after status\nbefore=%q\nafter=%q", string(eventsBefore), string(eventsAfter))
	}

	if !bytes.Equal(workflowBefore, workflowAfter) {
		t.Fatalf("workflow changed after status\nbefore=%q\nafter=%q", string(workflowBefore), string(workflowAfter))
	}
}

func TestStatusMissingRunStateReturnsCleanError(t *testing.T) {
	var out bytes.Buffer
	err := Run(t.Context(), []string{"status", "--state-dir", filepath.Join("ref", "tmp", "does-not-exist")}, &out)
	if err == nil {
		t.Fatal("Run(status missing) error = nil, want error")
	}

	if !strings.Contains(err.Error(), "run state not found") {
		t.Fatalf("Run(status missing) error = %v, want contains run state not found", err)
	}

	if out.Len() != 0 {
		t.Fatalf("Run(status missing) output = %q, want empty", out.String())
	}
}

func TestResumeCommandResumesPausedRunAndRejectsDuplicate(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "run-paused")
	writePausedRunState(t, stateDir)

	var out bytes.Buffer
	if err := Run(t.Context(), []string{"resume", "--state-dir", stateDir}, &out); err != nil {
		t.Fatalf("Run(resume) error = %v", err)
	}
	if out.String() != "run resumed\n" {
		t.Fatalf("Run(resume) output = %q, want %q", out.String(), "run resumed\n")
	}

	status, err := appsvc.StatusRun(t.Context(), StatusRunInput{StateDir: stateDir})
	if err != nil {
		t.Fatalf("StatusRun() after resume error = %v", err)
	}
	snapshotState := status.View.State
	if snapshotState != runtime.RunStateSucceeded {
		t.Fatalf("status.State after resume = %q, want %q", snapshotState, runtime.RunStateSucceeded)
	}

	var dupOut bytes.Buffer
	err = Run(t.Context(), []string{"resume", "--state-dir", stateDir}, &dupOut)
	if err == nil {
		t.Fatal("Run(resume duplicate) error = nil, want invalid resume state")
	}
	if !strings.Contains(err.Error(), "cannot resume run from") {
		t.Fatalf("Run(resume duplicate) error = %v, want contains cannot resume run from", err)
	}
	if dupOut.Len() != 0 {
		t.Fatalf("Run(resume duplicate) output = %q, want empty", dupOut.String())
	}
}

func TestReplayCommandDoesNotMutateRunState(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "run-replay")
	workflowPath := filepath.Join("..", "workflow", "testdata", "simple.yaml")

	var runOut bytes.Buffer
	if err := Run(t.Context(), []string{"run", workflowPath, "--state-dir", stateDir, "--approval=auto", "--allow-dirty"}, &runOut); err != nil {
		t.Fatalf("Run(run) error = %v", err)
	}

	runStore, err := store.OpenExisting(filepath.Dir(stateDir), filepath.Base(stateDir))
	if err != nil {
		t.Fatalf("store.OpenExisting() error = %v", err)
	}

	checkpointBefore, err := os.ReadFile(runStore.Layout().CheckpointPath)
	if err != nil {
		t.Fatalf("ReadFile(checkpoint) error = %v", err)
	}
	eventsBefore, err := os.ReadFile(runStore.Layout().EventsPath)
	if err != nil {
		t.Fatalf("ReadFile(events) error = %v", err)
	}
	workflowBefore, err := os.ReadFile(runStore.Layout().WorkflowPath)
	if err != nil {
		t.Fatalf("ReadFile(workflow) error = %v", err)
	}

	var out bytes.Buffer
	if err := Run(t.Context(), []string{"replay", runStore.Layout().EventsPath}, &out); err != nil {
		t.Fatalf("Run(replay) error = %v", err)
	}
	for _, want := range []string{"replay OK", "run_id=run-replay", "state=succeeded", "transitions="} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("Run(replay) output = %q, want contains %q", out.String(), want)
		}
	}

	checkpointAfter, err := os.ReadFile(runStore.Layout().CheckpointPath)
	if err != nil {
		t.Fatalf("ReadFile(checkpoint after replay) error = %v", err)
	}
	eventsAfter, err := os.ReadFile(runStore.Layout().EventsPath)
	if err != nil {
		t.Fatalf("ReadFile(events after replay) error = %v", err)
	}
	workflowAfter, err := os.ReadFile(runStore.Layout().WorkflowPath)
	if err != nil {
		t.Fatalf("ReadFile(workflow after replay) error = %v", err)
	}

	if !bytes.Equal(checkpointBefore, checkpointAfter) {
		t.Fatalf("checkpoint changed after replay\nbefore=%q\nafter=%q", string(checkpointBefore), string(checkpointAfter))
	}
	if !bytes.Equal(eventsBefore, eventsAfter) {
		t.Fatalf("events changed after replay\nbefore=%q\nafter=%q", string(eventsBefore), string(eventsAfter))
	}
	if !bytes.Equal(workflowBefore, workflowAfter) {
		t.Fatalf("workflow changed after replay\nbefore=%q\nafter=%q", string(workflowBefore), string(workflowAfter))
	}
}

func TestCancelCommandCancelsPausedRunAndRejectsDuplicate(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "run-cancel")
	writePausedRunState(t, stateDir)

	var out bytes.Buffer
	if err := Run(t.Context(), []string{"cancel", "--state-dir", stateDir}, &out); err != nil {
		t.Fatalf("Run(cancel) error = %v", err)
	}
	if out.String() != "run canceled\n" {
		t.Fatalf("Run(cancel) output = %q, want %q", out.String(), "run canceled\n")
	}

	status, err := appsvc.StatusRun(t.Context(), StatusRunInput{StateDir: stateDir})
	if err != nil {
		t.Fatalf("StatusRun() after cancel error = %v", err)
	}
	if status.View.State != runtime.RunStateCanceled {
		t.Fatalf("status.State after cancel = %q, want %q", status.View.State, runtime.RunStateCanceled)
	}

	var dupOut bytes.Buffer
	err = Run(t.Context(), []string{"cancel", "--state-dir", stateDir}, &dupOut)
	if err == nil {
		t.Fatal("Run(cancel duplicate) error = nil, want invalid cancel state")
	}
	if !strings.Contains(err.Error(), "cannot cancel run from") {
		t.Fatalf("Run(cancel duplicate) error = %v, want contains cannot cancel run from", err)
	}
	if dupOut.Len() != 0 {
		t.Fatalf("Run(cancel duplicate) output = %q, want empty", dupOut.String())
	}
}

func TestResumeCommandUsesPersistedWorkingDir(t *testing.T) {
	baseDir := t.TempDir()
	repoDir := filepath.Join(baseDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(repo) error = %v", err)
	}

	stateDir := filepath.Join(baseDir, "runs", "run-working-dir")
	writePausedCommandRunState(t, stateDir, repoDir, "pwd")

	otherDir := filepath.Join(baseDir, "other")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(other) error = %v", err)
	}
	t.Chdir(otherDir)

	var out bytes.Buffer
	if err := Run(t.Context(), []string{"resume", "--state-dir", stateDir}, &out); err != nil {
		t.Fatalf("Run(resume persisted working dir) error = %v", err)
	}

	runStore, err := store.OpenExisting(filepath.Dir(stateDir), filepath.Base(stateDir))
	if err != nil {
		t.Fatalf("store.OpenExisting() error = %v", err)
	}
	stdoutPath := filepath.Join(runStore.Layout().RunDir, providerLogsDir, "prepare", "attempt-prepare-01-stdout.log")
	stdout, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("ReadFile(stdout log) error = %v", err)
	}
	if strings.TrimSpace(string(stdout)) != repoDir {
		t.Fatalf("stdout log = %q, want %q", strings.TrimSpace(string(stdout)), repoDir)
	}
}

func writePausedRunState(t *testing.T, stateDir string) {
	t.Helper()

	compiled, err := workflow.LoadFile(filepath.Join("..", "workflow", "testdata", "simple.yaml"))
	if err != nil {
		t.Fatalf("workflow.LoadFile() error = %v", err)
	}

	runStore, err := store.Open(filepath.Dir(stateDir), filepath.Base(stateDir))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	if err := workflow.SaveResolvedFile(runStore.Layout().WorkflowPath, compiled); err != nil {
		t.Fatalf("workflow.SaveResolvedFile() error = %v", err)
	}

	for _, event := range []store.Event{
		{Type: store.EventRunCreated, Message: "run created", Data: appEventData("", string(runtime.RunStatePending), "run created", "", "")},
		{Type: store.EventRunStarted, Message: "run started", Data: appEventData(string(runtime.RunStatePending), string(runtime.RunStateRunning), "run started", "", "")},
		{Type: store.EventRunPaused, Message: "operator pause", Data: appEventData(string(runtime.RunStateRunning), string(runtime.RunStatePaused), "operator pause", "", "")},
	} {
		if _, err := runStore.AppendEvent(event); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	if err := runStore.SaveCheckpoint(&store.Checkpoint{
		RunID:        filepath.Base(stateDir),
		RepoPath:     filepath.Dir(filepath.Dir(stateDir)),
		WorkingDir:   filepath.Dir(filepath.Dir(stateDir)),
		State:        string(runtime.RunStatePaused),
		LastSequence: 3,
		UpdatedAt:    appFixedEventTime().Format(time.RFC3339Nano),
		Steps: map[string]store.StepCheckpoint{
			"prepare": {State: string(runtime.StepStatePending)},
			"review":  {State: string(runtime.StepStatePending)},
			"notify":  {State: string(runtime.StepStatePending)},
		},
	}); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}
}

func writePausedCommandRunState(t *testing.T, stateDir, workingDir, command string) {
	t.Helper()

	compiled, err := workflow.CompileWorkflow(&workflow.Spec{
		Metadata: workflow.Metadata{Name: "resume-working-dir"},
		Steps: []workflow.StepSpec{{
			ID:      "prepare",
			Kind:    workflow.StepKindCommand,
			Command: &workflow.CommandStepSpec{Command: command},
		}},
	})
	if err != nil {
		t.Fatalf("workflow.CompileWorkflow() error = %v", err)
	}

	runStore, err := store.Open(filepath.Dir(stateDir), filepath.Base(stateDir))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	if err := workflow.SaveResolvedFile(runStore.Layout().WorkflowPath, compiled); err != nil {
		t.Fatalf("workflow.SaveResolvedFile() error = %v", err)
	}

	for _, event := range []store.Event{
		{Type: store.EventRunCreated, Message: "run created", Data: appEventData("", string(runtime.RunStatePending), "run created", "", "")},
		{Type: store.EventRunStarted, Message: "run started", Data: appEventData(string(runtime.RunStatePending), string(runtime.RunStateRunning), "run started", "", "")},
		{Type: store.EventRunPaused, Message: "operator pause", Data: appEventData(string(runtime.RunStateRunning), string(runtime.RunStatePaused), "operator pause", "", "")},
	} {
		if _, err := runStore.AppendEvent(event); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	if err := runStore.SaveCheckpoint(&store.Checkpoint{
		RunID:        filepath.Base(stateDir),
		RepoPath:     workingDir,
		WorkingDir:   workingDir,
		State:        string(runtime.RunStatePaused),
		LastSequence: 3,
		UpdatedAt:    appFixedEventTime().Format(time.RFC3339Nano),
		Steps: map[string]store.StepCheckpoint{
			"prepare": {State: string(runtime.StepStatePending)},
		},
	}); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}
}

func appEventData(from, to, summary, providerSessionID, normalizedStatus string) map[string]string {
	data := map[string]string{
		"occurred_at": appFixedEventTime().Format(time.RFC3339Nano),
		"from_state":  from,
		"to_state":    to,
		"summary":     summary,
	}

	if providerSessionID != "" {
		data["provider_session_id"] = providerSessionID
	}
	if normalizedStatus != "" {
		data["normalized_status"] = normalizedStatus
	}

	return data
}

func appFixedEventTime() time.Time {
	return time.Date(2026, time.March, 22, 16, 0, 0, 0, time.UTC)
}
