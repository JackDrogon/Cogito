package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/task/feishuproject"
)

// TestBuildFeishuRunPlanRepoMatchRelativeVsAbsolute is v3.2 N6: a relative
// story repo_path and an absolute --repo that resolve to the same directory
// must not trigger a false conflict, and the canonical absolute path must be
// returned downstream so the runtime targets an absolute root.
func TestBuildFeishuRunPlanRepoMatchRelativeVsAbsolute(t *testing.T) {
	abs, err := filepath.Abs("workrepo")
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}

	stories := []feishuproject.Story{{ID: 7, Description: "do the thing", RepoPath: "workrepo"}}
	flags := feishuRunFlags{storyID: 7, agentName: "codex"}
	flags.shared.repo = abs

	_, repoPath, err := buildFeishuRunPlan(stories, flags)
	if err != nil {
		t.Fatalf("buildFeishuRunPlan() error = %v, want relative/absolute match", err)
	}

	if repoPath != abs {
		t.Fatalf("buildFeishuRunPlan() repoPath = %q, want canonical %q", repoPath, abs)
	}
}

// TestBuildFeishuRunPlanRepoMatchTrailingSlash is v3.2 N6: a trailing-slash
// variant of the same directory must not trigger a false conflict.
func TestBuildFeishuRunPlanRepoMatchTrailingSlash(t *testing.T) {
	stories := []feishuproject.Story{{ID: 7, Description: "do the thing", RepoPath: "/tmp/workrepo"}}
	flags := feishuRunFlags{storyID: 7, agentName: "codex"}
	flags.shared.repo = "/tmp/workrepo/"

	_, repoPath, err := buildFeishuRunPlan(stories, flags)
	if err != nil {
		t.Fatalf("buildFeishuRunPlan() error = %v, want trailing-slash match", err)
	}

	if repoPath != "/tmp/workrepo" {
		t.Fatalf("buildFeishuRunPlan() repoPath = %q, want /tmp/workrepo", repoPath)
	}
}

