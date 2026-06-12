# Provider SPI

Cogito uses an provider service-provider interface to keep workflow orchestration
independent from concrete AI tools such as Codex, Claude, and OpenCode.

The current implementation lives in `internal/provider` and is designed around a
staged lifecycle rather than a single blocking call.

The original three design goals are still a useful way to read the SPI:

1. **Abstraction** - hide provider-specific CLI details behind a stable interface
2. **Feature detection** - model optional behavior through a capability matrix
3. **Consistency** - normalize provider output into one workflow-facing result type

## Adapter Interface

```go
type Provider interface {
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

Runtime checks capabilities before relying on these methods. The built-in providers
now implement both on top of `internal/provider`:

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
output text, artifact refs, logs, and optional usage metadata.

Usage normalization preserves only per-step audit fields: input tokens, output
tokens, total tokens, and provider-reported USD cost when present. Codex reports
token counts in its `turn.completed` NDJSON `usage` object; Cogito records the
latest reported usage and derives `total_tokens` from input plus output. Claude's
`--print --output-format json` result reports `usage` token fields and
`total_cost_usd`, both mapped directly. OpenCode's parsed JSON response currently
does not expose documented token or cost fields in this adapter, so its usage is
left nil and no `usage` field is persisted.

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

The three built-in providers now declare the same matrix:

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

Three providers are registered via package `init()`:

- `codex`
- `claude`
- `opencode`

### Shared current behavior

All three providers currently:

- register themselves with the process-local registry
- launch the provider CLI through the shared async `internal/provider`
- hold the live `provider.Session` (with its cancel func) in an in-memory map
- honor optional `ProcessRequest.Timeout` wall-clock and `IdleTimeout`
  no-output watchdogs when the app passes `--agent-timeout` or
  `--agent-idle-timeout`; both default to `0` / disabled
- ask the shared process supervisor to write `<attempt>.pid.json` beside the
  provider log while a child process is alive; the supervisor removes that file
  just before delivering `ProcessResult`, so naturally-finished processes leave
  no pidfile behind
- support `PollOrCollect` by awaiting the session's `Done` channel
- parse the `AGENT_RESULT_JSON` line into a normalized `AgentResult` and marshal
  it into `Execution.StructuredOutput`
- expose `structured_output`, `resume`, `interrupt`, and `machine_readable_logs`

Provider process logs are redacted before they reach provider log files or the
live `ExtraSink` console stream. Runtime event persistence and command-step
stdout/stderr artifacts use the same secret heuristics before writing durable
audit records. At redaction time, Cogito reads the parent environment and treats
values of variables whose names contain
`TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `CREDENTIAL`, `API_KEY`, `APIKEY`,
`PRIVATE_KEY`, or `AUTH` (case-insensitive) as secrets when the value is at least
8 bytes. Matching output bytes are replaced with `***REDACTED***`, including
matches split across stream chunks. The in-memory stdout/stderr buffers remain
raw so session-id scraping and `AGENT_RESULT_JSON` parsing continue to operate on
the provider's original output.

### Orphan process pidfiles

Provider children run in their own process group (`setsid`) so Cogito can signal
the whole provider tree. If the parent Cogito process is killed with `SIGKILL`,
that process group can survive and keep mutating the repository. To make this
discoverable without polluting deterministic replay state, `ProcessRequest.PIDFile`
points the supervisor at a plain JSON pidfile under
`provider-logs/<step-id>/<attempt>.pid.json`.

Pidfiles contain the child `pid`, `pgid`, provider `binary`, `started_at`, and a
free-form run/step/attempt label. They are machine-local and ephemeral: they are
not events, checkpoints, or artifacts. `StartProcess` writes them after
`cmd.Start()` with warn-only error handling, and the supervise goroutine removes
them on every normal completion path, including cancellation.

On a later `cogito resume` or `cogito cancel`, the app calls
`provider.ReapOrphans(runDir)` before continuing runtime work. Reaping first
classifies each pidfile:

| Process state | Identity check | Action |
|---------------|----------------|--------|
| alive | `/proc/<pid>/cmdline` basename matches recorded binary | send SIGTERM to `-pgid`, wait `DefaultExitGrace`, then SIGKILL to `-pgid` if needed; remove pidfile |
| alive | binary mismatch or unsafe PID | do not signal; remove stale pidfile |
| dead | not applicable | remove stale pidfile |

The identity check is mandatory because PIDs can be reused. `cogito status` uses
`provider.FindOrphans(runDir)` and is read-only: it reports only alive,
identity-matched provider orphans and leaves pidfiles and processes untouched.

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

### Process timeout semantics

`internal/provider.ProcessRequest` carries two optional agent-process watchdogs:

- `Timeout`: maximum wall-clock duration from `StartProcess` until process exit.
- `IdleTimeout`: maximum gap with no stdout/stderr bytes from the process.

Values `<= 0` disable the corresponding watchdog. When either watchdog fires, the
shared process supervisor records `ProcessResult.TimeoutReason` with a human
readable summary such as `agent process timed out after 30m0s` or
`agent process produced no output for 10m0s`, then invokes the same cancel path as
manual interrupt (SIGTERM, grace, SIGKILL). Because that cancel path usually sets
`ProcessResult.Interrupted`, adapters must check `TimeoutReason` first. Built-in
adapters classify timeout results as `ExecutionStateFailed`, not
`ExecutionStateInterrupted`, so runtime records the existing failed-step events and
does not treat the execution as resumable.

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

1. built-in local provider resolver
2. registered provider resolver

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

Adapters report structured errors using `internal/provider/errors.go`.
Common error classes include:

- request validation errors
- execution errors (binary missing, CLI failure)
- result parsing errors
- unsupported capability errors

Runtime wraps those failures into step and run transition events.

## Contract Testing

Earlier drafts mentioned `contract_suite.go`, and that is still a real and useful
part of the implementation. `internal/provider/contract_suite.go` defines reusable
tests that verify:

- capability reporting via `DescribeCapabilities`
- start/poll/normalize behavior
- interrupt behavior when provided
- resume behavior when provided

This keeps provider implementations aligned with the SPI without forcing every
provider package to hand-roll the same lifecycle assertions.

## What is not implemented yet

The SPI is broader than the currently shipped providers. After the AgentLoop port,
resume, interrupt, and structured output are live, but the built-in providers still
do not provide:

- mandatory structured output contracts (an agent that omits the
  `AGENT_RESULT_JSON` line yields a zero-value `AgentResult`, not an error)
- provider-derived artifact references (`artifact_refs` stays false)

Those remaining capabilities can be added later without changing runtime
orchestration, which is the main reason the SPI is wider than today's provider
behavior.
