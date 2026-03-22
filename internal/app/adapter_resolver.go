package app

import (
	"fmt"
	"strings"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/runtime"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type adapterResolver interface {
	Resolve(provider string) (adapters.Adapter, bool)
}

type adapterResolverFunc func(provider string) (adapters.Adapter, bool)

func (f adapterResolverFunc) Resolve(provider string) (adapters.Adapter, bool) {
	return f(provider)
}

type adapterResolverChain struct {
	resolvers []adapterResolver
}

func newAdapterResolverChain(resolvers ...adapterResolver) adapterResolverChain {
	return adapterResolverChain{resolvers: append([]adapterResolver(nil), resolvers...)}
}

func (c adapterResolverChain) Resolve(provider string) (adapters.Adapter, bool) {
	for _, resolver := range c.resolvers {
		if resolver == nil {
			continue
		}

		adapter, ok := resolver.Resolve(provider)
		if ok {
			return adapter, true
		}
	}

	return nil, false
}

func newAdapterLookup(resolver adapterResolver) runtime.AdapterLookup {
	return func(step workflow.CompiledStep) (adapters.Adapter, error) {
		if step.Agent == nil {
			return nil, fmt.Errorf("agent config missing for step %q", step.ID)
		}

		provider := strings.TrimSpace(step.Agent.Agent)
		adapter, ok := resolver.Resolve(provider)
		if !ok {
			return nil, fmt.Errorf("adapter %q is not registered", provider)
		}

		return adapter, nil
	}
}

func builtinAdapterResolver() adapterResolver {
	return adapterResolverFunc(func(provider string) (adapters.Adapter, bool) {
		return lookupBuiltinLocalAdapter(provider)
	})
}

func registeredAdapterResolver() adapterResolver {
	return adapterResolverFunc(func(provider string) (adapters.Adapter, bool) {
		registration, ok := adapters.Lookup(provider)
		if !ok {
			return nil, false
		}

		return registration.New(), true
	})
}

func defaultAdapterLookup() runtime.AdapterLookup {
	return newAdapterLookup(newAdapterResolverChain(
		builtinAdapterResolver(),
		registeredAdapterResolver(),
	))
}
