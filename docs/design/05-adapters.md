# Adapter SPI

Cogito uses an adapter service-provider interface to keep workflow orchestration
independent from concrete AI tools such as Codex, Claude, and OpenCode.

The current implementation lives in `internal/adapters` and is designed around a
staged lifecycle rather than a single blocking call.

The original three design goals are still a useful way to read the SPI:

1. **Abstraction** - hide provider-specific CLI details behind a stable interface
2. **Feature detection** - model optional behavior through a capability matrix
3. **Consistency** - normalize provider output into one workflow-facing result type

## Adapter Interface

```go
type Adapter interface {
    DescribeCapabilities() CapabilityMatrix
    Start(ctx context.Context, request StartRequest) (*Execution, error)
    PollOrCollect(ctx context.Context, handle ExecutionHandle) (*Execution, error)
    Interrupt(ctx context.Context, handle ExecutionHandle) (*Execution, error)
    Resume(ctx context.Context, request ResumeRequest) (*Execution, error)
    NormalizeResult(ctx context.Context, request NormalizeRequest) (*StepResult, error)
}
```

## Lifecycle Contract

### 1. `Start`

Starts provider execution and returns an initial `Execution`.

`StartRequest` includes:

- `RunID`
- `StepID`
- `AttemptID`
- `WorkingDir`
- `Prompt`

### 2. `PollOrCollect`

Returns the current provider-facing execution snapshot. Runtime keeps polling until
the execution state becomes normalizable.

### 3. `Interrupt` / `Resume`

These methods exist in the SPI, but runtime checks capabilities before relying on
them. Today, the built-in provider adapters do not implement provider-native
interrupt or resume and will return capability errors when asked.

### 4. `NormalizeResult`

Transforms a provider-facing `Execution` into a workflow-facing `StepResult`.
This is the boundary where provider-specific output becomes runtime-safe status,
output text, artifact refs, and logs.

## Capability Matrix

Capabilities are represented by booleans on `CapabilityMatrix`:

- `structured_output`
- `resume`
- `interrupt`
- `artifact_refs`
- `machine_readable_logs`

`CapabilityMatrix.Require` is used to fail fast when runtime needs a feature that
an adapter does not support.

| Capability | Meaning in Cogito |
|------------|-------------------|
| `structured_output` | Adapter can provide machine-readable structured payloads |
| `resume` | Adapter can continue an existing provider session |
| `interrupt` | Adapter can stop a running provider session |
| `artifact_refs` | Adapter can report artifact references directly |
| `machine_readable_logs` | Adapter emits logs that runtime can preserve structurally |

## Current Built-in Providers

Three adapters are registered via package `init()`:

- `codex`
- `claude`
- `opencode`

### Shared current behavior

All three adapters currently:

- register themselves with the process-local registry
- shell out to the provider CLI
- persist the returned execution in an in-memory session map
- support `PollOrCollect` by replaying the cached session snapshot
- expose `machine_readable_logs` capability
- do **not** currently implement provider-native `interrupt` or `resume`

### Provider-specific command style

- **Codex**: `codex exec --json --color never --output-last-message <path> <prompt>`
- **Claude**: `claude --print --output-format json <prompt>`
- **OpenCode**: `opencode run --json <prompt>` or `opencode-desktop run --json <prompt>`

The older docs also emphasized that adapters translate between Cogito's internal
execution model and provider-specific environments. That remains accurate: each
adapter owns CLI invocation, output parsing, and conversion into `Execution` /
`StepResult`.

## Execution Data Types

### `Execution`

Represents provider-facing state during or after execution.

Important fields:

- `Handle`
- `State`
- `Summary`
- `OutputText`
- `StructuredOutput`
- `ArtifactRefs`
- `Logs`

### `StepResult`

Represents the normalized result consumed by runtime. It mirrors the major fields
of `Execution`, but uses `Status` instead of `State` to emphasize the transition
from provider state to workflow result.

### `ExecutionState`

The SPI defines these states:

- `running`
- `succeeded`
- `failed`
- `interrupted`
- `waiting_approval`

States considered normalizable by runtime are:

- `succeeded`
- `failed`
- `interrupted`
- `waiting_approval`

## Registry Model

Adapters are discovered through a process-local registry.

```go
type Registration struct {
    Name         string
    Capabilities CapabilityMatrix
    New          Factory
}
```

The registry supports:

- `Register`
- `Lookup`
- `RegisteredNames`

The application layer resolves adapters in this order:

1. built-in local adapter resolver
2. registered adapter resolver

This keeps runtime itself decoupled from provider package imports.

## Relationship with runtime drivers

Runtime does not call adapters directly for every step kind. Instead it builds a
`stepDriver` based on the compiled step kind:

- `agentDriver` -> wraps an adapter
- `commandDriver` -> wraps the command runner
- `approvalDriver` -> synthetic driver for explicit approval steps

That means approval steps participate in the same runtime lifecycle as provider
and command steps even though no external provider binary is involved.

## Error Boundary

Adapters report structured errors using `internal/adapters/errors.go`.
Common error classes include:

- request validation errors
- execution errors (binary missing, CLI failure)
- result parsing errors
- unsupported capability errors

Runtime wraps those failures into step and run transition events.

## Contract Testing

Earlier drafts mentioned `contract_suite.go`, and that is still a real and useful
part of the implementation. `internal/adapters/contract_suite.go` defines reusable
tests that verify:

- capability reporting via `DescribeCapabilities`
- start/poll/normalize behavior
- interrupt behavior when provided
- resume behavior when provided

This keeps adapter implementations aligned with the SPI without forcing every
provider package to hand-roll the same lifecycle assertions.

## What is not implemented yet

The SPI is broader than the currently shipped providers. In particular, the built-
in adapters do not yet provide:

- provider-native resume
- provider-native interrupt
- mandatory structured output contracts
- provider-derived artifact references

Those capabilities can be added later without changing runtime orchestration,
which is the main reason the SPI is wider than today's provider behavior.
