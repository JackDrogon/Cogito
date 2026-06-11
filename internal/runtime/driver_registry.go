package runtime

import (
	"fmt"

	"github.com/JackDrogon/Cogito/internal/workflow"
)

// StepDriverFactory constructs a stepDriver for a given compiled step.
// Implementations receive the Engine so they can access shared dependencies
// such as the adapter lookup and command runner.
type StepDriverFactory interface {
	Build(engine *Engine, step workflow.CompiledStep) (stepDriver, error)
}

// StepDriverFactoryFunc is a function adapter that implements StepDriverFactory,
// allowing plain functions to be used wherever a StepDriverFactory is expected.
type StepDriverFactoryFunc func(engine *Engine, step workflow.CompiledStep) (stepDriver, error)

func (f StepDriverFactoryFunc) Build(engine *Engine, step workflow.CompiledStep) (stepDriver, error) {
	return f(engine, step)
}

// builtinStepKindCount sizes the factory map for the step kinds registered by
// NewStepDriverRegistry (agent, command, approval, verify, commit_check).
const builtinStepKindCount = 5

// StepDriverRegistry maps workflow step kinds to their StepDriverFactory
// implementations. The engine calls Build to obtain a driver for each step
// before execution begins.
type StepDriverRegistry struct {
	factories map[workflow.StepKind]StepDriverFactory
}

// NewStepDriverRegistry constructs a StepDriverRegistry pre-populated with
// factories for all built-in step kinds: agent, command, approval, verify,
// and commit_check.
func NewStepDriverRegistry() *StepDriverRegistry {
	registry := &StepDriverRegistry{factories: make(map[workflow.StepKind]StepDriverFactory, builtinStepKindCount)}

	registry.Register(
		workflow.StepKindAgent,
		StepDriverFactoryFunc(func(engine *Engine, step workflow.CompiledStep) (stepDriver, error) {
			if engine.lookupAdapter == nil {
				return nil, newError(ErrorCodeConfig, "adapter lookup is required for agent steps")
			}

			adapter, err := engine.lookupAdapter(step)
			if err != nil {
				return nil, err
			}

			return agentDriver{adapter: adapter}, nil
		}),
	)

	registry.Register(
		workflow.StepKindCommand,
		StepDriverFactoryFunc(func(engine *Engine, _ workflow.CompiledStep) (stepDriver, error) {
			if engine.commandRunner == nil {
				return nil, newError(ErrorCodeConfig, "command runner is required for command steps")
			}

			return commandDriver{runner: engine.commandRunner}, nil
		}),
	)

	registry.Register(
		workflow.StepKindApproval,
		StepDriverFactoryFunc(func(engine *Engine, _ workflow.CompiledStep) (stepDriver, error) {
			return approvalDriver{runID: engine.runID, ids: engine.idGen}, nil
		}),
	)

	registry.Register(
		workflow.StepKindVerify,
		StepDriverFactoryFunc(func(engine *Engine, _ workflow.CompiledStep) (stepDriver, error) {
			return verifyDriver{syncTerminalDriver: syncTerminalDriver{kind: "verify"}, engine: engine}, nil
		}),
	)

	registry.Register(
		workflow.StepKindCommitCheck,
		StepDriverFactoryFunc(func(engine *Engine, _ workflow.CompiledStep) (stepDriver, error) {
			return commitCheckDriver{syncTerminalDriver: syncTerminalDriver{kind: "commit_check"}, engine: engine}, nil
		}),
	)

	return registry
}

// Register associates a StepDriverFactory with a step kind. A nil receiver is
// a no-op, allowing safe use before the registry is fully initialized.
func (r *StepDriverRegistry) Register(kind workflow.StepKind, factory StepDriverFactory) {
	if r == nil {
		return
	}

	r.factories[kind] = factory
}

// Build looks up the factory for step.Kind and delegates to it. Returns an
// ErrorCodeConfig error when the registry is nil, the kind is unregistered,
// or the registered factory is nil.
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
