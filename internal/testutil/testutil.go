// Package testutil gives every test package a real, migrated, isolated Postgres database.
//
// Resolution order:
//  1. DATABASE_URL_TEST is set (docker compose path): a per-package database named
//     <db>_<suffix> is (re)created on that server so packages can run in parallel.
//  2. Otherwise an embedded PostgreSQL 16 is started for the duration of the package's
//     tests (first run downloads the binaries, ~15s cold start afterwards).
//
// There is deliberately no database mocking: the invariants under test live in the DB.
package testutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ottodot/internal/db"
	"ottodot/internal/devpg"
	"ottodot/internal/seed"
)

// RunWithDatabase is meant to be called from TestMain. It prepares a migrated database,
// hands the pool to setPool, runs the tests, and tears everything down.
func RunWithDatabase(m *testing.M, suffix string, setPool func(*pgxpool.Pool)) int {
	ctx := context.Background()

	adminURL := os.Getenv("DATABASE_URL_TEST")
	var embedded *devpg.Instance
	if adminURL == "" {
		fmt.Fprintf(os.Stderr, "[testutil] DATABASE_URL_TEST not set; starting embedded PostgreSQL 16 for package %q\n", suffix)
		inst, err := devpg.Start(0, "ottodot_test", "", nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[testutil] embedded postgres failed:", err)
			return 1
		}
		embedded = inst
		adminURL = inst.URL("ottodot_test")
		defer func() { _ = embedded.Stop() }()
	}

	testURL, err := recreateDatabase(ctx, adminURL, suffix)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[testutil] prepare database:", err)
		return 1
	}

	pool, err := db.Connect(ctx, testURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[testutil] connect:", err)
		return 1
	}
	defer pool.Close()

	if _, err := db.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, "[testutil] migrate:", err)
		return 1
	}

	setPool(pool)
	return m.Run()
}

// recreateDatabase drops and creates <db>_<suffix> on the server adminURL points at and
// returns the URL of the new database.
func recreateDatabase(ctx context.Context, adminURL, suffix string) (string, error) {
	u, err := url.Parse(adminURL)
	if err != nil {
		return "", fmt.Errorf("parse DATABASE_URL_TEST: %w", err)
	}
	base := strings.TrimPrefix(u.Path, "/")
	if base == "" {
		base = "postgres"
	}
	name := sanitize(base + "_" + suffix)

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return "", fmt.Errorf("connect to %s: %w", adminURL, err)
	}
	defer conn.Close(ctx)

	// Identifier is derived from our own constants; quoting keeps it safe regardless.
	if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`); err != nil {
		return "", fmt.Errorf("drop %s: %w", name, err)
	}
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); err != nil {
		return "", fmt.Errorf("create %s: %w", name, err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// Truncate empties every table. Call at the start of each test for isolation.
func Truncate(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin truncate: %v", err)
	}
	if err := seed.Truncate(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("truncate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit truncate: %v", err)
	}
}

// Seed loads the fixed dataset (after truncating).
func Seed(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := seed.Run(ctx, pool, time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}
}
