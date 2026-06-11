package agentflow

import (
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/adapters/prompt"
)

func TestBuildSpecRequiresTasks(t *testing.T) {
	_, err := BuildSpec(SpecOptions{Name: "n", RepoPath: "/repo"})
	if err == nil {
		t.Fatal("BuildSpec() error = nil, want at least one task required")
	}
	if !strings.Contains(err.Error(), "at least one task is required") {
		t.Fatalf("BuildSpec() error = %v, want task required", err)
	}
}

func TestBuildSpecSanitizesWorkflowName(t *testing.T) {
	spec, err := BuildSpec(SpecOptions{
		Name:     "ad hoc/run #1",
		RepoPath: "/repo",
		Tasks:    []prompt.TaskRef{{ID: "t", Text: "do it"}},
	})
	if err != nil {
		t.Fatalf("BuildSpec() error = %v", err)
	}
	if strings.ContainsAny(spec.Metadata.Name, " /#") {
		t.Fatalf("spec.Metadata.Name = %q, still contains invalid chars", spec.Metadata.Name)
	}
}

func TestIsValidAgentName(t *testing.T) {
	for _, name := range []string{"codex", "claude", "opencode"} {
		if !IsValidAgentName(name) {
			t.Fatalf("IsValidAgentName(%q) = false, want true", name)
		}
	}
	if IsValidAgentName("gemini") {
		t.Fatal("IsValidAgentName(\"gemini\") = true, want false")
	}
}
