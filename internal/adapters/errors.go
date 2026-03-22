package adapters

import "fmt"

type ErrorCode string

const (
	ErrorCodeCapability ErrorCode = "capability"
	ErrorCodeRequest    ErrorCode = "request"
	ErrorCodeExecution  ErrorCode = "execution"
	ErrorCodeResult     ErrorCode = "result"
)

type Error struct {
	Code       ErrorCode
	Message    string
	Capability Capability
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	message := e.Message
	if e.Capability != "" {
		message = fmt.Sprintf("%s (%s)", message, e.Capability)
	}

	if e.Err != nil {
		return fmt.Sprintf("adapter %s error: %s: %v", e.Code, message, e.Err)
	}

	return fmt.Sprintf("adapter %s error: %s", e.Code, message)
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

func unsupportedCapabilityError(capability Capability) *Error {
	return &Error{Code: ErrorCodeCapability, Message: "capability unsupported", Capability: capability}
}
