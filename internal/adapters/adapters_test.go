package adapters

import (
	"errors"
	"testing"
)

func TestCapabilityMatrix(t *testing.T) {
	tests := []struct {
		name        string
		matrix      CapabilityMatrix
		capability  Capability
		wantSupport bool
	}{
		{
			name:        "structured output supported",
			matrix:      CapabilityMatrix{StructuredOutput: true},
			capability:  CapabilityStructuredOutput,
			wantSupport: true,
		},
		{
			name:        "resume unsupported",
			matrix:      CapabilityMatrix{},
			capability:  CapabilityResume,
			wantSupport: false,
		},
		{
			name:        "interrupt supported",
			matrix:      CapabilityMatrix{Interrupt: true},
			capability:  CapabilityInterrupt,
			wantSupport: true,
		},
		{
			name:        "artifact refs supported",
			matrix:      CapabilityMatrix{ArtifactRefs: true},
			capability:  CapabilityArtifactRefs,
			wantSupport: true,
		},
		{
			name:        "machine readable logs unsupported",
			matrix:      CapabilityMatrix{},
			capability:  CapabilityMachineReadableLogs,
			wantSupport: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.matrix.Supports(tt.capability); got != tt.wantSupport {
				t.Fatalf("Supports(%q) = %t, want %t", tt.capability, got, tt.wantSupport)
			}
		})
	}
}

func TestAdapterContractSuite(t *testing.T) {
	capabilities := CapabilityMatrix{
		StructuredOutput:    true,
		Resume:              true,
		Interrupt:           true,
		ArtifactRefs:        true,
		MachineReadableLogs: true,
	}

	RunContractSuite(t, []ContractCase{
		{
			Name:             "full capability success path",
			Adapter:          contractFakeAdapter(capabilities),
			StartRequest:     StartRequest{RunID: "run-123", StepID: "draft", AttemptID: "attempt-1", WorkingDir: ".", Prompt: "Draft the plan"},
			WantCapabilities: capabilities,
			WantStartState:   ExecutionStateRunning,
			WantPollStates:   []ExecutionState{ExecutionStateRunning, ExecutionStateSucceeded},
			NormalizeRequest: NormalizeRequest{RequireStructuredOutput: true, RequireArtifactRefs: true, RequireMachineReadableLogs: true},
			WantResult: StepResult{
				Handle:           ExecutionHandle{RunID: "run-123", StepID: "draft", AttemptID: "attempt-1", ProviderSessionID: "fake-session-01"},
				Status:           ExecutionStateSucceeded,
				Summary:          "completed",
				OutputText:       "ok",
				StructuredOutput: []byte(`{"summary":"done"}`),
				ArtifactRefs:     []ArtifactRef{{Path: "artifacts/report.txt", Kind: "report", Summary: "report"}},
				Logs:             []LogEntry{{Level: "info", Message: "finished", Fields: map[string]string{"event": "done"}}},
			},
			InterruptRequest:     &StartRequest{RunID: "run-123", StepID: "draft", AttemptID: "attempt-2", WorkingDir: ".", Prompt: "Interrupt me"},
			WantInterruptState:   ExecutionStateInterrupted,
			ResumeRequest:        &StartRequest{RunID: "run-123", StepID: "draft", AttemptID: "attempt-3", WorkingDir: ".", Prompt: "Resume me"},
			WantResumeState:      ExecutionStateRunning,
			WantResumePollStates: []ExecutionState{ExecutionStateSucceeded},
			ResumeNormalize:      NormalizeRequest{RequireStructuredOutput: true, RequireArtifactRefs: true, RequireMachineReadableLogs: true},
			WantResumeResult: &StepResult{
				Handle:           ExecutionHandle{RunID: "run-123", StepID: "draft", AttemptID: "attempt-3", ProviderSessionID: "fake-session-03"},
				Status:           ExecutionStateSucceeded,
				Summary:          "resumed complete",
				OutputText:       "resumed ok",
				StructuredOutput: []byte(`{"resume":true}`),
				ArtifactRefs:     []ArtifactRef{{Path: "artifacts/resume.txt", Kind: "resume"}},
				Logs:             []LogEntry{{Level: "info", Message: "resumed", Fields: map[string]string{"phase": "resume"}}},
			},
		},
	})

	t.Log("fake adapter passed")
}

