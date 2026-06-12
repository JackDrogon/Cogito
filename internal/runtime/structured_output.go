package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JackDrogon/Cogito/internal/prompt"
	"github.com/JackDrogon/Cogito/internal/provider"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

// readAgentResult decodes the normalized AgentResult JSON an upstream agent step
// persisted via Engine.StepStructuredOutput. The bytes are already a marshaled
// prompt.AgentResult (L1 contract), so this unmarshals directly and never
// re-scans logs for the AGENT_RESULT_JSON marker. A missing/empty structured
// output or a decode failure surfaces as ErrorCodeState: the upstream step may
// exist but has not produced consumable data, which verify/commit_check drivers
// fold into a Failed step rather than a fatal error.
func readAgentResult(engine *Engine, stepID string) (prompt.AgentResult, error) {
	raw, err := engine.StepStructuredOutput(stepID)
	if err != nil {
		return prompt.AgentResult{}, err
	}

	if len(raw) == 0 {
		return prompt.AgentResult{}, newError(ErrorCodeState, fmt.Sprintf("step %q has no structured output", stepID))
	}

	var result prompt.AgentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		message := fmt.Sprintf("decode structured output from step %q", stepID)

		return prompt.AgentResult{}, wrapError(ErrorCodeState, message, err)
	}

	return result, nil
}

// syncStepHandle builds the ExecutionHandle for a synchronous local-check
// driver. Such drivers have no provider session, so a synthetic session id is
// minted for handle completeness.
func syncStepHandle(engine *Engine, step workflow.CompiledStep, attemptID string) provider.ExecutionHandle {
	return provider.ExecutionHandle{
		RunID:             engine.runID,
		StepID:            step.ID,
		AttemptID:         attemptID,
		ProviderSessionID: engine.idGen.NewSyntheticSessionID(step.ID),
	}
}

// terminalExecution constructs an already-settled Execution. Synchronous
// drivers return one directly from Start because their work completes inline;
// the engine's poll loop is skipped for Normalizable states.
func terminalExecution(
	handle provider.ExecutionHandle,
	state provider.ExecutionState,
	summary string,
) *provider.Execution {
	return &provider.Execution{Handle: handle, State: state, Summary: summary}
}

// syncTerminalDriver supplies the non-Start lifecycle methods shared by the
// synchronous verify and commit_check drivers. They complete entirely inside
// Start and never poll or resume, so those paths report misuse while Interrupt
// and NormalizeResult mirror the approval driver's terminal handling.
type syncTerminalDriver struct {
	kind string
}

func (d syncTerminalDriver) Resume(_ context.Context, request stepResumeRequest) (*provider.Execution, error) {
	return nil, newError(ErrorCodeExecution, fmt.Sprintf("%s step %q does not support resume", d.kind, request.Step.ID))
}

func (d syncTerminalDriver) PollOrCollect(_ context.Context, _ provider.ExecutionHandle) (*provider.Execution, error) {
	return nil, newError(ErrorCodeExecution, d.kind+" step does not support polling")
}

func (d syncTerminalDriver) Interrupt(_ context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	return terminalExecution(handle, provider.ExecutionStateInterrupted, d.kind+" interrupted"), nil
}

func (d syncTerminalDriver) NormalizeResult(
	_ context.Context,
	execution *provider.Execution,
) (*provider.StepResult, error) {
	if execution == nil {
		return nil, newError(ErrorCodeExecution, d.kind+" execution is required")
	}

	return &provider.StepResult{Handle: execution.Handle, Status: execution.State, Summary: execution.Summary}, nil
}
