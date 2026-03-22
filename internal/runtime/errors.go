package runtime

import "fmt"

type ErrorCode string

const (
	ErrorCodePath          ErrorCode = "path"
	ErrorCodeGit           ErrorCode = "git"
	ErrorCodeLock          ErrorCode = "lock"
	ErrorCodeDirtyWorktree ErrorCode = "dirty_worktree"
	ErrorCodePermission    ErrorCode = "permission"
	ErrorCodeState         ErrorCode = "state"
	ErrorCodeExecution     ErrorCode = "execution"
	ErrorCodeReplay        ErrorCode = "replay"
	ErrorCodeConfig        ErrorCode = "config"
)

type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	if e.Err != nil {
		return fmt.Sprintf("runtime %s error: %s: %v", e.Code, e.Message, e.Err)
	}

	return fmt.Sprintf("runtime %s error: %s", e.Code, e.Message)
}

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
