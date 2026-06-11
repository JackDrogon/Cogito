package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgentsRunFlagsPromptBeforeFlags(t *testing.T) {
	flags, err := parseAgentsRunFlags([]string{"refactor auth module", "-p", "claude"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseAgentsRunFlags() error = %v", err)
	}
	if flags.prompt != "refactor auth module" {
		t.Fatalf("flags.prompt = %q, want refactor auth module", flags.prompt)
	}
	if flags.agentName != "claude" {
		t.Fatalf("flags.agentName = %q, want claude", flags.agentName)
	}
	if strings.TrimSpace(flags.shared.stateDir) == "" {
		t.Fatal("flags.shared.stateDir is empty, want defaulted")
	}
}

func TestParseAgentsRunFlagsPromptAfterFlags(t *testing.T) {
	flags, err := parseAgentsRunFlags([]string{"-p", "claude", "refactor auth module"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseAgentsRunFlags() error = %v", err)
	}
	if flags.prompt != "refactor auth module" {
		t.Fatalf("flags.prompt = %q, want refactor auth module", flags.prompt)
	}
	if flags.agentName != "claude" {
		t.Fatalf("flags.agentName = %q, want claude", flags.agentName)
	}
}

func TestParseAgentsRunFlagsDefaultsToCodex(t *testing.T) {
	flags, err := parseAgentsRunFlags([]string{"do the thing"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseAgentsRunFlags() error = %v", err)
	}
	if flags.agentName != "codex" {
		t.Fatalf("flags.agentName = %q, want codex (default)", flags.agentName)
	}
	if flags.noVerify || flags.noCommitCheck {
		t.Fatalf("flags noVerify=%v noCommitCheck=%v, want both false", flags.noVerify, flags.noCommitCheck)
	}
}

func TestParseAgentsRunFlagsAgentLongFlag(t *testing.T) {
	flags, err := parseAgentsRunFlags([]string{"--agent", "opencode", "do the thing"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseAgentsRunFlags() error = %v", err)
	}
	if flags.agentName != "opencode" {
		t.Fatalf("flags.agentName = %q, want opencode", flags.agentName)
	}
}

func TestParseAgentsRunFlagsSharedAndSkipFlags(t *testing.T) {
	flags, err := parseAgentsRunFlags([]string{
		"do the thing",
		"--no-verify", "--no-commit-check",
		"--state-dir", "/tmp/run-x", "--approval", "manual",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseAgentsRunFlags() error = %v", err)
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

func TestParseAgentsRunFlagsMissingPrompt(t *testing.T) {
	_, err := parseAgentsRunFlags([]string{"-p", "claude"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseAgentsRunFlags() error = nil, want missing prompt error")
	}
	if !strings.Contains(err.Error(), "<prompt> is required") {
		t.Fatalf("parseAgentsRunFlags() error = %v, want prompt required", err)
	}
}

func TestParseAgentsRunFlagsBlankPrompt(t *testing.T) {
	_, err := parseAgentsRunFlags([]string{"   "}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseAgentsRunFlags() error = nil, want missing prompt error")
	}
	if !strings.Contains(err.Error(), "<prompt> is required") {
		t.Fatalf("parseAgentsRunFlags() error = %v, want prompt required", err)
	}
}

func TestParseAgentsRunFlagsExtraPositionals(t *testing.T) {
	_, err := parseAgentsRunFlags([]string{"-p", "claude", "refactor", "auth", "module"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseAgentsRunFlags() error = nil, want unexpected positional error")
	}
	if !strings.Contains(err.Error(), "unexpected positional arguments") {
		t.Fatalf("parseAgentsRunFlags() error = %v, want unexpected positional", err)
	}
}

func TestParseAgentsRunFlagsInvalidAgent(t *testing.T) {
	_, err := parseAgentsRunFlags([]string{"do the thing", "-p", "gemini"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseAgentsRunFlags() error = nil, want invalid agent error")
	}
	if !strings.Contains(err.Error(), "invalid -p/--agent") {
		t.Fatalf("parseAgentsRunFlags() error = %v, want invalid agent", err)
	}
}

func TestParseAgentsRunFlagsUnknownFlag(t *testing.T) {
	_, err := parseAgentsRunFlags([]string{"do the thing", "--bogus"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseAgentsRunFlags() error = nil, want unknown flag error")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("parseAgentsRunFlags() error = %v, want unknown flag", err)
	}
}

func TestParseAgentsRunFlagsHelp(t *testing.T) {
	_, err := parseAgentsRunFlags([]string{"--help"}, &bytes.Buffer{})
	if !isHelpRequested(err) {
		t.Fatalf("parseAgentsRunFlags() error = %v, want help requested", err)
	}
}

func TestBuildAgentsRunPlanHappyPathCompiles(t *testing.T) {
	flags := agentsRunFlags{prompt: "refactor auth module", agentName: "claude"}
	flags.shared.repo = "/tmp/repo"

	plan, err := buildAgentsRunPlan(flags)
	if err != nil {
		t.Fatalf("buildAgentsRunPlan() error = %v", err)
	}
	if plan.compiled == nil {
		t.Fatal("buildAgentsRunPlan() plan.compiled = nil, want plan.compiled workflow")
	}
	// Default flags (no skip) → agent + verify + commit_check.
	if len(plan.compiled.Steps) != 3 {
		t.Fatalf("len(plan.compiled.Steps) = %d, want 3", len(plan.compiled.Steps))
	}
	if plan.repoPath != "/tmp/repo" {
		t.Fatalf("buildAgentsRunPlan() plan.repoPath = %q, want /tmp/repo", plan.repoPath)
	}
}

func TestBuildAgentsRunPlanSkipsVerifyAndCommit(t *testing.T) {
	flags := agentsRunFlags{
		prompt:        "do the thing",
		agentName:     "codex",
		noVerify:      true,
		noCommitCheck: true,
	}
	flags.shared.repo = "/tmp/repo"

	plan, err := buildAgentsRunPlan(flags)
	if err != nil {
		t.Fatalf("buildAgentsRunPlan() error = %v", err)
	}
	if len(plan.compiled.Steps) != 1 {
		t.Fatalf("len(plan.compiled.Steps) = %d, want 1", len(plan.compiled.Steps))
	}
}

// TestBuildAgentsRunPlanDefaultsRepoToCwd: an empty --repo must resolve to the
// canonical absolute current directory so the prompt and the runtime wiring
// agree on one root.
func TestBuildAgentsRunPlanDefaultsRepoToCwd(t *testing.T) {
	cwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}

	plan, err := buildAgentsRunPlan(agentsRunFlags{prompt: "do the thing", agentName: "codex"})
	if err != nil {
		t.Fatalf("buildAgentsRunPlan() error = %v", err)
	}
	if plan.repoPath != cwd {
		t.Fatalf("buildAgentsRunPlan() plan.repoPath = %q, want cwd %q", plan.repoPath, cwd)
	}
}

func TestBuildAgentsRunPlanCanonicalizesRepo(t *testing.T) {
	flags := agentsRunFlags{prompt: "do the thing", agentName: "codex"}
	flags.shared.repo = "/tmp/repo/"

	plan, err := buildAgentsRunPlan(flags)
	if err != nil {
		t.Fatalf("buildAgentsRunPlan() error = %v", err)
	}
	if plan.repoPath != "/tmp/repo" {
		t.Fatalf("buildAgentsRunPlan() plan.repoPath = %q, want /tmp/repo", plan.repoPath)
	}
}

func TestAgentsRunCommandListedInRegistry(t *testing.T) {
	cmd, ok := agentsCommandRegistry.Lookup("run")
	if !ok {
		t.Fatal("agentsCommandRegistry.Lookup(\"run\") = false, want command registered")
	}
	if cmd.Name() != "run" {
		t.Fatalf("cmd.Name() = %q, want run", cmd.Name())
	}
}
