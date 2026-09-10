# Decision log

Running, dated log kept while building (AI-assisted; who found what is recorded in `AI_USAGE.md`). Newest entries at
the bottom. Each entry records the alternatives that were on the table, what was chosen and why, and how it was verified.
Bugs and dead ends are logged too.

---

### D-01 — Go instead of Node/Next.js (2026-09-10)
Context: the original build brief fixed Next.js + Drizzle. Before starting, the
stack was reconsidered in favour of Go, *if* the assignment allows it.
Options: (a) Next.js + TypeScript as in the brief, (b) Go backend + Next.js frontend, (c) Go only, server-rendered HTML.
Chose: (c). The assignment PDF has no language requirement ("A CLI, script, API endpoint, server action, or
minimal app is fine") and explicitly de-prioritises the frontend. One Go binary serving JSON + `html/template`
pages keeps the moving parts to a minimum and puts all the evaluated judgement (schema, locking, tests) in one place.
Rejected because: (a) and (b) both put a JavaScript toolchain and build step in front of a UI nobody is grading; (b) also
doubles the runtimes and setup steps.
Evidence / verification: PDF text extracted with `pdftotext` and read in full before deciding.

### D-02 — Stack mapping from the brief to Go (2026-09-10)
Context: the brief's fixed decisions are Node-specific; each needed a Go equivalent that preserves the *intent*.
| Brief | Go equivalent | Why |
|---|---|---|
| Drizzle + `pg` | `pgx/v5` + `pgxpool`, hand-written SQL | pgx is the de-facto Postgres driver; no ORM means the `FOR UPDATE` and `FILTER (WHERE …)` are visible in the code being reviewed |
| drizzle-kit migrations committed as SQL | plain `.sql` files embedded with `embed.FS`, applied by a ~80-line runner in `internal/db/migrate.go` | The brief's requirement is "migrations exist as committed SQL"; a full migration framework (goose/golang-migrate) adds a dependency for two files |
| zod | strict JSON decoding (`DisallowUnknownFields`) + explicit validation functions returning `400 validation_error` | Same boundary guarantee, stdlib only |
| Next.js Route Handlers | `net/http` + Go 1.22 method/path patterns | stdlib |
| Vitest against real Postgres | `go test` against real Postgres, per-package database, `TRUNCATE` before each test | Same testing philosophy: no DB mocks |
| `npm run <script>` | `Makefile` targets + the raw `go run ./cmd/...` commands listed in README (Windows has no `make`) | |
Chose: the table above. Rejected: sqlc / GORM / ent — more generated code than the slice needs.

### D-03 — Tests need a real Postgres, also where Docker is unavailable (2026-09-10)
Context: the brief says tests run against real Postgres via docker compose, but Docker cannot be assumed on every
development machine.
Options: (a) mock the DB in tests (violates the brief and the point of the exercise), (b) require Docker and
ship untested code, (c) fall back to `embedded-postgres` (downloads a real PostgreSQL 16 binary, ~15s cold start)
when `DATABASE_URL_TEST` is not set.
Chose: (c). `DATABASE_URL_TEST` set → use it (the docker compose path, unchanged). Not set → tests start an
embedded PostgreSQL 16 automatically. Same SQL, same constraints, zero mocking. Also added `cmd/devdb` so the
app itself can be run locally without Docker.
Rejected because: (a) would make invariant tests meaningless; (b) means never seeing tests pass.
Evidence / verification: probe program started embedded PostgreSQL 16.9 and ran `select version()` successfully.

### D-04 — `-race` kept out of the default test target (2026-09-10)
Context: `go test -race` needs cgo, which is not available everywhere (Windows without gcc).
Chose: `make test` runs without `-race`; `make test-race` is provided for Linux/macOS/CI. Concurrency
correctness is asserted by the DB-level tests (#10, #11, #16), not by the Go race detector, which would only
catch in-process data races (there is no shared mutable state in the service anyway).

### D-05 — Where the card-shape validation lives (2026-09-10)
Context: the brief requires validation at every API boundary. Two boundaries exist here: the JSON API and the HTML form.
Options: (a) validate in the JSON handler only, HTML form passes raw values through; (b) duplicate the rules in both;
(c) one `payments.Validate(card, now)` used by both.
Chose: (c). The HTML form is a boundary too; duplicating rules is how they drift. First draft had the rules inline in
`internal/httpapi/respond.go`; moved them when the form handler needed the same thing.

### D-06 — snake_case for every JSON field, request and response (2026-09-10)
Context: the brief's examples mix `parentId` (request) with `seats_left` (response).
Chose: snake_case everywhere (`parent_id`, `student_id`, `trial_class_id`, `card.exp_month`). One convention, and it
matches the column names so the roster JSON reads like the schema. Deviation from the brief's literal request shape.

### D-07 — Charge before lock, not inside the transaction (2026-09-10)
Context: where the mock payment call sits relative to the `FOR UPDATE` lock in `PayForBooking`.
Options: (a) lock → charge → confirm, (b) charge → lock → confirm.
Chose: (b), as specified in the brief. The critical section is a handful of statements (lock class row, count, exists,
update, insert) — single-digit milliseconds. Cost: the paid-then-refunded path, which is surfaced honestly with a
`cancelled/class_full` status, a `refunded` payment attempt and the exact sentence the brief requires.
Rejected because: (a) serialises every payment for a class behind provider latency, and a provider timeout would hold the lock.
Evidence: tests #09, #10, #11 pass; `demo-race` prints A as cancelled+refunded.

### D-08 — Lock the `trial_classes` row, not `bookings` rows (2026-09-10)
Context: which row to `SELECT … FOR UPDATE`.
Chose: the class row. The invariant is a property of the *set* of bookings for one class; the class row is the single
natural gate every confirmation for that class must pass through. Locking individual booking rows cannot stop two
different rows from both flipping to confirmed. The booking row is *also* locked inside the same transaction, but for a
different reason (D-09).

### D-09 — Guard against concurrent double-pay of the same booking (2026-09-10)
Context: not in the brief. While writing `PayForBooking` it became clear that the pre-charge "is it pending?" check is outside the
transaction, so two simultaneous `/pay` calls for the *same* booking (double click, client retry) would both charge.
Options: (a) ignore (idempotency keys are listed as "next"), (b) re-read the booking's status under the class lock and,
if it is no longer pending, refund the duplicate charge and record it as `refunded`.
Chose: (b). It is four lines inside a transaction that already exists and it turns a silent double charge into a
recorded, refunded one with a 409 that says so. Idempotency keys remain the proper fix and stay in "What I would do next".
Evidence: test #16.

### D-10 — `refund_ref` column on `payment_attempts` (2026-09-10)
Context: the brief's `payment_attempts` has a single `provider_ref`. On the lost-race path the row is `refunded`, but
we also need the original charge reference for reconciliation with the provider.
Options: (a) two rows (`succeeded` then `refunded`), (b) one `refunded` row with `provider_ref` = charge ref and a new
`refund_ref` column.
Chose: (b). One row per charge keeps "a confirmed booking has exactly one succeeded attempt" trivially true and keeps
the lost-race row self-describing. A CHECK constraint ties `refund_ref IS NOT NULL` to `status = 'refunded'`.

### D-11 — All three payment outcomes are HTTP 200 (2026-09-10)
Context: what status code `/pay` returns for `payment_failed` and `cancelled`.
Options: (a) 402 / 409 for the non-confirmed outcomes, (b) 200 with an explicit `outcome` field for all three.
Chose: (b). The request *was* processed and a result recorded — like a payment intent that ends in
`requires_payment_method`. 4xx is reserved for protocol/authorisation problems (bad body, wrong parent, booking not
pending) where nothing was recorded. Mixing outcome bodies into 4xx would force clients to parse two shapes per status.

### D-12 — Test fixture bug: Aarav is already confirmed in C1 (2026-09-10)
Context: first run of the suite. Tests #04, #12 and #16 failed with `duplicate_confirmed` on `CreateBooking`.
Cause: the tests used Priya→Aarav→C1 as the "happy path" fixture, but the seed confirms Aarav in C1. The service was right;
the test was wrong. Fixed by using Diya for C1 in those tests. Recorded because it is exactly the kind of "the test
failed for a boring reason" that should not be confused with a product bug.

### D-13 — Test #16 was non-deterministic on first write (2026-09-10)
Context: test #16 (double-pay of one booking) asserted attempts = `[succeeded, refunded]` but got `[succeeded]`.
Cause: the second request's *pre-charge* pending check sometimes ran after the first request had already committed
(the fast request finishes in ~6ms), so it was rejected with 409 before charging — correct behaviour, but a different
path than the test intended, and timing-dependent.
Fix: give both requests a provider delay (400ms / 150ms) so both pass the pre-check before either charge returns; now
both charges happen and the refund path is the one exercised. Lesson: a concurrency test must pin its interleaving,
not hope for it. Test #10 does this with the 300ms delay *and* asserts B finished before A, so a vacuous pass is impossible.

### D-14 — Race demo uses two children of the same parent (2026-09-10)
Context: the brief asks for two distinct students not already in C2. With the fixed seed, the only such students are
Aarav and Diya — both Priya's. Adding a fourth parent would change the seed the brief fixes.
Chose: keep the seed; User A = Priya→Aarav, User B = Priya→Diya. The race is between two *bookings* for one seat; whose
account they sit under does not change the mechanism. Noted in the demo output.

### D-15 — `TRUNCATE` per test, per-package database (2026-09-10)
Context: `go test ./...` runs packages in parallel processes. Two packages truncating one database would corrupt each other.
Chose: `testutil.RunWithDatabase` (re)creates `<db>_<package>` per package on the `DATABASE_URL_TEST` server, or one
embedded instance per package when unset. Tests within a package run sequentially and start with the fresh seed.
Rejected: `-p 1` (slower, easy to forget), transaction-rollback isolation (would hide the real commit/lock behaviour the
race tests depend on).

### D-16 — Manual click-through of the four seeded cases (2026-09-10)
Done against the running server with `curl` (form POSTs + following redirects), not a browser; the video walkthrough
repeats the same four cases in a browser. Observed:
- **C1 happy path** (Priya → Diya → Volcanoes, card 4242…): `303 → /bookings/<id>/pay`, pay page shows "Amount due:
  SGD 20.00" and the "not reserved" copy; after pay `303 → /bookings/<id>` with status label **Confirmed**, one
  `succeeded` badge; roster page "2 / 4 confirmed" listing Aarav and Diya; roster JSON `confirmed_count: 2, seats_left: 2`.
- **C4 payment failure** (Marcus → Ethan → Shapes, card 4000…0002): status label **Payment failed**, message
  "Payment was declined (card_declined). You have not been charged and no seat was taken…", reason code
  `payment_declined`, `failed` badge with `card_declined`; roster JSON `confirmed_count: 0`, breakdown `payment_failed: 2`
  (seeded + this one). Visiting `/bookings/<id>/pay` for the failed booking redirects (303) to the status page.
- **C3 duplicate** (Priya → Diya → Water Cycle): `303 → /?parent=…&student=…&error=this+child+already+has+a+confirmed+booking…`;
  the index page renders the red banner.
- **C2 full** (after demo-race left C2 at 4/4): the class row renders the **Full** badge with a `disabled` radio;
  a direct form POST for C2 redirects with "this trial class is full"; `POST /api/bookings` → `409 {"error":{"code":"class_full",…}}`;
  admin classes page shows `4 / 4` + Full.
- Structured log lines confirmed, e.g. `{"level":"INFO","msg":"request","request_id":"…","method":"POST","path":"/api/bookings","status":409,"duration_ms":1.528}`.

### D-17 — Toolchain: go.mod says 1.25, staticcheck needed a rebuild (2026-09-10)
Context: `go mod tidy` bumped `go.mod` to `go 1.25.0` because `embedded-postgres v1.34` requires it; with a default
toolchain of 1.24.x, GOTOOLCHAIN=auto fetched 1.25 transparently. The pre-installed `staticcheck` refused to
analyse a 1.25 module, and `staticcheck@latest` needs Go 1.26.
Chose: keep `go 1.25.0` (current stable; reviewers on ≥1.21 get the toolchain auto-downloaded) and reinstall staticcheck
with `GOTOOLCHAIN=auto go install honnef.co/go/tools/cmd/staticcheck@latest`. `make lint` treats a missing staticcheck
as a skip with a message, not a failure. First staticcheck run found one real thing: an unused `errValidation` helper
left over from D-05 — removed.

### D-18 — What was verified, and how many times (2026-09-10)
- `gofmt -l .` clean; `go vet ./...` clean; `staticcheck ./...` clean.
- `go test ./... -count=1`: `internal/booking` ok (16.2s incl. ~14s embedded-Postgres start), `internal/httpapi` ok,
  `internal/payments` ok. 19 DB-backed subtests + 4 provider unit tests.
- `go test ./internal/booking -run TestLastSeatRace -count=5 -v`: 5/5 passes for #10, #11, #16, #17; interleaving log
  showed B at +2–3ms and A at +308–359ms every run.
- `go run ./cmd/demo-race` twice: identical result; second run printed the idempotent reset line.
- `go run ./cmd/migrate` twice: second run "database is up to date".

### D-19 — Docker compose path verified end to end from a fresh clone (2026-09-10)
Context: the docker compose path is the one the README tells reviewers to use, so it was run from a fresh clone following
only the README. Environment: Docker 20.10.17, Compose v2.7.0, Go 1.25.0 darwin/arm64, gcc present.
Observed, in order:
- `cp .env.example .env && docker compose up -d --wait` → container healthy in 4.4s; `docker/init.sql` created
  `ottodot_test`; server reports `PostgreSQL 16.15 on aarch64-unknown-linux-musl`.
- `go run ./cmd/migrate` → applied `0001_schema`, `0002_booking_constraints`; second run → "database is up to date".
- `go run ./cmd/seed` → 3 parents, 5 students, 4 classes, 6 bookings, 6 attempts; C1 1 / C2 3 / C3 1 (Diya) / C4 0 + 1 failed.
- `make verify` → green. Suite against docker compose: booking 2.9s, httpapi 2.3s, payments 1.3s (vs 16s / 14s embedded).
- `go run ./cmd/demo-race` twice → identical: B confirmed at +19ms, A `cancelled/class_full` + `refunded` at +359ms,
  C2 4/4 `[Ethan, Mia, Noah, Diya]`; second run printed the idempotent reset line.
- **`go test ./... -count=1 -race`** → green (booking 4.1s, httpapi 3.4s, payments 2.1s). `go test ./internal/booking -run TestLastSeatRace -count=5 -race` → ok.
- `go run ./cmd/server` against the docker DB: `GET /` 200, roster JSON for C1 `confirmed_count: 1, seats_left: 3`,
  `/api/trial-classes` (envelope `{"trial_classes":[…]}`), `/admin/classes` renders the Full badge; JSON request logs present.
- The embedded fallback was also run once (`env -u DATABASE_URL_TEST go test ./internal/httpapi`): downloaded the
  binaries and passed in 23s. Both database paths verified.
Two things were *not* green on the way and are logged as D-20 and D-21.

### D-20 — Stale `staticcheck` crashed and `make lint` reported it as "not installed" (2026-09-10)
Context: a staticcheck 2024.1.1 binary was installed. Against the Go 1.25 module it died with
`internal error in importing "internal/byteorder" (unsupported version: 2)` — exactly what D-17 predicted. The Makefile
line `command -v staticcheck && staticcheck ./... || echo "staticcheck not installed; skipped"` treated that crash the
same as a missing binary: `make verify` printed "skipped" and **exited 0**. A false green on the reviewer's entry point.
Options: (a) leave it, document; (b) `if … then staticcheck || fail; else skip; fi`; (c) make staticcheck mandatory.
Chose: (b). Missing binary → still a skip with a message (reviewers should not need to install it); installed-but-failing
→ non-zero exit with a one-line reinstall hint. (c) rejected: `make verify` must work on a fresh clone with only Go + Docker.
Also reinstalled with `GOTOOLCHAIN=auto go install honnef.co/go/tools/cmd/staticcheck@latest` → 2026.2.1 (Go 1.26.8
auto-fetched). `staticcheck ./...` → 0 findings.
Evidence: `make verify` from a clean shell now runs staticcheck silently (no "skipped" line) and exits 0.

### D-21 — Tests do not read `.env`; `make` now exports it (2026-09-10)
Context: `config.Load` (used by the binaries) calls `godotenv.Load()`, but `internal/testutil` reads
`os.Getenv("DATABASE_URL_TEST")` directly — and `go test` runs each package with cwd = the package directory, so a
repo-root `.env` would not be found anyway. Result: the README's `make verify`, with Docker running and `.env` in place,
still started an embedded Postgres per package (`[testutil] DATABASE_URL_TEST not set …`, httpapi 23s) instead of using
`ottodot_test`. Correctness is unaffected (real Postgres either way, D-03) — but the documented Docker path was not the
path actually being exercised.
Options: (a) teach `testutil` to walk up to the repo root and load `.env`; (b) `-include .env` + `export` in the
Makefile; (c) documentation only.
Chose: (b) + (c). The Makefile is the reviewer entry point and the fix is two lines with no test-code change. README now
says a bare `go test ./...` needs `DATABASE_URL_TEST` exported (`set -a; source .env; set +a`) or falls back to embedded.
(a) is a reasonable follow-up: it would also fix bare `go test`, at the cost of test infrastructure reading a dotfile.
Evidence: `make verify` from a shell with no `DATABASE_URL*` set → no `[testutil]` fallback line, booking 4.0s,
httpapi 1.2s; `make -n` shows the variables resolved from `.env`.

### D-22 — Supabase run skipped; it is not a requirement (2026-09-10)
Context: a README section inherited from the Next.js-era brief gave step-by-step instructions for running against Supabase.
Checked the assignment PDF: Supabase appears once, in "Suggested Model" — "seed file, JSON, CSV, SQLite, Postgres,
Supabase, or in-memory data". One option in a permissive list; plain PostgreSQL satisfies it.
Chose: skip the run, reword the README section to "Other Postgres hosts" stating that only Docker and the embedded
fallback were tested. Reason: a README paragraph with step-by-step Supabase instructions implies it was tried.
`.env.example` keeps its commented Supabase line (harmless, and true).
