# Spec #663 — Clamp the caught-up replay watermark to the newest retained id

**Ticket:** #663 — `fix(relay): clamp caught-up replay watermark to newest retained id` (#647 MUST FIX — silent live-stream suppression)
**Size:** S (held — not downgraded to XS; the production delta is XS but the trust-boundary regression coverage + security-sensitive care is the substance)
**Labels:** `security-sensitive` (inbound trust boundary: an untrusted remote `last_event_id` drives a drop-everything-at-or-below guard)
**Blocks:** pyrycode-mobile#416 (the client cannot advertise `last_event_id` until this server fix lands on `main`).

---

## Files to read first

- `internal/relay/v2session.go:1459-1506` — **`replayMissed`**: the defect is the unconditional `s.replayThrough = afterID` at **:1484**, reached even in the caught-up (`events == nil`) branch. The loop's per-frame `s.replayThrough = ev.ID` (:1504) is the *correct* precedent — the watermark should track real ids, never the raw remote cursor.
- `internal/relay/v2session.go:1938-1961` — **`forwardEnvelope`**: the paired guard `if env.EventID != nil && *env.EventID <= s.replayThrough { return nil }`. The fix must not change this; it must only stop the watermark being set above a real id. Note `EventID == nil` control frames (snapshot/error/rekey/resync) are never dropped.
- `internal/relay/v2session.go:1831-1860` — **`Push`**: the live-stream entry the regression tests drive. `Push` → `drainCh` → `drainOnce` → `forwardEnvelope`, so a live frame **does** pass the watermark guard. This is how the test asserts *delivery* of a live frame after a caught-up reconnect.
- `internal/eventring/ring.go:140-183` — **`After`**: the 3-way classifier. `latestID := c.nextID - 1` (:168) is exactly the value the new accessor surfaces; the unknown/empty-conversation handling (:160-166) is the shape `NewestID` mirrors.
- `internal/eventring/ring.go:60-124` — **`Ring`/`convRing`/`Append`**: `nextID` semantics (starts at 1, strictly increasing, advances on every `Append` independent of retention) and the eviction rule (the **newest event is never evicted** — oldest goes first). This is why `nextID - 1` is a sound "newest retained id".
- `internal/eventring/ring_test.go:101-117` (`TestAfter_CaughtUp`) and `:158-172` (`TestAfter_UnknownConversation`) — the table style + assertions the new `NewestID` tests mirror.
- `internal/relay/v2session_replay_test.go:25-136` — replay harness: `buildHelloEarlyDataReplay`, `appendRingEvents`, `reconnectScenario`, `waitConnOpen`. Drives a real Noise handshake whose hello carries `last_event_id`.
- `internal/relay/v2session_replay_test.go:330-388` — `TestV2Session_Reconnect_OtherConnsUnaffected`: the **inline-manager + `mgr.Push` + per-conn frame counting** pattern the live-delivery regression tests follow (the existing `reconnectScenario` discards the manager, so the new tests build the manager inline to push a live frame afterward).
- `internal/relay/v2session_replay_test.go:395-468` — `TestV2Session_ForwardEnvelope_ReplayWatermarkGuard`: shows injecting `replayThrough` into a session and asserting `forwardEnvelope` drop/forward directly — the dedup-preserved (AC-4) assertion can reuse this style.
- `internal/relay/v2session_test.go:169-184` (`waitForEnvelopes`) and `:848-…` (`decryptAppFrame`) — the helpers the live-delivery assertion reuses to wait for and decrypt the pushed live frame.
- `docs/knowledge/codebase/647.md` § "⚠️ Known issue" + § "Testing → Coverage gap that let the defect through" — the canonical defect description and the exact statement of the missing coverage: *assert subsequent live DELIVERY, not merely the absence of replay frames.*

---

## Context

#647 (PR #651, merged 2026-06-08) shipped the mid-turn reconnect replay path with a **MUST FIX still outstanding** — documented in `docs/knowledge/codebase/647.md` and present on `main`. `replayMissed` sets the per-connection dedup watermark `s.replayThrough` directly from the **untrusted** remote `afterID` even when the ring reports caught-up (no events to replay). Paired with the `forwardEnvelope` guard that drops any live frame with `EventID <= replayThrough`, this lets a `last_event_id` outside the current conversation's id space turn into a remote-triggerable mute switch on the connection's own live stream:

