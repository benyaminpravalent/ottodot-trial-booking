package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"ottodot/internal/payments"
)

// Service implements the booking use cases against a pgx pool.
type Service struct {
	pool     *pgxpool.Pool
	payments payments.Provider

	// skipInTxDuplicateCheck is a test-only hook. When true, the in-transaction
	// "already confirmed for this student+class?" check is bypassed so the test can
	// prove that the partial unique index and the 23505 handler catch the duplicate.
	skipInTxDuplicateCheck bool
}

// NewService wires the service to its two dependencies.
func NewService(pool *pgxpool.Pool, provider payments.Provider) *Service {
	return &Service{pool: pool, payments: provider}
}

// CreateBookingInput identifies who is booking what.
type CreateBookingInput struct {
	ParentID     uuid.UUID
	StudentID    uuid.UUID
	TrialClassID uuid.UUID
}

// CreateBookingResult is the new pending booking plus the amount due.
type CreateBookingResult struct {
	Booking     Booking `json:"booking"`
	ClassTitle  string  `json:"class_title"`
	AmountCents int     `json:"amount_cents"`
	Currency    string  `json:"currency"`
}

// CreateBooking inserts a pending_payment booking after fast-fail pre-checks.
//
// The pre-checks are *advisory*: they give the parent a friendly early answer, but
// the DB (partial unique index + the locked re-count in PayForBooking) is the source
// of truth. In particular the capacity check here can be stale by the time payment
// completes; that is exactly the last-seat race and it is settled in PayForBooking.
func (s *Service) CreateBooking(ctx context.Context, in CreateBookingInput) (*CreateBookingResult, error) {
	// 1. Student exists and belongs to the parent.
	var studentParentID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT parent_id FROM students WHERE id = $1`, in.StudentID).Scan(&studentParentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound("student not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load student: %w", err)
	}
	if studentParentID != in.ParentID {
		return nil, errForbidden("student does not belong to this parent")
	}

	// 2. Class exists and has not started. Compare against the DB clock, not the app clock.
	var (
		classTitle string
		capacity   int
		priceCents int
		currency   string
		upcoming   bool
	)
	err = s.pool.QueryRow(ctx, `
		SELECT title, capacity, price_cents, currency, starts_at > now()
		FROM trial_classes WHERE id = $1`, in.TrialClassID).
		Scan(&classTitle, &capacity, &priceCents, &currency, &upcoming)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound("trial class not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load class: %w", err)
	}
	if !upcoming {
		return nil, errConflict(CodeClassStarted, "this trial class has already started")
	}

	// 3. Friendly early exit for I1. The partial unique index is the real guard.
	dup, err := s.confirmedExists(ctx, s.pool, in.StudentID, in.TrialClassID)
	if err != nil {
		return nil, err
	}
	if dup {
		return nil, errConflict(CodeDuplicateConfirmed, "this child already has a confirmed booking for this class")
	}

	// 4. Advisory capacity check for I2. Can be stale; re-checked under lock at payment time.
	confirmed, err := s.confirmedCount(ctx, s.pool, in.TrialClassID)
	if err != nil {
		return nil, err
	}
	if confirmed >= capacity {
		return nil, errConflict(CodeClassFull, "this trial class is full")
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO bookings (trial_class_id, student_id, parent_id, status)
		VALUES ($1, $2, $3, 'pending_payment')
		RETURNING `+bookingColumns, in.TrialClassID, in.StudentID, in.ParentID)
	bk, err := scanBooking(row)
	if err != nil {
		return nil, fmt.Errorf("insert booking: %w", err)
	}
	return &CreateBookingResult{Booking: bk, ClassTitle: classTitle, AmountCents: priceCents, Currency: currency}, nil
}

// PayInput is the parent's attempt to pay for a pending booking.
type PayInput struct {
	BookingID uuid.UUID
	ParentID  uuid.UUID
	Card      payments.Card
	// ProviderDelay is forwarded to the mock provider so tests and the race demo can
	// control which payer reaches the lock first. Always zero from the HTTP layer.
	ProviderDelay time.Duration
}

