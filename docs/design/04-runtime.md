# Runtime State Machine

The runtime package is the execution core of Cogito. It advances a compiled
workflow by persisting events, folding them into an in-memory `Snapshot`, and
rewriting `checkpoint.json` after every durable transition.

The earlier summary of runtime goals is still accurate:

- **Deterministic scheduling** - ready steps are discovered from a stable topological order
- **State integrity** - transitions are validated against explicit allow-lists
- **Resilience** - snapshot state can be rebuilt from durable event history

## Core Model

`runtime.Engine` receives:

- a `workflow.CompiledWorkflow`
- an event store
- adapter lookup and command execution collaborators
- an approval policy
- repo/working directory context

The engine never mutates state optimistically. It first previews whether an event
would be valid, then appends the event, applies it to the live snapshot, and saves
the checkpoint.

## Run States

Run lifecycle states are defined in `internal/runtime/state_machine.go`:

- `pending`
- `running`
- `waiting_approval`
- `paused`
- `succeeded`
- `failed`
- `canceled`

### Allowed run transitions

```text
pending -> running, canceled
running -> waiting_approval, paused, succeeded, failed, canceled
waiting_approval -> running, paused, failed, canceled
paused -> running, canceled
```

Terminal states have no outbound transitions.

### State descriptions

- `pending` - run exists but execution has not advanced yet
- `running` - runtime is actively queueing or executing work
- `waiting_approval` - execution is paused on an approval gate
- `paused` - run was interrupted or explicitly paused and can later resume
- `succeeded` - all steps completed successfully
- `failed` - a step failed, approval was denied, or runtime could not continue safely
- `canceled` - run was explicitly canceled

## Step States

Step lifecycle states are:

- `pending`
- `queued`
- `running`
- `waiting_approval`
- `succeeded`
- `failed`
- `canceled`

### Allowed step transitions

```text
pending -> queued, canceled
queued -> running, canceled
running -> waiting_approval, queued, succeeded, failed, canceled
waiting_approval -> running, queued, succeeded, failed, canceled
failed -> queued, canceled
```

This means failed steps are structurally retryable, although the current CLI does
not expose a dedicated retry command.

## Initialization and Replay

When `NewEngine` is created, runtime loads checkpoint and event history from the
store and chooses one of three initialization paths:

1. **checkpoint only** - if checkpoint exists and is at least as fresh as the event log
2. **event replay** - if events are newer than the checkpoint
3. **empty snapshot** - if there is no persisted history yet

If no snapshot exists, the first runtime action is `RunCreated`, which also
initializes all compiled steps into `pending` state.

## Scheduling

Scheduling is deterministic and topological.

### Ready-step selection

Runtime scans `compiled.TopologicalOrder` and selects steps that are:

- still `pending`
- and have all dependencies in `succeeded`

Those steps are persisted as `StepQueued` in declaration/topological order.

### Execution policy

The engine then reads all queued step IDs and executes only the first queued step
for that engine tick. As a result:

- multiple steps can be ready at once
- their queued order is deterministic
- actual execution is sequential within one run

This preserves the old "parallel readiness, sequential execution" explanation,
which was accurate and remains an important distinction for readers.

## Execution Flow

```text
RunCreated
  -> RunStarted
     -> StepQueued (all newly ready steps)
        -> StepStarted
           -> one of:
              - StepSucceeded -> maybe queue dependents
              - StepFailed -> RunFailed
              - StepRetried -> RunPaused
              - ApprovalRequested -> RunWaitingApproval
```

## Approval Integration

Runtime supports three approval triggers:

- `explicit` - `kind: approval` workflow step
- `adapter` - provider returns `waiting_approval`
- `policy` - `ApprovalPolicy.EvaluateException` injects a gate before normal execution

### Entering approval

Approval entry always persists two events in order:

1. `ApprovalRequested`
2. `RunWaitingApproval`

The waiting step stores:

- `attempt_id`
- `provider_session_id`
- `approval_id`
- `approval_trigger`
- `summary`

### Resolving approval

Runtime exposes:

- `GrantApproval`
- `DenyApproval`
- `TimeoutApproval`

Resolution behavior:

- **approve** -> `ApprovalGranted`, run returns to `running`, then the step continues
- **deny** -> `ApprovalDenied`, step becomes `failed`, run becomes `failed`
- **timeout** -> `ApprovalTimedOut`, step becomes `failed`, run becomes `failed`

### Continuation rules by trigger

- `explicit` -> step resumes via the approval driver and becomes `succeeded`
- `adapter` -> runtime calls the driver's `Resume`
- `policy` -> runtime starts the driver from scratch using the same attempt ID

This distinction is important because policy approval happens before a real provider
session exists, while adapter-triggered approval happens during provider execution.

## Pause and cancel behavior

### Pause

- A running run can be paused explicitly.
- If a step returns `interrupted`, runtime persists `StepRetried` and sets the run to `paused`.

### Cancel

- `pending`, `running`, `waiting_approval`, and `paused` runs can be canceled.
- If the run is actively `running`, runtime first attempts to interrupt the active step.
- `RunCanceled` also marks all non-terminal steps as `canceled` when events are folded.

## Snapshot Contents

The in-memory `Snapshot` stores:

- `run_id`
- `state`
- `last_sequence`
- `updated_at`
- step snapshots keyed by step ID

Each `StepSnapshot` stores:

- `state`
- `attempt_id`
- `provider_session_id`
- `approval_id`
- `approval_trigger`
- `summary`

## Replay Guarantees

Replay validates more than just JSON shape.

- event `run_id` must match the replayed run
- sequences must be contiguous
- referenced step IDs must exist in the compiled workflow
- every transition must be allowed by the transition tables
- `occurred_at` must exist in event data

If any event violates those rules, replay fails instead of silently producing a
best-effort state.

This preserves the original replay contract: replay is not a best-effort viewer,
it is a validation boundary for durable runtime history.

## Status and Replay Views

The runtime package also produces view models consumed by the CLI:

- `RunStatusView` - run ID, run state, and per-step summaries in topological order
- `ReplayView` - replayed transitions plus final step statuses

These views are intentionally derived from the same compiled workflow + snapshot /
replay result pair, so inspection paths stay aligned with execution paths.
