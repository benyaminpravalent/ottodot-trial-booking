// Package seed loads the fixed synthetic dataset used by the demo, the README and the tests.
// It is idempotent: it truncates every table and re-inserts. All IDs are fixed so they
// can be referenced from documentation.
package seed

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixed IDs. Pattern: a… parents, b… students, c… classes, d… bookings, e… payment attempts.
var (
	ParentPriya  = uuid.MustParse("a0000000-0000-4000-8000-000000000001")
	ParentMarcus = uuid.MustParse("a0000000-0000-4000-8000-000000000002")
	ParentSofia  = uuid.MustParse("a0000000-0000-4000-8000-000000000003")

	StudentAarav = uuid.MustParse("b0000000-0000-4000-8000-000000000001") // Priya, Grade 4
	StudentDiya  = uuid.MustParse("b0000000-0000-4000-8000-000000000002") // Priya, Grade 6
	StudentEthan = uuid.MustParse("b0000000-0000-4000-8000-000000000003") // Marcus, Grade 5
	StudentMia   = uuid.MustParse("b0000000-0000-4000-8000-000000000004") // Sofia, Grade 3
	StudentNoah  = uuid.MustParse("b0000000-0000-4000-8000-000000000005") // Sofia, Grade 7

	ClassC1 = uuid.MustParse("c0000000-0000-4000-8000-000000000001") // Science: Why Do Volcanoes Erupt? — 1 confirmed
	ClassC2 = uuid.MustParse("c0000000-0000-4000-8000-000000000002") // Math: Fractions with Pizza — 3 confirmed (last seat)
	ClassC3 = uuid.MustParse("c0000000-0000-4000-8000-000000000003") // Science: The Water Cycle — duplicate case (Diya)
	ClassC4 = uuid.MustParse("c0000000-0000-4000-8000-000000000004") // Math: Shapes All Around Us — payment failure case

	BookingC1Aarav       = uuid.MustParse("d0000000-0000-4000-8000-000000000001")
	BookingC2Ethan       = uuid.MustParse("d0000000-0000-4000-8000-000000000002")
	BookingC2Mia         = uuid.MustParse("d0000000-0000-4000-8000-000000000003")
	BookingC2Noah        = uuid.MustParse("d0000000-0000-4000-8000-000000000004")
	BookingC3Diya        = uuid.MustParse("d0000000-0000-4000-8000-000000000005")
	BookingC4EthanFailed = uuid.MustParse("d0000000-0000-4000-8000-000000000006")
)

// SeededConfirmedC2 are the three confirmed bookings that leave exactly one seat in C2.
var SeededConfirmedC2 = []uuid.UUID{BookingC2Ethan, BookingC2Mia, BookingC2Noah}

// Summary is what Run reports.
type Summary struct {
	Parents, Students, Classes, Bookings, PaymentAttempts int
}

// Truncate empties every domain table. Used by Run and by the test harness.
func Truncate(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `TRUNCATE payment_attempts, bookings, students, trial_classes, parents CASCADE`)
	return err
}

