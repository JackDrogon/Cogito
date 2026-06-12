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
- `feishu pull`
- `feishu watch`
- `feishu run`
- `agents run`

Global root option:

- `--version`

## Shared Flags

Most execution-oriented commands parse the same shared flags:

| Flag | Meaning | Default |
|------|---------|---------|
| `--repo` | Repository root used for workflow execution context and repo locking | current directory |
| `--state-dir` | Run state directory | `<repo>/.cogito/runs/run-<timestamp>` |
| `--approval` | Approval mode: `auto`, `approve`, or `deny` | empty input resolves to `auto` |
| `--provider-timeout` | Timeout passed to command execution / providers | `0` |
| `--allow-dirty` | Skip dirty-worktree protection when acquiring repo lock | `false` |
| `-v` | Verbose mode: streams provider (agent) stdout/stderr to the console live while steps run, and replays the run's durable events after it settles | `false` |

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
cogito run ./workflow.yaml --state-dir ./.cogito/runs/run-123
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
cogito status --state-dir ./.cogito/runs/run-123
```

Behavior:

- opens an existing run store
- loads `workflow.json`
- reconstructs runtime state from checkpoint/events
- renders a `RunStatusView`
- scans `provider-logs/*/*.pid.json` read-only and warns about alive provider
  orphans that still match their recorded binary identity; it does not kill or
  clean anything

## `resume`

Resume a paused run.

```bash
cogito resume --state-dir ./.cogito/runs/run-123
```

Behavior:

- opens the existing run session
- reaps any confirmed orphan provider processes recorded under the run dir
- calls `engine.Resume("")`
- continues execution until the run settles again

This command resumes only runs in `paused` state. It does not resolve approvals.

## `approve`

Approve a run currently waiting for approval.

```bash
cogito approve --state-dir ./.cogito/runs/run-123
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
cogito cancel --state-dir ./.cogito/runs/run-123
```

Behavior:

- opens the existing run session
- reaps any confirmed orphan provider processes recorded under the run dir
- asks runtime to cancel the run
- if a step is actively running, runtime first attempts interruption

`cancel` still uses the event log for normal runtime state transitions. The orphan
reap step is a machine-local safety cleanup for provider child processes whose
parent Cogito process already died; it uses pidfiles and identity checks rather
than durable runtime events.

## `replay`

Replay a run from an event log.

```bash
cogito replay ./.cogito/runs/run-123/events.jsonl
```

Behavior:

- infers the run directory from the event log path
- loads `workflow.json` from the same run directory
- reads `events.jsonl`
- rebuilds runtime transitions with `runtime.Replay`
- renders a `ReplayView`

Replay is read-only and does not mutate the run.

## `feishu run`

Delegate a single Feishu Project story to a code agent. The story description
becomes the agent task; the command synthesizes an ephemeral
`agent -> verify -> commit_check` workflow (see `02-workflow-dsl.md`), compiles it,
and runs it through the same engine as `cogito run`.

```bash
cogito feishu run 7004653782 -c cogito.toml --agent codex
```

Flags:

| Flag | Meaning | Default |
|------|---------|---------|
| `-c` / `--config` | Feishu Project config TOML (required) | none |
| `--agent` | Code agent to delegate to: `codex`, `claude`, or `opencode` | `codex` |
| `--no-verify` | Omit the verify step from the ephemeral workflow | verify enabled |
| `--no-commit-check` | Omit the commit_check step | commit_check enabled |

It also accepts the shared execution flags (`--state-dir`, `--approval`,
`--repo`, `--provider-timeout`, `--allow-dirty`). The `<story-id>` positional may
appear before or after the flags.

Behavior:

1. load the config, build the Feishu service, and `Pull` the project stories
2. find the story by id (error if missing)
3. require the story's `repo_path` mapping (error: configure `[repos]` in the TOML);
   the repo path becomes the run working directory
4. `BuildEphemeralSpec` renders the agent prompt with `prompt.BuildMain`, embedding
   the story description as the single task
5. `workflow.CompileWorkflow` compiles the spec
6. `applicationService.RunCompiledWorkflow` executes it; `workflow.json` is written
   to the state dir, so the run is resumable with `cogito resume` just like a
   standard `cogito run`

`feishu pull` and `feishu watch` (story sync) are the other two `feishu`
subcommands; `run` is the execution path added during the AgentLoop port.

## `agents run`

Delegate an ad hoc prompt to a code agent without writing a workflow YAML or
configuring a task source. The prompt becomes the single agent task; like
`feishu run`, the command synthesizes an ephemeral
`agent -> verify -> commit_check` workflow through the shared
`internal/task/agentflow` builder, compiles it, and runs it through the same
engine as `cogito run`.

```bash
cogito agents run -p claude "refactor auth module"
```

Flags:

| Flag | Meaning | Default |
|------|---------|---------|
| `-p` / `--agent` | Code agent to delegate to: `codex`, `claude`, or `opencode` | `codex` |
| `--no-verify` | Omit the verify step from the ephemeral workflow | verify enabled |
| `--no-commit-check` | Omit the commit_check step | commit_check enabled |

It also accepts the shared execution flags (`--state-dir`, `--approval`,
`--repo`, `--provider-timeout`, `--allow-dirty`). The `<prompt>` positional must
be a single (quoted) argument and may appear before or after the flags.

Behavior:

1. resolve the target repository: explicit `--repo`, or the current directory
   when omitted; the path is canonicalized so the agent prompt and the runtime
   wiring agree on one absolute root
2. `agentflow.BuildSpec` renders the agent prompt with `prompt.BuildMain`,
   embedding the ad hoc prompt as the single task
3. `workflow.CompileWorkflow` compiles the spec
4. `applicationService.RunCompiledWorkflow` executes it; `workflow.json` is
   written to the state dir, so the run is resumable with `cogito resume` just
   like a standard `cogito run`

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

When `--state-dir` is omitted, the default is `<repo>/.cogito/runs/run-<timestamp>`,
anchored at `--repo` (or the current directory when `--repo` is omitted). Run
state created under a `.cogito` root is kept invisible to git: Cogito writes a
self-ignoring `.cogito/.gitignore` (`*`) before the dirty-worktree check, so
repeated runs inside a git repository do not trip the dirty gate.

### Repo context semantics

Execution context is resolved in this order:

1. explicit `--repo`
2. checkpoint `repo_path` / `working_dir`
3. current working directory

The directory does not have to be a git repository. Inside git, the lock root
is the repository top level and the dirty-worktree check applies. Outside git
(AgentLoop-ported non-git mode), the absolute directory path becomes the lock
root, the dirty-worktree check is skipped, and `commit_check` steps pass as an
explicit no-op ("not a git repository"). A `--repo` path that does not exist
still fails with the original git error.

### Exit codes

- `0` - success
- `1` - any CLI, validation, execution, replay, or provider error

The top-level `main` package prints the error to stderr as `cogito: <error>`
(the prefix is stable for script consumption) and returns exit code `1`.
Diagnostics and warnings also go to stderr; stdout carries only command output.

## Current limitations

- no dedicated `deny` command
- no `list runs` or `logs` command
- no machine-readable CLI output mode yet
- no direct subcommand for retrying a failed step
