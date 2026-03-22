package workflow

import (
	"fmt"
	"strings"
)

// CompileWorkflow freezes the workflow graph after semantic and DAG validation.
func CompileWorkflow(spec *Spec) (*CompiledWorkflow, error) {
	if spec == nil {
		return nil, newError(ErrorCodeSchema, "workflow spec is required")
	}

	if err := validateSemantic(spec); err != nil {
		return nil, err
	}

	compiled := buildCompiledWorkflow(spec)
	if err := validateDAG(compiled); err != nil {
		return nil, err
	}

	return compiled, nil
}

func validateSemantic(spec *Spec) error {
	stepIndex := make(map[string]int, len(spec.Steps))
	for index, step := range spec.Steps {
		if _, exists := stepIndex[step.ID]; exists {
			return newError(ErrorCodeSemantic, fmt.Sprintf("duplicate step id %q", step.ID))
		}

		stepIndex[step.ID] = index
	}

	for _, step := range spec.Steps {
		seenDependencies := make(map[string]struct{}, len(step.Needs))
		for _, dependencyID := range step.Needs {
			dependencyID = strings.TrimSpace(dependencyID)
			if dependencyID == "" {
				return newError(ErrorCodeSemantic, fmt.Sprintf("step %q has an empty dependency id", step.ID))
			}

			if _, exists := seenDependencies[dependencyID]; exists {
				return newError(ErrorCodeSemantic, fmt.Sprintf("duplicate dependency id %q in step %q", dependencyID, step.ID))
			}

			seenDependencies[dependencyID] = struct{}{}

			if _, exists := stepIndex[dependencyID]; !exists {
				return newError(ErrorCodeSemantic, fmt.Sprintf("step %q depends on unknown step %q", step.ID, dependencyID))
			}
		}
	}

	return nil
}

func buildCompiledWorkflow(spec *Spec) *CompiledWorkflow {
	cloned := cloneSpec(spec)
	compiled := &CompiledWorkflow{
		Spec:      cloned,
		Steps:     make([]CompiledStep, len(cloned.Steps)),
		StepIndex: make(map[string]int, len(cloned.Steps)),
	}

	for index, step := range cloned.Steps {
		compiled.Steps[index] = CompiledStep{StepSpec: step}
		compiled.StepIndex[step.ID] = index
	}

	for _, step := range compiled.Steps {
		for _, dependencyID := range step.Needs {
			dependencyIndex := compiled.StepIndex[dependencyID]
			compiled.Steps[dependencyIndex].Dependents = append(compiled.Steps[dependencyIndex].Dependents, step.ID)
		}
	}

	for index := range compiled.Steps {
		sortStepIDsByDeclaration(compiled.Steps[index].Dependents, compiled.StepIndex)
	}

	return compiled
}

func validateDAG(compiled *CompiledWorkflow) error {
	indegree := make(map[string]int, len(compiled.Steps))
	ready := make([]string, 0, len(compiled.Steps))

	for _, step := range compiled.Steps {
		indegree[step.ID] = len(step.Needs)
		if len(step.Needs) == 0 {
			ready = append(ready, step.ID)
		}
	}

	sortStepIDsByDeclaration(ready, compiled.StepIndex)

	order := make([]string, 0, len(compiled.Steps))
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		order = append(order, current)

		step := compiled.Steps[compiled.StepIndex[current]]
		for _, dependentID := range step.Dependents {
			indegree[dependentID]--
			if indegree[dependentID] == 0 {
				ready = append(ready, dependentID)
				sortStepIDsByDeclaration(ready, compiled.StepIndex)
			}
		}
	}

	if len(order) != len(compiled.Steps) {
		remaining := make([]string, 0, len(compiled.Steps)-len(order))
		for _, step := range compiled.Steps {
			if indegree[step.ID] > 0 {
				remaining = append(remaining, step.ID)
			}
		}

		sortStepIDsByDeclaration(remaining, compiled.StepIndex)
		return newError(ErrorCodeSemantic, fmt.Sprintf("cycle detected involving steps: %s", strings.Join(remaining, ", ")))
	}

	compiled.TopologicalOrder = order
	return nil
}
