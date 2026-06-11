// Package codex adapts the Codex CLI to the Cogito adapter SPI.
//
// Architecture:
//
//   - Start launches `codex exec --json --sandbox <mode>
//     --output-last-message <tmp>` through the shared async runner
//     (internal/adapters/runner), delivering the prompt on stdin. The
//     last-message file lives in a per-invocation temp dir removed once the
//     terminal Execution is built.
//   - PollOrCollect awaits the runner session exactly once (sync.Once),
//     parses the JSONL event stream for the thread id and failure events,
//     reads the last-message file, and folds everything into a terminal
//     Execution. The real thread id is aliased onto the session map.
//   - live_output.go renders selected events as human-readable progress lines
//     when the CLI runs with -v (AgentLoop-style live output).
//   - NormalizeResult and the structured-output extraction (AGENT_RESULT_JSON)
//     are shared across providers via internal/adapters/adapterutil.
//
// The adapter self-registers in init; registration can only fail on
// deterministic composition bugs, which panic at startup by design.
package codex
