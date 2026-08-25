# #1802 — Pin the stability reset on the attempt counter, not on a wall-clock gap

Test-only change to `internal/transport`. No production change.

## Files to read first

- `internal/transport/wssclient.go` → `Client.Connect` — the reconnect loop. Extract two things: (a) the post-serve tail, where `uptime >= c.stabilityReset` chooses between `attempt = 1` and `attempt++`, is **the behaviour under test**; (b) that tail falls straight through to the next `c.dialFn` call with **no sleep**. `c.backoff` / `c.sleepCancellable` are reached only from the dial-*failure* path and from `retryFatalClose`. This asymmetry is the whole bug.
- `internal/transport/wssclient.go` → `Client.backoff` — confirms the ladder is `reconnectInitial << (attempt-1)` capped at `reconnectMax`, ±20% jitter. Needed only to understand why the old bound was built the way it was; the new test does not call it.
- `internal/transport/wssclient_test.go` → `TestBackoff_ResetAfterStableConnection` — the function being rewritten. Read its comment block before deleting it; AC 4 is about that block.
- `internal/transport/wssclient_test.go` → `newClientForTest`, `testOpts` — the only supported way to shrink the cadence constants (`stabilityReset`, `reconnectInitial`, `pingInterval`, …) and to inject `dialFn`. Note each field is applied only when `> 0`.
- `internal/transport/wssclient_test.go` → `newTestRelay`, `relayCtrl.ForceClose` — `ForceClose` calls `CloseNow` on the live conns and clears the slice; the `httptest.Server` **stays up**, which is why the redial after a healthy drop succeeds.
- `internal/transport/wssclient_test.go` → `recordingHandler` — the existing capturing `slog.Handler`. `Handle` stores `r.Clone()` under `h.mu`, so iterating `Attrs` later is safe. It has no query helpers yet; this ticket adds them.
- `internal/transport/wssclient_test.go` → `TestConnect_LoudOnFirstUpgradeReject` — the house idiom for *both* halves of this design: a `recordingHandler` used as the assertion surface, and a `deadline := time.Now().Add(…)` poll loop used as a liveness guard. Mirror it.
- `docs/knowledge/features/transport-package.md` § "Cadence (locked at wire-spec level)" and § "Test surface" — the stability-reset row and the one-line description of this test. Read for context; the documentation phase owns the file.

## Context

`TestBackoff_ResetAfterStableConnection` is broken in both directions, and both breakages have one cause.

- **False red.** It reddens under whole-package load. Reproduced on 2026-08-25 in this worktree: 10 rounds of 4-way-concurrent `go test -race -count=1 ./internal/transport/` reddened it twice, with gaps `227.4ms` and `419.6ms` against its `≤224ms` bound.
- **False green.** Replacing the stability-reset branch in `Connect` with an unconditional `attempt++` — i.e. deleting the behaviour the test names — leaves it passing.

The cause: the post-serve path in `Connect` re-dials immediately. `backoff(attempt)` sleeps only after a *failed* dial. So the interval the test measures — success dial → next dial after a healthy drop — contains **no term controlled by `attempt`**. It is the test's own 150 ms hold plus scheduler noise. The bound `150ms + reconnectInitial*1.2 + 50ms` models a backoff sleep that never happens; the test's comment asserts that sleep exists. Noise dominates because signal is absent, which is exactly why load reddens it and why deleting the reset does not.

This is the only test in the package that exercises the stability reset, so the reset has no effective coverage today. Both the assertion and the comment have stood unchanged since #247.

No ADR is warranted. The design lesson — *a flaky wall-clock bound is often also vacuous; trace which sleeps the path actually contributes before loosening a bound* — belongs in the package overview's § Test surface, which the documentation phase owns.

## Design

### The observable

Replace the timing measurement with the attempt counter itself, read off the log record that already carries it. `Connect` emits `Logger.Info("transport: connected", "attempt", attempt)` on every successful dial, before serving. No production change is needed to observe it.

For the fixture below (two synthetic dial failures, then a success, then a forced drop, then a redial), the two `"transport: connected"` records read:

| tree | 1st connected | 2nd connected |
|---|---|---|
| unmodified | `attempt=3` | `attempt=1` |
| reset branch → unconditional `attempt++` | `attempt=3` | `attempt=4` |
| reset branch → unconditional `attempt = 1` | `attempt=3` | `attempt=1` |

The verdict is an integer comparison. Nothing in it is a duration.

### Two arms, not one

The reset branch has two outcomes and the ticket's mutant only exercises one. Assert both, or an unconditional `attempt = 1` mutant survives (see the table's third row). The two arms differ only in `stabilityReset` and the expected second attempt, so a two-row table-driven test is the natural shape here and matches § Testing in `CODING-STYLE.md`.

| row | `stabilityReset` | hold before `ForceClose` | want 2nd attempt |
|---|---|---|---|
| stable uptime resets the counter | 20 ms | 100 ms | `1` |
| brief uptime does not reset | 10 min | none | `4` |

