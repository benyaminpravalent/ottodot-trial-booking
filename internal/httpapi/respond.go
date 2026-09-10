package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"ottodot/internal/booking"
	"ottodot/internal/payments"
)

const maxBodyBytes = 64 << 10

// errorBody is the wire shape for every error: {"error":{"code":"...","message":"..."}}.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// writeServiceError maps a service-layer error to HTTP. Business errors carry their own
// status and code; anything else is a 500 with the detail kept server-side.
func writeServiceError(logger *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	if be, ok := booking.AsError(err); ok {
		writeError(w, be.Status, be.Code, be.Message)
		return
	}
	logger.Error("internal error", "request_id", RequestID(r.Context()), "path", r.URL.Path, "err", err.Error())
	writeError(w, http.StatusInternalServerError, "internal_error", "something went wrong; please retry")
}

func validationErr(msg string) *booking.Error {
	return &booking.Error{Code: booking.CodeValidation, Status: http.StatusBadRequest, Message: msg}
}

// validationError collects field-level problems and renders as one 400.
type validationError struct {
	problems []string
}

func (v *validationError) add(field, problem string) {
	v.problems = append(v.problems, field+": "+problem)
}

func (v *validationError) addAll(problems []string) {
	v.problems = append(v.problems, problems...)
}

func (v *validationError) err() error {
	if len(v.problems) == 0 {
		return nil
	}
	return validationErr(strings.Join(v.problems, "; "))
}

// decodeJSON is the strict boundary decoder: bounded body, unknown fields rejected,
// exactly one JSON value.
func decodeJSON(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			return validationErr("request body too large")
		case errors.Is(err, io.EOF):
			return validationErr("request body is required")
		default:
			return validationErr("malformed JSON: " + err.Error())
		}
	}
	if dec.More() {
		return validationErr("request body must contain a single JSON object")
	}
	return nil
}

func parseUUID(v *validationError, field, raw string) uuid.UUID {
	if raw == "" {
		v.add(field, "is required")
		return uuid.Nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		v.add(field, "must be a UUID")
		return uuid.Nil
	}
	return id
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, validationErr(fmt.Sprintf("%s must be a UUID", name))
	}
	return id, nil
}

// cardInput is the request shape for a card. Only the shape is validated here; the
// mock provider decides the outcome from the number.
type cardInput struct {
	Number   string `json:"number"`
	ExpMonth int    `json:"exp_month"`
	ExpYear  int    `json:"exp_year"`
	CVC      string `json:"cvc"`
}

func validateCard(v *validationError, c cardInput, now time.Time) payments.Card {
	card := payments.Normalize(payments.Card{Number: c.Number, ExpMonth: c.ExpMonth, ExpYear: c.ExpYear, CVC: c.CVC})
	v.addAll(payments.Validate(card, now))
	return card
}
