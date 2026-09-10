// Package booking is the framework-agnostic service layer for trial bookings.
// It talks to Postgres through pgx and to the payment provider through the
// payments.Provider port, and knows nothing about HTTP.
package booking

import (
	"time"

	"github.com/google/uuid"
)

// Status is the booking state machine's state. Mirrors the booking_status enum.
type Status string

const (
	StatusPendingPayment Status = "pending_payment"
	StatusConfirmed      Status = "confirmed"
	StatusPaymentFailed  Status = "payment_failed"
	StatusCancelled      Status = "cancelled"
)

// Machine-readable status_reason values for terminal states.
const (
	ReasonPaymentDeclined    = "payment_declined"
	ReasonClassFull          = "class_full"
	ReasonDuplicateConfirmed = "duplicate_confirmed"
)

// Outcome is what PayForBooking reports back.
type Outcome string

const (
	OutcomeConfirmed     Outcome = "confirmed"
	OutcomePaymentFailed Outcome = "payment_failed"
	OutcomeCancelled     Outcome = "cancelled"
)

// Parent is a seeded account used to simulate "logged in as".
type Parent struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Email string    `json:"email"`
}

// Student is a child of a parent.
type Student struct {
	ID       uuid.UUID `json:"id"`
	ParentID uuid.UUID `json:"parent_id"`
	Name     string    `json:"name"`
	Grade    int       `json:"grade"`
}

// ClassSummary is a trial class plus the seat figures derived from confirmed bookings.
type ClassSummary struct {
	ID             uuid.UUID `json:"id"`
	Subject        string    `json:"subject"`
	Title          string    `json:"title"`
	TeacherName    string    `json:"teacher_name"`
	StartsAt       time.Time `json:"starts_at"`
	Capacity       int       `json:"capacity"`
	PriceCents     int       `json:"price_cents"`
	Currency       string    `json:"currency"`
	ConfirmedCount int       `json:"confirmed_count"`
	SeatsLeft      int       `json:"seats_left"`
	IsFull         bool      `json:"is_full"`
}

// Booking is one row of the bookings table.
type Booking struct {
	ID           uuid.UUID  `json:"id"`
	TrialClassID uuid.UUID  `json:"trial_class_id"`
	StudentID    uuid.UUID  `json:"student_id"`
	ParentID     uuid.UUID  `json:"parent_id"`
	Status       Status     `json:"status"`
	StatusReason *string    `json:"status_reason"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ConfirmedAt  *time.Time `json:"confirmed_at"`
}

// PaymentAttempt is one row of payment_attempts.
type PaymentAttempt struct {
	ID          uuid.UUID `json:"id"`
	BookingID   uuid.UUID `json:"booking_id"`
	AmountCents int       `json:"amount_cents"`
	Currency    string    `json:"currency"`
	Provider    string    `json:"provider"`
	ProviderRef string    `json:"provider_ref"`
	Status      string    `json:"status"` // succeeded | failed | refunded
	FailureCode *string   `json:"failure_code"`
	RefundRef   *string   `json:"refund_ref"`
	CreatedAt   time.Time `json:"created_at"`
}

// BookingDetail is everything the status page needs.
type BookingDetail struct {
	Booking     Booking          `json:"booking"`
	ClassTitle  string           `json:"class_title"`
	StartsAt    time.Time        `json:"starts_at"`
	StudentName string           `json:"student_name"`
	ParentName  string           `json:"parent_name"`
	AmountCents int              `json:"amount_cents"`
	Currency    string           `json:"currency"`
	Message     string           `json:"message"` // plain-English explanation of the current state
	Attempts    []PaymentAttempt `json:"payment_attempts"`
}

// RosterEntry is one confirmed student on a class roster.
type RosterEntry struct {
	BookingID   uuid.UUID `json:"booking_id"`
	StudentName string    `json:"student_name"`
	Grade       int       `json:"grade"`
	ParentName  string    `json:"parent_name"`
	ParentEmail string    `json:"parent_email"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

// Roster is the admin/teacher view of one class. Confirmed contains only status='confirmed'.
type Roster struct {
	Class          ClassSummary  `json:"class"`
	Capacity       int           `json:"capacity"`
	ConfirmedCount int           `json:"confirmed_count"`
	SeatsLeft      int           `json:"seats_left"`
	Confirmed      []RosterEntry `json:"confirmed"`
	// Breakdown counts every non-confirmed status too, for the admin's benefit.
	Breakdown map[Status]int `json:"status_breakdown"`
}
