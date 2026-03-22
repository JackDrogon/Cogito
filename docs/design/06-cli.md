# Command Line Interface

This document describes the CLI surface currently implemented in `internal/app`.
The parser is a lightweight custom command registry built on Go's `flag` package,
not Cobra.

## Design Philosophy

The older CLI document had the right high-level framing, and it is still useful:

1. **Explicit over implicit** - commands and flags should make state changes obvious
2. **State transparency** - every run has a state directory that can be inspected directly
3. **Resilience** - `resume` and `replay` are first-class recovery/debugging paths
4. **Safety** - dirty-worktree checks and explicit flags protect repository execution

## Command Set

Top-level commands are:

- `workflow validate`
- `run`
- `status`
- `resume`
- `replay`
- `cancel`
- `approve`

Global root option:

- `--version`

## Shared Flags

Most execution-oriented commands parse the same shared flags:

| Flag | Meaning | Default |
|------|---------|---------|
| `--repo` | Repository root used for workflow execution context and repo locking | current directory |
| `--state-dir` | Run state directory | `ref/tmp/runs/run-<timestamp>` |
| `--approval` | Approval mode: `auto`, `approve`, or `deny` | empty input resolves to `auto` |
| `--provider-timeout` | Timeout passed to command execution / providers | `0` |
| `--allow-dirty` | Skip dirty-worktree protection when acquiring repo lock | `false` |

`--state-dir` is automatically generated when omitted for commands that create a new run.

## `workflow validate`

Validate a workflow file without executing it.

```bash
cogito workflow validate ./workflow.yaml
```

Behavior:

- loads the file through `workflow.LoadFile`
- performs schema, semantic, and DAG validation
- prints a success message on valid input

The parser accepts shared flags for consistency, but validation itself only needs
the workflow path.

## `run`

Execute a workflow and create a run directory.

```bash
cogito run ./workflow.yaml --state-dir ./ref/tmp/runs/run-123
```

Behavior:

1. parse approval mode
2. load and compile the workflow
3. derive run ID and base directory from `--state-dir`
4. acquire the repository lock
5. open the run store
6. persist `workflow.json`
7. build runtime wiring
8. execute until the run settles

If the final runtime state is `failed`, the command returns the latest non-empty
event message as an error.

## `status`

Show the current state of an existing run.

```bash
cogito status --state-dir ./ref/tmp/runs/run-123
```

Behavior:

- opens an existing run store
- loads `workflow.json`
- reconstructs runtime state from checkpoint/events
- renders a `RunStatusView`

## `resume`

Resume a paused run.

```bash
cogito resume --state-dir ./ref/tmp/runs/run-123
```

Behavior:

- opens the existing run session
- calls `engine.Resume("")`
- continues execution until the run settles again

This command resumes only runs in `paused` state. It does not resolve approvals.

## `approve`

Approve a run currently waiting for approval.

```bash
cogito approve --state-dir ./ref/tmp/runs/run-123
```

Behavior:

- opens the existing run session
- calls `engine.GrantApproval(ctx, "approved via CLI")`
- continues execution until the run settles again

There is currently no dedicated `deny` CLI command even though the runtime has a
deny path internally.

## `cancel`

Cancel a run.

```bash
cogito cancel --state-dir ./ref/tmp/runs/run-123
```

Behavior:

- opens the existing run session
- asks runtime to cancel the run
- if a step is actively running, runtime first attempts interruption

## `replay`

Replay a run from an event log.

```bash
cogito replay ./ref/tmp/runs/run-123/events.jsonl
```

Behavior:

- infers the run directory from the event log path
- loads `workflow.json` from the same run directory
- reads `events.jsonl`
- rebuilds runtime transitions with `runtime.Replay`
- renders a `ReplayView`

Replay is read-only and does not mutate the run.

## Usage Patterns

### Standard development loop

1. validate the workflow definition
2. run it locally, optionally with `--allow-dirty`
3. inspect state with `status`
4. use `resume`, `approve`, `cancel`, or `replay` depending on how the run settled

### Failure analysis

Two earlier usage notes are still accurate and worth keeping:

- `resume` is the operational recovery path for paused runs
- `replay` is the audit/debug path for understanding durable transitions after a run

## Usage Notes

### State directory semantics

For new runs, `--state-dir` is both:

- the place where run data will be written
- the source of the new run ID (`filepath.Base(stateDir)`)

This means the directory name is part of the durable run identity.

### Repo context semantics

Execution context is resolved in this order:

1. explicit `--repo`
2. checkpoint `repo_path` / `working_dir`
3. current working directory

### Exit codes

- `0` - success
- `1` - any CLI, validation, execution, replay, or provider error

The top-level `main` package prints the error to stderr and returns exit code `1`.

## Current limitations

- no dedicated `deny` command
- no `list runs` or `logs` command
- no machine-readable CLI output mode yet
- no direct subcommand for retrying a failed step
