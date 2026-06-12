# PROVIDER GUIDE

## OVERVIEW
`internal/provider/` is the SPI boundary for provider integrations; it owns lifecycle contracts, capability reporting, registry wiring, and the shared contract test suite.

## STRUCTURE
```text
internal/provider/
|-- types.go                # Provider interface, Execution/StepResult, capabilities
|-- registry.go             # process-local registration and lookup
|-- contract_suite.go       # provider contract tests shared by every provider
|-- fake.go                 # scriptable fake provider for tests
|-- <provider>_integration_test.go
|-- process.go              # async provider process launcher (StartProcess/Interrupt/Await + session map)
|-- support.go              # provider-facing shared helpers (exec runner, session map, validation/clone wrappers)
|-- codex/
|-- claude/
`-- opencode/
```

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Understand SPI lifecycle | `types.go`, `doc.go` | Start -> PollOrCollect -> Interrupt/Resume -> Normalize |
| Add a provider | `<provider>/adapter.go`, `registry.go` | subdir implementation + parent-level registration/contract tests |
| Parse provider event streams | `<provider>/events.go` when needed | split out only when provider logs are complex enough |
| Validate behavior across providers | `contract_suite.go`, `*_integration_test.go` | every provider should pass the same suite |
| Build test doubles | `fake.go` | scriptable fake snapshots for runtime-facing tests |

## CONVENTIONS
- Provider implementations live in subdirectories; parent package stays the SPI/test/registry hub.
- Parent-level integration tests use the pattern `<provider>_integration_test.go` and black-box package `provider_test`.
- Every provider exposes a static `Capabilities()` helper and self-registers in `init()` with name, capabilities, and factory.
- Runner execution is injected through config so tests can swap binaries out for mocks/fakes.
- Keep provider-specific parsing/helpers in the provider subdir, but shared contract/value types stay in the parent package.

## ANTI-PATTERNS
- Do not put provider integration tests inside provider subdirectories.
- Do not add provider-specific imports to runtime or app packages.
- Do not skip `RunContractSuite()` when introducing or changing a provider.
- Do not create duplicate SPI types in provider subdirs when the parent package already owns the contract.
- Avoid copy-pasting shared validation/clone helpers across providers; extract reusable code into `support.go` instead. SPI contract/value types and helpers consumed by provider subpackages live in the parent package.

## NOTES
- Current provider subdirs are `codex/`, `claude/`, and `opencode/`; mirror their layout before inventing a new one.
- Capability flags are explicit opt-in. Unsupported features should remain false rather than silently partial.
