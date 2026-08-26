# #1805 — `Session.Activate` fails fast on an already-cancelled context

**Size:** `s` — one production file (`internal/sessions/session.go`), one test file
(`internal/sessions/session_test.go`), no new exported types, no consumer migration.

## Files to read first

Production:

- `internal/sessions/session.go` → `Activate` — **the defect site.** Two things happen in the
  wrong order: the `activateCh` signal is sent before the context is ever consulted, and the
  context is then consulted in a `select` whose other arm is already ready.
- `internal/sessions/session.go` → `runEvicted`, `Run`, `transitionTo` — establishes that the
  `activateCh` signal is the **sole** trigger that takes a session out of `stateEvicted` and
  starts a supervisor. This is what makes "no signal was sent" a sound proxy for "no child
  spawned" in the test below.
- `internal/sessions/session.go` → `closedChan` — the warm-start closed `activeCh`. This is the
  state the "already active, transition complete" fixture reproduces by construction.
- `internal/sessions/session.go` → `Evict` — identical `select` shape, identical latent coin
  flip. **Explicitly out of scope** (ticket § Out of scope). Do not touch it.
- `internal/streamsup/runner.go` → `(*Runner).WaitForPTY` — returns `nil` unconditionally, by
  design (no PTY on the stream-json path). Read it to understand *why* the single `select` in
  `Activate` decides everything on the production path, and why the test fixture must mirror
  this rather than re-check the context.
- `internal/sessions/pool.go` → `(*Pool).Activate` — the only production caller of
  `Session.Activate`. Confirms no signature change and no call-site migration.
- `cmd/pyry/main.go` → `newInboundDeliver` — the one caller that derives the Activate context
  (`context.WithTimeout`) from a parent that can already be cancelled during drain. Read it to
  confirm the behaviour change is desirable there (it is; see § Consumer impact).

Tests / fixtures:

- `internal/sessions/runner_test.go` → `fakeRunner` — `WaitForPTY` returns `nil`
  unconditionally, exactly like the production runner. **This is the runner the new tests
  use.**
- `internal/sessions/runner_test.go` → `lifecycleRunner` — its `WaitForPTY` is a *second*
  `select` on `ctx.Done()`. **Do not use it in the new tests.** See § The fixture trap.
- `internal/sessions/session_test.go` → `TestSession_Run_RemovedCh_ExitsClean` — its
  `newEvictedSession` closure is the bare-`Session`-literal fixture pattern to copy: an evicted
  session with no pool, no lifecycle goroutine, no child.
- `internal/sessions/session_test.go` → `TestSession_ActivateNoOpWhenActive`,
  `TestSession_ActivateCtxCancellation` — the existing `Activate` tests: naming shape, and the
  assertion this ticket makes deterministic. `TestSession_ActivateCtxCancellation` stays as-is.
- `internal/sessions/pool_test.go` and `internal/sessions/pool_list_test.go` — each contains an
  *active* bare-`Session` literal (`lcState: stateActive`, `activeCh: closedChan()`,
  `evictedCh: make(chan struct{})`, both signal channels buffered 1). Copy that field set for
  the active-session fixture.

Docs (read-only; the documentation phase owns them):

- `docs/knowledge/features/sessions-package.md` § "Pool.Create (1.1a-A2)" → the
  **"ctx cancellation race"** paragraph, and § "`Session`" → the `Activate(ctx)` bullet. Both
  make claims this ticket narrows. See § Documentation follow-up — **do not edit them.**

## Context

`Session.Activate` consults its context in exactly one place: the `select` that waits on
`activeCh`. Once a session is fully active, `activeCh` is closed, so both arms of that `select`
are ready and Go picks uniformly at random. A caller passing an already-cancelled context gets
`nil` back roughly half the time on the production path — `streamsup`'s `WaitForPTY` returns
`nil` unconditionally, so there is no second check to catch the leak. The ticket measured
1-in-4 through the `lifecycleRunner` fixture, whose `WaitForPTY` is a second identical coin
flip that swallows half of what slips through the first.

There is a second, fully deterministic defect on the same call: `Activate` pushes the
`activateCh` signal *before* it looks at the context, so a caller that has already given up
still drives a full re-activation — the lifecycle goroutine leaves `runEvicted`, `Run` calls
`transitionTo(stateActive)`, and a child gets spawned. Measured 5 of 5 probe runs.

