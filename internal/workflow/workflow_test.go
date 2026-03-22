package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseWorkflow(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		fixture     string
		wantCode    ErrorCode
		wantMessage string
		check       func(t *testing.T, spec *Spec)
	}{
		{
			name:    "valid simple workflow",
			fixture: "simple.yaml",
			check: func(t *testing.T, spec *Spec) {
				t.Helper()

				if spec.Metadata.Name != "simple" {
					t.Fatalf("Metadata.Name = %q", spec.Metadata.Name)
				}

				if len(spec.Steps) != 3 {
					t.Fatalf("len(Steps) = %d", len(spec.Steps))
				}

				if spec.Steps[1].Kind != StepKindCommand {
					t.Fatalf("Steps[1].Kind = %q", spec.Steps[1].Kind)
				}

				if spec.Steps[2].Kind != StepKindAgent {
					t.Fatalf("Steps[2].Kind = %q", spec.Steps[2].Kind)
				}
			},
		},
		{
			name:        "unsupported version",
			input:       "apiVersion: cogito/v9\nkind: Workflow\nmetadata:\n  name: bad-version\nsteps:\n  - id: run\n    kind: command\n    command: echo hi\n",
			wantCode:    ErrorCodeVersion,
			wantMessage: "unsupported apiVersion",
		},
		{
			name:        "unknown field rejected",
			input:       "apiVersion: cogito/v1alpha1\nkind: Workflow\nmetadata:\n  name: bad-field\nsteps:\n  - id: run\n    kind: command\n    command: echo hi\n    extra: nope\n",
			wantCode:    ErrorCodeSchema,
			wantMessage: "unknown field",
		},
		{
			name:        "unsupported step kind",
			fixture:     "unsupported-kind.yaml",
			wantCode:    ErrorCodeSchema,
			wantMessage: "unsupported step kind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.input
			if tt.fixture != "" {
				input = readFixture(t, tt.fixture)
			}

			spec, err := ParseWorkflow([]byte(input))
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("ParseWorkflow() error = %v", err)
				}

				if tt.check != nil {
					tt.check(t, spec)
				}

				return
			}

			assertWorkflowError(t, err, tt.wantCode, tt.wantMessage)
		})
	}
}

func TestValidateWorkflowDAG(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		fixture     string
		wantCode    ErrorCode
		wantMessage string
		wantOrder   []string
	}{
		{
			name:      "simple workflow order",
			fixture:   "simple.yaml",
			wantOrder: []string{"prepare", "review", "notify"},
		},
		{
			name:      "approval workflow order",
			fixture:   "approval.yaml",
			wantOrder: []string{"draft", "legal", "publish"},
		},
		{
			name:        "duplicate dependency ids",
			input:       "apiVersion: cogito/v1alpha1\nkind: Workflow\nmetadata:\n  name: duplicate-deps\nsteps:\n  - id: first\n    kind: command\n    command: echo first\n  - id: second\n    kind: command\n    needs: [first, first]\n    command: echo second\n",
			wantCode:    ErrorCodeSemantic,
			wantMessage: "duplicate dependency id",
		},
		{
			name:        "missing dependency ids",
			input:       "apiVersion: cogito/v1alpha1\nkind: Workflow\nmetadata:\n  name: missing-deps\nsteps:\n  - id: first\n    kind: command\n    needs: [missing]\n    command: echo first\n",
			wantCode:    ErrorCodeSemantic,
			wantMessage: "depends on unknown step",
		},
		{
			name:        "cycle detected",
			fixture:     "cycle.yaml",
			wantCode:    ErrorCodeSemantic,
			wantMessage: "cycle detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.input
			if tt.fixture != "" {
				input = readFixture(t, tt.fixture)
			}

			compiled, err := LoadWorkflow([]byte(input))
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("LoadWorkflow() error = %v", err)
				}

				if !reflect.DeepEqual(compiled.TopologicalOrder, tt.wantOrder) {
					t.Fatalf("TopologicalOrder = %v, want %v", compiled.TopologicalOrder, tt.wantOrder)
				}

				return
			}

			assertWorkflowError(t, err, tt.wantCode, tt.wantMessage)
		})
	}
}

func assertWorkflowError(t *testing.T, err error, wantCode ErrorCode, wantMessage string) {
	t.Helper()

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var workflowErr *Error
	if !errors.As(err, &workflowErr) {
		t.Fatalf("error type = %T, want *workflow.Error", err)
	}

	if workflowErr.Code != wantCode {
		t.Fatalf("error code = %q, want %q", workflowErr.Code, wantCode)
	}

	if !strings.Contains(workflowErr.Error(), wantMessage) {
		t.Fatalf("error = %q, want substring %q", workflowErr.Error(), wantMessage)
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}

	return string(data)
}
