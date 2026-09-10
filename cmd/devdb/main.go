// Command devdb runs a local PostgreSQL 16 without Docker (embedded-postgres) on port 5432,
// with both `ottodot` and `ottodot_test` databases, so the default .env.example works as-is.
// It blocks until Ctrl+C. Data persists in ./.devdb between runs.
//
// This is a convenience for machines without Docker; docker compose is the documented default.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5"

	"ottodot/internal/devpg"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "devdb:", err)
		os.Exit(1)
	}
}

func run() error {
	fmt.Println("devdb: starting embedded PostgreSQL 16 on 127.0.0.1:5432 (first run downloads ~40MB)…")
	inst, err := devpg.Start(5432, "ottodot", ".devdb", nil)
	if err != nil {
		return err
	}
	defer func() { _ = inst.Stop() }()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, inst.URL("ottodot"))
	if err != nil {
		return err
	}
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'ottodot_test')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := conn.Exec(ctx, `CREATE DATABASE ottodot_test`); err != nil {
			return err
		}
	}
	conn.Close(ctx)

	fmt.Println("devdb: ready")
	fmt.Println("  DATABASE_URL      =", inst.URL("ottodot"))
	fmt.Println("  DATABASE_URL_TEST =", inst.URL("ottodot_test"))
	fmt.Println("devdb: press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\ndevdb: stopping")
	return nil
}
