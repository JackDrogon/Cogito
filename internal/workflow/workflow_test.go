package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type workflowErrorExpectation struct {
	Test        *testing.T
	Error       error
	WantCode    ErrorCode
	WantMessage string
}

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

			assertWorkflowError(workflowErrorExpectation{Test: t, Error: err, WantCode: tt.wantCode, WantMessage: tt.wantMessage})
		})
	}
}

func TestCompileStepKindSpecUsesRegisteredDescriptors(t *testing.T) {
	tests := []struct {
		name  string
		step  rawStep
		check func(t *testing.T, spec StepSpec)
	}{
		{
			name: "agent",
			step: rawStep{
				ID:     "review",
				Kind:   string(StepKindAgent),
				Agent:  ptr("claude"),
				Prompt: ptr("review this"),
			},
			check: func(t *testing.T, spec StepSpec) {
				t.Helper()
				if spec.Agent == nil || spec.Agent.Agent != "claude" || spec.Agent.Prompt != "review this" {
					t.Fatalf("agent spec = %#v", spec.Agent)
				}
			},
		},
		{
			name: "command",
			step: rawStep{
				ID:      "build",
				Kind:    string(StepKindCommand),
				Command: ptr("go test ./..."),
			},
			check: func(t *testing.T, spec StepSpec) {
				t.Helper()
				if spec.Command == nil || spec.Command.Command != "go test ./..." {
					t.Fatalf("command spec = %#v", spec.Command)
				}
			},
		},
		{
			name: "approval",
			step: rawStep{
				ID:      "gate",
				Kind:    string(StepKindApproval),
				Message: ptr("ship it?"),
			},
			check: func(t *testing.T, spec StepSpec) {
				t.Helper()
				if spec.Approval == nil || spec.Approval.Message != "ship it?" {
					t.Fatalf("approval spec = %#v", spec.Approval)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := compileStep(tt.step, 0)
			if err != nil {
				t.Fatalf("compileStep() error = %v", err)
			}

			tt.check(t, spec)
		})
	}
}

func TestStepKindDescriptorsCoverBuiltins(t *testing.T) {
	kinds := []StepKind{StepKindAgent, StepKindCommand, StepKindApproval, StepKindVerify, StepKindCommitCheck}
	for _, kind := range kinds {
		descriptor, ok := lookupStepKindDescriptor(kind)
		if !ok {
			t.Fatalf("descriptor missing for %q", kind)
		}
		if descriptor.bind == nil {
			t.Fatalf("descriptor bind missing for %q", kind)
		}
	}
}

func TestCompileStepVerifyAndCommitCheck(t *testing.T) {
	t.Run("verify with commands", func(t *testing.T) {
		spec, err := compileStep(rawStep{
			ID:       "check",
			Kind:     string(StepKindVerify),
			Commands: []string{"go test ./...", " "},
		}, 0)
		if err != nil {
			t.Fatalf("compileStep() error = %v", err)
		}
		if spec.Verify == nil || len(spec.Verify.Commands) != 1 || spec.Verify.Commands[0] != "go test ./..." {
			t.Fatalf("verify spec = %#v", spec.Verify)
		}
		if spec.Verify.From != "" {
			t.Fatalf("verify From = %q, want empty", spec.Verify.From)
		}
	})

	t.Run("verify with from", func(t *testing.T) {
		spec, err := compileStep(rawStep{
			ID:   "check",
			Kind: string(StepKindVerify),
			From: ptr("agent"),
		}, 0)
		if err != nil {
			t.Fatalf("compileStep() error = %v", err)
		}
		if spec.Verify == nil || spec.Verify.From != "agent" || len(spec.Verify.Commands) != 0 {
			t.Fatalf("verify spec = %#v", spec.Verify)
		}
	})

	t.Run("verify with both commands and from", func(t *testing.T) {
		_, err := compileStep(rawStep{
			ID:       "check",
			Kind:     string(StepKindVerify),
			Commands: []string{"go test ./..."},
			From:     ptr("agent"),
		}, 0)
		assertWorkflowError(workflowErrorExpectation{Test: t, Error: err, WantCode: ErrorCodeSchema, WantMessage: "not both"})
	})

	t.Run("verify with neither", func(t *testing.T) {
		_, err := compileStep(rawStep{ID: "check", Kind: string(StepKindVerify)}, 0)
		assertWorkflowError(workflowErrorExpectation{Test: t, Error: err, WantCode: ErrorCodeSchema, WantMessage: "either commands or from"})
	})

	t.Run("commit_check with from", func(t *testing.T) {
		requireSome := true
		allowDirty := true
		spec, err := compileStep(rawStep{
			ID:          "commits",
			Kind:        string(StepKindCommitCheck),
			From:        ptr("agent"),
			RequireSome: &requireSome,
			AllowDirty:  &allowDirty,
		}, 0)
		if err != nil {
			t.Fatalf("compileStep() error = %v", err)
		}
		if spec.CommitCheck == nil || spec.CommitCheck.From != "agent" {
			t.Fatalf("commit_check spec = %#v", spec.CommitCheck)
		}
		if !spec.CommitCheck.RequireSome || !spec.CommitCheck.AllowDirty {
			t.Fatalf("commit_check flags = %#v", spec.CommitCheck)
		}
	})

	t.Run("commit_check defaults", func(t *testing.T) {
		spec, err := compileStep(rawStep{ID: "commits", Kind: string(StepKindCommitCheck), From: ptr("agent")}, 0)
		if err != nil {
			t.Fatalf("compileStep() error = %v", err)
		}
		if spec.CommitCheck.RequireSome || spec.CommitCheck.AllowDirty {
			t.Fatalf("commit_check defaults = %#v, want both false", spec.CommitCheck)
		}
	})

	t.Run("commit_check missing from", func(t *testing.T) {
		_, err := compileStep(rawStep{ID: "commits", Kind: string(StepKindCommitCheck)}, 0)
		assertWorkflowError(workflowErrorExpectation{Test: t, Error: err, WantCode: ErrorCodeSchema, WantMessage: "is required"})
	})

	t.Run("commit_check with commands", func(t *testing.T) {
		_, err := compileStep(rawStep{
			ID:       "commits",
			Kind:     string(StepKindCommitCheck),
			From:     ptr("agent"),
			Commands: []string{"echo nope"},
		}, 0)
		assertWorkflowError(workflowErrorExpectation{Test: t, Error: err, WantCode: ErrorCodeSchema, WantMessage: "not allowed"})
	})
}

func TestLoadWorkflowAgentVerifyCommitCheckChain(t *testing.T) {
	input := "apiVersion: cogito/v1alpha1\n" +
		"kind: Workflow\n" +
		"metadata:\n  name: agent-verify-commit\n" +
		"steps:\n" +
		"  - id: agent\n    kind: agent\n    agent: codex\n    prompt: do the work\n" +
		"  - id: verify\n    kind: verify\n    needs: [agent]\n    from: agent\n" +
		"  - id: commit_check\n    kind: commit_check\n    needs: [verify]\n    from: agent\n    require_some: true\n"

	compiled, err := LoadWorkflow([]byte(input))
	if err != nil {
		t.Fatalf("LoadWorkflow() error = %v", err)
	}

	if want := []string{"agent", "verify", "commit_check"}; !reflect.DeepEqual(compiled.TopologicalOrder, want) {
		t.Fatalf("TopologicalOrder = %v, want %v", compiled.TopologicalOrder, want)
	}

	verifyStep := compiled.Steps[compiled.StepIndex["verify"]]
	if verifyStep.Verify == nil || verifyStep.Verify.From != "agent" {
		t.Fatalf("verify step = %#v", verifyStep.Verify)
	}

	commitStep := compiled.Steps[compiled.StepIndex["commit_check"]]
	if commitStep.CommitCheck == nil || commitStep.CommitCheck.From != "agent" || !commitStep.CommitCheck.RequireSome {
		t.Fatalf("commit_check step = %#v", commitStep.CommitCheck)
	}
}

func TestParseWorkflowVerifyConflictSurfacesSchemaError(t *testing.T) {
	input := "apiVersion: cogito/v1alpha1\n" +
		"kind: Workflow\n" +
		"metadata:\n  name: bad-verify\n" +
		"steps:\n" +
		"  - id: verify\n    kind: verify\n    from: agent\n    commands: [\"go test ./...\"]\n"

	_, err := ParseWorkflow([]byte(input))
	assertWorkflowError(workflowErrorExpectation{Test: t, Error: err, WantCode: ErrorCodeSchema, WantMessage: "not both"})
}

// TestValidateStepFromReferences is v3.2 N5: a verify/commit_check `from:` must
// name an existing agent step that the referencing step depends on. Bad
// references must fail compilation rather than silently read garbage or run
// before the agent they claim to consume.
func TestValidateStepFromReferences(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantMessage string
	}{
		{
			name: "verify from missing step",
			input: "apiVersion: cogito/v1alpha1\nkind: Workflow\nmetadata:\n  name: bad\nsteps:\n" +
				"  - id: agent\n    kind: agent\n    agent: codex\n    prompt: do work\n" +
				"  - id: verify\n    kind: verify\n    needs: [agent]\n    from: missing\n",
			wantMessage: "references unknown step",
		},
		{
			name: "verify from non-agent step",
			input: "apiVersion: cogito/v1alpha1\nkind: Workflow\nmetadata:\n  name: bad\nsteps:\n" +
				"  - id: build\n    kind: command\n    command: make\n" +
				"  - id: verify\n    kind: verify\n    needs: [build]\n    from: build\n",
			wantMessage: "must reference an agent step",
		},
		{
			name: "verify from without needs",
			input: "apiVersion: cogito/v1alpha1\nkind: Workflow\nmetadata:\n  name: bad\nsteps:\n" +
				"  - id: agent\n    kind: agent\n    agent: codex\n    prompt: do work\n" +
				"  - id: verify\n    kind: verify\n    from: agent\n",
			wantMessage: "does not declare it in needs",
		},
		{
			name: "commit_check from non-agent step",
			input: "apiVersion: cogito/v1alpha1\nkind: Workflow\nmetadata:\n  name: bad\nsteps:\n" +
				"  - id: build\n    kind: command\n    command: make\n" +
				"  - id: commit_check\n    kind: commit_check\n    needs: [build]\n    from: build\n",
			wantMessage: "must reference an agent step",
		},
		{
			name: "commit_check from without needs",
			input: "apiVersion: cogito/v1alpha1\nkind: Workflow\nmetadata:\n  name: bad\nsteps:\n" +
				"  - id: agent\n    kind: agent\n    agent: codex\n    prompt: do work\n" +
				"  - id: commit_check\n    kind: commit_check\n    from: agent\n",
			wantMessage: "does not declare it in needs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadWorkflow([]byte(tt.input))
			assertWorkflowError(workflowErrorExpectation{Test: t, Error: err, WantCode: ErrorCodeSemantic, WantMessage: tt.wantMessage})
		})
	}
}