Each row stands up its own `relayCtrl` and `Client`; there is no shared state between rows.

**Both rows are load-monotone — load can only push uptime *up*.**

- Row 1 needs `uptime >= stabilityReset`. The hold is a `time.Sleep`, which only ever oversleeps, and it starts **after** the client stamped `connectedAt` (see § The hold must be anchored client-side). So real uptime ≥ 100 ms ≫ 20 ms under any load.
- Row 2 needs `uptime < stabilityReset`, and 10 minutes is unreachable inside a test that is torn down in well under a second.

Neither row has an upper bound on anything the scheduler controls. That is what makes AC 2 hold.

### The hold must be anchored client-side

The old test started its hold on `relay.connectedCh`, which the relay handler signals from inside `newTestRelay`'s handler at `websocket.Accept` — i.e. **before** the client's dial returns and before `connectedAt` is stamped. Under scheduler pressure the hold and the client's dial completion overlap, so effective uptime can be shorter than the sleep.

Anchor on the first `"transport: connected"` record instead. `Connect` emits it immediately after stamping `connectedAt`, with only the `warnedUpgrade` reset in between, so the hold begins microseconds after the clock the production code compares against. Row 1's margin then depends on nothing the scheduler can shrink.

Row 2 uses the same anchor for a different reason: it must not force-close before the client considers itself connected, or there is no second dial to observe.

### Required non-vacuity guard

Assert the **first** connected record carries `attempt=3` before asserting anything about the second. Without it, a fixture that stopped injecting dial failures would enter the post-serve tail with `attempt=1` already, and row 1 would pass whatever the branch does. The guard costs one line and pins that the ladder actually climbed.

### Test-side surface to add

Two unexported helpers in `wssclient_test.go`, alongside `recordingHandler`:

```go
// connectedAttempt returns the "attempt" value on the nth (1-based)
// "transport: connected" record captured so far, and whether that record
// exists yet. Takes h.mu.
func (h *recordingHandler) connectedAttempt(n int) (int, bool)

// waitConnectedAttempt blocks until the nth "transport: connected" record
// exists and returns its "attempt" value; t.Fatalf on deadline. Liveness
// guard only — see § What is still a duration, and why that is fine.
func waitConnectedAttempt(t *testing.T, h *recordingHandler, n int) int
```

`connectedAttempt` scans `h.records` for `r.Message == "transport: connected"`, counts to `n`, then pulls the attr via `r.Attrs` matching key `"attempt"` and `a.Value.Int64()`. Report the attr-missing case as "not found" rather than `0`, so a renamed attr key cannot read as a legitimate attempt value.

`waitConnectedAttempt` is the `TestConnect_LoudOnFirstUpgradeReject` poll loop with a ~10 s deadline and a ~2–5 ms tick. Make its `t.Fatalf` self-diagnosing: it fires when either the message text or the attr key was renamed, and a bare "timed out" sends the next reader hunting a deadlock that isn't there. Include the distinct record messages seen, or at least the count of `"transport: connected"` records found.

The `Client` construction is the existing fixture with three changes: `Logger: slog.New(rec)` in place of `testLogger(t)`, `stabilityReset` from the row, and a `dialFn` that fails the first two calls and dials `relay.URL()` thereafter. The dial index can come from an `atomic.Int64` — the old `dialMu`/`dialTimes` bookkeeping exists only to serve the timing assertion and goes away with it. Keep `conn.SetReadLimit(maxFrameBytes)` on the success path.

### Name and comments

- The top-level function keeps the name `TestBackoff_ResetAfterStableConnection`. AC 1 names it, and CI/`-run` filters and the package overview's § Test surface reference it. Subtests carry the per-arm names.
- Delete the existing comment block entirely (AC 4). Its replacement should state the mechanism the test now relies on: **the post-serve path redials immediately, so the attempt counter is observable only through the connected log record, never through a dial interval.** Write it so the next reader does not re-derive a timing bound.
- Cite symbols, never lines — `make cite-guard` is diff-scoped and will fail the branch on a new `file.go:NNN` comment, ranges included.

### Alternatives considered

- **Loosen the bound.** Rejected: it removes the red and keeps the false green, which is the worst outcome. The 856 ms sample the ticket reports is out of scale for a `backoff(1)` capped at 24 ms — proof the assertion measures something else.
- **`Client.Connected()`.** It signals connection but carries no attempt value, so it cannot distinguish the arms.
- **Export the counter / add a test hook.** A production change for a test-only need, and the log record already carries the value.
- Asserting on log content is already the idiom in this file (`TestConnect_LoudOnFirstUpgradeReject` asserts on message text). The coupling to the message string fails *closed* — a rename times out red, never silently green.

## Concurrency model

Unchanged from the existing test. Per row:

- One goroutine runs `c.Connect(ctx)`, its return delivered on a buffered `connectErr` channel.
- `t.Cleanup` calls `c.Close()` then drains `connectErr`, so `Connect` is joined even when the body `t.Fatalf`s mid-wait.
- The test goroutine reads `recordingHandler.records` under `h.mu`; `Handle` appends `r.Clone()` under the same mutex from the `Connect` goroutine. `Record.Clone` is what makes the later `Attrs` walk safe.
- `dialFn` runs on the `Connect` goroutine; its call index must be atomic (or mutex-guarded) because `Client.Close` can race the loop at teardown.
- `ctx` gets a generous timeout (≥ the wait deadline; 30 s is fine) purely so a wedged run dies with a diagnostic instead of hanging the package.
- `t.Parallel()` at whichever level the developer prefers. Rows share nothing.

## Error handling

Test-only; the failure modes are diagnostic quality.

| failure mode | how it surfaces |
|---|---|
| reset branch broken (either direction) | `t.Errorf` naming got/want attempt and the arm |
| fixture failed to climb the ladder | the `attempt=3` guard reddens before the real assertion runs |
| log message or attr key renamed | `waitConnectedAttempt` `t.Fatalf` listing the messages it did see |
| relay never accepted / client wedged | same `t.Fatalf`, then `ctx` timeout at the outer bound |
| spurious dial failure after the drop | 2nd record reads `attempt=2` → red, not green. Optionally include the count of `"transport: connected"` and `"dial failed"` records in the message so this is diagnosable at a glance |

### What is still a duration, and why that is fine

AC 3 bans a verdict that depends on a wall-clock threshold. Three durations remain, none of them a verdict:

- **Row 1's 100 ms hold** — a *stimulus*, driving the production comparison into its `>=` arm. Load lengthens it; it cannot shorten.
- **`waitConnectedAttempt`'s deadline and the `ctx` timeout** — *liveness guards*. They convert a hang into a readable failure. They are the same construct `TestConnect_LoudOnFirstUpgradeReject` already uses, and a green run never approaches them (the verified implementation finishes in ~0.24 s against a 10 s+ deadline). Being generous costs nothing on green.

What AC 3 rules out is the thing being deleted: `elapsed > computedBound`. No `time.Since` / `Sub` result may be compared against an expectation.

## Testing strategy

The design was verified in this worktree on 2026-08-25 against a probe implementation, run via `go test -overlay` so nothing was written into the package. Reproduce with the same technique when the real change is in place.

**Mutation (AC 1).** Overlay a copy of `wssclient.go` with the post-serve stability branch in `Connect` replaced, run `go test -race -count=1 -run TestBackoff_ResetAfterStableConnection ./internal/transport/`:

| mutant | expected |
|---|---|
| branch → unconditional `attempt++` (the mutant AC 1 names) | row 1 red: `second connected attempt = 4, want 1`; row 2 green |
| branch → unconditional `attempt = 1` | row 2 red: `second connected attempt = 1, want 4`; row 1 green |
| unmodified | both green |

Both mutants were confirmed killed, each by exactly one row — so neither row is carrying the other, and each arm has sole coverage.

**Load (AC 2).** 10 rounds of 4-way-concurrent `go test -race -count=1 ./internal/transport/` — the whole package, not a `-run` filter; the reported trigger is package-wide contention.

Measured with the probe overlaid, 40 package runs / 80 executions of the new rows:

- new rows: **0 failures**
- `TestBackoff_ResetAfterStableConnection` as it stands today (control, same harness, same runs): **2 failures** — `227.4ms` and `419.6ms` against `≤224ms`

The control matters: it is what proves the harness actually reproduces the reported flake rather than measuring an idle machine.

**Gate.** `make check`.

### Out of scope, recorded so it is not lost

The same 40-run load sweep also reddened, on the unmodified tree:

- `TestConnected_FiresOnEveryConnect` — 3 of 40
- `TestPing_FiredAt30s` — 1 of 40 (the ticket already notes a single earlier observation of this one)

Both are pre-existing package flakes under whole-package load, unrelated to the stability reset. **Do not fix them here** — that is scope creep, and neither is named by an AC. `TestConnected_FiresOnEveryConnect` at 3-in-40 is the stronger signal of the two and deserves its own Inbox ticket.

## Open questions

- Whether row 2 lands as a second table row or a separate top-level test is the developer's call, provided `TestBackoff_ResetAfterStableConnection` survives by name and covers the reset arm. The table is the smaller diff.
- The 20 ms / 100 ms and 10 min figures are the verified values, not sacred. Any pair keeping the hold several times `stabilityReset` (row 1) and `stabilityReset` unreachably large (row 2) preserves the load-monotonicity argument.
- `docs/knowledge/features/transport-package.md` § "Test surface" describes this test by its old mechanism. The documentation phase owns that file and should refresh the line; it is **not** a developer deliverable.