// PayResult is the honest outcome of a payment attempt.
type PayResult struct {
	Outcome     Outcome `json:"outcome"`
	Reason      string  `json:"reason,omitempty"`       // status_reason for terminal states
	FailureCode string  `json:"failure_code,omitempty"` // provider failure code when Outcome == payment_failed
	Message     string  `json:"message"`
	Booking     Booking `json:"booking"`
}

// PayForBooking is the critical path. The order of operations is deliberate:
//
//  1. Load the booking; it must belong to the parent and be pending_payment.
//  2. Charge the card OUTSIDE any DB transaction. A declined card records a failed
//     attempt and moves the booking to payment_failed; nothing touches the roster.
//  3. In ONE short transaction: lock the trial_classes row (SELECT ... FOR UPDATE),
//     re-count confirmed bookings, re-check for a duplicate, and only then flip the
//     booking to confirmed. Every confirmation for a class serialises on that one row
//     lock, so two payers for the last seat cannot both see "3 confirmed".
//  4. If the seat is gone (or a duplicate appeared) the charge is refunded immediately
//     and the booking is cancelled with a machine-readable reason.
//  5. A unique_violation (23505) on the partial index is caught as a last-resort guard
//     and treated like duplicate_confirmed. It is unreachable while the lock is honoured,
//     but the handler exists and is tested.
//
// Why charge before locking: holding a row lock across a payment-provider round trip
// would serialise every payment for the class behind the slowest card network call.
// Charging first keeps the critical section to a few milliseconds; the cost is the
// rare "paid then refunded" path, which is made explicit and honest to the user.
func (s *Service) PayForBooking(ctx context.Context, in PayInput) (*PayResult, error) {
	// 1. Load + authorise + state check.
	bk, err := s.loadBookingForPayment(ctx, in.BookingID)
	if err != nil {
		return nil, err
	}
	if bk.ParentID != in.ParentID {
		return nil, errForbidden("booking does not belong to this parent")
	}
	if bk.Status != StatusPendingPayment {
		return nil, errConflict(CodeBookingNotPending,
			fmt.Sprintf("booking is %s and can no longer be paid", bk.Status))
	}

	// 2. Charge outside the transaction.
	charge, err := s.payments.Charge(ctx, payments.ChargeRequest{
		AmountCents: bk.priceCents,
		Currency:    bk.currency,
		Card:        in.Card,
		Delay:       in.ProviderDelay,
	})
	if err != nil {
		return nil, fmt.Errorf("payment provider: %w", err)
	}
	if !charge.Succeeded {
		return s.recordPaymentFailure(ctx, bk, charge.FailureCode)
	}

	// 3-5. Reconcile the successful charge against the seat count under lock.
	res, err := s.confirmUnderLock(ctx, bk, charge.ProviderRef)
	if isUniqueViolation(err, "bookings_one_confirmed_per_student_class") {
		// Last-resort guard (I1). Refund and cancel, exactly like the in-tx duplicate branch.
		return s.cancelAndRefund(ctx, bk, charge.ProviderRef, ReasonDuplicateConfirmed)
	}
	return res, err
}

