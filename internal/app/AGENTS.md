# APP GUIDE

## OVERVIEW
`internal/app/` is the CLI-facing orchestration layer; it owns command registration, shared flag parsing, presenter output, runtime wiring, and run-service entry points.

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Add or rename a CLI command | `app.go`, `app_commands.go`, `command_registry.go` | root command tables and subcommand groups live here |
| Change shared flags or defaults | `app.go` | `parseSharedFlags` and `defaultStateDir` define the common CLI contract |
| Change app-level workflow/run actions | `application_service.go`, `run_service.go` | service methods call workflow/runtime/store in a fixed order |
| Change runtime dependency wiring | `wiring.go`, `adapter_resolver.go`, `command_runner.go` | app builds runtime dependencies without leaking provider details |
| Change CLI presentation | `text_presenter.go` | keep human-readable output here, not in runtime |
| Change run-state path handling | `run_requests.go`, `app_runtime.go` | app owns request normalization before runtime/store calls |

## CONVENTIONS
- Keep `cmd/cogito/main.go` thin; CLI behavior belongs in this package.
- Register commands through the command registry tables instead of ad hoc argument branching.
- Parse shared flags once, then pass normalized values into service methods and runtime wiring.
- Let `application_service.go` coordinate workflow loading, store opening, lock acquisition, and runtime execution in that order.
- Keep presentation concerns in presenter types and execution concerns in services/wiring.

## ANTI-PATTERNS
- Do not import provider subpackages here except for wiring-time blank imports that populate the adapter registry.
- Do not move runtime transition logic into CLI commands or presenters.
- Do not parse raw workflow YAML directly in command handlers when `workflow.LoadFile` or resolved-file helpers already own that contract.
- Do not bypass repo locking when starting runs from CLI flows.
- Do not let command handlers format durable state directly when a presenter/service boundary already exists.

## NOTES
- `application_service.go` and `run_service.go` are the best entry points for end-to-end command behavior.
- `wiring.go` resolves repo path, working directory, command runner, adapter lookup, and repo lock roots; read it before changing execution context behavior.
