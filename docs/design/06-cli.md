# Command Line Interface

This document describes the Cogito command line interface, its design philosophy, and usage patterns.

## Design Philosophy

The Cogito CLI is designed to be a developer-friendly tool for orchestrating complex workflows. It follows several core principles:

1. **Explicit over Implicit**: Flags and arguments should clearly indicate their purpose.
2. **State Transparency**: Every workflow run is associated with a state directory that can be inspected.
3. **Resilience**: Commands like `resume` and `replay` allow recovering from failures or re-running logic with existing logs.
4. **Safety**: Destructive operations or operations on dirty repositories require explicit flags.

## Global Options

The following option is available globally:

- `--version`: Print the version information and exit.

## Command Reference

### workflow validate

Validates the syntax and structure of a workflow definition file.

**Syntax:**
```bash
cogito workflow validate <file> [flags]
```

**Flags:**
- All shared flags (see [Shared Flags](#shared-flags)) are accepted but mostly ignored by the validation logic.

**Example:**
```bash
cogito workflow validate ./workflows/deploy.yaml
```

---

### run

Executes a workflow definition. This command initiates a new run or continues an existing one if a state directory is provided.

**Syntax:**
```bash
cogito run <file> [flags]
```

**Flags:**
- `--state-dir <path>`: Directory to store or read run state.
- `--approval <mode>`: Set the approval mode (e.g., `manual`, `auto`).
- `--allow-dirty`: Allow execution even if the repository has uncommitted changes.
- `--repo <path>`: Specify the repository root.
- `--provider-timeout <duration>`: Set timeout for providers (e.g., `30s`, `5m`).

**Example:**
```bash
cogito run ./workflows/ci.yaml --allow-dirty --approval manual
```

---

### status

Displays the current status of a workflow run, including the state of each step.

**Syntax:**
```bash
cogito status [flags]
```

**Flags:**
- `--state-dir <path>`: The state directory of the run to inspect. Required if not using the default.

**Example:**
```bash
cogito status --state-dir ref/tmp/runs/run-123456789
```

**Output format:**
The output includes the Run ID, state directory, overall run state, and a summary of each step's execution status.

---

### resume

Resumes a paused or interrupted workflow run using the state preserved in the state directory.

**Syntax:**
```bash
cogito resume [flags]
```

**Flags:**
- `--state-dir <path>`: The state directory of the run to resume.
- All other shared flags.

**Example:**
```bash
cogito resume --state-dir ref/tmp/runs/run-123456789 --approval auto
```

---

### replay

Replays a workflow execution from an existing event log file (`.jsonl`). This is useful for debugging or auditing past runs.

**Syntax:**
```bash
cogito replay <events.jsonl> [flags]
```

**Example:**
```bash
cogito replay ref/tmp/runs/run-123456789/events.jsonl
```

---

### cancel

Cancels a currently running workflow. It signals the runner to stop execution gracefully at the next available checkpoint.

**Syntax:**
```bash
cogito cancel [flags]
```

**Flags:**
- `--state-dir <path>`: The state directory of the run to cancel.

**Example:**
```bash
cogito cancel --state-dir ref/tmp/runs/run-123456789
```

## Shared Flags

Most Cogito commands accept a set of shared flags that control the environment and behavior of the execution.

| Flag | Description | Default |
|------|-------------|---------|
| `--state-dir` | Path to the directory where run state and event logs are stored. | `ref/tmp/runs/run-<timestamp>` |
| `--repo` | The root directory of the repository for workflow context. | Current directory |
| `--approval` | Configures how manual approvals are handled during the run. | `manual` |
| `--provider-timeout` | Timeout duration for external provider calls (e.g., `1m`). | `0` (no timeout) |
| `--allow-dirty` | If true, permits running workflows in a repo with uncommitted changes. | `false` |

## Usage Patterns

### Standard Development Workflow

1. Validate your changes:
   ```bash
   cogito workflow validate deploy.yaml
   ```
2. Run locally to test (allowing dirty state):
   ```bash
   cogito run deploy.yaml --allow-dirty
   ```
3. Check progress:
   ```bash
   cogito status --state-dir ref/tmp/runs/latest
   ```

### Handling Failures

If a run fails due to a transient error, you can resume it:
```bash
cogito resume --state-dir ref/tmp/runs/run-failed
```

If you need to analyze what happened during a complex run, use the replay command:
```bash
cogito replay ref/tmp/runs/run-failed/events.jsonl
```

## Exit Codes

- `0`: Success.
- `1`: General error (unknown subcommand, invalid arguments).
- `Other`: Specific workflow execution errors or provider failures.

---
*Note: The CLI is currently in active development. Flags and commands are subject to refinement as the runtime evolves.*
