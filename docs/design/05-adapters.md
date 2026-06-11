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

Runtime checks capabilities before relying on these methods. The built-in adapters
now implement both on top of `internal/adapters/runner`:

- `Interrupt` looks up the live session by id and calls its cancel func, which
  signals the process group (SIGTERM, grace, then SIGKILL).
- `Resume` re-attaches to a prior provider session id by injecting a provider
  resume flag into the argv (`resume <sid>` for codex, `--resume <sid>` for
  claude, `--session <sid>` for opencode). `ResumeRequest.Prompt` carries the
  prompt to continue with — by default the step's main prompt, but runtime may
  substitute a recovery prompt (see `04-runtime.md`, "Recovery prompt selection").

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

### Built-in capability matrix

The three built-in adapters now declare the same matrix:

| Capability | codex | claude | opencode |
|------------|-------|--------|----------|
| `structured_output` | true | true | true |
| `resume` | true | true | true |
| `interrupt` | true | true | true |
| `machine_readable_logs` | true | true | true |
| `artifact_refs` | false | false | false |

`structured_output`, `resume`, and `interrupt` were all flipped to true during the
AgentLoop port: adapters parse the `AGENT_RESULT_JSON` result line into a
normalized `AgentResult` (`structured_output`), re-attach to a prior session
(`resume`), and cancel a running process group (`interrupt`). `artifact_refs`
remains false — artifacts are still derived by runtime, not reported by providers.

## Current Built-in Providers

Three adapters are registered via package `init()`:

- `codex`
- `claude`
- `opencode`

### Shared current behavior

All three adapters currently:

- register themselves with the process-local registry
- launch the provider CLI through the shared async `internal/adapters/runner`
- hold the live `runner.Session` (with its cancel func) in an in-memory map
- support `PollOrCollect` by awaiting the session's `Done` channel
- parse the `AGENT_RESULT_JSON` line into a normalized `AgentResult` and marshal
  it into `Execution.StructuredOutput`
- expose `structured_output`, `resume`, `interrupt`, and `machine_readable_logs`

### Provider-specific command style

Prompts are delivered on stdin for codex/claude and as the final argv for
opencode. The session-resume flag differs per provider:

- **Codex**: `codex exec --cd <root> --sandbox <sandbox> --skip-git-repo-check
  --json --color never --output-last-message <path> [--model <m>] [resume <sid>] -`
- **Claude**: `claude --print --permission-mode bypassPermissions --output-format
  json [--model <m>] [--resume <sid>]` (prompt on stdin)
- **OpenCode**: `opencode run --dir <root> --dangerously-skip-permissions
  --print-logs --output-format json [--model <m>] [--session <sid>] <prompt>`

Each adapter owns CLI invocation, output parsing, and conversion into `Execution`
/ `StepResult`.

### SessionID extraction protocol

The runner mints a synthetic session id at `Start` so a session is addressable
immediately (the async model returns before the process exits). While streaming
provider stdout, it matches a `session id: <uuid>` line and, when found, replaces
the synthetic id with the real provider session id (updating the `sessions` map
key as well). That extracted id flows back through `Result.SessionID` →
`Execution.Handle.ProviderSessionID`, and runtime persists it so a later `Resume`
can pass it to the provider's resume flag. When no session line is matched, the
synthetic id is kept so interrupt/await still work in-process.

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

The SPI is broader than the currently shipped providers. After the AgentLoop port,
resume, interrupt, and structured output are live, but the built-in adapters still
do not provide:

- mandatory structured output contracts (an agent that omits the
  `AGENT_RESULT_JSON` line yields a zero-value `AgentResult`, not an error)
- provider-derived artifact references (`artifact_refs` stays false)

Those remaining capabilities can be added later without changing runtime
orchestration, which is the main reason the SPI is wider than today's provider
behavior.