// Run truncates and reloads the dataset. Class start times are relative to now so the
// data is always "in the future" regardless of when the seed is run.
func Run(ctx context.Context, pool *pgxpool.Pool, now time.Time) (Summary, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Summary{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := Truncate(ctx, tx); err != nil {
		return Summary{}, fmt.Errorf("truncate: %w", err)
	}

	// Parents (3)
	if _, err := tx.Exec(ctx, `
		INSERT INTO parents (id, name, email) VALUES
		($1, 'Priya Nair',  'priya.nair@example.com'),
		($2, 'Marcus Tan',  'marcus.tan@example.com'),
		($3, 'Sofia Lim',   'sofia.lim@example.com')`,
		ParentPriya, ParentMarcus, ParentSofia); err != nil {
		return Summary{}, fmt.Errorf("parents: %w", err)
	}

	// Students (5)
	if _, err := tx.Exec(ctx, `
		INSERT INTO students (id, parent_id, name, grade) VALUES
		($1, $6, 'Aarav', 4),
		($2, $6, 'Diya',  6),
		($3, $7, 'Ethan', 5),
		($4, $8, 'Mia',   3),
		($5, $8, 'Noah',  7)`,
		StudentAarav, StudentDiya, StudentEthan, StudentMia, StudentNoah,
		ParentPriya, ParentMarcus, ParentSofia); err != nil {
		return Summary{}, fmt.Errorf("students: %w", err)
	}

	// Trial classes (4), all in the future.
	day := func(d int, hour int) time.Time {
		t := now.UTC().AddDate(0, 0, d)
		return time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, time.UTC)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trial_classes (id, subject, title, teacher_name, starts_at, capacity, price_cents, currency) VALUES
		($1, 'Science', 'Science: Why Do Volcanoes Erupt?', 'Ms. Chen',    $5, 4, 2000, 'SGD'),
		($2, 'Math',    'Math: Fractions with Pizza',        'Mr. Kumar',   $6, 4, 2000, 'SGD'),
		($3, 'Science', 'Science: The Water Cycle',          'Ms. Chen',    $7, 4, 1500, 'SGD'),
		($4, 'Math',    'Math: Shapes All Around Us',        'Mrs. Ortega', $8, 4, 1500, 'SGD')`,
		ClassC1, ClassC2, ClassC3, ClassC4,
		day(2, 9), day(3, 10), day(4, 9), day(5, 10)); err != nil {
		return Summary{}, fmt.Errorf("classes: %w", err)
	}

	// Confirmed bookings, each with one succeeded payment attempt.
	type confirmed struct {
		bookingID, classID, studentID, parentID uuid.UUID
		priceCents                              int
		seq                                     int
	}
	confirmedRows := []confirmed{
		{BookingC1Aarav, ClassC1, StudentAarav, ParentPriya, 2000, 1},
		{BookingC2Ethan, ClassC2, StudentEthan, ParentMarcus, 2000, 2},
		{BookingC2Mia, ClassC2, StudentMia, ParentSofia, 2000, 3},
		{BookingC2Noah, ClassC2, StudentNoah, ParentSofia, 2000, 4},
		{BookingC3Diya, ClassC3, StudentDiya, ParentPriya, 1500, 5},
	}
	attempts := 0
	for _, c := range confirmedRows {
		confirmedAt := now.Add(-time.Duration(48-c.seq) * time.Hour) // staggered so roster order is stable
		if _, err := tx.Exec(ctx, `
			INSERT INTO bookings (id, trial_class_id, student_id, parent_id, status, created_at, updated_at, confirmed_at)
			VALUES ($1, $2, $3, $4, 'confirmed', $5, $5, $5)`,
			c.bookingID, c.classID, c.studentID, c.parentID, confirmedAt); err != nil {
			return Summary{}, fmt.Errorf("confirmed booking %d: %w", c.seq, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_attempts (id, booking_id, amount_cents, currency, provider, provider_ref, status, created_at)
			VALUES ($1, $2, $3, 'SGD', 'mock', $4, 'succeeded', $5)`,
			attemptID(c.seq), c.bookingID, c.priceCents, fmt.Sprintf("mock_pay_seed%02d", c.seq), confirmedAt); err != nil {
			return Summary{}, fmt.Errorf("payment attempt %d: %w", c.seq, err)
		}
		attempts++
	}

	// C4: Ethan's card was declined. Booking is payment_failed with a failed attempt;
	// the roster for C4 must show 0 confirmed.
	failedAt := now.Add(-2 * time.Hour)
	if _, err := tx.Exec(ctx, `
		INSERT INTO bookings (id, trial_class_id, student_id, parent_id, status, status_reason, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'payment_failed', 'payment_declined', $5, $5)`,
		BookingC4EthanFailed, ClassC4, StudentEthan, ParentMarcus, failedAt); err != nil {
		return Summary{}, fmt.Errorf("failed booking: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_attempts (id, booking_id, amount_cents, currency, provider, provider_ref, status, failure_code, created_at)
		VALUES ($1, $2, 1500, 'SGD', 'mock', 'mock_fail_seed06', 'failed', 'card_declined', $3)`,
		attemptID(6), BookingC4EthanFailed, failedAt); err != nil {
		return Summary{}, fmt.Errorf("failed attempt: %w", err)
	}
	attempts++

	if err := tx.Commit(ctx); err != nil {
		return Summary{}, err
	}
	return Summary{Parents: 3, Students: 5, Classes: 4, Bookings: 6, PaymentAttempts: attempts}, nil
}

func attemptID(seq int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("e0000000-0000-4000-8000-%012d", seq))
}
