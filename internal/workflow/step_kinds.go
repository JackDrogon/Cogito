package workflow

import "fmt"

type stepKindDescriptor struct {
	required  []string
	forbidden []string
	bind      func(values map[string]string, step *StepSpec)
}

type compileStepKindParams struct {
	ID       string
	Kind     StepKind
	Step     rawStep
	Compiled StepSpec
}

func lookupStepKindDescriptor(kind StepKind) (stepKindDescriptor, bool) {
	switch kind {
	case StepKindAgent:
		return stepKindDescriptor{
			required:  []string{"agent", "prompt"},
			forbidden: []string{"command", "message"},
			bind: func(values map[string]string, step *StepSpec) {
				step.Agent = &AgentStepSpec{Agent: values["agent"], Prompt: values["prompt"]}
			},
		}, true
	case StepKindCommand:
		return stepKindDescriptor{
			required:  []string{"command"},
			forbidden: []string{"agent", "prompt", "message"},
			bind: func(values map[string]string, step *StepSpec) {
				step.Command = &CommandStepSpec{Command: values["command"]}
			},
		}, true
	case StepKindApproval:
		return stepKindDescriptor{
			required:  []string{"message"},
			forbidden: []string{"agent", "prompt", "command"},
			bind: func(values map[string]string, step *StepSpec) {
				step.Approval = &ApprovalStepSpec{Message: values["message"]}
			},
		}, true
	default:
		return stepKindDescriptor{}, false
	}
}

func rawStepFieldValues(step rawStep) map[string]*string {
	return map[string]*string{
		"agent":   step.Agent,
		"prompt":  step.Prompt,
		"command": step.Command,
		"message": step.Message,
	}
}

func compileStepKindSpec(params compileStepKindParams) (StepSpec, error) {
	descriptor, ok := lookupStepKindDescriptor(params.Kind)
	if !ok {
		return StepSpec{}, newError(ErrorCodeSchema, fmt.Sprintf("step %q uses unsupported step kind %q", params.ID, params.Kind))
	}

	fields := rawStepFieldValues(params.Step)

	values, err := requiredStepFields(stepFieldValidationParams{
		StepID: params.ID,
		Kind:   params.Kind,
		Names:  descriptor.required,
		Fields: fields,
	})
	if err != nil {
		return StepSpec{}, err
	}

	if err := rejectStepFieldNames(stepFieldValidationParams{
		StepID: params.ID,
		Kind:   params.Kind,
		Names:  descriptor.forbidden,
		Fields: fields,
	}); err != nil {
		return StepSpec{}, err
	}

	descriptor.bind(values, &params.Compiled)

	return params.Compiled, nil
}
