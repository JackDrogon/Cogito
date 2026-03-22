# Cogito

A CLI-first multi-agent workflow orchestrator for deterministic, auditable AI coding workflows.

## Overview

Cogito executes static YAML workflows across local command steps and provider-backed
agent steps. The current implementation is local-first and file-backed: by
default, runs persist their workflow, event log, checkpoint, artifacts, and lock
metadata under `ref/tmp/`, while `--state-dir` can relocate a run when needed.

-**Key Features:**
-- 🔄 **Deterministic Execution** - Reproducible runs from event logs
-- 📦 **Provider-Agnostic** - Unified interface for multiple AI providers
-- 💾 **Event Sourcing** - Complete audit trail with replay capability
-- ⏸️ **Resumable** - Pause, resume, and recover from failures
-- 🚦 **Approval Gates** - Human-in-the-loop control at critical steps
-- 🔒 **Local-First** - File-backed storage, no daemon required

Key properties:
- deterministic scheduling from a compiled DAG and append-only event history
- provider-agnostic agent execution through a common adapter SPI
- resumable runs through checkpoint recovery and replay
- approval gates for explicit workflow pauses, adapter requests, and policy exceptions
- repository locking and dirty-worktree protection for safer automation

## Installation

```bash
go install github.com/JackDrogon/Cogito/cmd/cogito@latest
```

Or build from source:

```bash
git clone https://github.com/JackDrogon/Cogito.git
cd Cogito
just build
```

## Quick Start

### Validate a workflow

```bash
cogito workflow validate workflow.yaml
```

### Execute a workflow

```bash
cogito run workflow.yaml --state-dir ./ref/tmp/runs/run-123
```

### Inspect, resume, approve, cancel, and replay

```bash
cogito status --state-dir ./ref/tmp/runs/run-123
cogito resume --state-dir ./ref/tmp/runs/run-123
cogito approve --state-dir ./ref/tmp/runs/run-123
cogito cancel --state-dir ./ref/tmp/runs/run-123
cogito replay ./ref/tmp/runs/run-123/events.jsonl
```

These examples use an explicit `--state-dir` so the run ID is stable and easy to
inspect. When omitted, Cogito generates `ref/tmp/runs/run-<timestamp>`.

## Workflow Example

The current DSL uses flat step fields rather than nested `agent` / `command`
configuration objects.

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

## Architecture

Cogito is built around three core concerns:

1. **Workflow Engine** - validates YAML, compiles the DAG, and preserves execution order
2. **Runtime State Machine** - manages event-sourced run and step transitions
3. **Adapter / Execution Boundary** - runs agent providers and local commands behind stable interfaces

Those concerns are implemented as five cooperating layers:

1. **CLI / App Layer** - parses commands, resolves flags, wires runtime dependencies
2. **Workflow Layer** - validates YAML and compiles a static DAG
3. **Runtime Layer** - drives the event-sourced run and step state machines
4. **Store Layer** - persists `events.jsonl`, `checkpoint.json`, `artifacts.json`, and `workflow.json`
5. **Execution Layer** - runs provider adapters and local command steps

The current engine is deterministic and sequential per run: multiple steps can be
ready at once, but one queued step is executed at a time in topological order.

## Design Principles

1. **Local-First** - run state is file-backed, with `ref/tmp/` as the default layout
2. **Deterministic** - ordering is reproducible from the compiled graph and event log
3. **Provider-Agnostic** - runtime targets one adapter SPI instead of hard-coding providers
4. **Auditable** - meaningful transitions are persisted before checkpoint updates

## Run Layout

```text
ref/tmp/
├── locks/
│   └── <repo>.lock.json
└── runs/
    └── <run-id>/
        ├── workflow.json
        ├── events.jsonl
        ├── checkpoint.json
        ├── artifacts.json
        ├── locks/
        │   └── repo.lock.json
        └── provider-logs/
            └── <step-id>/
                ├── <attempt>-stdout.log
                └── <attempt>-stderr.log
```

## CLI Commands

Implemented commands:

- `cogito workflow validate <file>`
- `cogito run <file>`
- `cogito status --state-dir <dir>`
- `cogito resume --state-dir <dir>`
- `cogito approve --state-dir <dir>`
- `cogito cancel --state-dir <dir>`
- `cogito replay <events.jsonl>`

Shared execution flags include `--repo`, `--state-dir`, `--approval`,
`--provider-timeout`, and `--allow-dirty`.

## Provider Support

Built-in adapters are registered for:

- `codex`
- `claude`
- `opencode`

These adapters currently wrap provider CLIs and expose machine-readable logs. The
SPI also models interrupt/resume capabilities, but provider-native support for
those flows is not fully implemented in the built-in adapters yet.

## Documentation

- [Architecture Overview](./docs/design/01-architecture.md)
- [Workflow DSL](./docs/design/02-workflow-dsl.md)
- [Storage Model](./docs/design/03-storage.md)
- [Runtime State Machine](./docs/design/04-runtime.md)
- [Adapter SPI](./docs/design/05-adapters.md)
- [CLI Commands](./docs/design/06-cli.md)
- [Approval Gates](./docs/design/07-approval.md)
- [Error Model](./docs/design/08-errors.md)

## Project Structure

```text
cogito/
├── cmd/cogito/              # CLI entrypoint
├── internal/
│   ├── app/                 # command routing, wiring, presenters
│   ├── workflow/            # YAML parsing and DAG compilation
│   ├── runtime/             # state machine, approvals, replay, locks
│   ├── store/               # file-backed persistence
│   ├── adapters/            # provider adapter SPI and implementations
│   ├── executor/            # local command supervision
│   └── version/             # version reporting
├── docs/design/             # design documentation
└── justfile                 # development commands
```

## Development

### Prerequisites

- Go 1.21+
- [just](https://github.com/casey/just) (optional)

### Commands

```bash
just build    # Build binary
just test     # Run tests
just lint     # Run linter
just cover    # Coverage report
```

## Error Handling

Subsystems expose structured errors with stable codes (`workflow`, `runtime`,
`store`, `adapters`, `executor`). The CLI prints surfaced errors to stderr and
returns exit code `1` on failure.

For runs that settle into the `failed` state, the app layer may surface the latest
durable event summary as `run failed: <message>` rather than only returning the
first in-process error.

See `docs/design/08-errors.md` for the full error taxonomy and propagation model.

## License

[MIT License](./LICENSE)
