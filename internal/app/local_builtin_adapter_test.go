package app

import (
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

func TestLookupRegisteredAdapterSupportsBuiltinLocalProviders(t *testing.T) {
	lookup := newAdapterLookup(newAdapterResolverChain(
		builtinAdapterResolver(),
		registeredAdapterResolver(),
	))

	for _, provider := range []string{"reviewer", "writer"} {
		t.Run(provider, func(t *testing.T) {
			adapter, err := lookup(workflow.CompiledStep{
				StepSpec: workflow.StepSpec{
					ID:    "agent-step",
					Kind:  workflow.StepKindAgent,
					Agent: &workflow.AgentStepSpec{Agent: provider, Prompt: "test prompt"},
				},
			})
			if err != nil {
				t.Fatalf("lookupRegisteredAdapter() error = %v", err)
			}

			execution, err := adapter.Start(t.Context(), adapters.StartRequest{
				RunID:     "run-1",
				StepID:    "agent-step",
				AttemptID: "attempt-1",
			})
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if execution.State != adapters.ExecutionStateSucceeded {
				t.Fatalf("execution.State = %q, want %q", execution.State, adapters.ExecutionStateSucceeded)
			}
		})
	}
}

func TestLookupRegisteredAdapterRejectsUnknownProvider(t *testing.T) {
	lookup := newAdapterLookup(newAdapterResolverChain(
		builtinAdapterResolver(),
		registeredAdapterResolver(),
	))

	_, err := lookup(workflow.CompiledStep{
		StepSpec: workflow.StepSpec{
			ID:    "agent-step",
			Kind:  workflow.StepKindAgent,
			Agent: &workflow.AgentStepSpec{Agent: "definitely-unknown-provider", Prompt: "test prompt"},
		},
	})
	if err == nil {
		t.Fatal("lookupRegisteredAdapter() error = nil, want unknown provider failure")
	}
	if !strings.Contains(err.Error(), `adapter "definitely-unknown-provider" is not registered`) {
		t.Fatalf("lookupRegisteredAdapter() error = %v", err)
	}
}

func TestAdapterResolverChainUsesFirstMatch(t *testing.T) {
	chain := newAdapterResolverChain(
		adapterResolverFunc(func(provider string) (adapters.Adapter, bool) {
			if provider != "reviewer" {
				return nil, false
			}

			return builtinLocalAdapter{provider: "first"}, true
		}),
		adapterResolverFunc(func(provider string) (adapters.Adapter, bool) {
			if provider != "reviewer" {
				return nil, false
			}

			return builtinLocalAdapter{provider: "second"}, true
		}),
	)

	adapter, ok := chain.Resolve("reviewer")
	if !ok {
		t.Fatal("Resolve() ok = false, want true")
	}

	execution, err := adapter.Start(t.Context(), adapters.StartRequest{RunID: "run-1", StepID: "agent-step", AttemptID: "attempt-1"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !strings.Contains(execution.Summary, "first") {
		t.Fatalf("execution.Summary = %q, want first resolver result", execution.Summary)
	}
}
