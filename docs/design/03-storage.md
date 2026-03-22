# Storage Model

This document describes the storage architecture for Cogito. It focuses on how workflow runs are persisted, how state is recovered, and how artifacts are indexed with security in mind.

## Overview

Cogito uses a hybrid storage approach combining an **append-only event log** with **periodic checkpoints**. 

Key principles:
- **Event Sourcing**: The event log is the canonical source of truth for all run activities.
- **Atomic Writes**: State changes use a write-temp-rename-sync pattern to prevent corruption.
- **Security by Default**: Sensitive data is redacted before being persisted to summaries or checkpoints.
- **Integrity**: Artifacts are indexed with SHA-256 digests to ensure content consistency.

## Directory Layout

Each workflow run is isolated within its own directory. The default root for these directories is `ref/tmp/runs`.

```text
ref/tmp/runs/<run-id>/
├── workflow.json       # Copy of the workflow definition being executed
├── events.jsonl        # Append-only log of all run events (JSON Lines)
├── checkpoint.json     # Current snapshot of the run state
├── artifacts.json      # Index of all artifacts produced by the run
├── locks/              # Directory for advisory locks
└── artifacts/          # (Optional) Subdirectory for raw artifact files
```

## Event Log

The event log (`events.jsonl`) is the primary record of what happened during a run. It uses the JSON Lines format to allow efficient appending and streaming reads.

### Format and Properties

- **Monotonic Sequences**: Each event is assigned a unique, incrementing sequence number.
- **Immutability**: Once an event is appended and synced to disk, it is never modified.
- **Structure**:
  ```json
  {"sequence": 1, "type": "RunCreated", "run_id": "run-123", "message": "run directory created"}
  {"sequence": 2, "type": "StepStarted", "run_id": "run-123", "step_id": "prepare"}
  ```

### Appending Events

When an event is appended, the store:
1. Increments the internal sequence counter.
2. Marshals the event to JSON.
3. Appends the encoded line to the file.
4. Calls `fsync` to ensure the data is committed to physical storage.

## Checkpoint

Checkpoints (`checkpoint.json`) provide a snapshot of the current state of a run. They are used to quickly resume or inspect a run without replaying the entire event log.

### Structure

A checkpoint includes the run state, the last processed event sequence, and per-step status:

```json
{
  "run_id": "run-123",
  "state": "running",
  "last_sequence": 42,
  "updated_at": "2026-03-22T10:00:00Z",
  "steps": {
    "prepare": {
      "state": "succeeded",
      "summary": "prepared 5 files"
    }
  }
}
```

### Atomic Write Pattern

To prevent data corruption during crashes, checkpoints are written using an atomic pattern:
1. Write JSON data to `checkpoint.json.tmp`.
2. Call `fsync` on the temporary file.
3. Rename the temporary file to `checkpoint.json`.
4. Call `fsync` on the parent directory to ensure the metadata update is durable.

### Recovery Strategy

On startup, Cogito attempts to load the primary checkpoint. If it's missing or corrupt, it tries to recover from the temporary file. If both fail, it may fall back to replaying events from the log.

## Artifacts

Artifacts are files produced during a run that need to be tracked and indexed.

### Indexing and Integrity

The `artifacts.json` file maintains a list of all artifacts associated with the run:

```json
[
  {
    "path": "logs/build.log",
    "kind": "log",
    "step_id": "build",
    "digest": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "summary": "Build output logs"
  }
]
```

- **SHA-256 Digests**: Every artifact is hashed upon indexing to ensure its content hasn't changed.
- **Path Security**: Artifact paths are relative and validated to prevent traversal outside the run directory.

### Redaction

Summaries and other metadata fields are scanned for sensitive patterns (API keys, passwords, tokens) and redacted before being saved to the index or checkpoints.

```go
// Example patterns used for redaction
var secretSummaryPatterns = []*regexp.Regexp{
    regexp.MustCompile(`(?i)(api[_-]?key\s*[:=]\s*)([^\s,;]+)`),
    regexp.MustCompile(`(?i)(token\s*[:=]\s*)([^\s,;]+)`),
}
```

## Startup Behavior

When opening an existing run, the store:
1. Validates the directory structure.
2. Reads the event log to determine the latest sequence number.
3. Loads and verifies the checkpoint.
4. Compares `checkpoint.last_sequence` with the log. If they diverge, it indicates a partial write or crash that needs handling.
