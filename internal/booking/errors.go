package booking

import (
	"errors"
	"net/http"
)

// Error codes returned to API clients as {"error":{"code":..., "message":...}}.
const (
	CodeValidation         = "validation_error"
	CodeNotFound           = "not_found"
	CodeForbidden          = "forbidden"
	CodeClassStarted       = "class_started"
	CodeClassFull          = "class_full"
	CodeDuplicateConfirmed = "duplicate_confirmed"
	CodeBookingNotPending  = "booking_not_pending"
)

// Error is a business error with a stable machine-readable code and the HTTP status
// the API layer should use. Anything that is not an *Error is a 500.
type Error struct {
	Code    string
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// AsError unwraps a business error if err is (or wraps) one.
func AsError(err error) (*Error, bool) {
	var be *Error
	if errors.As(err, &be) {
		return be, true
	}
	return nil, false
}

func errNotFound(msg string) *Error {
	return &Error{Code: CodeNotFound, Status: http.StatusNotFound, Message: msg}
}

func errForbidden(msg string) *Error {
	return &Error{Code: CodeForbidden, Status: http.StatusForbidden, Message: msg}
}

func errConflict(code, msg string) *Error {
	return &Error{Code: code, Status: http.StatusConflict, Message: msg}
}
