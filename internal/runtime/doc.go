/*
Package runtime executes compiled workflows through an event-sourced state
machine.

This package is the orchestration layer between static workflow definitions,
provider adapters, command execution, approval policy, and persistent run state.
It does not parse workflows and it does not own on-disk storage formats; instead
it coordinates those collaborators through narrow interfaces.

# Architecture

The runtime package centers on Engine, which consumes a workflow.CompiledWorkflow
and advances it by recording durable events and rebuilding an in-memory Snapshot.

	CompiledWorkflow + dependencies
	            |
	            ↓
	         Engine
	            |
	    +-------+--------+
	    |                |
	    ↓                ↓
	EventStore       Adapter/CommandRunner
	    |
	    ↓
	events + checkpoint
	    |
	    ↓
	  Replay / Resume

# Event-sourced execution model

Runtime behavior is driven by persisted events rather than ad hoc in-memory state
mutation. Each meaningful transition is appended to the EventStore, then folded
into Snapshot state. That ordering keeps execution auditable and gives replay,
resume, and status inspection a single source of truth.

The package exposes Replay helpers so callers can rebuild the same transition
history from stored events. Tests in state_machine_test.go rely on that property
to verify deterministic transitions.

# State model

Two state machines move in lockstep:

  - RunState tracks whole-run lifecycle (pending, running, paused, waiting for
    approval, terminal states).
  - StepState tracks each individual step lifecycle (pending, queued, running,
    waiting for approval, terminal states).

Transition tables are defined explicitly so invalid moves fail fast instead of
silently corrupting execution history.

# Execution flow

At a high level Engine performs the following loop:

 1. Identify steps whose dependencies are satisfied.
 2. Queue and start eligible work through either an Adapter or CommandRunner.
 3. Poll executions until they succeed, fail, or request approval.
 4. Persist every transition as an event.
 5. Save checkpoints so interrupted runs can resume without recomputing state
    from scratch every time.

# Approval and exception handling

ApprovalPolicy allows the caller to inject human-in-the-loop or policy-based
gates without changing the core scheduler. The runtime decides when a step must
pause for approval, but the policy decides whether to wait, approve, deny, or
time out.

# Dependency injection boundary

MachineDependencies bundles the small set of collaborators that runtime needs:
clock, ID generation, event storage, adapter lookup, approval policy, command
runner, and repository/working-directory paths. That boundary keeps the state
machine testable while avoiding direct imports of CLI-specific wiring.

# Locking and repository safety

RepoLockManager protects shared repositories from concurrent mutation by multiple
runs. Lock files are intentionally stored outside the state machine so execution,
recovery, and repository coordination remain separable concerns.

# Concurrency model

The Engine itself is single-run orchestration code and should be driven by one
caller at a time. Persisted events and explicit transition matrices provide the
determinism needed for resume and replay, even when underlying adapters are
asynchronous.
*/
package runtime
