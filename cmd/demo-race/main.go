// Command demo-race reproduces the assignment's last-seat race live against DATABASE_URL:
//
//  1. Reset C2 "Math: Fractions with Pizza" to its seeded state: 3 confirmed, 1 seat left.
//  2. User A (Priya → Aarav) and User B (Priya → Diya) both create pending bookings
//     for that last seat. Neither holds it: pending bookings do not reserve seats.
//  3. Both pay concurrently. A's card network is slow (300ms), so B lands first.
//  4. Print both outcomes and the final roster. Exit 1 if the invariant is violated.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"ottodot/internal/booking"
	"ottodot/internal/config"
	"ottodot/internal/db"
	"ottodot/internal/payments"
	"ottodot/internal/seed"
)

type payer struct {
	label    string
	student  uuid.UUID
	name     string
	delay    time.Duration
	booking  booking.Booking
	result   *booking.PayResult
	err      error
	finished time.Duration
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "demo-race:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	svc := booking.NewService(pool, payments.NewMock())

	// 1. Reset C2 to exactly the three seeded confirmed bookings.
	if err := resetC2(ctx, pool); err != nil {
		return err
	}
	before, err := svc.GetRoster(ctx, seed.ClassC2)
	if err != nil {
		return err
	}
	fmt.Printf("Class C2 %q before: %d/%d confirmed, %d seat left\n\n",
		before.Class.Title, before.ConfirmedCount, before.Capacity, before.SeatsLeft)

	// 2. Both users reach the payment page for the same last seat.
	payers := []*payer{
		{label: "User A", student: seed.StudentAarav, name: "Priya → Aarav", delay: 300 * time.Millisecond},
		{label: "User B", student: seed.StudentDiya, name: "Priya → Diya", delay: 0},
	}
	for _, p := range payers {
		res, err := svc.CreateBooking(ctx, booking.CreateBookingInput{ParentID: seed.ParentPriya, StudentID: p.student, TrialClassID: seed.ClassC2})
		if err != nil {
			return fmt.Errorf("create booking for %s: %w", p.label, err)
		}
		p.booking = res.Booking
		fmt.Printf("%s (%s) created booking %s → %s\n", p.label, p.name, p.booking.ID, p.booking.Status)
	}

	// 3. Pay concurrently. A is slower at the provider, so B confirms first and A pays last.
	fmt.Printf("\nBoth pay now. %s's provider call is delayed %v so %s lands first…\n\n", payers[0].label, payers[0].delay, payers[1].label)
	var wg sync.WaitGroup
	start := time.Now()
	for _, p := range payers {
		wg.Add(1)
		go func(p *payer) {
			defer wg.Done()
			p.result, p.err = svc.PayForBooking(ctx, booking.PayInput{
				BookingID:     p.booking.ID,
				ParentID:      seed.ParentPriya,
				Card:          payments.Card{Number: payments.CardSuccess, ExpMonth: 12, ExpYear: 2030, CVC: "123"},
				ProviderDelay: p.delay,
			})
			p.finished = time.Since(start)
		}(p)
	}
	wg.Wait()

	// 4. Report.
	fmt.Printf("%-8s %-15s %-10s %-16s %-20s %-22s %s\n", "Who", "Student", "Finished", "Outcome", "Reason", "Payment attempts", "Message")
	fmt.Println(strings.Repeat("-", 130))
	confirmed, cancelledFull := 0, 0
	for _, p := range payers {
		if p.err != nil {
			fmt.Printf("%-8s %-15s %-10s ERROR: %v\n", p.label, p.name, p.finished.Round(time.Millisecond), p.err)
			continue
		}
		detail, err := svc.GetBookingDetail(ctx, p.booking.ID)
		if err != nil {
			return err
		}
		attempts := make([]string, 0, len(detail.Attempts))
		for _, a := range detail.Attempts {
			attempts = append(attempts, a.Status)
		}
		fmt.Printf("%-8s %-15s %-10s %-16s %-20s %-22s %s\n",
			p.label, p.name, "+"+p.finished.Round(time.Millisecond).String(), p.result.Outcome, orDash(p.result.Reason),
			strings.Join(attempts, ","), p.result.Message)
		switch {
		case p.result.Outcome == booking.OutcomeConfirmed:
			confirmed++
		case p.result.Outcome == booking.OutcomeCancelled && p.result.Reason == booking.ReasonClassFull:
			cancelledFull++
		}
	}

	after, err := svc.GetRoster(ctx, seed.ClassC2)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(after.Confirmed))
	for _, e := range after.Confirmed {
		names = append(names, e.StudentName)
	}
	fmt.Printf("\nClass C2 after: %d/%d confirmed [%s], %d seat left, is_full=%v\n",
		after.ConfirmedCount, after.Capacity, strings.Join(names, ", "), after.SeatsLeft, after.Class.IsFull)

	if confirmed != 1 || cancelledFull != 1 || after.ConfirmedCount != 4 {
		return fmt.Errorf("INVARIANT VIOLATED: confirmed=%d cancelled/class_full=%d roster=%d (want 1, 1, 4)",
			confirmed, cancelledFull, after.ConfirmedCount)
	}
	fmt.Println("OK: exactly one confirmed, one cancelled+refunded, roster at capacity.")
	return nil
}

// resetC2 removes every C2 booking that is not one of the three seeded confirmed ones,
// so the demo can be re-run any number of times. Refuses to run if the seed is missing.
func resetC2(ctx context.Context, pool *pgxpool.Pool) error {
	var seeded int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM bookings
		WHERE trial_class_id = $1 AND status = 'confirmed' AND id = ANY($2)`,
		seed.ClassC2, seed.SeededConfirmedC2).Scan(&seeded); err != nil {
		return err
	}
	if seeded != len(seed.SeededConfirmedC2) {
		return fmt.Errorf("C2 has %d of the %d seeded confirmed bookings; run `go run ./cmd/seed` first", seeded, len(seed.SeededConfirmedC2))
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `
		DELETE FROM payment_attempts
		WHERE booking_id IN (SELECT id FROM bookings WHERE trial_class_id = $1 AND NOT (id = ANY($2)))`,
		seed.ClassC2, seed.SeededConfirmedC2); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM bookings WHERE trial_class_id = $1 AND NOT (id = ANY($2))`,
		seed.ClassC2, seed.SeededConfirmedC2)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		fmt.Printf("Reset: removed %d non-seeded booking(s) from C2 left over from a previous run.\n", n)
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
