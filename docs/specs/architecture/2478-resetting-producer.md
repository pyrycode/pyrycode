# #2478 — the `resetting` producer

Ship the producer for the `resetting` frame #2453 declared: a conversation reset
reports `wrapping_up`, then `restarting` carrying whether a handoff note was made,
then a falling edge, to every interactive client.

## Files read

- `internal/protocol/interactive.go` → `ResettingPayload`, `ResetPhaseWrappingUp`,
  `ResetPhaseRestarting`, `ResetHandoffPending`, `ResetHandoffWritten`,
  `ResetHandoffSkipped` — the wire shape and both closed sets, already shipped. The
  payload's own doc fixes the three-edge sequence this producer has to emit.
- `internal/protocol/codes.go` → `TypeResetting` — the envelope type constant.
- `cmd/pyry/session_reset.go` → `conversationReset.wrapUp`, `storeNote`,
  `previousNote`, `dropBacklog`, `begin`, `release` — the reset routine #2477
  shipped. `storeNote`'s successful `WriteHandoffNote` is the only `written`; its
  SECURITY paragraph is the no-error-value-in-records rule this ticket inherits.
- `cmd/pyry/main.go` → `activeSessionStarter`, `start`, `resetThenRotate`,
  `reportNewSessionOutcome`, `startFreshRunner`, `approvalParkedReport` — the
  dispatch arms, the reset tail where the three edges are orderable, and the house
  pattern for a late-bound relay seam threaded through `relayWiring`.
- `cmd/pyry/attachment_offer_v2.go` → `attachmentOfferEmitterV2`,
  `newAttachmentOfferEmitterV2`, `announce` — the *synchronous* v2 emitter: daemon
  ctx captured at construction, leaf mutex around `nextID`, no `Run` goroutine. This
  is the shape this producer copies, not `sessionErrorEmitterV2`'s.
- `cmd/pyry/session_error_v2.go` → `sessionErrorEmitterV2`, `broadcast`,
  `startSessionErrorStreamV2` — the interactive gate, the push-error posture, the
  `start…StreamV2` wiring slot, and the SECURITY block's content-free-records rule.
- `cmd/pyry/queue_state_v2.go` → `queueStateEmitterV2.broadcast` — the same fan-out
  loop, read to confirm the two siblings agree on it.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2` (its `qse` / `sessionErr` /
  `transitions` producer block and the returned cleanup) — where the broadcaster
  exists and where a producer is started and stopped.
- `cmd/pyry/new_session_reset_test.go` → `resetProbe`, `asyncRunner`, `safeLog`,
  `lateOutcome` — the #2477 dispatch doubles this ticket's tests reuse.
- `cmd/pyry/interactive_turn_v2_test.go` → `fakeInteractiveBcast` — read and
  **rejected** as the double for this ticket: its own doc says it carries no mutex
  because its emitter spawns no goroutine, and this one is driven from the reset
  tail goroutine.
- `docs/protocol-mobile.md` § `resetting` — the published edge table, the
  "`skipped` is a reported outcome, not a missing value" rule, and the
  "No producer yet" line this ticket makes false.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`
  — the seam's own overview: `boundedConvID` is the log-bounding rule for
  client-named ids, and only the invalid-shape arm needs it.

## Context

#2453 declared the frame with no producer. #2477 shipped the reset routine —
`conversationReset.wrapUp` runs a wrap-up turn against the outgoing child and
writes its reply as the successor's handoff note, and `resetThenRotate` runs that
and then the rotation on its own goroutine. From a client's side the whole thing is
invisible: the screen pauses for up to ninety seconds and nothing says why, or
whether the note was made. This ticket is the producer.

The frame is **daemon-originated**. Nothing on it is `claude`'s: the id is the
daemon's resolved canonical conversation id, `active` is a computed bool, and
`phase`/`handoff` are tokens selected from closed sets the daemon authors. It is
not a `turnevent` variant, so it must not enter the `turnbridge` mapper or
`interactiveTurnEmitterV2`.

