package adapters

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

type FakeAdapter struct {
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

func NewFakeAdapter(config FakeConfig) *FakeAdapter {
	scripts := make(map[string]FakeScript, len(config.Scripts))
	for attemptID, script := range config.Scripts {
		scripts[attemptID] = cloneFakeScript(script)
	}

	return &FakeAdapter{
		capabilities: config.Capabilities,
		scripts:      scripts,
		sessions:     map[string]*fakeSession{},
	}
}

func (a *FakeAdapter) DescribeCapabilities() CapabilityMatrix {
	return a.capabilities
}

func (a *FakeAdapter) Start(_ context.Context, request StartRequest) (*Execution, error) {
	if err := validateStartRequest(request); err != nil {
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

	return cloneExecution(execution), nil
}

func (a *FakeAdapter) PollOrCollect(_ context.Context, handle ExecutionHandle) (*Execution, error) {
	if err := validateHandle(handle); err != nil {
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

	return cloneExecution(session.current), nil
}

func (a *FakeAdapter) Interrupt(_ context.Context, handle ExecutionHandle) (*Execution, error) {
	if err := a.capabilities.Require(CapabilityInterrupt); err != nil {
		return nil, err
	}

	if err := validateHandle(handle); err != nil {
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
	return cloneExecution(session.current), nil
}

func (a *FakeAdapter) Resume(_ context.Context, request ResumeRequest) (*Execution, error) {
	if err := a.capabilities.Require(CapabilityResume); err != nil {
		return nil, err
	}

	if err := validateHandle(request.Handle); err != nil {
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
	return cloneExecution(session.current), nil
}

func (a *FakeAdapter) NormalizeResult(_ context.Context, request NormalizeRequest) (*StepResult, error) {
	if request.Execution == nil {
		return nil, newError(ErrorCodeResult, "execution is required")
	}

	if !request.Execution.State.Normalizable() {
		return nil, newError(ErrorCodeResult, "execution state cannot be normalized")
	}

	if request.RequireStructuredOutput {
		if err := a.capabilities.Require(CapabilityStructuredOutput); err != nil {
			return nil, err
		}
	}

	if request.RequireArtifactRefs {
		if err := a.capabilities.Require(CapabilityArtifactRefs); err != nil {
			return nil, err
		}
	}

	if request.RequireMachineReadableLogs {
		if err := a.capabilities.Require(CapabilityMachineReadableLogs); err != nil {
			return nil, err
		}
	}

	result := &StepResult{
		Handle:           request.Execution.Handle,
		Status:           request.Execution.State,
		Summary:          request.Execution.Summary,
		OutputText:       request.Execution.OutputText,
		StructuredOutput: cloneJSON(request.Execution.StructuredOutput),
		ArtifactRefs:     cloneArtifactRefs(request.Execution.ArtifactRefs),
		Logs:             cloneLogs(request.Execution.Logs),
	}

	return result, nil
}

func (a *FakeAdapter) lookupSession(handle ExecutionHandle) (*fakeSession, error) {
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
		StructuredOutput: cloneJSON(snapshot.StructuredOutput),
		ArtifactRefs:     cloneArtifactRefs(snapshot.ArtifactRefs),
		Logs:             cloneLogs(snapshot.Logs),
	}
}

func cloneExecution(execution *Execution) *Execution {
	if execution == nil {
		return nil
	}

	return &Execution{
		Handle:           execution.Handle,
		State:            execution.State,
		Summary:          execution.Summary,
		OutputText:       execution.OutputText,
		StructuredOutput: cloneJSON(execution.StructuredOutput),
		ArtifactRefs:     cloneArtifactRefs(execution.ArtifactRefs),
		Logs:             cloneLogs(execution.Logs),
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
		StructuredOutput: cloneJSON(snapshot.StructuredOutput),
		ArtifactRefs:     cloneArtifactRefs(snapshot.ArtifactRefs),
		Logs:             cloneLogs(snapshot.Logs),
	}
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}

	cloned := make(json.RawMessage, len(value))
	copy(cloned, value)
	return cloned
}

func cloneArtifactRefs(artifacts []ArtifactRef) []ArtifactRef {
	if artifacts == nil {
		return nil
	}

	cloned := make([]ArtifactRef, 0, len(artifacts))
	cloned = append(cloned, artifacts...)
	return cloned
}

func cloneLogs(logs []LogEntry) []LogEntry {
	if logs == nil {
		return nil
	}

	cloned := make([]LogEntry, 0, len(logs))
	for _, entry := range logs {
		clonedEntry := LogEntry{Level: entry.Level, Message: entry.Message}
		if entry.Fields != nil {
			clonedEntry.Fields = make(map[string]string, len(entry.Fields))
			for key, value := range entry.Fields {
				clonedEntry.Fields[key] = value
			}
		}

		cloned = append(cloned, clonedEntry)
	}

	return cloned
}
