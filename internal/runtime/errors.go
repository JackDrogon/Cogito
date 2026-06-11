package runtime

import "fmt"

// ErrorCode identifies the subsystem or failure category for a runtime Error.
type ErrorCode string

const (
	// ErrorCodePath indicates a missing or invalid path argument.
	ErrorCodePath ErrorCode = "path"
	// ErrorCodeGit indicates a git operation failure, such as being unable to
	// resolve the repository root.
	ErrorCodeGit ErrorCode = "git"
	// ErrorCodeLock indicates a lock acquisition or release failure.
	ErrorCodeLock ErrorCode = "lock"
	// ErrorCodeDirtyWorktree indicates that the repository has uncommitted
	// changes and AllowDirty was not set.
	ErrorCodeDirtyWorktree ErrorCode = "dirty_worktree"
	// ErrorCodePermission indicates a filesystem permission failure when
	// creating lock directories or writing lock files.
	ErrorCodePermission ErrorCode = "permission"
	// ErrorCodeState indicates an invalid or unexpected state machine
	// transition.
	ErrorCodeState ErrorCode = "state"
	// ErrorCodeExecution indicates a step execution failure, such as a driver
	// setup error or an unsupported approval decision.
	ErrorCodeExecution ErrorCode = "execution"
	// ErrorCodeReplay indicates a failure while replaying the event log to
	// rebuild snapshot state.
	ErrorCodeReplay ErrorCode = "replay"
	// ErrorCodeConfig indicates a missing or invalid configuration value
	// supplied to the engine or its dependencies.
	ErrorCodeConfig ErrorCode = "config"
)

// Error is the structured error type returned by all runtime operations.
// It carries a stable ErrorCode for programmatic handling, a human-readable
// Message, and an optional wrapped cause accessible via Unwrap.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

// Error returns a formatted string combining the error code, message, and
// wrapped cause. A nil receiver returns "<nil>".
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	if e.Err != nil {
		return fmt.Sprintf("runtime %s error: %s: %v", e.Code, e.Message, e.Err)
	}

	return fmt.Sprintf("runtime %s error: %s", e.Code, e.Message)
}

// Unwrap returns the wrapped cause so errors.Is and errors.As can traverse
// the chain. A nil receiver returns nil.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}

func newError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message}
}

func wrapError(code ErrorCode, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}
