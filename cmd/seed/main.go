// Command seed truncates every table and loads the fixed demo dataset (see internal/seed).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"ottodot/internal/config"
	"ottodot/internal/db"
	"ottodot/internal/seed"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
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

	s, err := seed.Run(ctx, pool, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("seed: %d parents, %d students, %d trial classes, %d bookings, %d payment attempts\n",
		s.Parents, s.Students, s.Classes, s.Bookings, s.PaymentAttempts)
	fmt.Println("seed: C1 Volcanoes = 1 confirmed | C2 Fractions = 3 confirmed (1 seat left) | C3 Water Cycle = 1 confirmed (Diya) | C4 Shapes = 0 confirmed, 1 payment_failed (Ethan)")
	return nil
}
