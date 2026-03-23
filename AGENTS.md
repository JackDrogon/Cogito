# PROJECT KNOWLEDGE BASE

**Generated:** 2026-03-23 Asia/Shanghai
**Commit:** 8d4abee
**Branch:** master

## OVERVIEW
Cogito is a Go CLI for deterministic, auditable AI workflow execution. The maintained code lives under `cmd/`, `internal/`, and `docs/design/`; `ref/tmp/` is the default home for runs, downloaded references, and scratch material.

## STRUCTURE
```text
Cogito/
|-- cmd/cogito/        # CLI entrypoint; turns argv + signals into app.Run
|-- internal/          # maintained implementation packages; see internal/AGENTS.md
|-- docs/design/       # code-aligned design notes for shipped behavior
|-- ref/tmp/           # default run state, tests, downloaded upstream code, scratch
|-- justfile           # maintainer command surface
`-- .github/workflows/ # CI mirrors build + test + lint gates
```

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Add or change CLI commands | `cmd/cogito/main.go`, `internal/app/` | `main.go` is thin; `internal/app/` owns routing and wiring |
| Change workflow schema or compilation | `internal/workflow/` | parse -> validate -> compile pipeline |
| Change run execution, replay, approvals, locks | `internal/runtime/` | see local runtime guide before editing state logic |
| Change provider integrations | `internal/adapters/` | see local adapters guide before editing SPI or providers |
| Change persistence layout | `internal/store/`, `docs/design/03-storage.md` | runtime depends on durable event/checkpoint semantics |
| Update design docs | `docs/design/` | keep docs code-aligned, not aspirational |
| Navigate maintained source quickly | `internal/AGENTS.md` | package-level map for maintained implementation only |

## CODE MAP
| Symbol | Type | Location | Refs | Role |
|--------|------|----------|------|------|
| `main` | function | `cmd/cogito/main.go` | entry | process entry; delegates to `run()` |
| `Run` | function | `internal/app/app.go` | central | top-level CLI router and shared flag handling |
| `CompiledWorkflow` | struct | `internal/workflow/model.go` | central | immutable runtime-ready DAG |
| `applyEvent` | function | `internal/runtime/state_machine.go` | central | folds persisted events into snapshot state |
| `Register` / `Lookup` | function | `internal/adapters/registry.go` | central | process-local adapter registry for CLI wiring |

## CONVENTIONS
- Temporary files, downloaded code, and test scratch space belong under `$repo/ref/tmp`.
- Treat `ref/`, `run-output/`, and `locks/` as runtime/reference areas, not product source. They may contain samples, logs, or generated state that should not drive architectural conclusions.
- `just` is the canonical task runner: `just build`, `just test`, `just lint`, `just pre-commit`.
- Formatting uses `gofumpt`; linting is intentionally strict via `.golangci.yml`.
- Docs under `docs/design/` describe current shipped behavior first; update them when implementation changes user-visible or architectural contracts.

## ANTI-PATTERNS (THIS PROJECT)
- Do not create ad hoc temp paths outside `ref/tmp/`.
- Do not edit files under `ref/tmp/` as if they were first-party source; that tree includes downloaded upstream projects and run artifacts.
- Do not bypass event durability when changing runtime behavior; meaningful state transitions must remain replayable from `events.jsonl`.
- Do not add provider-specific logic directly to runtime or app wiring; keep provider behavior behind `internal/adapters`.
- Do not document aspirational behavior in `docs/design/`; keep the docs code-aligned.

## UNIQUE STYLES
- Architecture is intentionally layered: CLI/app -> workflow/runtime -> store/adapters/executor.
- Workflow compilation preserves deterministic ordering so runtime replay stays auditable.
- Provider adapters are capability-driven rather than feature-assumed.
- Package docs and design docs carry most subsystem-specific architecture notes; use them before inferring behavior from call sites alone.

## COMMANDS
```bash
just build
just test
just lint
just pre-commit
just run -- --help
go test ./...
```

## NOTES
- CI runs `go build ./cmd/cogito`, `go test ./...`, and `golangci-lint`; local changes should satisfy the same gates.
- `docs/design/README.md` is the index for numbered design notes; preserve numbering and update the map when adding docs.
- If you need domain-specific guidance inside source packages, check `internal/AGENTS.md` first, then descend into child AGENTS files.
