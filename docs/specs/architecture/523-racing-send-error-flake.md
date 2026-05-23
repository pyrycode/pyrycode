# #523 — TestFatalCloseCodes_HaltsReconnect_RacingSendError flake

## Files to read first

- `internal/transport/wssclient.go:374-448` — `serve` + `awaitCloseStatus`. The close-status preference loop and the 50 ms grace branch are the surface this ticket sits on. Read the docstring at L417-432 — it states the invariant the test pins.
- `internal/transport/wssclient.go:28-41` — cadence constants block, including `closeFrameGrace = 50 * time.Millisecond`. This is the candidate dial if diagnosis points at the production side.
- `internal/transport/wssclient.go:181-255` — `Connect`. Understand what happens when `serve` returns an error WITHOUT a recognizable close status: the dial loop spins (relay returns HTTP 410 on retry), and `Connect` only escapes via ctx cancellation. This is the failure shape the test catches.
- `internal/transport/wssclient_test.go:710-871` — `racingCloseRelay` helpers + `TestFatalCloseCodes_HaltsReconnect_RacingSendError`. The 5 s deadline literal is at L814; the failing assertion is at L862. The docstring at L784-796 states the contract the test pins; do not weaken it.
- `internal/transport/wssclient_test.go:873-956` — `TestAwaitCloseStatus_GraceBranchPreservesCloseError`. This sibling test pins the helper's contract directly (orderings A / B / B′ as named in #290's codebase note). It must remain green; if any fix touches `awaitCloseStatus`, walk these three sub-tests through the new code mentally before running them.
- `internal/transport/wssclient_test.go:28-68` — `newClientForTest` + `testOpts`. `closeFrameGrace` is overridable via `testOpts.closeFrameGrace > 0` — useful if the diagnosis branch wants the stress test to run with a wider grace.
- `docs/knowledge/codebase/290.md` — full design rationale for the grace branch, the `prepareRead.done()` override learned-the-hard-way lesson, and the explicit "Order B′" failure mode (recvPump never surfaces a close-status error within grace). This ticket is a stress-flake on the same surface; do not duplicate that note's content, extend it.
- `docs/knowledge/codebase/288.md` (if present) — sibling regression-test ticket. Skim only for context on why the busy-Send loop exists in this test.

## Context

`TestFatalCloseCodes_HaltsReconnect_RacingSendError` failed once during PR #522's CI, with `wssclient_test.go:862: Connect did not return before ctx deadline` and a 5.01 s test duration that aligns to the 5 s ctx deadline at L814. Five subsequent `-count=5` reruns of the same test on the same tree all passed; full `make check` on the PR branch and on the merge-base were green. The failing PR touched only `internal/agentrun/...`, never `internal/transport` — the flake is pre-existing.

The shape — duration ≈ ctx deadline, Connect never returning — is the same surface #290 hardened, but observed against `awaitCloseStatus`'s 50 ms grace under `-race` on macOS. Two unconfirmed hypotheses (from the issue body):

1. **Test-side budget.** The 5 s ctx deadline at `wssclient_test.go:814` is tight under `-race` + macOS load (race detector adds 5-10× to tight goroutine-coordination paths). If a healthy `Connect` returns just-after-5 s on a heavily loaded macOS runner, the test's `select { case <-connectErr: ; case <-ctx.Done(): t.Fatal }` flaps.
2. **Production-side grace.** `closeFrameGrace = 50 ms` is the time `awaitCloseStatus` waits for `recvPump`'s close-status arrival when the first errCh slot has none. If `recvPump`'s pending `conn.Read` doesn't get scheduled within 50 ms after sendPump's mid-Write error lands, the grace expires, `serve` cancels, `prepareRead.done()` clobbers `recvPump`'s pending error with `ctx.Err()` (the #290 mechanism), no slot of `errs[]` carries a close status, `Connect` reconnects forever against a relay that returns 410 — exactly the 5.01 s ctx-deadline shape.

Both hypotheses produce the same external failure. Diagnosis is required before the fix; **a production-side defense ahead of evidence is exactly what the pipeline's "Evidence-Based Fix Selection" rule warns against.**

