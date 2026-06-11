package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultStateDirAnchorsAtRepo(t *testing.T) {
	got := defaultStateDir("/tmp/repo")
	if !strings.HasPrefix(got, filepath.Join("/tmp/repo", ".cogito", "runs")+string(filepath.Separator)) {
		t.Fatalf("defaultStateDir(/tmp/repo) = %q, want under /tmp/repo/.cogito/runs/", got)
	}
}

func TestDefaultStateDirEmptyRepoUsesCwd(t *testing.T) {
	got := defaultStateDir("")
	if !strings.HasPrefix(got, filepath.Join(".cogito", "runs")+string(filepath.Separator)) {
		t.Fatalf("defaultStateDir(\"\") = %q, want under .cogito/runs/", got)
	}
}

// TestEnsureStateRootIgnoredWritesGitignore covers the app wrapper end to end;
// layout-detection edge cases live with store.EnsureSelfIgnored.
func TestEnsureStateRootIgnoredWritesGitignore(t *testing.T) {
	baseDir := t.TempDir()
	stateRef, err := newRunStateRef(filepath.Join(baseDir, ".cogito", "runs", "run-1"))
	if err != nil {
		t.Fatalf("newRunStateRef() error = %v", err)
	}

	if err := ensureStateRootIgnored(stateRef); err != nil {
		t.Fatalf("ensureStateRootIgnored() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(baseDir, ".cogito", ".gitignore"))
	if err != nil {
		t.Fatalf("ReadFile(.cogito/.gitignore) error = %v", err)
	}
	if string(data) != "*\n" {
		t.Fatalf(".cogito/.gitignore = %q, want %q", data, "*\n")
	}
}

func TestParseAgentsRunFlagsDefaultStateDirUnderRepo(t *testing.T) {
	flags, err := parseAgentsRunFlags([]string{"do the thing", "--repo", "/tmp/target"}, &strings.Builder{})
	if err != nil {
		t.Fatalf("parseAgentsRunFlags() error = %v", err)
	}
	if !strings.HasPrefix(flags.shared.stateDir, filepath.Join("/tmp/target", ".cogito", "runs")+string(filepath.Separator)) {
		t.Fatalf("flags.shared.stateDir = %q, want under /tmp/target/.cogito/runs/", flags.shared.stateDir)
	}
}
