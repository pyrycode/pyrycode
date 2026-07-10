# #878 — Reconcile queue truth on connect (re-send outstanding `queue_state`)

**Size:** S · **Security-sensitive:** yes · **Packages:** `internal/msgqueue` (enumeration seam) + `internal/relay` (reconcile helper/seam/call site) + `cmd/pyry` (wire)

Twin of **#877**. That ticket established the connect-time reconcile hook in `handleNoiseInit`'s
interactive-open success tail (an `OutstandingModals` closure seam on `V2SessionConfig`, a
`reconcileModals` helper that unicasts to `s.connID`, and the call site). #878 reuses that hook for
the queued-message backlog and delivers #829's `queue_state` half. The shape is a near-mechanical
mirror; the one genuinely new piece is a pure-read enumeration seam on `msgqueue.Queue` (its `convs`
map is private).

## Files to read first

- `internal/relay/v2session.go:2195-2242` — `reconcileModals`: **the exact helper to mirror.** Guard
  (`!s.interactive || seam == nil`) → snapshot-once → per-payload `json.Marshal` + `m.Push(ctx,
  s.connID, env)` loop with content-free logging on both error branches. `reconcileQueues` is this
  with `protocol.TypeQueueState` / `QueueStatePayload` substituted.
- `internal/relay/v2session.go:738-756` — `V2SessionConfig.OutstandingModals`: the optional
  closure-returning-`protocol`-payload seam. `OutstandingQueues` is its sibling (same nil-default
  semantics, same "define the dependency where it is consumed" rationale — relay imports `protocol`,
  not `msgqueue`).
- `internal/relay/v2session.go:1408-1434` — `handleNoiseInit` success tail: `s.interactive` and
  `s.state = V2StateOpen` set (1407-1408), `m.queues[s.connID]` created (1413-1415), then
  `m.reconcileModals(ctx, s)` (1434). **The `reconcileQueues` call goes immediately after line 1434**,
  before the `#647` replay block.
- `internal/msgqueue/queue.go:243-256` — `Snapshot(convID)`: the single-conversation read to mirror —
  fresh `make([]QueuedMessage, len)` value-copy under `q.mu`, includes the in-flight head. `SnapshotAll`
  is this over every non-empty conversation.
- `internal/msgqueue/queue.go:132-156` — `convQueue` / `Queue` internals: `convs map[string]*convQueue`
  guarded by `q.mu`; the drain and `Enqueue`/`Remove` all mutate `c.items` under `q.mu`.
- `internal/msgqueue/queue.go:407-426` — `advanceLocked` / `shrinkLocked`: a fully-drained conversation
  keeps its `convQueue` entry in `convs` but with `items == nil`. **This is why `SnapshotAll` must skip
  `len(c.items) == 0`** — that filter is AC3's deterministic enforcement point.
- `cmd/pyry/queue_state_v2.go:166-185` — `toQueueStatePayload(convID, items)`: the **existing producer
  mapping to reuse** (`QueuedMessage{ID,Text,TS}` → `QueuedItem{QueuedMsgID,Text,TS}`, FIFO order,
  `Queued` initialised non-nil). The `outstandingQueues` adapter composes `SnapshotAll` with this.
- `cmd/pyry/queue_state_v2.go:22-46` — `queueStateEmitterV2` doc + the `SECURITY:` block: queued `text`
  is opaque, phone-originated transit, **never logged**. The reconcile inherits this discipline verbatim.
- `cmd/pyry/relay.go:445, 505, 526` — the `NewV2SessionManager(relay.V2SessionConfig{…})` literal;
  `OutstandingModals: modalReg.Snapshot` (the sibling wiring line the new field sits beside); and
  `QueueRemover: queue` — **proof `queue` (`*msgqueue.Queue`) is already in scope at this literal**, so
  no `startRelay`/`startRelayV2` signature change is needed.
- `internal/protocol/messaging.go:185-204` — `QueuedItem` / `QueueStatePayload`: the reused wire
  vocabulary (**no new payload type**). `internal/protocol/codes.go:310` — `TypeQueueState =
  "queue_state"`.
