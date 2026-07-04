# Spec #789 — Gate fakerelay tests on `WaitBinary` to close the accept→register race

**Size:** XS (dropped from PO's `s`). Test-harness-only: one file, no production change, ~6 mechanical gate inserts around an existing barrier method. The ticket explicitly authorizes the XS drop for "a pure mechanical barrier insert."

## Files to read first

- `internal/e2e/internal/fakerelay/fakerelay.go:527-549` — `Server.WaitBinary(ctx, serverID) bool`. The existing readiness barrier: polls `s.binaries[serverID]` every 2ms until present or ctx done. This is the fix's sole primitive; **no change here.**
- `internal/e2e/internal/fakerelay/fakerelay.go:210-255` — `handleBinary` pre-upgrade claim check (L218) and post-accept registration insert (L254). The window between `websocket.Accept` (L225) and the `s.binaries[serverID] = bc` insert (L254) is the race. `TestBinaryUpgrade_FirstClaimWins`'s second dial reads the claim check at L218.
- `internal/e2e/internal/fakerelay/fakerelay.go:285-305` — `handlePhone`. Header validation (L289) returns 400 *before* the binary-existence check (L300) that returns 503. This ordering is why `TestPhoneUpgrade_RequiresAllHeaders` is **not** affected (see § Excluded).
- `internal/e2e/internal/fakerelay/fakerelay_test.go:425-483` — `TestForceCloseBinary` and `TestWaitBinary`. The **precedent to mirror**: `if !s.WaitBinary(ctx, "alpha") { t.Fatal("binary registration did not complete") }`. Copy this idiom verbatim.
- `internal/e2e/internal/fakerelay/fakerelay_test.go:79-82` — `dialCtx(t)` returns a 3s-timeout context; every gated test already has one in scope named `ctx`. Reuse it for the `WaitBinary` call.

## Context

Under full-suite `go test -race ./...` load, fakerelay tests that dial a *dependent* connection immediately after establishing a binary intermittently fail: a phone gets `503` "no binary online" instead of `101`, or a first-claim-wins conflict is missed. Always passes in isolation; failing test *set* varies run-to-run — the signature of a harness synchronization race, not a product defect.

Root cause: `websocket.Accept` (fakerelay.go:225) unblocks the dialing client (`101` returned) **before** `handleBinary` finishes inserting the binary into `s.binaries` (L254). A test that dials a binary and then immediately issues a dependent dial can observe stale server-side state:

- **Phone on `/v1/client`** → `handlePhone` sees no entry at L300 → `503`.
- **Second binary on `/v1/server`** → the pre-upgrade claim check at L218 sees no entry → accepts instead of returning `409`.

The harness already ships the exact barrier for this: `Server.WaitBinary`. `TestForceCloseBinary`/`TestWaitBinary` gate on it; the flaky tests do not. The fix is to add the same gate at every "establish binary, then issue dependent dial" site.

Filed from QA of PR #788 / #411, where a single-run baseline name-comparison risks misclassifying one of these non-deterministic failing names as a PR regression.

## Design

Insert a `WaitBinary` gate between establishing a binary and issuing the first dependent dial, at each affected test. The gate is the established idiom — no new helper, no production change:

```go
if !s.WaitBinary(ctx, "alpha") {
    t.Fatal("binary registration did not complete")
}
```

Behavior contract: `WaitBinary` returns `true` once `s.binaries["alpha"]` is populated (microseconds after Accept in practice; the 3s `dialCtx` is only a safety ceiling). Once it returns `true`:
- A subsequent phone dial for `"alpha"` passes the L300 existence check → `101`, never `503`.
- A subsequent second binary dial for `"alpha"` hits the L218 claim check with the entry present → deterministic `409`.

`false` (barrier timed out) must `t.Fatal` rather than silently proceed — mirroring the precedent tests, so a genuinely stuck registration surfaces as a clear failure, not a downstream 503.

### Call sites to gate (6)

Insert the gate after the binary dial succeeds (and after its `t.Cleanup`, where present), before the dependent dial:

| Test | Insert gate before | Why |
|---|---|---|
| `TestBinaryUpgrade_FirstClaimWins` (L110) | the second `dialBinary(ctx, t, s, "alpha")` at L122 | second dial's claim check (fakerelay.go:218) must see the entry → `409` |
| `TestPhoneToBinary_FrameWrappedWithConnID` (L201) | `dialPhone` at L213 | phone existence check (L300) must pass → no `503` |
| `TestBinaryToPhone_FrameUnwrapped` (L232) | `dialPhone` at L244 | same |
| `TestConnIDIncrementsPerPhone` (L272) | the phone `for` loop at L284 | gate **once** before the loop; registration is stable thereafter |
| `TestPhoneClosedWhenBinaryGoes` (L301) | `dialPhone` at L311 | same |
| `TestServerClose_NoGoroutineLeaks` (L333) | `ph1` dial at L345 | **sweep addition** — identical binary-then-phone shape, latent flake; not named in the report but same cause |

All six already have a `ctx` from `dialCtx(t)` in scope (`TestServerClose_NoGoroutineLeaks` at L338). `TestConnIDIncrementsPerPhone` uses one server-id for the whole loop, so a single gate before the loop suffices — do not gate inside the loop.

### Excluded (with reasoning — do not gate these)

- `TestPhoneUpgrade_RequiresAllHeaders` (L148) — binds a binary but every dependent phone dial deliberately omits a header, so `handlePhone` returns `400` at L290 **before** the binary-existence check at L300. A `503` flake is unreachable; a gate here would defend a failure mode that cannot occur. **Skip** (evidence-based).
- `TestRejectNextBinaryWith4409` (L393) — no dependent dial on a registered binary; the follow-up `beta` dial is a fresh server-id independent of `alpha`'s registration.
- `TestBinaryUpgrade_RequiresServerHeader` (L95) — failure-path dial (empty header → 400), no dependent op.
- `TestForceCloseBinary` (L425), `TestWaitBinary` (L453) — already gate on `WaitBinary`; they are the precedent, leave unchanged.

### Considered and rejected: a `bindBinaryReady` helper

A helper bundling dial + `WaitBinary` would DRY the six sites and structurally prevent future authors from forgetting the gate. Rejected because the raw `dialBinary` is still required for the failure-path tests (empty-header 400, 4409 close, second-claim 409) — a helper would cover only the happy path, leaving two ways to bind a binary. The explicit gate matches the two existing precedent tests and keeps the file uniform; introducing a helper would also pressure a refactor of those precedents (scope creep). "Respect existing patterns" + "touch only what's necessary" both point to the explicit gate.

## Concurrency model

No new goroutines, channels, or shared state. `WaitBinary` reads `s.binaries` under the existing `s.mu`. The gate serializes the test goroutine against the handler goroutine's registration insert via that lock + poll — which is precisely the barrier's purpose.

## Error handling

`WaitBinary` false → `t.Fatal("binary registration did not complete")` (mirror the precedent message). No other new failure modes.

## Testing strategy

The tests *are* the deliverable; verify the fix by stress, not by new test functions:

- `go test -race -count=50 ./internal/e2e/internal/fakerelay/` — green across all 50 iterations.
- Full-suite `make check` green (ideally reproduce the aggravator: a concurrent CPU-heavy build in another worktree on the same host).
- Spot-confirm each gated test still asserts its original behavior (conn_id wrapping/unwrapping, 409 conflict, orphan-phone close, no goroutine leak) — the gate only adds a barrier, it must not change what the test observes afterward.

Do **not** add new test functions or a phone-readiness barrier — there is no phone-side analogue of `WaitBinary` and no phone-ordering flake has been observed (evidence-based; out of scope per the ticket).

## Acceptance criteria

- [ ] Each of the six tests in § Call sites gates its dependent dial on `s.WaitBinary(ctx, <server-id>)`, `t.Fatal`-ing on false, before issuing the dependent dial (phone on `/v1/client`, or the second binary on `/v1/server`).
- [ ] `TestPhoneToBinary_FrameWrappedWithConnID` and `TestBinaryToPhone_FrameUnwrapped` never fail with a `503` handshake.
- [ ] `TestBinaryUpgrade_FirstClaimWins` deterministically observes the `409` conflict on the second same-server-id dial — never a spurious accept.
- [ ] `go test -race -count=50 ./internal/e2e/internal/fakerelay/` passes green; full-suite `make check` passes green.
- [ ] The change is confined to `internal/e2e/internal/fakerelay/fakerelay_test.go`; no production code under `cmd/` or `internal/` (including `fakerelay.go`) changes.

## Open questions

None. `WaitBinary` exists and is battle-tested by `TestForceCloseBinary`/`TestWaitBinary`; the fix is a mechanical application of an established in-file pattern.
