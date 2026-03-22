package adapters

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

type ContractCase struct {
	Name string

	Adapter          Adapter
	StartRequest     StartRequest
	WantCapabilities CapabilityMatrix
	WantStartState   ExecutionState
	WantPollStates   []ExecutionState
	WantResult       StepResult

	InterruptRequest   *StartRequest
	WantInterruptState ExecutionState

	ResumeRequest        *StartRequest
	WantResumeState      ExecutionState
	WantResumePollStates []ExecutionState
	WantResumeResult     *StepResult
	ResumeNormalize      NormalizeRequest

	NormalizeRequest NormalizeRequest
}

func RunContractSuite(t *testing.T, cases []ContractCase) {
	t.Helper()

	for i := range cases {
		tc := &cases[i]
		t.Run(tc.Name, func(t *testing.T) {
			ctx := t.Context()

			capabilities := runCapabilityTest(t, tc)
			runStartAndPollTest(t, ctx, tc)

			if tc.InterruptRequest != nil {
				runInterruptTest(t, ctx, tc)
			}

			if tc.ResumeRequest != nil {
				runResumeTest(t, ctx, tc, capabilities)
			}
		})
	}
}

func runCapabilityTest(t *testing.T, tc *ContractCase) CapabilityMatrix {
	t.Helper()

	capabilities := tc.Adapter.DescribeCapabilities()
	if !reflect.DeepEqual(capabilities, tc.WantCapabilities) {
		t.Fatalf("DescribeCapabilities() = %+v, want %+v", capabilities, tc.WantCapabilities)
	}

	return capabilities
}

func runStartAndPollTest(t *testing.T, ctx context.Context, tc *ContractCase) {
	t.Helper()

	execution, err := tc.Adapter.Start(ctx, tc.StartRequest)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	assertHandleEcho(t, execution.Handle, tc.StartRequest)

	if execution.State != tc.WantStartState {
		t.Fatalf("Start().State = %q, want %q", execution.State, tc.WantStartState)
	}

	for i, wantState := range tc.WantPollStates {
		execution, err = tc.Adapter.PollOrCollect(ctx, execution.Handle)
		if err != nil {
			t.Fatalf("PollOrCollect() #%d error = %v", i+1, err)
		}

		if execution.State != wantState {
			t.Fatalf("PollOrCollect() #%d state = %q, want %q", i+1, execution.State, wantState)
		}
	}

	normalizeRequest := tc.NormalizeRequest
	normalizeRequest.Execution = execution

	result, err := tc.Adapter.NormalizeResult(ctx, normalizeRequest)
	if err != nil {
		t.Fatalf("NormalizeResult() error = %v", err)
	}

	assertStepResult(t, result, tc.WantResult)
}

func runInterruptTest(t *testing.T, ctx context.Context, tc *ContractCase) {
	t.Helper()

	interruptExecution, err := tc.Adapter.Start(ctx, *tc.InterruptRequest)
	if err != nil {
		t.Fatalf("Start() for interrupt error = %v", err)
	}

	interruptExecution, err = tc.Adapter.Interrupt(ctx, interruptExecution.Handle)
	if err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}

	if interruptExecution.State != tc.WantInterruptState {
		t.Fatalf("Interrupt().State = %q, want %q", interruptExecution.State, tc.WantInterruptState)
	}
}

