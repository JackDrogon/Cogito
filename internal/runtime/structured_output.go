package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JackDrogon/Cogito/internal/adapters"
	"github.com/JackDrogon/Cogito/internal/adapters/prompt"
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
		return prompt.AgentResult{}, wrapError(ErrorCodeState, fmt.Sprintf("decode structured output from step %q", stepID), err)
	}

	return result, nil
}

// syncStepHandle builds the ExecutionHandle for a synchronous local-check
// driver. Such drivers have no provider session, so a synthetic session id is
// minted for handle completeness.
func syncStepHandle(engine *Engine, step workflow.CompiledStep, attemptID string) adapters.ExecutionHandle {
	return adapters.ExecutionHandle{
		RunID:             engine.runID,
		StepID:            step.ID,
		AttemptID:         attemptID,
		ProviderSessionID: engine.ids.NewSyntheticSessionID(step.ID),
	}
}

// terminalExecution constructs an already-settled Execution. Synchronous
// drivers return one directly from Start because their work completes inline;
// the engine's poll loop is skipped for Normalizable states.
func terminalExecution(handle adapters.ExecutionHandle, state adapters.ExecutionState, summary string) *adapters.Execution {
	return &adapters.Execution{Handle: handle, State: state, Summary: summary}
}

// syncTerminalDriver supplies the non-Start lifecycle methods shared by the
// synchronous verify and commit_check drivers. They complete entirely inside
// Start and never poll or resume, so those paths report misuse while Interrupt
// and NormalizeResult mirror the approval driver's terminal handling.
type syncTerminalDriver struct {
	kind string
}

func (d syncTerminalDriver) Resume(_ context.Context, request stepResumeRequest) (*adapters.Execution, error) {
	return nil, newError(ErrorCodeExecution, fmt.Sprintf("%s step %q does not support resume", d.kind, request.Step.ID))
}

func (d syncTerminalDriver) PollOrCollect(_ context.Context, _ adapters.ExecutionHandle) (*adapters.Execution, error) {
	return nil, newError(ErrorCodeExecution, fmt.Sprintf("%s step does not support polling", d.kind))
}

func (d syncTerminalDriver) Interrupt(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return terminalExecution(handle, adapters.ExecutionStateInterrupted, fmt.Sprintf("%s interrupted", d.kind)), nil
}

func (d syncTerminalDriver) NormalizeResult(_ context.Context, execution *adapters.Execution) (*adapters.StepResult, error) {
	if execution == nil {
		return nil, newError(ErrorCodeExecution, fmt.Sprintf("%s execution is required", d.kind))
	}

	return &adapters.StepResult{Handle: execution.Handle, Status: execution.State, Summary: execution.Summary}, nil
}
