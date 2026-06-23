# Spec #722 — `queue_state` producer: fan inbound-backlog changes to interactive phones

**Epic #597 Phase 3.** Split from #705. Emit `queue_state` to interactive phones whenever a conversation's inbound message backlog changes. Security-sensitive (new outbound dispatch path carrying untrusted, phone-originated text; the `interactive` gate + per-payload conversation scoping are the confidentiality decisions). Size: **S** (held — see § Size).

## Files to read first

- `cmd/pyry/session_transition_v2.go` (whole file, ~224 lines) — **the template.** `sessionTransitionEmitterV2` solves queue_state's exact problem: a callback fired from arbitrary goroutines (`Enqueue`, non-blocking drop-on-full) hands off via a buffered `in chan` to a dedicated `Run` goroutine that does the blocking `ActiveConns` + per-conn `Push`. Copy its shape: struct, `newSessionTransitionEmitterV2`, `Run`, `broadcast`, `toWirePayload`, `startSessionTransitionStreamV2`. Note its EventID-nil + interactive-gate + per-conn-`nextID` discipline.
- `internal/msgqueue/queue.go:65-83` — `DeliverFunc` / `ChangeFunc` contracts; `:83` is the load-bearing one (`OnChange` fires from **multiple** goroutines — enqueue caller, each drain, remove caller — MUST NOT block, MUST be concurrency-safe; carries only `convID`, consumer re-reads via `Snapshot`).
- `internal/msgqueue/queue.go:193-216` — `Snapshot(convID) []QueuedMessage`; unknown conv → `nil`; fresh value-copy slice; includes the in-flight head.
- `internal/msgqueue/queue.go:98-107` — `QueuedMessage{ID, Text, TS}`; `Text` is untrusted phone content (never log it).
- `internal/protocol/messaging.go:164-198` — `QueueStatePayload{ConversationID, Queued []QueuedItem}` + `QueuedItem{QueuedMsgID, Text, TS}`; the producer maps `QueuedMessage.ID → QueuedMsgID`. **`:173-176`** — empty backlog must emit `[]` (non-nil slice), not `null`; the leaf type can't force it, so the producer must (`make([]QueuedItem, 0, …)`).
- `internal/protocol/codes.go:225` — `TypeQueueState = "queue_state"`.
- `cmd/pyry/interactive_modal_v2.go:214-246` — `broadcastInteractive`: the per-conn fan loop with `EventID` left nil ("control events, not part of the turn-event replay ring; `forwardEnvelope`'s dedup never touches `EventID==nil`").
- `internal/relay/v2session.go:1601-1656` — `broadcastModalDismissed`: documents the **deadlock hazard** — a fan-out running on the manager's Run goroutine MUST NOT call `ActiveConns` (it funnels onto that same goroutine). Explains why queue_state cannot fan out inline from `OnChange`.
- `internal/relay/v2session.go:2078-2107` (`Push`, non-blocking bounded enqueue), `:2228-2272` (`ActiveConn{ConnID, Interactive}` + `ActiveConns` — funnels onto Run; **safe from any goroutine other than the dispatch goroutine**), `:160-180` (`pushQueue.enqueue` — only `TypeAssistantDelta` is droppable; every other type is never-drop control).
- `cmd/pyry/main.go:771-796` — queue construction (`:782`, `Config.OnChange` currently absent), `go queue.Run` (`:790`), `startRelay` call (`:792`). The wiring insertion points.
- `cmd/pyry/relay.go:272-368` — `startRelayV2`: where `mgr` (`relay.NewV2SessionManager`) is built and where `startInteractiveTurnStreamV2` / `startSessionTransitionStreamV2` are wired (`:353`, `:368`). queue_state wires beside them.
- `cmd/pyry/interactive_turn_v2_test.go:19-75` — `recordedPush` + `fakeInteractiveBcast` (implements `interactiveBroadcaster`: `ActiveConns` + recording `Push`). **This is AC-4's "fake interactive phone."** Reuse verbatim.
- `docs/protocol-mobile.md:663-683` — the Queue (v2) § already documents the **wire vocabulary** and says emission timing "is the producer's (#722) runtime, documented there." AC-5 fills the `#### queue_state` subsection (`:667`) with the *when/gate/scoping*.

