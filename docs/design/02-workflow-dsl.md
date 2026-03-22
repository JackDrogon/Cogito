# Workflow DSL Specification

## Overview

Cogito workflows are defined in YAML and compiled into a static directed acyclic
graph before execution starts. The implementation lives in `internal/workflow`
and uses strict field validation, so this document describes the exact schema that
is currently accepted by the parser.

## Top-Level Document

The parser accepts exactly one YAML document with these top-level fields:

```yaml
apiVersion: cogito/v1alpha1
kind: Workflow
metadata:
  name: example
vars:
  repo_path: /workspace/repo
steps:
  - id: review
    kind: agent
    agent: codex
    prompt: Review the repository
```

### Supported top-level fields

- `apiVersion` - required, must be `cogito/v1alpha1`
- `kind` - required, must be `Workflow`
- `metadata.name` - required
- `vars` - optional string map
- `steps` - required non-empty array

`metadata.description` and other additional fields are not currently accepted.
The YAML decoder runs with `KnownFields(true)`, so unknown fields fail validation.

## Variable Model

`vars` is parsed and preserved as `map[string]string`, but the current execution
path does not perform runtime interpolation inside step fields. In other words,
values are stored in the resolved workflow, but strings such as `${repo_path}` are
not expanded automatically by the workflow package today.

Use `vars` as metadata for now, not as a guaranteed substitution mechanism.

## Step Shape

Each step uses a flat shape. The implementation does not support nested objects
such as `agent: { prompt: ... }` or `command: { command: ... }`.

### Common fields

```yaml
- id: unique-step-id
  kind: agent | command | approval
  needs: [optional, dependencies]
```

- `id` - required, unique within the workflow
- `kind` - required, one of `agent`, `command`, `approval`
- `needs` - optional list of prerequisite step IDs

## Step kinds

### Agent step

```yaml
- id: implement
  kind: agent
  agent: codex
  prompt: Implement the requested change
  needs: [prepare]
```

Required fields:

- `agent` - provider name
- `prompt` - prompt passed to the adapter

Forbidden fields for this kind:

- `command`
- `message`

### Command step

```yaml
- id: test
  kind: command
  command: go test ./...
  needs: [implement]
```

Required fields:

- `command` - raw command string

Forbidden fields for this kind:

- `agent`
- `prompt`
- `message`

Notes:

- The command string is parsed by `internal/executor/command_parser.go` rather
  than executed through a shell pipeline.
- Per-step `working_dir` and per-step timeout fields are not part of the current DSL.

Security note:

- command steps are tokenized into argv form rather than passed wholesale to a shell
- this means shell expansion, pipelines, and glob semantics are not part of the DSL contract

### Approval step

```yaml
- id: approve-release
  kind: approval
  message: Approve release deployment?
  needs: [test]
```

Required fields:

- `message` - summary shown when the runtime requests approval

Forbidden fields for this kind:

- `agent`
- `prompt`
- `command`

Approval steps are modeled as first-class steps. They are queued like other steps,
move into `waiting_approval`, and become `succeeded` only after approval is granted.

## Validation Rules

### Schema validation

- `apiVersion` must be `cogito/v1alpha1`
- `kind` must be `Workflow`
- `metadata.name` must be non-empty
- at least one step must exist
- every step must have `id` and `kind`
- step-kind required fields must be present and non-empty
- step-kind forbidden fields must be absent
- unknown YAML fields are rejected

### Semantic validation

- step IDs must be unique
- dependency IDs in `needs` must be non-empty
- dependency IDs must reference existing steps
- duplicate dependency IDs in one step are rejected

### DAG validation

- the compiled dependency graph must be acyclic
- topological sorting must cover every step

If a cycle exists, the error reports the remaining step IDs involved in the cycle.

## Execution Order

Declaration order matters when several steps are simultaneously eligible.

- root steps are discovered in declaration order
- dependents are sorted in declaration order
- the compiled workflow stores `TopologicalOrder`
- runtime queues all newly ready steps, then executes the first queued step during each engine tick

This means the workflow graph can express parallel readiness, but the current
engine still executes ready work one step at a time per run.

## Example Workflows

### Simple review workflow

```yaml
apiVersion: cogito/v1alpha1
kind: Workflow
metadata:
  name: review-change
steps:
  - id: inspect
    kind: agent
    agent: claude
    prompt: Inspect the repository and summarize the change

  - id: test
    kind: command
    command: go test ./...
    needs: [inspect]

  - id: approve
    kind: approval
    message: Approve merging this change?
    needs: [test]

  - id: finalize
    kind: agent
    agent: codex
    prompt: Prepare the final merge summary
    needs: [approve]
```

### Fan-out / fan-in workflow

```yaml
apiVersion: cogito/v1alpha1
kind: Workflow
metadata:
  name: split-checks
steps:
  - id: prepare
    kind: command
    command: go mod download

  - id: unit
    kind: command
    command: go test ./internal/...
    needs: [prepare]

  - id: integration
    kind: command
    command: go test ./cmd/...
    needs: [prepare]

  - id: report
    kind: agent
    agent: opencode
    prompt: Summarize the test results
    needs: [unit, integration]
```

Both `unit` and `integration` can become ready after `prepare`, but execution will
still be deterministic and sequential in the current runtime.

## Unsupported Features

The following features are not implemented in the current parser/runtime contract:

- dynamic step generation
- conditional execution
- loops
- nested workflows
- step-level environment objects
- step-level timeout fields in YAML
- nested `agent`, `command`, or `approval` config blocks
- guaranteed runtime variable interpolation
