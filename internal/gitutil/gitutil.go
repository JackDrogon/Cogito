// Package gitutil is the shared git boundary used by runtime commit checks and
// future recovery flows. It ports AgentLoop's GitOps semantics: a strict repo
// detection that refuses to treat a subdirectory of an enclosing repository as
// a repo, plus the narrow set of read-mostly operations the workflow engine
// needs to validate self-reported commits.
package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitOps binds git operations to a single work-tree root. Every git invocation
// runs with GIT_CEILING_DIRECTORIES pinned to the parent of Root so git never
// walks up into an enclosing repository — the footgun where a naive commit from
// a non-repo subdirectory lands in the outer repo.
type GitOps struct {
	Root string
}

// command builds a git invocation rooted at Root with the ceiling guard set.
// The context bounds the subprocess lifetime so a wedged git (network push,
// slow filesystem) cannot stall the caller forever.
func (g GitOps) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Root
	cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(g.Root))

	return cmd
}

// CommitRefValidation buckets the result of validating self-reported commit
// refs against real git history. Returning a struct keeps ValidateCommitRefs to
// two return values per the project's return-count rule.
type CommitRefValidation struct {
	Valid   []string
	Invalid []string
}

// IsRepo reports whether Root is itself the top level of a git work tree.
//
// It deliberately resolves `git rev-parse --show-toplevel` and requires the
// result to equal Root after symlink resolution. A subdirectory of an enclosing
// repository does NOT count: with the ceiling guard git cannot find the outer
// repo, and even without it the toplevel would differ from Root. Any git failure
// (missing binary, non-repo) yields false.
func (g GitOps) IsRepo(ctx context.Context) bool {
	out, err := g.command(ctx, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return false
	}

	top, err := resolvePath(strings.TrimSpace(string(out)))
	if err != nil {
		return false
	}

	root, err := resolvePath(g.Root)
	if err != nil {
		return false
	}

	return top == root
}

// HasUncommittedChanges reports whether the work tree has staged or unstaged
// changes via `git status --porcelain` (non-empty output means dirty).
func (g GitOps) HasUncommittedChanges(ctx context.Context) (bool, error) {
	out, err := g.command(ctx, "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git status --porcelain: %w", err)
	}

	return strings.TrimSpace(string(out)) != "", nil
}

// HeadCommit returns the resolved HEAD commit hash.
func (g GitOps) HeadCommit(ctx context.Context) (string, error) {
	out, err := g.command(ctx, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

// CommitsSince returns the SHAs reachable in ref..HEAD in oldest→newest order.
// `git log` prints newest first, so the output is reversed before returning.
func (g GitOps) CommitsSince(ctx context.Context, ref string) ([]string, error) {
	if err := validateRef(ref); err != nil {
		return nil, err
	}

	// The trailing "--" tells git the range is a revision, never a path, so a
	// ref that happens to collide with a file name cannot change the meaning
	// of the command.
	out, err := g.command(ctx, "log", "--format=%H", ref+"..HEAD", "--").Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s..HEAD: %w", ref, err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	commits := make([]string, 0, len(lines))

	for i := len(lines) - 1; i >= 0; i-- {
		if sha := strings.TrimSpace(lines[i]); sha != "" {
			commits = append(commits, sha)
		}
	}

	return commits, nil
}

// ValidateCommitRefs checks each ref with `git rev-parse --verify --quiet
// <ref>^{commit}` and buckets it into Valid or Invalid. A non-zero git exit
// (ref does not resolve to a commit) is an expected "invalid" outcome; only
// unexpected failures such as a missing git binary surface as an error.
func (g GitOps) ValidateCommitRefs(ctx context.Context, refs []string) (CommitRefValidation, error) {
	result := CommitRefValidation{Valid: []string{}, Invalid: []string{}}

	for _, ref := range refs {
		trimmed := strings.TrimSpace(ref)
		if trimmed == "" {
			continue
		}

		// A leading "-" can never start a valid revision but could be parsed
		// as a git flag; bucket it as invalid instead of passing it through.
		if validateRef(trimmed) != nil {
			result.Invalid = append(result.Invalid, trimmed)
			continue
		}

		err := g.command(ctx, "rev-parse", "--verify", "--quiet", trimmed+"^{commit}").Run()
		if err == nil {
			result.Valid = append(result.Valid, trimmed)
			continue
		}

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.Invalid = append(result.Invalid, trimmed)
			continue
		}

		return CommitRefValidation{}, fmt.Errorf("git rev-parse --verify %s: %w", trimmed, err)
	}

	return result, nil
}

// PushCommits runs `git push` best-effort. A clean tree with nothing to push
// exits zero; a missing remote or upstream surfaces the git error to the caller.
func (g GitOps) PushCommits(ctx context.Context) error {
	if err := g.command(ctx, "push").Run(); err != nil {
		return fmt.Errorf("git push (root %s): %w", g.Root, err)
	}

	return nil
}

// DiscoverToplevel resolves the enclosing git work-tree root for dir, walking
// upward exactly as plain `git -C dir rev-parse --show-toplevel` does. Unlike
// GitOps methods it intentionally does NOT pin GIT_CEILING_DIRECTORIES:
// callers (repo locking) WANT a run started in a subdirectory to resolve to —
// and contend on — the enclosing repository root. The trimmed git output is
// folded into the error so callers keep git's own diagnostic.
func DiscoverToplevel(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")

	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}

		return "", fmt.Errorf("git rev-parse --show-toplevel: %s: %w", message, err)
	}

	return strings.TrimSpace(string(output)), nil
}

// validateRef rejects ref spellings that git would parse as a flag instead of
// a revision. Refs come from agent self-reports, so they are untrusted input.
func validateRef(ref string) error {
	if strings.HasPrefix(strings.TrimSpace(ref), "-") {
		return fmt.Errorf("gitutil: invalid ref %q: leading dash", ref)
	}

	return nil
}

// resolvePath canonicalizes path by resolving symlinks, falling back to an
// absolute clean path when the target cannot be evaluated. Both the git
// toplevel and Root pass through this so equality in IsRepo is symlink-safe.
func resolvePath(path string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved), nil
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	return filepath.Clean(abs), nil
}
