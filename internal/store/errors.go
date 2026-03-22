package store

import "fmt"

type ErrorCode string

const (
	ErrorCodePath       ErrorCode = "path"
	ErrorCodePermission ErrorCode = "permission"
	ErrorCodeWorkflow   ErrorCode = "workflow"
	ErrorCodeEventLog   ErrorCode = "event_log"
	ErrorCodeCheckpoint ErrorCode = "checkpoint"
	ErrorCodeArtifacts  ErrorCode = "artifacts"
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
		return fmt.Sprintf("store %s error: %s: %v", e.Code, e.Message, e.Err)
	}

	return fmt.Sprintf("store %s error: %s", e.Code, e.Message)
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
