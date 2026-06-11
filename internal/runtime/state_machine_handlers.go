package runtime

import (
	"fmt"

	"github.com/JackDrogon/Cogito/internal/store"
	"github.com/JackDrogon/Cogito/internal/workflow"
)

type stateMachineEventHandler interface {
	Apply(request stateMachineEventRequest) error
}

type stateMachineEventRequest struct {
	Compiled    *workflow.CompiledWorkflow
	Snapshot    *Snapshot
	Transitions *[]Transition
	Event       store.Event
	Data        map[string]string
	Code        ErrorCode
}

type stateMachineEventHandlerFunc func(request stateMachineEventRequest) error

func (f stateMachineEventHandlerFunc) Apply(request stateMachineEventRequest) error {
	return f(request)
}

// stateMachineEventHandlers is the read-only event-type dispatch table. It is
// built once at package init (instead of per lookup) because applyEvent runs
// for every persisted event during replay. Do not mutate after init.
var stateMachineEventHandlers = map[store.EventType]stateMachineEventHandler{
	store.EventRunCreated:         stateMachineEventHandlerFunc(applyRunEvent),
	store.EventRunStarted:         stateMachineEventHandlerFunc(applyRunEvent),
	store.EventRunPaused:          stateMachineEventHandlerFunc(applyRunEvent),
	store.EventRunWaitingApproval: stateMachineEventHandlerFunc(applyRunEvent),
	store.EventRunSucceeded:       stateMachineEventHandlerFunc(applyRunEvent),
	store.EventRunFailed:          stateMachineEventHandlerFunc(applyRunEvent),
	store.EventRunCanceled:        stateMachineEventHandlerFunc(applyRunEvent),
	store.EventStepQueued:         stateMachineEventHandlerFunc(applyStepEvent),
	store.EventStepStarted:        stateMachineEventHandlerFunc(applyStepEvent),
	store.EventStepSucceeded:      stateMachineEventHandlerFunc(applyStepEvent),
	store.EventStepFailed:         stateMachineEventHandlerFunc(applyStepEvent),
	store.EventStepRetried:        stateMachineEventHandlerFunc(applyStepEvent),
	store.EventStepInterrupted:    stateMachineEventHandlerFunc(applyStepEvent),
	store.EventApprovalRequested:  stateMachineEventHandlerFunc(applyApprovalEvent),
	store.EventApprovalGranted:    stateMachineEventHandlerFunc(applyApprovalEvent),
	store.EventApprovalDenied:     stateMachineEventHandlerFunc(applyApprovalEvent),
	store.EventApprovalTimedOut:   stateMachineEventHandlerFunc(applyApprovalEvent),
}

func lookupStateMachineEventHandler(eventType store.EventType) (stateMachineEventHandler, error) {
	handler, ok := stateMachineEventHandlers[eventType]
	if !ok {
		return nil, newError(ErrorCodeReplay, fmt.Sprintf("unsupported event type %q", eventType))
	}

	return handler, nil
}
