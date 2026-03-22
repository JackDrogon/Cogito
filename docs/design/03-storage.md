# Storage Model

Cogito persists every run into a local directory tree. By default that layout
lives under `ref/tmp/`, while the app layer can relocate a run via `--state-dir`.
The store package is intentionally file-backed and single-user oriented: it
optimizes for auditable history, crash recovery, and easy inspection without
requiring any daemon or database.

## Run Layout

Each run is represented by `store.Layout` and defaults to:

```text
ref/tmp/runs/<run-id>/
├── workflow.json          # resolved compiled workflow
├── events.jsonl           # append-only event log
├── checkpoint.json        # last durable snapshot
├── artifacts.json         # artifact index
├── locks/
│   └── repo.lock.json     # per-run mirror of repo lock metadata
└── provider-logs/
    └── <step-id>/
        ├── <attempt>-stdout.log
        └── <attempt>-stderr.log
```

At the repository level, Cogito also writes:

```text
ref/tmp/locks/<repo>.lock.json
```

This file prevents concurrent runs from mutating the same repository.

## Canonical Persisted Shapes

The store package exposes four persisted data structures:

- `Event` - one durable transition in the event log
- `Checkpoint` - coarse-grained snapshot used for resume
- `ArtifactRecord` - metadata for files emitted during execution
- `Layout` - canonical paths for one run

## Event Log

`events.jsonl` is the source of truth for runtime history.

### Properties

- JSON Lines format
- append-only
- monotonically increasing `sequence`
- one store-local mutex serializes appends
- every event is `fsync`'d before the append is considered durable

### Event shape

```json
{
  "sequence": 5,
  "type": "StepStarted",
  "run_id": "run-123",
  "step_id": "review",
  "attempt_id": "attempt-review-01",
  "message": "step started",
  "data": {
    "occurred_at": "2026-03-23T03:30:00Z",
    "from_state": "queued",
    "to_state": "running",
    "provider_session_id": "command-review-attempt-review-01",
    "summary": "step started"
  }
}
```

### Event categories used by runtime

- run lifecycle: `RunCreated`, `RunStarted`, `RunPaused`, `RunWaitingApproval`, `RunSucceeded`, `RunFailed`, `RunCanceled`
- step lifecycle: `StepQueued`, `StepStarted`, `StepSucceeded`, `StepFailed`, `StepRetried`
- approval lifecycle: `ApprovalRequested`, `ApprovalGranted`, `ApprovalDenied`, `ApprovalTimedOut`
- replay reporting: `ReplayStarted`, `ReplaySucceeded`, `ReplayFailed`

## Checkpoint

`checkpoint.json` is a denormalized snapshot written after each successful event
append. It exists to speed up resume and status flows while keeping `events.jsonl`
as the canonical history.

### Checkpoint shape

```json
{
  "run_id": "run-123",
  "repo_path": "/workspace/Cogito",
  "working_dir": "/workspace/Cogito",
  "state": "running",
  "last_sequence": 12,
  "updated_at": "2026-03-23T03:30:00Z",
  "steps": {
    "review": {
      "state": "succeeded",
      "attempt_id": "attempt-review-01",
      "provider_session_id": "command-review-attempt-review-01",
      "summary": "review completed"
    }
  }
}
```

### Why `repo_path` and `working_dir` matter

The application layer resolves execution context from either:

1. explicit `--repo`
2. checkpoint data from a previous run
3. the current process working directory

Persisting both values lets resume operations reuse the original execution context.

### Atomic write pattern

Checkpoints are written using a temp-file-plus-rename strategy:

1. marshal JSON
2. write to `checkpoint.json.tmp`
3. `fsync` the temp file
4. rename to `checkpoint.json`
5. `fsync` the parent directory

If the primary checkpoint cannot be loaded, the store attempts to recover from the
temp file and promote it to the primary path.

## Artifact Index

`artifacts.json` tracks files created by execution. In the current implementation,
command steps append stdout/stderr log files under `provider-logs/` and store them
as artifact records.

### Artifact shape

```json
[
  {
    "path": "provider-logs/test/attempt-test-01-stdout.log",
    "kind": "log",
    "step_id": "test",
    "digest": "...sha256...",
    "summary": "command stdout log",
    "created_at": "2026-03-23T03:30:00Z"
  }
]
```

### Sanitization rules

- artifact paths must be relative
- artifact paths must stay inside the run directory
- artifact paths must reference existing files, not directories
- `digest` is recomputed from file contents using SHA-256
- summaries are redacted for secrets before persistence

The redaction patterns cover strings such as API keys, bearer tokens, passwords,
and generic secrets.

## File Modes and Locality

- directories are created with `0700`
- files are created with `0600`

This matches the local-first assumption: run data is private to the current user
unless permissions are changed externally.

## Recovery Model

When opening an existing run:

1. the app layer reconstructs the run ID from the state directory
2. `store.OpenExisting` validates the expected layout
3. the resolved workflow is loaded from `workflow.json`
4. runtime loads checkpoint and events
5. runtime prefers checkpoint when it is at least as recent as the latest event
6. otherwise runtime replays all events and rebuilds the snapshot

This allows resume and replay to share a single durable history model.

## Lock Files

Locking is implemented in `internal/runtime/lock.go`, but it is part of the on-disk
contract because the default repo-level metadata lives under `ref/tmp/locks/`.

Two lock files are written on acquisition:

- repo-global lock in `ref/tmp/locks/`
- run-local mirror in `<run-dir>/locks/`

Lock metadata includes:

- `run_id`
- `repo_root`
- `pid`
- `hostname`
- `acquired_at`
- `updated_at`
- `run_lock_path`

Stale locks are reclaimed when the recorded process is no longer running on the
same host.
