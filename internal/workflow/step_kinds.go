package workflow

import "fmt"

type stepKindDescriptor struct {
	required  []string
	forbidden []string
	bind      func(values map[string]string, step *StepSpec)
}

var stepKindDescriptors = map[StepKind]stepKindDescriptor{
	StepKindAgent: {
		required:  []string{"agent", "prompt"},
		forbidden: []string{"command", "message"},
		bind: func(values map[string]string, step *StepSpec) {
			step.Agent = &AgentStepSpec{Agent: values["agent"], Prompt: values["prompt"]}
		},
	},
	StepKindCommand: {
		required:  []string{"command"},
		forbidden: []string{"agent", "prompt", "message"},
		bind: func(values map[string]string, step *StepSpec) {
			step.Command = &CommandStepSpec{Command: values["command"]}
		},
	},
	StepKindApproval: {
		required:  []string{"message"},
		forbidden: []string{"agent", "prompt", "command"},
		bind: func(values map[string]string, step *StepSpec) {
			step.Approval = &ApprovalStepSpec{Message: values["message"]}
		},
	},
}

func lookupStepKindDescriptor(kind StepKind) (stepKindDescriptor, bool) {
	descriptor, ok := stepKindDescriptors[kind]
	return descriptor, ok
}

func rawStepFieldValues(step rawStep) map[string]*string {
	return map[string]*string{
		"agent":   step.Agent,
		"prompt":  step.Prompt,
		"command": step.Command,
		"message": step.Message,
	}
}

func compileStepKindSpec(id string, kind StepKind, step rawStep, compiled StepSpec) (StepSpec, error) {
	descriptor, ok := lookupStepKindDescriptor(kind)
	if !ok {
		return StepSpec{}, newError(ErrorCodeSchema, fmt.Sprintf("step %q uses unsupported step kind %q", id, kind))
	}

	fields := rawStepFieldValues(step)
	values, err := requiredStepFields(id, kind, descriptor.required, fields)
	if err != nil {
		return StepSpec{}, err
	}

	if err := rejectStepFieldNames(id, kind, descriptor.forbidden, fields); err != nil {
		return StepSpec{}, err
	}

	descriptor.bind(values, &compiled)
	return compiled, nil
}
