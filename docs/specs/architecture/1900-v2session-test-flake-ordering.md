# #1900 — `internal/relay` V2Session tests: remove the ordering hazards behind the full-module flake

**Ticket:** [#1900](https://github.com/pyrycode/pyrycode/issues/1900) · `bug` · `size:s` · `security-sensitive`
**Scope:** test-only, confined to `internal/relay/v2session_test.go`. Zero production files.

---

## Files to read first

Read these before writing anything. Each entry names the symbol and what to extract.

| File | Symbol | What to extract |
|---|---|---|
| `internal/relay/v2session_test.go` | `TestV2Session_Push_HoldGatedOnProbeNotSendError` | Failure 1's home. Note sub-case (b) runs **first**, sets `sendFail`, waits on `assertQueueDrains`, then flips `sendFail` back before sub-case (a). |
| `internal/relay/v2session_test.go` | `TestV2Session_RekeyScheduled_TransportDown_NoRekeyFailed_NoClose` | Failure 2's home. Note the transport is dropped **after** `driveToOpen` returns, and the single flat `time.Sleep` gating both the positive and the negative assertions. |
| `internal/relay/v2session_test.go` | `driveToOpen` | Returns only after the `noise_resp` is recorded. The session is already `V2StateOpen` **and the scheduled rekey timer is already armed** by then — this is the whole of failure 2's hazard. |
| `internal/relay/v2session_test.go` | `gatedRecorder`, `newGatedRecorder` | The `#874` fixture: one `atomic.Bool` drives `outbound` (records + nil when up, records nothing + `errTransportDown` when down) and `connected()` **in lockstep**. Failure 2's fix must preserve that lockstep. |
| `internal/relay/v2session_test.go` | `waitForLogContains` | **Already exists.** Poll-until-substring over a `syncLogBuffer`, 2 s deadline, quotes the buffer on failure. This is the wait-until-observed primitive for failure 2 — do not write a new one. |
| `internal/relay/v2session_test.go` | `bufferLogger`, `syncLogBuffer` | Mutex-guarded Debug-level log sink. `String()` is safe to poll from the test goroutine while Run writes. |
| `internal/relay/v2session_test.go` | `assertHeldQueued`, `assertQueueDrains`, `queueLen` | The shared assertions. **Neither helper changes in this ticket** — see § Design, "What deliberately does not change". `assertHeldQueued` has 7 call sites; `assertQueueDrains` has 2 (the second is in `v2session_debugbundle_test.go`). |
| `internal/relay/v2session.go` | `drainOnce` | The pop/unlock/forward sequence. The pop commits under `pushMu`; `pushMu` is released; **then** `forwardEnvelope` runs. That gap is failure 1's hazard. Also holds the transport-down guard that AC3's first mutant deletes. |
| `internal/relay/v2session.go` | `forwardEnvelope` | Seals under `s.send` and calls `m.send`. Confirms the seal+send is strictly downstream of the pop. |
| `internal/relay/v2session.go` | `send` (method on `*V2SessionManager`) | Calls `m.cfg.Outbound(env)` and swallows the error at Debug. **It does not consult `transportDown()`** — load-bearing for failure 2's fix. |
| `internal/relay/v2session.go` | `handleWake` | The `wakeRekeyEmit` arm holds the transport-down deferral that AC3's second mutant deletes, and emits `event=v2.rekey.emit.deferred_transport_down`. |
| `internal/relay/v2session_handshake.go` | `handleNoiseInit` | Ordering proof for failure 2's fix: `m.send(resp)` → `s.state = V2StateOpen` → `s.rekeyTimer = m.armRekeyTimer(...)`, all sequential on the Run goroutine. |
| `internal/relay/v2session_rekey.go` | `emitRekeyRequest`, `armRekeyTimer`, `armRekeyRetryTimer`, `armRekeyReplyTimer` | The emit path and the three timer cadences. `emitRekeyRequest` sets `awaitingRekeyReply` and arms the reply window only after `m.send`. |
| `docs/knowledge/features/v2-session-manager.md` | § "Transport-down hold on the push drain (#874)", § "Scheduled + manual rekey emit gated behind `transportDown()` (#912)" | The two invariants these tests pin, and the documented statement that the `gatedRecorder` drives `outbound` and `connected()` "in lockstep". Read before deciding how to drop the transport. |
| `docs/knowledge/features/transport-package.md` | § the `#1802` lesson | *"A flaky duration bound on this path is a sign to trace which sleep it actually contributes, not to widen the bound."* The rule this ticket is applying. |
| `docs/knowledge/features/streamsup-package.md` | § the `#1482` lesson | *"Where the test arms the gate matters as much as what it asserts."* Failure 2 is exactly that shape. |

---

## Context

`internal/relay`'s `V2Session` suite fails roughly two runs in three under full-module `go test -race ./...`, and passed 7/7 in isolation. Two different tests were seen failing across runs, which rules out a deterministically-broken test and points at load-sensitive ordering. `internal/relay/` is byte-identical between `bf68c2a6` (the baseline in the ticket) and current `main` — verified with `git diff --name-only bf68c2a6..HEAD -- internal/relay/`, which is empty — so reproducing on the branch's merge-base is reproducing on `bf68c2a6`.

Both failures are **test-side ordering hazards**, not production defects. Each has a fix that removes the race outright rather than widening a bound, and in both cases the fix makes the assertion *stronger*, not weaker. That matters here: these two tests are the pins for the `#874` push-drain hold and the `#912` rekey deferral, i.e. for Noise send-nonce contiguity on an internet-exposed session. The cheap fix — widening the sleep — would trade a flake for silently-deleted crypto-state coverage.

No ADR is warranted. This is a defect fix within an established, documented pattern.

---

## Design

Two independent fixes, one per test. Nothing shared changes.

### Failure 1 — `TestV2Session_Push_HoldGatedOnProbeNotSendError`: synchronise on the send, not the pop

**Confirmed mechanism.** `drainOnce` pops the head under `pushMu`, releases `pushMu`, and only then calls `forwardEnvelope` → `m.send` → the test's `Outbound` closure. `assertQueueDrains` returns the instant `queueLen` reads 0 — i.e. inside the pop→send gap. Sub-case (b) then flips `sendFail` back to `false` before starting sub-case (a). If that flip lands while the already-popped envelope is still in flight to `Outbound`, the closure takes the *success* branch, the recorder gets a second envelope, and sub-case (a)'s `assertHeldQueued` fails with the reported `recorded 2 envelopes while down, want 1`. In isolation the gap is microseconds; under full-module `-race` fan-out it widens enough to hit.

**Fix.** Give the `Outbound` closure a monotonic counter of completed send *decisions*, incremented **after** the `sendFail.Load()` on both branches. Sub-case (b) then waits until the counter reaches 2 (the handshake `noise_resp` is decision #1; the sub-case (b) push is #2) before touching either flag.

Contract for the closure:

```go
// sends counts completed send decisions. Incremented AFTER reading sendFail on
// both branches, so observing sends >= N means envelope N's sendFail read has
// already happened — flipping the flag afterwards cannot change its fate.
var sends atomic.Int64
outbound := func(env protocol.RoutingEnvelope) error { /* load, branch, then sends.Add(1) */ }
```

Why a counter and not a channel: the handshake `noise_resp` also runs through `Outbound`, so a buffered-channel signal would need draining after `driveToOpen`, and that drain races the signal's own send. A counter starting from a known baseline of 1 has no such race.

Why this is a real happens-before and not a wider window: the increment is sequenced after the `sendFail.Load()` inside the same closure invocation, and `atomic.Int64`/`atomic.Bool` operations on the same goroutine are program-ordered. Observing `sends >= 2` from the test goroutine therefore establishes that envelope 2's `sendFail` read is already in the past. There is no duration to tune.

Sequence in sub-case (b), in order:

1. `sendFail.Store(true)`; `Push(...)`.
2. Wait until `sends.Load() >= 2`, bounded by a generous deadline (2 s, matching `waitForEnvelopes` / `assertQueueDrains` / `waitForLogContains`). On expiry, `t.Fatalf` naming the observed count.
3. `assertQueueDrains` — now trivially true (the pop committed before the closure ran), kept because it states the "head consumed, not held" claim explicitly.
4. `rec.snapshot()` length check — now non-vacuous, because step 2 proved the send decision already happened.
5. Only then flip `probeUp` / `sendFail` for sub-case (a).

Steps 3 and 4 currently sit *before* any proof that the send ran, which makes step 4 a negative assertion that can pass because nothing has happened yet. Moving the wait in front of them fixes the flake and the latent vacuity in the same edit.

The wait loop is ~7 lines inline in the test. Do **not** add a package-level helper for one call site.

### Failure 2 — `TestV2Session_RekeyScheduled_TransportDown_NoRekeyFailed_NoClose`: drop the leg before the timer is armed

**Diagnosis first — this is a gate, not a formality.** The ticket names two candidates and says the fix differs. They produce distinct failure text, so the reproduction output discriminates them:

| Candidate | Mechanism | Failure text on the pre-fix tree |
|---|---|---|
| (a) fixed wait gating a positive | The flat sleep expires before the Run goroutine services a single `wakeRekeyEmit` | `log missing the deferred-transport-down line` |
| (b) arm-before-drop ordering | More than `rekeyInterval` elapses between `driveToOpen` returning and the transport being dropped, so the first scheduled boundary emits **for real**, arms the reply window, and tears the session down | `log contains noise.rekey_failed …` and/or `state = … want V2StateOpen` |

Record which one the reproduction produced on the PR. The design below applies both remedies — (b)'s because it is the likely cause, (a)'s because AC2 independently requires every waiting assertion to complete on observation — but the recorded diagnosis is what tells code review whether the reproduction actually exercised the hazard it claims.

**Fix for (b) — the ordering hazard.** The scheduled rekey timer is armed inside `handleNoiseInit`, and `handleNoiseInit` runs `m.send(resp)` → `s.state = V2StateOpen` → `armRekeyTimer` **sequentially on the Run goroutine**. `m.send` does not consult `transportDown()`; it calls `m.cfg.Outbound` unconditionally. So a `V2SessionConfig.Outbound` closure that wraps `gatedRecorder.outbound` and flips the gate down *after recording the frame it was handed* puts the transport in the down state at a point that is strictly program-ordered before `armRekeyTimer` — on the same goroutine, with no duration involved at all.

Contract for the closure:

```go
// Drop the relay leg the instant the handshake resp is on the wire. This runs
// on Run, sequenced before handleNoiseInit arms the scheduled rekey timer, so
// the FIRST scheduled boundary is guaranteed to find the transport down.
outbound := func(env protocol.RoutingEnvelope) error {
    err := gated.outbound(env)
    gated.up.Store(false)
    return err
}
```

Wire it as `Outbound: outbound, Connected: gated.connected`. `driveToOpen` still sees exactly one recorded envelope, because the flip happens after `gated.outbound` recorded it. The store is idempotent, so no one-shot guard is needed; a comment saying so is enough.

This **preserves the documented `gatedRecorder` lockstep** — after the flip, `outbound` errors and `connected()` reads false together, exactly as `docs/knowledge/features/v2-session-manager.md` describes. That was the deciding constraint: an alternative fix that gave the test independent probe/send flags (the shape `TestV2Session_Push_HoldGatedOnProbeNotSendError` uses) would have let a frame be *recorded while the probe reads down*, decoupling the recorder from the crypto claim the test exists to pin. Rejected for that reason.

Remove the three `time.Sleep`-based assumptions this replaces: the transport is now down before the session is even open, so the pre-existing `gated.up.Store(false)` line after `driveToOpen` goes away.

**Fix for (a) — the positive assertion.** Replace the flat sleep with `waitForLogContains(t, logBuf, "event=v2.rekey.emit.deferred_transport_down")`. The helper already exists, already polls to a 2 s deadline, and already quotes the whole buffer on failure. A starved scheduler now delays the test instead of failing it.

**The negatives keep a real settle window, anchored to an observed event.** After `waitForLogContains` returns, sleep a small multiple of `rekeyReplyTimeout` (the test sets it to 40 ms; 5× = 200 ms is ample) before asserting no `noise.rekey_failed` and `V2StateOpen`. The window is now measured from a *confirmed* deferral rather than from test start, so starvation lengthens it rather than consuming it. Say so in a comment, tying the multiplier to `rekeyReplyTimeout`.

**Keep the package-var override pattern** (`rekeyInterval` / `rekeyRetryInterval` / `rekeyReplyTimeout` with LIFO `t.Cleanup` restore) and keep the test non-parallel. `V2SessionConfig` does expose `RekeyInterval` / `RekeyRetryInterval` / `RekeyReplyTimeout` seams that would remove the globals and permit `t.Parallel()`, but every rekey test in this file uses the package-var pattern, the globals are not implicated in either observed failure, and `handleWake` reads the `rekeyRetryInterval` global directly for a log field regardless of the config override. Switching one test to the config seams is a style divergence with no evidence behind it. Leave it.

### What deliberately does not change

- **`assertHeldQueued`** — unchanged, all 7 call sites untouched. Its 100 ms settle is the one negative window AC2 explicitly preserves; its observed failure mode was a leaked in-flight frame from failure 1's sub-case (b), which failure 1's fix removes at the source. Widening or re-shaping it would be re-tuning the very bound the ticket forbids re-tuning.
- **`assertQueueDrains`, `queueLen`, `gatedRecorder`, `driveToOpen`, `waitForEnvelopes`, `waitForLogContains`** — unchanged. No shared helper is edited, so no call-site cascade.
- **All production code** — zero files. If the diagnosis instead lands on a production defect in the drain or rekey paths, **stop and route back with `needs-rework:po`** per the ticket's scope boundary. That is a different ticket with a different size and its own security review, not a widening of this one.
- **`TestV2Session_RekeyScheduled_DeferredWhileTransportDown_EmitsOnRecovery`** — out of scope. See § Open questions for the analysis and why it stays out.

---

## Reproduction (AC1)

AC1 requires an exact command that reddens the pre-fix tree, the observed failure output quoted, and 10 consecutive greens on that same command post-fix. Reproduce **before** making any edit, on the branch's merge-base — `internal/relay/` there is identical to `bf68c2a6`.

The failures need induced contention; isolated `./internal/relay/...` was green 7/7. Work the ladder cheapest-first and stop at the first rung that reddens:

1. `GOMAXPROCS=1 go test -race -count=10 ./internal/relay/` — one P forces the Run goroutine, the timer callback goroutines and the test goroutine to interleave coarsely, which widens both hazard windows. Whole package, so the serial rekey tests get in-package company.
2. Same as 1 at `-count=25`, or `GOMAXPROCS=2 -count=25`.
3. Rung 1 or 2 with a concurrent `go build ./...` loop in a second shell, to reproduce the compile-fan-out pressure of the original full-module run.
4. `GOMAXPROCS=2 go test -race -count=1 ./...`, repeated — the original observed shape. Expensive; the ticket's budget note says to keep repeat counts off this rung.

Record on the PR: the exact rung that reddened, the failing test name, and the verbatim `t.Fatalf` / `t.Errorf` output. That output is also what discriminates failure 2's candidate (a) from (b) — see the table above.

**If the two failures need different commands**, record both, and run each 10 consecutive times post-fix. AC1's "that same command" is per-failure; a single command covering both is preferable but not required, and claiming one command reproduces both when only one test ever reddened under it would be a false report.

A green rung is not a fix proof. AC1 is explicit: absence of a red does not satisfy it — the pre-fix red must be quoted.

---

## Mutation gate (AC3)

Both mutants are production-side deletions. Run them **without writing to the worktree**, using `go test -overlay`:

- Write the mutated copy of `internal/relay/v2session.go` into the scratchpad, point an overlay JSON at it, and run `go test -overlay=<abs path>.json -run '<TestName>' ./internal/relay/`.

| Mutant | Delete | Expected red |
|---|---|---|
| M1 | `drainOnce`'s leading `if m.transportDown() { return }` guard | `TestV2Session_Push_HoldGatedOnProbeNotSendError` — sub-case (a) pops and forwards while the probe reads down, so `assertHeldQueued` fails on both clauses (`queue depth = 0, want 1` and `recorded 2 envelopes while down, want 1`) |
| M2 | `handleWake`'s `wakeRekeyEmit` transport-down branch (the whole `if m.transportDown() { … return }` block) | `TestV2Session_RekeyScheduled_TransportDown_NoRekeyFailed_NoClose` — no deferral line is ever logged, so `waitForLogContains` fails at its deadline; if the deadline were longer the blind emit's reply window would also produce `noise.rekey_failed` and a 4426 teardown |

Both mutation results go on the PR. Sub-case (b) of failure 1 is unaffected by M1 (its probe reads up), which is correct — the hold claim lives entirely in sub-case (a). Note that failure 1's fix does not touch sub-case (a), so M1's redness is not something the fix could have weakened; state that explicitly on the PR.

---

## Concurrency model

No goroutines are created or removed. The relevant edges, all pre-existing:

- **Run goroutine** — owns `m.sessions`, `s.state`, `s.send`, and every timer arm. `drainOnce`, `forwardEnvelope`, `handleWake` and `handleNoiseInit` all execute here. The failure-2 fix relies on this: the `Outbound` closure is invoked *by* Run, so its `gated.up.Store(false)` is program-ordered before `armRekeyTimer` in the same `handleNoiseInit` call.
- **Test goroutine** — drives frames in, polls recorder/queue/log buffer out. It must never read Run-owned state directly while Run is live. Both tests already respect this: `sess.mgr.sessions[...]` is read only after `sess.stop()` (which cancels Run and joins its goroutine). **Do not add a `m.sessions` read before `stop()`** — it is a data race and `-race` will report it.
- **`time.AfterFunc` callback goroutines** — `armRekeyTimer` / `armRekeyRetryTimer` / `armRekeyReplyTimer` each push a `wakeSignal` onto `m.wake` under `select { case m.wake <- …; case <-ctx.Done() }`. Unchanged.
- **Cross-goroutine handoffs the tests use:** `atomic.Bool` (`gatedRecorder.up`, `probeUp`, `sendFail`), the new `atomic.Int64` send counter, the mutex-guarded `v2Recorder` slice, and the mutex-guarded `syncLogBuffer`. All already `-race`-clean shapes.
- **`t.Cleanup` ordering is LIFO and load-bearing** in failure 2's test: `sess.stop` is registered after the package-var restores, so Run is joined *before* the globals are put back. Preserve that ordering when editing.

---

## Error handling / failure modes

These are tests, so "error handling" is failure diagnostics. Every new wait must fail loudly and informatively:

- The send-counter wait fails with the observed count and what it was waiting for (`want >= 2 send decisions (handshake resp + the probe-up push), got N`), so a timeout is distinguishable from a wrong baseline.
- `waitForLogContains` already dumps the whole log buffer on failure — that is what makes it usable as failure 2's discriminator.
- Every deadline is 2 s or more, well clear of the millisecond-scale intervals the tests configure. A starved scheduler delays; it does not fail.
- The existing `t.Fatalf` messages that name the invariant (`head held un-popped while transport down`, `it was closed while transport was down`) stay as-is. They are what makes a future failure legible.

---

## Testing strategy

The deliverable *is* tests, so verification is the campaign, in this order:

1. **Reproduce on the pre-fix tree** (§ Reproduction). Quote the red. Record which failure-2 candidate it identified.
2. **Apply the two fixes.**
3. **10 consecutive greens** on the reproducing command(s), per failure.
4. **Mutation gate** (§ Mutation gate) — M1 and M2, via `-overlay`, both red, both recorded.
5. **`make check`** once, green. Read as a no-regression check on the rest of the module, not as flake proof — the baseline passed `make check` too.

Do not run `make check` in a repeat loop; the ticket's budget note puts the repeat count on the targeted command, which is ~17 s per relay-package run.

---

## Open questions

- **Which failure-2 candidate is real.** Resolved by the reproduction's output (see the discriminator table). Both remedies ship regardless; only the recorded diagnosis is pending.
- **`TestV2Session_RekeyScheduled_DeferredWhileTransportDown_EmitsOnRecovery` carries the same arm-before-drop ordering but a different consequence, and stays out of scope.** Analysis, so the next person does not have to redo it: that test sets `rekeyReplyTimeout = 2s`, so if its first scheduled boundary emits while the transport is still up, the emit is *recorded* (the gate is still up), the reply window is far too long to expire inside the test, and every assertion — envelope count, clean decrypt under `initRecv` at nonce 0, `reason: "scheduled"`, `awaitingRekeyReply` — still passes. It therefore degrades to **silent vacuity** (measuring an emit-while-up instead of a deferred-emit-on-recovery) rather than flaking, which is why it has never been seen red. Per the ticket's instruction and the project's evidence-based-fix-selection principle, an unobserved failure mode is not fixed here. It also would not take the same fix: its nonce oracle needs the transport genuinely down across the window, so the "drop the leg inside `Outbound`" shape does not transfer unchanged. **Recommend a follow-up ticket** to de-vacuate it; noted rather than filed, since filing is PO's call.
- **`handleWake` logs the `rekeyRetryInterval` package global in its `retry_in` field, ignoring the `V2SessionConfig.RekeyRetryInterval` override that `armRekeyRetryTimer` honours.** Cosmetic (no test asserts the field), out of scope here, but it is the reason a future migration of these tests to the config seams would not fully remove the global reads. Worth a line in the package overview when the documentation phase touches it.

---

## Security review

**Verdict:** PASS

Framing: this ticket writes no production code, so the adversarial question is not "what can an attacker reach" but **"can this change silently delete the crypto-state coverage these two tests exist to provide?"** That is the ticket's own stated failure mode for a bad fix, and it is the axis every finding below is judged on.

**Findings:**

- **[Trust boundaries]** No findings. The untrusted→trusted boundary on this path is the phone's inbound frame reaching `handleFrame` / `dispatchAppFrame`, and the outbound trust gate is `forwardEnvelope`'s `V2StateOpen` check. Neither is touched: the spec changes no production symbol and adds no new inbound path. The failure-2 fix moves a test-double state change (`gated.up.Store(false)`) into the `Outbound` callback, which is on the *egress* side, downstream of every authentication and state gate — it can only cause fewer frames to be delivered in the test, never more.
- **[Tokens, secrets, credentials]** No findings. Both tests use `v2PairedRegistry` with the existing `v2TestToken` fixture and `genV2Keypair`'s `crypto/rand`-backed X25519 pairs. No token, key, or ciphertext is added to an assertion string or a log assertion. Failure 2's new assertions match on `event=v2.rekey.emit.deferred_transport_down` and `noise.rekey_failed` — both content-free event names, per the package's no-AEAD-bytes-in-logs discipline.
- **[File operations]** Not applicable — no path is constructed, opened, or written. The mutation gate deliberately uses `go test -overlay` precisely so the mutants never touch the worktree.
- **[Subprocess / external command execution]** Not applicable — no `exec.Command`, no shell. The reproduction ladder is operator-run `go test` invocations differing only in `GOMAXPROCS` and `-count`; no test-tree modification is involved, which is what keeps AC1's "on unmodified `bf68c2a6`" honest.
- **[Cryptographic primitives]** **The load-bearing category, and the reason the `gatedRecorder` lockstep is a hard constraint.** These two tests are the pins for Noise send-nonce contiguity: `#874`'s hold and `#912`'s deferral both exist so that no nonce is burned on a frame the transport cannot deliver, because a burned nonce gaps the phone's recv sequence and kills a live session at 4421. The rejected alternative for failure 2 — independent probe and send flags, so the handshake resp records while the probe reads down — would have decoupled the recorder from the transport state and thereby weakened the "nothing was sealed while down" oracle from a transport-truthful one to a bookkeeping one. The adopted fix keeps `outbound` and `connected()` flipping together, so "the recorder saw exactly one envelope" continues to mean "exactly one frame was sealed under `s.send`". Failure 1's fix strictly strengthens its crypto claim: today's `rec.snapshot()` length check runs before any proof that the send decision happened and can pass vacuously; after the fix it runs after that proof.
- **[Network & I/O]** Not applicable — no socket, no `http.Server`, no size cap or deadline in play. Both tests are in-process against a channel-fed manager.
- **[Error messages, logs, telemetry]** No findings, one deliberate coupling to note. Failure 2's positive assertion matches the production log line `event=v2.rekey.emit.deferred_transport_down`, and failure 1's diagnostics quote only counts. `waitForLogContains` dumps the buffer on failure, and that buffer comes from `bufferLogger` at Debug — it can contain `conn_id` and `retry_in`, which are already the package's permitted operator-actionable fields, and no payload, token, or key bytes. The coupling to the event string is intentional and is the anti-vacuity mechanism for M2: rename the event without updating the test and the test goes red, which is the correct direction.
- **[Concurrency]** No findings, with one MUST-NOT the spec states explicitly. No lock is taken, no lock order is introduced, no goroutine is spawned or leaked — the only new state is an `atomic.Int64` written by the Run-invoked closure and read by the test goroutine. The one real hazard in this area is reading Run-owned `m.sessions` while Run is live; both tests already read it only after `sess.stop()` joins the Run goroutine, and § Concurrency model calls out that this must not regress. `t.Cleanup`'s LIFO ordering (Run joined before the rekey globals are restored) is likewise named as load-bearing. The failure-2 fix's happens-before rests on program order within a single goroutine, not on a duration, so there is no TOCTOU left to exploit.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security model's nonce/session-integrity concerns are the ones these tests pin, and the coverage is preserved or strengthened on both. The residual single-frame up→down TOCTOU documented for `#874` is a **pre-existing production posture, explicitly accepted in `docs/knowledge/features/v2-session-manager.md`**, and is neither widened nor addressed here — out of scope, owned by whoever picks up the rekey/resync backstop that `#874`'s notes name as the full closure.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
