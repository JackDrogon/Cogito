package prompt

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update golden prompt fixtures")

func TestBuildMainGolden(t *testing.T) {
	input := PromptInput{
		Root: ".",
		Tasks: []TaskRef{
			{ID: "agent", Text: "实现示例任务"},
		},
	}

	assertGolden(t, "prompt_main.golden", BuildMain(input))
}

func TestBuildCommitRecoveryGolden(t *testing.T) {
	input := PromptInput{
		Root: ".",
		Tasks: []TaskRef{
			{ID: "task-1", Text: "任务一"},
			{ID: "task-2", Text: "任务二"},
		},
	}

	assertGolden(t, "prompt_commit_recovery.golden", BuildCommitRecovery(input, []string{".agent/toolchain-bugs/bug.md"}))
}

func TestBuildDirtyWorktreeGolden(t *testing.T) {
	input := PromptInput{
		Root: ".",
		Tasks: []TaskRef{
			{ID: "agent", Text: "实现示例任务"},
		},
	}

	assertGolden(t, "prompt_dirty_worktree.golden", BuildDirtyWorktree(input))
}

func TestBuildMainRendersAllPlaceholders(t *testing.T) {
	got := BuildMain(PromptInput{
		Root:        "/repo",
		Tasks:       []TaskRef{{ID: "a", Text: "task a"}},
		FailedTasks: []TaskRef{{ID: "b", Text: "task b"}},
		BugDir:      "custom/bugs",
	})

	for _, leftover := range []string{
		"{project_root}", "{todo_path}", "{git_repo_note}",
		"{len(runnable_tasks)}", "{len(exhausted_tasks)}",
		"{current_task}", "{blocked_preview}",
		"{toolchain_bug_repro_dir_label}", "{toolchain_bug_dir_label}",
		"{RESULT_PREFIX}",
	} {
		if strings.Contains(got, leftover) {
			t.Fatalf("BuildMain left placeholder %q unrendered", leftover)
		}
	}

	if !strings.Contains(got, ResultPrefix) {
		t.Fatalf("BuildMain output missing result prefix %q", ResultPrefix)
	}

	if !strings.Contains(got, "custom/bugs/repros") {
		t.Fatalf("BuildMain output missing repro dir; got:\n%s", got)
	}

	if !strings.Contains(got, "1. task a") {
		t.Fatalf("BuildMain output missing rendered task; got:\n%s", got)
	}
}

// TestPromptsCarryInlineTaskSemantics guards the inline-task adaptation:
// Cogito has no todo file, so no template may instruct the agent to read,
// update, or checkbox-mark one — that wording previously sent agents on
// filesystem expeditions hunting for a todo file that does not exist.
func TestPromptsCarryInlineTaskSemantics(t *testing.T) {
	input := PromptInput{Root: "/repo", Tasks: []TaskRef{{ID: "a", Text: "task a"}}}

	prompts := map[string]string{
		"main":            BuildMain(input),
		"commit-recovery": BuildCommitRecovery(input, nil),
		"dirty-worktree":  BuildDirtyWorktree(input),
	}

	for name, got := range prompts {
		for _, forbidden := range []string{
			"这次要处理的 todo 文件",
			"todo 文件：\n/repo",
			"`[~]`", "`[x]`", "`[ ]`",
			"todo 勾选",
		} {
			if strings.Contains(got, forbidden) {
				t.Errorf("%s prompt still contains todo-file choreography %q", name, forbidden)
			}
		}

		if !strings.Contains(got, "本次运行没有 todo 文件") {
			t.Errorf("%s prompt missing inline task-source declaration", name)
		}

		if !strings.Contains(got, ".cogito/") {
			t.Errorf("%s prompt missing .cogito state-dir guard", name)
		}
	}
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()

	goldenPath := filepath.Join("testdata", name)

	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}

		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}

		return
	}

	want, err := os.ReadFile(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("read golden %s: %v (run `go test ./internal/adapters/prompt/ -update` to create)", name, err)
	}

	if got != string(want) {
		t.Fatalf("prompt %s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, string(want))
	}
}
