package feishuproject_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/JackDrogon/Cogito/internal/task/feishuproject"
)

func TestLoadConfigAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cogito.toml")
	contents := `
[meegle]
base_url = "https://project.feishu.cn"
plugin_id = "pid"
plugin_secret = "psecret"
user_key = "ukey"
project_key = "pkey"
`
	writeFile(t, path, contents)

	cfg, err := feishuproject.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.WorkItemTypeKey != "story" {
		t.Fatalf("WorkItemTypeKey default = %q, want %q", cfg.WorkItemTypeKey, "story")
	}
	if cfg.PageSize != 100 {
		t.Fatalf("PageSize default = %d, want 100", cfg.PageSize)
	}
	if cfg.Poll.Interval != time.Minute {
		t.Fatalf("Poll.Interval default = %s, want 1m", cfg.Poll.Interval)
	}
	if cfg.Poll.StateFile == "" || cfg.Poll.OutputFile == "" {
		t.Fatalf("poll defaults missing: state=%q output=%q", cfg.Poll.StateFile, cfg.Poll.OutputFile)
	}
}

func TestLoadConfigRejectsMissingCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cogito.toml")
	writeFile(t, path, `
[meegle]
base_url = "https://project.feishu.cn"
`)

	if _, err := feishuproject.LoadConfig(path); err == nil {
		t.Fatalf("expected validation error for missing credentials")
	}
}

func TestLoadConfigParsesPollSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cogito.toml")
	writeFile(t, path, `
[meegle]
base_url = "https://project.feishu.cn"
plugin_id = "pid"
plugin_secret = "psecret"
user_key = "ukey"
project_key = "pkey"
page_size = 50

[meegle.poll]
interval = "30s"
state_file = "/tmp/state.json"
output_file = "/tmp/stories.json"
`)

	cfg, err := feishuproject.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PageSize != 50 {
		t.Fatalf("PageSize = %d, want 50", cfg.PageSize)
	}
	if cfg.Poll.Interval != 30*time.Second {
		t.Fatalf("Poll.Interval = %s, want 30s", cfg.Poll.Interval)
	}
	if cfg.Poll.StateFile != "/tmp/state.json" {
		t.Fatalf("Poll.StateFile = %q", cfg.Poll.StateFile)
	}
	if cfg.Poll.OutputFile != "/tmp/stories.json" {
		t.Fatalf("Poll.OutputFile = %q", cfg.Poll.OutputFile)
	}
}

func TestStoryFingerprintStableAcrossOwnerOrder(t *testing.T) {
	first := feishuproject.Story{ID: 1, Name: "x", Owners: []string{"alice", "bob"}}
	second := feishuproject.Story{ID: 1, Name: "x", Owners: []string{"bob", "alice"}}

	if first.Fingerprint() != second.Fingerprint() {
		t.Fatalf("fingerprint must ignore owner ordering")
	}
}

func TestStoryFingerprintChangesOnFieldChange(t *testing.T) {
	base := feishuproject.Story{ID: 1, Name: "x", Status: "open"}
	modified := base
	modified.Status = "closed"

	if base.Fingerprint() == modified.Fingerprint() {
		t.Fatalf("fingerprint should change when status changes")
	}
}

func TestBuildDiffDetectsAddUpdateRemove(t *testing.T) {
	prev := feishuproject.NewState()
	prev.Fingerprints["1"] = "old-fp"
	prev.Fingerprints["2"] = "stable-fp"

	stable := feishuproject.Story{ID: 2, Name: "stable"}
	stable2 := stable
	prev.Fingerprints["2"] = stable2.Fingerprint()

	stories := []feishuproject.Story{
		{ID: 1, Name: "updated"}, // existed, but fingerprint differs from "old-fp"
		stable,                   // existed, fingerprint unchanged
		{ID: 3, Name: "fresh"},   // new
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	diff, next := feishuproject.BuildDiff(prev, stories, now)

	if len(diff.Added) != 1 || diff.Added[0] != 3 {
		t.Fatalf("Added = %v, want [3]", diff.Added)
	}
	if len(diff.Updated) != 1 || diff.Updated[0] != 1 {
		t.Fatalf("Updated = %v, want [1]", diff.Updated)
	}
	if len(diff.Removed) != 0 {
		t.Fatalf("Removed = %v, want []", diff.Removed)
	}
	if next.UpdatedAt != now {
		t.Fatalf("next.UpdatedAt = %v, want %v", next.UpdatedAt, now)
	}
	if len(next.Fingerprints) != 3 {
		t.Fatalf("next.Fingerprints len = %d, want 3", len(next.Fingerprints))
	}
}

func TestBuildDiffDetectsRemoval(t *testing.T) {
	prev := feishuproject.NewState()
	prev.Fingerprints["10"] = "gone-fp"
	prev.Fingerprints["20"] = "kept-fp"

	kept := feishuproject.Story{ID: 20, Name: "kept"}
	prev.Fingerprints["20"] = kept.Fingerprint()

	diff, _ := feishuproject.BuildDiff(prev, []feishuproject.Story{kept}, time.Now())
	if len(diff.Removed) != 1 || diff.Removed[0] != 10 {
		t.Fatalf("Removed = %v, want [10]", diff.Removed)
	}
}

func TestSaveAndLoadStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	state := feishuproject.State{
		UpdatedAt:    time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		Fingerprints: map[string]string{"1": "abc", "2": "def"},
	}

	if err := feishuproject.SaveState(path, state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := feishuproject.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !loaded.UpdatedAt.Equal(state.UpdatedAt) {
		t.Fatalf("UpdatedAt = %v, want %v", loaded.UpdatedAt, state.UpdatedAt)
	}
	if len(loaded.Fingerprints) != 2 || loaded.Fingerprints["1"] != "abc" {
		t.Fatalf("Fingerprints round-trip mismatch: %v", loaded.Fingerprints)
	}
}

func TestLoadStateMissingFileReturnsEmpty(t *testing.T) {
	state, err := feishuproject.LoadState(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("LoadState on missing file should not error: %v", err)
	}
	if len(state.Fingerprints) != 0 {
		t.Fatalf("expected empty fingerprints, got %d", len(state.Fingerprints))
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := writeAtomic(path, contents); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}
