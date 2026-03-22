package runtime

import (
	"fmt"

	"github.com/JackDrogon/Cogito/internal/workflow"
)

type StepDriverFactory interface {
	Build(engine *Engine, step workflow.CompiledStep) (stepDriver, error)
}

type StepDriverFactoryFunc func(engine *Engine, step workflow.CompiledStep) (stepDriver, error)

func (f StepDriverFactoryFunc) Build(engine *Engine, step workflow.CompiledStep) (stepDriver, error) {
	return f(engine, step)
}

type StepDriverRegistry struct {
	factories map[workflow.StepKind]StepDriverFactory
}

func NewStepDriverRegistry() *StepDriverRegistry {
	registry := &StepDriverRegistry{factories: make(map[workflow.StepKind]StepDriverFactory, 3)}
	registry.Register(workflow.StepKindAgent, StepDriverFactoryFunc(func(engine *Engine, step workflow.CompiledStep) (stepDriver, error) {
		if engine.lookupAdapter == nil {
			return nil, newError(ErrorCodeConfig, "adapter lookup is required for agent steps")
		}

		adapter, err := engine.lookupAdapter(step)
		if err != nil {
			return nil, err
		}

		return agentDriver{adapter: adapter}, nil
	}))
	registry.Register(workflow.StepKindCommand, StepDriverFactoryFunc(func(engine *Engine, _ workflow.CompiledStep) (stepDriver, error) {
		if engine.commandRunner == nil {
			return nil, newError(ErrorCodeConfig, "command runner is required for command steps")
		}

		return commandDriver{runner: engine.commandRunner}, nil
	}))
	registry.Register(workflow.StepKindApproval, StepDriverFactoryFunc(func(engine *Engine, _ workflow.CompiledStep) (stepDriver, error) {
		return approvalDriver{runID: engine.runID, ids: engine.ids}, nil
	}))

	return registry
}

func (r *StepDriverRegistry) Register(kind workflow.StepKind, factory StepDriverFactory) {
	if r == nil {
		return
	}

	r.factories[kind] = factory
}

func (r *StepDriverRegistry) Build(engine *Engine, step workflow.CompiledStep) (stepDriver, error) {
	if r == nil {
		return nil, newError(ErrorCodeConfig, "step driver registry is required")
	}

	factory, ok := r.factories[step.Kind]
	if !ok {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("unsupported step kind %q", step.Kind))
	}

	if factory == nil {
		return nil, newError(ErrorCodeConfig, fmt.Sprintf("step driver factory is required for %q", step.Kind))
	}

	return factory.Build(engine, step)
}
