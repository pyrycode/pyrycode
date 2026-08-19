# Spec #1505 — Bound control-class push-queue growth with a retained-bytes ceiling

**Ticket:** [#1505](https://github.com/pyrycode/pyrycode/issues/1505) · size `s` · `security-sensitive`
**One-sentence test:** *Tear a v2 session down once its push queue's retained control-class bytes exceed a hard ceiling.*

---

## Files to read first

Read these before touching anything. Symbols, not line numbers — resolve each with
`codegraph_node <symbol>` / `codegraph_search <symbol>`.

| File | Symbols | What to extract |
|---|---|---|
| `internal/relay/v2session_modal.go` | `pushQueue`, `queuedEnv`, `pushQueue.enqueue`, `pushQueueCap` | The three append sites and the one evict site. This is where the byte counter is maintained and where the ceiling check goes. The `enqueue` doc comment's drop-policy list is the contract you extend. |
| `internal/relay/v2session.go` | `V2SessionManager.Push`, `drainOnce`, `transportDown`, `closeWith`, `forwardEnvelope` | `Push`'s `pushMu` hold (where the latch is read), `drainOnce`'s pop (which becomes `popHead`), `closeWith`'s queue delete (what frees the bytes), and `transportDown`'s hold (why nothing drains). |
| `internal/relay/v2session.go` | `V2SessionManager.Run`, `handleWake`, `armIdleTimer`, `idleTimeout`, `StatusIdleTimeout` | The Run select-arm shape you add one arm to; `handleWake`'s `wakeIdleTimeout` case is the teardown precedent (`closeWith(ctx, s, StatusIdleTimeout, nil)` — note the **nil frame**). |
| `internal/relay/v2session.go` | `V2SessionManager` struct's `modalTimeout` field + its `make(chan string, wakeBufferSize)` in the constructor | The exact shape to mirror for the new off-Run→Run connID channel. Do not invent a new one. |
| `internal/relay/v2bundlestream.go` | `bundleChunkBytes`, `bundleEnvelopes`, `StreamBundle` | The chunk arithmetic (48000 raw → ~64200 B envelope payload) that AC#5's two fixtures are sized from, and the fact that `StreamBundle` returns the *first* `Push` error. |
| `internal/relay/v2session_debugbundle.go` | `bundleInFlight`, `handleDebugBundleRequest` | The #911 gate — read-only over `q.items`. Confirm you do not disturb it (it reads `items`, never `bytes`). |
| `internal/relay/v2session_test.go` | `TestPushQueue_Enqueue_AllControlSoftOverflow`, `pqEnv`, `assertQueue` | **AC#4's regression pin.** `pqEnv` builds envelopes with a **zero-length `Payload`** and the test binds `enqueue`'s return with `dropped := q.enqueue(...)` — both facts constrain the design (see § Design). |
| `internal/relay/v2session_test.go` | `gatedRecorder`, `newGatedRecorder`, `driveToOpen`, `queueLen`, `TestV2Session_Push_HoldGatedOnProbeNotSendError` | The #874 fixtures. The last one is the **independent probe/send wiring** (`Connected: probeUp.Load` + an `Outbound` that records unconditionally) that AC#2's test needs — plain `gatedRecorder` records *nothing* while down, so it cannot observe the close frame. |
| `internal/relay/v2session_debugbundle_test.go` | `bundleGatedManagerFor`, `waitQueueLen`, `requestBundle`, `fakeBundler`, `decryptFrames` | AC#5's fixture is already built: `bundleGatedManagerFor` returns a decoupled `probeUp` + unconditionally-recording `rec`. |
| `docs/protocol-mobile.md` | § Error codes (the close-code table + the per-code prose under it); § Application-envelope size cap | One new table row + one prose paragraph, in `4408`'s style. The 65519-byte cap is the number the ceiling is derived from. |
| `docs/knowledge/decisions/025-*.md` | § Backpressure (and its 2026-07-10 amendment) | The never-drop-control rule the ceiling is an exception to, and why. |

---

## Context

`pushQueue.enqueue`'s last branch — the "all-control saturated, incoming control" soft
overflow — appends with **no ceiling of any kind**. Every other branch is bounded by
`pushQueueCap` (256); this one is the single escape.

It is reachable in production two ways:

1. **The #874 transport-down hold + a live turn.** `drainOnce` returns at its first
   statement while `transportDown()` is true, so nothing drains. The #632 emitter keeps
   pushing `tool_result` / `tool_use` / `turn_state` / `turn_end` — all control-class, all
   never-droppable. A 10-minute outage mid-turn grows the queue to thousands of envelopes,
   released only when the 15-minute idle sweep fires.
2. **One large debug bundle.** `debugbundle.Assemble` puts the newest `.cast` recording
   into the archive whole with no size cap; `StreamBundle` turns it into
   `ceil(len/48000) + 1` never-droppable control envelopes in a single handler call.

The existing `enqueue` doc comment names only (2), and credits #911's in-flight gate for
bounding it. That gate rejects a *repeat* `request_debug_bundle` while a prior bundle is
queued — it says nothing about (1), where there is no request to reject.

**No live repro exists.** Growth is real but modest — thousands of envelopes / single-digit
MB in the scenario above. This is hardening of an unbounded axis, not a fix for an observed
OOM. The response is a backstop well above the nominal cap, not a new policy layer.

### Why teardown, and not "collapse older control events"

The rejected direction is worth stating once so it is not rediscovered. On a transport blip
that *recovers*, the same v2 session resumes: `reconcileModals` / `reconcileQueues` run only
on `handleNoiseInit`'s success tail, and #647 replay runs only from a fresh
`hello.last_event_id`. Neither fires on transport recovery alone. **A control envelope
discarded under a surviving session is a permanent silent gap with nothing to reconcile it.**

A teardown is the opposite: it forces a re-handshake, which runs both reconcilers *and*
#647 replay over the missed tail. That is what makes discarding-at-the-ceiling safe — the
discard and the teardown are inseparable. This has a direct consequence for the design
(§ Concurrency → "the latch is never cleared").

---

## Design

Four pieces, in `internal/relay/v2session_modal.go` and `internal/relay/v2session.go`.
No new files, no signature changes, no call-site cascade.

### 1. Byte accounting inside `pushQueue`

`pushQueue` gains two fields:

- `bytes int` — the sum of retained payload bytes over `items`. **Invariant:
  `q.bytes == Σ len(item.env.Payload)` at every point where `pushMu` is not held**, and
  `q.bytes <= pushQueueByteCeiling` always.
- `overflowed bool` — a one-way latch, set when the ceiling rejects an envelope. Never
  cleared (§ Concurrency).

**The measure is `len(env.Payload)` and nothing else**, and the doc comment must say so.
The fixed per-envelope fields (`Type`, `ID`, `TS`, `EventID`) and the `queuedEnv` slot are
deliberately not charged: they are O(10²) B against a payload that spans ~100 B to 64 KB,
and AC#1's second case explicitly requires that a large *count* of small control envelopes
be admitted. The residual — a producer emitting zero-payload control envelopes would grow
the queue in count without moving `bytes` — is a known, undefended axis; see § Security
review [Network & I/O].

All mutation of `bytes` lives inside `pushQueue` methods. Two methods touch it:

- `enqueue` (unchanged signature — see below), maintaining `bytes` at its three append
  sites and its one `slices.Delete` evict site.
- A new `popHead() protocol.Envelope` — pops the FIFO head, decrements `bytes`, zeroes the
  vacated slot for GC. `drainOnce`'s three-line hand-rolled pop is replaced by a call to it,
  so no caller outside the type ever maintains the counter.

`bundleInFlight` reads `q.items` only and is untouched.

### 2. The ceiling, and the one branch it can trip on

```go
// pushQueueByteCeiling bounds the retained PAYLOAD bytes (Σ len(env.Payload))
// one session's push queue may hold. Reachable only from enqueue's
// soft-overflow branch — see the derivation below.
const pushQueueByteCeiling = 32 << 20 // 32 MiB
```

**Derivation (put it in the doc comment).** A queue within `pushQueueCap` can already
retain at most `256 × 65519 = 16 772 864` B ≈ 16 MiB, because the v2 application-envelope
cap is 65519 bytes (`docs/protocol-mobile.md` § Application-envelope size cap). 32 MiB is
~2× that, which buys two properties:

- **The ceiling can never trip on a queue within the nominal count cap.** `enqueue`'s
  first two branches leave `len(items) <= pushQueueCap`, so they cannot reach 32 MiB.
  AC#4's "behaviour below the ceiling is unchanged" is therefore *structural*, not
  empirical — and the ~2× margin survives a future bump of `pushQueueCap` or a change to
  the envelope cap.
- **~4× above the failure scenario's single-digit MB**, so no realistic transport outage
  trips it.

Consequently **the ceiling check goes in exactly one place: the soft-overflow branch**.
Do not add it to the other two — it would be dead code there, and the reader would lose the
proof above.

The branch becomes: if admitting `env` would push `bytes` past the ceiling, set
`q.overflowed`, **do not append**, and return `false`. Otherwise append and add. `>` not
`>=`: an envelope landing exactly on the ceiling is admitted, so the invariant is
`q.bytes <= pushQueueByteCeiling`.

**`enqueue`'s signature does not change.** It still returns a single `bool` meaning "the
drop policy dropped or evicted something". Two reasons, both binding:

- `TestPushQueue_Enqueue_AllControlSoftOverflow` writes `dropped := q.enqueue(...)`; a
  two-value return is a compile error there, and AC#4 requires that test to pass
  **unmodified**.
- A ceiling rejection is not a drop-policy drop. It must not increment `q.dropped` and
  must not emit `v2.push.drop`, or the two conditions become indistinguishable in the logs.

The ceiling's signal is the `q.overflowed` field, which `Push` reads under the same
`pushMu` hold.

### 3. Off-Run → Run teardown signal

`enqueue` runs off the Run goroutine under `pushMu`; `closeWith` is Run-owned. The bridge
mirrors `modalTimeout` exactly — a `chan string` of connIDs plus one Run select arm:

- `V2SessionManager` gains `pushOverflow chan string`, constructed as
  `make(chan string, wakeBufferSize)`.
- `Push`, inside its existing `pushMu` hold, captures both the level and the edge:

  ```go
  before := q.overflowed
  dropped := q.enqueue(env)
  overflowed, justTripped := q.overflowed, q.overflowed && !before
  bytes := q.bytes
  ```

  After unlocking, in this order: the existing `drainCh` non-blocking signal (unchanged);
  then, if `overflowed`, a **non-blocking** send of `connID` on `m.pushOverflow`
  (`select` + `default`, the `drainCh` idiom); then, if `justTripped`, one `Warn` record;
  then the existing `dropped` `Debug` record. Return `nil`.

  Level-triggered signal, edge-triggered log: the latch stays set, so every subsequent
  `Push` re-drives a signal that a full channel dropped, while the operator sees exactly
  one Warn per session.

  **Non-blocking, not the blocking send `armIdleTimer` uses.** `armIdleTimer` blocks on a
  fresh `time.AfterFunc` goroutine; `Push` runs on the producer's goroutine, where blocking
  would break the "never blocks the producer" contract this whole queue exists to uphold
  (AC#3). The level-triggered re-drive plus the pre-existing idle sweep close the
  dropped-signal hole.

- `Run` gains one arm, `case connID := <-m.pushOverflow: m.handlePushOverflow(runCtx, connID)`.

- `handlePushOverflow(ctx context.Context, connID string)` — Run-goroutine only. Looks up
  `m.sessions[connID]`; returns if absent (already torn down — its queue went with it) or
  if `s.state != V2StateOpen`. Otherwise emits one `Warn` and calls
  `m.closeWith(ctx, s, StatusQueueOverflow, nil)`, which deletes the session, deletes the
  queue under `pushMu` (freeing every retained byte), and stops the per-session timers.

  **It must NOT re-check whether the queue has since drained below the ceiling.** By the
  time the latch is set, control envelopes have already been discarded; only the teardown
  makes them recoverable (§ Context). A "kinder" re-check would leave a surviving session
  with a permanent silent gap — the exact outcome the ticket rejects.

### 4. Close code

```go
// StatusQueueOverflow is the WS close code the binary asks the relay to apply
// when a v2 session's push queue exceeds pushQueueByteCeiling. Echoes HTTP 413
// (Content Too Large), consistent with the 44xx←HTTP convention.
StatusQueueOverflow websocket.StatusCode = 4413
```

Added to the existing close-code `const` block in `v2session.go` (4413 is free; the repo
uses 4401, 4408, 4409, 4421, 4426, 4429). Deliberately **not** `StatusIdleTimeout` — reusing
4408 would mislabel an overflow as an idle session, the same class of mistake #912
documented when a transport outage surfaced as `noise.rekey_failed`.

**The frame is `nil` — no sealed error envelope.** Three reasons, all worth one line in the
doc comment: the trip condition is a parked drain, which in the #874 case means the
transport is down, so sealing burns a Noise send-nonce for a frame that cannot arrive and
gaps the phone's recv nonce (the #912 hazard); the idle sweep already sets this precedent
(`closeWith(ctx, s, StatusIdleTimeout, nil)`); and the close code alone is sufficient,
because the phone's recovery path is a re-handshake, not a message.

Add one row to `docs/protocol-mobile.md` § Error codes and one prose paragraph beneath it in
`4408`'s style: what trips it, that it is per-session, that the phone's recovery is a fresh
Noise handshake, and that a phone whose transport was down never sees the close frame (below).

---

## Data flow

```
producer goroutine                     Run goroutine
------------------                     -------------
Push(ctx, connID, env)
  pushMu.Lock()
  q.enqueue(env) ──► soft-overflow branch
                       bytes+len(Payload) > ceiling?
                         yes → q.overflowed = true; DO NOT append
                         no  → append; bytes += len(Payload)
  read overflowed / justTripped / bytes
  pushMu.Unlock()
  drainCh  <- {}    (non-blocking, unchanged) ──────►  drainOnce
  pushOverflow <- connID  (non-blocking, if latched) ─►  handlePushOverflow
  Warn "v2.push.ceiling" (once, on the edge)                │
  return nil                                                ▼
                                                     m.sessions[connID], state==Open?
                                                       Warn "v2.push.ceiling.teardown"
                                                       closeWith(ctx, s, 4413, nil)
                                                         ├ state = V2StateClosed
                                                         ├ stop timers, close s.done
                                                         ├ delete(m.sessions, connID)
                                                         ├ pushMu: delete(m.queues, connID)  ← bytes freed
                                                         └ m.send({CloseCode: 4413})  (best-effort)
```

Later pushes to that connID find no queue → the pre-existing `ErrConnNotFound`. No new
error value ever reaches a producer (AC#3).

### What the phone loses, and how it gets it back

Every control class that can be discarded at the ceiling has a recovery path, and the
teardown is what triggers all of them:

| Discarded class | Recovered by |
|---|---|
| `tool_result` / `tool_use` / `turn_state` / `turn_end` (#632) | #647 replay from `hello.last_event_id` — the emitter stamps `EventID` on these |
| `modal_shown` | `reconcileModals` (#877) on the re-handshake's success tail |
| `queue_state` | `reconcileQueues` (#878), same tail |
| `debug_bundle_chunk` / `debug_bundle_done` | Not replay-covered (`EventID` is nil by construction) — the operator re-requests the bundle after reconnect. See § Open questions. |

**The close frame itself is best-effort.** `closeWith` ends in `m.send`, which the down
transport drops with a debug log. A phone whose relay leg is down therefore never sees
`4413`; it learns on its next inbound frame, which lands on a session the daemon no longer
has and gets the existing not-open handling → re-handshake. This is not new: the idle sweep
(#774) has exactly this property, and it fires precisely when the phone is silent. Say so
in the protocol-doc paragraph rather than implying the code is always delivered.

---

## Concurrency model

No new goroutines. No new locks. No change to lock ordering.

- **`pushMu` stays a leaf lock**, taken alone and never held across an external call. The
  latch read, the byte read and `enqueue` all happen inside the *existing* hold in `Push`;
  the channel send and both log calls happen after the unlock.
- **The teardown is Run-owned.** `enqueue` cannot close a session — it only sets a field.
  `handlePushOverflow` runs on the Run goroutine, so its `m.sessions` read and its
  `closeWith` call sit under the package's single-owner invariant, exactly like
  `handleWake`'s `wakeIdleTimeout` case.
- **The latch is never cleared, and that is deliberate.** Run's select is unordered, so
  several `drainOnce` passes can run between the signal and the `pushOverflow` arm; if the
  transport recovered in that window the queue may be under the ceiling by the time the arm
  fires. The session is torn down anyway. Envelopes were already discarded when the latch
  tripped, and only the re-handshake replays them (§ Context). The cost is one extra
  handshake in a rare race; the alternative is a silent gap.
- **Dropped signals are re-driven.** A full `pushOverflow` buffer drops the send; the latch
  stays set, so the next `Push` signals again. With no further pushes the queue is frozen at
  ≤ ceiling and the 15-minute idle sweep reaps it — the pre-existing bound, strictly no
  worse than today.
- **Shutdown.** `closeWith` already checks `ctx.Err()` before its send. A pending
  `pushOverflow` value is simply garbage-collected with the channel on Run exit; nothing
  blocks on it.

---

## Error handling

| Failure | Behaviour |
|---|---|
| Ceiling tripped | `Push` returns `nil`, promptly. The envelope is not appended. One `Warn` (`v2.push.ceiling`) on the edge; one `Warn` (`v2.push.ceiling.teardown`) from Run. |
| `pushOverflow` buffer full | Send dropped. Next `Push` re-signals (level-triggered). Idle sweep is the terminal backstop. |
| Session already gone when the arm fires | `handlePushOverflow` returns; the queue was deleted with the session. |
| Close frame undeliverable (transport down) | `m.send` debug-logs the drop. The phone learns on its next frame. Same as the idle sweep. |
| `StreamBundle` mid-stream teardown | Its remaining `Push` calls start returning `ErrConnNotFound`, and it returns that error. **Which push first sees it is racy — tests MUST NOT assert on `StreamBundle`'s return value** (§ Testing). |

**Log fields.** `v2.push.ceiling`: `event`, `conn_id`, `bytes`, `ceiling`, `type`
(`env.Type` only — the same discriminator `v2.push.drop` already carries).
`v2.push.ceiling.teardown`: `event`, `conn_id`, `close_code`. Payload bytes NEVER appear in
either, matching the package's no-content-in-logs discipline. Both are `Warn`, not `Debug`:
this is an operator-visible runtime event, unlike the routine per-envelope drop.

---

## Testing strategy

Scenarios, not code. Table-driven where the shape allows; stdlib `testing` only.

### Unit — `pushQueue`, no manager (`internal/relay/v2session_test.go`)

- **Both sides of the boundary (AC#1).** Build an all-control queue from envelopes of a
  known payload size, enqueued until one more would cross `pushQueueByteCeiling`. Assert the
  last admitted envelope lands (length grew by one, `bytes` rose by exactly its payload
  length, `bytes <= ceiling`, `overflowed` still false), then that the next is rejected
  (length unchanged, `bytes` unchanged, `overflowed` true, `dropped` unchanged, and the tail
  is still the previously-admitted envelope).
- **Bytes, not count (AC#1, second case).** Enqueue far more than `pushQueueCap` *small*
  control envelopes whose total stays under the ceiling. Assert every one is admitted,
  `overflowed` stays false, `dropped` stays 0. A count-only implementation fails this.
- **Accounting round-trip.** Enqueue a mix of control and delta envelopes with differing
  payload sizes, then `popHead` every one. Assert `bytes == 0` and `len(items) == 0`. This
  is the guard against a missed decrement, whose production symptom is a phantom-byte leak
  that eventually tears down a perfectly healthy long-lived session.
- **Eviction decrements.** At cap with a queued delta, enqueue a control envelope. Assert
  `bytes` fell by exactly the evicted delta's payload length and rose by the incoming one's.

### Integration — manager, drain parked (`internal/relay/v2session_test.go`)

- **Ceiling teardown (AC#2 + AC#3).** Use the **independent probe/send wiring** from
  `TestV2Session_Push_HoldGatedOnProbeNotSendError` — `Connected: probeUp.Load` plus an
  `Outbound` that records unconditionally. `driveToOpen` with `probeUp` true, then store
  false so `drainOnce` holds. Push control envelopes of a known payload size in a bounded
  loop until past the ceiling. Assert:
  - every `Push` returned `nil` (AC#3);
  - the session is gone from the manager (`ActiveConnIDs` no longer lists it) and has left
    `V2StateOpen`;
  - `queueLen(mgr, connID) == -1` (queue deleted);
  - a subsequent `Push` returns `ErrConnNotFound` — and is `errors.Is`-comparable to it, so
    no new error value reached the producer;
  - the recorder captured a `RoutingEnvelope` with `CloseCode == 4413` and a nil `Frame`.

  **Do not touch `idleTimeout`.** Plain `gatedRecorder` is the wrong fixture here: it
  records nothing while down, so the close frame would be invisible. Note in the test why.

  *Why this cannot pass via the idle sweep (AC#2):* `idleTimeout` is left at its production
  15 minutes and the test completes in well under a second, so the observed teardown is
  necessarily the ceiling's.

### Bundle path (`internal/relay/v2session_debugbundle_test.go`)

Both cases use `bundleGatedManagerFor` (already returns a decoupled `probeUp` and an
unconditionally-recording `rec`) with `probeUp` stored false after the handshake, plus
`fakeBundler` returning a synthetic blob. Size the blobs from `bundleChunkBytes`: a blob of
`n` bytes yields `ceil(n/48000)` chunk envelopes whose payloads total ≈ `n × 64200/48000`,
plus one small `done`. Compute the target blob size from the constants in the test — do not
hardcode a number that silently drifts if either constant moves.

- **Just under the ceiling streams to completion (AC#5).** A blob whose retained-byte total
  lands under `pushQueueByteCeiling`. Assert no teardown (session still open, queue present),
  `waitQueueLen` reaches the full chunk count, then store `probeUp` true, signal `reconnect`,
  and assert the stream drains and round-trips through `decryptFrames` + `ReassembleBundle`
  to the original blob.
- **Past the ceiling produces the ceiling's outcome (AC#5).** A larger blob. Assert the
  session is torn down with `CloseCode == 4413` and the queue is deleted. **Assert nothing
  about `StreamBundle`'s return value** — it returns the first `Push` error, and which push
  first observes the teardown depends on Run's scheduling.

### Must stay green, unmodified (AC#4)

`TestPushQueue_Enqueue_AllControlSoftOverflow`, every `TestV2Session_DebugBundle_*`,
`internal/relay/v2bundlestream_test.go`, and `internal/e2e/relay_v2_debug_bundle_test.go`.

Verified, not assumed: `pqEnv` builds envelopes with a zero-length `Payload`, so the 257
envelopes that test enqueues retain 0 bytes. `assembleFixture` `t.Fatalf`s if its archive
reaches `bundleChunkBytes` (48000), so every `TestV2Session_DebugBundle_*` fixture is a
single sub-48 KB chunk, and the largest blob anywhere in `internal/relay`'s tests is the
60 000-byte two-chunk one in `v2bundlestream_test.go` — ≈ 0.2 % of the ceiling.

**If any of these needs editing, the ceiling is at the wrong altitude — say so on the
ticket rather than editing the test.**

Gate: `make check`. The e2e suite is behind the `e2e` build tag — use the Makefile target's
flags, and read the `=== RUN` count, not the exit code.

---

## Out of scope

Named explicitly so nobody widens the slice: changing `pushQueueCap` (256); changing the
drop policy for `assistant_delta`; capping the debug-bundle archive in
`debugbundle.Assemble`; extending the #911 in-flight gate; making `pushQueueByteCeiling`
config-driven; and `docs/knowledge/codebase/1505.md` (the documentation phase writes it from
this spec plus the merged diff).

---

## Open questions

1. **A very large legitimate bundle now tears the conn down.** The ceiling trips at an
   archive of roughly 25 MB (`32 MiB × 48000/64200`). Before this ticket such a bundle was
   retained until drain or the idle sweep; now it ends the conn. The ticket calls this a
   defensible-but-chosen outcome, and AC#5's second case tests it. The residual question is
   operator experience: a re-request after re-handshake hits the same wall, so the operator
   sees a loop rather than an error. Mitigation available today is the loud `Warn` pair. If
   this bites, the fix belongs in `debugbundle.Assemble` (cap the `.cast` member), not here
   — file it against the bundle package.
2. **Zero-payload control envelopes are not charged.** No production producer emits one, so
   this is an undefended-but-named residual rather than a hole to plug now (Evidence-Based
   Fix Selection). If a future producer can emit them, the fix is a fixed per-envelope charge
   plus a ceiling raised to keep the "never trips within the count cap" derivation intact.
3. **Stale branch, not a blocker.** `origin/feature/449` touches `internal/relay/v2session.go`
   and `internal/relay/v2session_test.go`, but issue #449 closed 2026-05-17 and no PR was
   ever opened for that branch — it is abandoned and will never merge. No open PR touches
   `internal/relay/`, `docs/protocol-mobile.md`, or `internal/e2e/relay*`. No `blockedBy`
   set.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — no new boundary. The ceiling measures
  `len(env.Payload)` on envelopes the *daemon* produced; push is server→phone only, so the
  phone cannot drive control-event volume directly (the #610 security note). The one
  indirect lever is `request_debug_bundle`, and the archive's size is determined by the
  daemon's own `.cast` recording, not by anything the requester supplies.
- **[Network & I/O]** SHOULD FIX, accepted as designed — a paired phone can trip the ceiling
  on **its own** conn by requesting an oversized bundle, and the outcome is a teardown it can
  repeat. This is bounded three ways: the ceiling and the teardown are per-session, so no
  other conn is affected; #911's in-flight gate prevents stacking bundles on one conn; and a
  paired phone can already hot-loop handshakes, which is cheaper for it than this. Residual,
  named: the count axis is unbounded for zero-payload control envelopes — no production
  producer emits one (§ Open questions 2).
- **[Cryptographic primitives]** No findings, and one load-bearing positive: the teardown
  path passes `frame = nil`, so **nothing is sealed**. No `s.send.Encrypt` call means no
  Noise send-nonce is burned for a frame that cannot reach the phone — the #874/#912 hazard
  where a burned nonce gaps the phone's recv nonce and kills a still-live session as a
  mislabelled failure. A future change that adds a sealed error frame here would reintroduce
  it; the doc comment must say so.
- **[Error messages, logs, telemetry]** No findings — both new records carry only
  content-free discriminants (`event`, `conn_id`, `bytes`, `ceiling`, `close_code`, and
  `env.Type`, which `v2.push.drop` already logs). Payload bytes appear in neither. `bytes` is
  a length, not content, and its only reader is the operator reading the daemon's own log.
- **[Concurrency]** No findings — no new goroutines, no new locks, no change to lock
  ordering. `pushMu` remains a leaf lock taken alone; the latch read joins the existing hold;
  the channel send and both logs happen after the unlock. The teardown is Run-owned, so
  `enqueue` (off-Run) can never close a session. The one TOCTOU worth naming — the queue
  draining below the ceiling between the signal and the arm — is resolved deliberately in
  favour of tearing down anyway, because envelopes were already discarded and only the
  re-handshake replays them; a re-check would manufacture the permanent silent gap the ticket
  forbids.
- **[Threat model alignment]** No findings — the relevant `docs/protocol-mobile.md`
  § Security model threat is resource exhaustion by a paired peer, addressed above. The
  close-code addition is a wire-visible change and gets its row plus prose in § Error codes.
- **[Tokens/secrets]**, **[File operations]**, **[Subprocess execution]** — not applicable:
  the change touches no credential, no filesystem path, and spawns no process. It adds two
  struct fields, one constant, one close code, one channel, one Run arm and one method, all
  within `internal/relay`.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
