# RUNTIME GUIDE

## OVERVIEW
`internal/runtime/` executes compiled workflows through an event-sourced state machine; this is the highest-risk package for behavioral regressions.

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Understand state transitions | `state_machine.go`, `state_catalog.go` | run and step transitions are explicit, not inferred |
| Add or change runtime event handling | `state_machine_handlers.go`, `state_machine.go` | handler wiring and transition folding must stay aligned |
| Change scheduler/execution loop | `engine.go`, `step_executor.go` | preserve topological determinism and one-step-at-a-time execution |
| Change event durability/replay | `event_sourcing.go`, `snapshot.go` | persisted events remain the source of truth |
| Change approval behavior | `approval.go`, `approval_strategy.go`, `approval_decision_strategy.go` | waiting states affect both run and step |
| Change repo safety/locking | `lock.go` | lock behavior is separate from state-machine folding |

## CONVENTIONS
- Persist meaningful transitions before folding them into snapshot state.
- Keep `RunState` and `StepState` rules explicit and synchronized; invalid transitions should fail fast.
- Preserve deterministic ordering from compiled workflow topology; replay and resume depend on stable event order.
- Inject collaborators through runtime dependencies instead of importing CLI wiring or provider implementations directly.
- Treat approval, replay, and locking as first-class runtime behaviors, not side effects bolted onto the engine.

## ANTI-PATTERNS
- Do not mutate run or step state ad hoc without a durable event.
- Do not bypass transition tables for "obvious" state moves.
- Do not introduce concurrent multi-step orchestration without revisiting the single-run execution model documented in package docs and design docs.
- Do not bury approval or repository-safety behavior inside adapters or app commands.
- Do not change on-disk event/checkpoint expectations here without checking `internal/store/` and `docs/design/03-storage.md` + `docs/design/04-runtime.md`.

## NOTES
- `state_machine_test.go` carries the densest behavioral coverage in the repo; extend it when touching transitions or replay semantics.
- `runtime` consumes compiled workflows only; raw YAML/schema concerns stay in `internal/workflow/`.
