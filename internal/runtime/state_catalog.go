package runtime

type stateSet[T comparable] map[T]struct{}

func (s stateSet[T]) Has(value T) bool {
	_, ok := s[value]
	return ok
}

var validRunStates = stateSet[RunState]{
	RunStatePending:         {},
	RunStateRunning:         {},
	RunStateWaitingApproval: {},
	RunStatePaused:          {},
	RunStateSucceeded:       {},
	RunStateFailed:          {},
	RunStateCanceled:        {},
}

var validStepStates = stateSet[StepState]{
	StepStatePending:         {},
	StepStateQueued:          {},
	StepStateRunning:         {},
	StepStateWaitingApproval: {},
	StepStateSucceeded:       {},
	StepStateFailed:          {},
	StepStateCanceled:        {},
}

var terminalStepStates = stateSet[StepState]{
	StepStateSucceeded: {},
	StepStateFailed:    {},
	StepStateCanceled:  {},
}
