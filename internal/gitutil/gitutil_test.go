package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepo creates a fresh git repository under t.TempDir() with one initial
// commit and returns its root plus that commit's SHA.
func initRepo(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test User")

	writeFile(t, filepath.Join(root, "README.md"), "hello\n")
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "initial commit")

	return root, headSHA(t, root)
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(root))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v error = %v: %s", args, err, out)
	}
}

func headSHA(t *testing.T, root string) string {
	t.Helper()

	out, err := GitOps{Root: root}.HeadCommit(t.Context())
	if err != nil {
		t.Fatalf("HeadCommit() error = %v", err)
	}

	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func TestIsRepoTrueForRepoRoot(t *testing.T) {
	root, _ := initRepo(t)

	if !(GitOps{Root: root}).IsRepo(t.Context()) {
		t.Fatal("IsRepo() = false, want true for repo root")
	}
}

func TestIsRepoFalseForNonRepo(t *testing.T) {
	dir := t.TempDir()

	if (GitOps{Root: dir}).IsRepo(t.Context()) {
		t.Fatal("IsRepo() = true, want false for non-repo dir")
	}
}

func TestIsRepoFalseForSubdirOfEnclosingRepo(t *testing.T) {
	root, _ := initRepo(t)
	sub := filepath.Join(root, "nested")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	// nested is inside an enclosing repo but is not itself a work-tree top.
	if (GitOps{Root: sub}).IsRepo(t.Context()) {
		t.Fatal("IsRepo() = true, want false for subdir of enclosing repo")
	}
}

func TestHasUncommittedChanges(t *testing.T) {
	root, _ := initRepo(t)
	git := GitOps{Root: root}

	dirty, err := git.HasUncommittedChanges(t.Context())
	if err != nil {
		t.Fatalf("HasUncommittedChanges() error = %v", err)
	}
	if dirty {
		t.Fatal("HasUncommittedChanges() = true, want false on clean tree")
	}

	writeFile(t, filepath.Join(root, "new.txt"), "change\n")

	dirty, err = git.HasUncommittedChanges(t.Context())
	if err != nil {
		t.Fatalf("HasUncommittedChanges() error = %v", err)
	}
	if !dirty {
		t.Fatal("HasUncommittedChanges() = false, want true with untracked file")
	}
}

func TestHeadCommit(t *testing.T) {
	root, want := initRepo(t)

	got, err := (GitOps{Root: root}).HeadCommit(t.Context())
	if err != nil {
		t.Fatalf("HeadCommit() error = %v", err)
	}
	if got != want {
		t.Fatalf("HeadCommit() = %q, want %q", got, want)
	}
	if len(got) != 40 {
		t.Fatalf("HeadCommit() length = %d, want 40", len(got))
	}
}

func TestCommitsSinceReturnsOldestToNewest(t *testing.T) {
	root, first := initRepo(t)
	git := GitOps{Root: root}

	writeFile(t, filepath.Join(root, "a.txt"), "a\n")
	runGit(t, root, "add", "a.txt")
	runGit(t, root, "commit", "-m", "second")
	second := headSHA(t, root)

	writeFile(t, filepath.Join(root, "b.txt"), "b\n")
	runGit(t, root, "add", "b.txt")
	runGit(t, root, "commit", "-m", "third")
	third := headSHA(t, root)

	got, err := git.CommitsSince(t.Context(), first)
	if err != nil {
		t.Fatalf("CommitsSince() error = %v", err)
	}

	want := []string{second, third}
	if len(got) != len(want) {
		t.Fatalf("CommitsSince() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CommitsSince()[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestCommitsSinceEmptyWhenAtHead(t *testing.T) {
	root, head := initRepo(t)

	got, err := (GitOps{Root: root}).CommitsSince(t.Context(), head)
	if err != nil {
		t.Fatalf("CommitsSince() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("CommitsSince(HEAD) = %v, want empty", got)
	}
}

func TestValidateCommitRefsMix(t *testing.T) {
	root, head := initRepo(t)

	result, err := (GitOps{Root: root}).ValidateCommitRefs(t.Context(), []string{head, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "HEAD"})
	if err != nil {
		t.Fatalf("ValidateCommitRefs() error = %v", err)
	}

	if len(result.Valid) != 2 {
		t.Fatalf("Valid = %v, want 2 entries", result.Valid)
	}
	if len(result.Invalid) != 1 || result.Invalid[0] != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" {
		t.Fatalf("Invalid = %v, want the bogus ref", result.Invalid)
	}
}

func TestValidateCommitRefsAllValid(t *testing.T) {
	root, head := initRepo(t)

	result, err := (GitOps{Root: root}).ValidateCommitRefs(t.Context(), []string{head})
	if err != nil {
		t.Fatalf("ValidateCommitRefs() error = %v", err)
	}
	if len(result.Valid) != 1 || len(result.Invalid) != 0 {
		t.Fatalf("result = %+v, want one valid and no invalid", result)
	}
}
