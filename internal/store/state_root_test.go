package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSelfIgnoredWritesGitignoreForFilePath(t *testing.T) {
	baseDir := t.TempDir()
	statePath := filepath.Join(baseDir, DefaultStateRoot, "feishu", "state.json")

	if err := EnsureSelfIgnored(statePath); err != nil {
		t.Fatalf("EnsureSelfIgnored() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(baseDir, DefaultStateRoot, ".gitignore"))
	if err != nil {
		t.Fatalf("ReadFile(.cogito/.gitignore) error = %v", err)
	}
	if string(data) != "*\n" {
		t.Fatalf(".cogito/.gitignore = %q, want %q", data, "*\n")
	}
}

func TestEnsureSelfIgnoredKeepsExistingGitignore(t *testing.T) {
	baseDir := t.TempDir()
	stateRoot := filepath.Join(baseDir, DefaultStateRoot)
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	custom := []byte("runs/\n")
	if err := os.WriteFile(filepath.Join(stateRoot, ".gitignore"), custom, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := EnsureSelfIgnored(filepath.Join(stateRoot, "runs", "run-1")); err != nil {
		t.Fatalf("EnsureSelfIgnored() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(stateRoot, ".gitignore"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != string(custom) {
		t.Fatalf(".cogito/.gitignore = %q, want untouched %q", data, custom)
	}
}

func TestEnsureSelfIgnoredSkipsNonCogitoLayout(t *testing.T) {
	baseDir := t.TempDir()
	custom := filepath.Join(baseDir, "custom", "runs", "run-1")

	if err := EnsureSelfIgnored(custom); err != nil {
		t.Fatalf("EnsureSelfIgnored() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(baseDir, "custom")); !os.IsNotExist(err) {
		t.Fatalf("custom state root created = %v, want untouched (not exist)", err)
	}
}

func TestEnsureSelfIgnoredAcceptsStateRootItself(t *testing.T) {
	baseDir := t.TempDir()
	stateRoot := filepath.Join(baseDir, DefaultStateRoot)

	if err := EnsureSelfIgnored(stateRoot); err != nil {
		t.Fatalf("EnsureSelfIgnored() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(stateRoot, ".gitignore")); err != nil {
		t.Fatalf("Stat(.cogito/.gitignore) error = %v, want written", err)
	}
}
