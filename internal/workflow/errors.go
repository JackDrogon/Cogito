package workflow

import "fmt"

// ErrorCode identifies the validation stage that produced an error.
type ErrorCode string

const (
	ErrorCodeParse    ErrorCode = "parse"
	ErrorCodeSchema   ErrorCode = "schema"
	ErrorCodeSemantic ErrorCode = "semantic"
	ErrorCodeVersion  ErrorCode = "version"
)

// Error provides stable error codes and messages for workflow validation.
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
		return fmt.Sprintf("workflow %s error: %s: %v", e.Code, e.Message, e.Err)
	}

	return fmt.Sprintf("workflow %s error: %s", e.Code, e.Message)
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
