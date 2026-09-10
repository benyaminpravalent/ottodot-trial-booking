package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"ottodot/internal/booking"
	"ottodot/internal/payments"
	"ottodot/internal/seed"
	"ottodot/internal/testutil"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithDatabase(m, "httpapi", func(p *pgxpool.Pool) { pool = p }))
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	testutil.Seed(t, pool)
	svc := booking.NewService(pool, payments.NewMock())
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mux := http.NewServeMux()
	New(svc, logger).Register(mux)
	srv := httptest.NewServer(WithRequestLogging(logger, mux))
	t.Cleanup(srv.Close)
	return srv
}

func postJSON(t *testing.T, url, body string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp, out
}

func getJSON(t *testing.T, url string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp, out
}

func errorCode(t *testing.T, body map[string]any) string {
	t.Helper()
	e, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error body, got %v", body)
	}
	code, _ := e["code"].(string)
	return code
}

func TestAPI(t *testing.T) {
	t.Run("14 POST /api/bookings validates the body and returns 400 on bad input", func(t *testing.T) {
		srv := newServer(t)
		cases := map[string]string{
			"empty body":       ``,
			"malformed JSON":   `{"parent_id": `,
			"missing fields":   `{"parent_id":"` + seed.ParentPriya.String() + `"}`,
			"non-UUID id":      `{"parent_id":"priya","student_id":"aarav","trial_class_id":"c1"}`,
			"unknown field":    `{"parent_id":"` + seed.ParentPriya.String() + `","student_id":"` + seed.StudentAarav.String() + `","trial_class_id":"` + seed.ClassC2.String() + `","seats_taken":1}`,
			"two JSON objects": `{} {}`,
		}
		for name, body := range cases {
			resp, out := postJSON(t, srv.URL+"/api/bookings", body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400 (%v)", name, resp.StatusCode, out)
				continue
			}
			if code := errorCode(t, out); code != booking.CodeValidation {
				t.Errorf("%s: error code = %s, want validation_error", name, code)
			}
			if resp.Header.Get("X-Request-Id") == "" {
				t.Errorf("%s: missing X-Request-Id echo", name)
			}
		}

		// Business errors keep the same envelope with their own status.
		resp, out := postJSON(t, srv.URL+"/api/bookings",
			`{"parent_id":"`+seed.ParentMarcus.String()+`","student_id":"`+seed.StudentAarav.String()+`","trial_class_id":"`+seed.ClassC1.String()+`"}`)
		if resp.StatusCode != http.StatusForbidden || errorCode(t, out) != booking.CodeForbidden {
			t.Errorf("wrong-parent booking: %d %v, want 403 forbidden", resp.StatusCode, out)
		}
		resp, out = postJSON(t, srv.URL+"/api/bookings",
			`{"parent_id":"`+seed.ParentPriya.String()+`","student_id":"`+seed.StudentDiya.String()+`","trial_class_id":"`+seed.ClassC3.String()+`"}`)
		if resp.StatusCode != http.StatusConflict || errorCode(t, out) != booking.CodeDuplicateConfirmed {
			t.Errorf("duplicate booking: %d %v, want 409 duplicate_confirmed", resp.StatusCode, out)
		}
	})

	t.Run("15 POST /api/bookings/:id/pay end-to-end happy path returns confirmed", func(t *testing.T) {
		srv := newServer(t)

		// Discover data the way a client would.
		resp, classes := getJSON(t, srv.URL+"/api/trial-classes")
		if resp.StatusCode != http.StatusOK || len(classes["trial_classes"].([]any)) != 4 {
			t.Fatalf("list classes: %d %v", resp.StatusCode, classes)
		}
		resp, students := getJSON(t, srv.URL+"/api/parents/"+seed.ParentPriya.String()+"/students")
		if resp.StatusCode != http.StatusOK || len(students["students"].([]any)) != 2 {
			t.Fatalf("list students: %d %v", resp.StatusCode, students)
		}

		// Create → 201 pending_payment with amount due.
		resp, created := postJSON(t, srv.URL+"/api/bookings",
			`{"parent_id":"`+seed.ParentPriya.String()+`","student_id":"`+seed.StudentDiya.String()+`","trial_class_id":"`+seed.ClassC1.String()+`"}`)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create: %d %v", resp.StatusCode, created)
		}
		bk := created["booking"].(map[string]any)
		if bk["status"] != "pending_payment" || created["amount_cents"].(float64) != 2000 {
			t.Fatalf("create body = %v", created)
		}
		bookingID := bk["id"].(string)
		req := strings.NewReplacer("{parent}", seed.ParentPriya.String())

		// Card validation happens at the boundary before any charge.
		resp, bad := postJSON(t, srv.URL+"/api/bookings/"+bookingID+"/pay",
			req.Replace(`{"parent_id":"{parent}","card":{"number":"42","exp_month":13,"exp_year":2019,"cvc":"1"}}`))
		if resp.StatusCode != http.StatusBadRequest || errorCode(t, bad) != booking.CodeValidation {
			t.Fatalf("bad card: %d %v, want 400 validation_error", resp.StatusCode, bad)
		}

		// Pay → 200 outcome=confirmed.
		resp, paid := postJSON(t, srv.URL+"/api/bookings/"+bookingID+"/pay",
			req.Replace(`{"parent_id":"{parent}","card":{"number":"4242 4242 4242 4242","exp_month":12,"exp_year":2030,"cvc":"123"}}`))
		if resp.StatusCode != http.StatusOK || paid["outcome"] != "confirmed" {
			t.Fatalf("pay: %d %v", resp.StatusCode, paid)
		}

		// Status endpoint reflects it, with exactly one succeeded attempt.
		resp, detail := getJSON(t, srv.URL+"/api/bookings/"+bookingID)
		if resp.StatusCode != http.StatusOK || detail["booking"].(map[string]any)["status"] != "confirmed" {
			t.Fatalf("status: %d %v", resp.StatusCode, detail)
		}
		if attempts := detail["payment_attempts"].([]any); len(attempts) != 1 || attempts[0].(map[string]any)["status"] != "succeeded" {
			t.Errorf("attempts = %v, want one succeeded", attempts)
		}

		// Paying again → 409 booking_not_pending.
		resp, again := postJSON(t, srv.URL+"/api/bookings/"+bookingID+"/pay",
			req.Replace(`{"parent_id":"{parent}","card":{"number":"4242424242424242","exp_month":12,"exp_year":2030,"cvc":"123"}}`))
		if resp.StatusCode != http.StatusConflict || errorCode(t, again) != booking.CodeBookingNotPending {
			t.Errorf("pay again: %d %v, want 409 booking_not_pending", resp.StatusCode, again)
		}

		// Roster shows Diya on C1 with the seat count derived.
		resp, roster := getJSON(t, srv.URL+"/api/trial-classes/"+seed.ClassC1.String()+"/roster")
		if resp.StatusCode != http.StatusOK || roster["confirmed_count"].(float64) != 2 || roster["seats_left"].(float64) != 2 {
			t.Fatalf("roster: %d %v", resp.StatusCode, roster)
		}
	})

	t.Run("15b POST /api/bookings/:id/pay with a declined card returns outcome=payment_failed and leaves the roster unchanged", func(t *testing.T) {
		srv := newServer(t)
		resp, created := postJSON(t, srv.URL+"/api/bookings",
			`{"parent_id":"`+seed.ParentMarcus.String()+`","student_id":"`+seed.StudentEthan.String()+`","trial_class_id":"`+seed.ClassC4.String()+`"}`)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create: %d %v", resp.StatusCode, created)
		}
		bookingID := created["booking"].(map[string]any)["id"].(string)
		resp, paid := postJSON(t, srv.URL+"/api/bookings/"+bookingID+"/pay",
			`{"parent_id":"`+seed.ParentMarcus.String()+`","card":{"number":"4000000000000002","exp_month":12,"exp_year":2030,"cvc":"123"}}`)
		if resp.StatusCode != http.StatusOK || paid["outcome"] != "payment_failed" || paid["failure_code"] != "card_declined" {
			t.Fatalf("pay: %d %v", resp.StatusCode, paid)
		}
		_, roster := getJSON(t, srv.URL+"/api/trial-classes/"+seed.ClassC4.String()+"/roster")
		if roster["confirmed_count"].(float64) != 0 || len(roster["confirmed"].([]any)) != 0 {
			t.Errorf("roster after failed payment = %v, want empty", roster)
		}
	})
}
