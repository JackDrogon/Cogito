package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/provider"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

func TestRunVerifyCommandsAllSucceed(t *testing.T) {
	if failure := runVerifyCommands(t.Context(), t.TempDir(), []string{"true", "exit 0", " "}); failure != "" {
		t.Fatalf("runVerifyCommands() = %q, want empty", failure)
	}
}

func TestRunVerifyCommandsStopsAtFirstFailure(t *testing.T) {
	failure := runVerifyCommands(t.Context(), t.TempDir(), []string{"true", "echo boom >&2; exit 3", "true"})
	if failure == "" {
		t.Fatal("runVerifyCommands() = empty, want failure")
	}
	if !strings.Contains(failure, "verify failed") || !strings.Contains(failure, "boom") {
		t.Fatalf("runVerifyCommands() = %q, want failure with stderr snippet", failure)
	}
}

func verifyOnlySpec(commands []string) *workflow.Spec {
	return &workflow.Spec{
		Metadata: workflow.Metadata{Name: "verify-only"},
		Steps: []workflow.StepSpec{{
			ID:     "check",
			Kind:   workflow.StepKindVerify,
			Verify: &workflow.VerifyStepSpec{Commands: commands},
		}},
	}
}

func TestVerifyDriverStepSucceedsWhenCommandsPass(t *testing.T) {
	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test: t,
		Spec: verifyOnlySpec([]string{"true", "exit 0"}),
	})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	if state := fixture.engine.Snapshot().Steps["check"].State; state != StepStateSucceeded {
		t.Fatalf("check step state = %q, want %q", state, StepStateSucceeded)
	}
	if state := fixture.engine.Snapshot().State; state != RunStateSucceeded {
		t.Fatalf("run state = %q, want %q", state, RunStateSucceeded)
	}
}

func TestVerifyDriverStepFailsWhenAnyCommandNonZero(t *testing.T) {
	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test: t,
		Spec: verifyOnlySpec([]string{"true", "exit 7"}),
	})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	if state := fixture.engine.Snapshot().Steps["check"].State; state != StepStateFailed {
		t.Fatalf("check step state = %q, want %q", state, StepStateFailed)
	}
	if state := fixture.engine.Snapshot().State; state != RunStateFailed {
		t.Fatalf("run state = %q, want %q", state, RunStateFailed)
	}
}

func TestVerifyDriverPullsCommandsFromUpstreamAgent(t *testing.T) {
	spec := &workflow.Spec{
		Metadata: workflow.Metadata{Name: "agent-verify"},
		Steps: []workflow.StepSpec{
			{ID: "agent", Kind: workflow.StepKindAgent, Agent: &workflow.AgentStepSpec{Agent: "fake", Prompt: "do"}},
			{ID: "verify", Kind: workflow.StepKindVerify, Needs: []string{"agent"}, Verify: &workflow.VerifyStepSpec{From: "agent"}},
		},
	}

	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test: t,
		Spec: spec,
		Provider: provider.NewFakeProvider(provider.FakeConfig{
			Capabilities: provider.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true},
			Scripts: map[string]provider.FakeScript{
				"attempt-agent-01": {
					Start: provider.FakeSnapshot{State: provider.ExecutionStateRunning, Summary: "agent started"},
					Polls: []provider.FakeSnapshot{{
						State:            provider.ExecutionStateSucceeded,
						Summary:          "agent ok",
						StructuredOutput: json.RawMessage(`{"commits":[],"verification":["true"],"summary":"done"}`),
					}},
				},
			},
		}),
	})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	if state := fixture.engine.Snapshot().Steps["verify"].State; state != StepStateSucceeded {
		t.Fatalf("verify step state = %q, want %q", state, StepStateSucceeded)
	}
}

// agentVerifySpec builds the minimal agent -> verify(from: agent) workflow used
// by the missing/empty structured-output regression tests.
func agentVerifySpec() *workflow.Spec {
	return &workflow.Spec{
		Metadata: workflow.Metadata{Name: "agent-verify"},
		Steps: []workflow.StepSpec{
			{ID: "agent", Kind: workflow.StepKindAgent, Agent: &workflow.AgentStepSpec{Agent: "fake", Prompt: "do"}},
			{ID: "verify", Kind: workflow.StepKindVerify, Needs: []string{"agent"}, Verify: &workflow.VerifyStepSpec{From: "agent"}},
		},
	}
}

// TestVerifyDriverFailsWhenUpstreamHasNoStructuredOutput is Oracle coverage gap
// #3: an agent step that succeeds without emitting AGENT_RESULT_JSON persists a
// nil StructuredOutput, so a downstream verify(from: agent) MUST fail rather
// than trivially passing with zero commands.
func TestVerifyDriverFailsWhenUpstreamHasNoStructuredOutput(t *testing.T) {
	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test: t,
		Spec: agentVerifySpec(),
		Provider: provider.NewFakeProvider(provider.FakeConfig{
			Capabilities: provider.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true},
			Scripts: map[string]provider.FakeScript{
				"attempt-agent-01": {
					Start: provider.FakeSnapshot{State: provider.ExecutionStateRunning, Summary: "agent started"},
					Polls: []provider.FakeSnapshot{{
						State:   provider.ExecutionStateSucceeded,
						Summary: "agent ok",
						// No StructuredOutput: the agent forgot the marker line.
					}},
				},
			},
		}),
	})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	if state := fixture.engine.Snapshot().Steps["verify"].State; state != StepStateFailed {
		t.Fatalf("verify step state = %q, want %q", state, StepStateFailed)
	}
	if state := fixture.engine.Snapshot().State; state != RunStateFailed {
		t.Fatalf("run state = %q, want %q", state, RunStateFailed)
	}

	summary := fixture.engine.Snapshot().Steps["verify"].Summary
	if !strings.Contains(summary, "no structured output") {
		t.Fatalf("verify summary = %q, want mention of missing structured output", summary)
	}
}

// TestVerifyDriverFailsWhenUpstreamReportsNoCommands covers the sibling case:
// the agent emitted a valid AGENT_RESULT_JSON but with an empty verification
// array. Verify(from: agent) must still fail instead of passing with zero
// commands run.
func TestVerifyDriverFailsWhenUpstreamReportsNoCommands(t *testing.T) {
	fixture := newRuntimeMachineFixture(runtimeMachineFixtureParams{
		Test: t,
		Spec: agentVerifySpec(),
		Provider: provider.NewFakeProvider(provider.FakeConfig{
			Capabilities: provider.CapabilityMatrix{MachineReadableLogs: true, StructuredOutput: true},
			Scripts: map[string]provider.FakeScript{
				"attempt-agent-01": {
					Start: provider.FakeSnapshot{State: provider.ExecutionStateRunning, Summary: "agent started"},
					Polls: []provider.FakeSnapshot{{
						State:            provider.ExecutionStateSucceeded,
						Summary:          "agent ok",
						StructuredOutput: json.RawMessage(`{"commits":[],"verification":[],"summary":"done"}`),
					}},
				},
			},
		}),
	})

	if err := fixture.engine.ExecuteAll(t.Context()); err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	if state := fixture.engine.Snapshot().Steps["verify"].State; state != StepStateFailed {
		t.Fatalf("verify step state = %q, want %q", state, StepStateFailed)
	}

	summary := fixture.engine.Snapshot().Steps["verify"].Summary
	if !strings.Contains(summary, "no verification commands reported") {
		t.Fatalf("verify summary = %q, want mention of no verification commands reported", summary)
	}
}
