// Package httpapi exposes the booking service as a JSON API.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"ottodot/internal/booking"
)

// API holds the handlers' dependencies.
type API struct {
	svc    *booking.Service
	logger *slog.Logger
	now    func() time.Time
}

// New creates the API.
func New(svc *booking.Service, logger *slog.Logger) *API {
	return &API{svc: svc, logger: logger, now: time.Now}
}

// Register mounts every /api route on mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/parents", a.listParents)
	mux.HandleFunc("GET /api/parents/{parentId}/students", a.listStudents)
	mux.HandleFunc("GET /api/trial-classes", a.listClasses)
	mux.HandleFunc("GET /api/trial-classes/{id}/roster", a.roster)
	mux.HandleFunc("POST /api/bookings", a.createBooking)
	mux.HandleFunc("GET /api/bookings/{id}", a.getBooking)
	mux.HandleFunc("POST /api/bookings/{id}/pay", a.payBooking)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

func (a *API) listParents(w http.ResponseWriter, r *http.Request) {
	parents, err := a.svc.ListParents(r.Context())
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"parents": parents})
}

func (a *API) listStudents(w http.ResponseWriter, r *http.Request) {
	parentID, err := pathUUID(r, "parentId")
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	students, err := a.svc.ListStudents(r.Context(), parentID)
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"students": students})
}

func (a *API) listClasses(w http.ResponseWriter, r *http.Request) {
	classes, err := a.svc.ListClasses(r.Context(), true)
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trial_classes": classes})
}

func (a *API) roster(w http.ResponseWriter, r *http.Request) {
	classID, err := pathUUID(r, "id")
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	roster, err := a.svc.GetRoster(r.Context(), classID)
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, roster)
}

type createBookingRequest struct {
	ParentID     string `json:"parent_id"`
	StudentID    string `json:"student_id"`
	TrialClassID string `json:"trial_class_id"`
}

func (a *API) createBooking(w http.ResponseWriter, r *http.Request) {
	var req createBookingRequest
	if err := decodeJSON(r, &req); err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	var v validationError
	in := booking.CreateBookingInput{
		ParentID:     parseUUID(&v, "parent_id", req.ParentID),
		StudentID:    parseUUID(&v, "student_id", req.StudentID),
		TrialClassID: parseUUID(&v, "trial_class_id", req.TrialClassID),
	}
	if err := v.err(); err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	res, err := a.svc.CreateBooking(r.Context(), in)
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (a *API) getBooking(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	detail, err := a.svc.GetBookingDetail(r.Context(), id)
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type payRequest struct {
	ParentID string    `json:"parent_id"`
	Card     cardInput `json:"card"`
}

// payBooking runs the critical path. All three business outcomes (confirmed,
// payment_failed, cancelled) are returned as 200 with an explicit `outcome`: the request
// itself was processed and a result recorded. Protocol/authorisation problems (booking
// not pending, wrong parent, bad body) are 4xx with the standard error body.
func (a *API) payBooking(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	var req payRequest
	if err := decodeJSON(r, &req); err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	var v validationError
	parentID := parseUUID(&v, "parent_id", req.ParentID)
	card := validateCard(&v, req.Card, a.now())
	if err := v.err(); err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	res, err := a.svc.PayForBooking(r.Context(), booking.PayInput{BookingID: id, ParentID: parentID, Card: card})
	if err != nil {
		writeServiceError(a.logger, w, r, err)
		return
	}
	a.logger.Info("payment settled",
		"request_id", RequestID(r.Context()),
		"booking_id", id.String(),
		"outcome", string(res.Outcome),
		"reason", res.Reason,
		"failure_code", res.FailureCode,
	)
	writeJSON(w, http.StatusOK, res)
}