## Design

The ticket is **diagnosis-first, branching fix.** No code change ships before reproduction. The developer follows the protocol below in order and lets the diagnosis output dictate the patch.

### Phase 1 — Reproduce under stress

Run each of the following matrices on macOS. Each runs to completion regardless of pass/fail (collect data, don't stop on the first failure). Record pass/fail counts per matrix.

| Matrix | Command |
| --- | --- |
| A — race, default GOMAXPROCS, count=200 | `go test -race -run '^TestFatalCloseCodes_HaltsReconnect_RacingSendError$' -count=200 ./internal/transport/...` |
| B — race, GOMAXPROCS=1, count=200 | `GOMAXPROCS=1 go test -race -run '^TestFatalCloseCodes_HaltsReconnect_RacingSendError$' -count=200 ./internal/transport/...` |
| C — race, default GOMAXPROCS, count=200, with background CPU load | Open a second terminal running `yes > /dev/null & yes > /dev/null & yes > /dev/null &` for the duration of matrix A's rerun. Kill the loaders after. |

Pass criteria for *no-repro*: all three matrices pass 200/200. Anything below is a reproduction.

### Phase 2 — Branch on reproduction outcome

#### 2a. No reproduction across all three matrices

Treat this as a single observed failure with no reproducible mechanism. Apply the test-side budget fix only — **do not** modify production code on speculation.

- Change `wssclient_test.go:814` from `5*time.Second` to `15*time.Second`.
- Add a one-line comment above the line: `// 15s allowances for -race on macOS; see #523.` No multi-line essay.
- Leave a brief paragraph in `docs/knowledge/codebase/523.md` (documentation phase writes this from the spec + merged diff; do not write it as part of the implementation PR — same convention as #471/#478) covering: matrices A/B/C run, no repro, test-side bump applied per the "single observation, no reproducible mechanism" path.
- Open follow-up in the PR description: "If this test flakes again, route here for stress repro with extended matrices (count=500, longer load duration, async load profile)."

PR description must state: *"Root cause not confirmed under stress matrices A/B/C (200/200 each). Applied test-side ctx-deadline bump as the minimum-risk patch; no production code change."*

#### 2b. Reproduction in matrix A or B (no extra load needed)

This is a production-side race the existing test surface is genuinely sensitive to. Instrument and identify the slot before patching.

**Instrumentation (temporary, REMOVE before commit):** Add four `c.cfg.Logger.Info` (or `t.Logf` via the test logger — use whichever is easier; the test logger already routes through `slog`) calls in `serve` around `awaitCloseStatus`, logging:

1. Time delta from `serve` entry to first errCh arrival, and `websocket.CloseStatus(first)`.
2. Whether the grace branch was entered.
3. Time delta from grace entry to second errCh arrival (or grace expiry).
4. After the preference walk, log which `errs[i]` was returned and its `websocket.CloseStatus`.

Re-run the reproducing matrix. The instrumentation will distinguish three sub-cases:

| Sub-case | What logs show | Patch |
| --- | --- | --- |
| **i.** Grace expires; no slot carries close status | grace = full 50 ms, returned err has CloseStatus = -1 | **Bump `closeFrameGrace`** at `wssclient.go:40` from `50 * time.Millisecond` to `250 * time.Millisecond`. Rationale: `recvPump`'s `conn.Read` needs to be scheduled-then-return after the close frame arrives in the OS buffer; under `-race` + load, 50 ms is below the 99th percentile of scheduler latency. 250 ms keeps the human-perceptible disconnect-to-reconnect latency bounded. Re-run matrix A 500× to confirm. |
| **ii.** Grace catches close, but Connect still doesn't see fatal status | one errs[] slot has CloseStatus = 4409, but Connect's post-serve `CloseStatus(serveErr)` check returns -1 | bug in the preference loop at `wssclient.go:410-415` — the `for _, e := range errs` walk should return the close-status error, not `errs[0]`. Investigate why it didn't. This would be a regression of the contract pinned by `TestAwaitCloseStatus_GraceBranchPreservesCloseError`. |
| **iii.** Close status is returned correctly, but `Connect` returns ctx.Err() before sending to `connectErr` | logs show `serve` returned a fatal-wrapped error, but the test still timed out | test-side race between the goroutine's `connectErr <- c.Connect(ctx)` send and the `<-ctx.Done()` arm of the test's select. **Bump the test ctx-deadline** to 15 s (same as 2a) and tighten the test's select: read connectErr in a follow-up step (after the deadline arm fires, drain `connectErr` non-blockingly and report) so a just-late return is still observable. |

Whichever sub-case applies: remove the instrumentation, apply only the matching patch, and re-run the reproducing matrix to ≥500 iterations of green before committing.

#### 2c. Reproduction only in matrix C (background load required)

Same as 2b sub-case **i** by default: `closeFrameGrace` is undersized under contention. Apply the 50 ms → 250 ms bump and confirm matrix C clears at 500×.

### Constraint: do not weaken the contract

The fix MUST keep both of these green at `-count=500`:

- `TestFatalCloseCodes_HaltsReconnect_RacingSendError` (this test, post-fix)
- `TestAwaitCloseStatus_GraceBranchPreservesCloseError` (helper-level pin from #290)

The close-status preference invariant — *"when the peer sends a fatal close, `Connect` classifies it as fatal regardless of which pump returns first"* — is not negotiable. If a candidate patch would require relaxing either test's expectations, that patch is wrong.

### Constraint: scope discipline

This is a one-file diagnostic ticket. Whichever fix branch fires, exactly one of `wssclient.go` OR `wssclient_test.go` is modified, plus a knowledge note `docs/knowledge/codebase/523.md` written by the documentation phase (not the developer). Do not refactor `serve`, do not restructure the pump-error model, do not extend `testOpts`. If diagnosis surfaces something deeper, file a follow-up issue and do not expand this ticket's scope.

## Concurrency model

Unchanged. The existing serve()/awaitCloseStatus invariant — "first errCh arrival → if no close status, wait up to grace for one more → cancel and drain remaining slots → return the first slot carrying a close status, else errs[0]" — is what the diagnosis tests. The only candidate production change is the grace duration, which preserves the invariant and only widens the time window for `recvPump` to surface a close frame.

## Error handling

Not applicable — no new error paths introduced regardless of which branch fires.

## Testing strategy

The diagnostic stress matrices in Phase 1 ARE the testing strategy. There is no new test to add — `TestFatalCloseCodes_HaltsReconnect_RacingSendError` is itself the regression test, and `TestAwaitCloseStatus_GraceBranchPreservesCloseError` is the helper-level pin. Acceptance bar:

- Reproducing matrix (whichever surfaced the failure) passes at `-count=500`.
- `go test -race ./internal/transport/...` passes (the full package).
- `make check` is green on the fix branch.

If 2a (no-repro path) is taken, run matrix A at `-count=500` post-patch as the bar — even though no production code changed, this is the cheap sanity check that the test-side bump didn't perturb anything else.

## Open questions

- **Should `closeFrameGrace` be exposed via `Config` so operators can tune it?** No — wire-internal timing, per #290's "Patterns established." If the stress-flake surfaces in production telemetry post-fix, revisit then; do not pre-empt.
- **Could the fix raise `closeFrameGrace` to something larger than 250 ms (e.g., 500 ms or 1 s)?** Bias toward keeping it as low as the stress test allows. The grace bounds the additional latency between *peer-initiated disconnect* and *the application learning about the disconnect* in the edge case where `sendPump` or `pingLoop` errors before `recvPump`; making it large pessimizes the disconnect path. 250 ms is the suggested ceiling; if 500× of matrix A still flakes at 250 ms, raise to 500 ms and re-evaluate (this is the only judgment call in the spec, and should be reported in the PR description rather than silently chosen).
- **Should the `racingCloseRelay` busy-Send pattern be replaced with the function-extraction approach (`awaitCloseStatus`-style unit testing) that #290 landed for the other angle?** No — they cover different cases. The busy-Send integration shape exercises the actual goroutine arrival ordering inside `serve`; the helper unit test pins `awaitCloseStatus`'s behavioural contract. Both are needed. Re-litigating that decision is out of scope here.
