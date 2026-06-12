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
		if err := validateStepRetries(step); err != nil {
			return err
		}

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

	return validateStepReferences(spec, stepIndex)
}

func validateStepRetries(step StepSpec) error {
	if step.Retries < 0 {
		return newError(ErrorCodeSemantic, fmt.Sprintf("step %q retries must be >= 0", step.ID))
	}

	if step.Kind == StepKindApproval && step.Retries > 0 {
		return newError(ErrorCodeSemantic, fmt.Sprintf("step %q field %q is not allowed for kind %q", step.ID, "retries", step.Kind))
	}

	return nil
}

// validateStepReferences checks that every verify/commit_check `from:` field
// names an existing agent step that the referencing step actually depends on.
// Without this a verify step could read structured output from a non-agent step
// or run BEFORE the agent it claims to verify, silently producing garbage.
func validateStepReferences(spec *Spec, stepIndex map[string]int) error {
	scope := referenceScope{spec: spec, stepIndex: stepIndex}

	for _, step := range spec.Steps {
		switch step.Kind {
		case StepKindVerify:
			if step.Verify == nil {
				continue
			}

			if from := strings.TrimSpace(step.Verify.From); from != "" {
				if err := scope.validateFromReference(step, from); err != nil {
					return err
				}
			}
		case StepKindCommitCheck:
			if step.CommitCheck == nil {
				continue
			}

			if from := strings.TrimSpace(step.CommitCheck.From); from != "" {
				if err := scope.validateFromReference(step, from); err != nil {
					return err
				}
			}
		// Agent, command, and approval steps carry no cross-step references.
		case StepKindAgent, StepKindCommand, StepKindApproval:
		}
	}

	return nil
}

// referenceScope bundles the compiled-spec lookup state shared by the `from:`
// reference checks, keeping their methods within the project's three-parameter
// rule.
type referenceScope struct {
	spec      *Spec
	stepIndex map[string]int
}

// validateFromReference enforces the three rules a `from:` target must satisfy:
// it exists, it is an agent step, and it is reachable through the referencing
// step's needs (directly or transitively). A missing dependency is an explicit
// error rather than an auto-injected edge: the user opted into `from`, so they
// must also order the step after the agent they consume.
func (s referenceScope) validateFromReference(step StepSpec, from string) error {
	fromIndex, exists := s.stepIndex[from]
	if !exists {
		return newError(ErrorCodeSemantic, fmt.Sprintf("step %q references unknown step %q in from", step.ID, from))
	}

	if s.spec.Steps[fromIndex].Kind != StepKindAgent {
		return newError(ErrorCodeSemantic, fmt.Sprintf(
			"step %q from %q must reference an agent step, got kind %q",
			step.ID, from, s.spec.Steps[fromIndex].Kind))
	}

	if !s.dependsOn(step.ID, from) {
		return newError(ErrorCodeSemantic, fmt.Sprintf(
			"step %q references step %q in from but does not declare it in needs (directly or transitively)",
			step.ID, from))
	}

	return nil
}

// dependsOn reports whether stepID reaches targetID through the needs graph. It
// uses a visited set so a cycle (caught separately by validateDAG) cannot make
// this loop forever.
func (s referenceScope) dependsOn(stepID, targetID string) bool {
	spec, stepIndex := s.spec, s.stepIndex
	visited := make(map[string]struct{}, len(spec.Steps))
	queue := []string{stepID}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		index, ok := stepIndex[current]
		if !ok {
			continue
		}

		for _, need := range spec.Steps[index].Needs {
			need = strings.TrimSpace(need)
			if need == targetID {
				return true
			}

			if _, seen := visited[need]; seen {
				continue
			}

			visited[need] = struct{}{}

			queue = append(queue, need)
		}
	}

	return false
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

	for index := range compiled.Steps {
		step := &compiled.Steps[index]
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

	for index := range compiled.Steps {
		step := &compiled.Steps[index]
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
				// Re-sorting the ready queue on every insertion keeps the
				// topological order DETERMINISTIC (declaration order breaks
				// ties), which replay correctness depends on. Workflows are
				// CLI-sized, so the extra sorts are irrelevant in practice.
				sortStepIDsByDeclaration(ready, compiled.StepIndex)
			}
		}
	}

	if len(order) != len(compiled.Steps) {
		remaining := make([]string, 0, len(compiled.Steps)-len(order))

		for index := range compiled.Steps {
			step := &compiled.Steps[index]
			if indegree[step.ID] > 0 {
				remaining = append(remaining, step.ID)
			}
		}

		sortStepIDsByDeclaration(remaining, compiled.StepIndex)

		return newError(ErrorCodeSemantic, "cycle detected involving steps: "+strings.Join(remaining, ", "))
	}

	compiled.TopologicalOrder = order

	return nil
}
