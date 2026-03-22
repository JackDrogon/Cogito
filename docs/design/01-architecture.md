# Architecture Overview

## System Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                         CLI Layer                            │
│  (cmd/cogito + internal/app)                                │
│  - Command routing                                           │
│  - Flag parsing                                              │
│  - Output formatting                                         │
└────────────────┬────────────────────────────────────────────┘
                 │
┌────────────────▼────────────────────────────────────────────┐
│                    Orchestration Layer                       │
│  (internal/runtime)                                          │
│  - State machine                                             │
│  - Step scheduling                                           │
│  - Approval resolution                                       │
│  - Event emission                                            │
└────┬──────────┬──────────┬──────────┬──────────────────────┘
     │          │          │          │
     │          │          │          │
┌────▼─────┐ ┌─▼────────┐ ┌▼────────┐ ┌▼──────────────────┐
│ Workflow │ │  Store   │ │ Adapters│ │    Executor       │
│ Parser   │ │ (Events/ │ │ (Codex/ │ │ (Process Super-   │
│          │ │ Checkpt) │ │ Claude/ │ │  visor)           │
│          │ │          │ │ OpenCode│ │                   │
└──────────┘ └──────────┘ └─────────┘ └───────────────────┘
```

## Component Responsibilities

### CLI Layer (`cmd/cogito`, `internal/app`)
- Parse command-line arguments
- Route to appropriate command handlers
- Format output for user consumption
- Handle errors and exit codes

### Workflow Parser (`internal/workflow`)
- Parse YAML workflow definitions
- Validate schema and semantics
- Build dependency DAG
- Compile to executable representation

### Storage Layer (`internal/store`)
- Append-only event log (`events.jsonl`)
- Atomic checkpoint snapshots (`checkpoint.json`)
- Artifact index (`artifacts.json`)
- Repository locking

### Runtime Engine (`internal/runtime`)
- Deterministic state machine
- Topological step scheduling
- Approval gate management
- Event-sourced state reconstruction

### Adapter Layer (`internal/adapters`)
- Provider capability matrix
- Unified execution interface
- Result normalization
- Contract test suite

### Executor (`internal/executor`)
- Process supervision
- Timeout enforcement
- Signal handling
- Log capture

## Data Flow

### Workflow Execution Flow

```
1. Parse workflow YAML
   └─> Validate schema
       └─> Build DAG
           └─> Compile to executable

2. Initialize run
   └─> Create run directory
       └─> Persist resolved workflow
           └─> Initialize checkpoint

3. Execute steps (topological order)
   └─> For each ready step:
       ├─> Check approval requirements
       ├─> Lookup adapter
       ├─> Start execution
       ├─> Poll for completion
       ├─> Normalize result
       ├─> Emit events
       └─> Update checkpoint

4. Handle completion
   └─> Emit terminal event (succeeded/failed/canceled)
       └─> Save final checkpoint
```

### Event Sourcing Pattern

```
Event Log (append-only)     Checkpoint (snapshot)
─────────────────────        ────────────────────
RunCreated                   {
RunStarted                     "state": "running",
StepQueued (step-1)            "steps": {
StepStarted (step-1)             "step-1": "succeeded",
StepSucceeded (step-1)           "step-2": "running"
StepQueued (step-2)            },
StepStarted (step-2)           "last_sequence": 6
...                          }
```

On restart:
1. Load checkpoint
2. Compare `last_sequence` with event log
3. If events are ahead, replay from checkpoint sequence
4. Resume execution from current state

## Directory Structure

```
ref/tmp/
├── locks/                    # Repository-level locks
│   └── repo-{hash}.lock.json
└── runs/
    └── {run-id}/
        ├── events.jsonl      # Append-only event log
        ├── checkpoint.json   # Latest state snapshot
        ├── artifacts.json    # Artifact index
        ├── workflow.json     # Resolved workflow
        ├── locks/            # Run-local lock mirror
        └── provider-logs/    # Provider stdout/stderr
            └── {step-id}/
                └── attempt-{id}-{stdout,stderr}.log
```

## Concurrency Model

- **Single-run policy**: One workflow execution per repository at a time
- **Repository lock**: Acquired at run start, released on exit
- **Dirty worktree check**: Prevents runs on uncommitted changes (unless `--allow-dirty`)
- **Stale lock recovery**: Detects and reclaims locks from dead processes

## Error Handling Strategy

1. **Validation errors**: Fail fast at parse/compile time
2. **Runtime errors**: Persist to event log, update checkpoint, exit with error
3. **Provider errors**: Normalize to standard error codes, allow retry classification
4. **Approval denial**: Persist denial event, mark run as failed
5. **Crash recovery**: Replay from event log on next invocation
