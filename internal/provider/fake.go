package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

type FakeSnapshot struct {
	State            ExecutionState
	Summary          string
	OutputText       string
	StructuredOutput json.RawMessage
	ArtifactRefs     []ArtifactRef
	Logs             []LogEntry
}

type FakeScript struct {
	Start       FakeSnapshot
	Polls       []FakeSnapshot
	Interrupt   *FakeSnapshot
	Resume      *FakeSnapshot
	ResumePolls []FakeSnapshot
}

type FakeConfig struct {
	Capabilities CapabilityMatrix
	Scripts      map[string]FakeScript
}

type FakeProvider struct {
	capabilities CapabilityMatrix
	scripts      map[string]FakeScript

	mu       sync.Mutex
	sessions map[string]*fakeSession
	started  int
}

type fakeSession struct {
	handle          ExecutionHandle
	script          FakeScript
	current         *Execution
	pollIndex       int
	resumed         bool
	resumePollIndex int
	interrupted     bool
}

func NewFakeProvider(config FakeConfig) *FakeProvider {
	scripts := make(map[string]FakeScript, len(config.Scripts))
	for attemptID := range config.Scripts {
		scripts[attemptID] = cloneFakeScript(config.Scripts[attemptID])
	}

	return &FakeProvider{
		capabilities: config.Capabilities,
		scripts:      scripts,
		sessions:     map[string]*fakeSession{},
	}
}

func (a *FakeProvider) DescribeCapabilities() CapabilityMatrix {
	return a.capabilities
}

func (a *FakeProvider) Start(_ context.Context, request StartRequest) (*Execution, error) {
	if err := ValidateStartRequest(request); err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	script, ok := a.scripts[request.AttemptID]
	if !ok {
		return nil, newError(ErrorCodeExecution, "fake script not found")
	}

	a.started++
	handle := ExecutionHandle{
		RunID:             request.RunID,
		StepID:            request.StepID,
		AttemptID:         request.AttemptID,
		ProviderSessionID: fmt.Sprintf("fake-session-%02d", a.started),
	}

	execution := buildExecution(handle, script.Start)
	a.sessions[handle.ProviderSessionID] = &fakeSession{
		handle:  handle,
		script:  cloneFakeScript(script),
		current: execution,
	}

	return CloneExecution(execution), nil
}

func (a *FakeProvider) PollOrCollect(_ context.Context, handle ExecutionHandle) (*Execution, error) {
	if err := ValidateHandle(handle); err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	session, err := a.lookupSession(handle)
	if err != nil {
		return nil, err
	}

	var snapshots []FakeSnapshot

	var index *int

	if session.resumed {
		snapshots = session.script.ResumePolls
		index = &session.resumePollIndex
	} else {
		snapshots = session.script.Polls
		index = &session.pollIndex
	}

	if len(snapshots) > 0 && *index < len(snapshots) {
		session.current = buildExecution(session.handle, snapshots[*index])
		(*index)++
	}

	return CloneExecution(session.current), nil
}

func (a *FakeProvider) Interrupt(_ context.Context, handle ExecutionHandle) (*Execution, error) {
	if err := a.capabilities.Require(CapabilityInterrupt); err != nil {
		return nil, err
	}

	if err := ValidateHandle(handle); err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	session, err := a.lookupSession(handle)
	if err != nil {
		return nil, err
	}

	snapshot := FakeSnapshot{State: ExecutionStateInterrupted, Summary: "interrupted"}
	if session.script.Interrupt != nil {
		snapshot = *session.script.Interrupt
	}

	session.current = buildExecution(session.handle, snapshot)
	session.interrupted = true

	return CloneExecution(session.current), nil
}

func (a *FakeProvider) Resume(_ context.Context, request ResumeRequest) (*Execution, error) {
	if err := a.capabilities.Require(CapabilityResume); err != nil {
		return nil, err
	}

	if err := ValidateHandle(request.Handle); err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	session, err := a.lookupSession(request.Handle)
	if err != nil {
		return nil, err
	}

	if !session.interrupted && session.current.State != ExecutionStateWaitingApproval {
		return nil, newError(ErrorCodeExecution, "execution is not resumable")
	}

	snapshot := FakeSnapshot{State: ExecutionStateRunning, Summary: "resumed"}
	if session.script.Resume != nil {
		snapshot = *session.script.Resume
	}

	session.current = buildExecution(session.handle, snapshot)
	session.resumed = true
	session.interrupted = false

	return CloneExecution(session.current), nil
}

func (a *FakeProvider) NormalizeResult(_ context.Context, request NormalizeRequest) (*StepResult, error) {
	return NormalizeResult(request, a.capabilities)
}

func (a *FakeProvider) lookupSession(handle ExecutionHandle) (*fakeSession, error) {
	session, ok := a.sessions[handle.ProviderSessionID]
	if !ok {
		return nil, newError(ErrorCodeExecution, "execution session not found")
	}

	if session.handle.RunID != handle.RunID || session.handle.StepID != handle.StepID || session.handle.AttemptID != handle.AttemptID {
		return nil, newError(ErrorCodeExecution, "execution handle does not match session")
	}

	return session, nil
}

func buildExecution(handle ExecutionHandle, snapshot FakeSnapshot) *Execution {
	return &Execution{
		Handle:           handle,
		State:            snapshot.State,
		Summary:          snapshot.Summary,
		OutputText:       snapshot.OutputText,
		StructuredOutput: CloneJSON(snapshot.StructuredOutput),
		ArtifactRefs:     CloneArtifactRefs(snapshot.ArtifactRefs),
		Logs:             CloneLogs(snapshot.Logs),
	}
}

func cloneFakeScript(script FakeScript) FakeScript {
	cloned := FakeScript{
		Start:       cloneSnapshot(script.Start),
		Polls:       cloneSnapshots(script.Polls),
		ResumePolls: cloneSnapshots(script.ResumePolls),
	}

	if script.Interrupt != nil {
		snapshot := cloneSnapshot(*script.Interrupt)
		cloned.Interrupt = &snapshot
	}

	if script.Resume != nil {
		snapshot := cloneSnapshot(*script.Resume)
		cloned.Resume = &snapshot
	}

	return cloned
}

func cloneSnapshots(snapshots []FakeSnapshot) []FakeSnapshot {
	if snapshots == nil {
		return nil
	}

	cloned := make([]FakeSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		cloned = append(cloned, cloneSnapshot(snapshot))
	}

	return cloned
}

func cloneSnapshot(snapshot FakeSnapshot) FakeSnapshot {
	return FakeSnapshot{
		State:            snapshot.State,
		Summary:          snapshot.Summary,
		OutputText:       snapshot.OutputText,
		StructuredOutput: CloneJSON(snapshot.StructuredOutput),
		ArtifactRefs:     CloneArtifactRefs(snapshot.ArtifactRefs),
		Logs:             CloneLogs(snapshot.Logs),
	}
}
