package booking

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"ottodot/internal/payments"
	"ottodot/internal/seed"
	"ottodot/internal/testutil"
)

// Every test in this package runs against a real, migrated Postgres database and starts
// from the fixed seed dataset (see internal/seed). Nothing is mocked except the payment
// provider, which is the in-process mock the app itself uses.
var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithDatabase(m, "booking", func(p *pgxpool.Pool) { pool = p }))
}

// --- fixtures ----------------------------------------------------------------

func newService(t *testing.T) *Service {
	t.Helper()
	testutil.Seed(t, pool)
	return NewService(pool, payments.NewMock())
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func card(number string) payments.Card {
	return payments.Card{Number: number, ExpMonth: 12, ExpYear: 2030, CVC: "123"}
}

func mustCreate(t *testing.T, svc *Service, parent, student, class uuid.UUID) Booking {
	t.Helper()
	res, err := svc.CreateBooking(ctx(t), CreateBookingInput{ParentID: parent, StudentID: student, TrialClassID: class})
	if err != nil {
		t.Fatalf("CreateBooking(%s): %v", student, err)
	}
	if res.Booking.Status != StatusPendingPayment {
		t.Fatalf("new booking status = %s, want pending_payment", res.Booking.Status)
	}
	return res.Booking
}

func mustPay(t *testing.T, svc *Service, bk Booking, number string) *PayResult {
	t.Helper()
	res, err := svc.PayForBooking(ctx(t), PayInput{BookingID: bk.ID, ParentID: bk.ParentID, Card: card(number)})
	if err != nil {
		t.Fatalf("PayForBooking(%s): %v", bk.ID, err)
	}
	return res
}

func confirmedCount(t *testing.T, class uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx(t), `SELECT count(*) FROM bookings WHERE trial_class_id = $1 AND status = 'confirmed'`, class).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func bookingStatus(t *testing.T, id uuid.UUID) (Status, string) {
	t.Helper()
	var (
		st     string
		reason *string
	)
	if err := pool.QueryRow(ctx(t), `SELECT status, status_reason FROM bookings WHERE id = $1`, id).Scan(&st, &reason); err != nil {
		t.Fatal(err)
	}
	if reason == nil {
		return Status(st), ""
	}
	return Status(st), *reason
}

// attemptStatuses returns the payment_attempts statuses for a booking in creation order.
func attemptStatuses(t *testing.T, id uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(ctx(t), `SELECT status FROM payment_attempts WHERE booking_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// insertConfirmedDirect bypasses the service layer entirely and writes a confirmed row.
// Used to prove the database constraint holds on its own.
func insertConfirmedDirect(t *testing.T, class, student, parent uuid.UUID) error {
	t.Helper()
	_, err := pool.Exec(ctx(t), `
		INSERT INTO bookings (trial_class_id, student_id, parent_id, status, confirmed_at)
		VALUES ($1, $2, $3, 'confirmed', now())`, class, student, parent)
	return err
}

// insertPendingDirect writes a pending_payment row without CreateBooking's pre-checks.
func insertPendingDirect(t *testing.T, class, student, parent uuid.UUID) Booking {
	t.Helper()
	bk, err := scanBooking(pool.QueryRow(ctx(t), `
		INSERT INTO bookings (trial_class_id, student_id, parent_id, status)
		VALUES ($1, $2, $3, 'pending_payment') RETURNING `+bookingColumns, class, student, parent))
	if err != nil {
		t.Fatal(err)
	}
	return bk
}

// addStudents creates n extra children for a parent (the race stress test needs 10 payers).
func addStudents(t *testing.T, parent uuid.UUID, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		var id uuid.UUID
		if err := pool.QueryRow(ctx(t), `INSERT INTO students (parent_id, name, grade) VALUES ($1, $2, 5) RETURNING id`,
			parent, "Racer "+string(rune('A'+i))).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func assertBusinessError(t *testing.T, err error, wantCode string, wantStatus int) {
	t.Helper()
	be, ok := AsError(err)
	if !ok {
		t.Fatalf("expected business error %s, got %T: %v", wantCode, err, err)
	}
	if be.Code != wantCode || be.Status != wantStatus {
		t.Fatalf("error = %s (%d), want %s (%d)", be.Code, be.Status, wantCode, wantStatus)
	}
}

func assertPgCode(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != code {
		t.Fatalf("SQLSTATE = %s, want %s (%s)", pgErr.Code, code, pgErr.Message)
	}
	if constraint != "" && pgErr.ConstraintName != constraint {
		t.Fatalf("constraint = %q, want %q", pgErr.ConstraintName, constraint)
	}
}

// Seeded shorthands used across the tests.
var (
	priya, marcus, sofia = seed.ParentPriya, seed.ParentMarcus, seed.ParentSofia
	aarav, diya, ethan   = seed.StudentAarav, seed.StudentDiya, seed.StudentEthan
	mia                  = seed.StudentMia
	c1, c2, c3, c4       = seed.ClassC1, seed.ClassC2, seed.ClassC3, seed.ClassC4
)
