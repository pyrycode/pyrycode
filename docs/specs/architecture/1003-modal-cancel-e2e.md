# Spec — #1003 test(e2e): `modal_cancel` over the v2 wire

**Size:** S (single new `internal/e2e/*_test.go` file, one test function, ~130 lines, zero new helpers, zero production files). **Not security-sensitive** (per PO; the verb sends a *fixed* `0x1b` via the same `SendEsc` seam as `interrupt` + an operator-global `modal_dismissed` broadcast — no attacker-content injection, no new path, no crypto).

## Context

The v2 control verbs handled inside `V2SessionManager.dispatchAppFrame` have method-level unit tests (`internal/relay/v2session_modal_test.go`) but uneven fake-daemon e2e coverage. Siblings `modal_answer`, `interrupt`, `dequeue_message`, `rekey_request`, `request_snapshot` have e2e; `modal_cancel` is one of four uncovered verbs from the 2026-07-15 review gap. This ticket adds the `modal_cancel` slice only (split from the oversized four-verb #962).

`handleModalCancel` (`internal/relay/v2session.go:2283`) consumes the named modal via the `ModalResolver.ResolveCancel` seam and — **only if** an outstanding modal was consumed — fans a `modal_dismissed` broadcast to every interactive-capable conn. It is fire-and-broadcast: there is **no reply** correlated to the request. The observables are the `modal_dismissed` broadcast frame plus the bare ESC to the child.

The production resolver (`cmd/pyry/modal_resolve_v2.go:63` `ResolveCancel`) does, in order: `reg.Resolve(modalID)` (the single one-shot idempotency gate) → `SendEsc()` (lone `0x1b` to fakeclaude stdin, best-effort) → `audit.Log` → returns `ModalDismissal{Outcome: "cancelled", Source: "remote"}`. So the wire `modal_dismissed` for a cancel carries **`Outcome == "cancelled"`, `Source == "remote"`** (constants `audit.OutcomeCancelled` / `audit.SourceRemote`).

This test confirms the wired path live over one spawned daemon + a fake claude that raises a permission modal; it does not re-prove correctness (the unit tier and #791/#793 own that) — it confirms the encrypted-v2 round trip.

## Files to read first

- `internal/e2e/relay_v2_modal_answer_test.go:46-215` — **the primary template.** `modalHarness` struct + `bringUpModalHarness` (daemon + gated interactive phone + `sealSend`/`nextEnv`/`stdinLog`/`modalTrig`) and `awaitModalShown`. **Reuse both verbatim** — do not mutate (frozen for #791) and do not duplicate.
- `internal/e2e/relay_v2_modal_answer_test.go:224-348` — `TestRelayV2_RemotePermissionAnswered`: the exact assertion shape to mirror (await `modal_shown`, capture `modalID`, send a control frame via `h.sealSend`, drain to `modal_dismissed` under a long deadline, then a cheap short-deadline replay negative). The `modal_cancel` test is this minus the option-ID/keystroke-digit specifics.
- `internal/e2e/relay_v2_modal_answer_test.go:442-454` — `assertNoAnswerDigit(t, log, when)`: reuse verbatim to prove cancel routed **no** answer digit (only the ESC).
- `internal/e2e/relay_v2_interrupt_test.go:323-362` — the **bare-ESC stdin-log oracle**: `hasBareESC(b []byte) bool` (reuse verbatim) and its bounded-poll cross-process fsync-visibility loop (~2 s). This is AC-3's non-vacuity mechanism; mirror the poll, not the single-read.
- `internal/e2e/relay_v2_two_head_modal_test.go:360-417` — the snapshot-then-byte-unchanged stdin-log pattern (`logAfterWin` → `bytes.Equal`) for the one-shot replay negative (AC-4 cheap substitute).
- `internal/relay/v2session.go:2268-2300` — `handleModalCancel`: fire-and-broadcast, no reply; unknown/already-resolved id ⇒ no keystroke, no broadcast.
- `cmd/pyry/modal_resolve_v2.go:56-100` — `ResolveCancel`: `Resolve` (one-shot gate) → `SendEsc` → audit → `{Outcome:"cancelled", Source:"remote"}`. Confirms the expected wire values and the replay/timeout one-shot semantics.
- `internal/protocol/messaging.go:146-169` — `ModalCancelPayload{ModalID}` and `ModalDismissedPayload{ModalID, Outcome, Source}` wire shapes; `internal/protocol/codes.go:277-278` — `TypeModalCancel` / `TypeModalDismissed`.
- `internal/audit/audit.go:44-61` — confirms `OutcomeCancelled == "cancelled"`, `SourceRemote == "remote"`.

## Design

One new file, `internal/e2e/relay_v2_modal_cancel_test.go`, build tag `//go:build e2e`, package `e2e`. It contains **exactly one** test function and **no new helpers** — every helper it needs already exists in-package (same build tag): `bringUpModalHarness`, `awaitModalShown`, `hasBareESC`, `assertNoAnswerDigit`, `mustJSON`. Redefining any of them is a duplicate-symbol compile error; do not.

### Data flow under test

```
phone modal_cancel{modal_id} frame → Noise decrypt → dispatchAppFrame intercept →
  handleModalCancel → ModalResolver.ResolveCancel →
    reg.Resolve(modal_id)  [one-shot gate] → SendEsc() → lone 0x1b → fakeclaude stdin log
  → broadcastModalDismissed → modal_dismissed{modal_id, "cancelled", "remote"} → phone
```

No `send_message` / prompt is driven, so the stdin log contains **no bracketed-paste marker** (`0x1b 0x5b`) — the only ESC in the log is `SendEsc`'s lone `0x1b`. (The bare-ESC oracle is still used, per AC-3, because a plain "contains `0x1b`" check is vacuous by policy.)

### Test: `TestRelayV2_ModalCancelDismisses`

Assertion order is load-bearing — each positive precondition has its own `t.Fatal` naming its failure mode, so the later assertions can't pass vacuously (the sibling discipline).

1. **Bring up + raise one modal (precondition).** `h := bringUpModalHarness(t)`; `shown := awaitModalShown(t, h)`. Assert `shown.Class == "permission"` (proves a real permission modal was raised before the cancel — else the dismissal assertion is vacuous). Capture `modalID := shown.ModalID`.
   - *Reuse note:* `bringUpModalHarness` pairs the phone `--allow-remote-permissions`. `ResolveCancel` does **not** gate on that capability (it uses `dev` only for the audit identity), so the flag is a harmless no-op for this path — reusing the frozen helper as-is is correct.
2. **Send the cancel.** `h.sealSend` an `Envelope{ID: <n>, Type: protocol.TypeModalCancel, TS: now, Payload: mustJSON(t, protocol.ModalCancelPayload{ModalID: modalID})}`.
3. **AC-2 — await the dismissal (long deadline, ~20 s, back-to-back `h.nextEnv` reads).** Drain, skipping non-`modal_dismissed`; `t.Fatal` on an unexpected `TypeError` envelope and on deadline (naming "no `modal_dismissed` after cancel"). On the first `modal_dismissed`, decode `ModalDismissedPayload` and assert:
   - `ModalID == modalID` (`t.Fatalf` — wrong/absent modal is a hard fail; the ESC-oracle step below fences off it).
   - `Outcome == "cancelled"` and `Source == "remote"` (`t.Errorf` — value fidelity, mirrors the answer test's `Outcome`/`Source` checks).
4. **AC-3 — bare-ESC stdin oracle (bounded poll ~2 s).** `modal_dismissed` is a happens-after fence, but the ESC crosses a process boundary (SendEsc writes the PTY; fakeclaude reads+fsyncs asynchronously), so mirror `interrupt_test.go`'s bounded poll: read `h.stdinLog` in a loop until `hasBareESC(log)` is true or the deadline elapses; `t.Fatalf` (naming "cancel keystroke never routed to `SendEsc`") if not. Then `assertNoAnswerDigit(t, log, "after modal_cancel")` (cancel routes the ESC deny, never a `<n>\r` answer digit — belt-and-suspenders, different fabric from the wire report). Snapshot `logAfterCancel := log`.
5. **AC-4 cheap one-shot negative — replayed cancel.** Re-send the **identical** `modal_cancel` (same `modalID`). The registry `Resolve` already consumed the modal, so the replay misses the one-shot gate ⇒ no `SendEsc`, no broadcast. Drain `h.nextEnv` under a short deadline (~3 s): `break` on drain (the expected no-op); `t.Fatalf` if a second `modal_dismissed` or any `TypeError` appears. Then assert `bytes.Equal(readStdinLog, logAfterCancel)` — byte-for-byte unchanged proves no second ESC routed. See § "AC-4 disposition" for why this substitutes for the literal timeout variant.

## AC-4 disposition (timeout-vs-cancel one-shot)

AC-4 asks — *if cheap* — that "a modal resolved by cancel is not double-resolved by a subsequent timeout (exactly one `modal_dismissed`)."

The **literal** timeout variant is **not cheap**: the deny-on-timeout window is the real `modalDenyTimeout` (~2 min), lives inside the pyry subprocess, and cannot be shrunk from the e2e (the sibling `TestRelayV2_RemotePermissionDeniedOnTimeout` pays a full `2m+20s` for exactly this reason). Forcing it here would add ~2 min to the e2e run for a negative the shared gate already guarantees.

The one-shot property is enforced at a **single** point for both cancel-replay and cancel-then-timeout: `modalbridge.Registry.Resolve` (`cmd/pyry/modal_resolve_v2.go` — `ResolveCancel` and `ResolveTimeout` both call `reg.Resolve` and bail on `ok=false`). Step 5's replayed-cancel negative exercises that exact gate cheaply (~3 s) and deterministically. So this spec **covers the one-shot distinction via the replay path and explicitly defers the literal 2-minute timeout variant**, per the AC's "if not cheap, deferred and noted" clause. Note it in the test's doc comment.

## Concurrency model

None new. The test drives the daemon as a subprocess over one Noise session; all decrypts go through `h.nextEnv` in capture order (receive-nonce sequencing, the frozen harness contract). Long single deadlines with back-to-back reads on the phone conn — never a short poll on a phone `Receive` (fakephone closes the WS on a timed-out receive). The stdin-log poll is a bounded local-file read loop, independent of the phone conn.

## Error handling

- Harness/precondition failures (`bringUpModalHarness`, `awaitModalShown`) already `t.Fatal` with diagnostic context — reused as-is.
- Every wire-drain loop `t.Fatal`s on an unexpected `TypeError` envelope and on deadline, each message naming the specific failure mode (no vacuous pass).
- `pty.Open` unavailability is not a concern here (no local attach head; that was #793's path).

## Testing strategy

- The file **is** the test. Verify with `make e2e` (which runs `-tags e2e -race`); the deliverable is green under `-race`.
- Run `go test -tags e2e -race -run TestRelayV2_ModalCancelDismisses -count=1 ./internal/e2e/` to exercise just this test.
- **Non-vacuity is the acceptance bar** (this is a test-only PR): the ordered `t.Fatal` preconditions (modal actually raised as `permission` → dismissal observed with `cancelled`/`remote` → bare ESC on disk) must each fail loudly if its predecessor didn't happen. Confirm by construction, not by a single "contains 0x1b" check.

## Open questions

- **Envelope IDs.** Pick distinct `uint64` request IDs for the cancel and the replay (the sibling tests use arbitrary small ints, e.g. `41`/`42`); there is no ack correlated to `modal_cancel`, so the IDs are cosmetic. Not blocking.
- **`Source`/`Outcome` assertion severity.** Spec uses `t.Errorf` for `Outcome`/`Source` (fidelity) and `t.Fatalf` for `ModalID` mismatch (wrong modal invalidates the ESC oracle), matching the answer test. Developer may keep or unify; not blocking.
