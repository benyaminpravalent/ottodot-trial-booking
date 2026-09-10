package booking

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// bookingColumns is the canonical column list for scanBooking. Keep in sync.
const bookingColumns = `id, trial_class_id, student_id, parent_id, status, status_reason, created_at, updated_at, confirmed_at`

func scanBooking(row pgx.Row) (Booking, error) {
	var (
		b      Booking
		status string
	)
	err := row.Scan(&b.ID, &b.TrialClassID, &b.StudentID, &b.ParentID, &status, &b.StatusReason,
		&b.CreatedAt, &b.UpdatedAt, &b.ConfirmedAt)
	if err != nil {
		return b, err
	}
	b.Status = Status(status)
	return b, nil
}

func (s *Service) getBooking(ctx context.Context, id uuid.UUID) (Booking, error) {
	bk, err := scanBooking(s.pool.QueryRow(ctx, `SELECT `+bookingColumns+` FROM bookings WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return bk, errNotFound("booking not found")
	}
	if err != nil {
		return bk, fmt.Errorf("load booking: %w", err)
	}
	return bk, nil
}

// ListParents returns the seeded parents for the "log in as" dropdown.
func (s *Service) ListParents(ctx context.Context) ([]Parent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, email FROM parents ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list parents: %w", err)
	}
	defer rows.Close()
	out := []Parent{}
	for rows.Next() {
		var p Parent
		if err := rows.Scan(&p.ID, &p.Name, &p.Email); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListStudents returns a parent's children. Unknown parent → not_found.
func (s *Service) ListStudents(ctx context.Context, parentID uuid.UUID) ([]Student, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM parents WHERE id = $1)`, parentID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check parent: %w", err)
	}
	if !exists {
		return nil, errNotFound("parent not found")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, parent_id, name, grade FROM students WHERE parent_id = $1 ORDER BY name`, parentID)
	if err != nil {
		return nil, fmt.Errorf("list students: %w", err)
	}
	defer rows.Close()
	out := []Student{}
	for rows.Next() {
		var st Student
		if err := rows.Scan(&st.ID, &st.ParentID, &st.Name, &st.Grade); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

const classSummarySelect = `
	SELECT c.id, c.subject, c.title, c.teacher_name, c.starts_at, c.capacity, c.price_cents, c.currency,
	       count(b.id) FILTER (WHERE b.status = 'confirmed') AS confirmed_count
	FROM trial_classes c
	LEFT JOIN bookings b ON b.trial_class_id = c.id`

func scanClassSummary(row pgx.Row) (ClassSummary, error) {
	var c ClassSummary
	err := row.Scan(&c.ID, &c.Subject, &c.Title, &c.TeacherName, &c.StartsAt, &c.Capacity,
		&c.PriceCents, &c.Currency, &c.ConfirmedCount)
	if err != nil {
		return c, err
	}
	c.SeatsLeft = max(c.Capacity-c.ConfirmedCount, 0)
	c.IsFull = c.ConfirmedCount >= c.Capacity
	return c, nil
}

// ListClasses returns classes with seat figures derived in SQL from confirmed bookings.
// Full classes are still returned (flagged is_full) so the UI can show them disabled;
// that UI check is advisory only. upcomingOnly=false is for the admin page.
func (s *Service) ListClasses(ctx context.Context, upcomingOnly bool) ([]ClassSummary, error) {
	where := ""
	if upcomingOnly {
		where = " WHERE c.starts_at > now()"
	}
	rows, err := s.pool.Query(ctx, classSummarySelect+where+` GROUP BY c.id ORDER BY c.starts_at, c.title`)
	if err != nil {
		return nil, fmt.Errorf("list classes: %w", err)
	}
	defer rows.Close()
	out := []ClassSummary{}
	for rows.Next() {
		c, err := scanClassSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetRoster returns the admin/teacher view: confirmed students only, plus a status breakdown.
func (s *Service) GetRoster(ctx context.Context, classID uuid.UUID) (*Roster, error) {
	class, err := scanClassSummary(s.pool.QueryRow(ctx, classSummarySelect+` WHERE c.id = $1 GROUP BY c.id`, classID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound("trial class not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load class: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT b.id, s.name, s.grade, p.name, p.email, b.confirmed_at
		FROM bookings b
		JOIN students s ON s.id = b.student_id
		JOIN parents  p ON p.id = b.parent_id
		WHERE b.trial_class_id = $1 AND b.status = 'confirmed'
		ORDER BY b.confirmed_at`, classID)
	if err != nil {
		return nil, fmt.Errorf("load roster: %w", err)
	}
	defer rows.Close()
	confirmed := []RosterEntry{}
	for rows.Next() {
		var e RosterEntry
		if err := rows.Scan(&e.BookingID, &e.StudentName, &e.Grade, &e.ParentName, &e.ParentEmail, &e.ConfirmedAt); err != nil {
			return nil, err
		}
		confirmed = append(confirmed, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	breakdown := map[Status]int{StatusPendingPayment: 0, StatusConfirmed: 0, StatusPaymentFailed: 0, StatusCancelled: 0}
	brows, err := s.pool.Query(ctx, `
		SELECT status::text, count(*) FROM bookings WHERE trial_class_id = $1 GROUP BY status`, classID)
	if err != nil {
		return nil, fmt.Errorf("load breakdown: %w", err)
	}
	defer brows.Close()
	for brows.Next() {
		var (
			st string
			n  int
		)
		if err := brows.Scan(&st, &n); err != nil {
			return nil, err
		}
		breakdown[Status(st)] = n
	}
	if err := brows.Err(); err != nil {
		return nil, err
	}

	return &Roster{
		Class:          class,
		Capacity:       class.Capacity,
		ConfirmedCount: class.ConfirmedCount,
		SeatsLeft:      class.SeatsLeft,
		Confirmed:      confirmed,
		Breakdown:      breakdown,
	}, nil
}

// GetBookingDetail returns everything the status page needs, including payment history.
func (s *Service) GetBookingDetail(ctx context.Context, id uuid.UUID) (*BookingDetail, error) {
	var (
		d      BookingDetail
		status string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT b.id, b.trial_class_id, b.student_id, b.parent_id, b.status, b.status_reason,
		       b.created_at, b.updated_at, b.confirmed_at,
		       c.title, c.starts_at, c.price_cents, c.currency, s.name, p.name
		FROM bookings b
		JOIN trial_classes c ON c.id = b.trial_class_id
		JOIN students s ON s.id = b.student_id
		JOIN parents  p ON p.id = b.parent_id
		WHERE b.id = $1`, id).
		Scan(&d.Booking.ID, &d.Booking.TrialClassID, &d.Booking.StudentID, &d.Booking.ParentID, &status,
			&d.Booking.StatusReason, &d.Booking.CreatedAt, &d.Booking.UpdatedAt, &d.Booking.ConfirmedAt,
			&d.ClassTitle, &d.StartsAt, &d.AmountCents, &d.Currency, &d.StudentName, &d.ParentName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound("booking not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load booking detail: %w", err)
	}
	d.Booking.Status = Status(status)

	rows, err := s.pool.Query(ctx, `
		SELECT id, booking_id, amount_cents, currency, provider, provider_ref, status, failure_code, refund_ref, created_at
		FROM payment_attempts WHERE booking_id = $1 ORDER BY created_at`, id)
	if err != nil {
		return nil, fmt.Errorf("load payment attempts: %w", err)
	}
	defer rows.Close()
	d.Attempts = []PaymentAttempt{}
	lastFailure := ""
	for rows.Next() {
		var a PaymentAttempt
		if err := rows.Scan(&a.ID, &a.BookingID, &a.AmountCents, &a.Currency, &a.Provider, &a.ProviderRef,
			&a.Status, &a.FailureCode, &a.RefundRef, &a.CreatedAt); err != nil {
			return nil, err
		}
		if a.FailureCode != nil {
			lastFailure = *a.FailureCode
		}
		d.Attempts = append(d.Attempts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	d.Message = StatusMessage(d.Booking.Status, d.Booking.StatusReason, lastFailure)
	return &d, nil
}
