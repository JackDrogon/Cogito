package app

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/JackDrogon/Cogito/internal/provider"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type providerResolver interface {
	Resolve(providerName string, options provider.Options) (provider.Provider, bool)
}

type providerResolverFunc func(providerName string, options provider.Options) (provider.Provider, bool)

func (f providerResolverFunc) Resolve(providerName string, options provider.Options) (provider.Provider, bool) {
	return f(providerName, options)
}

type providerResolverChain struct {
	resolvers []providerResolver
}

func newProviderResolverChain(resolvers ...providerResolver) providerResolverChain {
	return providerResolverChain{resolvers: append([]providerResolver(nil), resolvers...)}
}

func (c providerResolverChain) Resolve(providerName string, options provider.Options) (provider.Provider, bool) {
	for _, resolver := range c.resolvers {
		if resolver == nil {
			continue
		}

		adapter, ok := resolver.Resolve(providerName, options)
		if ok {
			return adapter, true
		}
	}

	return nil, false
}

// providerOptionDefaults holds the run-scoped inputs used to build per-step
// adapter options. Sandbox/Model are run-wide; LogDirRoot is the run directory
// under which each step gets its own provider-logs subdirectory; LiveSink is
// the optional console stream for verbose live output.
type providerOptionDefaults struct {
	Sandbox    string
	Model      string
	LogDirRoot string
	LiveSink   io.Writer
}

func newProviderLookup(resolver providerResolver, defaults providerOptionDefaults) runtime.ProviderLookup {
	return func(step workflow.CompiledStep) (provider.Provider, error) {
		if step.Agent == nil {
			return nil, fmt.Errorf("agent config missing for step %q", step.ID)
		}

		providerName := strings.TrimSpace(step.Agent.Agent)

		options := provider.Options{
			Sandbox:  defaults.Sandbox,
			Model:    defaults.Model,
			LogDir:   stepLogDir(defaults.LogDirRoot, step.ID),
			LiveSink: defaults.LiveSink,
		}

		adapter, ok := resolver.Resolve(providerName, options)
		if !ok {
			return nil, fmt.Errorf("provider %q is not registered", providerName)
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

func builtinProviderResolver() providerResolver {
	return providerResolverFunc(func(providerName string, _ provider.Options) (provider.Provider, bool) {
		return lookupBuiltinLocalProvider(providerName)
	})
}

func registeredProviderResolver() providerResolver {
	return providerResolverFunc(func(providerName string, options provider.Options) (provider.Provider, bool) {
		registration, ok := provider.Lookup(providerName)
		if !ok {
			return nil, false
		}

		return registration.Build(options), true
	})
}

func defaultProviderLookup(defaults providerOptionDefaults) runtime.ProviderLookup {
	return newProviderLookup(newProviderResolverChain(
		builtinProviderResolver(),
		registeredProviderResolver(),
	), defaults)
}
