/*
Package store provides the local file-backed persistence layer for Cogito runs.

The store package is intentionally simple: each run is represented by a stable
directory layout, an append-only event log, a checkpoint file, an artifact index,
and lock metadata. Runtime and CLI layers depend on this package to persist and
recover execution state without introducing a daemon or external database.

# Architecture

Every run is mapped onto a Layout rooted under DefaultRunsRoot.

	ref/tmp/runs/<run-id>/
	    workflow.json      resolved workflow definition
	    events.jsonl       append-only event history
	    checkpoint.json    latest durable run snapshot
	    artifacts.json     artifact index
	    locks/             per-run lock metadata

LayoutForRun centralizes these paths so higher layers never assemble filenames by
hand. Keeping layout construction in one place avoids drift between execution,
resume, replay, and status inspection flows.

# Append-only event history

Events are persisted as JSON Lines and assigned monotonically increasing sequence
numbers under a store-local mutex. The append-only model is important because the
runtime reconstructs history and validates replay behavior from the exact event
stream that was recorded during execution.

# Checkpoint and artifact persistence

Checkpoint and artifact files are written with a temp-file-plus-rename strategy.
That pattern minimizes the chance of exposing partially written JSON after a crash
or forced shutdown. Directory syncs are used after rename so metadata changes are
durable before the write is considered complete.

# Recovery model

Checkpoint loading can recover from an interrupted write by checking the temp file
path and promoting it when it contains a valid checkpoint. This keeps resume logic
idempotent after mid-write interruption without requiring the runtime layer to know
anything about storage internals.

# Data model

The package exposes four main persisted shapes:

  - Layout: canonical file locations for a run.
  - Event: one durable transition in the event log.
  - Checkpoint: latest coarse-grained run snapshot used for resume.
  - ArtifactRecord: metadata for files emitted by execution steps.

# Permissions and locality

Store-created files use restricted modes so run data stays local to the current
user by default. The package assumes a single-user, local-first environment and
optimizes for determinism and recoverability rather than remote coordination.

# Concurrency model

Store serializes event appends with a mutex so sequence numbers remain stable.
Other operations are ordinary file-system interactions and rely on the caller to
coordinate higher-level workflow execution.
*/
package store