`TestSession_ActivateCtxCancellation` is the only test asserting this contract, and it drives
an *evicted* session, where `activeCh` is open and the `select` therefore has only one ready
arm. That is why it passes almost always and reddened once under `make check` scheduler
starvation (PR #1804): its verdict is a coin flip, so it can distinguish neither a working
contract from a deleted one, nor a real regression from bad luck.

**No ADR needed.** This restores the contract the doc comment already claims; it introduces no
new design decision.

## Design

### Production change — one guard, at the top of `Activate`

Add an entry guard as the **first statement** of `Session.Activate`, before `lcMu` is taken:

```go
func (s *Session) Activate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// ... existing body unchanged ...
}
```

That is the entire production change. It fixes both defects at once:

- **The coin flip** — a caller with an already-cancelled context never reaches the `select`, so
  the random pick cannot happen. Returns `context.Canceled` (or `context.DeadlineExceeded` for
  an expired deadline — `ctx.Err()` is correct for both).
- **The side effect** — the guard sits *above* the `lcMu` block, so the non-blocking send on
  `activateCh` never runs. Placement is load-bearing: a guard below the `lcMu` block would fix
  AC 1 and leave AC 2 broken.

**Rejected: re-checking the context inside the `<-ch` arm of the `select`** (a "priority
select"). It would not fix the side effect — the signal has already been sent by then — and it
would change behaviour for a case nobody has observed: a context cancelled *during* a wait that
then completes successfully. Per the evidence-based-fix rule, the observed failure is
"cancelled before the call", and the guard covers exactly that.

**Not in scope: `Evict`.** It carries the identical shape. No failure has been observed against
it; the ticket says to file it separately if one ever is. Leave it alone — a "while I'm here"
edit to `Evict` is a scope violation, not a bonus.

### Doc comment (AC 4)

`Activate`'s doc comment currently says the call blocks "…AND the supervisor has bound its PTY
(or ctx is cancelled)", which reads as an unconditional guarantee. Amend it to state, in two or
three lines:

- A context that is **already cancelled or expired when `Activate` is called** returns
  `ctx.Err()` immediately, before any re-activation is requested. No `activateCh` signal, no
  supervisor start, no child.
- A cancellation that **arrives while the call is waiting** still returns `ctx.Err()`, except
  that a transition completing at the same instant may win the race — the fail-fast guarantee
  is scoped to "cancelled before the call".

Draw that line explicitly. The over-broad version of this claim is what the package overview
already gets wrong (§ Documentation follow-up), and the doc comment is what a reader reaches
first.

**Citation discipline:** name symbols, never lines. `make cite-guard` fails the build on a
`//`-comment citation that resolves into a declaration, at any depth, ranges included. Write
``the `activateCh` send in `Activate` `` and ``see `runEvicted` ``; never `session.go:287` and
never a bare `:NNN`.

### Consumer impact — none to migrate

The signature is unchanged, so no call site is edited. All four production paths reach
`Session.Activate` through `(*Pool).Activate`, and every one of them propagates the error and
does nothing else on failure:

| Call site | Behaviour with an already-cancelled ctx, after the change |
|---|---|
| `internal/sessions/get_or_create.go` (register path) | Returns `(id, ctx.Err())` — as today, minus a spawn nobody waits for. |
| `internal/sessions/pool.go` → `Create` | Same. The registry entry is already persisted; a later attach activates it. |
| `cmd/pyry/main.go` → `boundSession.Activate` (the `internal/control` `Session` seam) | Pass-through; error surfaces to the control-plane caller. |
| `cmd/pyry/main.go` → `newInboundDeliver` | Derives `activateCtx` from the drain context with a timeout. If the drain context is already cancelled, the delivery now fails fast instead of spawning a child for a message it will not deliver. `errors.Is(err, context.Canceled)` still holds, so msgqueue's retry classification is unchanged. |

No caller relies on the warm-up side effect of a cancelled `Activate`. Nothing to route back.

## Concurrency model

Unchanged. No new goroutine, channel, or lock. The guard reads `ctx.Err()` — safe from any
goroutine, no `lcMu` involvement — and returns before touching any session state. Lock ordering,
the buffered-signal collapse, and the `transitionTo` persist ordering are all untouched.

The one ordering fact worth stating: the guard must precede `s.lcMu.Lock()`. That is the whole
of AC 2.

## Error handling

- Already-cancelled context → `ctx.Err()` verbatim, unwrapped. Callers match with `errors.Is`
  against `context.Canceled` / `context.DeadlineExceeded`; wrapping would be a gratuitous
  contract change and the existing `select` arm returns `ctx.Err()` bare too.
- Live context → every existing path is byte-identical to today.
- No new failure mode, no new log line. The guard is not worth a `slog` call: it fires on a
  caller that has already given up, and the caller gets the error.

## Testing strategy

Two new tests in `internal/sessions/session_test.go`. Both use **bare `Session` literals with no
pool, no lifecycle goroutine, and no child process** — the pattern already established by
`newEvictedSession` in `TestSession_Run_RemovedCh_ExitsClean`. That is what makes them
deterministic: no sleep, no `pollUntil`, nothing scheduler-dependent in either direction.

`Activate` touches only `lcMu`, `lcState`, `activeCh`, `activateCh` and `s.sup`, so a literal
with `sup: fakeRunner{}` and a `nil` pool exercises the real code path end to end.

### Test 1 — already-active session, 400 consecutive calls (AC 1)

Fixture: bare `Session` literal, `lcState: stateActive`, `activeCh: closedChan()`,
`evictedCh: make(chan struct{})`, both signal channels buffered 1, `sup: fakeRunner{}`,
discard logger. The closed `activeCh` **is** "the transition has completed", by construction.

Scenario:

- Build one cancelled context (`context.WithCancel` + immediate `cancel()`).
- Call `Activate` with it `activateCalls` times (a named const, 400).
- Every call must satisfy `errors.Is(err, context.Canceled)`. Fail on the first one that does
  not, reporting the iteration index and how many `nil`s were seen so far — a count makes the
  failure legible as "leaked N of 400" rather than "failed once".

Why this reddens under the mutant: with the guard removed, both `select` arms are ready, so the
`<-ch` arm is taken ~50% of the time and `fakeRunner.WaitForPTY` then returns `nil`. Over 400
calls the probability of zero `nil`s is `2^-400`.

### Test 2 — evicted session, no re-activation requested (AC 2)

Fixture: bare `Session` literal, `lcState: stateEvicted`, `activeCh: make(chan struct{})`,
`evictedCh: closedChan()`, both signal channels buffered 1, `sup: fakeRunner{}`, discard logger.
**No `Run` goroutine** — nothing drains `activateCh`, which is what makes the assertion below
deterministic rather than a race against a wake-up.

Scenario — one call, three assertions:

- `Activate(cancelled)` returns an error satisfying `errors.Is(err, context.Canceled)`.
- `len(sess.activateCh) == 0` — no re-activation was ever requested.
- `sess.LifecycleState() == stateEvicted` — the session is where it was.

Why the channel-depth assertion is the right observable for "spawns no child": the buffered
`activateCh` send is the **only** thing that lets `runEvicted` return `nil`, which is the only
path by which `Run` reaches `transitionTo(stateActive)` and starts a supervisor. Asserting the
trigger was never armed is deterministic; observing the *absence* of a spawn is a timed negative
that can only ever be "not yet". Under the mutant this reads exactly `1` — the session is not
active, so the non-blocking send on an empty buffered channel always succeeds.

**Deliberately not added: a live-pool variant that waits ~2s and asserts the session is still
evicted.** It is strictly weaker (a timed negative cannot be red 10 times out of 10, which AC 3
requires), it is slower, and it adds flake surface in the false-red direction — the exact defect
shape #1802 was filed for. Determinism here comes from removing the racing goroutine, not from
adding a second stochastic observation.

### The fixture trap — read this before writing either test

**Do not build these tests on `lifecycleRunner`.** Its `WaitForPTY` is a second `select` on
`ctx.Done()`, and it will quietly wreck the mutant proof in one of two ways:

- If its `readyCh` is closed (the lifecycle goroutine ran), it is a second coin flip: the
  observed leak drops from ~1-in-2 to ~1-in-4, and the test measures the fixture rather than
  production.
- If its `readyCh` is **open** (a literal that never ran `Run` — exactly what these tests build),
  `WaitForPTY` sees only `ctx.Done()` as ready and returns `context.Canceled` every time. The
  mutant then goes **green**, AC 3 fails, and the failure looks like "the guard wasn't needed".

`fakeRunner.WaitForPTY` returns `nil` unconditionally, which is precisely the production
`(*streamsup.Runner).WaitForPTY` shape. It is the only correct stand-in here.

### Existing tests

`TestSession_ActivateCtxCancellation` stays exactly as it is. The fix *cures* its flake rather
than obsoleting it: it drives an evicted session with a live lifecycle goroutine, and its one
observed red needed the re-activation to complete before the caller reached the `select`. With
the entry guard the caller never reaches the `select` at all, so that window closes. Leave it —
it is the only test covering the cancelled-Activate path with a real lifecycle goroutine
attached, and it costs nothing.

`TestSession_ActivateNoOpWhenActive` passes a 500 ms deadline context that is live at entry, so
the guard does not fire. No other test in the repo calls `Activate` with a pre-cancelled
context. No existing test needs an edit.

### Mutant verification (AC 3)

Required evidence, all of it producible **without a worktree write** using `go test -overlay`
(this is a hard requirement — the guard must be removed from the *compiled* tree, never from the
committed one):

1. Copy `internal/sessions/session.go` to a file outside the repo, delete the entry guard from
   the copy, and write an overlay mapping the repo path to it. Both sides must be **absolute**
   paths:
   ```json
   {"Replace": {"/abs/.../internal/sessions/session.go": "/abs/tmp/session_mutant.go"}}
   ```
2. **Mutant, 10 consecutive runs:** `go test -overlay=<abs overlay.json> -race -count=1
   ./internal/sessions/`. Every run must FAIL, and **both new tests must appear in every run's
   FAIL list.** The full package takes ~9.5 s per run on the reference machine, so 10 runs is
   ~95 s — the AC's literal command is affordable; do not substitute a `-run` filter for it.
   Judge by the FAIL list, not by the exit code: the package carries a known unrelated flake
   (`TestPool_Run_StartsWatcher`, 1 in 48 per the ticket), so an exit code alone cannot tell
   "the mutant was caught" from "something else reddened".
3. **Clean tree, no false red:** `go test -race -count=10 -run 'TestSession_Activate'
   ./internal/sessions/` must pass. The new tests are sleep-free, so this is near-instant.
4. `make check` green.

Record the outcome — the 10-of-10 FAIL tally and the two test names present in each run — as a
short `## Verification log` section appended to **this spec file**. That is the only file
outside `cmd/` and `internal/` the implementation may touch, and it is what makes AC 3
auditable at review time rather than a claim in a PR body.

## Documentation follow-up (documentation phase — not this ticket's developer)

`docs/knowledge/features/sessions-package.md` makes two claims this change narrows. Both are
owned by the documentation phase; **the developer must not edit that file** (it is outside the
mutable set, and knowledge-base edits are deliberately not acceptance criteria here).

1. § "Pool.Create (1.1a-A2)" → the **"ctx cancellation race"** paragraph states that
   `sess.Activate` "may have already sent the buffered signal on `activateCh` before its ctx
   check", and concludes that tests "should not depend on 'Activate error → claude not running'
   as a hard invariant". After this ticket that paragraph is true only for a cancellation that
   arrives *mid-flight*. For a context already cancelled at the call, no signal is sent and the
   invariant **is** hard. The paragraph needs that line drawn, not deletion — the mid-flight race
   it describes is real and unchanged.
2. § "`Session`" → the `Activate(ctx)` bullet says "No-op when already active." That has been
   wrong since the no-early-return design landed (the `Activate` doc comment says so in as many
   words), and this ticket makes the already-active path the one that changed. Worth correcting
   in the same pass.

## Open questions

None blocking. Two judgement calls are settled above and recorded so review does not reopen
them: the guard goes above `lcMu` rather than inside the `select` (§ Design), and AC 2 is pinned
on `len(activateCh)` against a goroutine-free literal rather than on a timed "still evicted"
poll (§ Testing strategy).