// TestValidateStepFromTransitiveNeeds is v3.2 N5: a `from:` target reachable
// only through a transitive needs chain is accepted (no false rejection).
func TestValidateStepFromTransitiveNeeds(t *testing.T) {
	input := "apiVersion: cogito/v1alpha1\nkind: Workflow\nmetadata:\n  name: chain\nsteps:\n" +
		"  - id: agent\n    kind: agent\n    agent: codex\n    prompt: do work\n" +
		"  - id: gate\n    kind: command\n    needs: [agent]\n    command: echo gate\n" +
		"  - id: verify\n    kind: verify\n    needs: [gate]\n    from: agent\n"

	if _, err := LoadWorkflow([]byte(input)); err != nil {
		t.Fatalf("LoadWorkflow() error = %v, want nil (transitive needs reach agent)", err)
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

			assertWorkflowError(workflowErrorExpectation{Test: t, Error: err, WantCode: tt.wantCode, WantMessage: tt.wantMessage})
		})
	}
}

func assertWorkflowError(expectation workflowErrorExpectation) {
	expectation.Test.Helper()

	if expectation.Error == nil {
		expectation.Test.Fatal("expected error, got nil")
	}

	var workflowErr *Error
	if !errors.As(expectation.Error, &workflowErr) {
		expectation.Test.Fatalf("error type = %T, want *workflow.Error", expectation.Error)
	}

	if workflowErr.Code != expectation.WantCode {
		expectation.Test.Fatalf("error code = %q, want %q", workflowErr.Code, expectation.WantCode)
	}

	if !strings.Contains(workflowErr.Error(), expectation.WantMessage) {
		expectation.Test.Fatalf("error = %q, want substring %q", workflowErr.Error(), expectation.WantMessage)
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

func ptr(value string) *string {
	return &value
}
