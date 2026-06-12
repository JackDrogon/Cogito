package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/JackDrogon/Cogito/internal/provider"
)

var builtinLocalProviders = map[string]struct{}{
	"reviewer": {},
	"writer":   {},
}

type builtinLocalProvider struct {
	provider string
}

func lookupBuiltinLocalProvider(name string) (provider.Provider, bool) {
	if _, ok := builtinLocalProviders[name]; !ok {
		return nil, false
	}

	return builtinLocalProvider{provider: name}, true
}

func (a builtinLocalProvider) DescribeCapabilities() provider.CapabilityMatrix {
	return provider.CapabilityMatrix{Resume: true, Interrupt: true}
}

func (a builtinLocalProvider) Start(_ context.Context, request provider.StartRequest) (*provider.Execution, error) {
	return &provider.Execution{
		Handle: provider.ExecutionHandle{
			RunID:             request.RunID,
			StepID:            request.StepID,
			AttemptID:         request.AttemptID,
			ProviderSessionID: fmt.Sprintf("builtin-%s-%s", a.provider, request.StepID),
		},
		State:   provider.ExecutionStateSucceeded,
		Summary: a.provider + " step ok",
	}, nil
}

func (a builtinLocalProvider) PollOrCollect(_ context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	return &provider.Execution{Handle: handle, State: provider.ExecutionStateSucceeded, Summary: a.provider + " step ok"}, nil
}

func (a builtinLocalProvider) Interrupt(_ context.Context, handle provider.ExecutionHandle) (*provider.Execution, error) {
	return &provider.Execution{Handle: handle, State: provider.ExecutionStateInterrupted, Summary: "interrupted"}, nil
}

func (a builtinLocalProvider) Resume(_ context.Context, request provider.ResumeRequest) (*provider.Execution, error) {
	return &provider.Execution{Handle: request.Handle, State: provider.ExecutionStateSucceeded, Summary: a.provider + " step ok"}, nil
}

func (a builtinLocalProvider) NormalizeResult(_ context.Context, request provider.NormalizeRequest) (*provider.StepResult, error) {
	if request.Execution == nil {
		return nil, errors.New("builtinLocalProvider.NormalizeResult: execution is required")
	}

	return &provider.StepResult{
		Handle:  request.Execution.Handle,
		Status:  request.Execution.State,
		Summary: request.Execution.Summary,
	}, nil
}
