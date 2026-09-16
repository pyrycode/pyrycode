# #2477 — a `new_session` runs a wrap-up turn and writes its reply as the handoff note

## Files read

- `cmd/pyry/main.go` → `activeSessionStarter.StartNewSession`, `resolveSpawnDir`,
  `startFreshRunner`, `beginRotationOrNoop`, `resolveBoundSession`,
  `interruptRunner`, `boundSession.WriteUserTurn`, `newInboundDeliver` — the reset
  path this ticket puts the wrap-up in front of, plus the delivery seam whose
  `waitIdleForDelivery` / `openForDelivery` ordering the wrap-up write copies.
- `cmd/pyry/relay_context_usage.go` → `contextUsageResolver`, `fly` — the
  coordinator shape: base-context flight, bounded round trip, nil-tracker check
  the tracker deliberately does not carry, and the "nothing here logs" rule.
- `cmd/pyry/session_reset_follow.go` → `sessionResetFollower`, its doc block —
  states the placement rule the reply capture must obey (parser's side of the
  fan-in channel, above both the drain's active-session gate and the droppable
  send) and names `TestSessionResetFollower_ObservesPastASaturatedSink` as its pin.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`, `streamRunner` — the
  decorator chain (`vocab.sinkFor` → `newTurnEndContextUsageRequester` →
  `newSessionResetFollower` → `sink.sinkForTag`) the capture joins, and the
  adapter that must expose it to the coordinator.
- `cmd/pyry/stream_turn_busy.go` → `turnBusyTracker.WaitIdle`,
  `waitIdleForDelivery`, `openForDelivery`, `turnMarkFor`, `observe` — idle wait,
  the mid-turn mark, and the confirmation that `TextChunk` is turn-open (hence
  droppable) while `TurnEnd` is the close.
- `internal/sessions/handoff.go` → `Pool.WriteHandoffNote`, `Pool.HandoffNote`,
  `MaxHandoffNoteBytes`, `ErrHandoffNotesDisabled` — the store, its truncation
  rule, and its "logs nothing, holds no logger" posture.
- `internal/sessions/systemprompt.go` → `admissibleHandoffNote`,
  `handoffNoteSection`, `handoffNoteFence`/`handoffNoteBegin`/`handoffNoteEnd` —
  the admission predicate and fence this ticket must reuse rather than re-derive.
- `internal/sessions/session.go` → `Session.WriteUserTurn`, `Session.Runner`,
  `Session.State` — the write surface and the runner reach.
- `internal/msgqueue/queue.go` → `Queue.Snapshot`, `Queue.Remove`, `Queue.notify`,
  `commitGate` — the backlog drop pair and why the committing head may refuse.
- `internal/relay/v2session_modal.go` → `V2SessionManager.handleNewSession` —
  **the constraint that shapes this whole design**: the seam is called
  synchronously on the manager's single Run dispatch goroutine.
- `internal/relay/v2session_seams.go` → `SessionStarter`,
  `RotatedWithoutWorkspaceError` — the seam contract and the one reply the verb owes.
- `docs/knowledge/features/sessions-package-key-types-handoffnote-store.md` and
  `…-writesystemprompt-systemprompttext.md` — #2467's and #2475's recorded
  reasoning: the note is untrusted claude-authored text, the composing site owns
  admission, and refusal is deliberately narrow.

## Context

`new_session` kills and respawns claude at once, so whatever the outgoing session
learned and never filed dies with it. #2467 shipped the store and #2475 shipped the
read side: a spawn already carries its conversation's note in the appended system
prompt. Nothing writes one. This ticket is the writer — the reset routine that puts
a bounded wrap-up turn in front of the kill.

Sizing: the refiner estimated ~1100 lines over 5 production files, over the 800-line
line of the sizing table and deliberately so, because the reply capture's only
consumer is the wrap-up routine in this same ticket and it changes nothing
observable on its own. The floor beats the ceiling; the overage is stated and the
ticket is built whole.

No ADR is warranted. The one decision that would justify one — "a handoff note is
keyed by conversation and outlives the session" — was already made and recorded by
#2467.

## Design

### The constraint that shapes everything: the seam is synchronous on Run

`handleNewSession` calls `SessionStarter.StartNewSession` **inline on the
V2SessionManager's single Run dispatch goroutine**, and its own doc says so. A
wrap-up turn is bounded at 90 seconds. Blocking that goroutine for up to 90 seconds
freezes frame dispatch for every connection and every conversation the daemon hosts.

So the reset's tail moves off that goroutine. `StartNewSession` keeps every inert
arm, keeps `resolveSpawnDir` where it is, and then — when there is a live child and
a resetter is wired — hands `wrap-up → rotate` to a goroutine and returns at once.

This is also what makes AC-4 meaningful: "a second `new_session` for a conversation
whose reset is in progress" is only reachable because the first one returned early.

**What this costs and why it is acceptable.** `resolveSpawnDir` is evaluated
synchronously as today, so `RotatedWithoutWorkspaceError` (#2443) is still returned
from `StartNewSession` and the client still gets its one reply. What changes is the
claim behind it: on the wrap-up path the rotation has been *committed to* rather
than *completed* when the reply seals. The rendered outcome is identical — the
successor comes up in the directory it was already in — and the alternative (drop
the reply on the wrap-up path) loses a shipped behaviour outright. The rotate
error, which the synchronous path returns for `handleNewSession` to Warn, has no
channel on the async path and is logged by the goroutine instead.

### Four pieces

**1. `wrapUpCapture` — a sink decorator (new file `cmd/pyry/wrapup_capture.go`).**

Chained in `newStreamRunnerFactory` alongside `newSessionResetFollower` and
`vocab.sinkFor`, i.e. on the **parser's side of the fan-in channel**. That
placement is not a preference, it is the correctness condition
`session_reset_follow.go`'s doc block states and
`TestSessionResetFollower_ObservesPastASaturatedSink` pins: the drain's
active-session gate drops every event whose producing session is not the *active*
conversation's, and `new_session` names background conversations; and
`turnMarkFor` answers `turnMarkOpen` for `TextChunk`, so assistant text is in the
droppable class and `sinkForTag` refuses it at `droppableCap`. A capture at
`busy.observe` clears the first gate and sits below the second, so it would write a
silently truncated note under load. The capture goes above both.

Contract:

```go
func newWrapUpCapture(next func(turnevent.Event)) *wrapUpCapture
func (c *wrapUpCapture) Sink(ev turnevent.Event)                    // decorator; forwards EVERY event unchanged
func (c *wrapUpCapture) begin() (*wrapUpReply, func(), bool)        // arm; false when already armed or c == nil
func (r *wrapUpReply) wait(ctx context.Context) (string, bool)      // blocks until turn end or ctx
```

- `Sink` appends `TextChunk.Text` while armed, ignores every other variant, and
  finishes the armed reply on `TurnEnd`. It forwards every event of every variant
  unchanged — the contract each link in this chain states, for the same reason:
  swallowing one would change what the fan-in and the drain observe.
- Only `TextChunk` contributes. `ThoughtChunk` and tool events do not (AC-1). No
  `ParentToolCallID` filter: the AC enumerates the classes it wants and a subagent
  is a prompt violation this ticket has not observed — a branch defending it would
  be a defence for a failure mode with no evidence.
- Accumulation is bounded at `sessions.MaxHandoffNoteBytes`, the same bound the
  store truncates at, so a runaway child cannot grow the buffer without limit. Past
  the bound further text is dropped, not the reply.
- `begin` refuses a second concurrent arm. A nil `*wrapUpCapture` refuses too, so a
  hand-built `streamRunner{}` is inert.
- The returned `stop` is idempotent and disarms without a turn end, for the
  deadline and error paths.

**2. `streamRunner` exposure.** One new pointer field and one method, so the
coordinator reaches the capture the way `contextUsageResolve` reaches
`QueryContextUsage`: an optional capability asserted at the consumer, never widened
onto `sessions.Runner`.

```go
func (s streamRunner) BeginWrapUp() (*wrapUpReply, func(), bool)
```

**3. `conversationReset` — the coordinator (new file `cmd/pyry/session_reset.go`).**

`contextUsageResolver`'s shape: a daemon-side coordinator over a base context, with
injected seams, that waits idle, makes one bounded round trip, and logs no content.

```go
type conversationReset struct { … }                       // base ctx, resolve, busy, backlog, notes, log, deadline
func (r *conversationReset) begin(convID string) (release func(), ok bool)   // AC-4 in-progress guard
func (r *conversationReset) wrapUp(convID string)                            // the routine; never fails the reset
```

Seams, each an interface defined at this consumer:

- `backlogDropper` — `Snapshot(convID) []msgqueue.QueuedMessage` + `Remove(convID, id) bool`.
- `handoffNoteStore` — `HandoffNote(id) (string, error)` + `WriteHandoffNote(id, text) (string, error)`.
- `wrapUpCapturer` — `BeginWrapUp() (*wrapUpReply, func(), bool)`.
- a resolve func answering the bound `*sessions.Session`, its runner, and the
  registry's own conversation id — built over `resolveBoundSession`, never keyed on
  the client's string.

`wrapUp`'s sequence, each step's position load-bearing:

1. **Drop the backlog first.** `Snapshot` then `Remove` each id, so msgqueue's drain
   cannot deliver a queued message into the idle window the wrap-up needs. `Remove`
   fires the change notify that republishes the emptied `queue_state`, so AC-5 needs
   no second publish. A refusal (`false`) is tolerated: past the commit gate that
   message is already going into the outgoing child, and AC-5 asks for the backlog
   to be dropped, not for `Remove` to be made total.
2. **Resolve on this goroutine**, not on the caller's — the binding can move across
   the hand-off, and the write surface is the `*sessions.Session`, which
   `activeSessionStarter.resolveBound` does not carry.
3. **Interrupt** via `interruptRunner`. The error is tolerated; `armNone` is inert.
4. **Wait idle** via `turnBusyTracker.WaitIdle`, bounded by the deadline. The nil
   check is this type's, the bargain `contextUsageResolver` already keeps.
5. **Arm the capture** *before* the write, so no chunk of the reply can precede it.
6. **Compose the prompt** — see below — and **mark the conversation mid-turn** with
   `openForDelivery` before `WriteUserTurn`, running the undo on a write error. That
   is `newInboundDeliver`'s ordering and its argument transfers verbatim: the
   tracker's opener feed is asynchronous, so a `send_message` arriving during the
   wrap-up would otherwise find the conversation idle and deliver into the turn.
7. **`sess.WriteUserTurn`** with the deadline-bounded context. The #1330 rotation
   gate is deliberately NOT armed yet — `startFreshRunner` arms it, and it runs
   after this returns — because this write must succeed.
8. **Wait for the reply** to its turn end, bounded by the same deadline.
9. **Write the note**, but admit it first. The reply is claude-authored text that
   crossed the subprocess boundary, and a note the read side will refuse is worth
   no more to the successor than an empty one — #2475's `handoffNoteSection`
   returns `""` for a note carrying a forged fence marker, so storing one silently
   destroys the previous note and hands the successor nothing. The same
   `sessions.FencedHandoffNote` that admits the *incoming* note therefore judges
   the *outgoing* one, and a refusal joins AC-2's list: previous note stands. What
   is stored is the raw reply, never the fenced rendering — the fence belongs to
   the composing site, and the store's contract is that it holds the note verbatim.
   Blank after trimming is subsumed by the same predicate, which refuses it first.
   `ErrHandoffNotesDisabled` or any other error → one content-free record, return.

Every refusal leaves the previous note standing and none of them fails the reset
(AC-2). The routine returns `void`: there is no failure a caller could act on.

**4. `StartNewSession`'s new arm.** Below every existing inert arm, so nothing about
#2099's reject set moves:

```
… inert arms unchanged …
if reset wired && the resolved runner has a live child:
        release, ok := reset.begin(convID)      // AC-4: a second frame is dropped here, with a record
        if !ok: return nil
        spawnDir, refused := resolveSpawnDir(…)  // still below every inert arm
        go { defer release(); reset.wrapUp(convID); startFreshRunner(…) }
        if refused: return &relay.RotatedWithoutWorkspaceError{…}
        return nil
