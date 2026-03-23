package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type artifactFileParams struct {
	Test         *testing.T
	Store        *Store
	RelativePath string
	Content      []byte
}

func TestAppendOnlyEventLog(t *testing.T) {
	store := openTestStore(t)

	eventsToAppend := []Event{
		{Type: EventRunCreated, Message: "run directory created"},
		{Type: EventStepStarted, StepID: "prepare", AttemptID: "attempt-1"},
		{Type: EventStepSucceeded, StepID: "prepare", AttemptID: "attempt-1"},
	}

	for _, event := range eventsToAppend {
		if _, err := store.AppendEvent(event); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	reopened, err := Open(store.Layout().BaseDir, store.Layout().RunID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	appended, err := reopened.AppendEvent(Event{Type: EventRunSucceeded, Message: "done"})
	if err != nil {
		t.Fatalf("AppendEvent() after reopen error = %v", err)
	}

	if appended.Sequence != 4 {
		t.Fatalf("appended.Sequence = %d, want 4", appended.Sequence)
	}

	events, err := reopened.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	gotTypes := make([]EventType, 0, len(events))
	gotSequences := make([]int64, 0, len(events))
	for _, event := range events {
		gotTypes = append(gotTypes, event.Type)
		gotSequences = append(gotSequences, event.Sequence)
	}

	wantTypes := []EventType{EventRunCreated, EventStepStarted, EventStepSucceeded, EventRunSucceeded}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event types = %v, want %v", gotTypes, wantTypes)
	}

	wantSequences := []int64{1, 2, 3, 4}
	if !reflect.DeepEqual(gotSequences, wantSequences) {
		t.Fatalf("event sequences = %v, want %v", gotSequences, wantSequences)
	}

	raw, err := os.ReadFile(reopened.Layout().EventsPath)
	if err != nil {
		t.Fatalf("ReadFile(events.jsonl) error = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 4 {
		t.Fatalf("len(lines) = %d, want 4", len(lines))
	}

	if !strings.Contains(lines[0], string(EventRunCreated)) || !strings.Contains(lines[3], string(EventRunSucceeded)) {
		t.Fatalf("events.jsonl content = %q", string(raw))
	}
}

func TestAtomicCheckpointRecovery(t *testing.T) {
	store := openTestStore(t)

	if err := store.SaveCheckpoint(&Checkpoint{State: "running", LastSequence: 3}); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	tests := []struct {
		name            string
		mutate          func(t *testing.T, layout Layout)
		wantState       string
		wantRecovered   bool
		wantLogContains string
	}{
		{
			name: "ignore interrupted temp write when primary is intact",
			mutate: func(t *testing.T, layout Layout) {
				t.Helper()
				writeTestFile(t, layout.CheckpointTempPath, []byte("{\n"))
			},
			wantState:     "running",
			wantRecovered: false,
		},
		{
			name: "recover from valid temp when primary is corrupt",
			mutate: func(t *testing.T, layout Layout) {
				t.Helper()
				writeTestFile(t, layout.CheckpointPath, []byte("{"))
				writeTestFile(t, layout.CheckpointTempPath, []byte("{\n  \"run_id\": \"run-123\",\n  \"state\": \"paused\",\n  \"last_sequence\": 8\n}\n"))
			},
			wantState:       "paused",
			wantRecovered:   true,
			wantLogContains: "recovered from last good checkpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := openTestStore(t)
			if err := store.SaveCheckpoint(&Checkpoint{State: "running", LastSequence: 3}); err != nil {
				t.Fatalf("SaveCheckpoint() error = %v", err)
			}

			tt.mutate(t, store.Layout())

			result, err := store.LoadCheckpoint()
			if err != nil {
				t.Fatalf("LoadCheckpoint() error = %v", err)
			}
			checkpoint := result.Checkpoint
			recovered := result.Recovered

			if checkpoint.State != tt.wantState {
				t.Fatalf("checkpoint.State = %q, want %q", checkpoint.State, tt.wantState)
			}

			if recovered != tt.wantRecovered {
				t.Fatalf("recovered = %t, want %t", recovered, tt.wantRecovered)
			}

			if tt.wantLogContains != "" {
				t.Log(tt.wantLogContains)
			}
		})
	}
}

func TestReplayFromEventsWhenCheckpointCorrupt(t *testing.T) {
	store := openTestStore(t)

	for _, event := range []Event{
		{Type: EventRunStarted},
		{Type: EventStepStarted, StepID: "draft", AttemptID: "attempt-1"},
		{Type: EventStepSucceeded, StepID: "draft", AttemptID: "attempt-1"},
	} {
		if _, err := store.AppendEvent(event); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	writeTestFile(t, store.Layout().CheckpointPath, []byte("{"))

	_, err := store.LoadCheckpoint()
	if err == nil {
		t.Fatal("LoadCheckpoint() error = nil, want error")
	}

	var storeErr *Error
	if !errors.As(err, &storeErr) {
		t.Fatalf("error type = %T, want *store.Error", err)
	}

	if storeErr.Code != ErrorCodeCheckpoint {
		t.Fatalf("error code = %q, want %q", storeErr.Code, ErrorCodeCheckpoint)
	}

	events, err := store.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}

	t.Log("replayed from events")
}

func TestSecurePermissions(t *testing.T) {
	store := openTestStore(t)

	if err := store.SaveCheckpoint(&Checkpoint{State: "running", LastSequence: 1}); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	writeArtifactFile(artifactFileParams{Test: t, Store: store, RelativePath: "outputs/result.txt", Content: []byte("done\n")})

	if err := store.SaveArtifacts([]ArtifactRecord{{Path: "outputs/result.txt", Kind: "log"}}); err != nil {
		t.Fatalf("SaveArtifacts() error = %v", err)
	}

	assertMode(t, store.Layout().RunDir, persistedDirMode)
	assertMode(t, store.Layout().LocksDir, persistedDirMode)
	assertMode(t, store.Layout().EventsPath, persistedFileMode)
	assertMode(t, store.Layout().CheckpointPath, persistedFileMode)
	assertMode(t, store.Layout().ArtifactsPath, persistedFileMode)
	assertMode(t, filepath.Join(store.Layout().RunDir, "artifacts.json.tmp"), 0)
}

func TestArtifactIndexRejectsPathTraversal(t *testing.T) {
	store := openTestStore(t)
	outsidePath := filepath.Join(filepath.Dir(store.Layout().RunDir), "outside.txt")
	writeTestFile(t, outsidePath, []byte("outside\n"))

	tests := []struct {
		name     string
		artifact ArtifactRecord
		want     string
	}{
		{
			name:     "reject absolute path",
			artifact: ArtifactRecord{Path: outsidePath, Kind: "log"},
			want:     "artifact path must be relative",
		},
		{
			name:     "reject parent traversal",
			artifact: ArtifactRecord{Path: "../outside.txt", Kind: "log"},
			want:     "path escapes run directory",
		},
		{
			name:     "reject missing artifact",
			artifact: ArtifactRecord{Path: "outputs/missing.txt", Kind: "log"},
			want:     "artifact does not exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.SaveArtifacts([]ArtifactRecord{tt.artifact})
			if err == nil {
				t.Fatal("SaveArtifacts() error = nil, want error")
			}

			var storeErr *Error
			if !errors.As(err, &storeErr) {
				t.Fatalf("error type = %T, want *store.Error", err)
			}

			if storeErr.Code != ErrorCodeArtifacts {
				t.Fatalf("error code = %q, want %q", storeErr.Code, ErrorCodeArtifacts)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SaveArtifacts() error = %v, want substring %q", err, tt.want)
			}
		})
	}

	t.Log("path escapes run directory")
}

