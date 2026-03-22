# Architecture Overview

## System Shape

Cogito is a CLI-first workflow runner that turns a static YAML workflow into a
durable, event-sourced execution. By default, run state is stored under `ref/tmp/`,
and the implementation is split into a small number of explicit packages with
narrow responsibilities.

At a higher level, the architecture can also be read as six cooperating concerns:

- CLI/app coordination
- workflow parsing and compilation
- runtime orchestration
- storage and recovery
- provider adapters
- local command supervision

```text
cmd/cogito/main.go
        |
        v
internal/app
  - command parsing
  - shared flags
  - runtime wiring
  - presenter output
        |
        +------------------------------+
        |                              |
        v                              v
internal/workflow                internal/runtime
  - YAML parsing                   - run/step state machine
  - schema checks                  - scheduling
  - DAG compilation                - approval handling
  - resolved workflow persistence  - replay/checkpoint folding
        |                              |
        |                              +------------------+
        |                                                 |
        v                                                 v
internal/store                                      adapters / executor
  - run layout                                        - provider adapters
  - events.jsonl                                      - command supervision
  - checkpoint.json                                   - artifact log capture
  - artifacts.json
  - run-local locks
```

The boxed "layered" view from earlier drafts was still directionally correct: the
CLI feeds orchestration, which depends on workflow, storage, adapters, and
execution helpers. The updated diagram above keeps that same mental model but uses
current package boundaries and naming.

## Package Responsibilities

### CLI and application layer

`cmd/cogito` is intentionally thin. It creates a signal-aware context and hands
control to `internal/app`.

`internal/app` owns:

- command registration and flag parsing
- workflow execution, resume, replay, status, cancel, and approve flows
- runtime dependency wiring
- provider adapter lookup
- command-step execution via `executor.Supervisor`
- user-facing output formatting

### Workflow layer

`internal/workflow` defines the implemented YAML contract.

- Parses a single YAML document with `KnownFields(true)`
- Validates `apiVersion: cogito/v1alpha1` and `kind: Workflow`
- Rejects unknown fields and invalid step-kind field combinations
- Builds a compiled DAG with `TopologicalOrder`, `StepIndex`, and dependents
- Persists the resolved workflow to `workflow.json` for resume and replay

### Runtime layer

`internal/runtime` is the orchestration core.

- Persists events before mutating in-memory state
- Rebuilds `Snapshot` from events or checkpoint data
- Queues ready steps using the compiled topological order
- Executes one ready step at a time per engine instance
- Handles explicit approvals, adapter-triggered approvals, and policy exceptions
- Produces replay and status views for CLI consumption

### Storage layer

`internal/store` manages the on-disk contract for a single run.

- Canonicalizes run layout under `ref/tmp/runs/<run-id>`
- Appends JSON Lines events with monotonic sequence numbers
- Writes checkpoints and artifact indexes atomically
- Recovers from interrupted checkpoint writes via `.tmp` fallback
- Sanitizes summaries and validates artifact paths stay within the run directory

### Adapter and command execution layer

Cogito has two execution paths:

- `internal/adapters/*` for agent/provider steps (`codex`, `claude`, `opencode`)
- `internal/executor` plus `internal/app/command_runner.go` for shell command steps

This keeps workflow scheduling independent from provider-specific process logic.

## End-to-End Execution Flow

```text
1. CLI parses command + flags
2. Workflow YAML is loaded and compiled into a static DAG
3. A run store is opened under ref/tmp/runs/<run-id>
4. workflow.json is persisted for future resume/replay
5. Repo lock is acquired under ref/tmp/locks/ and mirrored into the run directory
6. runtime.Engine initializes from checkpoint or event replay
7. Engine emits RunCreated / RunStarted / StepQueued ... events
8. Each event is appended to events.jsonl and folded into Snapshot
9. checkpoint.json is rewritten atomically after each persisted transition
10. CLI renders success, failure, status, replay, or approval output
```

## Event-Sourcing Pattern

The implementation still follows the same hybrid pattern described in the earlier
docs: an append-only event log is the source of truth, while checkpoints provide a
faster recovery point.

```text
Event log (append-only)        Checkpoint (snapshot)
------------------------       ---------------------
RunCreated                     {
RunStarted                       "state": "running",
StepQueued                       "steps": { ... },
StepStarted                      "last_sequence": N
StepSucceeded                  }
...
```

Recovery follows the same high-level model:

1. load checkpoint when present
2. compare checkpoint freshness with the latest event sequence
3. replay events when the log is newer
4. continue from the reconstructed snapshot

## Determinism Model

The deterministic boundary is defined by three things:

1. the compiled workflow graph produced by `internal/workflow`
2. the ordered event stream in `events.jsonl`
3. the explicit run/step transition tables in `internal/runtime/state_machine.go`

Ready steps are discovered by scanning `TopologicalOrder` and checking whether all
dependencies are already `succeeded`. When multiple steps become ready together,
the engine queues them in declaration order and executes the first queued step on
each `ExecuteNext` call. This makes replay and resume behavior predictable.

## Directory Layout

```text
ref/tmp/
├── locks/
│   └── <repo>.lock.json          # repository-wide lock metadata
└── runs/
    └── <run-id>/
        ├── workflow.json         # resolved compiled workflow persisted by app layer
        ├── events.jsonl          # append-only event log
        ├── checkpoint.json       # latest durable snapshot
        ├── artifacts.json        # artifact index
        ├── locks/
        │   └── repo.lock.json    # per-run mirror of repo lock metadata
        └── provider-logs/
            └── <step-id>/
                ├── <attempt>-stdout.log
                └── <attempt>-stderr.log
```

## Safety Boundaries

- **Dirty worktree check**: enforced before run start unless `--allow-dirty` is set.
- **Single repo lock**: only one run may mutate a repository at a time.
- **Strict schema parsing**: unknown workflow fields fail validation immediately.
- **Event-first persistence**: runtime previews each transition before appending it.
- **Checkpoint recovery**: incomplete checkpoint writes fall back to the temp file.

## Error Handling Strategy

The original high-level strategy also remains correct and useful:

1. validation errors fail fast during parsing/compilation
2. runtime errors are durably reflected in events/checkpoints before the process exits
3. provider and command execution errors are normalized behind runtime transitions
4. approval denial or timeout fails both the waiting step and the run
5. crash recovery relies on replaying durable state rather than reconstructing from memory

## Known Constraints

- Workflow graphs are static; there is no conditional branching or dynamic step generation.
- The engine is single-run orchestration code; it is not safe for concurrent use of one instance.
- Inner provider adapters currently behave like one-shot command wrappers and do not yet implement provider-level interrupt/resume.
- The CLI exposes approve, but not deny, as a first-class subcommand today.
