# Error Model

Cogito does not use one global error enum. Instead, each subsystem exposes a small
typed error with a subsystem-local `ErrorCode`, and the application / CLI layer
decides how much of that detail to preserve or translate.

This document describes the implemented error taxonomy and how failures propagate
from lower layers to the CLI.

## Design Goals

- preserve subsystem context close to the source of failure
- keep error strings readable in CLI output
- allow `errors.Is` / `errors.As` to inspect wrapped causes
- avoid coupling all packages to one giant shared error package

## Error Shapes by Package

Five core packages define typed errors.

### `internal/workflow`

```go
type Error struct {
    Code    ErrorCode
    Message string
    Err     error
}
```

Error codes:

- `parse`
- `schema`
- `semantic`
- `version`

Typical use:

- malformed YAML
- unknown fields rejected by `KnownFields(true)`
- unsupported `apiVersion` / `kind`
- duplicate step IDs or invalid dependencies

Rendered form:

```text
workflow schema error: metadata.name is required
workflow semantic error: duplicate step id "build"
```

### `internal/runtime`

```go
type Error struct {
    Code    ErrorCode
    Message string
    Err     error
}
```

Error codes:

- `path`
- `git`
- `lock`
- `dirty_worktree`
- `permission`
- `state`
- `execution`
- `replay`
- `config`

Typical use:

- invalid run IDs or path setup
- git failures during repo-root detection or dirty-worktree checks
- repo lock acquisition or stale-lock reclamation issues
- invalid run/step transitions
- approval resolution failures
- replay validation failures
- missing collaborators such as store, driver factory, or compiled workflow

Rendered form:

```text
runtime lock error: repo lock already held for /repo by run run-123: repo lock already held
runtime state error: run is waiting approval
```

### `internal/store`

```go
type Error struct {
    Code    ErrorCode
    Message string
    Err     error
}
```

Error codes:

- `path`
- `permission`
- `workflow`
- `event_log`
- `checkpoint`
- `artifacts`

Typical use:

- invalid or missing run layout paths
- failure creating or chmod'ing files/directories
- event log append / read / decode failures
- checkpoint read, write, or recovery failures
- artifact validation, hashing, or path-escape failures

Rendered form:

```text
store checkpoint error: load checkpoint: checkpoint not found
store artifacts error: validate artifact path: path escapes run directory
```

### `internal/adapters`

```go
type Error struct {
    Code       ErrorCode
    Message    string
    Capability Capability
    Err        error
}
```

Error codes:

- `capability`
- `request`
- `execution`
- `result`

Typical use:

- unsupported optional capability such as `resume` or `interrupt`
- missing run/step/attempt IDs in start or handle validation
- provider binary lookup or subprocess execution failures
- provider JSON parsing / normalization failures

The adapter error is slightly richer than the others because capability failures
also record the missing capability.

Rendered form:

```text
adapter capability error: capability unsupported (resume)
adapter execution error: codex binary not found: executable file not found in $PATH
```

### `internal/executor`

```go
type Error struct {
    Code    ErrorCode
    Message string
    Err     error
}
```

Error codes:

- `request`
- `execution`
- `result`

Typical use:

- invalid command specs
- local subprocess failures
- normalization / output processing failures

## Common Structural Properties

Across these packages, typed errors share the same broad pattern:

- a stable subsystem-local code
- a short human-readable message
- optional wrapped cause in `Err`
- `Unwrap()` support so callers can inspect the underlying error

This means the project gets most of the value of structured errors without forcing
every package into the same code namespace.

## How Errors Propagate

## 1. Workflow loading

`internal/app/application_service.go` calls `workflow.LoadFile()`.

- workflow parse/validation failures are returned directly to the CLI layer
- the CLI prints the resulting message to stderr

Example path:

```text
workflow.LoadFile -> *workflow.Error -> app.Run -> stderr + exit code 1
```

## 2. Run startup

Starting a run crosses several subsystems:

1. parse approval mode
2. load workflow
3. resolve state dir
4. acquire repo lock
5. open store
6. persist resolved workflow
7. build engine
8. execute until settled

Any of these can return:

- plain Go errors from app helpers
- `*workflow.Error`
- `*runtime.Error`
- `*store.Error`

The app layer usually forwards them unchanged.

## 3. Runtime execution failures

Runtime wraps lower-level execution failures into runtime state transitions.

For example, if a step driver fails during polling or result normalization:

1. runtime persists `StepFailed`
2. runtime persists `RunFailed`
3. runtime returns a wrapped `runtime execution error`

This is important: the durable run history is updated before the process returns an
error to the caller.

## 4. Failed runs surfaced by application service

After `RunWorkflow` or `ResumeRun` completes, the application layer checks whether
the final snapshot state is `failed`.

If so, it does not simply return the raw runtime error. In that settled-failed
path, it reads the event log and extracts the latest non-empty event message:

```text
run failed: <latest event message>
```

This behavior is implemented in `internal/app/session_manager.go` via
`latestRunFailure()`.

Effectively, the user sees the final durable failure summary, not just the first
in-process stack of wrapped errors.

## 5. Missing run state translation

When opening an existing run, `internal/app/session_manager.go` uses `errors.As`
and `errors.Is` to detect missing-path conditions, including `*store.Error` values
whose wrapped cause is `os.ErrNotExist`.

Those are translated into a friendlier message:

```text
run state not found: <state-dir>
```

This is one of the few places where the app layer intentionally hides lower-level
store details behind a user-facing message.

## 6. CLI boundary

The top-level boundary is `cmd/cogito/main.go` plus `internal/app/app.go`.

- commands return `error`
- `main` prints the error to stderr
- the process exits with status `1`

There is currently no structured JSON error output mode. Human-readable error
strings are the stable CLI contract today.

## Translation Rules in Practice

The implemented translation strategy can be summarized like this:

- **workflow / runtime / store / adapter / executor**: keep typed context near the source
- **app layer**: only translate when it materially improves UX
- **CLI layer**: print the final error string verbatim

Examples of translation at the app layer:

- convert missing state-dir errors into `run state not found: ...`
- convert a settled failed run into `run failed: <latest event message>`

Examples of no translation:

- invalid workflow schema
- invalid approval mode
- replay validation failure
- repo lock acquisition failure

## Error Taxonomy by Concern

If you are debugging a problem, this mapping is usually enough to find the right
package quickly.

- **YAML or DAG invalid** -> `workflow`
- **repo root, git, lock, state machine, replay** -> `runtime`
- **missing run files, checkpoint corruption, artifact path issues** -> `store`
- **provider binary missing, provider output invalid, capability unsupported** -> `adapters`
- **local command parsing or subprocess supervision** -> `executor`
- **state-dir parsing, user-facing translation, command routing** -> `app`

## Current Constraints

- error codes are package-local, not globally namespaced
- CLI always returns exit code `1` for failures, regardless of subsystem
- no machine-readable error envelope is exposed yet
- app-layer translations are selective rather than fully standardized

These constraints are acceptable for the current CLI-first workflow, but they also
show where a future API-facing error envelope could be introduced if needed.
