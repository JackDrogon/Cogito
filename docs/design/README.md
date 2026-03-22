# Cogito Design Documentation

This directory documents the design that is currently implemented in the repository.
It is intentionally code-aligned: when the implementation and earlier design notes
diverged, the documents here were updated to describe the shipped behavior first.

## Documentation Map

- [Architecture Overview](./01-architecture.md) - Component boundaries and execution flow
- [Workflow DSL](./02-workflow-dsl.md) - YAML schema implemented by `internal/workflow`
- [Storage Model](./03-storage.md) - Run layout, events, checkpoints, artifacts, and locks
- [Runtime State Machine](./04-runtime.md) - Run/step state transitions and replay model
- [Adapter SPI](./05-adapters.md) - Provider adapter lifecycle and current capabilities
- [CLI Commands](./06-cli.md) - Command surface implemented in `internal/app`
- [Approval Gates](./07-approval.md) - Explicit, adapter, and policy-driven approvals
- [Error Model](./08-errors.md) - Error codes, wrapping, and CLI surfacing rules

## Quick Start

```bash
# Validate a workflow
cogito workflow validate path/to/workflow.yaml

# Execute a workflow
cogito run path/to/workflow.yaml --state-dir ./ref/tmp/runs/run-123

# Inspect current status
cogito status --state-dir ./ref/tmp/runs/run-123

# Resume a paused run
cogito resume --state-dir ./ref/tmp/runs/run-123

# Approve a waiting run
cogito approve --state-dir ./ref/tmp/runs/run-123

# Replay from an event log
cogito replay ./ref/tmp/runs/run-123/events.jsonl

# Cancel a running run
cogito cancel --state-dir ./ref/tmp/runs/run-123
```

## Design Principles

1. **Local-first** - Run state is file-backed with `ref/tmp/` as the default layout and no daemon or database.
2. **Deterministic** - Workflow ordering is compiled once and replayed from durable events.
3. **Code-aligned contracts** - Documentation describes the current implementation, not aspirational APIs.
4. **Auditable** - Every meaningful runtime transition is appended to `events.jsonl` before checkpoint persistence.
5. **Human-in-the-loop** - Approval can be explicit in YAML, requested by an adapter, or injected by policy.

## Reading Order

- Start with `01-architecture.md` for the system boundary.
- Read `02-workflow-dsl.md` and `06-cli.md` for user-facing contracts.
- Use `03-storage.md` and `04-runtime.md` when debugging resume, replay, or recovery.
- Use `05-adapters.md` and `07-approval.md` when working on provider integration or pause/resume behavior.
