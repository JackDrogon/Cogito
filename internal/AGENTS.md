# INTERNAL MAP

## OVERVIEW
`internal/` holds the maintained implementation surface; each package maps cleanly to one architectural layer or boundary.

## STRUCTURE
```text
internal/
|-- app/       # CLI routing, shared flags, presenters, dependency wiring
|-- workflow/  # YAML parse/validate/compile into immutable DAG
|-- runtime/   # event-sourced execution engine and approval/lock logic
|-- adapters/  # provider SPI, registry, fake adapter, provider impls
|-- store/     # file-backed events/checkpoints/artifacts/workflow persistence
|-- executor/  # local command supervision and normalization
`-- version/   # build-time version surface
```

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Add command or shared CLI flag | `internal/app/` | see `internal/app/AGENTS.md` for registry, flags, and wiring boundaries |
| Extend workflow DSL | `internal/workflow/` | keep parse, semantic validation, and compile stages distinct |
| Change scheduling, replay, approval, or lock behavior | `internal/runtime/` | see `internal/runtime/AGENTS.md` before editing |
| Add a new provider | `internal/adapters/` | see `internal/adapters/AGENTS.md`; use contract suite |
| Change on-disk layout or persistence semantics | `internal/store/` | runtime/store/docs must stay aligned |
| Change local command execution | `internal/executor/` | command runner boundary used by runtime/app |

## CONVENTIONS
- Keep package boundaries sharp; most directories are single-purpose and already mirrored in `docs/design/`.
- Prefer package-local `doc.go` files as the authoritative architecture note for one package.
- `app` wires dependencies but should not absorb provider-specific or storage-specific behavior.
- `workflow` produces immutable compiled state; `runtime` consumes it but should not parse raw YAML.
- Tests live beside packages and use Go's standard `testing` package; adapters are the only area with a reusable contract suite.

## ANTI-PATTERNS
- Do not move shared behavior into `internal/app/` just because the CLI reaches it first.
- Do not let `runtime/` know provider details beyond the adapter SPI.
- Do not let `workflow/` depend on execution concerns such as polling, locks, or approvals.
- Do not treat `version/` or `executor/` as extension points unless the change really belongs there.

## NOTES
- Highest-complexity domains are `runtime/`, `app/`, and `adapters/`; read package docs before changing them.
- `store/`, `workflow/`, and `executor/` are smaller; usually the package `doc.go` plus `docs/design/` is enough unless you are changing a cross-layer contract.
