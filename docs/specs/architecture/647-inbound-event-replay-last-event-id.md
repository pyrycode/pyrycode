# Spec #647 — Inbound mid-turn replay: `hello.last_event_id` → ring replay / resync marker

**Ticket:** [#647](https://github.com/pyrycode/pyrycode/issues/647) · **Size:** S (confirmed) · **Label:** `security-sensitive`
**EPIC:** #596 Phase 2 structured streaming · **ADR:** [025](../../knowledge/decisions/025-mobile-remote-head-interactive-session.md) § Backpressure / replay
**Upstream (both merged):** #646 (event ring + durable id), #649 (durable `event_id` on the outbound wire)

The reconnect **consumer** of mid-turn replay. A phone advertises where it left off via `hello.last_event_id`; the daemon, on that connection, replays the conversation's events with `id > last_event_id` from the in-memory ring (#646) **before** the live stream resumes — or, when the requested position has aged out of the bounded ring, emits an honest **resync marker** telling the phone to do a full reload. `last_event_id` is untrusted remote input: range/shape it, bound the work by the ring, scope it to the reconnecting conversation and connection.

---

## Files to read first

| Path / range | Extract |
|---|---|
| `internal/eventring/ring.go:140-183` | `Ring.After(convID, afterID) (events, gap)` — the **3-way classification** this slice consumes: caught-up `(nil,false)` / gap `(nil,true)` / replay `(events,false)`. `Event{ID,Type,Payload,TS}` at `:53`. Replay reads this; nothing in the ring changes. |
| `internal/relay/v2session.go:734-948` | `handleNoiseInit` — the reconnect handshake. The hello is decoded at `:815-824` (`helloPayload`); the success tail (`:925-947`) sends `noise_resp`, sets `V2StateOpen`, creates the push queue under `pushMu`. **The replay hook goes at the tail of this success path.** |
| `internal/relay/v2session.go:1262-1379` | `handleRequestSnapshot` / `snapshotReplyError` — the established **inline control-reply via `forwardEnvelope`** pattern (seal immediately on the Run goroutine, bypass the buffered push stream). The replay + resync emit mirror this exactly. |
| `internal/relay/v2session.go:1754-1799` | `forwardEnvelope` — the single seal-and-forward path. The `V2StateOpen` gate is here; the **replay-watermark guard** is added here. |
| `internal/relay/v2session.go:1213-1252` | `handleRekeyRequest` + `emitRekeyRequest` (`:1400-1456`) — the **payload-less / inline-anonymous-struct control envelope** precedent (`TypeRekeyRequest` has no named payload struct). The resync marker mirrors this — no new `protocol` payload type. |
| `internal/relay/v2session.go:463-528` | `pushMu` doc + `NewV2SessionManager` — `pushMu` is the leaf lock that lets an off-Run goroutine reach manager state without touching Run-owned `m.sessions`. The replay-source setter reuses it. |
| `internal/protocol/handshake.go:24-48` | `HelloClientPayload` + the `LastSeenTS`/`Capabilities` `omitempty` precedent the new `LastEventID` field mirrors (key absent → byte-identical round-trip). |
| `internal/protocol/codes.go:64-125` | The v2-only `Type*` const blocks (`TypeRekeyRequest`, `TypeRequestSnapshot`/`TypeScreenSnapshot`) + the **"MUST NOT appear in v1TypeSet; partitioned in compat_test.go's `v2OnlyTypes`"** discipline `TypeResync` must follow. |
| `internal/protocol/compat_test.go:84-145` | `v2OnlyTypes` (test-local) — `TypeResync` must be added here or the disjoint-partition drift detector fails. |
| `cmd/pyry/interactive_turn_v2.go:67-117` | The emitter — `ring *eventring.Ring` (`:93`) is **emitter-owned, unexported, no accessor**; created in the constructor (`:115`). `cursorReader.CurrentConversation()` keys ring appends (`Handle`, `:131`). This is the ring the manager must read. |
| `cmd/pyry/interactive_turn_stream_v2.go:45-52` | `startInteractiveTurnStreamV2(... mgr *relay.V2SessionManager ...)`; `emitter := newInteractiveTurnEmitterV2(sup, mgr, logger)`. **The one-line `SetReplaySource` wiring goes immediately after `:52`.** `mgr` is concrete; `emitter.ring` is same-package. |
| `internal/supervisor/supervisor.go:261` | `func (s *Supervisor) CurrentConversation() string` — the cursor (#312). `sup.CurrentConversation` (method value, `func() string`) is the conversation-resolution seam. **No new supervisor plumbing.** |
| `docs/knowledge/codebase/646.md` (§ Concurrency, § Patterns) | Why `After` is callable off the emitter goroutine (the ring's own mutex), and why durable `event_id` ≠ per-conn `env.ID`. |
| `docs/knowledge/codebase/649.md` (§ Patterns) | `Envelope.EventID *uint64 json:"event_id,omitempty"` — the durable cursor the phone learned outbound and now advertises back. |
| `internal/relay/v2session_test.go` (esp. `startManager`, the handshake/initiator helpers, frame-decrypt helpers) | The harness the replay tests reuse: drive a real Noise handshake with a hello carrying `last_event_id`, decrypt the forwarded frames, assert. |

---

## Context

ADR 025 § Backpressure / replay specifies the reconnect contract: the daemon keeps a bounded per-conversation event ring; a phone records the latest durable `event_id` it has seen and, on mid-turn reconnect, advertises it as `last_event_id`; the daemon replays the missed tail or signals a resync if the position fell off the bounded window. #646 built the ring + `After`. #649 surfaced `event_id` on the live outbound wire so a phone *has* a position to advertise. This slice closes the loop: accept `last_event_id` inbound and act on it.

`backfill_since` (the long-absence full-reload handler) is **out of scope** — see § Out of scope. This slice only *emits* the resync signal; it does not implement the reload handler.

---

## Design

### The circular dependency (the load-bearing decision)

The emitter and the manager have a **circular** relationship:

- `newInteractiveTurnEmitterV2(sup, mgr, logger)` takes the manager as its `interactiveBroadcaster` (`ActiveConns` + `Push`).
- The replay path needs the emitter-owned `eventring.Ring` (`emitter.ring`, unexported, created **inside** the emitter constructor — codebase/646 made it emitter-owned to avoid the 20-call-site constructor cascade).
- Construction order (`cmd/pyry/relay.go`): `NewV2SessionManager` (:287) → `go mgr.Run` (:315) → `startInteractiveTurnStreamV2` builds the emitter (:340). **The ring does not exist when the manager is constructed.**

Therefore the ticket's literal hint ("wire the ring into `V2SessionConfig` the way `Snapshotter`/`KnownConversation` are") is **not buildable as a construction-time field**: a config field forces ring-creation-before-manager, which forces injecting the ring into the emitter constructor (20-call-site cascade) or hoisting + a delegate constructor (6 production files — over the size gate).

**Resolution — a late-bound replay-source setter.** Break the cycle by publishing the ring to the manager *after* the emitter exists, via a setter called once during wiring. This is the only seam that keeps the change at 4 production files / S.

### Contracts

**1. Protocol surface (`internal/protocol`)**

- `internal/protocol/handshake.go` — one additive field on `HelloClientPayload`:
  - `LastEventID *uint64 `json:"last_event_id,omitempty"`` — placed after `Capabilities`, with a doc comment mirroring `LastSeenTS`/`Capabilities`: optional; a phone advertising none round-trips byte-identically with today's hello (**key absent, not null**); a pointer so a non-nil value never encodes `0` and nil is omitted. SECURITY note in the comment: untrusted remote input — range/shape-validated and ring-bounded by the consumer (`internal/relay`), not here.
- `internal/protocol/codes.go` — one additive const in a new v2-only block:
  - `TypeResync = "resync"` — binary → phone, outbound v2 control marker. Doc comment per the file's convention: **MUST NOT be added to `v1TypeSet`** (`envelope.go`); it is partitioned into `v2OnlyTypes` in `compat_test.go`.
  - **No named payload struct.** The resync marker carries its conversation id in an inline anonymous `struct{ ConversationID string `json:"conversation_id"` }` marshalled at emit time — exactly the precedent `TypeRekeyRequest` sets (no `RekeyRequestPayload`; `handleRekeyRequest`/`emitRekeyRequest` use inline structs). This deliberately avoids a third `protocol` file.
- `internal/protocol/compat_test.go` (test) — add `TypeResync` to the `v2OnlyTypes` literal so the disjoint-partition drift detector stays green.

**2. Manager replay seam (`internal/relay/v2session.go`)**

New manager state + setter (the late-bind):

```
// replayRing + replayCursor are the reconnect-replay source, published once
// after the emitter is built (SetReplaySource). Guarded by the existing pushMu
// leaf lock: set-once off the Run goroutine, read on the Run goroutine at
// reconnect. nil/nil ⇒ replay disabled (no setter called, or stream off).
replayRing   *eventring.Ring
replayCursor func() string
```

- `func (m *V2SessionManager) SetReplaySource(ring *eventring.Ring, currentConv func() string)` — stores both under `m.pushMu`. Idempotent-by-construction (called once); nil-tolerant. 1-line body + lock.
- `func (m *V2SessionManager) replayMissed(ctx, s *V2Session, afterID uint64)` — the replay/resync decision, run inline on the Run goroutine. Behaviour (full prose contract; **not** a 20-line body):
  1. Read `(ring, cursor)` under `pushMu`; if either nil → return (replay disabled).
  2. `convID := cursor()`; if `convID == ""` → return (no active conversation; nothing to replay).
  3. `events, gap := ring.After(convID, afterID)`.
  4. **gap** → emit one resync marker for `convID` via `forwardEnvelope` (see below) and return. **Do not** set the watermark (the phone full-reloads and must accept all live events afterward).
  5. **caught-up** (`events == nil, gap == false`) → set `s.replayThrough = afterID` and return (no frames; guards against a late in-flight live push ≤ `afterID`).
  6. **replay** → set `s.replayThrough = afterID`, then for each `ev` in ascending order: `forwardEnvelope(ctx, s.connID, <replay envelope>)`, then `s.replayThrough = ev.ID`. The watermark trails one event behind during the loop so a replay envelope is never self-dropped by the guard.
  - Replay envelope shape: `protocol.Envelope{ID: ev.ID, Type: ev.Type, TS: ev.TS, Payload: ev.Payload, EventID: &id}` where `id := ev.ID` is a **per-iteration local** (do not take `&ev.ID` of the range variable). `EventID` is **required** — the phone advances its cursor from it. `ID = ev.ID` makes the per-conn id ascending and self-consistent within the replay; the phone correlates the structured stream on `event_id`, not `env.ID` (env.ID is informational here — codebase/649 § Patterns).
  - Resync marker shape: `protocol.Envelope{ID: 1, Type: protocol.TypeResync, TS: <now UTC>, Payload: <inline {conversation_id: convID}>}`. `ID:1` is non-load-bearing (matches `handleRequestSnapshot`/`emitRekeyRequest`); the phone keys resync handling on `Type`, not `ID`. **No `EventID`** (it is not a structured event), so the watermark guard never touches it.
- **Hook in `handleNoiseInit`** — at the very tail of the success path (after the `pushMu` queue-create block at `:944-946`, after `s.rekeyTimer = …`), add:
  - `if helloPayload.LastEventID != nil { m.replayMissed(ctx, s, *helloPayload.LastEventID) }`
  - Placement rationale: `noise_resp` has already been sent (`:932`, so the phone's recv CipherState exists), the session is `V2StateOpen` (so `forwardEnvelope`'s gate passes and `s.send` seals correctly), and the queue exists. The replay seals under the **new** session keys, after the handshake, as the first AEAD-transport frames.
- **`replayThrough` watermark on `V2Session`** + a guard in `forwardEnvelope`:
  - New field `replayThrough uint64` on `V2Session` (Run-owned; no lock — same regime as `s.state`/`s.interactive`).
  - In `forwardEnvelope`, after the `V2StateOpen` check and before the marshal: `if env.EventID != nil && *env.EventID <= s.replayThrough { return nil }` — drop a live structured envelope already covered by replay. Envelopes with `EventID == nil` (snapshot, error, rekey, resync) are never dropped. Conns that never advertised `last_event_id` keep `replayThrough == 0`, and live ids are ≥ 1, so the guard is inert for them.

**3. Wiring (`cmd/pyry/interactive_turn_stream_v2.go`)** — one line after `emitter := newInteractiveTurnEmitterV2(sup, mgr, logger)` (`:52`):

```
mgr.SetReplaySource(emitter.ring, sup.CurrentConversation)
```

`emitter.ring` is same-package (unexported, no accessor needed); `mgr` is the concrete `*relay.V2SessionManager`; `sup.CurrentConversation` is the `func() string` cursor. No signature change, no new gate — the helper already runs only when `bridge != nil && claudeSessionsDir != ""` (the same gate that means the emitter is live and writing to `emitter.ring`). When the stream is disabled, `SetReplaySource` is never called → `replayRing`/`replayCursor` stay nil → `replayMissed` short-circuits → a phone advertising `last_event_id` simply gets no replay (live stream only). No resync spam on a feature-off daemon.

### Data flow

```
phone reconnect (noise_init, hello.early_data carries last_event_id=N)
        │  Run goroutine
        ▼
handleNoiseInit ── send noise_resp ── state=Open ── create queue
        │
        └── replayMissed(ctx, s, N)
              convID = cursor()                       (sup.CurrentConversation)
              events, gap = ring.After(convID, N)     (self-synchronised ring read)
              ├── gap   → forwardEnvelope(resync{convID})            ; replayThrough untouched
              ├── caught→ replayThrough = N                          ; no frames
              └── replay→ replayThrough = N; ∀ev: forwardEnvelope(ev); replayThrough = ev.ID
        │
        ▼ (handleNoiseInit returns; Run resumes its select)
live stream: emitter.Push → drainCh → drainOnce → forwardEnvelope
              guard drops env.EventID ≤ replayThrough (the transient overlap), forwards the rest
```

---

## Concurrency model

- **No new goroutine.** Replay + resync run **inline on the manager's single Run goroutine**, inside `handleNoiseInit`, via `forwardEnvelope` (the established `handleRequestSnapshot` pattern). Because Run is single-threaded, the entire replay completes **before** Run returns to its select to service `drainCh` (live events) or `m.snapshot` (the emitter's `ActiveConns`). This is what structurally guarantees AC-2's "before the live stream resumes."
- **Cross-goroutine ring read is already safe.** `ring.After` runs on the Run goroutine; the emitter's `ring.Append` runs on the producer goroutine. The ring is self-synchronised by its own `sync.Mutex` (codebase/646 § Concurrency) — the emitter keeps its lock-free single-Run-goroutine invariant unchanged. No new lock on the ring.
- **The replay-source publish is race-clean.** `SetReplaySource` writes `replayRing`/`replayCursor` under `pushMu`; `replayMissed` reads them under `pushMu`. `pushMu` is the existing leaf lock (taken alone, orders below nothing), so no new lock and no new ordering. The publish happens once during wiring (after `go mgr.Run`); the read happens at reconnect (much later, after a full network handshake). The narrow window where a reconnect lands between `go mgr.Run` and `SetReplaySource` reads nil → that one reconnect gets no replay (benign: live stream only), **never** a torn read or a crash.
- **`replayThrough` is Run-owned.** Written in `replayMissed` (on Run) and read in `forwardEnvelope` (on Run) — same single-writer regime as `s.state`. No atomic, no lock.
- **The duplicate this watermark prevents is a proven race, not speculative.** Sequence: the emitter's `emit()` does `ring.Append(X=50)` (producer goroutine), then `ActiveConns()` (blocks on `m.snapshot`); meanwhile Run processes the reconnect — `After(convID, 45)` returns `…,50` and replays it; `handleNoiseInit` returns; Run then services the pending `ActiveConns`, returning the now-Open conn; the emitter `Push`es `env(EventID=50)` live → without the guard the phone receives event 50 twice (replay + live). The watermark (`replayThrough=50` after replay) makes `drainOnce`'s `forwardEnvelope` drop the live `EventID=50 ≤ 50`. Deterministic (different fabric from the phone's own `event_id` dedup, which remains a defense-in-depth layer).

---

## Error handling

- **Absent `last_event_id`** (nil pointer): `handleNoiseInit` skips `replayMissed` entirely → no replay, normal live stream (AC-1).
- **Replay disabled** (no `SetReplaySource`, or nil ring/cursor): `replayMissed` returns early → no replay, no resync.
- **No active conversation** (`cursor() == ""`): return early → nothing to replay (the daemon has no in-flight turn to catch up on).
- **Caught-up** (`afterID ≥ latest`, incl. `afterID` far in the future / hostile-large): `After` returns `(nil,false)` → no frames, `replayThrough = afterID`. Bounded.
- **Gap** (`afterID` predates the oldest retained event, or unknown conversation with `afterID > 0`): `After` returns `(nil,true)` → one resync marker, **never** a partial gap-ful replay (AC-4).
- **`forwardEnvelope` failure during replay** (session vanished / seal failure): returns a wrapped error; log at debug and continue (the package's outbound-drop posture — mirrors `handleRequestSnapshot`'s push-dropped branch). Never echo payload/ciphertext/key bytes.
- **Malformed / out-of-range / hostile `last_event_id`** (AC-5): a non-numeric or non-integer JSON value fails `HelloClientPayload` decode → the existing hello-decode-failure path closes the conn at 4421 (no special handling needed). A syntactically valid but absurd `uint64` is caught-up (no work) or gap (one resync) — both bounded. Replay is bounded by `MaxEventsPerConversation` (the ring's hard cap) and scoped to `cursor()`'s single conversation, so a hostile id can neither trigger unbounded work nor surface another conversation's events.

---

## Testing strategy

**`internal/protocol/handshake_test.go`** (additions only) — `HelloClientPayload` round-trip:
- nil `LastEventID` → key **absent** from marshalled bytes (no `"last_event_id"`); the pre-existing v1 hello fixtures round-trip byte-identically (AC-1 "absent round-trips byte-identically").
- set `LastEventID` → `"last_event_id":<n>` present and round-trips to a pointer to the same value.

**`internal/protocol/compat_test.go`** (additions only) — `TypeResync` added to `v2OnlyTypes`; the disjoint-partition + count assertions stay green (proves it is not in `v1TypeSet`).

**`internal/relay/v2session_test.go`** (additions only; reuse `startManager` + the existing Noise-initiator / frame-decrypt helpers). Each scenario drives a handshake whose hello carries `last_event_id`, against a manager whose `SetReplaySource` was given a **hand-populated** `eventring.Ring` + a stub cursor returning a fixed `convID`, then decrypts the forwarded frames and asserts:
- **In-ring replay (AC-2):** ring holds ids 1..5 for `convID`; `last_event_id=2` → the conn receives exactly events 3,4,5, ascending, each carrying `event_id` 3,4,5, **before** any live frame; assert no skips/dups.
- **Caught-up / idempotent (AC-3):** `last_event_id ≥ 5` (and a far-future value) → **no** replay frame; re-running the handshake with the same `last_event_id` replays nothing new.
- **Gap → resync (AC-4):** populate-then-evict so the oldest retained id is > `last_event_id+1` (use the codebase/646 eviction-boundary technique); assert a single `TypeResync` envelope with `conversation_id == convID` and **no** structured replay frames.
- **Absent `last_event_id` (AC-1):** hello without the field → no replay frame; the conn proceeds to normal open state.
- **Untrusted-input bound (AC-5):** a hostile-large `last_event_id` → caught-up, no frames, no panic; replay never returns more than the ring holds; with the cursor stubbed to conversation B while the ring holds only conversation A, a replay request surfaces **no** A events (scoped to `cursor()`).
- **Other conns unaffected:** a second open conn receives nothing from the first conn's replay.
- **Watermark guard (the dedup mechanism, unit-level):** with `s.replayThrough = 5`, `forwardEnvelope` of an `EventID=5` envelope is dropped (not sent) while `EventID=6` is forwarded; an `EventID == nil` envelope (e.g. a snapshot/error) is never dropped.
- **Replay-disabled:** no `SetReplaySource` (nil ring) → a hello with `last_event_id` yields no replay and no resync.

**`cmd/pyry/interactive_turn_stream_v2_test.go`** (additions only, optional): assert that after `startInteractiveTurnStreamV2`, the manager's replay source is wired (e.g. a reconnect with an in-ring `last_event_id` against the real shared ring replays) — only if it fits without a harness expansion; the unit coverage above is the behaviour oracle.

**No live two-phone e2e in this slice.** The structured-receive e2e is blocked on the same harness gap as #642 (fakeclaude emits no structured JSONL; realclaude has no Noise-phone harness; #603 open). The `internal/relay` unit tests driving a real Noise handshake against a hand-populated ring are the behaviour oracle here (the inherited #589/#615/#632/#633 caveat). `make check` (vet + `-race` + staticcheck + substrate-guard) and `make build` green.

---

## Scope (S confirmed)

**4 production `.go` files:** `internal/protocol/handshake.go`, `internal/protocol/codes.go`, `internal/relay/v2session.go`, `cmd/pyry/interactive_turn_stream_v2.go`. (Under the §4 ≥5-file gate.) Test files: `handshake_test.go`, `compat_test.go`, `v2session_test.go` — excluded from the count.

- **New exported types:** 0 (`TypeResync` is a const; `SetReplaySource` is a method; `LastEventID`/`replayThrough` are fields). Under 5.
- **Edit fan-out:** 0 cascade. The new `HelloClientPayload` field is additive (`omitempty`); `TypeResync` is additive; `SetReplaySource` is a new method (no call-site change); the wiring is one new line. `newInteractiveTurnEmitterV2` is **untouched** (the late-bind avoids the constructor cascade #646 flagged). Well under 10.
- **Reject/branch count in the replay state machine:** disabled / no-active-conv / absent-id / caught-up / gap / replay ≈ 6. Under 10.
- **ACs:** 5. At the boundary, not over.
- **Point (2) "resolve the conversation to replay for" did not need significant new plumbing** — it is `sup.CurrentConversation` (the existing #312 cursor), wired as a `func() string` exactly like `KnownConversation`. The ticket's flag-back condition for point (2) is **not** triggered.

**§1.5 file-overlap:** the branch scan flags stale `feature/449` (touching `codes.go` + `v2session.go`), but **#449 is CLOSED** (the re-key responder shipped 2026-05-17; `handleRekeyInit`/`TypeRekeyRequest` are already in main). `feature/449` is an undeleted stale branch for already-merged work, **not** in-flight — no integration-time conflict. The §1.5 block targets concurrent *active* tickets; not triggered here.

---

## Open questions

1. **Cross-`/clear` reconnect (stale `last_event_id` from a rotated-away conversation).** The hello carries only `last_event_id`, no conversation id; the daemon resolves `convID` from the current cursor. If the conversation rotated (`/clear`) while the phone was away, `last_event_id` refers to the *old* conversation and `After(currentConvID, last_event_id)` reports caught-up (the new conversation has fewer events) → no replay. The phone detects the conversation change via the `conversation_id` field on the resumed live stream's envelopes and resyncs itself. **This slice scopes replay to the current conversation only**, which is the AC's "the conversation's events" intent; cross-conversation catch-up is the deferred `backfill_since` territory. Flagged for the documentation phase / #369 phone contract.
2. **`replayThrough` vs. phone-side `event_id` dedup.** The watermark is a deterministic daemon-side guarantee for AC-2; the phone (#369) also dedups by `event_id` for replay-safety. The two are independent fabrics (defense in depth). If real-world testing shows the watermark is redundant given the phone's dedup, it could be retired in a follow-up — but it is cheap (1 field + 1 guard) and makes the AC provable in a unit test, so it ships here.
3. **`MaxEventsPerConversation` calibration (1024).** Inherited from #646; the reconnect e2e (#642, when the harness lands) or a Phase 2 load test is the right place to tune it. Not touched here.

---

## Out of scope (deferred)

- **The `backfill_since` full-reload handler** a phone calls in response to the resync marker. It needs a message-history store that does not exist (`internal/conversations` is metadata-only); `backfill_since` is a phone→binary *type* with no handler. This slice only **emits** the resync signal. File a follow-up when a message store lands.
- **Surfacing `event_id` on the live stream** — sibling #649 (merged). This slice consumes the position the phone learned from #649.
- **A live two-phone reconnect-replay e2e** — blocked on the structured-receive harness gap (#603; see #642). Noted, not built.

---

## Security review

**Verdict:** PASS

This slice opens an inbound trust boundary: `hello.last_event_id` is attacker-controllable remote input that, accepted, causes the daemon to return buffered conversation content. Walked adversarially:

- **[Trust boundaries]** Single new boundary: `helloPayload.LastEventID` (decoded inside `handleNoiseInit`, *after* Noise IK authentication and *after* the device-token check — the replay hook is at the success tail, so a replay is only ever served to an **authenticated, token-validated, capability-negotiated** conn). The value flows only into `ring.After(cursor(), *LastEventID)`. It is a `*uint64`: a non-integer/over-range JSON value fails `HelloClientPayload` decode and hits the existing 4421 close (no new branch); a valid `uint64` is classified by `After` into caught-up/gap/replay. No code path lets the phone choose the *conversation* — `convID` comes from the daemon's own `cursor()`, never from the phone — so the phone cannot address another conversation's ring. Pinned by the AC-5 conversation-scoping test (cursor→B, ring holds A → zero A events).
- **[Resource bounds / DoS]** Replay output is bounded by `MaxEventsPerConversation` (the ring's hard cap, enforced at `Append`-time eviction, #646) — a single reconnect can forward at most one ring's worth of frames, regardless of `last_event_id`. `After` is O(retained) with an early caught-up/gap short-circuit; no unbounded loop, no allocation amplification. A hostile-large `last_event_id` is caught-up (zero work). Replay runs inline on the Run goroutine, so a reconnect cannot fan out work onto new goroutines. The only repetition vector is reconnect-spam, which is gated by the full Noise IK handshake cost (unchanged from #445) and bounded per reconnect.
- **[Information disclosure]** Replayed payloads are the same structured envelopes #649 already streams to interactive conns — no new data class is exposed, and only to the conn that authenticated as the owner of this daemon's session. Cross-conversation leakage is structurally prevented (convID is daemon-resolved). The resync marker carries only `conversation_id` (the daemon's own id, not attacker-derived). **No application content (assistant text, tool I/O) is logged** at any level — replay/resync log lines carry only `conn_id`, `conversation_id`, event class, counts, and `close_code`; the replayed bytes are never named in a log field (extends the #632/#646 no-app-output-in-logs discipline). The `last_event_id` value itself is non-secret and may appear in a debug field if useful.
- **[Cryptographic primitives]** No new primitive. Replay/resync frames seal under the **existing** `s.send` CipherState via the shared `forwardEnvelope` → `Encrypt` → `noise_msg` path; they are the first AEAD-transport frames after the handshake's `noise_resp`, so the send nonce sequence is intact and strictly ordered (all sends are on the single Run goroutine). The watermark guard drops frames *before* seal, so it cannot gap the nonce. No bypass of AEAD on egress.
- **[Concurrency]** Replay-source publish is `pushMu`-guarded (set-once/read); `replayThrough` and the replay itself are Run-owned. No new cross-goroutine sharing beyond the already-reviewed self-synchronised ring. The single-Run-goroutine invariant for `s.send`/`s.state` is preserved (no new mutation site off Run). Covered in § Concurrency model.
- **[Error messages / logs / telemetry]** Reject/decode failures reuse the existing static-message, no-echo posture (`handleNoiseInit`'s hello-decode path; `handleRequestSnapshot`'s no-render-error-echo). The resync marker's message is the daemon's own `conversation_id`; never attacker bytes.
- **[Threat model alignment]** Threat #3 (relay MITM): replay frames are AEAD-sealed; a tampered/injected frame fails the phone's recv AEAD. Threat #5 (compromised phone): a paired device can advertise any `last_event_id`, but the worst it achieves is replaying **its own** conversation's bounded tail or triggering a resync — no escalation, no foreign-conversation access. Threat #6/#7 (replay/tamper of the wire): unchanged from #445/#446 — the Noise transport rejects them before `handleNoiseInit`'s success path is reached.

**Reviewer:** architect (self-review; canonical `agents/architect/security-review.md` not present in the worktree — walked the standard categories: trust boundaries, resource bounds, information disclosure, crypto, concurrency, logs, threat-model alignment).
**Date:** 2026-06-08

---

## Related

- **Upstream:** [codebase/646.md](../../knowledge/codebase/646.md) (ring + `After` + durable id), [codebase/649.md](../../knowledge/codebase/649.md) (`event_id` on the wire).
- **Wiring template:** [codebase/633.md](../../knowledge/codebase/633.md) (`startInteractiveTurnStreamV2`), [codebase/632.md](../../knowledge/codebase/632.md) (the emitter that owns the ring).
- **Pattern precedents in `v2session.go`:** `handleRequestSnapshot`/`snapshotReplyError` (inline control reply via `forwardEnvelope`), `handleRekeyRequest`/`emitRekeyRequest` (payload-less inline-struct control envelope).
- **ADR:** [025](../../knowledge/decisions/025-mobile-remote-head-interactive-session.md) § Backpressure / replay.
- **Phone consumer:** pyrycode-mobile#369.
- **Split lineage:** #611 → #646 / #649 / **#647** (this).
