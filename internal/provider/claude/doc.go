// Package claude adapts the Claude Code CLI to the Cogito provider SPI.
//
// Architecture:
//
//   - Start launches `claude --print --permission-mode bypassPermissions
//     --output-format json` through the shared async runner
//     (internal/provider), delivering the prompt on stdin. The provider
//     registers the session under a synthetic provider session id and reports
//     ExecutionStateRunning immediately.
//   - PollOrCollect awaits the runner session exactly once (sync.Once) and
//     folds the final JSON response into a terminal Execution. The real
//     session id parsed from the response is aliased onto the session map so
//     later lookups by either id resolve to the same record.
//   - Resume re-invokes the CLI with `--resume <session-id>`, threading the
//     recovery prompt selected by the runtime when one applies.
//   - NormalizeResult and the structured-output extraction (AGENT_RESULT_JSON)
//     are shared across providers via internal/provider.
//
// The provider self-registers in init; registration can only fail on
// deterministic composition bugs, which panic at startup by design.
package claude
