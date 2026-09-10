// Package web serves the minimal server-rendered HTML pages. Plain forms, no JavaScript,
// one stylesheet. Every page calls the same booking.Service the JSON API uses.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"ottodot/internal/booking"
	"ottodot/internal/httpapi"
	"ottodot/internal/payments"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Pages holds the handlers' dependencies.
type Pages struct {
	svc    *booking.Service
	logger *slog.Logger
	tmpl   map[string]*template.Template
}

// New parses the embedded templates.
func New(svc *booking.Service, logger *slog.Logger) (*Pages, error) {
	funcs := template.FuncMap{
		"money": func(cents int, currency string) string {
			return fmt.Sprintf("%s %d.%02d", strings.TrimSpace(currency), cents/100, cents%100)
		},
		"datetime": func(t time.Time) string { return t.Local().Format("Mon 2 Jan 2006, 15:04 MST") },
		"label":    statusLabel,
	}
	layout, err := template.New("layout").Funcs(funcs).ParseFS(assets, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	pages := []string{"index", "pay", "status", "admin_classes", "roster"}
	tmpl := make(map[string]*template.Template, len(pages))
	for _, name := range pages {
		t, err := template.Must(layout.Clone()).ParseFS(assets, "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		tmpl[name] = t
	}
	return &Pages{svc: svc, logger: logger, tmpl: tmpl}, nil
}

// Register mounts the pages and the stylesheet.
func (p *Pages) Register(mux *http.ServeMux) {
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	mux.HandleFunc("GET /{$}", p.index)
	mux.HandleFunc("POST /bookings", p.createBooking)
	mux.HandleFunc("GET /bookings/{id}/pay", p.payForm)
	mux.HandleFunc("POST /bookings/{id}/pay", p.paySubmit)
	mux.HandleFunc("GET /bookings/{id}", p.status)
	mux.HandleFunc("GET /admin/classes", p.adminClasses)
	mux.HandleFunc("GET /admin/classes/{id}", p.roster)
}

func (p *Pages) render(w http.ResponseWriter, r *http.Request, name string, status int, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := p.tmpl[name].ExecuteTemplate(w, "layout", data); err != nil {
		p.logger.Error("render", "template", name, "request_id", httpapi.RequestID(r.Context()), "err", err.Error())
	}
}

// fail renders errors the same way for every page: business errors with their status
// and message, everything else as a generic 500.
func (p *Pages) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusInternalServerError, "Something went wrong. Please try again."
	if be, ok := booking.AsError(err); ok {
		status, msg = be.Status, be.Message
	} else {
		p.logger.Error("page error", "path", r.URL.Path, "request_id", httpapi.RequestID(r.Context()), "err", err.Error())
	}
	http.Error(w, msg, status)
}

// --- Book a trial ------------------------------------------------------------

type indexData struct {
	Parents         []booking.Parent
	Parent          *booking.Parent
	Students        []booking.Student
	SelectedStudent string
	Classes         []booking.ClassSummary
	Error           string
}

func (p *Pages) index(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := indexData{SelectedStudent: r.URL.Query().Get("student"), Error: r.URL.Query().Get("error")}

	parents, err := p.svc.ListParents(ctx)
	if err != nil {
		p.fail(w, r, err)
		return
	}
	data.Parents = parents

	if raw := r.URL.Query().Get("parent"); raw != "" {
		parentID, err := uuid.Parse(raw)
		if err != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		for i := range parents {
			if parents[i].ID == parentID {
				data.Parent = &parents[i]
			}
		}
		if data.Parent == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if data.Students, err = p.svc.ListStudents(ctx, parentID); err != nil {
			p.fail(w, r, err)
			return
		}
		if data.Classes, err = p.svc.ListClasses(ctx, true); err != nil {
			p.fail(w, r, err)
			return
		}
	}
	p.render(w, r, "index", http.StatusOK, data)
}

func (p *Pages) createBooking(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	parentID, err1 := uuid.Parse(r.PostForm.Get("parent_id"))
	studentID, err2 := uuid.Parse(r.PostForm.Get("student_id"))
	classID, err3 := uuid.Parse(r.PostForm.Get("trial_class_id"))
	back := "/?parent=" + url.QueryEscape(r.PostForm.Get("parent_id")) + "&student=" + url.QueryEscape(r.PostForm.Get("student_id"))
	if err1 != nil || err2 != nil || err3 != nil {
		http.Redirect(w, r, back+"&error="+url.QueryEscape("Please pick a child and a class."), http.StatusSeeOther)
		return
	}

	res, err := p.svc.CreateBooking(r.Context(), booking.CreateBookingInput{ParentID: parentID, StudentID: studentID, TrialClassID: classID})
	if err != nil {
		if be, ok := booking.AsError(err); ok {
			http.Redirect(w, r, back+"&error="+url.QueryEscape(be.Message), http.StatusSeeOther)
			return
		}
		p.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/bookings/%s/pay?parent=%s", res.Booking.ID, parentID), http.StatusSeeOther)
}

// --- Payment -----------------------------------------------------------------

type payData struct {
	Detail   *booking.BookingDetail
	ParentID string
	Card     payments.Card
	Errors   []string
	Cards    map[string]string
}

var testCards = map[string]string{
	payments.CardSuccess:           "succeeds",
	payments.CardDeclined:          "fails: card_declined",
	payments.CardInsufficientFunds: "fails: insufficient_funds",
}

func (p *Pages) payForm(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	detail, err := p.svc.GetBookingDetail(r.Context(), id)
	if err != nil {
		p.fail(w, r, err)
		return
	}
	if detail.Booking.Status != booking.StatusPendingPayment {
		http.Redirect(w, r, "/bookings/"+id.String(), http.StatusSeeOther)
		return
	}
	parent := r.URL.Query().Get("parent")
	if parent == "" {
		parent = detail.Booking.ParentID.String()
	}
	p.render(w, r, "pay", http.StatusOK, payData{
		Detail:   detail,
		ParentID: parent,
		Card:     payments.Card{Number: payments.CardSuccess, ExpMonth: 12, ExpYear: 2030, CVC: "123"},
		Cards:    testCards,
	})
}

func (p *Pages) paySubmit(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	parentID, err := uuid.Parse(r.PostForm.Get("parent_id"))
	if err != nil {
		http.Error(w, "parent_id must be a UUID", http.StatusBadRequest)
		return
	}
	expMonth, _ := strconv.Atoi(r.PostForm.Get("exp_month"))
	expYear, _ := strconv.Atoi(r.PostForm.Get("exp_year"))
	card := payments.Normalize(payments.Card{
		Number:   r.PostForm.Get("number"),
		ExpMonth: expMonth,
		ExpYear:  expYear,
		CVC:      r.PostForm.Get("cvc"),
	})
	if problems := payments.Validate(card, time.Now()); len(problems) > 0 {
		detail, err := p.svc.GetBookingDetail(r.Context(), id)
		if err != nil {
			p.fail(w, r, err)
			return
		}
		p.render(w, r, "pay", http.StatusBadRequest, payData{
			Detail: detail, ParentID: parentID.String(), Card: card, Errors: problems, Cards: testCards,
		})
		return
	}

	res, err := p.svc.PayForBooking(r.Context(), booking.PayInput{BookingID: id, ParentID: parentID, Card: card})
	if err != nil {
		if be, ok := booking.AsError(err); ok {
			http.Redirect(w, r, "/bookings/"+id.String()+"?error="+url.QueryEscape(be.Message), http.StatusSeeOther)
			return
		}
		p.fail(w, r, err)
		return
	}
	p.logger.Info("payment settled", "request_id", httpapi.RequestID(r.Context()), "booking_id", id.String(),
		"outcome", string(res.Outcome), "reason", res.Reason, "failure_code", res.FailureCode, "via", "form")
	http.Redirect(w, r, "/bookings/"+id.String(), http.StatusSeeOther)
}

// --- Status ------------------------------------------------------------------

type statusData struct {
	Detail *booking.BookingDetail
	Error  string
}

func (p *Pages) status(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	detail, err := p.svc.GetBookingDetail(r.Context(), id)
	if err != nil {
		p.fail(w, r, err)
		return
	}
	p.render(w, r, "status", http.StatusOK, statusData{Detail: detail, Error: r.URL.Query().Get("error")})
}

// --- Admin -------------------------------------------------------------------

func (p *Pages) adminClasses(w http.ResponseWriter, r *http.Request) {
	classes, err := p.svc.ListClasses(r.Context(), false)
	if err != nil {
		p.fail(w, r, err)
		return
	}
	p.render(w, r, "admin_classes", http.StatusOK, classes)
}

func (p *Pages) roster(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	roster, err := p.svc.GetRoster(r.Context(), id)
	if err != nil {
		p.fail(w, r, err)
		return
	}
	p.render(w, r, "roster", http.StatusOK, roster)
}

func statusLabel(s booking.Status) string {
	switch s {
	case booking.StatusPendingPayment:
		return "Pending payment"
	case booking.StatusConfirmed:
		return "Confirmed"
	case booking.StatusPaymentFailed:
		return "Payment failed"
	case booking.StatusCancelled:
		return "Cancelled"
	}
	return string(s)
}