func TestUnsupportedCapabilitiesAreExplicit(t *testing.T) {
	tests := []struct {
		name           string
		call           func(adapter Adapter) error
		wantCapability Capability
	}{
		{
			name: "interrupt unsupported",
			call: func(adapter Adapter) error {
				start, err := adapter.Start(t.Context(), StartRequest{RunID: "run-123", StepID: "draft", AttemptID: "attempt-1"})
				if err != nil {
					return err
				}
				_, err = adapter.Interrupt(t.Context(), start.Handle)
				return err
			},
			wantCapability: CapabilityInterrupt,
		},
		{
			name: "resume unsupported",
			call: func(adapter Adapter) error {
				start, err := adapter.Start(t.Context(), StartRequest{RunID: "run-123", StepID: "draft", AttemptID: "attempt-1"})
				if err != nil {
					return err
				}
				_, err = adapter.Resume(t.Context(), ResumeRequest{Handle: start.Handle})
				return err
			},
			wantCapability: CapabilityResume,
		},
		{
			name: "structured output unsupported",
			call: func(adapter Adapter) error {
				start, err := adapter.Start(t.Context(), StartRequest{RunID: "run-123", StepID: "draft", AttemptID: "attempt-1"})
				if err != nil {
					return err
				}
				execution, err := adapter.PollOrCollect(t.Context(), start.Handle)
				if err != nil {
					return err
				}
				execution, err = adapter.PollOrCollect(t.Context(), start.Handle)
				if err != nil {
					return err
				}
				_, err = adapter.NormalizeResult(t.Context(), NormalizeRequest{Execution: execution, RequireStructuredOutput: true})
				return err
			},
			wantCapability: CapabilityStructuredOutput,
		},
		{
			name: "artifact refs unsupported",
			call: func(adapter Adapter) error {
				start, err := adapter.Start(t.Context(), StartRequest{RunID: "run-123", StepID: "draft", AttemptID: "attempt-1"})
				if err != nil {
					return err
				}
				execution, err := adapter.PollOrCollect(t.Context(), start.Handle)
				if err != nil {
					return err
				}
				execution, err = adapter.PollOrCollect(t.Context(), start.Handle)
				if err != nil {
					return err
				}
				_, err = adapter.NormalizeResult(t.Context(), NormalizeRequest{Execution: execution, RequireArtifactRefs: true})
				return err
			},
			wantCapability: CapabilityArtifactRefs,
		},
		{
			name: "machine readable logs unsupported",
			call: func(adapter Adapter) error {
				start, err := adapter.Start(t.Context(), StartRequest{RunID: "run-123", StepID: "draft", AttemptID: "attempt-1"})
				if err != nil {
					return err
				}
				execution, err := adapter.PollOrCollect(t.Context(), start.Handle)
				if err != nil {
					return err
				}
				execution, err = adapter.PollOrCollect(t.Context(), start.Handle)
				if err != nil {
					return err
				}
				_, err = adapter.NormalizeResult(t.Context(), NormalizeRequest{Execution: execution, RequireMachineReadableLogs: true})
				return err
			},
			wantCapability: CapabilityMachineReadableLogs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := contractFakeAdapter(CapabilityMatrix{})
			err := tt.call(adapter)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			var adapterErr *Error
			if !errors.As(err, &adapterErr) {
				t.Fatalf("error type = %T, want *adapters.Error", err)
			}

			if adapterErr.Code != ErrorCodeCapability {
				t.Fatalf("error code = %q, want %q", adapterErr.Code, ErrorCodeCapability)
			}

			if adapterErr.Capability != tt.wantCapability {
				t.Fatalf("error capability = %q, want %q", adapterErr.Capability, tt.wantCapability)
			}
		})
	}

	t.Log("capability unsupported")
}

func contractFakeAdapter(capabilities CapabilityMatrix) Adapter {
	return NewFakeAdapter(FakeConfig{
		Capabilities: capabilities,
		Scripts: map[string]FakeScript{
			"attempt-1": {
				Start: FakeSnapshot{State: ExecutionStateRunning, Summary: "started"},
				Polls: []FakeSnapshot{
					{State: ExecutionStateRunning, Summary: "streaming"},
					{
						State:            ExecutionStateSucceeded,
						Summary:          "completed",
						OutputText:       "ok",
						StructuredOutput: []byte(`{"summary":"done"}`),
						ArtifactRefs:     []ArtifactRef{{Path: "artifacts/report.txt", Kind: "report", Summary: "report"}},
						Logs:             []LogEntry{{Level: "info", Message: "finished", Fields: map[string]string{"event": "done"}}},
					},
				},
			},
			"attempt-2": {
				Start:     FakeSnapshot{State: ExecutionStateRunning, Summary: "started"},
				Interrupt: &FakeSnapshot{State: ExecutionStateInterrupted, Summary: "stopped"},
			},
			"attempt-3": {
				Start:     FakeSnapshot{State: ExecutionStateRunning, Summary: "started"},
				Interrupt: &FakeSnapshot{State: ExecutionStateInterrupted, Summary: "stopped"},
				Resume:    &FakeSnapshot{State: ExecutionStateRunning, Summary: "resumed"},
				ResumePolls: []FakeSnapshot{{
					State:            ExecutionStateSucceeded,
					Summary:          "resumed complete",
					OutputText:       "resumed ok",
					StructuredOutput: []byte(`{"resume":true}`),
					ArtifactRefs:     []ArtifactRef{{Path: "artifacts/resume.txt", Kind: "resume"}},
					Logs:             []LogEntry{{Level: "info", Message: "resumed", Fields: map[string]string{"phase": "resume"}}},
				}},
			},
		},
	})
}
