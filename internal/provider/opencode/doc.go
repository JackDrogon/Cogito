// Package opencode adapts the OpenCode CLI to the Cogito provider SPI.
//
// Architecture:
//
//   - Start launches `opencode run --print-logs --output-format json` through
//     the shared async runner (internal/provider). Unlike codex and
//     claude, the prompt travels as the final argv value rather than stdin.
//   - PollOrCollect awaits the runner session exactly once (sync.Once) and
//     folds the final JSON payload into a terminal Execution, probing several
//     well-known fields for the session id and output text since the
//     opencode response schema is loosely structured. The real session id is
//     aliased onto the session map.
//   - Resume re-invokes the CLI with `--session <id>`, threading the recovery
//     prompt selected by the runtime when one applies.
//   - NormalizeResult and the structured-output extraction (AGENT_RESULT_JSON)
//     are shared across providers via internal/provider.
//
// The provider self-registers in init; registration can only fail on
// deterministic composition bugs, which panic at startup by design.
package opencode