1. **`/clear` + reconnect.** Phone saw `event_id=100` in conversation A; `/clear` rotates to B (counter restarts at 1). Reconnect advertises `last_event_id=100`; `cursor()` resolves to B; `After(B,100)` → caught-up (B's newest id ≪ 100) → no replay, `replayThrough=100`. Every live event in B with id ≤ 100 is then dropped by the guard.
2. **Hostile / absurd `last_event_id`.** Same conversation, `last_event_id = 2^64-1` → caught-up → `replayThrough = 2^64-1` → all future live frames on that connection are dropped permanently.

The normal same-conversation caught-up case is fine (live ids are always `> afterID`), which is why the green unit suite missed it — the existing caught-up tests assert *no replay frames*, never that the **live stream still flows** afterward.

**The fix** (prescribed by the AC and `647.md` § Known issue): in the non-gap branch, clamp the watermark to server-known reality — `replayThrough = min(afterID, newestRetained(convID))` — which requires the ring to surface its newest retained id for a conversation. That keeps the legitimate same-conversation dedup (`afterID == newestRetained` there) while letting a rotated-away or hostile `afterID` fall back to the live stream.

This is a server-internal logic fix only. **No wire change**: `last_event_id` and the `resync` marker already exist on the wire (#647); `docs/protocol-mobile.md` and the `internal/protocol` package are untouched. The mobile client (#416) depends on this landing but consumes no new field.

---

## Design

Two production touch points, both additive/minimal. No new types, no new wire surface.

### 1. `internal/eventring/ring.go` — additive accessor `NewestID`

```go
// NewestID returns the newest durable event id retained for convID — the id
// the most recent Append assigned — or 0 if the conversation is unknown / has
// had no events appended. It is the #663 caught-up-watermark clamp source:
// replayMissed bounds the per-conn dedup watermark to min(afterID, NewestID),
// so an untrusted remote last_event_id beyond this conversation's id space can
// never set the watermark above a real id and silently mute the live stream.
func (r *Ring) NewestID(convID string) uint64
```

- **Contract:** returns `c.nextID - 1` for a known conversation (mirroring `After`'s internal `latestID`), `0` for an unknown one. Locked by the ring's existing `sync.Mutex` — same safe-off-the-emitter-goroutine guarantee as `After`. Because the newest event is never evicted, `nextID - 1` equals the highest retained event's id; it also equals `After`'s caught-up boundary, so the clamp is consistent with `After`'s own classification.
- **Why an accessor, not extending `After`'s signature.** `After` has **18 call sites** (1 production + 17 in `ring_test.go`); adding a return value cascades a mechanical arity bump across all of them — over the 10-call-site edit-fan-out red line, for zero correctness gain over a clean additive method. The accessor is purely additive (no existing call site changes), keeps `After`'s carefully-documented 3-way contract untouched, and — with the call ordering in §2 — is correctness-equivalent to the atomic variant. This is the project's "small additive accessor / accept interfaces, return structs / simplicity first" posture.

### 2. `internal/relay/v2session.go` — clamp in `replayMissed`

Replace the single defective line at `:1484`. The contract of the non-gap branch becomes:

- Read the conversation's newest retained id **before** classifying with `After`, and clamp: `s.replayThrough = min(afterID, newest)`.
- Reading `newest` *before* `After` is deliberate (see § Concurrency model): any event the emitter appends concurrently gets an id `> newest` and therefore falls through to the live stream rather than under the clamp. Staleness can only ever *lower* the watermark (deliver more), never raise it (suppress) — the safe direction for a "never a silent gap" guarantee.
- The gap branch is unchanged: `After` returns `(nil, true)` → `emitResync` → return early, `replayThrough` untouched.
- The replay branch is unchanged in effect: `min(afterID, newest) == afterID` there (since `afterID < latestID == newest`), and the loop still advances `replayThrough = ev.ID` per forwarded frame, ending at `newest`.

Sketch of the changed region (the loop body below it is unchanged):

```go
// Read newest BEFORE classifying: a concurrently-appended event then has
// id > newest and reaches the live stream instead of the clamp (#663).
newest := ring.NewestID(convID)
events, gap := ring.After(convID, afterID)
if gap {
    m.emitResync(ctx, s, convID)
    return
}
// Clamp the watermark to server-known reality. An untrusted afterID beyond
// this conversation's id space (stale cross-/clear id, or hostile 2^64-1)
// must not set the watermark above a real id and mute the live stream.
// min() preserves legitimate same-conversation dedup (afterID == newest there).
s.replayThrough = min(afterID, newest)
for _, ev := range events { /* unchanged: ... s.replayThrough = ev.ID */ }
```

`min` is the Go builtin (≥1.21; repo `go.mod` is `go 1.26.2`), `uint64`-typed operands.

### Data flow (unchanged except the clamp value)

```
hello.last_event_id (untrusted *uint64, AEAD-authenticated conn)
        │
        ▼
replayMissed(afterID)
        │  newest := ring.NewestID(convID)      ← new, server-known bound
        │  events,gap := ring.After(convID,afterID)
        ├─ gap   → emitResync; return            (replayThrough untouched)
        ├─ caught-up → replayThrough = min(afterID, newest)   ← FIX (was: = afterID)
        └─ replay    → replayThrough = min(afterID,newest)=afterID; loop → newest
                              │
                              ▼
                     forwardEnvelope guard: drop live frame iff EventID ≤ replayThrough
```

---

## Concurrency model

- **No new goroutine, no new lock, no new shared field.** `NewestID` is guarded by the ring's existing `sync.Mutex` (the same lock `After`/`Append` use). `replayThrough` remains Run-owned (written in `replayMissed`, read in `forwardEnvelope`, both on the manager's single `Run` goroutine) — the fix only changes the *value* assigned, not the access regime.
- **The two ring reads (`NewestID` then `After`) are separate lock acquisitions** — not atomic. This is intentional and safe because of the read order: `newest` is captured first, so any event the emitter `Append`s in the window (or at any later point) carries an id `> newest` and is delivered live, never clamped away. The only frame a clamp can ever drop is one whose id `≤ newest` that was already in the ring when measured — which, for a freshly-reconnected connection, is content it did not receive live (it joined after those events fanned out). The single benign edge — a pending-broadcast event whose id `== newest` — is recoverable via the resync-on-`conversation_id`-change path and is identical under an atomic single-read design; surfacing `newest` from `After` would not improve it. The legitimate same-conversation live-continuity path is race-free regardless: there `afterID == newest`, so `min` selects `afterID` and no concurrent append is affected.
- The cross-goroutine ring read remains safe by the ring's own mutex (#646 self-synchronised ring); the emitter keeps its lock-free single-`Run`-goroutine invariant. Replay still runs inline at the tail of `handleNoiseInit`, completing before `Run` services `drainCh`.

---

## Error handling

- `NewestID` cannot fail — it returns `0` for unknown/empty conversations, which clamps `replayThrough` to `0` and leaves the guard inert (live ids ≥ 1 all delivered). This is the correct behaviour for a conversation the daemon has no record of.
- The gap path's early return (resync, watermark untouched) is preserved exactly — an aged-out cursor still triggers a full-reload signal, never a partial gap-ful replay (AC: "do not regress the gap branch").
- No new failure modes, no new reject branches, no new log calls. The existing `forwardEnvelope` debug-on-drop posture is unchanged.
- **gofmt baseline caveat** (from `647.md` Lessons): the repo is gofmt-dirty at HEAD under local Go 1.26.2 across untouched files. Conform new code to the **repo baseline** (`git show HEAD:<file> | gofmt -l`), do not reformat untouched files, or CI's pinned Go will fight spurious diffs.

---

## Testing strategy

The production delta is two lines; **the test coverage is the deliverable** (the precise gap the green suite missed). Tests are bullet-pointed scenarios; the developer writes them in the project's table-driven, `t.Parallel()`, stdlib-only idiom.

### `internal/eventring/ring_test.go` — `NewestID` unit coverage

- **Unknown conversation** → `NewestID("never-seen") == 0`.
- **Single conversation after N appends** → `NewestID == N` (e.g. append 5 → 5).
- **After eviction** (cap small, appends > cap, e.g. `New(2)` + 5 appends) → still `5` — the counter advances independent of retention; the newest is never evicted. This is the load-bearing property the clamp relies on.
- **Conversation isolation** → `NewestID(A)` and `NewestID(B)` are independent (append 3 to A, 7 to B → 3 and 7).

### `internal/relay/v2session_replay_test.go` — the two regression tests (AC-5 mandate)

Both reconnect into the **caught-up branch** with an `afterID` outside the conversation's id space, then push a live frame and assert **delivery**. Build the manager inline (the `OtherConnsUnaffected` pattern) so the test can `mgr.Push(...)` after the handshake and decrypt the forwarded frame with `decryptAppFrame` + the initiator's recv `CipherState`.

- **Scenario A — out-of-range / hostile `last_event_id`, live stream survives (AC-2/AC-5).**
  Ring: `conv-A` populated with ids 1..5; `cursor → conv-A`. Reconnect with `last_event_id = math.MaxUint64` (and a plain out-of-range case, e.g. 99). Caught-up → no replay frames (as the existing test asserts) **and** with the fix `replayThrough == 5`. Then `mgr.Push` a live envelope with `EventID = &6` (conv's next id); assert it is **forwarded and decrypts to the pushed event**. *Under the bug (`replayThrough = MaxUint64`) this frame is silently dropped — the assertion that fails today and passes after the fix.*

- **Scenario B — `/clear` rotation, the new conversation's live events are delivered (AC-3).**
  Ring: pre-populate **`conv-B` with a few events (ids 1..3)** so `After(conv-B, stale)` lands in **caught-up, not gap**; `cursor → conv-B`. Reconnect with `last_event_id = 100` (stale from A). Caught-up → with the fix `replayThrough == 3`. Then `mgr.Push` live events in `conv-B` with ids 4, 5; assert both **delivered**.
  **Critical test note:** `conv-B` must be non-empty at reconnect. If it is empty/unknown, `After(conv-B, 100)` returns **gap** (`afterID > 0`), which emits a resync and leaves `replayThrough = 0` — a path that already delivers live events *even with the bug present*. An empty-`conv-B` test would pass against the buggy code, reproducing the exact false-confidence the green suite suffered. The caught-up branch is only reachable when the conversation already has at least one retained event with `latestID < afterID`.

- **Scenario C — legitimate same-conversation dedup unchanged (AC-4).**
  Reuse the existing replay shape (`conv-A` ids 1..5, reconnect `last_event_id = 2`, replay 3,4,5). After replay, `replayThrough == 5`; assert a live re-delivery of `EventID = &3` is **dropped** (≤ 5) while `EventID = &6` is **delivered**. This can extend the existing `ForwardEnvelopeGuard`/`ReplaysMissedTail` tests rather than add a new one — it pins that the clamp did not loosen real dedup.

### Full suite

- `make check` (`go vet` + `go test -race` + `staticcheck` + substrate-guard) and `make build` must be green.
- The `internal/relay` and `internal/eventring` suites are the behaviour oracle (a live two-phone reconnect e2e remains blocked on the structured-receive harness gap, #603/#642 — out of scope here, same inherited caveat as #647).

---

## Open questions

None blocking. Two notes for the developer:

- `min` builtin is available under the repo's pinned Go (`go.mod` `go 1.26.2`); no helper needed.
- If the developer prefers a tighter atomic read, extending `After` to also return `latestID` is the alternative the AC sanctioned — **not recommended here** because of the 18-call-site arity cascade for no correctness gain over the read-newest-first accessor (see § Design). Stay with the accessor.

---

## Security review

**Verdict:** PASS

This fix *closes* an inbound trust-boundary defect: an untrusted remote `last_event_id` could set a drop-everything-at-or-below watermark above the conversation's real id space and silently mute the connection's live stream. Walked adversarially against the standard categories:

- **[Trust boundaries]** The boundary is unchanged from #647: `hello.last_event_id` (a `*uint64`, decoded inside `handleNoiseInit` *after* Noise IK auth + device-token check + capability negotiation). This fix narrows what an accepted value can *do*: it can no longer push `replayThrough` above `NewestID(convID)`. The conversation is still daemon-resolved (`cursor()`), never phone-chosen — no foreign-conversation addressing. The clamp source (`NewestID`) is server-internal state, not attacker-derived.
- **[Completeness of the fix]** The two documented exploit classes are both closed: (1) cross-`/clear` stale id → `min(100, newestB)` = `newestB`, so B's live ids `> newestB` flow (pinned by regression Scenario B); (2) hostile `2^64-1` → `min(2^64-1, newest)` = `newest`, so all live ids `> newest` flow (pinned by Scenario A). The legitimate same-conversation dedup is preserved because `afterID == newest` there, so `min` selects `afterID` (pinned by Scenario C). No `afterID` value can now raise the watermark above a real id.
- **[Residual / silent-gap audit]** The two-read (`NewestID` then `After`) non-atomicity was audited (§ Concurrency): the read order biases all staleness toward *under*-clamping (more delivery), never over-clamping (suppression). The single benign edge (a pending-broadcast event with id `== newest` in the deferred cross-conversation case) is recoverable via the resync-on-`conversation_id`-change path and is identical under an atomic single-read design. No new silent-gap vector is introduced; the one the ticket targets is removed.
- **[Resource bounds / DoS]** `NewestID` is O(1) (a map lookup + subtraction) under the ring's existing mutex; one extra rare-path acquisition per reconnect. No new allocation, no new goroutine, no unbounded loop. Replay output remains bounded by `MaxEventsPerConversation` (#646). Reconnect-spam is gated by the unchanged Noise IK handshake cost.
- **[Information disclosure]** No new data class is exposed; the fix *reduces* exposure (it stops suppressing the owner's own live stream — it never reveals more). `NewestID` returns a count/id, not content. No application content (assistant text, tool I/O) is logged; no new log call is added. The `last_event_id` and event ids are non-secret.
- **[Cryptographic primitives]** None touched. Live and replay frames continue to seal under the existing `s.send` CipherState via `forwardEnvelope` → `Encrypt`; the guard still drops *before* seal, so the nonce sequence is never gapped. No AEAD bypass on egress.
- **[Concurrency]** `replayThrough` stays Run-owned (single-writer); `NewestID` is mutex-guarded like `After`. No new cross-goroutine sharing, no new lock ordering. The single-`Run`-goroutine invariant for `s.send`/`s.state` is preserved.
- **[Error messages / logs / telemetry]** No new reject branch, no new message. The existing static-message, no-echo, no-app-bytes posture is unchanged.

**Reviewer:** architect (self-review; canonical `agents/architect/security-review.md` not present in the worktree — walked the standard categories: trust boundaries, fix completeness, residual/silent-gap audit, resource bounds, information disclosure, crypto, concurrency, logs).
**Date:** 2026-06-17

---

## Related

- **Defect source:** [codebase/647.md](../../knowledge/codebase/647.md) § "⚠️ Known issue" (the MUST FIX this resolves) + § "Testing → Coverage gap".
- **Upstream primitive:** [codebase/646.md](../../knowledge/codebase/646.md) (`eventring` ring + `After` 3-way classifier + `nextID`/eviction discipline `NewestID` reads).
- **Pattern precedents in `v2session.go`:** `forwardEnvelope` watermark guard, `Push`/`drainOnce` live path, `handleNoiseInit` success-tail replay hook.
- **ADR:** [025](../../knowledge/decisions/025-mobile-remote-head-interactive-session.md) § Backpressure / replay (belt-and-suspenders: server watermark = the deterministic fabric complementing the phone's own `event_id` dedup). Part of EPIC #596 Phase 2 structured-streaming exit-gate.
- **Wire SSOT (unchanged):** `docs/protocol-mobile.md` § "Interactive events (v2, capability-gated)" → "Replay cursor (`event_id`)".
- **Blocks:** pyrycode-mobile#416.
