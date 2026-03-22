# Approval Gates

Approval is how Cogito pauses an otherwise deterministic workflow and waits for a
human or policy decision before continuing. The implementation lives in
`internal/runtime/approval*.go` and is integrated directly into the runtime state
machine instead of being modeled as a separate external service.

## Approval Triggers

Cogito currently supports three approval triggers.

### 1. Explicit workflow approval

An `approval` step in YAML creates a first-class workflow gate:

```yaml
- id: approve-release
  kind: approval
  message: Approve release deployment?
```

Runtime starts the approval step, synthesizes a provider session ID, and requests
approval using the step message as the default summary.

### 2. Adapter-triggered approval

If a provider step normalizes to `ExecutionStateWaitingApproval`, runtime treats it
as an approval gate and transitions the step into `waiting_approval`.

This is how a provider can request human input in the middle of a step.

### 3. Policy-triggered approval

Before starting a non-approval step, runtime asks `ApprovalPolicy.EvaluateException`
whether an exceptional approval gate should be inserted.

If the policy says approval is required:

- runtime synthesizes a provider session ID
- marks the step as started
- emits `ApprovalRequested`
- moves the run to `waiting_approval`

This creates a gate before the real step execution begins.

## Approval Modes

The default built-in policy is `approvalModePolicy`, configured from `--approval`.

Supported modes:

- `auto` - request approval and wait
- `approve` - auto-approve immediately
- `deny` - auto-deny immediately

An empty CLI flag resolves to `auto`.

## Persisted Approval Data

Approval state is stored inside both the event log and step checkpoint data.

### Step checkpoint fields

When a step is waiting for approval, its checkpoint includes:

- `approval_id`
- `approval_trigger`
- `attempt_id`
- `provider_session_id`
- `summary`

### Approval-related event types

- `ApprovalRequested`
- `ApprovalGranted`
- `ApprovalDenied`
- `ApprovalTimedOut`

Approval events also carry transition metadata such as:

- `from_state`
- `to_state`
- `provider_session_id`
- `approval_trigger`
- `summary`
- `occurred_at`

## Runtime State Changes

Entering approval always affects both the step and the run.

### Enter approval

```text
Step: running -> waiting_approval
Run:  running -> waiting_approval
```

### Resolve approval

#### Approve

```text
Step: waiting_approval -> running
Run:  waiting_approval -> running
```

After that transition, runtime continues the step according to the trigger type.

#### Deny / timeout

```text
Step: waiting_approval -> failed
Run:  waiting_approval -> failed
```

Both deny and timeout are terminal for the current run.

## Continuation Strategy by Trigger

This is the most important implementation detail to understand when debugging
approval behavior.

### Explicit approval step

After approval is granted, runtime resumes the synthetic approval driver. That
driver returns a succeeded execution, so the approval step completes successfully.

### Adapter-triggered approval

After approval is granted, runtime resumes the step driver with the existing
`ExecutionHandle`, which gives the provider adapter a chance to continue the same
provider session.

### Policy-triggered approval

After approval is granted, runtime starts the step driver rather than resuming it.
This is because policy approval happened before any real provider or command work
had started.

## CLI Integration

The CLI currently exposes one approval-resolution command:

```bash
cogito approve --state-dir ./ref/tmp/runs/run-123
```

That command calls `engine.GrantApproval(ctx, "approved via CLI")` and then keeps
executing until the run settles again.

There is currently no dedicated CLI for deny or timeout, although runtime has the
internal APIs `DenyApproval` and `TimeoutApproval`.

## Failure Modes

Approval handling can fail for several reasons:

- run is not actually in `waiting_approval`
- no waiting step can be found
- more than one waiting approval is present (not supported)
- waiting step is missing `approval_id`
- a provider driver cannot resume after approval

These failures are treated as runtime execution errors rather than silent no-ops.

## Current Constraints

- only one pending approval is supported at a time
- deny/timeout do not have dedicated CLI commands
- policy-triggered approval can gate a step before real provider execution exists
- adapter-level resume depends on future provider capability work for full fidelity
