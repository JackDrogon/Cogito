package store

import (
	"errors"
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
		return wrapError(ErrorCodePath, "create state root "+root, err)
	}

	gitignorePath := filepath.Join(root, ".gitignore")

	if err := writeGitignoreDurably(gitignorePath); err != nil {
		return wrapError(ErrorCodePath, "write state root gitignore "+gitignorePath, err)
	}

	return nil
}

// writeGitignoreDurably creates the self-ignoring .gitignore exactly once.
// O_EXCL makes creation atomic (no stat-then-write race), and the explicit
// Sync guarantees the content is on disk: without it a crash right after
// creation could leave an EXISTING but EMPTY .gitignore that would be skipped
// forever while no longer ignoring anything.
func writeGitignoreDurably(path string) error {
	file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, persistedFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}

		return err
	}

	if _, err := file.WriteString(selfIgnoreContent); err != nil {
		return errors.Join(err, file.Close())
	}

	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}

	return file.Close()
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
