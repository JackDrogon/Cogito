package workflow

import (
	"fmt"
	"strings"
)

type stepKindDescriptor struct {
	required  []string
	forbidden []string
	bind      func(values map[string]string, raw rawStep, step *StepSpec) error
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
			bind: func(values map[string]string, _ rawStep, step *StepSpec) error {
				step.Agent = &AgentStepSpec{Agent: values["agent"], Prompt: values["prompt"]}
				return nil
			},
		}, true
	case StepKindCommand:
		return stepKindDescriptor{
			required:  []string{"command"},
			forbidden: []string{"agent", "prompt", "message"},
			bind: func(values map[string]string, _ rawStep, step *StepSpec) error {
				step.Command = &CommandStepSpec{Command: values["command"]}
				return nil
			},
		}, true
	case StepKindApproval:
		return stepKindDescriptor{
			required:  []string{"message"},
			forbidden: []string{"agent", "prompt", "command"},
			bind: func(values map[string]string, _ rawStep, step *StepSpec) error {
				step.Approval = &ApprovalStepSpec{Message: values["message"]}
				return nil
			},
		}, true
	case StepKindVerify:
		return stepKindDescriptor{
			required:  nil,
			forbidden: []string{"agent", "prompt", "command", "message"},
			bind:      bindVerifyStep,
		}, true
	case StepKindCommitCheck:
		return stepKindDescriptor{
			required:  []string{"from"},
			forbidden: []string{"agent", "prompt", "command", "message"},
			bind:      bindCommitCheckStep,
		}, true
	default:
		return stepKindDescriptor{}, false
	}
}

// bindVerifyStep binds a verify step, enforcing the commands/from XOR that the
// string-based required/forbidden descriptors cannot express: commands is a
// list and from is a scalar, so the conflict is resolved here.
func bindVerifyStep(_ map[string]string, raw rawStep, step *StepSpec) error {
	commands := trimNonEmpty(raw.Commands)
	from := trimPointer(raw.From)

	hasCommands := len(commands) > 0
	hasFrom := from != ""

	switch {
	case hasCommands && hasFrom:
		return newError(ErrorCodeSchema, fmt.Sprintf("verify step %q must specify either commands or from, not both", step.ID))
	case !hasCommands && !hasFrom:
		return newError(ErrorCodeSchema, fmt.Sprintf("verify step %q must specify either commands or from", step.ID))
	}

	step.Verify = &VerifyStepSpec{Commands: commands, From: from}
	return nil
}

// bindCommitCheckStep binds a commit_check step. The required from field is
// validated by the descriptor; commands is rejected here because it is a list
// field outside the scalar forbidden set.
func bindCommitCheckStep(values map[string]string, raw rawStep, step *StepSpec) error {
	if len(trimNonEmpty(raw.Commands)) > 0 {
		return newError(ErrorCodeSchema, fmt.Sprintf("step %q field %q is not allowed for kind %q", step.ID, "commands", StepKindCommitCheck))
	}

	spec := &CommitCheckStepSpec{From: values["from"]}
	if raw.RequireSome != nil {
		spec.RequireSome = *raw.RequireSome
	}
	if raw.AllowDirty != nil {
		spec.AllowDirty = *raw.AllowDirty
	}

	step.CommitCheck = spec
	return nil
}

func trimNonEmpty(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		if v := strings.TrimSpace(value); v != "" {
			trimmed = append(trimmed, v)
		}
	}

	return trimmed
}

func trimPointer(value *string) string {
	if value == nil {
		return ""
	}

	return strings.TrimSpace(*value)
}

func rawStepFieldValues(step rawStep) map[string]*string {
	return map[string]*string{
		"agent":   step.Agent,
		"prompt":  step.Prompt,
		"command": step.Command,
		"message": step.Message,
		"from":    step.From,
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

	if err := descriptor.bind(values, params.Step, &params.Compiled); err != nil {
		return StepSpec{}, err
	}

	return params.Compiled, nil
}