No ADR is warranted — this is a producer for an already-decided wire contract.

## Design

### The emitter — `cmd/pyry/resetting_v2.go`

A new unexported `resettingEmitterV2`, **synchronous**: no hand-off channel and no
`Run` goroutine.

`queueStateEmitterV2` and `sessionErrorEmitterV2` buffer onto a `Run` goroutine
because their callers — msgqueue's `OnChange` and `OnGiveUp` seams — are under a
MUST-NOT-BLOCK contract, and a drop-on-full send is what satisfies it. This
producer's caller is `resetThenRotate`, which already runs on its own goroutine
precisely so a ninety-second wrap-up cannot block dispatch. So the queue buys
nothing here, and it would actively **cost** something: a drop-on-full send can
drop the falling edge, and the published contract is that every rising sequence
ends in one so a client needs no timeout of its own. A synchronous call cannot
drop an edge and cannot reorder two.

`attachmentOfferEmitterV2` is the emitter this copies — the one existing v2 emitter
that fans out synchronously off a caller's goroutine, with the daemon ctx captured
at construction and a leaf mutex around `nextID`.

Contract:

```go
type resettingEmitterV2 struct {
	ctx    context.Context // daemon ctx, captured at construction
	logger *slog.Logger

	mu     sync.Mutex             // leaf; never held across ActiveConns or Push
	bcast  interactiveBroadcaster // late-bound; nil before attach and after detach
	nextID uint64
}

func newResettingEmitterV2(ctx context.Context, logger *slog.Logger) *resettingEmitterV2

func (e *resettingEmitterV2) wrappingUp(convID string)          // active:true  wrapping_up pending
func (e *resettingEmitterV2) restarting(convID string, wrote bool) // active:true  restarting written|skipped
func (e *resettingEmitterV2) done(convID string)                // active:false ""          ""

func (e *resettingEmitterV2) attach(bcast interactiveBroadcaster)
func (e *resettingEmitterV2) detach()

func startResettingStreamV2(re *resettingEmitterV2, bcast interactiveBroadcaster) func()
```

Three named edge methods rather than one `emit(active, phase, handoff)` at the call
site: the set is closed at exactly three and will not grow, the call sites in
`resetThenRotate` then read as the sequence itself, and the token selection — the
one place `written` vs `skipped` is decided — stays in the file that owns the wire
mapping. Each is a two-line delegation to a private `emit(convID string, active
bool, phase, handoff string)` holding the single fan-out loop.

`bool` rather than a protocol token crossing out of `session_reset.go`: that file
holds the reset's daemon-side coordination and imports no wire package, and the
mapping from "a note was written" to `ResetHandoffWritten` belongs at the wire
boundary. `restarting` performs it.

**Every method is nil-receiver safe.** `activeSessionStarter` is a constructor-less
bag of injected seams built as a named-field literal, so an omitted `resetting` is a
reachable state — the PTY posture and every existing test literal in the package.
A nil emitter emits nothing, the same tolerance `logger()` and `approvalParkedReport`
already keep.

**The late-bound broadcaster.** The broadcaster exists only inside `startRelayV2`,
after the `activeSessionStarter` literal is built in `main.go` — the chicken-and-egg
`qse` and `sessionErr` solve by taking `bcast` as a `Run` parameter. With no `Run`
there is no parameter to take, so the emitter is pre-built in `main.go` and the
broadcaster is published into it from `relay.go`, the shape `approvalParkedReport`
already uses for `ApprovalParked`.

