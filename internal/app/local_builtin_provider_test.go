package app

import (
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/provider"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

func TestLookupRegisteredProviderSupportsBuiltinLocalProviders(t *testing.T) {
	lookup := newProviderLookup(newProviderResolverChain(
		builtinProviderResolver(),
		registeredProviderResolver(),
	), providerOptionDefaults{})

	for _, providerName := range []string{"reviewer", "writer"} {
		t.Run(providerName, func(t *testing.T) {
			resolvedProvider, err := lookup(workflow.CompiledStep{
				StepSpec: workflow.StepSpec{
					ID:    "agent-step",
					Kind:  workflow.StepKindAgent,
					Agent: &workflow.AgentStepSpec{Agent: providerName, Prompt: "test prompt"},
				},
			})
			if err != nil {
				t.Fatalf("lookupRegisteredProvider() error = %v", err)
			}

			execution, err := resolvedProvider.Start(t.Context(), provider.StartRequest{
				RunID:     "run-1",
				StepID:    "agent-step",
				AttemptID: "attempt-1",
			})
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if execution.State != provider.ExecutionStateSucceeded {
				t.Fatalf("execution.State = %q, want %q", execution.State, provider.ExecutionStateSucceeded)
			}
		})
	}
}

func TestLookupRegisteredProviderRejectsUnknownProvider(t *testing.T) {
	lookup := newProviderLookup(newProviderResolverChain(
		builtinProviderResolver(),
		registeredProviderResolver(),
	), providerOptionDefaults{})

	_, err := lookup(workflow.CompiledStep{
		StepSpec: workflow.StepSpec{
			ID:    "agent-step",
			Kind:  workflow.StepKindAgent,
			Agent: &workflow.AgentStepSpec{Agent: "definitely-unknown-provider", Prompt: "test prompt"},
		},
	})
	if err == nil {
		t.Fatal("lookupRegisteredProvider() error = nil, want unknown provider failure")
	}
	if !strings.Contains(err.Error(), `provider "definitely-unknown-provider" is not registered`) {
		t.Fatalf("lookupRegisteredProvider() error = %v", err)
	}
}

func TestProviderResolverChainUsesFirstMatch(t *testing.T) {
	chain := newProviderResolverChain(
		providerResolverFunc(func(providerName string, _ provider.Options) (provider.Provider, bool) {
			if providerName != "reviewer" {
				return nil, false
			}

			return builtinLocalProvider{provider: "first"}, true
		}),
		providerResolverFunc(func(providerName string, _ provider.Options) (provider.Provider, bool) {
			if providerName != "reviewer" {
				return nil, false
			}

			return builtinLocalProvider{provider: "second"}, true
		}),
	)

	resolvedProvider, ok := chain.Resolve("reviewer", provider.Options{})
	if !ok {
		t.Fatal("Resolve() ok = false, want true")
	}

	execution, err := resolvedProvider.Start(t.Context(), provider.StartRequest{RunID: "run-1", StepID: "agent-step", AttemptID: "attempt-1"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !strings.Contains(execution.Summary, "first") {
		t.Fatalf("execution.Summary = %q, want first resolver result", execution.Summary)
	}
}
