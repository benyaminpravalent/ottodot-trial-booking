package booking

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"ottodot/internal/payments"
	"ottodot/internal/seed"
)

// Test numbering follows the spec list in README → Verification. Each subtest name is
// the spec sentence so `go test -v` reads like the specification.

func TestCreateBooking(t *testing.T) {
	t.Run("01 creates a pending_payment booking for an eligible student and class", func(t *testing.T) {
		svc := newService(t)
		res, err := svc.CreateBooking(ctx(t), CreateBookingInput{ParentID: priya, StudentID: aarav, TrialClassID: c2})
		if err != nil {
			t.Fatal(err)
		}
		if res.Booking.Status != StatusPendingPayment {
			t.Errorf("status = %s, want pending_payment", res.Booking.Status)
		}
		if res.Booking.ConfirmedAt != nil {
			t.Errorf("confirmed_at should be nil for a pending booking")
		}
		if res.AmountCents != 2000 || res.Currency != "SGD" {
			t.Errorf("amount = %d %s, want 2000 SGD", res.AmountCents, res.Currency)
		}
		// A pending booking does not take a seat.
		if got := confirmedCount(t, c2); got != 3 {
			t.Errorf("confirmed count after pending booking = %d, want 3", got)
		}
	})

	t.Run("02 rejects booking a student who does not belong to the parent", func(t *testing.T) {
		svc := newService(t)
		_, err := svc.CreateBooking(ctx(t), CreateBookingInput{ParentID: marcus, StudentID: aarav, TrialClassID: c1})
		assertBusinessError(t, err, CodeForbidden, http.StatusForbidden)
	})

	t.Run("03 rejects a class that is already full at creation time", func(t *testing.T) {
		svc := newService(t)
		// Fill C2's 4th seat, then a 5th student tries to start a booking.
		if err := insertConfirmedDirect(t, c2, aarav, priya); err != nil {
			t.Fatal(err)
		}
		_, err := svc.CreateBooking(ctx(t), CreateBookingInput{ParentID: priya, StudentID: diya, TrialClassID: c2})
		assertBusinessError(t, err, CodeClassFull, http.StatusConflict)
	})

	t.Run("07 duplicate confirmed booking for same child+class is rejected at creation (I1, service layer)", func(t *testing.T) {
		svc := newService(t)
		// Diya is already confirmed in C3 by the seed.
		_, err := svc.CreateBooking(ctx(t), CreateBookingInput{ParentID: priya, StudentID: diya, TrialClassID: c3})
		assertBusinessError(t, err, CodeDuplicateConfirmed, http.StatusConflict)
	})

	t.Run("08 duplicate confirmed booking is rejected at the DB level even if the service check is bypassed (I1, DB layer)", func(t *testing.T) {
		newService(t)
		err := insertConfirmedDirect(t, c3, diya, priya)
		assertPgCode(t, err, "23505", "bookings_one_confirmed_per_student_class")

		// The partial index must NOT block non-confirmed repeats (that is what makes retries possible).
		insertPendingDirect(t, c3, diya, priya)
		insertPendingDirect(t, c3, diya, priya)
	})
}

