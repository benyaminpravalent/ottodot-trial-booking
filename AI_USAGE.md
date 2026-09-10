# AI usage

## Which AI tools I used

**Claude Code** (Claude Fable 5.1, in VS Code) was the coding agent for this project, in two sittings: a build
session that took the repository from an empty folder to green tests and first-draft docs, and a review pass afterwards.

The build session ran from a written brief that I prepared with AI help after reading the assignment and then edited. The decisions in it are the ones I wanted evaluated: Postgres as the single source of truth,
a pessimistic row lock on the class row, charge before lock, an explicit booking state machine, seats derived from
confirmed rows rather than a counter, and a fixed list of tests including the last-seat race.

Two things worth being precise about:

- The brief originally fixed Next.js + Drizzle. I changed my mind and asked for Go, *conditional on the assignment
  allowing it*. The agent extracted the PDF text, confirmed there is no language requirement ("a CLI, script, API
  endpoint, server action, or minimal app is fine") and switched (D-01). So the agent did read the assignment PDF, once,
  for that check.
- Everything the agent did is logged with dates in `DECISIONS.md`, including the dead ends. That file was written during
  the work, not reconstructed afterwards.

## What I used AI for

The division of labour was: I specified *what* and *why*; the agent implemented, tested and documented against that spec.

| I decided | The AI produced |
|---|---|
| Postgres as the single source of truth; no `seats_taken` counter, seats derived from confirmed rows | Schema SQL (`internal/db/migrations/*.sql`), pool, ~80-line migration runner |
| Pessimistic `SELECT … FOR UPDATE` on the **class row**, re-count under lock, partial unique index as defence in depth | `internal/booking/service.go` — `CreateBooking`, `PayForBooking`, roster/list queries |
| Charge **before** the lock; lost race ⇒ immediate refund + `cancelled/class_full` | Mock provider with deterministic test cards and a `Delay` knob (`internal/payments`) |
| The state machine (`pending_payment → confirmed | payment_failed | cancelled`) and reason codes | JSON API + form pages, request-id middleware, structured logs |
| The 15-case test list, incl. the two race tests | 19 DB-backed test cases + 4 unit tests against a real Postgres, seed data with fixed UUIDs, `demo-race`, `devdb` |
| Go instead of Node (D-01) | Go equivalents for each brief decision (D-02) |
| README and AI_USAGE structure | First drafts of both, plus `DECISIONS.md` kept during the build |

Things the agent proposed on its own that I reviewed and kept: the double-pay guard for two concurrent `/pay` calls on
the same booking (D-09), the `refund_ref` column so a refunded attempt still carries the original charge reference (D-10),
the embedded-Postgres fallback for machines without Docker (D-03), and the "B finished before A" assertion that makes the
race test impossible to pass vacuously (D-13). Each of them is small, has a test, and does not change the design I asked for.

Things I did not let it add: a `seats_taken` counter, authentication, a queue or background worker, regular enrollment,
and hiding full classes instead of disabling them. All of these are listed in the README under *What I deliberately cut*.

## One place AI helped me move faster

The **deterministic concurrency test harness**. Getting two (or ten) payments to genuinely overlap, prove which one
landed first, and assert the database state afterwards is fiddly to write by hand. The agent produced
`payConcurrently` in `internal/booking/race_test.go` — one goroutine per payer, each on its own pool connection, a
provider `Delay` to pin the interleaving, and a recorded finish time per call — and tests #10, #11, #16 and #17 on top
of it. All four passed five consecutive runs after the fixture fix (D-12), with the log line
`B finished at +3ms (confirmed), A finished at +309ms (cancelled/class_full)` proving the overlap.

By hand I would budget well over an hour for this, most of it spent making the test fail for the right reason. The agent
produced it in a few minutes; the time I spent on it was reading it and re-running it, including under `go test -race`.

## One place I disagreed with, corrected, or rejected AI output

Attribution first, because it matters for this file: items 1–4 below were surfaced by the agent's *own* verify loop
during the build (failing tests, staticcheck), not by me reading a diff. My contribution was refusing to take "green" at
face value: I re-ran everything from a fresh clone, following only the README, with the raw output reported verbatim,
and that pass found item 5, which the first "green" had hidden.

1. **Test #16 was timing-dependent.** The agent's first version of the double-pay test assumed both requests would
   charge and asserted `[succeeded, refunded]`. It failed because the fast request committed before the slow one had even
   passed its pre-charge status check — a correct product path, but not the one the test claimed to cover. Fix: pin the
   interleaving with delays on *both* requests. A concurrency test must force its schedule, not hope for it. (D-13)
2. **A "happy path" fixture contradicted the seed.** Tests #04, #12 and #16 used Priya→Aarav→C1, but the seed already
   confirms Aarav in C1, so `CreateBooking` correctly returned `duplicate_confirmed`. The service was right; the tests
   were wrong. (D-12)
3. **Card validation was about to be duplicated.** The first draft put the card-shape rules inline in the JSON handler.
   When the HTML form needed the same rules, they were moved to one `payments.Validate` used by both boundaries rather
   than copied. (D-05)
4. **Dead code.** `staticcheck` flagged an unused `errValidation` helper left over from the refactor above; removed. (D-17)
5. **`make verify` was falsely green.** The agent's Makefile ran staticcheck as
   `command -v staticcheck && staticcheck ./... || echo "not installed; skipped"`. On the fresh-clone run the installed
   staticcheck was too old for Go 1.25 and crashed; the `||` swallowed the crash, printed "skipped", and `make verify`
   exited 0. It only surfaced because I had asked for the raw output instead of a summary. Fixed so a missing binary
   still skips but a failing one fails the build with a reinstall hint. The same pass showed the tests never read `.env`,
   so the documented Docker path had not actually been exercised by `make verify` until then. (D-20, D-21)

Not an AI error, but a real deviation from my own brief: the brief's request bodies were camelCase while the responses
were snake_case; I took snake_case everywhere, matching the column names (D-06).

## What I would change about my AI workflow next time

- **Ask for raw output, not summaries, from the first `make verify`.** The false green in item 5 above was one
  `|| echo "skipped"` away from being shipped. Summaries hide exactly the lines that matter.
- **Write the failing race test before the implementation.** Test #10 was written after `PayForBooking`; it passed
  first time, which is reassuring but proves less than a test that was red first. With the harness now in place, the
  next concurrency feature (seat holds) should start from a red test.
- **Ask the agent to propose extra test cases before coding.** The two most valuable tests in the suite, #16 (double-pay)
  and #17 (23505 fallback), were not on my list; they came from asking "what else could go wrong here". Do that at the
  start, not halfway.
- **Verify at the end of every phase, not at the end of the build.** The fixture bug (D-12) sat unnoticed through two
  phases; a `make verify` after the seed phase would have caught it in a minute.
- **Keep `DECISIONS.md` from minute one, including the boring failures.** I did this, and it is the only reason this file
  could be written honestly instead of from memory.

## How I verified the final implementation

- `make verify` (gofmt, `go vet`, `staticcheck`, `go test ./... -count=1`) is green — 19 database-backed test cases plus
  4 provider unit tests across `internal/booking`, `internal/httpapi`, `internal/payments`, all against a real PostgreSQL 16.
- `go test ./internal/booking -run TestLastSeatRace -count=5` — the race tests pass five consecutive runs.
- Re-ran everything from a fresh clone, following only the README, against docker compose Postgres 16.15: `make verify`,
  `make test-race` (Go race detector), the race tests five times under `-race`, and `demo-race` twice. All green; details
  in D-19. This is the pass that exposed §4 item 5.
- `go run ./cmd/demo-race` — prints User B confirmed at +3ms, User A `cancelled/class_full` with a `refunded` attempt at
  ~+360ms, roster 4/4; re-running it resets C2 and produces the same result.
- Exercised the four seeded cases end to end over HTTP against the running server, form POSTs with redirects followed
  (D-16): happy path on C1, declined card on C4 leaves the roster at 0, duplicate on C3 rejected with a message, full C2
  shown disabled and rejected on a direct POST. The video walkthrough repeats the same four cases in a browser.
- Read `internal/db/migrations/0002_booking_constraints.sql` to confirm the partial unique index is real SQL, and test #08
  proves Postgres raises `23505` on it.
- Walked `PayForBooking` in `internal/booking/service.go` line by line against the brief's ordering during the review
  pass: load → owner and `pending_payment` check → charge outside the transaction → `FOR UPDATE` on `trial_classes` →
  `FOR UPDATE` on the booking row (double-pay guard) → count → duplicate check → confirm, else refund + cancel; `23505`
  from the partial index handled as `duplicate_confirmed`. The code matches the README's description of it.
