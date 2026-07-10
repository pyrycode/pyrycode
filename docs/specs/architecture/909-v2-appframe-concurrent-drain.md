# Spec: v2 `dispatchAppFrame` drains handler replies concurrently with `Route` (#909)

**Size:** S (1 production file, ~15–25 net production LOC, 0 new exported types). `security-sensitive`.

## Files to read first

- `internal/relay/v2session.go:1689-1760` — `dispatchAppFrame`: the whole function under change. Extract the current shape — per-frame `outbound` (cap `handlerOutboundBuf`), synchronous `dispatch.Route`, then the drain-and-seal loop. This is the ~30 lines you rewrite.
- `internal/relay/v2session.go:1675-1688` — `dispatchAppFrame`'s doc comment: the synchronous-handler assumption and the "channel deliberately NOT closed" property (from #446). Both survive the fix; the doc comment needs a targeted rewrite.
- `internal/relay/v2session.go:238-246` — `handlerOutboundBuf = 8` const + comment. **Unchanged** — the buffer stays 8; the fix removes the *dependence* on it being large enough, it does not resize it.
- `internal/relay/v2session.go:1599-1673` — `handleNoiseMsg`, the sole caller of `dispatchAppFrame` (line 1670, `V2StateOpen` branch). Confirms no signature/caller change.
- `internal/relay/v2session.go:925-962` — `Run`: the single dispatch goroutine. `dispatchAppFrame` runs on this goroutine (Run → `handleFrame` → `handleNoiseMsg` → `dispatchAppFrame`). This is the goroutine that must remain the sole owner of `s.send.Encrypt`.
- `internal/relay/v2session.go:3035-3044` — `m.send`: the forward-to-relay seam (`m.cfg.Outbound`). Called on Run; unchanged.
- `internal/relay/v2session.go:790-829` — `V2SessionManager` struct + the single-owner-goroutine invariant comments (the `wake`/`manualRekey`/`pushMu` doc blocks). The canonical statement of why off-Run work funnels *back* onto Run before touching `s.send`. Your fix must not violate this.
- `internal/dispatch/dispatch.go:130-163` — `Conn.Send` / `Conn.Reply`: `Send` blocks on `c.outbound <- routing`, unblocking only on ctx cancel. This is the blocking primitive that deadlocks today.
- `internal/dispatch/dispatch.go:539-586` — `Route`: single-frame dispatch through the handler table. Synchronous; returns after `h(ctx, conn, env)` returns. Handlers reply via `conn.Send`/`conn.Reply` only — they never touch `s.send`.
- `internal/relay/v2session_test.go:862-938` — `TestV2Session_OpenState_EncryptedRoundTrip`: the exact harness the regression test copies — `driveToOpen`, `sealAppFrame`, `decryptAppFrame`, `v2Recorder`, `waitForEnvelopes`, a `Handlers` map keyed by `protocol.TypeListConversations`.
- `internal/relay/v2session_test.go:98-169` — `startManager` + `waitForEnvelopes` (and `waitForOutboundCount` at :1861) — how a test drives frames and observes sealed outbound with a deadline (a deadlock surfaces as a timeout here).
- `docs/knowledge/codebase/446.md` — the ticket that introduced `dispatchAppFrame`, `handlerOutboundBuf`, and the synchronous-handler invariant. Read the "Lessons learned" → head-of-line-blocking note and the "never close the channel" pattern; both frame this fix. **Do not add `docs/knowledge/codebase/909.md`** — documentation phase owns it.
- `docs/knowledge/features/v2-session-manager.md` § transition table / "Out of scope" — the "per-conn fan-out for handler dispatch" follow-up. This fix is *narrower* than that follow-up (see Open questions).

## Context

**The deadlock.** `dispatchAppFrame` (v2session.go:1731) allocates a per-frame `outbound` channel of capacity `handlerOutboundBuf = 8`, runs `dispatch.Route` **synchronously on the manager's single Run goroutine**, and drains that channel only **after** `Route` returns. `Conn.Send` (dispatch.go:135) blocks when the channel is full and unblocks only on ctx cancel — and the ctx is the manager's `runCtx`, which cancels only at daemon shutdown.

