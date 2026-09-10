# AI usage

<!-- MAX: this whole file is a DRAFT written by the AI in your voice. Every claim about what *you* did or decided is
     wrapped in a MAX comment. Confirm, rewrite, or delete each one, then remove all MAX comments before pushing. -->

## Which AI tools I used

I used **Claude Code** (Claude Fable 5.1, the desktop/VS Code agent) as the primary and only coding agent, working from a
written build brief I authored after reading the assignment (`OTTODOT_BUILD_PROMPT.md`).
<!-- MAX: verify — the brief fixed the stack, the locking strategy, the state machine, the charge→lock ordering, the
     derived seat count and the test list. If you drafted the brief with AI help, say so here. -->

Two things worth being precise about:
- The brief originally fixed Next.js + Drizzle. I changed my mind and asked for Go, *conditional on the assignment
  allowing it*. The agent extracted the PDF text, confirmed there is no language requirement ("a CLI, script, API
  endpoint, server action, or minimal app is fine"), and switched. So the AI **did** read the assignment PDF, once, for
  that check. <!-- MAX: verify -->
- I did not use ChatGPT, Copilot or any other assistant. <!-- MAX: add any other tool you actually used, or confirm "none" -->

## What I used AI for

The division of labour was: I specified *what* and *why*; the agent implemented, tested and documented against that spec.

| I decided | The AI produced |
|---|---|
| Postgres as the single source of truth; no `seats_taken` counter, seats derived from confirmed rows | Schema SQL (`internal/db/migrations/*.sql`), pool, ~80-line migration runner |
| Pessimistic `SELECT … FOR UPDATE` on the **class row**, re-count under lock, partial unique index as defence in depth | `internal/booking/service.go` — `CreateBooking`, `PayForBooking`, roster/list queries |
| Charge **before** the lock; lost race ⇒ immediate refund + `cancelled/class_full` | Mock provider with deterministic test cards and a `Delay` knob (`internal/payments`) |
| The state machine (`pending_payment → confirmed | payment_failed | cancelled`) and reason codes | JSON API + form pages, request-id middleware, structured logs |
| The 15-case test list, incl. the two race tests | 19 DB-backed test cases + 4 unit tests against a real Postgres, seed data with fixed UUIDs, `demo-race`, `devdb` |
| Go instead of Node <!-- MAX: verify --> | Go equivalents for each brief decision (logged as D-02) |
| README / AI_USAGE / video-guide structure | First drafts of all three, plus `DECISIONS.md` kept during the build |

Things the AI proposed on its own that I kept: the double-pay guard (D-09), the `refund_ref` column (D-10), the
embedded-Postgres fallback for machines without Docker (D-03), and the "B finished before A" assertion that makes the
race test impossible to pass vacuously. <!-- MAX: confirm you reviewed and agree with each -->

## One place AI helped me move faster

The **deterministic concurrency test harness**. Getting two (or ten) payments to genuinely overlap, prove which one
landed first, and assert the database state afterwards is fiddly to write by hand. The agent produced
`payConcurrently` in `internal/booking/race_test.go` — one goroutine per payer, each on its own pool connection, a
provider `Delay` to pin the interleaving, and a recorded finish time per call — and tests #10, #11, #16 and #17 on top
of it. All four passed five consecutive runs on the first try after the fixture fix, with the log line
`B finished at +3ms (confirmed), A finished at +309ms (cancelled/class_full)` proving the overlap.
<!-- MAX: quantify — e.g. "I'd budget 60–90 minutes for this by hand; it took ~10 minutes of agent time plus my review." -->

## One place I disagreed with, corrected, or rejected AI output

<!-- MAX: IMPORTANT — the four items below are real and are logged in DECISIONS.md, but they were caught by the agent's
     own verify loop *during* the session, not by you personally. Either (a) re-verify them yourself and own them as
     review findings, or (b) reword to "the AI's first attempt was wrong and it corrected itself; here is what I checked".
     Do not present them as your catches unless you actually re-checked them. -->

1. **Test #16 was timing-dependent.** The first version of the double-pay test assumed both requests would charge and
   asserted `[succeeded, refunded]`. It failed because the fast request committed before the slow one had even passed
   its pre-charge status check — a correct product path, but not the one the test claimed to cover. Fix: pin the
   interleaving with delays on *both* requests. A concurrency test must force its schedule, not hope for it. (D-13)
   <!-- MAX: confirm this happened as described -->
2. **A "happy path" fixture contradicted the seed.** Tests #04, #12 and #16 used Priya→Aarav→C1, but the seed already
   confirms Aarav in C1, so `CreateBooking` correctly returned `duplicate_confirmed`. The service was right; the tests
   were wrong. (D-12) <!-- MAX: confirm this happened as described -->
3. **Card validation was about to be duplicated.** The first draft put the card-shape rules inline in the JSON handler.
   When the HTML form needed the same rules, they were moved to one `payments.Validate` used by both boundaries rather
   than copied. (D-05) <!-- MAX: confirm this happened as described -->
4. **Dead code.** `staticcheck` flagged an unused `errValidation` helper left over from the refactor above; removed.
   <!-- MAX: confirm -->

Not an AI error, but a real deviation I chose from my own brief: the brief's request bodies were camelCase while the
responses were snake_case; I took snake_case everywhere (D-06). <!-- MAX: verify you agree -->

## What I would change about my AI workflow next time

<!-- MAX: write this section yourself; the bullets below are prompts, not claims. -->
- Write the failing race test (#10) *before* letting the agent implement `PayForBooking`, so the implementation is
  driven by the invariant rather than checked against it afterwards.
- Give the agent the test list and ask it to propose *additional* cases before coding — #16 (double-pay) and #17
  (23505 fallback) came out of that kind of thinking and were the most valuable additions.
- Run `make verify` at the end of every phase instead of at the end of the build; the fixture bug would have been
  caught 20 minutes earlier.
- Keep `DECISIONS.md` from minute one, including the boring failures; it is what made this file honest to write.

## How I verified the final implementation

- `make verify` (gofmt, `go vet`, `staticcheck`, `go test ./... -count=1`) is green — 19 database-backed test cases plus 4 provider unit tests across
  `internal/booking`, `internal/httpapi`, `internal/payments`, all against a real PostgreSQL 16.
- `go test ./internal/booking -run TestLastSeatRace -count=5` — the race tests pass five consecutive runs.
- `go run ./cmd/demo-race` — prints User B confirmed at +3ms, User A `cancelled/class_full` with a `refunded` attempt at
  ~+360ms, roster 4/4; re-running it resets C2 and produces the same result.
- Clicked through the four seeded cases against the running server (recorded in D-16): happy path on C1, declined card
  on C4 leaves the roster at 0, duplicate on C3 rejected with a message, full C2 shown disabled and rejected on a direct POST.
  <!-- MAX: the agent did this with curl; do it once yourself in a browser before recording. -->
- Read `internal/db/migrations/0002_booking_constraints.sql` to confirm the partial unique index is real SQL, and test #08
  proves Postgres raises `23505` on it.
- Read `PayForBooking` in `internal/booking/service.go` line by line against the brief's §5.2 ordering:
  load → charge outside tx → `FOR UPDATE` on `trial_classes` → count → duplicate check → confirm, else refund + cancel;
  `23505` handled as `duplicate_confirmed`. <!-- MAX: do this read yourself; it is the thing the interviewer will ask about -->
