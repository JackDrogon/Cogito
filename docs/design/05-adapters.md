# Adapter SPI

The Adapter Service Provider Interface (SPI) provides a provider-agnostic way to execute agent steps. It decouples the core execution engine from specific agent implementations like Codex, Claude, or OpenCode. This architecture ensures that Cogito can support new AI providers by simply adding a new adapter without modifying the core runtime.

## Overview

Adapters act as the translation layer between Cogito's internal execution model and various external agent environments. Each adapter manages the lifecycle of a single execution step, handling process invocation, output streaming, and result parsing.

The SPI is designed around three core concepts:
1. **Abstraction**: Hiding provider-specific CLI flags or API details.
2. **Feature Detection**: Using a capability matrix to handle varying provider features.
3. **Consistency**: Normalizing diverse outputs into a standard step result.

## Adapter Interface

The `Adapter` interface defines the contract for all provider implementations:

```go
type Adapter interface {
    // DescribeCapabilities returns the static capabilities supported by this adapter.
    DescribeCapabilities() CapabilityMatrix

    // Start initiates a new execution step.
    Start(ctx context.Context, request StartRequest) (*Execution, error)

    // PollOrCollect retrieves the current state of an ongoing execution.
    PollOrCollect(ctx context.Context, handle ExecutionHandle) (*Execution, error)

    // Interrupt stops a running execution.
    Interrupt(ctx context.Context, handle ExecutionHandle) (*Execution, error)

    // Resume continues an execution that was waiting for input.
    Resume(ctx context.Context, request ResumeRequest) (*Execution, error)

    // NormalizeResult converts an Execution snapshot into a standard StepResult.
    NormalizeResult(ctx context.Context, request NormalizeRequest) (*StepResult, error)
}
```

### Method Details

- **Start**: Takes a `StartRequest` containing `RunID`, `StepID`, and the `Prompt`. It returns an `Execution` object which includes a `ProviderSessionID` used for subsequent calls.
- **PollOrCollect**: Used for long-running steps. The engine polls this method until the execution state is terminal (Succeeded, Failed, or Interrupted).
- **Interrupt**: Attempts to gracefully stop a running process. This is essential for user cancellations and timeouts.
- **Resume**: Used when an agent hits an "Approval Gate" or needs user clarification. It sends the user's response back to the provider session.
- **NormalizeResult**: The final step in the execution lifecycle. It validates the output against the engine's requirements (e.g., ensuring structured output is present if requested).

## Capability Matrix

Not all adapters support every feature. The `CapabilityMatrix` allows adapters to declare their supported features, enabling the engine to fail fast if a workflow requires unsupported capabilities.

| Capability | Description |
|------------|-------------|
| `structured_output` | Provider can return machine-readable JSON results. |
| `resume` | Provider supports pausing and resuming sessions. |
| `interrupt` | Provider supports stopping a running task mid-execution. |
| `artifact_refs` | Provider can identify and link to files created or modified. |
| `machine_readable_logs` | Provider emits structured logs during execution. |

### Capability Enforcement

The engine uses the `Require` helper to check for mandatory features:

```go
func (m CapabilityMatrix) Require(capability Capability) error {
    if m.Supports(capability) {
        return nil
    }
    return unsupportedCapabilityError(capability)
}
```

## Provider Implementations

Adapters are implemented in `internal/adapters/[provider]`.

### Codex Adapter
The Codex adapter wraps the `codex` CLI. It maps `Start` to `codex exec`.
- **Strengths**: Native support for local file manipulation and multi-step reasoning.
- **Implementation**: Captures stdout/stderr via a JSON event stream.
- **Capabilities**: Focuses on `machine_readable_logs` and `artifact_refs`.

### Claude Adapter
The Claude adapter interacts with the `claude` CLI using standard IO redirection.
- **Implementation**: Uses `--print --output-format json` to get structured responses.
- **Lifecycle**: Primarily handles one-shot prompt/response cycles.

### OpenCode Adapter
The OpenCode adapter provides deep integration with the OpenCode ecosystem.
- **Implementation**: Uses `opencode run --json` for execution.
- **Capabilities**: Highly compatible with Cogito's parallel execution model.

## Result Normalization Pattern

Cogito uses a two-stage approach to results to maintain provider independence:

### 1. Execution Snapshot
The `Execution` struct represents the raw, "in-flight" state of a provider.

```go
type Execution struct {
    Handle           ExecutionHandle `json:"handle"`
    State            ExecutionState  `json:"state"`
    Summary          string          `json:"summary,omitempty"`
    OutputText       string          `json:"output_text,omitempty"`
    StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
    ArtifactRefs     []ArtifactRef   `json:"artifact_refs,omitempty"`
    Logs             []LogEntry      `json:"logs,omitempty"`
}
```

### 2. Standardized StepResult
`NormalizeResult` transforms the raw `Execution` into a clean `StepResult`. During this phase, the adapter:
- Cleans up ANSI escape codes from logs.
- Validates that `StructuredOutput` matches the expected schema.
- Filters and deduplicates `ArtifactRefs`.
- Maps provider-specific error codes to standard Cogito errors.

## Registry and Discovery

Adapters are registered via an `init()` function, allowing for a plugin-like architecture where simply importing the package enables the provider.

```go
func init() {
    adapters.MustRegister(adapters.Registration{
        Name:         "codex",
        Capabilities: Capabilities(),
        New: func() adapters.Adapter {
            return New(Config{})
        },
    })
}
```

### Discovery Logic
The runtime looks up adapters by the `provider` field in the workflow YAML:

```go
reg, ok := adapters.Lookup(step.Provider)
if !ok {
    return nil, fmt.Errorf("provider %q not found", step.Provider)
}
adapter := reg.New()
```

## Execution Lifecycle Example

1. **Initialization**: The engine loads the workflow and looks up the required adapter in the registry.
2. **Start**: The engine calls `adapter.Start(ctx, request)`. The adapter spawns a sub-process and returns an `Execution` object with a `running` state.
3. **Execution**: The sub-process runs. The engine periodically calls `adapter.PollOrCollect(ctx, handle)`.
4. **Completion**: Once the sub-process exits, `PollOrCollect` returns an `Execution` with a `succeeded` or `failed` state.
5. **Normalization**: The engine calls `adapter.NormalizeResult(ctx, normalizeRequest)`. The adapter parses the final output and returns a `StepResult`.
6. **Next Step**: The engine uses the `StepResult` to decide the next action in the workflow state machine.

## Error Handling

Adapters use a structured error system to provide actionable feedback to the user.

- **ErrorCodeRequest**: The prompt or parameters were malformed.
- **ErrorCodeExecution**: The underlying CLI or API failed (e.g., binary not found, timeout).
- **ErrorCodeResult**: The provider returned output that couldn't be parsed.
- **ErrorCodeUnsupported**: The workflow requested a capability the adapter doesn't have.

## Implementing a New Adapter

To add a new provider to Cogito:
1. Create a new package in `internal/adapters/`.
2. Implement the `Adapter` interface.
3. Define the `CapabilityMatrix` reflecting the provider's features.
4. Use `shared.MustRegister` in an `init()` function.
5. Add unit tests using the `contract_suite.go` to ensure SPI compliance.

The `contract_suite` provides a set of reusable tests that verify an adapter correctly handles state transitions, error cases, and result normalization.