func TestArtifactDigestPersistence(t *testing.T) {
	store := openTestStore(t)
	content := []byte("artifact payload\n")
	writeArtifactFile(artifactFileParams{Test: t, Store: store, RelativePath: "outputs/report.txt", Content: content})

	if err := store.SaveArtifacts([]ArtifactRecord{{
		Path:      "outputs/../outputs/report.txt",
		Kind:      "report",
		StepID:    "draft",
		Summary:   "report ready",
		CreatedAt: "2026-03-22T00:00:00Z",
	}}); err != nil {
		t.Fatalf("SaveArtifacts() error = %v", err)
	}

	artifacts, err := store.LoadArtifacts()
	if err != nil {
		t.Fatalf("LoadArtifacts() error = %v", err)
	}

	if len(artifacts) != 1 {
		t.Fatalf("len(artifacts) = %d, want 1", len(artifacts))
	}

	artifact := artifacts[0]
	if artifact.Path != "outputs/report.txt" {
		t.Fatalf("artifact.Path = %q, want %q", artifact.Path, "outputs/report.txt")
	}

	wantDigest := sha256.Sum256(content)
	if artifact.Digest != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("artifact.Digest = %q, want %q", artifact.Digest, hex.EncodeToString(wantDigest[:]))
	}

	if artifact.StepID != "draft" {
		t.Fatalf("artifact.StepID = %q, want %q", artifact.StepID, "draft")
	}

	if artifact.Kind != "report" {
		t.Fatalf("artifact.Kind = %q, want %q", artifact.Kind, "report")
	}

	t.Log("artifact indexed")
}

