package workflow

const (
	v1Alpha1APIVersion = "cogito/v1alpha1"
	workflowKind       = "Workflow"
)

// StepKind identifies the supported step type in the workflow DSL.
type StepKind string

const (
	StepKindAgent       StepKind = "agent"
	StepKindCommand     StepKind = "command"
	StepKindApproval    StepKind = "approval"
	StepKindVerify      StepKind = "verify"
	StepKindCommitCheck StepKind = "commit_check"
)

// Metadata holds workflow metadata.
type Metadata struct {
	Name string
}

// AgentStepSpec describes an agent step.
type AgentStepSpec struct {
	Agent  string
	Prompt string
}

// CommandStepSpec describes a command step.
type CommandStepSpec struct {
	Command string
}

// ApprovalStepSpec describes an approval step.
type ApprovalStepSpec struct {
	Message string
}

// VerifyStepSpec describes a verify step. Exactly one of Commands or From is
// populated (enforced during binding): Commands lists explicit shell commands to
// replay, while From pulls the verification command list from an upstream agent
// step's AgentResult.Verification.
type VerifyStepSpec struct {
	Commands []string
	From     string
}

// CommitCheckStepSpec describes a commit_check step. From names the upstream
// agent step whose AgentResult.Commits are validated against real git history.
// RequireSome demands at least one self-reported commit; AllowDirty skips the
// post-check that the worktree is clean.
type CommitCheckStepSpec struct {
	From        string
	RequireSome bool
	AllowDirty  bool
}

// StepSpec is the validated workflow step specification after parse stage.
// It represents a schema-valid step with kind-specific fields populated correctly,
// but has not yet been compiled into the dependency graph.
type StepSpec struct {
	ID          string
	Kind        StepKind
	Needs       []string
	Agent       *AgentStepSpec
	Command     *CommandStepSpec
	Approval    *ApprovalStepSpec
	Verify      *VerifyStepSpec
	CommitCheck *CommitCheckStepSpec
}

// Spec is the validated, schema-checked workflow definition.
type Spec struct {
	APIVersion string
	Kind       string
	Metadata   Metadata
	Vars       map[string]string
	Steps      []StepSpec
}

// CompiledStep freezes graph relationships for a single step.
type CompiledStep struct {
	StepSpec
	Dependents []string
}

// CompiledWorkflow is the immutable, runtime-ready DAG produced by CompileWorkflow.
// It contains the validated dependency graph with precomputed topological order and
// step index for O(1) lookup. Runtime consumes this instead of raw YAML to ensure
// deterministic scheduling and reproducible execution order.
type CompiledWorkflow struct {
	Spec             *Spec
	Steps            []CompiledStep
	StepIndex        map[string]int
	TopologicalOrder []string
}

type rawWorkflow struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   rawMetadata       `yaml:"metadata"`
	Vars       map[string]string `yaml:"vars"`
	Steps      []rawStep         `yaml:"steps"`
}

type rawMetadata struct {
	Name string `yaml:"name"`
}

type rawStep struct {
	ID          string   `yaml:"id"`
	Kind        string   `yaml:"kind"`
	Needs       []string `yaml:"needs"`
	Agent       *string  `yaml:"agent"`
	Prompt      *string  `yaml:"prompt"`
	Command     *string  `yaml:"command"`
	Message     *string  `yaml:"message"`
	Commands    []string `yaml:"commands"`
	From        *string  `yaml:"from"`
	RequireSome *bool    `yaml:"require_some"`
	AllowDirty  *bool    `yaml:"allow_dirty"`
}