// recordPaymentFailure handles I3: a declined charge never touches the roster.
func (s *Service) recordPaymentFailure(ctx context.Context, bk paymentBooking, failureCode string) (*PayResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := insertAttempt(ctx, tx, bk, "failed", "mock_fail_"+uuid.NewString()[:8], &failureCode, nil); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status = 'payment_failed', status_reason = $2, updated_at = now()
		WHERE id = $1 AND status = 'pending_payment'`, bk.ID, ReasonPaymentDeclined); err != nil {
		return nil, fmt.Errorf("mark payment_failed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.payResult(ctx, bk.ID, OutcomePaymentFailed, ReasonPaymentDeclined, failureCode)
}

// confirmUnderLock is step 3 of PayForBooking: the only place a booking becomes confirmed.
func (s *Service) confirmUnderLock(ctx context.Context, bk paymentBooking, providerRef string) (*PayResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// (a) Serialise all confirmations for this class on the class row. We lock the
	// trial_classes row rather than bookings rows because the invariant is about the
	// *set* of bookings for a class: locking individual booking rows cannot stop two
	// different rows from both being flipped to confirmed.
	var capacity int
	if err := tx.QueryRow(ctx, `SELECT capacity FROM trial_classes WHERE id = $1 FOR UPDATE`, bk.TrialClassID).
		Scan(&capacity); err != nil {
		return nil, fmt.Errorf("lock class row: %w", err)
	}

	// Re-read our own booking's status under the lock: a second concurrent /pay for the
	// same booking (double click, retry) must not confirm twice or leave a charge unrecorded.
	var current string
	if err := tx.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1 FOR UPDATE`, bk.ID).Scan(&current); err != nil {
		return nil, fmt.Errorf("lock booking row: %w", err)
	}
	if Status(current) != StatusPendingPayment {
		refund, err := s.payments.Refund(ctx, providerRef)
		if err != nil {
			return nil, fmt.Errorf("refund duplicate charge: %w", err)
		}
		if err := insertAttempt(ctx, tx, bk, "refunded", providerRef, nil, &refund.RefundRef); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, errConflict(CodeBookingNotPending, fmt.Sprintf(
			"booking was already %s by an earlier payment; this duplicate charge has been refunded", current))
	}

	// (b) Re-count confirmed seats and (c) re-check for a duplicate, both under the lock.
	confirmed, err := s.confirmedCount(ctx, tx, bk.TrialClassID)
	if err != nil {
		return nil, err
	}
	dup := false
	if !s.skipInTxDuplicateCheck {
		if dup, err = s.confirmedExists(ctx, tx, bk.StudentID, bk.TrialClassID); err != nil {
			return nil, err
		}
	}

	// (d) Decide.
	var reason string
	switch {
	case dup:
		reason = ReasonDuplicateConfirmed
	case confirmed >= capacity:
		reason = ReasonClassFull
	}

	if reason == "" {
		if _, err := tx.Exec(ctx, `
			UPDATE bookings SET status = 'confirmed', confirmed_at = now(), updated_at = now(), status_reason = NULL
			WHERE id = $1`, bk.ID); err != nil {
			return nil, err // may be a 23505 from the partial index; caller inspects it
		}
		if err := insertAttempt(ctx, tx, bk, "succeeded", providerRef, nil, nil); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return s.payResult(ctx, bk.ID, OutcomeConfirmed, "", "")
	}

	// (e) Lost the race or duplicate: refund immediately, cancel with the reason.
	refund, err := s.payments.Refund(ctx, providerRef)
	if err != nil {
		return nil, fmt.Errorf("refund: %w", err)
	}
	if err := insertAttempt(ctx, tx, bk, "refunded", providerRef, nil, &refund.RefundRef); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status = 'cancelled', status_reason = $2, updated_at = now()
		WHERE id = $1`, bk.ID, reason); err != nil {
		return nil, fmt.Errorf("cancel booking: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.payResult(ctx, bk.ID, OutcomeCancelled, reason, "")
}

// cancelAndRefund is the fallback used when the partial unique index rejected a
// confirmation that the in-transaction check did not catch. Runs in a fresh
// transaction because the previous one was aborted by the constraint violation.
func (s *Service) cancelAndRefund(ctx context.Context, bk paymentBooking, providerRef, reason string) (*PayResult, error) {
	refund, err := s.payments.Refund(ctx, providerRef)
	if err != nil {
		return nil, fmt.Errorf("refund: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := insertAttempt(ctx, tx, bk, "refunded", providerRef, nil, &refund.RefundRef); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status = 'cancelled', status_reason = $2, updated_at = now()
		WHERE id = $1 AND status = 'pending_payment'`, bk.ID, reason); err != nil {
		return nil, fmt.Errorf("cancel booking: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.payResult(ctx, bk.ID, OutcomeCancelled, reason, "")
}

// --- helpers -----------------------------------------------------------------

// querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (s *Service) confirmedCount(ctx context.Context, q querier, classID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM bookings WHERE trial_class_id = $1 AND status = 'confirmed'`, classID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count confirmed: %w", err)
	}
	return n, nil
}