func runResumeTest(t *testing.T, ctx context.Context, tc *ContractCase, capabilities CapabilityMatrix) {
	t.Helper()

	resumeExecution, err := tc.Adapter.Start(ctx, *tc.ResumeRequest)
	if err != nil {
		t.Fatalf("Start() for resume error = %v", err)
	}

	if capabilities.Interrupt {
		resumeExecution, err = tc.Adapter.Interrupt(ctx, resumeExecution.Handle)
		if err != nil {
			t.Fatalf("Interrupt() before resume error = %v", err)
		}
	}

	resumeExecution, err = tc.Adapter.Resume(ctx, ResumeRequest{Handle: resumeExecution.Handle})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	if resumeExecution.State != tc.WantResumeState {
		t.Fatalf("Resume().State = %q, want %q", resumeExecution.State, tc.WantResumeState)
	}

	for i, wantState := range tc.WantResumePollStates {
		resumeExecution, err = tc.Adapter.PollOrCollect(ctx, resumeExecution.Handle)
		if err != nil {
			t.Fatalf("PollOrCollect() after resume #%d error = %v", i+1, err)
		}

		if resumeExecution.State != wantState {
			t.Fatalf("PollOrCollect() after resume #%d state = %q, want %q", i+1, resumeExecution.State, wantState)
		}
	}

	if tc.WantResumeResult != nil {
		resumeNormalize := tc.ResumeNormalize
		resumeNormalize.Execution = resumeExecution

		result, err := tc.Adapter.NormalizeResult(ctx, resumeNormalize)
		if err != nil {
			t.Fatalf("NormalizeResult() after resume error = %v", err)
		}

		assertStepResult(t, result, *tc.WantResumeResult)
	}
}

func assertHandleEcho(t *testing.T, got ExecutionHandle, want StartRequest) {
	t.Helper()

	if got.RunID != want.RunID {
		t.Fatalf("Handle.RunID = %q, want %q", got.RunID, want.RunID)
	}

	if got.StepID != want.StepID {
		t.Fatalf("Handle.StepID = %q, want %q", got.StepID, want.StepID)
	}

	if got.AttemptID != want.AttemptID {
		t.Fatalf("Handle.AttemptID = %q, want %q", got.AttemptID, want.AttemptID)
	}
}

func assertStepResult(t *testing.T, got *StepResult, want StepResult) {
	t.Helper()

	if got == nil {
		t.Fatal("StepResult = nil")
	}

	if want.Handle.RunID != "" && got.Handle.RunID != want.Handle.RunID {
		t.Fatalf("Handle.RunID = %q, want %q", got.Handle.RunID, want.Handle.RunID)
	}

	if want.Handle.StepID != "" && got.Handle.StepID != want.Handle.StepID {
		t.Fatalf("Handle.StepID = %q, want %q", got.Handle.StepID, want.Handle.StepID)
	}

	if want.Handle.AttemptID != "" && got.Handle.AttemptID != want.Handle.AttemptID {
		t.Fatalf("Handle.AttemptID = %q, want %q", got.Handle.AttemptID, want.Handle.AttemptID)
	}

	if want.Handle.ProviderSessionID != "" && got.Handle.ProviderSessionID != want.Handle.ProviderSessionID {
		t.Fatalf("Handle.ProviderSessionID = %q, want %q", got.Handle.ProviderSessionID, want.Handle.ProviderSessionID)
	}

	if want.Status != "" && got.Status != want.Status {
		t.Fatalf("Status = %q, want %q", got.Status, want.Status)
	}

	if want.Summary != "" && got.Summary != want.Summary {
		t.Fatalf("Summary = %q, want %q", got.Summary, want.Summary)
	}

	if want.OutputText != "" && got.OutputText != want.OutputText {
		t.Fatalf("OutputText = %q, want %q", got.OutputText, want.OutputText)
	}

	if want.StructuredOutput != nil && !equalJSON(got.StructuredOutput, want.StructuredOutput) {
		t.Fatalf("StructuredOutput = %s, want %s", string(got.StructuredOutput), string(want.StructuredOutput))
	}

	if want.ArtifactRefs != nil && !reflect.DeepEqual(got.ArtifactRefs, want.ArtifactRefs) {
		t.Fatalf("ArtifactRefs = %#v, want %#v", got.ArtifactRefs, want.ArtifactRefs)
	}

	if want.Logs != nil && !reflect.DeepEqual(got.Logs, want.Logs) {
		t.Fatalf("Logs = %#v, want %#v", got.Logs, want.Logs)
	}
}

func equalJSON(left, right json.RawMessage) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}

	var leftValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}

	var rightValue any
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}

	return reflect.DeepEqual(leftValue, rightValue)
}
