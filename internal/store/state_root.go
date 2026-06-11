package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// selfIgnoreContent makes a directory invisible to git: a .gitignore containing
// "*" ignores everything inside, including itself.
const selfIgnoreContent = "*\n"

// EnsureSelfIgnored keeps Cogito state invisible to git. When path lives under
// (or is) a directory named DefaultStateRoot (".cogito"), that directory is
// created if needed and receives a self-ignoring .gitignore ("*"). Without
// this, the first run inside a git repository would leave an untracked
// .cogito/ tree that trips the dirty-worktree gate on the next run.
//
// Paths outside a .cogito layout are left untouched, and an existing
// .gitignore is never overwritten.
func EnsureSelfIgnored(path string) error {
	root := cogitoRoot(path)
	if root == "" {
		return nil
	}

	if err := os.MkdirAll(root, persistedDirMode); err != nil {
		return fmt.Errorf("create state root %q: %w", root, err)
	}

	gitignorePath := filepath.Join(root, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		return nil
	}

	if err := os.WriteFile(gitignorePath, []byte(selfIgnoreContent), persistedFileMode); err != nil {
		return fmt.Errorf("write state root gitignore %q: %w", gitignorePath, err)
	}

	return nil
}

// cogitoRoot returns the nearest ancestor of path (including path itself)
// whose base name is DefaultStateRoot, or "" when path is outside any .cogito
// layout.
func cogitoRoot(path string) string {
	current := filepath.Clean(path)
	for {
		if filepath.Base(current) == DefaultStateRoot {
			return current
		}

		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}
