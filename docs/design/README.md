# Cogito Design Documentation

Cogito is a CLI-first multi-agent coding workflow orchestrator that executes static, template-based workflows across multiple AI coding providers (Codex, Claude, OpenCode).

## Documentation Structure

- [Architecture Overview](./01-architecture.md) - System architecture and component design
- [Workflow DSL](./02-workflow-dsl.md) - Workflow definition language specification
- [Storage Model](./03-storage.md) - Event log, checkpoints, and persistence
- [Runtime State Machine](./04-runtime.md) - Deterministic execution engine
- [Adapter SPI](./05-adapters.md) - Provider integration interface
- [CLI Commands](./06-cli.md) - Command-line interface design
- [Approval Gates](./07-approval.md) - Human-in-the-loop approval system

## Quick Start

```bash
# Validate a workflow
cogito workflow validate path/to/workflow.yaml

# Execute a workflow
cogito run path/to/workflow.yaml --state-dir ./run-output

# Check status
cogito status --state-dir ./run-output

# Resume a paused workflow
cogito resume --state-dir ./run-output

# Replay from event log
cogito replay ./run-output/events.jsonl
```

## Design Principles

1. **Local-first**: Single-user, file-backed, no daemon required
2. **Deterministic**: Reproducible execution from event history
3. **Provider-agnostic**: Unified interface for multiple AI providers
4. **Exception-driven**: Approval gates only on failures or explicit steps
5. **Auditable**: Complete event log for debugging and replay
