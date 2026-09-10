# Decision log

Running, dated log kept while building. Newest entries at the bottom. Each entry records the alternatives
that were on the table, what was chosen and why, and how it was verified. Bugs and dead ends are logged too.

---

### D-01 — Go instead of Node/Next.js (2026-09-10)
Context: the original build brief (`OTTODOT_BUILD_PROMPT.md`) fixed Next.js + Drizzle. Max changed his mind
and wanted Go, *if* the assignment allows it.
Options: (a) Next.js + TypeScript as in the brief, (b) Go backend + Next.js frontend, (c) Go only, server-rendered HTML.
Chose: (c). The assignment PDF has no language requirement ("A CLI, script, API endpoint, server action, or
minimal app is fine") and explicitly de-prioritises the frontend. One Go binary serving JSON + `html/template`
pages keeps the moving parts to a minimum and puts all the evaluated judgement (schema, locking, tests) in one place.
Rejected because: (a) contradicts Max's decision; (b) doubles the runtimes and setup steps for a UI nobody is grading.
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

### D-03 — Tests need a real Postgres but this machine has no Docker (2026-09-10)
Context: the brief says tests run against real Postgres via docker compose. Docker is not installed on the
build machine, and `psql` is not either.
Options: (a) mock the DB in tests (violates the brief and the point of the exercise), (b) require Docker and
ship untested code, (c) fall back to `embedded-postgres` (downloads a real PostgreSQL 16 binary, ~15s cold start)
when `DATABASE_URL_TEST` is not set.
Chose: (c). `DATABASE_URL_TEST` set → use it (the docker compose path, unchanged). Not set → tests start an
embedded PostgreSQL 16 automatically. Same SQL, same constraints, zero mocking. Also added `cmd/devdb` so the
app itself can be run locally without Docker.
Rejected because: (a) would make invariant tests meaningless; (b) means never seeing tests pass.
Evidence / verification: probe program started embedded PostgreSQL 16.9 and ran `select version()` successfully.

### D-04 — `-race` not available on this Windows box (2026-09-10)
Context: `go test -race` needs cgo/gcc on Windows; no gcc installed.
Chose: `make test` runs without `-race`; `make test-race` is provided for Linux/macOS/CI. Concurrency
correctness is asserted by the DB-level tests (#10, #11, #16), not by the Go race detector, which would only
catch in-process data races (there is no shared mutable state in the service anyway).

### D-05 — Where the card-shape validation lives (2026-09-10)
Context: the brief says "zod at every API boundary". Two boundaries exist here: the JSON API and the HTML form.
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
Context: not in the brief. While writing `PayForBooking` I noticed the pre-charge "is it pending?" check is outside the
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
Cause: I used Priya→Aarav→C1 as the "happy path" fixture, but the seed confirms Aarav in C1. The service was right;
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
account they sit under does not change the mechanism. Noted in the demo output and video guide.

### D-15 — `TRUNCATE` per test, per-package database (2026-09-10)
Context: `go test ./...` runs packages in parallel processes. Two packages truncating one database would corrupt each other.
Chose: `testutil.RunWithDatabase` (re)creates `<db>_<package>` per package on the `DATABASE_URL_TEST` server, or one
embedded instance per package when unset. Tests within a package run sequentially and start with the fresh seed.
Rejected: `-p 1` (slower, easy to forget), transaction-rollback isolation (would hide the real commit/lock behaviour the
race tests depend on).

### D-16 — Manual click-through of the four seeded cases (2026-09-10)
Done against the running server with `curl` (form POSTs + following redirects), not a browser; Max should repeat it
once in a browser before recording. Observed:
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
Context: `go mod tidy` bumped `go.mod` to `go 1.25.0` because `embedded-postgres v1.34` requires it; the machine's
default toolchain is 1.24.3 (GOTOOLCHAIN=auto fetched 1.25 transparently). The pre-installed `staticcheck` refused to
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

### D-19 — Suggested commit sequence (repo was not under git during the build)
1. `chore: scaffold Go module, docker compose, env example, Makefile`
2. `feat(db): schema + booking constraints migrations, pool, embedded SQL migration runner`
3. `feat(payments): deterministic mock provider with test cards, delay knob, shared card validation`
4. `feat(booking): service layer — create, pay (charge → FOR UPDATE → count → confirm/refund), roster, list`
5. `feat(seed): fixed-UUID demo dataset covering the four assignment cases`
6. `test(booking): 17 DB-backed cases incl. last-seat race, 10-payer stress, double-pay, 23505 fallback`
7. `feat(api): JSON route handlers, request-id + structured logging, API tests`
8. `feat(web): server-rendered pages — book, pay, status, admin classes, roster`
9. `feat(cmd): demo-race, devdb (embedded Postgres fallback), migrate -reset`
10. `docs: README, AI_USAGE draft, DECISIONS log, video walkthrough guide`
