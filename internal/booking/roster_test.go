package booking

import (
	"testing"

	"ottodot/internal/payments"
)

func TestRoster(t *testing.T) {
	t.Run("13 roster lists only confirmed students and reports seats_left = capacity - confirmed", func(t *testing.T) {
		svc := newService(t)

		// C4: one payment_failed booking, nobody confirmed. Roster must be empty.
		r4, err := svc.GetRoster(ctx(t), c4)
		if err != nil {
			t.Fatal(err)
		}
		if len(r4.Confirmed) != 0 || r4.ConfirmedCount != 0 || r4.SeatsLeft != 4 {
			t.Errorf("C4 roster = %d listed / %d confirmed / %d left, want 0/0/4", len(r4.Confirmed), r4.ConfirmedCount, r4.SeatsLeft)
		}
		if r4.Breakdown[StatusPaymentFailed] != 1 {
			t.Errorf("C4 breakdown = %v, want payment_failed=1", r4.Breakdown)
		}

		// C2: exactly three confirmed, one seat left; a pending booking must not appear.
		mustCreate(t, svc, priya, aarav, c2)
		r2, err := svc.GetRoster(ctx(t), c2)
		if err != nil {
			t.Fatal(err)
		}
		if r2.ConfirmedCount != 3 || r2.SeatsLeft != 1 || r2.Class.IsFull {
			t.Errorf("C2 = %d confirmed / %d left / full=%v, want 3/1/false", r2.ConfirmedCount, r2.SeatsLeft, r2.Class.IsFull)
		}
		names := map[string]bool{}
		for _, e := range r2.Confirmed {
			names[e.StudentName] = true
			if e.ConfirmedAt.IsZero() || e.ParentEmail == "" {
				t.Errorf("roster entry incomplete: %+v", e)
			}
		}
		if len(names) != 3 || !names["Ethan"] || !names["Mia"] || !names["Noah"] || names["Aarav"] {
			t.Errorf("C2 roster names = %v, want exactly Ethan, Mia, Noah", names)
		}
		if r2.Breakdown[StatusPendingPayment] != 1 {
			t.Errorf("C2 breakdown = %v, want pending_payment=1", r2.Breakdown)
		}

		// Confirming a booking moves the student onto the roster and reduces seats_left.
		bk := mustCreate(t, svc, priya, diya, c1)
		if res := mustPay(t, svc, bk, payments.CardSuccess); res.Outcome != OutcomeConfirmed {
			t.Fatalf("outcome = %s", res.Outcome)
		}
		r1, err := svc.GetRoster(ctx(t), c1)
		if err != nil {
			t.Fatal(err)
		}
		if r1.ConfirmedCount != 2 || r1.SeatsLeft != 2 || len(r1.Confirmed) != 2 {
			t.Errorf("C1 = %d confirmed / %d left / %d listed, want 2/2/2", r1.ConfirmedCount, r1.SeatsLeft, len(r1.Confirmed))
		}
	})

	t.Run("13b list classes derives confirmed_count/seats_left/is_full in SQL and keeps full classes visible", func(t *testing.T) {
		svc := newService(t)
		if err := insertConfirmedDirect(t, c2, aarav, priya); err != nil {
			t.Fatal(err)
		}
		classes, err := svc.ListClasses(ctx(t), true)
		if err != nil {
			t.Fatal(err)
		}
		if len(classes) != 4 {
			t.Fatalf("listed %d classes, want 4 (full classes must remain visible)", len(classes))
		}
		byID := map[string]ClassSummary{}
		for _, c := range classes {
			byID[c.ID.String()] = c
		}
		if c := byID[c2.String()]; !c.IsFull || c.SeatsLeft != 0 || c.ConfirmedCount != 4 {
			t.Errorf("C2 = %+v, want full with 0 seats left", c)
		}
		if c := byID[c1.String()]; c.IsFull || c.SeatsLeft != 3 || c.ConfirmedCount != 1 {
			t.Errorf("C1 = %+v, want 3 seats left", c)
		}
		if c := byID[c4.String()]; c.ConfirmedCount != 0 || c.SeatsLeft != 4 {
			t.Errorf("C4 = %+v, want 0 confirmed (payment_failed must not count)", c)
		}
	})
}
