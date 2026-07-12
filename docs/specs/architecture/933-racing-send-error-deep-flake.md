# #933 — TestFatalCloseCodes_HaltsReconnect_RacingSendError: the deep close-status-loss flake

**Size:** S (test-only, one file). May land XS if diagnosis confirms a small busy-Send tweak clears it. Do **not** grow past `internal/transport/wssclient_test.go`.

**Security-sensitive:** No (no `security-sensitive` label). This is a test-timing fix; the design surface is unchanged. Skip the security-review pass.

## Files to read first

- `docs/knowledge/codebase/523.md` — **start here.** The parent ticket. Read § "Matrix-C residual flake at the 15s budget is filed, not chased" and § "A production-side knob that doesn't move the failure rate when ratcheted is not the bottleneck". Two facts drive this spec: (a) #933 is the deferred *deeper* shape (`Connect returned context deadline exceeded`, not `did not return before ctx deadline`); (b) bumping `closeFrameGrace` (50 → 250 → 500 ms) did **not** move this shape's rate.
- `docs/knowledge/codebase/290.md` — the `coder/websocket` `prepareRead.done()` clobber mechanism (§ "Lessons learned") and "Order B′" (recvPump never surfaces a close status within grace → close status lost). This is the loss mechanism #933 hits under heavier load.
- `internal/transport/wssclient_test.go:937-1038` — `TestFatalCloseCodes_HaltsReconnect_RacingSendError`. The **only file the fix modifies.** The operative surface is the busy-Send loop (L994-1006) and the result-check select (L1015-1030). The failing assertion is L1025-1027 (`errors.Is(err, ErrFatalClose)`); the observed value is `context deadline exceeded`.
- `internal/transport/wssclient_test.go:863-935` — `racingCloseRelay`: single-shot accept, echo-drains client writes, rejects every reconnect dial with HTTP 410 (`http.StatusGone`). Read this to understand why a *lost* close status is fatal to the test: Connect then reconnect-loops against a 410 relay until the ctx deadline.
- `internal/transport/wssclient.go:443-479` — `serve`: `awaitCloseStatus(errCh, grace)` → `cancel()` → drain remaining slots → preference loop → **`return errs[0]` (L479)**. L479 is the AC2 regression target: reverting the preference loop to unconditional `return errs[0]` must still fail the test. **Read-only — do not modify.**
- `internal/transport/wssclient.go:482-513` — `awaitCloseStatus` + its docstring (the `prepareRead.done()` rationale). **Read-only.**
- `internal/transport/wssclient.go:203-289` — `Connect`'s dial/serve/backoff loop. Trace the path a *non-fatal* `serve` return takes: redial → 410 → `backoff` → redial → … until `ctx.Err()` at L280-282. This is the source of the `context deadline exceeded` return value.
- `internal/transport/wssclient.go:296-323` — `Send`: returns `ErrNotConnected` / `ErrDisconnected` the moment the live conn drops. The busy-Send loop (`for { if err := c.Send(...); err != nil { return } }`) therefore self-terminates shortly after the close — it does **not** spin for the full 15 s. The operative contention window is the *grace window inside `serve`*, during which the loop is still hot.
- `internal/transport/wssclient_test.go:1040-1123` — `TestAwaitCloseStatus_GraceBranchPreservesCloseError` (sibling pin, orderings A/B/B′). Must stay green at `-race -count=10`.
- `internal/transport/wssclient_test.go:786-817` — `TestFatalCloseCodes_HaltsReconnect` (sibling pin). Must stay green at `-race -count=10`.
- `internal/transport/wssclient_test.go:32-68` — `testOpts` + `newClientForTest`. `closeFrameGrace` is overridable via `opts.closeFrameGrace > 0` — needed only for the Phase-1 baseline that *re-confirms* grace bumps don't move this shape's rate before the developer abandons that lever.

## Context

Under a full `go test -race ./...` on a loaded machine, `TestFatalCloseCodes_HaltsReconnect_RacingSendError` intermittently fails with:

```
Connect returned context deadline exceeded, want wrapping ErrFatalClose
```

This is a **distinct shape** from the one #523 already fixed. Do not conflate them:

| Shape | Message | Cause | Status |
| --- | --- | --- | --- |
| Test-budget race | `Connect did not return before ctx deadline` | Buffered `connectErr` send vs. `<-ctx.Done()` in the test's select | Fixed by #523 (grace-drain in the select, L1015-1024) |
| **Deep close-status-loss (#933)** | `Connect returned context deadline exceeded` | `Connect` genuinely returns `ctx.Err()` — it never observed the fatal close in budget | This ticket |

`#523` explicitly filed-and-deferred this deeper shape with a documented re-open condition ("if it surfaces in real, non-synthetic telemetry"). It surfaced 2026-07-11 on a developer's real `make check` — the condition is met.

### Confirmed static trace of the failure (the developer's *starting hypothesis*, to be confirmed by Phase-2 instrumentation — not a licence to skip Phase 1)

1. Connected; the busy-Send loop primes in-flight `sendPump` writes.
2. `relay.TriggerClose()` → relay closes the conn with status 4409.
3. In `serve`: `sendPump`'s mid-write Write fails first with a generic net error (no close status). `awaitCloseStatus` enters the grace branch and waits up to `closeFrameGrace` (50 ms) for `recvPump` to surface the peer's 4409 `CloseError`.
4. Under CPU saturation stacked with the test's own unthrottled busy-Send loop, `recvPump`'s goroutine does not get scheduled to drain the buffered close frame within the grace window. Grace expires; `serve` calls `cancel()`; `prepareRead.done()` then clobbers `recvPump`'s pending Read with `ctx.Err()` (the #290 mechanism). **No slot of `errs[]` carries a close status.**
5. `serve` returns a non-fatal error. `Connect`'s `CloseStatus(serveErr) == -1` check (L267) fails to match → reconnect path.
6. Redial hits the single-shot relay → HTTP 410 → `backoff` → redial → … until the 15 s ctx deadline fires (L280-282) → `Connect` returns `ctx.Err()` = `context deadline exceeded`.

**Why the deadline bump and the grace bump are both ruled out as the fix** (per #523's evidence, re-stated here so the developer doesn't re-litigate): the loss in step 4 is *goroutine-starvation-bound*, not *wall-clock-bound*. Widening the grace gives `recvPump` more wall-clock but not more scheduler slots, so the rate doesn't move; widening the ctx deadline only postpones step 6's `ctx.Err()` without ever making step 4 succeed. The only lever left is the **test's own contribution to the starvation** — the unthrottled busy-Send loop — which is test code, which is why this stays a test-only fix.

## Design

**Diagnose-first, branching fix.** No code ships before reproduction *and* confirmation that the chosen lever moves the rate. This mirrors the #290/#523 protocol; the difference is that two candidate levers (deadline, grace) are pre-ruled-out by inherited evidence, so Phase 1 uses them only as a *baseline* and Phase 2 targets the busy-Send path.

### Phase 1 — Reproduce and re-baseline the ruled-out levers

Run each matrix to completion (collect counts; do not stop on first failure). Record the failure *message* per run — only `context deadline exceeded` counts as a #933 repro; a `did not return before ctx deadline` is the already-fixed shape and should not appear.

| Matrix | Command |
| --- | --- |
| A — race, default GOMAXPROCS | `go test -race -run '^TestFatalCloseCodes_HaltsReconnect_RacingSendError$' -count=200 ./internal/transport/...` |
| B — race, GOMAXPROCS=1 | `GOMAXPROCS=1 go test -race -run '^…RacingSendError$' -count=200 ./internal/transport/...` |
| C — race + CPU saturation | Matrix A's command while a second terminal runs `yes >/dev/null & yes >/dev/null & yes >/dev/null &` (kill the loaders after). |

Expected from #523: the repro is contention-dominated (matrix C, possibly A at high count). If **no** matrix reproduces at `-count=200`, escalate the count (500, 1000) and lengthen the C-load before concluding no-repro — this shape is rarer than the #523 shape. A genuine no-repro after `-count=1000` + sustained C-load routes to the *no-repro branch* below.

**Baseline the ruled-out levers once (to confirm the inherited evidence still holds on this tree, not to adopt them):** on the reproducing matrix, re-run at the current 15 s deadline and again with `testOpts.closeFrameGrace` bumped to 250 ms. If either materially moves the rate, the inherited evidence is stale — stop and re-rank (unlikely; #523 measured a flat rate across a 10× grace sweep). Otherwise proceed to Phase 2 with both levers abandoned.

### Phase 2 — Instrument, confirm the loss mechanism, then fix the busy-Send path

**Instrumentation (temporary, REMOVE before commit).** Add `t.Logf`/`Logger.Info` calls in `serve` (or feed via the test logger) recording, per serve invocation: (a) whether the grace branch was entered, (b) whether `recvPump`'s error arrived within grace and its `websocket.CloseStatus`, (c) which `errs[]` slot the preference loop returned and its status. Re-run the reproducing matrix.

The logs discriminate two sub-cases:

| Sub-case | Logs show | Read |
| --- | --- | --- |
| **i (expected)** | grace entered; `recvPump` did **not** return within grace (clobbered to `ctx.Canceled`); returned slot has `CloseStatus == -1` | **recvPump starvation** — the busy-Send loop's contention is the operative cause. Proceed to the fix below. |
| **ii (off-model)** | one `errs[]` slot carries 4409 but the preference loop returned a non-close slot | A `serve` preference-loop bug, not a test flake. This would also break `TestAwaitCloseStatus_GraceBranchPreservesCloseError`. **Out of scope for #933** — stop, file a follow-up, route back to PO. Do not fix production `serve` under this ticket (AC4). |

**The fix (sub-case i): relieve the busy-Send loop's scheduler monopolization without weakening the in-flight-write priming.**

The busy-Send loop exists to keep a `sendPump` Write in flight at close time, so `sendPump` errors *first* (feeding a non-close error into `errCh` ahead of `recvPump`) — this is what exercises the preference loop and gives the test its regression-catching power (AC2). The loop does **not** need to be an unthrottled spin: it needs a Write in flight across the ~tens-of-ms close-propagation window, not maximal CPU occupancy that starves `recvPump` during the grace window.

Weigh these two shapes; confirm the chosen one moves the rate on the reproducing matrix *before* finalizing:

- **(a) Cooperative yield — leading candidate.** Insert a `runtime.Gosched()` per loop iteration. Keeps Send throughput high (priming preserved) while returning a scheduler slot to `recvPump` each iteration. Smallest, most idiom-neutral change; least likely to weaken AC2's priming. **Prefer this if it moves the rate.**
- **(b) Light inter-send throttle.** A small fixed delay between sends (start at the low microsecond range; do **not** exceed a bound that lets a full `WriteTimeout` (50 ms) elapse between sends, or the loop stops priming). Stronger starvation relief than (a) but a tuning knob with a direct tension against AC2 — the larger the delay, the lower the probability a Write is mid-flight at close.

Pick the smallest change that clears the reproducing matrix at `-count=500`. Report the choice and its measured before/after rate in the PR description (same "ratchet the leading candidate, confirm it moves the metric" discipline #523 established).

### Constraint: do not weaken the regression contract (AC2)

The fix MUST preserve the test's ability to catch a `serve` regression. **Verification gate, run last, before commit:** temporarily revert `wssclient.go:479`'s preference loop to unconditional `return errs[0]`, run `-race -count=10` on the reproducing matrix, and confirm the test **fails on a meaningful fraction**. Restore the preference loop. If the reverted-serve run passes 10/10, the fix has made the busy-Send priming vacuous — the throttle is too heavy (shape b) or the yield removed the in-flight-write pressure; back off toward shape (a) / a lighter throttle and repeat. This gate is non-negotiable; it is the deterministic guard against a green-but-useless test.

### Constraint: scope discipline

One file: `internal/transport/wssclient_test.go`. **No production change** (AC4). Do not touch `serve`, `awaitCloseStatus`, `closeFrameGrace`, or `Config`/`testOpts` shape. Do not replace the `racingCloseRelay` busy-Send pattern with the `awaitCloseStatus`-unit-test approach — #290/#523 settled that they cover complementary cases; re-litigating is out of scope. If diagnosis lands on sub-case ii (a real `serve` defect) or otherwise shows the flake is *only* fixable in production, **do not expand scope** — file a follow-up issue and route back to PO. The knowledge note `docs/knowledge/codebase/933.md` is written by the documentation phase from this spec + the merged diff, **not** by the developer.

## Concurrency model

Unchanged in production. The `serve` invariant — "first errCh arrival → if no close status, wait up to grace for one more → cancel → drain remaining → return first slot with a close status, else `errs[0]`" — is exactly what the test exercises and is not modified. The only change is *how hard the test's busy-Send goroutine competes for the scheduler during that invariant's grace window*. The busy-Send goroutine's lifecycle (start before `TriggerClose`, self-terminate when `Send` errors after conn drop, joined via `stopSend`/`sendDone`) is preserved; only its per-iteration pacing changes.

## Error handling

No new error paths. The fix changes goroutine pacing in a test, nothing else. `Connect`'s error contract (`ErrFatalClose` vs. `ctx.Err()` vs. `ErrClosed`) is untouched.

## Testing strategy

The stress matrices in Phase 1 **are** the test plan — `TestFatalCloseCodes_HaltsReconnect_RacingSendError` is itself the regression test; there is no new test to add. Acceptance bar before commit:

- Reproducing matrix (whichever surfaced `context deadline exceeded`) passes at `-race -count=500` — no `context deadline exceeded` failures. (AC1)
- The AC2 verification gate above: reverted-`serve` (`return errs[0]`) still fails a meaningful fraction of `-race -count=10` on the reproducing matrix; restore serve after.
- `TestFatalCloseCodes_HaltsReconnect` and `TestAwaitCloseStatus_GraceBranchPreservesCloseError` pass at `-race -count=10`. (AC3)
- `go test -race ./internal/transport/...` green; `make check` green on the fix branch.

## Open questions

- **Does relieving the test's *own* contention clear the flake even under *external* matrix-C saturation?** The hypothesis is yes: the test's unthrottled busy-Send stacks on external load, and removing the test's contribution drops `recvPump` below the starvation threshold. If Phase-2 measurement shows the rate improves but does not reach 500/500 under sustained 3-core C-load, that is the boundary of a test-only fix — report the residual rate and its (extreme, synthetic) conditions in the PR rather than reaching for a production change or a further deadline bump. A residual flake *only* under 3-core saturation well beyond CI is a file-a-follow-up outcome, consistent with #523's own disposition of its residual.
- **Is `runtime.Gosched()` idiomatic here?** It reads oddly in application code but is a legitimate, minimal tool in a *test* whose explicit job is to shape goroutine scheduling for a race. Prefer it over a `time.Sleep` if it moves the rate, precisely because it does not introduce a wall-clock tuning constant. Justify the choice in a one-line comment citing #933.
