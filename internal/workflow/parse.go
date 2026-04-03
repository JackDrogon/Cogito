package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type stepFieldValidationParams struct {
	StepID string
	Kind   StepKind
	Names  []string
	Fields map[string]*string
}

// ParseWorkflow decodes YAML, rejects unknown fields, and validates schema-level rules.
func ParseWorkflow(data []byte) (*Spec, error) {
	raw, err := decodeRawWorkflow(data)
	if err != nil {
		return nil, err
	}

	if err := validateDocumentVersion(raw); err != nil {
		return nil, err
	}

	return compileSpec(raw)
}

// LoadWorkflow runs the full workflow loading pipeline through DAG validation.
func LoadWorkflow(data []byte) (*CompiledWorkflow, error) {
	spec, err := ParseWorkflow(data)
	if err != nil {
		return nil, err
	}

	return CompileWorkflow(spec)
}

// LoadFile loads and validates a workflow file from disk.
func LoadFile(path string) (*CompiledWorkflow, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read workflow file: %w", err)
	}

	return LoadWorkflow(data)
}

func decodeRawWorkflow(data []byte) (rawWorkflow, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var raw rawWorkflow
	if err := decoder.Decode(&raw); err != nil {
		return rawWorkflow{}, classifyDecodeError(err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return rawWorkflow{}, classifyDecodeError(err)
		}

		return rawWorkflow{}, newError(ErrorCodeParse, "multiple YAML documents are not supported")
	}

	return raw, nil
}

func classifyDecodeError(err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "field ") && strings.Contains(message, "not found") {
		return wrapError(ErrorCodeSchema, "unknown field", err)
	}

	return wrapError(ErrorCodeParse, "invalid YAML", err)
}

func validateDocumentVersion(raw rawWorkflow) error {
	if strings.TrimSpace(raw.APIVersion) != v1Alpha1APIVersion {
		return newError(ErrorCodeVersion, fmt.Sprintf("unsupported apiVersion %q", raw.APIVersion))
	}

	if strings.TrimSpace(raw.Kind) != workflowKind {
		return newError(ErrorCodeSchema, fmt.Sprintf("unsupported resource kind %q", raw.Kind))
	}

	return nil
}

func compileSpec(raw rawWorkflow) (*Spec, error) {
	name := strings.TrimSpace(raw.Metadata.Name)
	if name == "" {
		return nil, newError(ErrorCodeSchema, "metadata.name is required")
	}

	if len(raw.Steps) == 0 {
		return nil, newError(ErrorCodeSchema, "at least one step is required")
	}

	spec := &Spec{
		APIVersion: raw.APIVersion,
		Kind:       raw.Kind,
		Metadata: Metadata{
			Name: name,
		},
		Vars:  cloneVars(raw.Vars),
		Steps: make([]StepSpec, 0, len(raw.Steps)),
	}

	for index, step := range raw.Steps {
		compiledStep, err := compileStep(step, index)
		if err != nil {
			return nil, err
		}

		spec.Steps = append(spec.Steps, compiledStep)
	}

	return spec, nil
}

func compileStep(step rawStep, position int) (StepSpec, error) {
	id := strings.TrimSpace(step.ID)
	if id == "" {
		return StepSpec{}, newError(ErrorCodeSchema, fmt.Sprintf("step %d id is required", position+1))
	}

	kind := StepKind(strings.TrimSpace(step.Kind))
	if kind == "" {
		return StepSpec{}, newError(ErrorCodeSchema, fmt.Sprintf("step %q kind is required", id))
	}

	compiled := StepSpec{
		ID:    id,
		Kind:  kind,
		Needs: cloneStrings(step.Needs),
	}

	return compileStepKindSpec(compileStepKindParams{ID: id, Kind: kind, Step: step, Compiled: compiled})
}

func requiredStepFields(params stepFieldValidationParams) (map[string]string, error) {
	values := make(map[string]string, len(params.Names))

	for _, name := range params.Names {
		value := params.Fields[name]
		if value == nil || strings.TrimSpace(*value) == "" {
			return nil, newError(ErrorCodeSchema, fmt.Sprintf("step %q field %q is required for kind %q", params.StepID, name, params.Kind))
		}

		values[name] = strings.TrimSpace(*value)
	}

	return values, nil
}

func rejectStepFieldNames(params stepFieldValidationParams) error {
	fields := params.Fields

	for _, name := range params.Names {
		if fields[name] != nil {
			return newError(ErrorCodeSchema, fmt.Sprintf("step %q field %q is not allowed for kind %q", params.StepID, name, params.Kind))
		}
	}

	return nil
}

func cloneVars(vars map[string]string) map[string]string {
	if len(vars) == 0 {
		return map[string]string{}
	}

	cloned := make(map[string]string, len(vars))
	for key, value := range vars {
		cloned[key] = value
	}

	return cloned
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	cloned := make([]string, len(values))
	copy(cloned, values)

	return cloned
}

func cloneStep(step StepSpec) StepSpec {
	cloned := StepSpec{
		ID:    step.ID,
		Kind:  step.Kind,
		Needs: cloneStrings(step.Needs),
	}

	if step.Agent != nil {
		agent := *step.Agent
		cloned.Agent = &agent
	}

	if step.Command != nil {
		command := *step.Command
		cloned.Command = &command
	}

	if step.Approval != nil {
		approval := *step.Approval
		cloned.Approval = &approval
	}

	return cloned
}

func cloneSpec(spec *Spec) *Spec {
	if spec == nil {
		return nil
	}

	cloned := &Spec{
		APIVersion: spec.APIVersion,
		Kind:       spec.Kind,
		Metadata:   spec.Metadata,
		Vars:       cloneVars(spec.Vars),
		Steps:      make([]StepSpec, len(spec.Steps)),
	}

	for index, step := range spec.Steps {
		cloned.Steps[index] = cloneStep(step)
	}

	return cloned
}

func sortStepIDsByDeclaration(ids []string, order map[string]int) {
	sort.Slice(ids, func(left, right int) bool {
		return order[ids[left]] < order[ids[right]]
	})
}
