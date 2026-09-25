// Package apperr defines the small set of error shapes the application and
// adapter layers use to communicate failure without leaking framework or
// storage details into domain/application code.
package apperr

import "fmt"

type Code string

const (
	CodeValidation      Code = "VALIDATION_ERROR"
	CodeNotFound        Code = "NOT_FOUND"
	CodeConflict        Code = "CONFLICT"
	CodePayloadTooLarge Code = "PAYLOAD_TOO_LARGE"
	CodeUnavailable     Code = "UPSTREAM_UNAVAILABLE"
	CodeInternal        Code = "INTERNAL_ERROR"
)

// Error is the single error type carried across layers. Handlers map it to
// an HTTP status + the documented JSON error envelope.
type Error struct {
	Code    Code
	Message string
	Fields  map[string]string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func Validation(message string, fields map[string]string) *Error {
	return &Error{Code: CodeValidation, Message: message, Fields: fields}
}

func NotFound(message string) *Error {
	return &Error{Code: CodeNotFound, Message: message}
}

func PayloadTooLarge(message string) *Error {
	return &Error{Code: CodePayloadTooLarge, Message: message}
}

func Internal(message string) *Error {
	return &Error{Code: CodeInternal, Message: message}
}

// Unavailable signals a dependency outside our control (e.g. the geocoder)
// is down; the client can retry later.
func Unavailable(message string) *Error {
	return &Error{Code: CodeUnavailable, Message: message}
}
