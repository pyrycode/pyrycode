# Spec: Give-up → session_error producer (#1008)

Split-child B of #1001. Blocked-by #1007 (wire vocabulary, merged). This ticket
adds the **producer** that consumes the merged `msgqueue.OnGiveUp` seam (#1000)
and emits the merged `TypeSessionError` frame (#1007) over the v2 wire, plus the
daemon wiring that connects the two. Purely additive; no existing behaviour
changes.

Size: **S**. 3 production files (1 new, 2 modified), 0 new exported types, purely
additive wiring (no consumer cascade), ~375 LOC total incl. tests.

`security-sensitive` — the security-review pass is at the end of this spec
(verdict PASS).

---

## Files to read first

The producer is a near-clone of the `queue_state` producer with the confidential
`Snapshot` path removed. Read these before writing anything:

- `cmd/pyry/queue_state_v2.go:1-231` — **the template to mirror.** Copy its shape:
  the queue-size const + never-log SECURITY doc block, the `newXEmitterV2`
  constructor, the `xNotify` seam-builder (non-blocking drop-on-full send +
  content-free Warn), `Run(ctx, bcast)`, `broadcast(...)`, and
  `startXStreamV2(...)`. **Do NOT copy** `toQueueStatePayload` (no per-item
  mapping here) or `outstandingQueues` (no connect-time reconcile — see § Design).
- `cmd/pyry/session_transition_v2.go:47-120` — precedent for a **struct-carrying**
  hand-off channel (`in chan sessions.SessionTransition`). Your channel carries a
  `giveUpNotice` struct, not a bare string; this is the shape to follow.
- `internal/protocol/messaging.go:223-251` — `SessionErrorPayload{ConversationID,
  Code, Message}` (all `json` tags, no omitempty). The exact wire type to stamp.
  Note the doc: "the never-log discipline for Message is #1008's concern."
- `internal/protocol/codes.go:34` — `CodeSessionBlocked = "session.blocked"` (the
  fixed terminal code the producer stamps). `codes.go:476` — `TypeSessionError`.
- `internal/msgqueue/queue.go:103-114` — `GiveUpFunc func(convID, reason string)`
  contract: MUST-NOT-BLOCK, safe for concurrent invocation, nil ⇒ disabled.
- `internal/msgqueue/queue.go:502-532` — `giveUp`: fires `notifyGiveUp(convID,
  reason)` **exactly once** per give-up, off-lock, after `q.mu` release; `reason`
  is a daemon-generated `fmt.Sprintf` over elapsed+delivery-err, **never**
  `head.text`. This is the upstream sanitiser your producer relies on.
- `cmd/pyry/main.go:807-828` — the `queueChanges` channel create → `msgqueue.New`
  (OnChange set) → `newQueueStateEmitterV2` build sequence. Add the parallel
  `giveUps` channel + `OnGiveUp` set + `newSessionErrorEmitterV2` build here.
- `cmd/pyry/main.go:862-884` — the `relayWiring{...}` literal; add `sessionErr: see`.
- `cmd/pyry/relay.go:200-202` — the `qse` field in `relayWiring`; add the
  parallel `sessionErr` field.
- `cmd/pyry/relay.go:565-590` — the `startQueueStateStreamV2` start-site inside
  `startRelayV2` (where `mgr`, the broadcaster, exists) + the cleanup block. Add
  the parallel start + teardown here.
- `cmd/pyry/interactive_turn_v2.go:31-34` — the `interactiveBroadcaster` interface
  (`ActiveConns` + `Push`) your `Run`/`broadcast`/`startX` signatures take.
- `cmd/pyry/interactive_turn_v2_test.go:52-91` — `fakeInteractiveBcast` +
  `recordedPush` test doubles (same package, directly reusable). Your unit tests
  drive the producer through these.
- `cmd/pyry/queue_state_v2_test.go:270-373` — the notify-drop-on-full,
  notify-delivers-value, run-delivers-from-channel, and cleanup-joins-on-cancel
  tests to mirror.
- `cmd/pyry/relay_guard_test.go:127` — `"TypeSessionError": "push"` **already
  present** (added by #1007). Do NOT touch this file; the totality guard is green.

---

## Context

The interactive message queue (`internal/msgqueue`) drains a conversation's
backlog one head at a time through the reliable `WriteUserTurn` path. When a
claude child parks at startup (an unanswerable dialog, a failing readiness gate,
a network stall), the head fails delivery persistently. #1000's engine-side bound
(`GiveUpAfter`, default 2 min per head) stops the forever-retry: it drops the
failed head, clears `draining`, and fires the `OnGiveUp` seam carrying the
affected `conversation_id` and a **daemon-generated** reason — then the drain
exits (a later `Enqueue` re-spawns it). #1000 shipped that seam **disabled**
(`OnGiveUp` nil): the give-up is real but produces no client-visible frame.

#1007 (merged) defined the wire vocabulary: `TypeSessionError`, the terminal
`CodeSessionBlocked`, and the conversation-scoped `SessionErrorPayload`. It is
vocabulary only — no producer.

This ticket closes the gap. It sets `OnGiveUp` to a producer that fans a
`session_error` frame to interactive phones, so a wedged session tells the client
something is wrong instead of leaving a queued turn that silently never runs. The
route mirrors the `queue_state` producer (#722) exactly: seam → buffered channel
→ dedicated Run goroutine → capability-gated fan-out over the v2 manager.

---

## Design

### New file: `cmd/pyry/session_error_v2.go`

A structural near-clone of `queue_state_v2.go`, **minus the queue reference** — the
producer holds no `Snapshot` and no `*msgqueue.Queue`, so it *cannot* reach
queued text (the AC-3 confidentiality property, enforced by construction rather
than by discipline).

**1. Queue-size const.**

```go
const sessionErrorQueueSize = 16
```

Mirrors `queueStateQueueSize`/`sessionTransitionQueueSize`. Give-ups are far rarer
than backlog changes (each needs ≥2 min of persistent failure per head), so the
buffer is effectively unreachable except under a simultaneous-mass-wedge burst
(N wedged conversations all crossing the bound at once). Drop-on-full is mandated
by `GiveUpFunc`'s MUST-NOT-BLOCK contract — blocking the send would stall the
drain's exit→respawn.

**2. Hand-off value (unexported struct).**

```go
type giveUpNotice struct { convID, reason string }
```

Unlike `queue_state` (whose channel carries a bare `convID` because the consumer
re-reads current state via `Snapshot`), the give-up `reason` is a **one-shot edge
value that cannot be recovered by re-reading queue state** (AC-4). It must ride
the channel alongside the id. This is the same struct-channel shape as
`sessionTransitionEmitterV2.in chan sessions.SessionTransition`.

**3. Emitter struct + constructor.**

```go
type sessionErrorEmitterV2 struct {
    in     <-chan giveUpNotice // the ONLY cross-goroutine state; no queue ref
    logger *slog.Logger
    nextID uint64              // per-conn env-ID counter; Run-goroutine-only, no atomic
}

func newSessionErrorEmitterV2(in <-chan giveUpNotice, logger *slog.Logger) *sessionErrorEmitterV2
```

The channel is the constructor's only wiring input. There is deliberately **no
`snapshot`/`queue` parameter** (contrast `newQueueStateEmitterV2`) — the type
system is the AC-3 guarantee. `EventID` is left nil on every emitted envelope: a
give-up is a one-shot edge, so this producer does **not** join the #647/#649
reconnect-replay ring and there is **no** connect-time reconcile source (contrast
`OutstandingQueues`/`OutstandingModals`). A phone that connects *after* a give-up
fired does not get a replay — correct, because #1000 respawns the session after
give-up, so replaying a stale "session blocked" to a reconnecting phone would be
wrong. `TypeSessionError` ≠ `TypeAssistantDelta`, so it is a never-drop control
event in the push queue automatically (same as `queue_state`).

**4. The `OnGiveUp` seam builder.**

```go
func sessionErrorNotify(ch chan<- giveUpNotice, logger *slog.Logger) msgqueue.GiveUpFunc
```

Returns a closure that does a **non-blocking buffered send** of
`giveUpNotice{convID, reason}` with drop-on-full + a content-free Warn (event +
`conversation_id` **only** — never `reason`). Captures the channel, not the
emitter (so the emitter is never read off the constructing goroutine). Direct
analogue of `queueStateNotify`. Honors MUST-NOT-BLOCK on the drain goroutine.

**5. `Run` + `broadcast`.**

```go
func (e *sessionErrorEmitterV2) Run(ctx context.Context, bcast interactiveBroadcaster)
func (e *sessionErrorEmitterV2) broadcast(ctx context.Context, bcast interactiveBroadcaster, n giveUpNotice)
```

`Run` drains `in` until `ctx.Done()` or the channel closes, calling `broadcast`
per notice — one notice → one broadcast → one envelope per interactive conn (AC-5:
emits once per give-up, no busy-loop). `broadcast` builds the payload, marshals it
**once**, then fans to interactive conns:

- payload = `protocol.SessionErrorPayload{ConversationID: n.convID, Code:
  protocol.CodeSessionBlocked, Message: n.reason}`. `Code` is the fixed terminal
  constant the producer stamps — the seam carries no code field.
- marshal-error branch: content-free Debug log + return (never echo payload bytes
  or `err.Error()`), mirroring `queue_state`'s defensive branch.
- fan-out: `for _, c := range bcast.ActiveConns(ctx) { if !c.Interactive {
  continue }; e.nextID++; env := protocol.Envelope{ID: e.nextID, Type:
  protocol.TypeSessionError, TS: ts, Payload: payloadJSON}; Push... }`. The
  `!c.Interactive` skip is the capability gate (AC-2: interactive conns only). A
  per-conn Push error is logged at Debug and the loop continues; a `ctx.Err() !=
  nil` mid-fan-out returns early (teardown). Exact same loop as
  `queueStateEmitterV2.broadcast`.

**6. Stream lifecycle helper.**

```go
func startSessionErrorStreamV2(ctx context.Context, see *sessionErrorEmitterV2, bcast interactiveBroadcaster) func()
```

Starts `see.Run(ctx, bcast)` on a goroutine, returns an idempotent cleanup that
waits for Run's `done` channel. Does **not** close `in` (a late `OnGiveUp` send
racing teardown drops harmlessly into the open-but-unread buffer). Direct
analogue of `startQueueStateStreamV2`.

### Wiring: `cmd/pyry/main.go`

Parallel to the `queueChanges`/`qse` lines (main.go:807-828):

- Before `msgqueue.New`: `giveUps := make(chan giveUpNotice, sessionErrorQueueSize)`.
- In the `msgqueue.Config` literal (main.go:813-817): add `OnGiveUp:
  sessionErrorNotify(giveUps, logger),` alongside the existing `OnChange`. This is
  the whole "wire it in" — flips #1000's seam from nil to live.
- After `qse := newQueueStateEmitterV2(...)`: `see :=
  newSessionErrorEmitterV2(giveUps, logger)`.
- In the `relayWiring{...}` literal (main.go:862-884): add `sessionErr: see,`.

### Wiring: `cmd/pyry/relay.go`

- Add a field to `relayWiring` (next to `qse`, relay.go:200-202):

```go
// sessionErr is the pre-built session_error emitter (#1008) whose Run goroutine
// startRelayV2 starts over the v2 manager. Built at main.go (channel shared with
// the msgqueue OnGiveUp seam) for the same chicken-and-egg reason as qse.
sessionErr *sessionErrorEmitterV2
```

- Inside `startRelayV2`, next to `streamQueueStateCleanup` (relay.go:573):
  `streamSessionErrCleanup := startSessionErrorStreamV2(ctx, w.sessionErr, mgr)`.
  This is where `mgr` (the broadcaster) is constructed — the Run goroutine must
  start here, not at main.go (AC-5 lifecycle: starts in `startRelayV2`, teardown
  in the relay cleanup block).
- In the cleanup block (relay.go:575-590), before `<-mgrDone`, add
  `streamSessionErrCleanup()` (after `streamQueueStateCleanup()`). Stopping the
  producer before waiting on the manager keeps a fan-out from racing a
  winding-down manager — the established teardown ordering.

### What this ticket does NOT do

- Does not touch the `queue_state` producer. The give-up *also* fires `OnChange`
  (the head drop shrinks the backlog), so `queue_state` reflects the shrink
  independently; this ticket adds only the new typed error frame.
- Does not add wire vocabulary (that was #1007) or change the drain's bound/retry
  logic (that was #1000).
- Does not add a connect-time reconcile (`OutstandingErrors`) — intentional, per
  AC-4's one-shot-edge framing.
- Does not touch `internal/protocol` or `relay_guard_test.go` — the type, code,
  payload, compat-drift lists, and the `"push"` classification all landed in #1007.

---

## Concurrency model

- **Goroutines:** one dedicated `Run` goroutine (started by
  `startSessionErrorStreamV2`), exactly like `queue_state`. Neither the msgqueue
  Run goroutine nor a drain goroutine — this makes `ActiveConns` safe to call and
  `nextID` single-goroutine (no atomic).
- **Cross-goroutine state:** the `in` channel only (channels are concurrency-safe).
  `sessionErrorNotify` fires from the drain goroutine off-lock (queue.go releases
  `q.mu` before `notifyGiveUp`); its non-blocking send never touches emitter
  fields.
- **Lifecycle:** `Run` exits on `ctx.Done()` (daemon shutdown) or `in` close.
  Cleanup joins on the `done` channel. No leak. No locks taken anywhere in the
  producer → no lock-ordering concern.
- **Shutdown ordering:** cleanup runs before `<-mgrDone` so no `Push` races a
  winding-down manager — mirrors the four sibling producers' teardown.

---

## Error handling

- **Marshal failure:** `SessionErrorPayload` is a closed struct of three strings
  and cannot fail to marshal in practice; the defensive branch logs content-free
  Debug and returns (never echoes bytes or `err.Error()`).
- **Per-conn Push failure:** logged at Debug (content-free discriminants only),
  loop continues to the next conn; a dropped conn re-syncs on its own reconnect
  (or, for `session_error`, simply misses this one-shot edge — accepted, no
  replay).
- **Drop-on-full:** the notify seam drops with a content-free Warn. Unlike
  `queue_state` (recoverable via the next `OnChange` + `Snapshot`), a dropped
  give-up is **not** recovered — but the 16-deep buffer makes overflow unreachable
  short of a simultaneous mass-wedge, and the alternative (blocking) violates
  MUST-NOT-BLOCK and stalls the drain's respawn. Documented, accepted edge.

---

## Testing strategy

Same-package unit tests in `cmd/pyry/session_error_v2_test.go`, driving the
producer through the reusable `fakeInteractiveBcast` + `recordedPush` doubles.
Table-driven where natural; stdlib `testing` only. Scenarios (bullet form — the
developer writes the bodies in the project idiom, mirroring
`queue_state_v2_test.go`):

- **Fans one frame per interactive conn.** Notice `{conv, reason}` over a snapshot
  of ≥2 interactive conns → one `TypeSessionError` push per conn, each carrying a
  `SessionErrorPayload` that decodes to `{ConversationID==conv,
  Code==CodeSessionBlocked, Message==reason}`. Env IDs increment per conn.
- **Skips non-interactive conns.** A snapshot mixing interactive + non-interactive
  → pushes land only on the interactive ones (AC-2). Reuse the
  `interactive_modal_v2_test.go` mixed-conn shape.
- **Stamps the fixed terminal code, not the seam.** Assert `Code ==
  protocol.CodeSessionBlocked` regardless of the notice contents (the seam carries
  no code) — pins AC-2's "terminal, not transient".
- **Message carries the daemon reason verbatim, never queued text.** Feed a
  reason string; assert it round-trips into `Message` unchanged. (The producer has
  no queue handle, so "no queued text" is structural — this test pins the
  pass-through, and the security review pins the structural property.)
- **Push error continues the loop.** Inject a Push error on conn 1 of 3 → conns 2
  and 3 still receive their pushes (AC: a dropped conn must not abort the others).
- **`sessionErrorNotify` drop-on-full does not block.** Fill a cap-1 (or cap-0)
  channel, invoke the seam again → returns immediately, no send, Warn emitted
  (mirror `TestQueueStateNotify_DropOnFullDoesNotBlock`).
- **`sessionErrorNotify` delivers the full `{convID, reason}`.** A send with room
  → the received `giveUpNotice` carries *both* fields (guards against a
  bare-convID regression that would lose the one-shot reason).
- **`Run` delivers from the channel.** Start `Run` on a goroutine, send a notice,
  assert the broadcast happened; cancel ctx → `Run` returns.
- **`startSessionErrorStreamV2` cleanup joins on cancel.** Start, cancel ctx, call
  cleanup → returns after Run exits; second cleanup call is a no-op (idempotent).
  Mirror `TestStartQueueStateStreamV2_CleanupJoinsOnCancel`.
- **Never-log discipline (light).** Optional: a race-clean concurrent-notify test
  mirroring `TestQueueStateEmitterV2_Run_ConcurrentOnChange` to exercise
  `-race` on the seam under concurrent invocation.

No new integration/e2e coverage — the wiring is exercised by the existing relay
start path; the producer's behaviour is fully unit-testable through the
broadcaster double. The compat/guard drift is already covered by #1007's tests.

---

## Open questions

- **Buffer depth.** 16 matches the siblings and is effectively unreachable given
  the 2-min-per-head bound. Left at 16 for consistency; if a future mass-wedge
  scenario proves it drops, revisit with a reconcile source rather than a bigger
  buffer (a reconcile is the correct fix for "phone must not miss it", not depth).
  Not a blocker.
- **No reconcile is deliberate.** If product later wants a reconnecting phone to
  learn about a still-wedged session, that is a new `OutstandingErrors` source on
  the manager (a follow-up ticket), not a change here — and it only makes sense if
  the session is still wedged at reconnect time, which #1000's respawn makes
  transient. Explicitly out of scope.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The producer sits entirely downstream of an
  already-sanitised boundary. Its only two inputs arrive via the `OnGiveUp` seam:
  `convID` (daemon-resolved routing key, non-secret — same posture as
  `QueueStatePayload.ConversationID`) and `reason` (daemon-generated at
  `internal/msgqueue/queue.go:512`, a `fmt.Sprintf` over the elapsed window and
  the delivery error — the #1000 contract at `queue.go:508-509` guarantees it
  "NEVER carries `head.text`"). The producer holds **no** `*msgqueue.Queue` and no
  `Snapshot` reference, so untrusted queued text is *structurally* unreachable
  (AC-3) — the confidentiality property is enforced by the type (no queue field on
  `sessionErrorEmitterV2`), not by developer discipline. No untrusted→trusted
  crossing happens inside this file.

- **[Tokens, secrets, credentials]** N/A — the producer handles no tokens, keys,
  or credentials. `conversation_id` is a non-secret routing id (treated
  identically to `queue_state`/`session_transition`).

- **[File operations]** N/A — no filesystem access.

- **[Subprocess / external command execution]** N/A — no `exec`.

- **[Cryptographic primitives]** N/A in this file. The emitted envelope is sealed
  by the v2 manager's established Noise_IK transport on the same `mgr.Push` path
  every sibling producer uses; this ticket adds no crypto and reuses no key/nonce.

- **[Network & I/O]** No MUST FIX. Delivery is via `mgr.Push` (the established
  sealed, capability-gated fan-out); no new socket read, so no size-cap/slow-loris
  surface is introduced. The payload is a closed struct of three short strings
  (`convID` + fixed `CodeSessionBlocked` + the bounded daemon `reason`); no
  unbounded growth. Audience is **all** capability-gated interactive conns (paired,
  trusted devices), matching AC-2 and the `queue_state` model — and this frame
  discloses *strictly less* than `queue_state` (no queued text at all), so the
  cross-device disclosure posture is narrower than an already-shipped sibling. The
  device pairing (Noise_IK + token) remains the trust boundary; a paired
  interactive device seeing another of the same user's conversations' error frame
  is by-design multi-device behaviour, not a leak.

- **[Error messages, logs, telemetry]** No MUST FIX. Never-log discipline (the
  `messaging.go:228` "#1008's concern"): `Message` (== `reason`), the marshaled
  `payloadJSON`, and `err.Error()` on the marshal path are **never** logged at any
  level. Logged fields are content-free discriminants only — `event`,
  `conversation_id` (non-secret routing id), `conn_id`, `env_id`, and Push's
  transport-sentinel `err`. The drop-on-full Warn logs `event` +
  `conversation_id` only, never `reason`. Same discipline as the four sibling
  emitters and msgqueue itself.

- **[Concurrency]** No MUST FIX. Single `Run` goroutine owns `nextID` (no atomic
  needed); the `in` channel is the only cross-goroutine state (concurrency-safe).
  The `OnGiveUp` seam fires off-lock (queue.go releases `q.mu` before
  `notifyGiveUp`, `queue.go:529`) and its non-blocking send honors MUST-NOT-BLOCK,
  so it can never stall the drain's exit→respawn. No locks are taken in the
  producer → no lock-ordering concern. `Run` exits on `ctx.Done()` or `in` close;
  cleanup joins the done channel; no goroutine leak. Teardown runs before
  `<-mgrDone` so no `Push` races a winding-down manager.

- **[Threat model alignment]** The relevant `docs/protocol-mobile.md`
  § Security-model threat is untrusted phone content leaking cross-conversation or
  into logs. Addressed by (a) the structural no-`Snapshot` design (queued text
  unreachable), (b) the daemon-generated, text-free `reason` guaranteed upstream
  by #1000, and (c) the never-log discipline above. A malicious *phone* cannot
  influence the frame's content: it carries only the daemon's own `convID` and
  `reason`; the phone's queued message text never enters it. OUT OF SCOPE: a
  connect-time reconcile for a phone that reconnects mid-wedge — named as a
  follow-up in § Open questions; not required for this ticket and made transient by
  #1000's respawn.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-15