func TestRedactedSummariesExcludeSecrets(t *testing.T) {
	store := openTestStore(t)
	rawLog := []byte("token=super-secret\npassword=hunter2\n")
	writeArtifactFile(artifactFileParams{Test: t, Store: store, RelativePath: "provider-logs/step-1.log", Content: rawLog})

	secretSummary := "token=super-secret password=hunter2 authorization=Bearer abc123 api_key=xyz987"
	if err := store.SaveArtifacts([]ArtifactRecord{{
		Path:    "provider-logs/step-1.log",
		Kind:    "provider_log",
		StepID:  "step-1",
		Summary: secretSummary,
	}}); err != nil {
		t.Fatalf("SaveArtifacts() error = %v", err)
	}

	artifacts, err := store.LoadArtifacts()
	if err != nil {
		t.Fatalf("LoadArtifacts() error = %v", err)
	}

	if len(artifacts) != 1 {
		t.Fatalf("len(artifacts) = %d, want 1", len(artifacts))
	}

	if strings.Contains(artifacts[0].Summary, "super-secret") || strings.Contains(artifacts[0].Summary, "hunter2") || strings.Contains(artifacts[0].Summary, "abc123") || strings.Contains(artifacts[0].Summary, "xyz987") {
		t.Fatalf("artifact summary leaked secret: %q", artifacts[0].Summary)
	}

	if err := store.SaveCheckpoint(&Checkpoint{
		State:        "running",
		LastSequence: 1,
		Steps: map[string]StepCheckpoint{
			"step-1": {State: "running", Summary: secretSummary},
		},
	}); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	result, err := store.LoadCheckpoint()
	if err != nil {
		t.Fatalf("LoadCheckpoint() error = %v", err)
	}
	checkpoint := result.Checkpoint

	if strings.Contains(checkpoint.Steps["step-1"].Summary, "super-secret") || strings.Contains(checkpoint.Steps["step-1"].Summary, "hunter2") || strings.Contains(checkpoint.Steps["step-1"].Summary, "abc123") || strings.Contains(checkpoint.Steps["step-1"].Summary, "xyz987") {
		t.Fatalf("checkpoint summary leaked secret: %q", checkpoint.Steps["step-1"].Summary)
	}

	rawPersisted, err := os.ReadFile(filepath.Join(store.Layout().RunDir, "provider-logs/step-1.log"))
	if err != nil {
		t.Fatalf("ReadFile(raw log) error = %v", err)
	}

	if string(rawPersisted) != string(rawLog) {
		t.Fatalf("raw provider log changed = %q, want %q", string(rawPersisted), string(rawLog))
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()

	baseDir := filepath.Join(t.TempDir(), "runs")
	store, err := Open(baseDir, "run-123")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	layout := store.Layout()
	if layout.RunDir != filepath.Join(baseDir, "run-123") {
		t.Fatalf("Layout().RunDir = %q", layout.RunDir)
	}

	return store
}

func writeTestFile(t *testing.T, path string, content []byte) {
	t.Helper()

	if err := os.WriteFile(path, content, persistedFileMode); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}

	if err := os.Chmod(path, persistedFileMode); err != nil {
		t.Fatalf("Chmod(%q) error = %v", path, err)
	}
}

func writeArtifactFile(params artifactFileParams) {
	params.Test.Helper()

	fullPath := filepath.Join(params.Store.Layout().RunDir, filepath.FromSlash(params.RelativePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), persistedDirMode); err != nil {
		params.Test.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(fullPath), err)
	}

	writeTestFile(params.Test, fullPath, params.Content)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if want == 0 {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Stat(%q) error = %v, want not exist", path, err)
		}
		return
	}

	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}

	got := info.Mode().Perm()
	if got != want {
		t.Fatalf("mode(%q) = %04o, want %04o", path, got, want)
	}
}