func (s *Service) confirmedExists(ctx context.Context, q querier, studentID, classID uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM bookings
			WHERE student_id = $1 AND trial_class_id = $2 AND status = 'confirmed')`, studentID, classID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check duplicate: %w", err)
	}
	return exists, nil
}

// paymentBooking is the booking joined with the class fields payment needs.
type paymentBooking struct {
	Booking
	priceCents int
	currency   string
}

func (s *Service) loadBookingForPayment(ctx context.Context, id uuid.UUID) (paymentBooking, error) {
	var (
		pb     paymentBooking
		status string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT b.id, b.trial_class_id, b.student_id, b.parent_id, b.status, b.status_reason,
		       b.created_at, b.updated_at, b.confirmed_at, c.price_cents, c.currency
		FROM bookings b
		JOIN trial_classes c ON c.id = b.trial_class_id
		WHERE b.id = $1`, id).
		Scan(&pb.ID, &pb.TrialClassID, &pb.StudentID, &pb.ParentID, &status, &pb.StatusReason,
			&pb.CreatedAt, &pb.UpdatedAt, &pb.ConfirmedAt, &pb.priceCents, &pb.currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return pb, errNotFound("booking not found")
	}
	if err != nil {
		return pb, fmt.Errorf("load booking: %w", err)
	}
	pb.Status = Status(status)
	return pb, nil
}

func insertAttempt(ctx context.Context, q querier, bk paymentBooking, status, providerRef string, failureCode, refundRef *string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO payment_attempts (booking_id, amount_cents, currency, provider, provider_ref, status, failure_code, refund_ref)
		VALUES ($1, $2, $3, 'mock', $4, $5, $6, $7)`,
		bk.ID, bk.priceCents, bk.currency, providerRef, status, failureCode, refundRef)
	if err != nil {
		return fmt.Errorf("insert payment attempt (%s): %w", status, err)
	}
	return nil
}

func (s *Service) payResult(ctx context.Context, bookingID uuid.UUID, outcome Outcome, reason, failureCode string) (*PayResult, error) {
	bk, err := s.getBooking(ctx, bookingID)
	if err != nil {
		return nil, err
	}
	return &PayResult{
		Outcome:     outcome,
		Reason:      reason,
		FailureCode: failureCode,
		Message:     StatusMessage(bk.Status, bk.StatusReason, failureCode),
		Booking:     bk,
	}, nil
}

// StatusMessage is the plain-English copy shown to the parent for each state.
func StatusMessage(status Status, reason *string, failureCode string) string {
	r := ""
	if reason != nil {
		r = *reason
	}
	switch status {
	case StatusPendingPayment:
		return "Your seat is not reserved yet. Complete payment to confirm the booking."
	case StatusConfirmed:
		return "Booking confirmed. Your child is on the class roster."
	case StatusPaymentFailed:
		if failureCode != "" {
			return fmt.Sprintf("Payment was declined (%s). You have not been charged and no seat was taken. You can start a new booking and try another card.", failureCode)
		}
		return "Payment was declined. You have not been charged and no seat was taken. You can start a new booking and try another card."
	case StatusCancelled:
		switch r {
		case ReasonClassFull:
			return "Payment was taken and has been refunded; the last seat was taken moments before you paid."
		case ReasonDuplicateConfirmed:
			return "Payment was taken and has been refunded; this child already has a confirmed booking for this class."
		}
		return "This booking was cancelled."
	}
	return ""
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
