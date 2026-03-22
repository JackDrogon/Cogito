# Workflow DSL Specification

## Overview

Cogito workflows are defined in YAML using a versioned schema. Workflows are **static DAGs** - the graph structure is fixed at parse time and cannot be modified during execution.

## Schema Version: `cogito/v1alpha1`

### Top-Level Structure

```yaml
apiVersion: cogito/v1alpha1
kind: Workflow
metadata:
  name: example-workflow
  description: Optional description
vars:
  key: value
steps:
  - id: step-1
    kind: agent
    # ... step-specific fields
```

### Metadata

```yaml
metadata:
  name: string          # Required: workflow identifier
  description: string   # Optional: human-readable description
```

### Variables

```yaml
vars:
  repo_path: /path/to/repo
  timeout: 300
```

Variables are resolved once at run start and substituted into step configurations. Variable syntax: `${var_name}`.

## Step Kinds

### 1. Agent Step

Execute a task using an AI coding agent.

```yaml
- id: implement-feature
  kind: agent
  agent:
    agent: codex              # Provider: codex, claude, opencode
    prompt: "Implement login feature"
    timeout: 600              # Optional: step timeout in seconds
  needs: [previous-step]      # Optional: dependencies
```

**Fields:**
- `agent.agent`: Provider name (must be registered)
- `agent.prompt`: Task description for the agent
- `agent.timeout`: Execution timeout (default: 300s)

### 2. Command Step

Execute a shell command.

```yaml
- id: run-tests
  kind: command
  command:
    command: "go test ./..."  # Command string (parsed, not shell-executed)
    working_dir: ${repo_path} # Optional: working directory
    timeout: 120              # Optional: timeout in seconds
  needs: [implement-feature]
```

**Fields:**
- `command.command`: Command string (tokenized, not passed to shell)
- `command.working_dir`: Working directory (default: repo root)
- `command.timeout`: Execution timeout (default: 60s)

**Security Note**: Commands are parsed into argv arrays, not executed via shell. No shell expansion, globbing, or piping.

### 3. Approval Step

Explicit human approval gate.

```yaml
- id: approve-deployment
  kind: approval
  approval:
    message: "Deploy to production?"
    timeout: 3600             # Optional: approval timeout
  needs: [run-tests]
```

**Fields:**
- `approval.message`: Prompt shown to user
- `approval.timeout`: How long to wait for approval (default: no timeout)

## Dependency Specification

```yaml
steps:
  - id: step-a
    kind: agent
    # ... no dependencies, runs first

  - id: step-b
    kind: agent
    needs: [step-a]           # Runs after step-a completes

  - id: step-c
    kind: agent
    needs: [step-a, step-b]   # Runs after both complete
```

**Rules:**
- Dependencies must reference existing step IDs
- Circular dependencies are rejected at validation time
- Steps with no dependencies run first (in declaration order)
- Steps with satisfied dependencies run in topological order

## Validation Rules

### Schema Validation
- `apiVersion` must be `cogito/v1alpha1`
- `kind` must be `Workflow`
- `metadata.name` is required
- Each step must have unique `id`
- Step `kind` must be one of: `agent`, `command`, `approval`

### Semantic Validation
- All `needs` references must point to existing steps
- No circular dependencies
- Unknown fields are rejected (strict parsing)

### DAG Validation
- Topological sort must succeed (no cycles)
- All steps must be reachable from roots

## Example Workflows

### Simple Linear Workflow

```yaml
apiVersion: cogito/v1alpha1
kind: Workflow
metadata:
  name: simple-workflow
steps:
  - id: prepare
    kind: command
    command:
      command: "echo Preparing"

  - id: review
    kind: agent
    agent:
      agent: codex
      prompt: "Review code quality"
    needs: [prepare]
```

### Parallel Execution

```yaml
apiVersion: cogito/v1alpha1
kind: Workflow
metadata:
  name: parallel-workflow
steps:
  - id: setup
    kind: command
    command:
      command: "echo Setup"

  - id: test-unit
    kind: command
    command:
      command: "go test ./internal/..."
    needs: [setup]

  - id: test-integration
    kind: command
    command:
      command: "go test ./e2e/..."
    needs: [setup]

  - id: report
    kind: agent
    agent:
      agent: codex
      prompt: "Generate test report"
    needs: [test-unit, test-integration]
```

### Approval Gate

```yaml
apiVersion: cogito/v1alpha1
kind: Workflow
metadata:
  name: approval-workflow
steps:
  - id: build
    kind: command
    command:
      command: "go build ./..."

  - id: approve-deploy
    kind: approval
    approval:
      message: "Deploy to production?"
    needs: [build]

  - id: deploy
    kind: command
    command:
      command: "kubectl apply -f deploy.yaml"
    needs: [approve-deploy]
```

## Limitations (V1)

**Not Supported:**
- Dynamic step generation
- Conditional branches
- Loops or iteration
- Nested workflows
- Runtime graph mutation
- Provider-specific fields outside the schema
