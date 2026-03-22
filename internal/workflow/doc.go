/*
Package workflow owns the static workflow DSL that feeds Cogito's runtime layer.

The package turns author-written YAML into a validated, deterministic, and
effectively immutable execution graph. Runtime code never schedules directly
from raw YAML; it only consumes a CompiledWorkflow produced here.

# Architecture

The package is organized as a narrow pipeline with clear stage boundaries:

	YAML bytes/file
	    |
	    v
	ParseWorkflow / LoadFile      (parse.go)
	    |
	    v
	Spec                          (schema-valid definition)
	    |
	    v
	CompileWorkflow               (validate.go)
	    |
	    v
	CompiledWorkflow              (runtime-ready DAG)
	    |
	    v
	SaveResolvedFile / LoadResolvedFile (persist.go)

Stage 1: parse and schema validation

ParseWorkflow decodes YAML with KnownFields enabled so configuration mistakes
fail early instead of silently producing partial state. This stage is also
responsible for validating document-level invariants such as apiVersion, kind,
required metadata, and step-kind-specific field rules.

At the end of this stage the package returns a Spec: the workflow is structurally
valid, but it has not yet been proven to be executable as a DAG.

Stage 2: semantic validation and graph compilation

CompileWorkflow validates cross-step semantics and then freezes the dependency
graph into a CompiledWorkflow. This includes detecting duplicate step IDs,
unknown dependencies, empty dependency references, and cyclic graphs.

The compiler also computes two runtime-facing structures:

  - StepIndex for stable O(1) step lookup by ID.
  - TopologicalOrder for deterministic scheduling order.

Stage 3: persistence of resolved definitions

Resolved workflow persistence stores the validated Spec, not the fully expanded
graph. Loading a resolved file recompiles the graph so the persisted format stays
compact while graph invariants are re-checked on the way back in.

# Determinism and ordering

This package preserves declaration order wherever the DAG allows it. Helpers such
as sortStepIDsByDeclaration keep dependent lists and the ready queue stable, so
equivalent inputs always produce the same TopologicalOrder. That deterministic
ordering matters because later layers rely on event logs and replays remaining
auditable and reproducible.

# Immutability contract

CompiledWorkflow is treated as immutable after construction. The compiler clones
maps, slices, and nested step payloads before exposing the final graph. That
defensive copy step prevents callers from mutating shared workflow state after the
runtime has accepted it, which keeps replay and checkpoint behavior predictable.

# Supported step kinds

The DSL currently supports three mutually exclusive step kinds:

  - agent: requires agent and prompt
  - command: requires command
  - approval: requires message

Kind-specific fields are validated here so downstream code can rely on a single,
well-formed representation instead of repeatedly checking for impossible states.

# Error model

Errors are tagged by phase so callers can distinguish parse failures from schema,
semantic, or version mismatches. That separation lets CLI and runtime code report
configuration issues with enough context while preserving the original cause.

# Concurrency model

Parsing and compilation are ordinary synchronous operations. The resulting
CompiledWorkflow is safe to share for concurrent read access because it is not
mutated after construction.
*/
package workflow