func TestParseFeishuRunFlagsStoryIDBeforeFlags(t *testing.T) {
	flags, err := parseFeishuRunFlags([]string{"7004653782", "-c", "cfg.toml"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFeishuRunFlags() error = %v", err)
	}
	if flags.storyID != 7004653782 {
		t.Fatalf("flags.storyID = %d, want 7004653782", flags.storyID)
	}
	if flags.configPath != "cfg.toml" {
		t.Fatalf("flags.configPath = %q, want cfg.toml", flags.configPath)
	}
	if flags.agentName != "codex" {
		t.Fatalf("flags.agentName = %q, want codex (default)", flags.agentName)
	}
	if flags.noVerify || flags.noCommitCheck {
		t.Fatalf("flags noVerify=%v noCommitCheck=%v, want both false", flags.noVerify, flags.noCommitCheck)
	}
	if strings.TrimSpace(flags.shared.stateDir) == "" {
		t.Fatal("flags.shared.stateDir is empty, want defaulted")
	}
}

func TestParseFeishuRunFlagsStoryIDAfterFlags(t *testing.T) {
	flags, err := parseFeishuRunFlags([]string{"-c", "cfg.toml", "--agent", "claude", "42"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFeishuRunFlags() error = %v", err)
	}
	if flags.storyID != 42 {
		t.Fatalf("flags.storyID = %d, want 42", flags.storyID)
	}
	if flags.agentName != "claude" {
		t.Fatalf("flags.agentName = %q, want claude", flags.agentName)
	}
}

func TestParseFeishuRunFlagsSharedAndSkipFlags(t *testing.T) {
	flags, err := parseFeishuRunFlags([]string{
		"7", "-c", "cfg.toml",
		"--no-verify", "--no-commit-check",
		"--state-dir", "/tmp/run-x", "--approval", "manual",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFeishuRunFlags() error = %v", err)
	}
	if !flags.noVerify || !flags.noCommitCheck {
		t.Fatalf("flags noVerify=%v noCommitCheck=%v, want both true", flags.noVerify, flags.noCommitCheck)
	}
	if flags.shared.stateDir != "/tmp/run-x" {
		t.Fatalf("flags.shared.stateDir = %q, want /tmp/run-x", flags.shared.stateDir)
	}
	if flags.shared.approval != "manual" {
		t.Fatalf("flags.shared.approval = %q, want manual", flags.shared.approval)
	}
}

func TestParseFeishuRunFlagsMissingConfig(t *testing.T) {
	_, err := parseFeishuRunFlags([]string{"7"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseFeishuRunFlags() error = nil, want missing config error")
	}
	if !strings.Contains(err.Error(), "--config") {
		t.Fatalf("parseFeishuRunFlags() error = %v, want config required", err)
	}
}

func TestParseFeishuRunFlagsMissingStoryID(t *testing.T) {
	_, err := parseFeishuRunFlags([]string{"-c", "cfg.toml"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseFeishuRunFlags() error = nil, want missing story-id error")
	}
	if !strings.Contains(err.Error(), "<story-id> is required") {
		t.Fatalf("parseFeishuRunFlags() error = %v, want story-id required", err)
	}
}

func TestParseFeishuRunFlagsInvalidStoryID(t *testing.T) {
	_, err := parseFeishuRunFlags([]string{"not-an-int", "-c", "cfg.toml"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseFeishuRunFlags() error = nil, want invalid story-id error")
	}
	if !strings.Contains(err.Error(), "invalid story-id") {
		t.Fatalf("parseFeishuRunFlags() error = %v, want invalid story-id", err)
	}
}

func TestParseFeishuRunFlagsInvalidAgent(t *testing.T) {
	_, err := parseFeishuRunFlags([]string{"7", "-c", "cfg.toml", "--agent", "gemini"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseFeishuRunFlags() error = nil, want invalid agent error")
	}
	if !strings.Contains(err.Error(), "invalid --agent") {
		t.Fatalf("parseFeishuRunFlags() error = %v, want invalid agent", err)
	}
}

func TestParseFeishuRunFlagsUnknownFlag(t *testing.T) {
	_, err := parseFeishuRunFlags([]string{"7", "-c", "cfg.toml", "--bogus"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseFeishuRunFlags() error = nil, want unknown flag error")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("parseFeishuRunFlags() error = %v, want unknown flag", err)
	}
}

func TestParseFeishuRunFlagsHelp(t *testing.T) {
	_, err := parseFeishuRunFlags([]string{"--help"}, &bytes.Buffer{})
	if !isHelpRequested(err) {
		t.Fatalf("parseFeishuRunFlags() error = %v, want help requested", err)
	}
}

func TestBuildFeishuRunPlanStoryNotFound(t *testing.T) {
	stories := []feishuproject.Story{{ID: 1, RepoPath: "/repo"}}
	_, _, err := buildFeishuRunPlan(stories, feishuRunFlags{storyID: 999, agentName: "codex"})
	if err == nil {
		t.Fatal("buildFeishuRunPlan() error = nil, want story not found")
	}
	if !strings.Contains(err.Error(), "story 999 not found") {
		t.Fatalf("buildFeishuRunPlan() error = %v, want story not found", err)
	}
}

func TestBuildFeishuRunPlanMissingRepoPath(t *testing.T) {
	stories := []feishuproject.Story{{ID: 7, RepoPath: "   "}}
	_, _, err := buildFeishuRunPlan(stories, feishuRunFlags{storyID: 7, agentName: "codex"})
	if err == nil {
		t.Fatal("buildFeishuRunPlan() error = nil, want missing repo_path")
	}
	if !strings.Contains(err.Error(), "has no repo_path mapping") {
		t.Fatalf("buildFeishuRunPlan() error = %v, want repo_path mapping error", err)
	}
}

func TestBuildFeishuRunPlanHappyPathCompiles(t *testing.T) {
	stories := []feishuproject.Story{{ID: 7, Description: "do the thing", RepoPath: "/repo"}}
	compiled, repoPath, err := buildFeishuRunPlan(stories, feishuRunFlags{storyID: 7, agentName: "codex"})
	if err != nil {
		t.Fatalf("buildFeishuRunPlan() error = %v", err)
	}
	if compiled == nil {
		t.Fatal("buildFeishuRunPlan() compiled = nil, want compiled workflow")
	}
	// Default flags (no skip) → agent + verify + commit_check.
	if len(compiled.Steps) != 3 {
		t.Fatalf("len(compiled.Steps) = %d, want 3", len(compiled.Steps))
	}
	// Coverage #1: the story repo path must be surfaced so runtime wiring can
	// target it via the shared --repo flag.
	if repoPath != "/repo" {
		t.Fatalf("buildFeishuRunPlan() repoPath = %q, want /repo", repoPath)
	}
}

// TestBuildFeishuRunPlanPropagatesRepoPath is Oracle coverage gap #1: a story
// with a RepoPath and no conflicting --repo flag must surface that path so
// runFeishuRun can set sharedFlags.repo from it.
func TestBuildFeishuRunPlanPropagatesRepoPath(t *testing.T) {
	stories := []feishuproject.Story{{ID: 7, Description: "do the thing", RepoPath: "/tmp/repo"}}
	_, repoPath, err := buildFeishuRunPlan(stories, feishuRunFlags{storyID: 7, agentName: "codex"})
	if err != nil {
		t.Fatalf("buildFeishuRunPlan() error = %v", err)
	}
	if repoPath != "/tmp/repo" {
		t.Fatalf("buildFeishuRunPlan() repoPath = %q, want /tmp/repo", repoPath)
	}
}

// TestBuildFeishuRunPlanRejectsConflictingRepo verifies that a user --repo that
// disagrees with the story mapping is rejected rather than silently ignored.
func TestBuildFeishuRunPlanRejectsConflictingRepo(t *testing.T) {
	stories := []feishuproject.Story{{ID: 7, Description: "do the thing", RepoPath: "/tmp/repo"}}
	flags := feishuRunFlags{storyID: 7, agentName: "codex"}
	flags.shared.repo = "/somewhere/else"
	_, _, err := buildFeishuRunPlan(stories, flags)
	if err == nil {
		t.Fatal("buildFeishuRunPlan() error = nil, want repo conflict")
	}
	if !strings.Contains(err.Error(), "conflicts with story") {
		t.Fatalf("buildFeishuRunPlan() error = %v, want repo conflict", err)
	}
}

// TestBuildFeishuRunPlanAllowsMatchingRepo verifies an explicit --repo equal to
// the story mapping is accepted (no false-positive conflict).
func TestBuildFeishuRunPlanAllowsMatchingRepo(t *testing.T) {
	stories := []feishuproject.Story{{ID: 7, Description: "do the thing", RepoPath: "/tmp/repo"}}
	flags := feishuRunFlags{storyID: 7, agentName: "codex"}
	flags.shared.repo = "/tmp/repo"
	_, repoPath, err := buildFeishuRunPlan(stories, flags)
	if err != nil {
		t.Fatalf("buildFeishuRunPlan() error = %v", err)
	}
	if repoPath != "/tmp/repo" {
		t.Fatalf("buildFeishuRunPlan() repoPath = %q, want /tmp/repo", repoPath)
	}
}

func TestBuildFeishuRunPlanSkipsVerifyAndCommit(t *testing.T) {
	stories := []feishuproject.Story{{ID: 7, Description: "do the thing", RepoPath: "/repo"}}
	compiled, _, err := buildFeishuRunPlan(stories, feishuRunFlags{
		storyID:       7,
		agentName:     "codex",
		noVerify:      true,
		noCommitCheck: true,
	})
	if err != nil {
		t.Fatalf("buildFeishuRunPlan() error = %v", err)
	}
	if len(compiled.Steps) != 1 {
		t.Fatalf("len(compiled.Steps) = %d, want 1", len(compiled.Steps))
	}
}

func TestFindStory(t *testing.T) {
	stories := []feishuproject.Story{{ID: 1}, {ID: 2}, {ID: 3}}
	story, ok := findStory(stories, 2)
	if !ok || story.ID != 2 {
		t.Fatalf("findStory(2) = (%#v, %v), want id 2 found", story, ok)
	}
	if _, ok := findStory(stories, 99); ok {
		t.Fatal("findStory(99) found = true, want false")
	}
}

func TestFeishuRunCommandListedInRegistry(t *testing.T) {
	cmd, ok := feishuCommandRegistry.Lookup("run")
	if !ok {
		t.Fatal("feishuCommandRegistry.Lookup(\"run\") = false, want command registered")
	}
	if cmd.Name() != "run" {
		t.Fatalf("cmd.Name() = %q, want run", cmd.Name())
	}
}