Consequence: any handler in the shared `V2SessionConfig.Handlers` table that emits more than 8 envelopes in one invocation (or ~7 plus `Route`'s own error reply) blocks forever inside `Conn.Send`. Because `Route` runs on the Run goroutine, **the whole v2 session manager wedges** — every phone connection stops receiving frames until the daemon restarts.

Today's three handlers (`send_message`, `list_conversations`, `register_push_token`) emit exactly one reply each, so this is **latent, not live**. But the same `dispatch.Handler` type is safe under the v1 `Dispatcher` (which drains its outbound concurrently via a forwarder goroutine), so a handler that works in v1 deadlocks in v2 — an easy trap for the next handler ticket. The `handlerOutboundBuf` comment documents the assumption; nothing enforces it.

**The constraint.** `s.send.Encrypt` is the AEAD seal that advances the Noise send counter. The v2 manager runs under a pervasive single-owner-goroutine invariant: `s.send` / `s.recv` / `s.state` / `m.sessions` are touched only on the Run goroutine. Concurrent access to the Noise send `CipherState` would corrupt the send counter/nonce on the internet-exposed encrypted wire. **The fix must keep `s.send.Encrypt` on the Run goroutine.** This is what the `security-sensitive` label gates.

## Design

**Chosen shape: run `Route` on a short-lived goroutine; the Run goroutine interleaves drain-and-seal with waiting for `Route` to finish** (the ticket's option (b)). Rejected alternatives are in Open questions.

Why this shape:
1. **Seals stay on Run.** The drain-and-seal loop *is* the Run goroutine. Only `Route` (→ handler → `conn.Send`, which just marshals JSON and pushes to a channel) moves off-Run. `s.send.Encrypt` is never reached from the spawned goroutine. This is the AC #3 property, structural.
2. **Bounded memory.** Replies are sealed and forwarded as they arrive — no accumulation of an unbounded buffered slice (option (a) accumulates; this does not).
3. **Minimal, local diff.** One function, one new helper, no changes to `internal/dispatch`, no new exported types, no touch to `handlerOutboundBuf`.
4. **Preserves #446's "never close `outbound`"** property verbatim — a handler that forks a late sender still writes into a leaked-but-capacity-bounded channel that GC reclaims; nothing closes it.

### Change 1 — extract the seal-and-forward body into a Run-only helper

The three-step body currently inline at v2session.go:1737-1755 (`s.send.Encrypt` → `marshalInnerFrameV2(TypeNoiseMsg, …)` → `m.send`) becomes a method, so both the interleave arm and the post-`Route` residual drain call one code path instead of duplicating it:

```
// forwardAppReply seals one handler reply under s.send and forwards it as a
// noise_msg via m.send. MUST run only on the manager's Run goroutine —
// s.send is single-owner (the Noise send CipherState). Drops the reply
// (WARN, no wire emission) on the realistically-unreachable seal/marshal
// error, exactly as #446: never emit an unsealed frame.
func (m *V2SessionManager) forwardAppReply(s *V2Session, reply protocol.RoutingEnvelope)
```

Behaviour is byte-identical to the current inline loop body (same two WARN messages, same drop-on-error posture, same `ConnID`/`Frame` construction). This is a pure extraction — the invariant it asserts (Run-only) is already true at both call sites.

### Change 2 — rewrite the dispatch/drain control flow

Replace the synchronous `Route` + post-drain loop (v2session.go:1733-1759) with: spawn `Route` on a goroutine that signals completion, and have the Run goroutine select between "a reply arrived, seal+forward it" and "`Route` finished, drain any residual then return". Contract sketch (the concurrency structure is the crux of this spec):

```
routeDone := make(chan struct{})
go func() { defer close(routeDone); dispatch.Route(ctx, m.cfg.Logger, conn, m.cfg.Handlers, plaintext) }()

for {
    select {
    case reply := <-outbound:      // interleave: seal+forward as replies arrive
        m.forwardAppReply(s, reply)
    case <-routeDone:              // Route done; drain residual FIFO non-blockingly, then return
        for {
            select {
            case reply := <-outbound:
                m.forwardAppReply(s, reply)
            default:
                return
            }
        }
    }
}
```
(Both `forwardAppReply` call sites are on Run; `outbound` and `conn` are unchanged from today; the channel is never closed.)

`outbound` (cap `handlerOutboundBuf`) and `conn := dispatch.NewConn(s.connID, outbound, s.device)` are unchanged from today. The channel is **never closed** (preserved).

### Data flow

```
Run goroutine                          spawned Route goroutine
────────────                           ───────────────────────
dispatchAppFrame                       Route(ctx, …, conn, handlers, plaintext)
  spawn Route ─────────────────────────►  h(ctx, conn, env)
  for { select                              conn.Send → outbound <- reply   (marshal + push only)
    <-outbound: forwardAppReply  ◄──────── (FIFO)      conn.Send → outbound <- reply
       s.send.Encrypt  (SEAL, Run-only)                 …
       marshalInnerFrameV2                              return
       m.send ──► relay                 close(routeDone)
    <-routeDone: drain residual, return }
```

Single sender (the handler on the Route goroutine) + single receiver (Run) over `outbound` ⇒ Go guarantees FIFO ⇒ emission order preserved end to end (AC #2). Sealing happens only in `forwardAppReply`, only on Run (AC #3).

## Concurrency model

- **Goroutines:** one new short-lived goroutine per app frame, running exactly `dispatch.Route`. It exits via `defer close(routeDone)` when `Route` returns (i.e. when the handler returns). No leak in-contract: a handler blocked in `conn.Send` at shutdown unblocks via `Conn.Send`'s `ctx.Done()` arm (ctx is `runCtx`), the handler returns, `Route` returns, the goroutine exits. The only leak vector is the #446-documented forked-late-sender — out of contract, bounded, GC-reclaimed, unchanged.
- **Shared state between the two goroutines:** the `outbound` channel (safe for concurrent single-sender/single-receiver) and `conn` (`conn.id` immutable; `conn.auth` set once before dispatch, read-only; `conn.nextID` atomic). The spawned goroutine **never** touches `s.send`, `s.recv`, `s.state`, or `m.sessions`. ⇒ `-race` clean, and `s.send` stays single-owner.
- **No deadlock for any finite or infinite reply stream:** Run drains `outbound` continuously until `routeDone`, so the handler's `conn.Send` can never fill-and-block while Run is alive. The 9th (or millionth) reply is drained as fast as the handler emits it.
- **`ctx.Done()` is deliberately NOT a third select arm.** The handler already observes cancellation through `Conn.Send`'s ctx arm, so a shutdown drives `Route` to return and fires `routeDone` naturally. Adding a `ctx.Done()` arm would risk (a) orphaning the still-running `Route` goroutine and (b) dropping ordered replies mid-stream. The two-arm select + non-blocking residual drain is the complete and minimal shape. Document this rationale in the code (a reviewer will ask).
- **Lock ordering:** unchanged. This fix introduces no lock. `pushMu` and the Run-owned maps are untouched.
- **Head-of-line blocking on a genuinely hung/slow handler is unchanged** and out of scope — that is the separate "per-conn fan-out for handler dispatch" follow-up tracked in `features/v2-session-manager.md` § Out of scope. Today `Route` runs synchronously on Run, so a hung handler already stalls Run; after this fix a *hung* handler still stalls Run, but a *reply-prolific* handler no longer does. Strictly-better posture, no regression.

## Error handling

- **Seal/marshal failure** inside `forwardAppReply`: WARN + drop that one reply, continue draining (identical to today's inline `continue`). Realistically unreachable under correct flynn/noise. AC #2's "none dropped or reordered by the concurrent drain" refers to the drain mechanics — this pre-existing seal-error drop path is preserved verbatim and is not a drain-induced drop.
- **Handler returns an error:** logged at WARN inside `dispatch.Route` (unchanged); no synthesised reply. Not this ticket's concern.
- **`Route`'s own error replies** (malformed/unsupported/unknown-type/no-handler) flow through the same `outbound` channel and are sealed+forwarded identically — no special-casing.

## Testing strategy

New focused test file **`internal/relay/v2session_appframe_test.go`** (a new file, not an edit to the 175 KB `v2session_test.go` — avoids churn on a hot shared file and keeps this change self-contained). All tests reuse the existing exported-within-package helpers (`driveToOpen`, `sealAppFrame`, `decryptAppFrame`, `v2Recorder`, `waitForEnvelopes` / `waitForOutboundCount`). Run under `go test -race`.

Scenarios (bullet-pointed, not full bodies — developer writes them in the project idiom):

- **`TestV2Session_OpenState_ProlificHandler_NoDeadlock` (AC #1, #4 — the regression guard).**
  - Register a handler keyed by `protocol.TypeListConversations` that emits `2 * handlerOutboundBuf` (16) replies in one invocation via `c.Reply(ctx, env, protocol.TypeConversations, payload_i)` with a per-reply distinguishable payload (e.g. an index).
  - Drive to open; send one sealed app frame of that type.
  - Assert all 16 replies arrive as sealed outbound (`waitForOutboundCount` past the noise_resp) within a bounded deadline. **Against the current drain-after-Route code this deadlocks and the deadline fires → test fails; after the fix it passes.** This is the AC #4 fail-then-pass witness.
  - Then send a **second** frame on the **same** conn (any registered type, or a single-reply handler branch) and assert its reply arrives — proves the manager keeps servicing subsequent frames after the prolific handler returned (AC #1).

- **`TestV2Session_OpenState_ProlificHandler_EmissionOrder` (AC #2).**
  - Same 16-reply handler with monotonically-indexed payloads.
  - Decrypt each sealed reply via `decryptAppFrame(…, sess.initRecv)` and assert the decrypted payload indices are `0,1,…,15` in order and none missing. Proves no drop, no reorder through the concurrent drain.
  - (`initRecv` decrypts in send-counter order; a reordered or dropped seal would surface as an AEAD/order mismatch — this test doubles as a send-counter-integrity check.)

- **`TestV2Session_OpenState_ProlificHandler_OtherConnServiced` (AC #1 cross-conn).**
  - Open two conns (distinct `conn_id`); fire the 16-reply handler on conn A; assert a normal single-reply frame on conn B still gets its reply within the deadline. Proves the wedge is gone across connections, not just within one.
  - *If the harness makes two concurrent open sessions materially harder than a second frame on one conn,* fold this facet into the first test's "second frame" step and note the single-conn coverage is sufficient — a wedge is a wedge regardless of `conn_id`. Developer's call at implementation time; don't spawn harness machinery for marginal coverage.

- **AC #3 is verified by `-race` across all three tests**, not a separate test: the handler emits from the spawned `Route` goroutine while Run seals concurrently; if `s.send.Encrypt` ever ran off-Run (or the send `CipherState` were touched concurrently) the race detector would fire and the ordered-decrypt assertion in the emission-order test would break. Call this out in a comment on the emission-order test so the reviewer sees the invariant is pinned, mirroring #446's approach of pinning an invariant via an existing test rather than a bespoke fixture.

Existing `TestV2Session_OpenState_EncryptedRoundTrip` and the other open-state dispatch tests are the single-reply regression guard — they must stay green (the extracted `forwardAppReply` is behaviour-preserving).

## Open questions

- **Rejected option (a) — off-Run drainer buffers into a slice, Run seals after join.** Also keeps sealing on Run, but accumulates an unbounded slice for a prolific handler and needs extra join coordination. Option (b) seals as it drains (bounded memory) and is fewer lines. No reason to prefer (a); documented here so a reviewer doesn't re-litigate.
- **Should `handlerOutboundBuf` shrink now that the drain is concurrent?** No. A larger buffer still reduces `conn.Send`/Run rendezvous churn for the common 1–few-reply case, and the const's comment is the historical anchor for the synchronous-handler doc. Leave it at 8; the fix removes the *correctness dependence* on its size, which is the point.
- **Per-conn fan-out (one goroutine per `conn_id`) for handler dispatch** — the broader "a slow handler stalls all conns" follow-up — remains out of scope and tracked in `features/v2-session-manager.md`. This ticket fixes only the reply-count deadlock, not the slow-handler head-of-line stall.

## Security review

**Verdict:** PASS

This ticket restructures goroutine ownership around the AEAD-seal path on the internet-exposed encrypted wire, so the review centres on cryptographic-primitive integrity (send-counter/nonce non-reuse) and concurrency. Reviewed adversarially per `architect/security-review.md`, assuming the spec has holes.

**Findings:**

- **[Cryptographic primitives] No MUST FIX — this is the property the ticket exists to preserve.** The Noise send `CipherState` (`s.send`, flynn/noise ChaChaPoly per ADR 024) advances a monotonic send counter; two concurrent `Encrypt` calls could reuse a nonce (breaks ChaChaPoly confidentiality) or corrupt the counter (breaks the wire). The design keeps `s.send.Encrypt` reachable only through `forwardAppReply`, which is called only from `dispatchAppFrame`'s two Run-side select arms. The spawned goroutine runs only `dispatch.Route` → handler → `conn.Send` (dispatch.go:135 — `json.Marshal` + channel push, **no AEAD**). No new crypto, no new key/nonce derivation. All other seal sites (`emitRekeyRequest`, `sealError`, `forwardEnvelope`) already run on Run and cannot run concurrently with `dispatchAppFrame` (Run processes one select-case to completion). Send-CipherState single-ownership is preserved. Pinned by the `-race` emission-order test (an off-Run seal or a concurrent CipherState touch fires the race detector and breaks the ordered-decrypt assertion).

- **[Cryptographic primitives / Concurrency] SHOULD FIX (code-review gate, not a spec change) — the one invariant a developer could break.** The correctness of the whole design rests on `forwardAppReply` never being called from inside the `go func() { … Route … }` closure. The spec states this and the helper's doc-comment asserts "MUST run only on the Run goroutine," but it is a discipline a future edit could violate silently. Code-review must confirm `forwardAppReply` (and any `s.send.Encrypt`) has zero call sites on the spawned goroutine. Deterministically backstopped by `-race`, so this is SHOULD not MUST.

- **[Concurrency] No findings.** No lock introduced (ordering unchanged). The spawned goroutine's only shared state is the `outbound` channel (safe single-sender/single-receiver) and `conn` (immutable `id`, atomic `nextID`, once-set `auth`); it never touches `s.send`/`s.recv`/`s.state`/`m.sessions`. Handlers receive `*dispatch.Conn`, not `*V2Session`, so they cannot reach the CipherState. Goroutine lifecycle: exits when `Route` returns; `defer close(routeDone)` fires even under handler panic (which stays fatal, same as today's on-Run `Route`). Shutdown drives cancellation through `Conn.Send`'s `ctx.Done()` arm → handler returns → `routeDone` → clean `dispatchAppFrame` exit; each `forwardAppReply` is an atomic seal-then-send, so a mid-stream shutdown leaves the send counter consistent (no half-written counter state).

- **[Trust boundaries] No findings.** The untrusted→trusted crossing (`s.recv.Decrypt` at handleNoiseMsg:1653) is upstream of and unchanged by this fix; `dispatchAppFrame` consumes already-authenticated plaintext. The handler's `conn.Auth()` read now executes on the spawned goroutine, but `s.device` is written once in `handleNoiseInit`'s token-accept branch before `V2StateOpen` and is never mutated afterward (re-key preserves it per #449). Goroutine-start happens-before makes the read data-race-free — exactly the case `dispatch.go:63-79` already documents for handler-spawned workers reading `Auth()`.

- **[Network & I/O] No findings — net security-positive.** The change *removes* a DoS: today a handler emitting >8 replies wedges the entire manager for all conns until daemon restart. Concurrent draining lets a handler emit an arbitrary number of replies, but this grants **no new phone-controlled amplification**: handlers are first-party code registered in `V2SessionConfig.Handlers` at daemon-wire time; a phone can only *trigger* an existing handler via an authenticated envelope, not register one or dictate reply volume beyond what that handler produces for the request. Per-request output bounds remain each handler's responsibility — the debug-bundle path (#813) already established that large/attacker-influenced output routes *around* this reply path rather than through it. Inbound frame-size caps (WSS read + AEAD) are upstream and untouched. `m.send`'s forward-side blocking behaviour is unchanged (per-reply `m.send` on Run, exactly as today's post-Route drain).

- **[Tokens, secrets, credentials] No findings.** No token generation/storage/rotation/revocation in this change; the device token was consumed at handshake (upstream). Reply payloads are handler-produced, not credentials.

- **[Error messages, logs, telemetry] No findings.** `forwardAppReply`'s two WARN lines carry `conn_id` only — no plaintext, ciphertext, frame bytes, or counter state — byte-identical strings to the #446 inline loop, honouring the #445/#446 "MUST NOT log AEAD bytes/counter state" rule.

- **[File operations] Not applicable.** This change performs no filesystem I/O.

- **[Subprocess / external command execution] Not applicable.** No `exec`, no external command, no environment handling.

- **[Threat model alignment] Addressed.** `docs/protocol-mobile.md` § Security model: (a) channel confidentiality/integrity — preserved by send-CipherState single-ownership (no nonce reuse); (b) DoS — a wedge-based denial is removed, none introduced; (c) malicious-phone — cannot register handlers or exceed authenticated-request-driven reply volume. The broader "slow handler stalls all conns" head-of-line concern is explicitly out of scope and tracked as the per-conn-fan-out follow-up in `features/v2-session-manager.md`.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-10