func TestPayForBooking(t *testing.T) {
	t.Run("04 payment success on a class with seats → confirmed, exactly one succeeded payment attempt", func(t *testing.T) {
		svc := newService(t)
		bk := mustCreate(t, svc, priya, diya, c1)
		res := mustPay(t, svc, bk, payments.CardSuccess)

		if res.Outcome != OutcomeConfirmed {
			t.Fatalf("outcome = %s, want confirmed (%s)", res.Outcome, res.Message)
		}
		if res.Booking.Status != StatusConfirmed || res.Booking.ConfirmedAt == nil {
			t.Errorf("booking = %+v, want confirmed with confirmed_at", res.Booking)
		}
		if got := attemptStatuses(t, bk.ID); len(got) != 1 || got[0] != "succeeded" {
			t.Errorf("payment attempts = %v, want [succeeded]", got)
		}
		if got := confirmedCount(t, c1); got != 2 {
			t.Errorf("C1 confirmed = %d, want 2", got)
		}
	})

	t.Run("05 payment failure → payment_failed, failed attempt recorded, roster unchanged (I3)", func(t *testing.T) {
		svc := newService(t)
		before := confirmedCount(t, c4) // 0: Ethan's seeded booking is payment_failed
		bk := mustCreate(t, svc, marcus, ethan, c4)
		res := mustPay(t, svc, bk, payments.CardDeclined)

		if res.Outcome != OutcomePaymentFailed || res.FailureCode != "card_declined" {
			t.Fatalf("outcome = %s/%s, want payment_failed/card_declined", res.Outcome, res.FailureCode)
		}
		if st, reason := bookingStatus(t, bk.ID); st != StatusPaymentFailed || reason != ReasonPaymentDeclined {
			t.Errorf("booking = %s/%s, want payment_failed/payment_declined", st, reason)
		}
		if got := attemptStatuses(t, bk.ID); len(got) != 1 || got[0] != "failed" {
			t.Errorf("payment attempts = %v, want [failed]", got)
		}
		if after := confirmedCount(t, c4); after != before {
			t.Errorf("roster changed on payment failure: %d → %d", before, after)
		}
		roster, err := svc.GetRoster(ctx(t), c4)
		if err != nil {
			t.Fatal(err)
		}
		if len(roster.Confirmed) != 0 {
			t.Errorf("roster lists %d students after failed payments, want 0", len(roster.Confirmed))
		}

		// insufficient_funds goes down the same path with its own code.
		bk2 := mustCreate(t, svc, marcus, ethan, c4)
		if res2 := mustPay(t, svc, bk2, payments.CardInsufficientFunds); res2.FailureCode != "insufficient_funds" {
			t.Errorf("failure code = %s, want insufficient_funds", res2.FailureCode)
		}
	})

	t.Run("06 after payment_failed, the parent can create a new booking for the same child/class and confirm it", func(t *testing.T) {
		svc := newService(t)
		first := mustCreate(t, svc, sofia, mia, c4)
		if res := mustPay(t, svc, first, payments.CardDeclined); res.Outcome != OutcomePaymentFailed {
			t.Fatalf("first attempt outcome = %s, want payment_failed", res.Outcome)
		}
		// payment_failed is terminal for that booking…
		_, err := svc.PayForBooking(ctx(t), PayInput{BookingID: first.ID, ParentID: sofia, Card: card(payments.CardSuccess)})
		assertBusinessError(t, err, CodeBookingNotPending, http.StatusConflict)

		// …but a fresh booking for the same child + class is allowed and can confirm.
		second := mustCreate(t, svc, sofia, mia, c4)
		if res := mustPay(t, svc, second, payments.CardSuccess); res.Outcome != OutcomeConfirmed {
			t.Fatalf("second attempt outcome = %s, want confirmed", res.Outcome)
		}
		roster, err := svc.GetRoster(ctx(t), c4)
		if err != nil {
			t.Fatal(err)
		}
		if len(roster.Confirmed) != 1 || roster.Confirmed[0].StudentName != "Mia" {
			t.Errorf("roster = %+v, want exactly [Mia]", roster.Confirmed)
		}
	})

	t.Run("09 never exceeds capacity: with 3 confirmed, the 4th confirms and the 5th is cancelled with class_full and refunded (I2)", func(t *testing.T) {
		svc := newService(t)
		fourth := mustCreate(t, svc, priya, aarav, c2)
		fifth := mustCreate(t, svc, priya, diya, c2) // allowed: pending bookings do not hold seats

		if res := mustPay(t, svc, fourth, payments.CardSuccess); res.Outcome != OutcomeConfirmed {
			t.Fatalf("4th payer outcome = %s, want confirmed", res.Outcome)
		}
		res := mustPay(t, svc, fifth, payments.CardSuccess)
		if res.Outcome != OutcomeCancelled || res.Reason != ReasonClassFull {
			t.Fatalf("5th payer outcome = %s/%s, want cancelled/class_full", res.Outcome, res.Reason)
		}
		if res.Message != "Payment was taken and has been refunded; the last seat was taken moments before you paid." {
			t.Errorf("unexpected user-facing message: %q", res.Message)
		}
		if got := attemptStatuses(t, fifth.ID); len(got) != 1 || got[0] != "refunded" {
			t.Errorf("5th payer attempts = %v, want [refunded]", got)
		}
		if got := confirmedCount(t, c2); got != 4 {
			t.Errorf("C2 confirmed = %d, want 4", got)
		}
	})

	t.Run("12 paying a booking that is not pending_payment → 409", func(t *testing.T) {
		svc := newService(t)

		// Seeded confirmed booking (Aarav in C1).
		_, err := svc.PayForBooking(ctx(t), PayInput{BookingID: seed.BookingC1Aarav, ParentID: priya, Card: card(payments.CardSuccess)})
		assertBusinessError(t, err, CodeBookingNotPending, http.StatusConflict)

		// Seeded payment_failed booking (Ethan in C4).
		_, err = svc.PayForBooking(ctx(t), PayInput{BookingID: seed.BookingC4EthanFailed, ParentID: marcus, Card: card(payments.CardSuccess)})
		assertBusinessError(t, err, CodeBookingNotPending, http.StatusConflict)

		// Neither call may have recorded a charge.
		if got := attemptStatuses(t, seed.BookingC1Aarav); len(got) != 1 || got[0] != "succeeded" {
			t.Errorf("C1 booking attempts = %v, want the single seeded [succeeded]", got)
		}

		// Related authorisation/lookup errors on the same path.
		pending := mustCreate(t, svc, priya, diya, c1)
		_, err = svc.PayForBooking(ctx(t), PayInput{BookingID: pending.ID, ParentID: marcus, Card: card(payments.CardSuccess)})
		assertBusinessError(t, err, CodeForbidden, http.StatusForbidden)
		_, err = svc.PayForBooking(ctx(t), PayInput{BookingID: uuid.New(), ParentID: priya, Card: card(payments.CardSuccess)})
		assertBusinessError(t, err, CodeNotFound, http.StatusNotFound)
		if st, _ := bookingStatus(t, pending.ID); st != StatusPendingPayment {
			t.Errorf("booking status after rejected attempts = %s, want pending_payment", st)
		}
	})
}