… today's synchronous path, byte-for-byte …
```

The live-child test is `runner.State().ChildPID != 0`, the signal the named arm
already uses. Applying it to the bare cursor frame as well is deliberate: a bare
frame on a childless conversation keeps rotating exactly as #2099 promised, and a
bare frame on a live one gets the same wrap-up a named one gets — the operator
pressed the same control.

### Composing the wrap-up prompt, and the note that rides in it

The prompt is a fixed daemon constant carrying the ticket's draft wording. No
operator or client text enters it.

The previous note is **admitted before composition, not trusted** (AC-3). #2475
already built the admission for these exact bytes; a second predicate over the same
untrusted bytes is how the two drift apart. But `handoffNoteSection`'s lead sentence
is written for a *system prompt* and is wrong for a user turn, so the section cannot
be reused whole.

The seam is therefore cut below the lead: `internal/sessions` grows one exported
function and `handoffNoteSection` is rewritten over it, byte-identically.

```go
// FencedHandoffNote renders note between the handoff-note markers, or ("", false)
// when admissibleHandoffNote refuses it.
func FencedHandoffNote(note string) (string, bool)
```

One predicate, one fence, two leads: `handoffNoteSection` prepends
`handoffNoteLead`, and `cmd/pyry`'s composer prepends the fixed wrap-up prompt and
a `Previous handoff note` heading. The fence's guarantee — a note cannot forge a
marker line — carries across unchanged, because the markers are the same constants
the refusal tests against.

## Concurrency model

- **One goroutine per reset**, spawned by `StartNewSession` and owned by the
  `conversationReset`. It exits on the deadline, on the base (daemon) context, or on
  completion — whichever comes first — so daemon shutdown reaps it and a silent
  child cannot strand it. Every blocking step inside it is bounded by that same
  deadline context.
- **The flight runs under the daemon context, never a caller's.** `contextUsageResolver.fly`'s
  rule and reason: the caller here is a relay dispatch goroutine that returns
  immediately, so a caller-scoped context would cancel the wrap-up before it began.
- **The in-progress set** is a mutex-guarded `map[string]struct{}` keyed by the
  resolved conversation id. Check-and-insert happen under one lock acquisition, so
  two frames cannot both arm. `release` deletes under the same lock and is run from
  the goroutine's `defer`, so a panic cannot wedge the conversation permanently
  resetting.
- **The capture's arm/finish** is guarded by its own leaf mutex, taken on the
  stdout-forwarder goroutine (`Sink`) and on the reset goroutine (`begin`/`stop`).
  Never held across a call-out: `Sink` forwards to `next` outside it.
- **`wrapUpReply.done`** is the memory barrier, `contextUsageFlight.done`'s
  discipline: the text is written before the close and read after it.
- **Lock order.** The capture's mutex and the reset's mutex are both leaves and are
  never held together. `openForDelivery`/`WaitIdle` take the tracker's own lock,
  which this code never holds anything across.

## Error handling

| Failure | Answer |
|---|---|
| No resetter wired (PTY / unwired daemon) | Today's synchronous rotation, unchanged |
| Reset already in progress for this conversation | Dropped with a Debug record; no rotation (AC-4) |
| `Remove` refuses the committing head | Tolerated; that message is already in the outgoing child |
| Conversation unresolvable on the reset goroutine | Wrap-up abandoned, rotation still runs |
| Runner exposes no `BeginWrapUp`, or a capture is already armed | Wrap-up skipped, rotation still runs |
| `Interrupt` errors or is inert | Tolerated; the idle wait is the real gate |
| `WaitIdle` / write / reply wait hits the 90 s deadline | Reset proceeds, previous note stands (AC-2) |
| Reply empty, blank after trimming, or refused by `FencedHandoffNote` | Previous note stands (AC-2) |
| `ErrHandoffNotesDisabled` or any store error | One content-free record, previous note stands (AC-2) |
| `startFreshRunner` errors on the async path | Warn-logged by the goroutine — no return channel |

**No fragment of either note reaches a log line at any level** (AC-3). Every record
in the new code carries an event key, the resolved conversation id, and at most a
daemon-authored error. The store holds no logger by construction; the coordinator
holds one and never passes it note bytes.

**The `WriteUserTurn` error is recorded WITHOUT its value**, and that is the one
place this rule needs a named exception rather than a habit. The payload that call
carries IS the composed prompt, previous note included, and an error wrapping any
part of a rejected payload would put note bytes into a record through a channel no
reviewer would look for. So the failure is reported as an event key and the
conversation id and nothing else — `resolveSpawnDir`'s posture for its confinement
error, applied here for the same reason. The store's errors are the opposite case
and may carry their value: `handoff.go`'s wraps name paths and OS errors and are
documented to carry no note bytes.

## Testing strategy

`cmd/pyry/wrapup_capture_test.go`:

- The saturated-sink property, the one that justifies the placement: a capture
  behind a sink that refuses droppable events still sees every `TextChunk` — the
  shape `TestSessionResetFollower_ObservesPastASaturatedSink` established.
- Only `TextChunk` accumulates; `ThoughtChunk`, `ToolStart`, `ToolUpdate` do not.
- `TurnEnd` finishes the reply and disarms; a second `begin` then succeeds.
- A second concurrent `begin` is refused; a nil capture refuses.
- Every event of every variant is forwarded to `next` unchanged.
- Accumulation stops at `MaxHandoffNoteBytes`.
- `wait` returns on ctx cancellation without the turn ending.
- Run under `-race` with a producer goroutine driving `Sink`.

`cmd/pyry/session_reset_test.go` (table-driven over the reject rows above, with
fakes for each seam and an injected short deadline):

- Happy path, ordered: backlog dropped → interrupt → idle wait → prompt written →
  reply accumulated → note written. Asserted as a recorded call order, so "the
  backlog is dropped before the prompt is delivered" (AC-5) is a real assertion.
- Each AC-2 row leaves the previous note untouched and the routine returns.
- The composed prompt carries the previous note's bytes under the heading, inside
  the fence; a note the predicate refuses yields the bare prompt (AC-3).
- A reply carrying a forged fence marker is refused and the previous note stands —
  the write-side admission, asserted against the same predicate the read side uses.
- A log-capturing handler asserts no record at any level contains a substring of
  either note (AC-3), including on the write-failure row, whose fake returns an
  error quoting the payload back.
- `begin` admits once and refuses while in flight, and admits again after release (AC-4).

`cmd/pyry/new_session_starter_test.go` (extending the existing dispatch-arm table):

- A live child with a resetter wired: `StartNewSession` returns promptly, the
  wrap-up runs, and the rotation follows it — never before it.
- No resetter wired, or no live child: byte-identical to today's path, including the
  `RotatedWithoutWorkspaceError` reply and every #2099 inert arm.
- A second frame mid-reset rotates nothing.

`internal/sessions/systemprompt_test.go`:

- `handoffNoteSection`'s output is unchanged by the refactor for an admitted note,
  a refused note and an absent one.
- `FencedHandoffNote` refuses exactly what `admissibleHandoffNote` refuses, and its
  output plus `handoffNoteLead` reconstructs the section.

## Open questions

1. Should the wrap-up apply to the bare (cursor) `new_session` frame as well as a
   named one? **Resolved in this plan:** yes, gated on a live child, which preserves
   #2099's bare-path behaviour exactly.
2. Does `RotatedWithoutWorkspaceError` survive the move off the Run goroutine?
   **Resolved in this plan:** yes, by evaluating `resolveSpawnDir` synchronously
   before the hand-off. The semantic shift is recorded in § Design.
3. Whether to export the admission predicate or a composer from `internal/sessions`.
   **Resolved in this plan:** a composer (`FencedHandoffNote`), so the fence
   constants are named once.

## Security review

**Verdict:** PASS (re-run after two revisions; the first pass returned FAIL on the
logging finding below, and the plan was revised inline before this record).

**Findings:**

- **[Trust boundaries]** Three boundaries, each explicit and each a single
  function. (a) The client's `conversationID` — already discharged above the new
  arm by `conversations.ValidID` in `StartNewSession`; the arm sits below it, so the
  in-progress map is keyed by a canonical id on the named path and by the
  daemon-authored cursor id on the bare one. It is also armed only *after*
  `resolveBound` succeeded, so a conversation the daemon does not host can never
  mint an entry — the containment property `contextUsageResolver.flights` states,
  reached by resolution-precedes-arming rather than by canonical keying. (b) The
  previous note read off disk — admitted by `FencedHandoffNote` before composition,
  never trusted. (c) The child's reply — admitted by the *same* predicate before it
  is stored (revision 2, below). No fourth place judges these bytes.
- **[Trust boundaries]** MUST FIX → **fixed in this plan.** The first draft said
  "content-free records" but did not name the `WriteUserTurn` error, whose payload
  is the composed prompt with the previous note inside it. An error wrapping a
  rejected payload would carry note bytes into a log through a channel no reviewer
  would check. The plan now forbids logging that error's value outright; see
  § Error handling.
- **[Trust boundaries]** SHOULD FIX → **fixed in this plan.** The first draft stored
  the reply after a blank check only. A reply carrying a forged fence marker would
  be stored happily and then refused by `handoffNoteSection` at read time, silently
  destroying the previous note and handing the successor nothing. The write side now
  runs the same admission the read side runs; the verifier must check it landed.
- **[Prompt injection / subprocess]** No finding. Nothing reaches `exec.Command`,
  the environment or argv: the only new thing crossing into the child is a user
  turn on its stdin. The previous note's bytes ride inside the fence, which
  attributes everything between the markers to the note *by position*, and
  `admissibleHandoffNote`'s two independent refusals (line-anchored on the fence,
  whole-note on the exact tags) are what keep a note from forging its way out. The
  governing sentence the system-prompt path gets from `handoffNoteLead` is supplied
  here by the fixed wrap-up prompt itself — "keep what is still live and drop what
  is finished" tells the reader the note is material to prune, not instructions to
  obey — and it is composed *above* the heading, so it is read first.
- **[File operations]** Not applicable: this ticket derives no path. Every write is
  #2467's store, whose `handoffNotePathFor` re-gates the id on
  `conversations.ValidID` and hard-errors on anything else, so even the bare frame's
  un-shape-checked cursor id cannot place a file outside `handoff-notes/`. Atomic
  temp+fsync+rename, 0600 in a 0700 dir, `Lstat` not `Stat` — all unchanged.
- **[Tokens, secrets]** Not applicable: no tokens, keys or credentials are minted,
  stored or compared. The note is a fragment of the operator's own conversation and
  its confidentiality rests on the store's file modes, already shipped.
- **[Cryptographic primitives]** Not applicable: no randomness and no primitives.
  The fence is deliberately a fixed string, not a per-compose nonce —
  `handoffNoteFence`'s own doc argues that the line-start refusal already buys
  unforgeability and a nonce would cost byte stability.
- **[Network & I/O]** No new frame type, no new socket, no new read loop. Every
  input is bounded: the previous note by `MaxHandoffNoteBytes` at the store's read,
  the accumulated reply by the same value at the capture, the prompt by being a
  constant. The composed prompt is therefore bounded at ~16 KB plus a constant.
- **[Network & I/O]** OUT OF SCOPE — there is no cooldown after a *completed* reset,
  so a paired client can reset one conversation repeatedly, each frame costing one
  real wrap-up turn's tokens. The in-progress guard bounds concurrency to one per
  conversation and `ActiveCap` bounds live children, so this is a cost amplification
  rather than an exhaustion, and `new_session` already drives a respawn per frame
  today. A rate limit on the verb belongs with the sibling that gives the reset a
  client-visible lifecycle, not here.
- **[Errors, logs, telemetry]** Beyond the MUST FIX above: every new record carries
  an event key, the resolved conversation id — already logged unbounded on these
  arms — and at most a daemon-authored sentinel. No metrics are added.
- **[Concurrency]** No finding, one widened window named. The reset goroutine holds
  the runner and session id resolved at dispatch time and rotates with them up to 90
  seconds later, where the synchronous path held them for milliseconds. A rotation
  that won in between is already handled by machinery that exists:
  `Pool.RotateForNewSession` answers `ErrSessionNotFound` for a session that moved
  and `startFreshRunner` disarms the #1330 gate and returns the error — which its
  own doc calls "the ORDINARY way to land here". A concurrent rotation during the
  wrap-up likewise turns the write into `ErrNoLiveChild`, which AC-2 already
  tolerates. Check-and-insert on the guard is one lock acquisition; `release` runs
  from a `defer`, so a panic cannot wedge a conversation permanently resetting.
- **[Concurrency]** No finding on lifetime: every blocking step runs under a
  deadline context derived from the daemon base, so shutdown reaps the goroutine and
  a child that never answers cannot strand it — `contextUsageResolver.fly`'s bargain.
- **[Threat model alignment]** Cross-conversation isolation (#678) is preserved: the
  new arm sits *below* `resolveBound`'s `CurrentSessionID == ""` guard and never
  falls through to the bootstrap session, and the reset goroutine re-resolves through
  the same guard rather than trusting the dispatch-time value. The frame answers no
  existence oracle it did not already answer.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16

## Documentation handoff

Pending for the documentation stage — not touched by this ticket:

- `docs/protocol-mobile.md` § New session (v2): say that the frame runs the wrap-up
  turn first, and that the backlog is dropped rather than carried to the successor.
- The same file's changelog gains a dated entry for both.