## Context

Phase 3 of the mobile structured stream. Siblings already landed: #719 (msgqueue introspection + `OnChange`), #720 (`queue_state` wire types), #721 (daemon `send_message`→queue wiring; the queue is live at `cmd/pyry/main.go:782`). A phone with an interactive session can type while claude is busy; those turns buffer in `internal/msgqueue`. This slice makes the daemon **push** the backlog to the phone whenever it changes — enqueue, drain-advance, or remove — so the phone shows what's still waiting and watches it drain.

ADR-025 line 118: `queue_state = {conversation_id, queued:[{queued_msg_id, text, ts}]}`, daemon→phone. The interactive structured stream already fans capability-gated events to phones; queue_state is one more producer on that surface.

There is **no** intermediate internal "QueueState" model — the SSOT types live only in the wire layer (`internal/protocol`). The producer maps `[]msgqueue.QueuedMessage` straight to `protocol.QueueStatePayload`.

## Design

### The binding constraint: `OnChange` fires from a *mix* of goroutines, including the manager's Run goroutine

This is the slice's real cost (Technical Notes called it; here is the sharp form). `msgqueue.ChangeFunc` fires synchronously on its caller's goroutine (`queue.go:189/252/361`). Those callers are:

| Trigger | Goroutine | Notes |
|---|---|---|
| **Enqueue** (send_message #721) | **manager's Run/dispatch goroutine** | `handleFrame → handleNoiseMsg → dispatchAppFrame → dispatch.Route → SendMessage → Enqueue` is fully synchronous on Run (`v2session.go:641→773→1275→1323`). |
| **Drain advance** | a per-conversation **drain goroutine** | `msgqueue` `drain` (`queue.go:361`). |
| **Remove** (dequeue_message #723) | **manager's Run/dispatch goroutine** | `dequeue_message` intercepts in `dispatchAppFrame` (same path as the modal control arms). Not wired this slice, but the seam already calls `OnChange` from there. |

Two consequences kill the naïve approaches:

1. **Inline `OnChange → ActiveConns` deadlocks.** From the enqueue/remove callers, `OnChange` runs on the manager's Run goroutine; `ActiveConns` posts to `m.snapshot` and waits for Run to service it → Run is blocked inside `OnChange` → deadlock. `broadcastModalDismissed` (`v2session.go:1601-1607`) documents this exact hazard.
2. **Inline direct `m.sessions` read races.** The deadlock-free alternative `broadcastModalDismissed` uses (read `m.sessions` directly) is valid *only on the Run goroutine* — but the **drain** caller is not on Run, so it can't use that path. And it lives in `internal/relay` (off-limits this slice).

No single inline implementation is correct for all three callers. **The fix is to decouple `OnChange` from the fan-out with a buffered channel and a dedicated Run goroutine** — exactly what `sessionTransitionEmitterV2` (#657) does for pool transitions (another arbitrary-goroutine trigger). `OnChange` does only a non-blocking channel send; the dedicated goroutine — neither the manager's Run goroutine nor a drain — does the blocking `ActiveConns` + `Push`, so `ActiveConns` is safe and `nextID` is single-goroutine.

### New file: `cmd/pyry/queue_state_v2.go`

Mirrors `session_transition_v2.go`. All new code confined here plus two small wiring edits.

**`queueStateNotify(ch chan<- string, logger *slog.Logger) msgqueue.ChangeFunc`** — factory for the `OnChange` seam. Returns a closure that does a non-blocking buffered send of `convID` with drop-on-full + a content-free `Warn` (mirror `sessionTransitionEmitterV2.Enqueue`, `session_transition_v2.go:74-82`). Drop-on-full is safe: the seam is edge-triggered and `Snapshot` re-reads current state, so a dropped notification is recovered by the next change (only the final state matters). Never blocks → satisfies `ChangeFunc`'s MUST-NOT-BLOCK contract on the drain path.

**`type queueStateEmitterV2 struct`** — fields: `in <-chan string` (the shared hand-off channel), `snapshot func(convID string) []msgqueue.QueuedMessage` (bound to `queue.Snapshot` at construction), `logger *slog.Logger`, `nextID uint64`. `nextID` is read/written **only on the single Run goroutine** — no atomic, no mutex (same contract as `sessionTransitionEmitterV2.nextID`).

**`newQueueStateEmitterV2(in <-chan string, snapshot func(string) []msgqueue.QueuedMessage, logger) *queueStateEmitterV2`** — plain constructor. Built *after* `queue` (so `Snapshot` is bound) but the channel is created *before* `msgqueue.New` and shared with the `OnChange` seam — this breaks the chicken-and-egg without any late-bound field (see § Construction).

**`(e *queueStateEmitterV2) Run(ctx context.Context, bcast interactiveBroadcaster)`** — the dedicated goroutine. `select` on `ctx.Done()` / `<-e.in`; on a `convID`, call `e.broadcast(ctx, bcast, convID)`. `bcast` is a goroutine-local parameter (supplied at start, when `mgr` exists) — no stored broadcaster field, no race. Mirrors `sessionTransitionEmitterV2.Run` (`session_transition_v2.go:86-98`).

**`(e *queueStateEmitterV2) broadcast(ctx, bcast, convID)`** — the fan-out:
1. `items := e.snapshot(convID)` — snapshot **only** this `convID` (AC-3: per-payload scoping; another conversation's text can never enter this payload).
2. `payload := toQueueStatePayload(convID, items)`; `json.Marshal` once. Marshal failure → content-free `Debug` log + return (defensive; closed struct).
3. Fresh `bcast.ActiveConns(ctx)` snapshot; for each conn `if !c.Interactive { continue }` (AC-2 gate); `e.nextID++`; build `protocol.Envelope{ID: e.nextID, Type: protocol.TypeQueueState, TS: now, Payload: payloadJSON}` with **`EventID` left nil**; `bcast.Push`; per-conn Push error → `ctx`-cancel returns early, else `Debug` + continue. Byte-for-byte the loop in `sessionTransitionEmitterV2.broadcast` (`:128-149`) / `broadcastInteractive`.

**`toQueueStatePayload(convID string, items []msgqueue.QueuedMessage) protocol.QueueStatePayload`** — pure mapping seam (unit-testable, like `toWirePayload`). Maps each `QueuedMessage{ID,Text,TS}` → `QueuedItem{QueuedMsgID: ID, Text, TS}`, preserving FIFO order. **Initialize `Queued: make([]protocol.QueuedItem, 0, len(items))`** so an empty/unknown backlog (Snapshot → `nil`) marshals to `[]`, not `null` (AC-1; protocol note `messaging.go:173-176`).

**`startQueueStateStreamV2(ctx, qse *queueStateEmitterV2, bcast interactiveBroadcaster, logger) func()`** — starts `go qse.Run(ctx, bcast)`, returns a cleanup that joins on `ctx`-cancel. Mirrors `startSessionTransitionStreamV2` (`:200-223`), except it takes a **pre-built** `qse` (the emitter must exist at `msgqueue.New` time for the shared channel; see § Construction) rather than constructing internally.

**`const queueStateQueueSize = 16`** — buffered hand-off size; copy `sessionTransitionQueueSize`'s rationale (rare, human-paced changes; drop-on-full bounds memory).

### Construction / ordering (the one wrinkle vs. the transition template)

`session_transition_v2.go` installs its trigger via `SetTransitionObserver` *inside* `startSessionTransitionStreamV2` (where `mgr` exists). queue_state can't: `OnChange` is a `msgqueue.Config` field set at `msgqueue.New()` (`main.go:782`), **before** `startRelay` builds `mgr`. Break it by creating the hand-off channel first and sharing it:

```
// cmd/pyry/main.go, runSupervisor, around :781-789
queueChanges := make(chan string, queueStateQueueSize)
queue, err := msgqueue.New(msgqueue.Config{
    Deliver:  newInboundDeliver(router.resolve),
    OnChange: queueStateNotify(queueChanges, logger),   // send-only; no mgr/queue dependency
    Logger:   logger,
})
// … existing err check, go queue.Run(ctx) …
qse := newQueueStateEmitterV2(queueChanges, queue.Snapshot, logger)  // Snapshot bound here
```

Then thread `qse` through `startRelay` → `startRelayV2` (one new param each — mirrors how `queue`, `active`, `boundHost`, `transitions` are already threaded; `startRelay`/`startRelayV2` each have exactly one call site, so no fan-out). Inside `startRelayV2`, beside the other stream wirings (`relay.go:~368`):

```
streamQueueStateCleanup := startQueueStateStreamV2(ctx, qse, mgr, logger)
// defer/collect alongside streamCleanup / streamTransitionsCleanup
```

No late-bound field: `snapshot` is bound at construction, `bcast` is a `Run` parameter. The channel is the only shared state between the `OnChange` sender (arbitrary goroutines) and the `Run` receiver (single goroutine) — channels are concurrency-safe. In practice the channel is empty until a phone connects (enqueue requires a `send_message` from a connected phone; drains only fire on real backlog changes), which is after `qse.Run` starts, so no startup races to reason about beyond the benign drop-on-full.

### Data flow

```
phone send_message ─(Run goroutine)─┐
drain advance ──────(drain goroutine)┼─► queue OnChange ─► queueStateNotify
dequeue_message #723 (Run goroutine)─┘        (non-blocking send: convID → queueChanges)

queueChanges ─► qse.Run (dedicated goroutine) ─► broadcast:
    queue.Snapshot(convID) ─► toQueueStatePayload ─► json.Marshal
    mgr.ActiveConns(ctx)  ─► for interactive conns: nextID++; mgr.Push(QueueState envelope, EventID nil)
```

### Replay-ring membership & backpressure (the pinned design call)

**Decision: `queue_state` does NOT join the #647/#649 reconnect-replay ring; `EventID` stays nil.** Rationale:
- Mirrors every other v2 control event — `session_transition` (#657) and `modal_shown`/`modal_dismissed` are all `EventID`-nil ("no replay AC"). The ring is the **turn-stream** replay source (`eventring`, owned by `interactiveTurnEmitterV2`'s single Run goroutine); joining it would force queue_state onto that goroutine/ring, re-coupling exactly what the dedicated funnel decouples.
- `queue_state` is idempotent full-state: only the latest backlog matters. Durable replay of intermediate snapshots is pointless; the current state is re-derivable and re-emitted on the next change.
- **Backpressure: never-drop control event, automatically.** `pushQueue.enqueue` (`v2session.go:160`) marks only `TypeAssistantDelta` droppable; `queue_state`, like all other types, is never evicted as a droppable delta. ADR-025 line 130's "never-drop control" class is satisfied with zero extra code.

Reconnect consequence (out of scope — see Open Questions): a phone reconnecting after missing a `queue_state` sees the current backlog only on the next change.

### Out of scope (explicit)

- **Per-connection conversation isolation.** The fan-out reaches *every* interactive conn, each payload stamped with its `conversation_id` (the phone attributes by id). There is no connection→conversation output binding today (cf. #679); a phone seeing only "its" conversation's output is undesigned and out of scope, consistent with the rest of the structured stream. AC-3 is satisfied at the **payload** level (each payload carries only its `convID`'s items), which is the confidentiality boundary within reach.
- The `dequeue_message` handler that calls `queue.Remove` is #723. This slice only *produces* `queue_state`; it correctly emits on a Remove-driven `OnChange` once #723 wires it, with no change here.

## Concurrency model

- **`OnChange` (sender):** runs on the manager Run goroutine (enqueue/remove) and drain goroutines (advance). Does only a non-blocking buffered send — never calls back into the manager, so no deadlock; never blocks, so the drain is never stalled (`ChangeFunc` contract honored).
- **`qse.Run` (single receiver):** the sole owner of `nextID` and the sole caller of `ActiveConns`/`Push` for this stream. Not the manager's Run goroutine and not a drain → `ActiveConns` is safe (its "any goroutine other than the dispatch goroutine" contract).
- **`snapshot`/`bcast`:** `snapshot` bound at construction (read-only on Run); `bcast` is a `Run` parameter (goroutine-local). No shared mutable emitter state besides the channel.
- **Shutdown:** `ctx`-cancel ends `Run`; the cleanup joins it (mirror `startSessionTransitionStreamV2`). The channel is **not** closed (a late `OnChange` send racing teardown drops harmlessly into the open-but-unread buffer — same rationale as the transition emitter).

## Error handling

- `OnChange` buffer full → drop + content-free `Warn` (`queue_full`); recovered by the next change. Never an error to the caller (the seam is fire-and-forget).
- `json.Marshal` failure → content-free `Debug` + skip (defensive; `QueueStatePayload` is a closed struct of strings/ints/time and can't fail in practice). **Never echo payload or `err.Error()`** — `Text` is untrusted phone content.
- Per-conn `Push` error → on `ctx`-cancel return early (teardown); otherwise `Debug` (transport sentinel only) + continue the fan-out (a dropped conn must not abort the others; it re-syncs on reconnect).
- **Logging discipline (security):** only content-free discriminants — `event`, `conversation_id` (a non-secret routing id), `conn_id`, `env_id`, Push's transport `err`. **Never** `text`, `queued_msg_id` values as content, the payload bytes, or `err.Error()` on the marshal path. Same posture as the three sibling emitters.

## Testing strategy

New file `cmd/pyry/queue_state_v2_test.go`. Reuse `fakeInteractiveBcast` + `recordedPush` (`interactive_turn_v2_test.go:19-75`) as the fake interactive phone, and a fake `snapshot` func returning scripted `[]msgqueue.QueuedMessage`. Drive `broadcast` directly (sync, no goroutine) for the fan-out assertions; drive `Run` + `queueStateNotify` for the channel/lifecycle ones. Scenarios (bullet form — developer writes them table-driven in the package idiom):

- **Enqueue → queue_state with expected backlog** (AC-1, AC-4): snapshot returns 2 items; `broadcast` to one interactive conn produces exactly one `queue_state` whose payload matches `{conversation_id, queued:[{queued_msg_id,text,ts}×2]}` in order. Compare `ts` via `time.Time.Equal` (monotonic-strip discipline).
- **Drain-advance → updated queue_state** (AC-1): second `broadcast` with a shorter snapshot produces a fresh `queue_state` with the reduced backlog.
- **Empty backlog → `[]` not `null`** (AC-1): snapshot returns `nil`; assert the marshaled payload contains `"queued":[]` (decode + `len==0` *and* a raw-JSON check, or assert `Queued != nil`). Guards the `make([]…,0,…)` requirement.
- **Non-interactive conn receives none** (AC-2, AC-4): mixed `ActiveConns` (one `Interactive:true`, one `false`); only the interactive conn is Pushed.
- **Per-conversation scoping** (AC-3, AC-4): a `snapshot` keyed by `convID` returning conv-A items for "A" and conv-B items for "B"; `broadcast(…, "A")` produces a payload with `conversation_id=="A"` and **only** A's items — assert B's text never appears. This proves the snapshot-the-named-conv contract.
- **`toQueueStatePayload` mapping** (pure unit): `ID→QueuedMsgID`, order preserved, empty→non-nil.
- **`queueStateNotify` drop-on-full does not block / does not panic**: fill a cap-1 channel, call the notify func again, assert it returns promptly (no deadlock) and logs the `queue_full` warn.
- **`Run` exits on `ctx`-cancel**: cancel → `Run` returns, cleanup joins.
- **`-race`**: a test that fires `OnChange` from several goroutines while `Run` drains, asserting no race on `nextID`/the channel.

No e2e / two-phone harness needed: AC-4's "fake interactive phone" is the in-package `fakeInteractiveBcast` (the same double the turn/transition/modal emitters unit-test against). This is a unit-testable producer, unlike the structured-receive capstone (#642).

## Size

**S, held.** New production: one file `cmd/pyry/queue_state_v2.go` (~115 LOC) + ~6 LOC in `main.go` + ~6 LOC in `relay.go` (thread `qse`, start the stream) ≈ **125 production LOC**, all confined to `cmd/pyry` (no `internal/*` edits — queue API, broadcaster, wire types all exist). Tests ~180 LOC. Production source files with new/modified content: `queue_state_v2.go` (new), `main.go`, `relay.go` = **3** (< 5 gate). Not refactor-shaped: additive emitter + two wiring edits; the `startRelay`/`startRelayV2` `+1 param` each touches their single call site only (no cascade). The goroutine-safety reconciliation is real but bounded — it's a faithful copy of the shipped `sessionTransitionEmitterV2`, so it does **not** downgrade to XS (the multi-goroutine `OnChange` vs. the deadlock-prone inline path is exactly the factor the ticket said holds it at S).

## Open questions

- **Reconnect snapshot of `queue_state`.** With `EventID` nil, a phone that reconnects after a missed `queue_state` sees the backlog only on the next change. If product wants the current backlog delivered on reconnect, a future slice can add `queue_state` to the `request_snapshot` reply (`handleRequestSnapshot`). No AC asks for it here; flagged for the documentation/epic owner.
- **`queueStateQueueSize = 16`.** Copied from the transition emitter's rationale. If a future high-churn workload (rapid enqueue+drain) makes drops common, revisit — but drops are self-healing (next change re-emits current state) so this is a tuning knob, not a contract.

## Security review

**Verdict:** PASS (no MUST FIX). Adversarial self-review per `architect/security-review.md`; default-FAIL walked to PASS across all applicable categories.

**Findings:**

- **[Trust boundaries] No finding.** The untrusted datum is the queued `text` (phone-originated, enqueued via `send_message` #721). It flows `Snapshot → toQueueStatePayload → QueueStatePayload.Text → Push` as **opaque transit content** — never parsed, compared, or used in any routing/gating decision. The one control value, `convID`, is the queue's **own FIFO key** (not attacker-derived at the producer); it is threaded as a **single variable** `OnChange(convID) → snapshot(convID) → payload.conversation_id`, so a payload's `conversation_id` and its items cannot desync (AC-3 enforced structurally — there is no second source for either). An arbitrary `convID` only ever yields an empty snapshot (`Snapshot` → `nil` → `[]`); it is a map key, never a path or arg, so there is no traversal/injection surface. The producer does **not** widen the existing `send_message`/#678 trust model — it mirrors out whatever the validated handler already enqueued.
- **[Tokens, secrets, credentials] N/A.** No tokens/secrets here. `queued_msg_id` is a non-secret per-conversation counter (ADR-025 §Queue: queue ops are *ungated for any paired phone*, no nonce). `EventID` is nil. No capability or auth value is minted, stored, or compared.
- **[File operations] N/A.** The producer performs **no** filesystem access — it reads the in-memory `msgqueue`. No path is constructed from any input. (Contrast the turn stream's JSONL resolvers, which are not on this path.)
- **[Subprocess / external command] N/A.** None.
- **[Cryptographic primitives] N/A.** No new crypto and no RNG. Outbound sealing reuses the V2 session's existing AEAD `CipherState` inside `forwardEnvelope` (`v2session.go:2185`), unchanged. `nextID` is a non-secret per-conn counter, not a token; per-emitter-independent ID spaces are the established pattern (modal/transition emitters), and the phone correlates on `conversation_id`+`queued_msg_id`, not global envelope order.
- **[Network & I/O] SHOULD FIX → OUT OF SCOPE (#704).** The producer fans the **full** backlog on **every** change, so n enqueues during a busy turn cost ≈ O(n²) marshalled bytes, and `msgqueue` currently has **no per-conversation backlog cap** (`queue.go` Enqueue appends unconditionally; #704 explicitly deferred "the single insertion point for a future bound is Enqueue"). This is bounded in practice — the enqueuer is an authenticated paired phone (network-rate-limited, same trust domain), and the push buffer never retains unboundedly: at `pushQueueCap=256` a never-drop control envelope that can't be buffered is **dropped**, not accumulated. So this is CPU-only, self-inflicted, and bounded — not remote-amplifiable against another party. The correct cap belongs in `msgqueue` (#704), not this producer; flagged, not gated. No inbound socket read happens here (size caps live at the frame decode upstream).
- **[Error messages, logs, telemetry] No finding (primary control — reinforced).** The untrusted `text` is the thing that must never leak. Every log line in the design carries **only** content-free discriminants — `event`, `conversation_id` (a non-secret routing id), `conn_id`, `env_id`, and Push's transport-sentinel `err`. MUST-NOT-log: the payload bytes, any `text`, and `err.Error()` on the marshal path (`encoding/json` can quote input bytes into its error). This matches the three sibling emitters and `msgqueue`'s own discipline. The spec's § Error handling mandates it explicitly; code-review must verify no `text`/payload field reaches any `slog` call.
- **[Concurrency] No finding.** (a) **Deadlock-free:** `OnChange` does only a non-blocking channel send — it never calls `ActiveConns`/`Push`, so even when it runs on the manager's Run goroutine (enqueue/remove) it cannot block on Run servicing itself; the blocking `ActiveConns` runs on the dedicated `qse.Run` goroutine, which is neither the dispatch goroutine nor a drain (its documented safe-from-any-other-goroutine contract). (b) **Race-free:** `nextID` is single-goroutine (Run only); `snapshot` is construction-bound and read-only; `bcast` is a `Run` parameter (goroutine-local); the channel is the only cross-goroutine shared state (concurrency-safe); the `OnChange` closure captures the **channel**, not `qse`, so `qse` is never read off the constructing goroutine except via `go qse.Run` (happens-before). (c) **No lock nesting** — the producer holds no lock; `Snapshot`/`Push` take and release their own leaf locks (and `msgqueue` fires `OnChange` only *after* releasing `q.mu`, so the Run-goroutine `Snapshot` can't deadlock a drain). (d) **Lifecycle:** `Run` exits on `ctx.Done()`, cleanup joins it; the channel is intentionally not closed (a late send racing teardown drops harmlessly). No leak. `-race` test mandated.
- **[Threat model alignment] OUT OF SCOPE (named).** protocol-mobile.md §Security model: the *old/non-interactive phone never receives v2 events* threat is closed by the `interactive` gate (AC-2) atop `forwardEnvelope`'s `V2StateOpen` gate. **Per-connection conversation isolation is OUT OF SCOPE**: every interactive phone receives every conversation's `queue_state` (fan-by-capability, each payload stamped with its `conversation_id`), exactly as the rest of the structured stream fans turn/modal/transition events — and consistent with ADR-025's *ungated-for-any-paired-phone* model (a user's paired devices are one trust domain). The confidentiality boundary **within reach** is per-payload scoping (AC-3), which the design enforces. Per-connection isolation is undesigned (no connection→conversation output binding; cf #679) and would be a cross-cutting change to the whole structured stream, not this producer.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-23
