package app

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type adapterResolver interface {
	Resolve(provider string, options adapters.AdapterOptions) (adapters.Adapter, bool)
}

type adapterResolverFunc func(provider string, options adapters.AdapterOptions) (adapters.Adapter, bool)

func (f adapterResolverFunc) Resolve(provider string, options adapters.AdapterOptions) (adapters.Adapter, bool) {
	return f(provider, options)
}

type adapterResolverChain struct {
	resolvers []adapterResolver
}

func newAdapterResolverChain(resolvers ...adapterResolver) adapterResolverChain {
	return adapterResolverChain{resolvers: append([]adapterResolver(nil), resolvers...)}
}

func (c adapterResolverChain) Resolve(provider string, options adapters.AdapterOptions) (adapters.Adapter, bool) {
	for _, resolver := range c.resolvers {
		if resolver == nil {
			continue
		}

		adapter, ok := resolver.Resolve(provider, options)
		if ok {
			return adapter, true
		}
	}

	return nil, false
}

// adapterOptionDefaults holds the run-scoped inputs used to build per-step
// adapter options. Sandbox/Model are run-wide; LogDirRoot is the run directory
// under which each step gets its own provider-logs subdirectory; LiveSink is
// the optional console stream for verbose live output.
type adapterOptionDefaults struct {
	Sandbox    string
	Model      string
	LogDirRoot string
	LiveSink   io.Writer
}

func newAdapterLookup(resolver adapterResolver, defaults adapterOptionDefaults) runtime.AdapterLookup {
	return func(step workflow.CompiledStep) (adapters.Adapter, error) {
		if step.Agent == nil {
			return nil, fmt.Errorf("agent config missing for step %q", step.ID)
		}

		provider := strings.TrimSpace(step.Agent.Agent)

		options := adapters.AdapterOptions{
			Sandbox:  defaults.Sandbox,
			Model:    defaults.Model,
			LogDir:   stepLogDir(defaults.LogDirRoot, step.ID),
			LiveSink: defaults.LiveSink,
		}

		adapter, ok := resolver.Resolve(provider, options)
		if !ok {
			return nil, fmt.Errorf("adapter %q is not registered", provider)
		}

		return adapter, nil
	}
}

// stepLogDir mirrors the store layout (provider-logs/<step-id>) so adapter
// process logs land beside the durable stdout/stderr artifacts for the run.
func stepLogDir(root, stepID string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}

	return filepath.Join(root, providerLogsDir, stepID)
}

func builtinAdapterResolver() adapterResolver {
	return adapterResolverFunc(func(provider string, _ adapters.AdapterOptions) (adapters.Adapter, bool) {
		return lookupBuiltinLocalAdapter(provider)
	})
}

func registeredAdapterResolver() adapterResolver {
	return adapterResolverFunc(func(provider string, options adapters.AdapterOptions) (adapters.Adapter, bool) {
		registration, ok := adapters.Lookup(provider)
		if !ok {
			return nil, false
		}

		return registration.Build(options), true
	})
}

func defaultAdapterLookup(defaults adapterOptionDefaults) runtime.AdapterLookup {
	return newAdapterLookup(newAdapterResolverChain(
		builtinAdapterResolver(),
		registeredAdapterResolver(),
	), defaults)
}
