# Cogito

A CLI-first multi-agent workflow orchestrator for deterministic, auditable AI coding workflows.

## Overview

Cogito executes static, template-based workflows across multiple AI coding providers (Codex, Claude, OpenCode) with full event sourcing, checkpoint recovery, and human-in-the-loop approval gates.

**Key Features:**
- 🔄 **Deterministic Execution** - Reproducible runs from event logs
- 📦 **Provider-Agnostic** - Unified interface for multiple AI providers
- 💾 **Event Sourcing** - Complete audit trail with replay capability
- ⏸️ **Resumable** - Pause, resume, and recover from failures
- 🚦 **Approval Gates** - Human-in-the-loop control at critical steps
- 🔒 **Local-First** - File-backed storage, no daemon required

## Quick Start

### Installation

```bash
go install github.com/JackDrogon/Cogito/cmd/cogito@latest
```

Or build from source:

```bash
git clone https://github.com/JackDrogon/Cogito.git
cd Cogito
just build
```

### Basic Usage

```bash
# Validate a workflow
cogito workflow validate workflow.yaml

# Execute a workflow
cogito run workflow.yaml --state-dir ./run-output

# Check status
cogito status --state-dir ./run-output

# Resume a paused workflow
cogito resume --state-dir ./run-output

# Replay from event log
cogito replay ./run-output/events.jsonl
```

## Workflow Example

```yaml
apiVersion: cogito/v1alpha1
kind: Workflow
metadata:
  name: deploy-app
steps:
  - id: build
    kind: command
    command: "go build -o app ."
    
  - id: test
    kind: command
    command: "go test ./..."
    needs: ["build"]
    
  - id: approve-deploy
    kind: approval
    needs: ["test"]
    message: "Approve production deployment?"
    
  - id: deploy
    kind: agent
    needs: ["approve-deploy"]
    agent: "deployer"
    prompt: "Deploy the built application to production"
```

## Architecture

Cogito is built on three core components:

1. **Workflow Engine** - Parses YAML, validates DAG, schedules steps
2. **Runtime State Machine** - Manages execution state with event sourcing
3. **Adapter SPI** - Provider-agnostic interface for AI agents

## Documentation

- [Architecture Overview](./docs/design/01-architecture.md)
- [Workflow DSL](./docs/design/02-workflow-dsl.md)
- [Storage Model](./docs/design/03-storage.md)
- [Runtime State Machine](./docs/design/04-runtime.md)
- [Adapter SPI](./docs/design/05-adapters.md)
- [CLI Commands](./docs/design/06-cli.md)
- [Approval Gates](./docs/design/07-approval.md)

## Project Structure

```
cogito/
├── cmd/cogito/              # CLI entrypoint
├── internal/
│   ├── workflow/            # Workflow parsing and validation
│   ├── runtime/             # State machine and execution engine
│   ├── store/               # Event log and checkpoint storage
│   ├── adapters/            # Provider adapters
│   ├── executor/            # Process supervision
│   └── app/                 # CLI command routing
├── docs/design/             # Design documentation
└── justfile                 # Development commands
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

## Design Principles

1. **Local-First** - File-backed, no daemon
2. **Deterministic** - Reproducible from events
3. **Provider-Agnostic** - Unified interface
4. **Auditable** - Complete event log

## Storage Model

```
ref/tmp/runs/<run-id>/
├── workflow.json
├── events.jsonl
├── checkpoint.json
├── artifacts.json
└── locks/
```

## License

[MIT License](./LICENSE)
