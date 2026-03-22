package app

import (
	"context"
	"fmt"

	"github.com/JackDrogon/Cogito/internal/adapters"
)

var builtinLocalProviders = map[string]struct{}{
	"reviewer": {},
	"writer":   {},
}

type builtinLocalAdapter struct {
	provider string
}

func lookupBuiltinLocalAdapter(name string) (adapters.Adapter, bool) {
	if _, ok := builtinLocalProviders[name]; !ok {
		return nil, false
	}

	return builtinLocalAdapter{provider: name}, true
}

func (a builtinLocalAdapter) DescribeCapabilities() adapters.CapabilityMatrix {
	return adapters.CapabilityMatrix{Resume: true, Interrupt: true}
}

func (a builtinLocalAdapter) Start(_ context.Context, request adapters.StartRequest) (*adapters.Execution, error) {
	return &adapters.Execution{
		Handle: adapters.ExecutionHandle{
			RunID:             request.RunID,
			StepID:            request.StepID,
			AttemptID:         request.AttemptID,
			ProviderSessionID: fmt.Sprintf("builtin-%s-%s", a.provider, request.StepID),
		},
		State:   adapters.ExecutionStateSucceeded,
		Summary: fmt.Sprintf("%s step ok", a.provider),
	}, nil
}

func (a builtinLocalAdapter) PollOrCollect(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateSucceeded, Summary: fmt.Sprintf("%s step ok", a.provider)}, nil
}

func (a builtinLocalAdapter) Interrupt(_ context.Context, handle adapters.ExecutionHandle) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: handle, State: adapters.ExecutionStateInterrupted, Summary: "interrupted"}, nil
}

func (a builtinLocalAdapter) Resume(_ context.Context, request adapters.ResumeRequest) (*adapters.Execution, error) {
	return &adapters.Execution{Handle: request.Handle, State: adapters.ExecutionStateSucceeded, Summary: fmt.Sprintf("%s step ok", a.provider)}, nil
}

func (a builtinLocalAdapter) NormalizeResult(_ context.Context, request adapters.NormalizeRequest) (*adapters.StepResult, error) {
	if request.Execution == nil {
		return nil, fmt.Errorf("execution is required")
	}

	return &adapters.StepResult{
		Handle:  request.Execution.Handle,
		Status:  request.Execution.State,
		Summary: request.Execution.Summary,
	}, nil
}