Unlike that one, `bcast` is guarded by the mutex rather than resting on a
goroutine-creation happens-before argument. The mutex already exists for `nextID`
(two concurrent conversations reset concurrently, so the counter genuinely races,
exactly as `attachmentOfferEmitterV2`'s doc records for its own). Extending it to
`bcast` costs one uncontended acquisition per edge — three per reset, on an
operator-driven path — and removes a happens-before chain that would have to be
re-derived every time a new caller of `StartNewSession` appears. `detach` then also
means a fan-out cannot race a winding-down manager, which is what `startRelayV2`'s
cleanup comment asks of every producer.

**No replay ring, no durable history, `EventID` nil.** `resetting` is ephemeral
status like `queue_state` and `session_error`, not a session boundary: no
`appendConversationHistory` counterpart, no connect-time reconcile. A phone that
connects mid-reset learns nothing about it, which is correct — the falling edge is
seconds away and a stale rising edge replayed after the fact would pin an indicator
open.

Outbound-only: `IsKnownAppType` rejects an inbound `resetting`, so no handler is
owed.

### `wrapUp` returns the outcome — `cmd/pyry/session_reset.go`

`wrapUp(convID string)` becomes `wrapUp(convID string) (wrote bool)`, and
`storeNote(convID, text string)` becomes `storeNote(convID, text string) (wrote bool)`.

`storeNote` answers `true` on exactly one path — `WriteHandoffNote` returning no
error — and `false` on its refusal arms (`notes == nil`, an unusable reply,
`ErrHandoffNotesDisabled`, a write error). Every early return in `wrapUp` answers
`false`: nil receiver or unresolved seam, no bound session, a runner that captures
no reply, the conversation not going idle, a capture already armed, the prompt
failing to deliver, the deadline expiring. That is the closed skip set AC-2
enumerates, and it is the one that file's records already enumerate — no new branch
is introduced, only a value threaded out of the existing ones.

Its sixteen call sites are expression statements and compile unchanged.

### The edges — `activeSessionStarter.resetThenRotate` in `cmd/pyry/main.go`

`activeSessionStarter` gains one optional field, `resetting *resettingEmitterV2`.
`resetThenRotate` becomes:

1. `a.resetting.wrappingUp(convID)` — the first rising edge, before `wrapUp`.
2. `wrote := a.reset.wrapUp(convID)`.
3. `a.resetting.restarting(convID, wrote)` — the phase change, after the wrap-up and
   before the rotation.
4. `defer a.resetting.done(convID)` — registered here, so the falling edge fires at
   every exit, after the rotation attempt has completed on all three paths (clean,
   rotate error, workspace refused).

**The deferred falling edge must run before `release`, and LIFO gives that for
free.** `defer release()` is registered first, so it runs last; `done` registered
later runs first. The ordering is load-bearing rather than incidental: `release`
un-claims the conversation, and a second reset admitted before the falling edge
would put its own `wrapping_up` on the wire ahead of the previous reset's
`active:false`, clearing an indicator the client had just re-opened.

One deferred call rather than a call at each of the three exits, the reasoning
`reportNewSessionOutcome`'s own doc records for the same function: "the fourth edit
would forget it".

Nothing else in `start` changes. Because the emitter is reached only from
`resetThenRotate`, and `resetThenRotate` runs only on the wrap-up arm, AC-4 holds
structurally: every #2099 inert arm, the refused second reset, the synchronous
rotation of a childless conversation, and a daemon with `a.reset == nil` all return
before the `go` statement and emit nothing.

`convID` at that point is the daemon's resolved canonical id — `start` has already
passed it through `conversations.ValidID` or taken it from the cursor — so all three
edges name the same canonical id and never a raw client string.

### Wiring — `cmd/pyry/main.go` and `cmd/pyry/relay.go`

- `main.go`, beside `qse` and `see`: `resetting := newResettingEmitterV2(ctx, logger)`,
  passed into the `activeSessionStarter` literal as `resetting:` and into
  `relayWiring` as `resetting:`.
- `relay.go`: `relayWiring` gains `resetting *resettingEmitterV2`; `startRelayV2`
  calls `startResettingStreamV2(w.resetting, mgr)` beside the sibling producers and
  adds its cleanup to the returned teardown, before `<-mgrDone`.

`startResettingStreamV2` takes no `ctx` — the siblings take one for their `Run`
goroutine and this one has none. It attaches and returns an idempotent detach.

## Concurrency model

No goroutine is created by this ticket. The emitter runs entirely on the reset tail
goroutine `activeSessionStarter.start` already spawns, whose lifetime is bounded by
`wrapUpDeadline` plus a rotation and by the daemon context.

One lock, `resettingEmitterV2.mu`, a **leaf**: acquired to read `bcast`, released
before `ActiveConns`; re-acquired around the `nextID` bump alone and released before
`Push`. It is therefore never held across a call into the manager and can never nest
inside the manager's own `pushMu`, so there is no lock order to reason about — the
rule `attachmentOfferEmitterV2.mu`'s doc states for itself.

Two conversations can reset concurrently. Their fan-outs interleave freely; order is
only ever asserted *within* one conversation, and there the three edges are three
sequential calls on one goroutine, with `conversationReset.begin` guaranteeing no
second reset of the same conversation overlaps.

Teardown: daemon ctx cancellation makes `ActiveConns` answer empty and a racing
`Push` return its error; `detach` additionally makes the emitter inert.

## Error handling

- **Marshal failure** — defensive only (`ResettingPayload` is two strings, a string
  and a bool). Debug record, no frame, never echoing the payload or `err.Error()`.
- **Per-conn `Push` failure** — Debug record and continue to the next conn; a
  dropped conn must not abort the others. `ctx.Err() != nil` returns early
  (teardown). There is no re-sync path to mention: the frame is live-only.
- **No broadcaster attached** — inert. Reachable before `startRelayV2` attaches and
  after teardown detaches, and in a PTY-mode daemon that never starts the relay leg.
- **A failing wrap-up never fails the reset** — unchanged from #2477. The bool this
  ticket threads out is an outcome, not an error, and no caller acts on it beyond
  choosing a token.
- **A failing rotation still gets a falling edge** — the deferred `done` is on every
  exit path.

## Testing strategy

New `cmd/pyry/resetting_v2_test.go`, with its own mutex-guarded broadcaster double
(`fakeInteractiveBcast` documents itself as single-goroutine-only, and these tests
drive frames off the reset tail goroutine):

- **The three edges, in order, over one reset** — drive `resetThenRotate` directly
  and assert exactly three frames: `active:true/wrapping_up/pending`,
  `active:true/restarting/<outcome>`, `active:false/""/""`, all `TypeResetting`, all
  naming the same canonical conversation id (AC-1).
- **`written` vs `skipped`** — a table over the second edge: a `wrapUp` that stored a
  note versus each refusal shape, asserting the token (AC-2).
- **Ordering against the wrap-up and the rotation** — `wrapping_up` is on the wire
  before `wrapUp` is entered, and `restarting` before `RestartFresh`.
- **The falling edge follows a FAILED rotation** — a `rotate` seam returning an
  error still produces `active:false` last, and exactly one rotation was attempted
  (AC-3, and the untouched half of AC-5).
- **The falling edge precedes `release`** — a release seam that records the frames
  seen so far proves the LIFO ordering rather than assuming it.
- **A non-interactive conn receives nothing** while an interactive one on the same
  snapshot receives all three (AC-5).
- **Inert arms emit nothing** — drive `start` for a refused second reset, a named
  conversation with no live child, and a nil `reset`, asserting zero frames (AC-4).
- **A nil emitter and an unattached emitter are inert** — no panic, no frame (the
  PTY posture and the pre-attach window).
- **`detach` stops the fan-out.**

Extensions to `cmd/pyry/session_reset_test.go`: assert `wrapUp`'s returned bool —
`true` on the stored-note path, `false` on the refusal arms already covered there
(unresolved, no capture, not idle, capture busy, write failure, deadline, unusable
reply, notes disabled, write error).

Gate: `go test -race ./cmd/pyry/... ./internal/protocol/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Documentation handoff

Pending for the documentation stage; not touched by this ticket.

- `docs/protocol-mobile.md` § `resetting`: replace the "**No producer yet**: #2455
  owns the reset routine that emits this frame, and #2456 routes a client's `/clear`
  into it." line with the shipped producer (#2478), keeping #2456 named as the
  remaining `/clear` route.
- The same file's § `new_session` paragraph beginning "Since #2477, a conversation
  with a live child runs a wrap-up turn before the kill" ends by naming #2455 as the
  unshipped reporter of the two phases. So does the `2026-09-16` changelog entry for
  #2477. Both are now false — re-point both at #2478.
- The changelog gains a dated entry for the producer.

## Open questions

1. **Does `startResettingStreamV2` earn its existence over a bare `attach` call in
   `startRelayV2`?** It exists for the detach half — `startRelayV2`'s cleanup
   comment asks that producers stop before the manager is waited on. Resolve during
   Phase B; if the cleanup turns out to be a no-op in practice, collapse it to the
   attach and say so in a `## Revisions` entry.
2. **Do the three named edge methods stay, or collapse to one `emit` with tokens at
   the call site?** Kept if `resetThenRotate` reads as the sequence; revisited if the
   delegations turn out to carry no doc of their own.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. There is one boundary and it is upstream of
  everything this ticket adds: `activeSessionStarter.start` is the trust boundary
  for the client-named conversation id (`internal/relay` imports neither
  `internal/conversations` nor `internal/sessions`, so every check lives there). By
  the time `resetThenRotate` runs, `convID` is either `conversations.ValidID`-passed
  or the daemon's own cursor value — the emitter is handed a resolved canonical id
  and never a raw client string. The other three fields are daemon-authored by
  construction: `active` is a computed bool and `phase`/`handoff` are `internal/protocol`
  constants selected by `restarting`, never interpolated from any input.
- **[Trust boundaries — the note]** No findings, enforced structurally rather than by
  discipline. `wrapUp` returns a **bool**, `storeNote` returns a **bool**, and the
  emitter holds no `handoffNoteStore`, no reply text and no `conversationReset` — so
  the note's bytes structurally cannot enter the payload or a record. This is
  deliberately the same construction as `sessionErrorEmitterV2`, which holds no
  `*msgqueue.Queue` so it cannot reach queued text. A future edit that widened
  `wrapUp`'s return to carry the reply (a length, a prefix, a reason string) would
  reopen this and must not be made without re-running this pass.
- **[Tokens, secrets, credentials]** Not applicable, by design decision rather than by
  absence: `ResettingPayload` declares no prose field, so the frame has no channel a
  secret could ride. Nothing on this path reads, mints, stores or compares a
  credential. `Envelope.ID` is a per-emitter wire counter, not a nonce — each v2
  emitter starts its own at zero and their ids collide freely across producers, and
  `internal/noise` never reads it — so it needs neither unpredictability nor
  `crypto/rand`.
- **[File operations]** Not applicable. This ticket touches no filesystem path. The
  handoff note's write is `storeNote`'s existing `WriteHandoffNote` call, unchanged
  in behaviour; only a bool is threaded out of it.
- **[Subprocess / external command execution]** Not applicable. No `exec.Command`,
  no argv, no environment. The wrap-up prompt's delivery to the child is #2477's and
  is not touched.
- **[Cryptographic primitives]** Not applicable. The frame rides the existing
  Noise-sealed v2 channel through `V2SessionManager.Push`; this producer performs no
  crypto and selects no key, nonce or comparison.
- **[Network & I/O]** No findings. The frame is **outbound-only** — `IsKnownAppType`
  rejects an inbound `resetting`, so this ticket adds no parse surface and no read
  to cap. The payload is bounded by construction: a 36-byte id, a bool, and two
  tokens drawn from closed sets. Amplification was considered and is bounded
  upstream: a hostile paired phone spamming `new_session` gets at most three frames
  per conn per reset, `conversationReset.begin` refuses a second reset of the same
  conversation while one is live, and every #2099 inert arm returns before the tail
  runs. Fanning three frames where one `session_transition` used to go is a 3×
  increase on a path already gated by a ninety-second wrap-up and a process respawn.
- **[Error messages, logs, telemetry]** No findings, and this is the category with
  the most rules to keep. Every record the emitter writes carries only content-free
  discriminants — `event`, `conversation_id`, `conn_id`, `env_id`, and Push's
  transport sentinel as `err` — which is `sessionErrorEmitterV2`'s SECURITY block
  applied verbatim. The marshal-failure arm never echoes the payload bytes or
  `err.Error()` (`encoding/json` quotes input bytes into its error). No record is
  added to `session_reset.go`, so that file's absolute rule — no error value from
  the wrap-up path reaches a record — is inherited untouched. Two deliberate
  omissions, stated so a reviewer does not read them as gaps: (a) **no per-edge
  Info record.** The outcome is already recorded by `reset.wrapup.note_written` and
  its four granular refusal keys; three more records per reset would be noise
  carrying nothing new. (b) **no `boundedConvID` on the emitter's records.** That
  helper exists for the one arm where an arbitrary attacker-controlled string
  reaches a log call — the shape-check failure in `start` — and every arm past
  `conversations.ValidID` logs a string already known to be 36 bytes. Applying it
  here would be cargo-culting a bound onto a value that cannot be unbounded.
- **[Concurrency]** No findings; three hazards walked. **Lock ordering:**
  `resettingEmitterV2.mu` is a leaf held only around a field read and a counter bump,
  never across `ActiveConns` or `Push`, so it cannot nest inside the manager's own
  push lock and there is no order to state. **TOCTOU on `bcast`:** a `detach`
  landing between the guarded read and the `Push` means the frame goes into a
  manager that is tearing down; `Push` answers an error, the Debug arm logs it and
  `ctx.Err() != nil` returns early — the same benign race the siblings already run.
  **Goroutine lifecycle:** none is created, so none can leak. The emitter runs on the
  reset tail goroutine #2477 already spawns, bounded by `wrapUpDeadline` and the
  daemon context.
- **[Concurrency — the falling-edge guarantee]** SHOULD FIX, carried into Phase B as
  a test rather than a code change. The published contract is that every rising
  sequence ends in a falling edge. Two ways it can be violated, both acceptable and
  both to be asserted or named: a **panic** inside `wrapUp` or `startFreshRunner`
  unwinds through the deferred `done`, so the indicator still clears (the client sees
  `wrapping_up` then `active:false`, a terminated sequence missing its middle); and
  **daemon shutdown mid-reset** cancels the ctx, `ActiveConns` answers empty and the
  falling edge reaches nobody — which is not a gap, because the connection the client
  would have received it on is going away in the same instant and a client must
  already handle a dropped transport. The Phase B test must pin the
  falling-edge-before-`release` LIFO ordering explicitly; getting that backwards
  would let a second reset's rising edge precede the first's falling edge and strand
  an indicator open, which is the one ordering error a client cannot recover from.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security
  model threat 1 — `claude`-authored prose reaching a client unsanitized — does
  **not** land on this frame, and `ResettingPayload`'s own doc says why: nothing on it
  is `claude`'s, so no render-as-inert-text rule is owed and a client must not
  inherit `compacting`'s from the neighbourhood. The one thing this frame newly tells
  a paired device is a single bit — whether a handoff note was written for a
  conversation — and it goes to every interactive conn rather than being scoped to
  the conversation, exactly as `queue_state`, `session_error` and `session_transition`
  already do. That is the existing pairing trust model (any paired interactive device
  sees every conversation's status), not a widening: the same device already learns
  the reset happened from `session_transition`. Conversation-scoped fan-out for the
  whole status class is out of scope here and belongs to no ticket yet.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16

## Revisions

_(none yet)_
