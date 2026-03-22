/*
Package adapters defines Cogito's provider adapter SPI.

This package is the boundary between runtime orchestration and concrete AI tool
integrations such as Codex, Claude, and OpenCode. Runtime code depends only on
the interfaces and value types declared here so provider-specific process logic
can evolve without changing workflow scheduling semantics.

# Architecture

The SPI is centered on Adapter plus a small registry used by higher-level wiring.

	  runtime.Engine
	       |
	       v
	    Adapter
	       |
	  +----+----+-------------------+
	  |         |                   |
	  ↓         ↓                   ↓
	Start   Poll/Collect   Interrupt/Resume/Normalize

# Lifecycle contract

Provider execution is modeled as a staged lifecycle rather than a single blocking
call so runtime can persist intermediate states and resume interrupted work.

 1. Start receives a StartRequest and returns an initial Execution.
 2. PollOrCollect advances or observes the remote/local provider session.
 3. Interrupt or Resume are used only when capabilities and runtime state allow.
 4. NormalizeResult converts a finished Execution into a StepResult suitable for
    workflow state transitions and artifact capture.

# Capability-driven behavior

CapabilityMatrix makes optional adapter features explicit. Runtime and tests can
ask whether structured output, resume, interrupt, artifact references, or machine-
readable logs are supported before depending on them. That keeps unsupported flows
as validation errors instead of provider-specific surprises.

# Execution model

Execution describes provider-facing state while StepResult is the normalized form
consumed by runtime after terminal or approval-relevant transitions. Keeping those
shapes separate lets adapters preserve provider details during polling and then
emit a narrower, workflow-safe result at the normalization boundary.

# Registry role

Register, Lookup, and RegisteredNames provide a process-local catalog of adapter
implementations. The registry exists to keep CLI wiring simple: adapters self-
register during package initialization, while runtime receives only a lookup
function and never imports provider-specific packages directly.

# Testing support

FakeAdapter and RunContractSuite codify the SPI behavior expected of every real
provider implementation. Contract tests protect behavioral parity across adapters
by checking capability reporting, handle echoing, lifecycle transitions, and
normalization output through the same public interface.

# Concurrency model

The registry is protected by an RWMutex for concurrent lookup and registration.
Individual adapter implementations are responsible for any additional internal
synchronization needed to manage provider sessions safely.
*/
package adapters
