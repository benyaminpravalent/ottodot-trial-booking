package booking

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"ottodot/internal/payments"
)

// payConcurrently fires one PayForBooking per input on its own goroutine. Each goroutine
// acquires its own connection from the pool, so the transactions really do contend for
// the trial_classes row lock. It returns results in input order plus each call's
// completion time relative to start, which the tests use to prove the interleaving.
type concurrentResult struct {
	res      *PayResult
	err      error
	finished time.Duration
}

func payConcurrently(t *testing.T, svc *Service, inputs []PayInput) []concurrentResult {
	t.Helper()
	out := make([]concurrentResult, len(inputs))
	var wg sync.WaitGroup
	start := time.Now()
	for i, in := range inputs {
		wg.Add(1)
		go func(i int, in PayInput) {
			defer wg.Done()
			res, err := svc.PayForBooking(ctx(t), in)
			out[i] = concurrentResult{res: res, err: err, finished: time.Since(start)}
		}(i, in)
	}
	wg.Wait()
	return out
}

func TestLastSeatRace(t *testing.T) {
	t.Run("10 last-seat race: two concurrent payments for one remaining seat → exactly one confirmed, the other cancelled+refunded (I4)", func(t *testing.T) {
		svc := newService(t)
		if got := confirmedCount(t, c2); got != 3 {
			t.Fatalf("precondition: C2 confirmed = %d, want 3", got)
		}

		// Both users reach the payment page (both bookings are pending; neither holds the seat).
		userA := mustCreate(t, svc, priya, aarav, c2)
		userB := mustCreate(t, svc, priya, diya, c2)

		// A's card network is slow (300ms); B completes first. This is the assignment's scenario:
		// A selected first, B pays first, A pays last.
		results := payConcurrently(t, svc, []PayInput{
			{BookingID: userA.ID, ParentID: priya, Card: card(payments.CardSuccess), ProviderDelay: 300 * time.Millisecond},
			{BookingID: userB.ID, ParentID: priya, Card: card(payments.CardSuccess)},
		})
		a, b := results[0], results[1]
		if a.err != nil || b.err != nil {
			t.Fatalf("unexpected errors: A=%v B=%v", a.err, b.err)
		}
		t.Logf("interleaving: B finished at +%v (%s), A finished at +%v (%s/%s)",
			b.finished.Round(time.Millisecond), b.res.Outcome, a.finished.Round(time.Millisecond), a.res.Outcome, a.res.Reason)

		// Prove the two calls actually overlapped and B really landed first.
		if !(b.finished < a.finished) {
			t.Fatalf("B should finish before A (B=%v, A=%v); the test would be vacuous otherwise", b.finished, a.finished)
		}
		if b.res.Outcome != OutcomeConfirmed {
			t.Errorf("B outcome = %s, want confirmed", b.res.Outcome)
		}
		if a.res.Outcome != OutcomeCancelled || a.res.Reason != ReasonClassFull {
			t.Errorf("A outcome = %s/%s, want cancelled/class_full", a.res.Outcome, a.res.Reason)
		}
		if st, reason := bookingStatus(t, userA.ID); st != StatusCancelled || reason != ReasonClassFull {
			t.Errorf("A booking = %s/%s, want cancelled/class_full", st, reason)
		}
		if got := attemptStatuses(t, userA.ID); len(got) != 1 || got[0] != "refunded" {
			t.Errorf("A attempts = %v, want [refunded]", got)
		}
		if got := attemptStatuses(t, userB.ID); len(got) != 1 || got[0] != "succeeded" {
			t.Errorf("B attempts = %v, want [succeeded]", got)
		}
		if got := confirmedCount(t, c2); got != 4 {
			t.Errorf("C2 confirmed = %d, want exactly 4", got)
		}
	})

	t.Run("11 last-seat race, 10 concurrent payers for 1 seat → exactly one confirmed", func(t *testing.T) {
		svc := newService(t)
		students := addStudents(t, priya, 10)

		inputs := make([]PayInput, 0, len(students))
		bookings := make([]Booking, 0, len(students))
		for _, st := range students {
			bk := mustCreate(t, svc, priya, st, c2)
			bookings = append(bookings, bk)
			inputs = append(inputs, PayInput{BookingID: bk.ID, ParentID: priya, Card: card(payments.CardSuccess)})
		}

		results := payConcurrently(t, svc, inputs)

		confirmed, cancelled := 0, 0
		for i, r := range results {
			if r.err != nil {
				t.Fatalf("payer %d: %v", i, r.err)
			}
			switch {
			case r.res.Outcome == OutcomeConfirmed:
				confirmed++
				if got := attemptStatuses(t, bookings[i].ID); len(got) != 1 || got[0] != "succeeded" {
					t.Errorf("winner attempts = %v, want [succeeded]", got)
				}
			case r.res.Outcome == OutcomeCancelled && r.res.Reason == ReasonClassFull:
				cancelled++
				if got := attemptStatuses(t, bookings[i].ID); len(got) != 1 || got[0] != "refunded" {
					t.Errorf("loser %d attempts = %v, want [refunded]", i, got)
				}
			default:
				t.Errorf("payer %d: unexpected outcome %s/%s", i, r.res.Outcome, r.res.Reason)
			}
		}
		if confirmed != 1 || cancelled != 9 {
			t.Errorf("confirmed=%d cancelled=%d, want 1/9", confirmed, cancelled)
		}
		if got := confirmedCount(t, c2); got != 4 {
			t.Errorf("C2 confirmed = %d, want exactly 4", got)
		}
	})

	t.Run("16 concurrent double-pay of the same booking → one confirmed, the duplicate charge refunded", func(t *testing.T) {
		svc := newService(t)
		bk := mustCreate(t, svc, priya, diya, c1)

		// Two identical requests in flight at once (double click / retry). Both delays are long
		// enough that BOTH requests pass the pre-charge "is it pending?" check before either
		// charge returns, so both charges succeed at the provider. Only one may confirm, and
		// the other charge must be refunded and recorded, not silently dropped.
		results := payConcurrently(t, svc, []PayInput{
			{BookingID: bk.ID, ParentID: priya, Card: card(payments.CardSuccess), ProviderDelay: 400 * time.Millisecond},
			{BookingID: bk.ID, ParentID: priya, Card: card(payments.CardSuccess), ProviderDelay: 150 * time.Millisecond},
		})

		var confirmed, rejected int
		for _, r := range results {
			switch {
			case r.err == nil && r.res.Outcome == OutcomeConfirmed:
				confirmed++
			case r.err != nil:
				assertBusinessError(t, r.err, CodeBookingNotPending, http.StatusConflict)
				rejected++
			default:
				t.Errorf("unexpected result: %+v", r)
			}
		}
		if confirmed != 1 || rejected != 1 {
			t.Fatalf("confirmed=%d rejected=%d, want 1/1", confirmed, rejected)
		}
		if got := attemptStatuses(t, bk.ID); len(got) != 2 || got[0] != "succeeded" || got[1] != "refunded" {
			t.Errorf("attempts = %v, want [succeeded refunded]", got)
		}
		if got := confirmedCount(t, c1); got != 2 {
			t.Errorf("C1 confirmed = %d, want 2", got)
		}
	})

	t.Run("17 unique_violation fallback: with the in-transaction duplicate check bypassed, 23505 is caught and the booking is cancelled+refunded", func(t *testing.T) {
		svc := newService(t)
		svc.skipInTxDuplicateCheck = true

		// Diya is already confirmed in C3. Bypass CreateBooking's pre-check to get a pending row.
		dup := insertPendingDirect(t, c3, diya, priya)
		res, err := svc.PayForBooking(ctx(t), PayInput{BookingID: dup.ID, ParentID: priya, Card: card(payments.CardSuccess)})
		if err != nil {
			t.Fatalf("expected the 23505 to be handled, got error: %v", err)
		}
		if res.Outcome != OutcomeCancelled || res.Reason != ReasonDuplicateConfirmed {
			t.Fatalf("outcome = %s/%s, want cancelled/duplicate_confirmed", res.Outcome, res.Reason)
		}
		if got := attemptStatuses(t, dup.ID); len(got) != 1 || got[0] != "refunded" {
			t.Errorf("attempts = %v, want [refunded]", got)
		}
		var n int
		if err := pool.QueryRow(ctx(t), `SELECT count(*) FROM bookings WHERE student_id = $1 AND trial_class_id = $2 AND status = 'confirmed'`,
			diya, c3).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("Diya has %d confirmed bookings in C3, want exactly 1", n)
		}
	})
}
