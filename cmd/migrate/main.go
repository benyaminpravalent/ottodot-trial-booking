// Command migrate applies pending SQL migrations to DATABASE_URL.
//
//	go run ./cmd/migrate          # apply pending migrations
//	go run ./cmd/migrate -reset   # drop this app's tables and types, then re-apply everything
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"ottodot/internal/config"
	"ottodot/internal/db"
)

func main() {
	reset := flag.Bool("reset", false, "drop all trial-booking tables and re-run every migration (destroys data)")
	flag.Parse()
	if err := run(*reset); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(reset bool) error {
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

	if reset {
		// Only this application's objects: safe on a shared database such as Supabase,
		// where dropping the whole public schema would also remove extensions.
		if _, err := pool.Exec(ctx, `
			DROP TABLE IF EXISTS payment_attempts, bookings, students, trial_classes, parents, schema_migrations CASCADE;
			DROP TYPE IF EXISTS booking_status`); err != nil {
			return fmt.Errorf("reset: %w", err)
		}
		fmt.Println("migrate: dropped all trial-booking tables")
	}

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("migrate: database is up to date")
		return nil
	}
	for _, v := range applied {
		fmt.Println("migrate: applied", v)
	}
	return nil
}