- `internal/relay/v2session_modalreconcile_test.go` (whole file) — **the test harness to clone.**
  `startManager` + `V2SessionConfig{…}`, `openModalConn`, `waitForEnvelopes`, `noiseMsgsForConn`,
  `decryptAppFrame`, `v2Recorder`. `reconciledModals` (`:39-57`) is the decrypt-and-key-by-id helper to
  mirror as `reconciledQueues` (key by `conversation_id`).

## Context

`queue_state` is pushed only on change (#722): the `queueStateEmitterV2` fans a snapshot to every open
interactive conn whenever a conversation's backlog changes (enqueue, drain-advance, remove). A client
that connects or reconnects *between* changes never learns the current backlog — it sees an empty or
stale view. #829 (ADR 025) names this as the identical gap `modal_shown` had, with the same root cause
and the same fix shape.

This ships the **queue half of #829** via the *enumerate-current-truth* shape #877 established: on a v2
session reaching `V2StateOpen` with the `interactive` capability, re-send the current `queue_state` for
each non-empty conversation **only to that connection**. `queue_state` is already snapshot-shaped (full
per-conversation list; `queued: []` clears), so re-sending current state is idempotent by construction —
there is no stale-replay risk and no special-casing of how long the client was away. The client-side
contract (filed alongside) is reset-on-reconnect, so an absent snapshot after the handshake means an
empty backlog. It reuses the existing `queue_state` wire vocabulary and the `toQueueStatePayload`
producer mapping; **no new payload type**, so mobile consumes the same behaviour later with no daemon
change.

## Design

Three additive changes across three packages, plus one wiring line. No existing signature changes.

### 1. New pure-read enumeration seam on `msgqueue.Queue`

`Snapshot(convID)` reads one named conversation; `convs` is private, so enumerating the conversations
that hold a non-empty backlog needs a new read method.

```go
// SnapshotAll returns every conversation's not-yet-delivered backlog keyed by
// conversation id, omitting conversations whose backlog is empty. Each value is
// a freshly allocated, value-copy slice (identical semantics to Snapshot), so a
// caller cannot mutate engine state through it. A pure read: it mints no id and
// dequeues nothing. The empty map means no conversation has a backlog.
func (q *Queue) SnapshotAll() map[string][]QueuedMessage
```

Behaviour (each line a `Snapshot` mirror):

- Takes `q.mu` for the whole read (one consistent instant across conversations — no TOCTOU between
  enumerate and per-conversation read).
- Iterates `convs`; **skips `len(c.items) == 0`** (a drained-but-retained `convQueue` has `items ==
  nil`; see `shrinkLocked`). This filter is AC3: an empty conversation contributes no map entry, hence
  no `queue_state`.
- For each non-empty conversation, builds a fresh `[]QueuedMessage` exactly as `Snapshot` does
  (value-copy of `id`/`text`/`ts`, FIFO order, including the in-flight head).
- Returns a fresh `map[string][]QueuedMessage` (never nil-mapped into shared state). Invariant asserted
  by test: every returned slice has `len >= 1`.

Map return (not `[]struct{ConvID; Messages}`) keeps the seam to existing types — consistent with #722's
"no intermediate internal QueueState model." Map iteration order is irrelevant: each `queue_state` is
self-identifying by `conversation_id`, and the reconcile sends one per entry.

### 2. New optional seam on `V2SessionConfig` (sibling of `OutstandingModals`)

```go
// OutstandingQueues enumerates the daemon's current per-conversation queued
// backlogs as marshal-ready queue_state payloads (one per non-empty
// conversation) for connect-time reconcile (#878). Called on the Run goroutine
// from handleNoiseInit's interactive-open tail; unicast to the just-opened conn
// only. A pure read: it mints no id and dequeues nothing.
// Optional: nil ⇒ no reconcile (byte-identical to the pre-#878 / foreground /
// existing-test posture). Production wires the cmd/pyry outstandingQueues adapter.
OutstandingQueues func() []protocol.QueueStatePayload
```

Same rationale as `OutstandingModals`: a closure returning `[]protocol.QueueStatePayload` crosses the
boundary with no new import (`internal/relay` imports `protocol`, not `msgqueue`) and no cycle; nil is a
legal disabled state, not validated in `NewV2SessionManager`.

### 3. `reconcileQueues` helper in `internal/relay` (Run-goroutine only)

```go
// reconcileQueues unicasts the current per-conversation queue_state set to a
// freshly interactive-open conn (#878). Structural twin of reconcileModals: same
// guard, same marshal-and-Push loop, TypeQueueState instead of TypeModalShown.
// Run-goroutine only, so s.interactive / s.connID are read lock-free.
func (m *V2SessionManager) reconcileQueues(ctx context.Context, s *V2Session)
```

Behaviour — identical control flow to `reconcileModals` (v2session.go:2195-2242), substituting the queue
type. Each bullet maps to an AC and to the `reconcileModals` line it mirrors:

- **Guard:** `if !s.interactive || m.cfg.OutstandingQueues == nil { return }` — capability gate (AC4) +
  unwired/foreground opt-out.
- **Snapshot once:** `outstanding := m.cfg.OutstandingQueues()`; `len == 0 ⇒ return` (AC3 — the seam
  already omits empty conversations, so an empty slice means nothing pending).
- **One shared `ts := time.Now().UTC()`** for the batch.
- **Per payload:** `json.Marshal(p)`; on the defensive marshal-error branch, content-free `Warn`
  (`event`, `conn_id`, `conversation_id` — a non-secret routing id; **never** `text`/payload/`err`) and
  `continue`. Else `m.Push(ctx, s.connID, protocol.Envelope{ID: 1, Type: protocol.TypeQueueState, TS:
  ts, Payload: payloadJSON})`.
  - `ID: 1` — **non-load-bearing**; the phone correlates `queue_state` on `conversation_id` +
    `queued_msg_id`, not envelope order (#722 security review). Fixed `ID` keeps the reconcile entirely
    relay-side and off the `cmd/pyry` emitter's producer-goroutine-owned `nextID` — no cross-goroutine
    coupling, exactly as `reconcileModals` avoids the modal emitter's `nextID`.
  - `EventID` left nil — a control event, never in the turn-event replay ring; also a never-drop control
    frame (`pushQueue.enqueue` evicts only `TypeAssistantDelta`).
- **Push error:** `Debug`-log the transport sentinel + `conn_id` (never payload bytes); on `ctx.Err() !=
  nil` return early (teardown), else `continue`.

### 4. Call site in `handleNoiseInit`

One line immediately after `m.reconcileModals(ctx, s)` (v2session.go:1434), before the `#647` replay
block:

```go
m.reconcileModals(ctx, s)
m.reconcileQueues(ctx, s)
if helloPayload.LastEventID != nil { … replayMissed … }
```

Same placement guarantees as `reconcileModals`: after `s.interactive`/`V2StateOpen` are set (guard and
seal-time gate pass) and after `m.queues[s.connID]` is created (so `Push` finds the queue, never
`ErrConnNotFound`). Ordering relative to `reconcileModals` and `replayMissed` is immaterial to
correctness (distinct payload types; `queue_state` sits in `m.queues` behind any `#777` replay tail and
drains after). Modal-then-queue documents intent: surface the time-sensitive permission prompt ahead of
the backlog.

### 5. `cmd/pyry` wiring — `outstandingQueues` adapter + one field

The relay seam wants `[]protocol.QueueStatePayload`; the queue produces `map[string][]QueuedMessage`.
`toQueueStatePayload` (the existing #722 mapping) bridges one entry. Add a small adapter beside it in
`cmd/pyry/queue_state_v2.go`:

```go
// outstandingQueues adapts msgqueue.SnapshotAll to the relay reconcile seam:
// one marshal-ready QueueStatePayload per non-empty conversation, reusing the
// #722 toQueueStatePayload mapping. Pure read; text stays opaque transit.
func outstandingQueues(queue *msgqueue.Queue) func() []protocol.QueueStatePayload
```

Body: `queue.SnapshotAll()` → `make([]protocol.QueueStatePayload, 0, len(backlogs))` → append
`toQueueStatePayload(convID, items)` per entry → return. Every entry is non-empty (AC3 enforced upstream
in `SnapshotAll`), so every payload carries `len(Queued) >= 1`.

Then in the `NewV2SessionManager(relay.V2SessionConfig{…})` literal (cmd/pyry/relay.go:445), beside
`OutstandingModals`:

```go
OutstandingQueues: outstandingQueues(queue),
```

`queue` is already the `startRelayV2` parameter wired to `QueueRemover: queue` (relay.go:526) and
`handlers.SendMessage(…, queue, …)` — the same live daemon queue the #722 producer snapshots on change.
No new threading through `startRelay`/`startRelayV2`.

## Concurrency model

- `reconcileQueues` runs on the single Run dispatch goroutine (called synchronously from
  `handleNoiseInit`), so `s.interactive`/`s.connID`/`m.cfg` are read lock-free under the package's
  single-owner invariant — identical to `reconcileModals`, `broadcastModalDismissed`, `handleInterrupt`.
- `m.Push` takes the leaf `pushMu` and signals `drainCh`; never blocks, documented safe from the Run
  goroutine. Seal-and-forward happens on a later Run pass in `drainOnce`/`forwardEnvelope`, one frame at
  a time — the `#874` transport-down HOLD applies unchanged.
- `SnapshotAll` (called via `outstandingQueues` **from the relay Run goroutine**, i.e. off any msgqueue
  drain) takes and releases `q.mu` — the same leaf lock `Snapshot`/`Enqueue`/`Remove`/the drain use.
  msgqueue never calls back into relay while holding `q.mu` (`notify` fires post-unlock), so no lock
  nesting hazard: relay → `q.mu` → return.
- **Snapshot→deliver race (benign, self-healing).** A conversation may enqueue/drain/empty in the window
  between `SnapshotAll()` reading it and the phone receiving the re-send. The phone then holds a slightly
  stale full-state snapshot; because `queue_state` is idempotent full-state, the next #722 change
  re-emits current truth to this now-open conn, and the reset-on-reconnect client contract makes an
  absent later snapshot mean empty. No ordering inversion, no lost clear.

## Error handling

- **Marshal failure:** defensive only (`QueueStatePayload` is a closed struct of strings/ints/time).
  Content-free `Warn` + skip that one payload; the rest still send. **Never** echo `text`, the payload
  bytes, or `err.Error()` (encoding/json can quote the untrusted `text` into its error).
- **`Push` failure:** `ErrConnNotFound` unreachable (queue created two statements earlier, same
  goroutine); `ctx.Err()` (teardown) returns early; any other → `Debug` (transport sentinel + `conn_id`)
  + continue. Never a panic, never a body byte in a log.
- **Empty / nil:** nil seam or empty `SnapshotAll` ⇒ zero sends (AC3), no error.
- **Nothing minted:** the reconcile calls neither `Enqueue`, `Remove`, nor any id-minting path — the
  backlog and its `queued_msg_id` space are untouched.

## Testing strategy

Two test surfaces, both cloning existing precedents.

**`internal/msgqueue/queue_test.go` — `SnapshotAll` (table-driven, stdlib):**

- Two conversations each with a backlog → map has both keys, each slice matches its FIFO (order, `id`,
  `text`, `ts` via `time.Time.Equal`).
- A conversation drained to empty (Enqueue then deliver-through a fake `DeliverFunc`) → **omitted** from
  the map (AC3 at the seam). Pair a non-empty conversation so the map isn't trivially empty.
- No conversations / fresh queue → empty (non-nil) map.
- In-flight head is included (mirror `Snapshot`'s head-included assertion).
- Returned slices are value copies — mutating a returned slice/element does not change a subsequent
  `Snapshot`/`SnapshotAll`.
- `-race`: `SnapshotAll` concurrent with `Enqueue`/drain on other conversations (reuses the queue's
  existing race-test shape).

**`internal/relay/v2session_queuereconcile_test.go` — clone `v2session_modalreconcile_test.go`:** reuse
`startManager`, `openModalConn`, `waitForEnvelopes`, `noiseMsgsForConn`, `decryptAppFrame`, `v2Recorder`;
add a `sampleQueuePayload(convID, ids…)` fixture and a `reconciledQueues` decrypt-and-key-by-`conversation_id`
helper mirroring `reconciledModals`. Wire `OutstandingQueues` to a test closure. Scenarios (bullets;
developer writes them in the package idiom):

- **Interactive open, one non-empty conversation → unicast one `queue_state` (AC1).** Assert the frame is
  `TypeQueueState`, its `conversation_id` and `queued` items (order, `queued_msg_id`, `text`, `ts`) match.
- **Two non-empty conversations → two `queue_state`, keyed by `conversation_id`, order-independent (AC1).**
  Build a `map[conversation_id]payload` from the decrypted frames.
- **Unicast — only the opening conn (AC2).** Conn A already open (interactive), then open conn B
  (interactive); B receives the re-send, A receives **no** new `queue_state`.
- **Non-interactive open → nothing (AC4).** Open a conn advertising no `interactive` capability; assert
  zero `queue_state`.
- **Empty backlog → nothing for that conversation (AC3).** Seam returns `[]` (the adapter's real
  behaviour when all conversations are empty); assert no `queue_state`. (`SnapshotAll`'s empty-skip is
  unit-tested at the msgqueue layer above.)
- **nil seam → inert (foreground/unwired byte-stability).** `OutstandingQueues` nil; open interactive
  conn; assert no `queue_state`, session stays `V2StateOpen`.
- **Content-free logging (AC5).** Drive the marshal-error / push-drop branch (or assert by construction)
  and confirm no `text` substring appears in any emitted log record — reuse the log-capture pattern the
  sibling emitters test against.

**`cmd/pyry/queue_state_v2_test.go` — `outstandingQueues` adapter:** over a real `msgqueue.New` + a couple
of `Enqueue`s across two conversations (one drained empty), assert the closure returns one
`QueueStatePayload` per non-empty conversation with `conversation_id` + mapped items, and none for the
empty one. Reuses the file's existing `toQueueStatePayload` tests.

Run `go test -race ./internal/msgqueue/... ./internal/relay/... ./cmd/pyry/...` and `go vet ./...`.

## Open questions

- **Delivery ordering vs `#777` replay for a reconnecting client advertising `last_event_id`:** the
  reconcile `queue_state` is held in `m.queues` behind the replay tail and drains after it (same as the
  #877 modal reconcile). Acceptable (replayed history first, then current backlog); jumping the tail is a
  future follow-up, out of scope.
- **#829 disposition:** with both halves (#877 modal, #878 queue) landed, the operator decides rescope
  vs supersede on the #877 umbrella. No daemon change here either way.

## Security review

**Verdict:** PASS

This ticket carries `security-sensitive`, so this adversarial pass over the spec is mandatory. The active
crux is **[Trust boundaries]** joined by **[Error messages, logs, telemetry]**: the reconcile dispatches
confidential, phone-originated queued `text` over the internet-exposed v2 relay to a possibly-untrusted
peer. This is the exact audit surface #722 (the on-change `queue_state` producer) and #877 (the modal
reconcile twin) carried the label for — the wire producer of a current-truth read earns it. Each finding
cites a real anchor; default-FAIL walked to PASS across every applicable category.

**Findings:**

- **[Trust boundaries] No MUST-FIX.** The untrusted datum is the queued `text` (phone-originated via
  `send_message` #721). It flows `SnapshotAll → toQueueStatePayload → QueueStatePayload.Text → Push` as
  **opaque transit** — never parsed, compared, or used in any routing/gating decision, identical to the
  #722 producer path. Three deterministic, reused gates keep the re-send from reaching an unauthorized
  peer:
  - *Authentication.* `reconcileQueues` is called only from `handleNoiseInit`'s success tail — after
    Noise_IK completed and the device token validated (v2session.go:1370-1408); a token-rejected peer
    closes at 4401 and never reaches this code. `m.Push → drainOnce → forwardEnvelope` then **re-checks
    `s.state == V2StateOpen`** at seal time, so a conn torn down between enqueue and drain is dropped
    there, never delivered.
  - *Capability.* The `!s.interactive` guard (AC4) means a conn that handshook without the `interactive`
    capability receives nothing. `s.interactive` is set from the same negotiated slice the ack echoed
    (v2session.go:1403-1407), so a spoofed advertisement can never flag the session. Any paired
    interactive device within a server-id is one trust domain (ADR 025 § Security model), consistent with
    the raise-time #722 fan-out — this reconcile is *tighter* (unicast, not broadcast).
  - *Conversation scoping / no desync.* Each payload's `conversation_id` and its items come from a single
    map entry in `SnapshotAll` (`convID` is the key, items are that key's slice) threaded through
    `toQueueStatePayload` — a payload cannot carry conversation A's id with conversation B's text. An
    arbitrary/hostile `convID` never enters this path: the producer enumerates the daemon's own map keys,
    it does not accept a caller-supplied id here.
- **[Tokens, secrets, credentials] N/A — and fail-safe by construction.** `queued_msg_id` is a
  non-secret per-conversation counter (ADR 025: queue ops are ungated for any paired phone; no nonce).
  The reconcile **mints nothing**: `SnapshotAll` is a pure read that never touches `nextID`/`Enqueue`, so
  no id enters the wire and the `queued_msg_id` space is unchanged. `queue_state` carries no
  `answer_token` or capability value. `EventID` is nil. Re-sending the same snapshot grants no new
  capability — it is idempotent full-state.
- **[File operations] / [Subprocess execution] N/A.** The change touches neither the filesystem nor any
  subprocess. `SnapshotAll` reads the in-memory `msgqueue`; the reconcile enqueues an envelope. No path
  is constructed from any input.
- **[Cryptographic primitives] N/A.** No crypto added. The payload is sealed under the session's existing
  AEAD `CipherState` in `forwardEnvelope`, unchanged; the frame is enqueued unsealed and sealed in FIFO
  by the single drain, so no send-nonce is burned out of order and the `#874` transport-down HOLD still
  applies. `ID: 1` is a non-secret, non-load-bearing counter, not a token.
- **[Network & I/O, DoS] No MUST-FIX.** The re-send count is bounded by the number of non-empty
  conversations, each capped at `MaxQueuedPerConversation` (default 100, `queue.go:72`) not-yet-delivered
  messages — the same bound the #722 producer and `Enqueue`'s reject-past-cap already enforce. Each
  message's size is capped at frame-decode upstream. It is a single unicast per non-empty conversation to
  one conn on connect — no amplification, no per-connect unbounded work, no new socket/HTTP/TLS surface.
  `queue_state` is a never-drop control envelope; a push buffer that cannot hold it drops it (bounded),
  never accumulates.
- **[Error messages, logs, telemetry] No MUST-FIX — the co-crux.** The queued `text` MUST NOT be logged
  at any level. The helper logs only content-free discriminants — `event`, `conn_id`, `conversation_id`
  (a non-secret routing id) — on the marshal-error and Push-drop branches; **never** `text`, the payload
  bytes, or a raw `err` that could quote payload bytes (the marshal branch drops `err` entirely, matching
  `reconcileModals` and the #722 emitter's `SECURITY:` discipline). A test asserts no `text` substring
  appears in the reject/drop log record.
- **[Concurrency] No finding.** No new goroutine, no new shared state, no new lock. `reconcileQueues`
  runs on the relay Run goroutine and reuses `pushMu` (leaf) transitively via `m.Push`; `SnapshotAll`
  reuses `q.mu` (leaf) and msgqueue fires `notify` only after releasing `q.mu`, so the relay→`q.mu` read
  cannot deadlock a drain. The Snapshot→deliver race is benign and self-healing (idempotent full-state;
  next #722 change re-emits current truth). No nonce reused; no CipherState read off-Run.
- **[Threat model alignment] In scope and addressed:** leaking a backlog to an un-authenticated peer
  (blocked by the 4401 handshake + seal-time `V2StateOpen` re-check), a non-interactive peer (blocked by
  the `s.interactive` guard, AC4), or the wrong conn (prevented by unicast to `s.connID`); minting an
  id/nonce on reconnect (structurally impossible — `SnapshotAll` mints nothing); `text` in logs
  (content-free logging, AC5). **Out of scope, named:** per-connection conversation isolation — every
  interactive conn on this server-id may receive every conversation's `queue_state`, exactly as the #722
  producer fans it and consistent with ADR 025's ungated-for-any-paired-phone model; AC1's confidentiality
  boundary within reach is per-payload conversation scoping, which the design enforces. Mobile
  match-and-replace behaviour is a separate client contract (no daemon change). #829's disposition is the
  operator's call on the #877 umbrella.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-10
