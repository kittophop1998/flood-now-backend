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
	CodeUnauthorized    Code = "UNAUTHORIZED"
	CodeForbidden       Code = "FORBIDDEN"
	CodeRateLimited     Code = "RATE_LIMITED"
	// Cookie-session request forgery guards (HTTP adapter): a signed-in
	// state-changing request without / with a wrong X-CSRF-Token, or from an
	// origin that isn't the web app.
	CodeCSRFTokenMissing Code = "CSRF_TOKEN_MISSING"
	CodeCSRFTokenInvalid Code = "CSRF_TOKEN_INVALID"
	CodeInvalidOrigin    Code = "INVALID_ORIGIN"
	// A provider's credit balance can't pay the match fee (local services).
	CodeInsufficientCredit Code = "INSUFFICIENT_CREDIT"
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

func Conflict(message string) *Error {
	return &Error{Code: CodeConflict, Message: message}
}

// Unauthorized means the caller must sign in (or send a valid operator
// token, on admin routes) to do this.
func Unauthorized(message string) *Error {
	return &Error{Code: CodeUnauthorized, Message: message}
}

// Forbidden means the caller is signed in but may not act on this resource
// (e.g. editing someone else's event).
func Forbidden(message string) *Error {
	return &Error{Code: CodeForbidden, Message: message}
}

// RateLimited means the caller did the same thing too often; retry later.
func RateLimited(message string) *Error {
	return &Error{Code: CodeRateLimited, Message: message}
}

// Rejected builds one of the 403 request-forgery errors (CSRF_TOKEN_MISSING,
// CSRF_TOKEN_INVALID, INVALID_ORIGIN).
func Rejected(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// InsufficientCredit refuses a match the provider's balance can't pay for.
// Fields carry "balance" and "required" (credits) so the client can show
// them next to a top-up action.
func InsufficientCredit(balance, required int) *Error {
	return &Error{Code: CodeInsufficientCredit, Message: "not enough credit to accept this job; top up and accept again",
		Fields: map[string]string{"balance": fmt.Sprint(balance), "required": fmt.Sprint(required)}}
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
