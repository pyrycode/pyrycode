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

> **SUPERSEDED — see § Revisions, 2026-09-16.** The paragraph below is left as
> written because it is the reasoning the first implementation was built on and the
> verifier's MUST FIX answers it directly. What shipped: the wrap-up arm returns
> `nil`, and the workspace refusal is recorded below a rotation that actually
> returned rather than replied at dispatch time.

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
        go { defer release(); reset.wrapUp(convID)
             if startFreshRunner(…) != nil: Warn rotate_failed; return
             if refused: Warn workspace_refused }   // revised 2026-09-16
        return nil                                  // revised 2026-09-16: never #2443's reply
… today's synchronous path, byte-for-byte …
```

The two `revised` lines are the verifier's MUST FIX; § Revisions holds the
argument. The first draft returned `&relay.RotatedWithoutWorkspaceError{…}` here,
which asserts a rotation that has not happened yet and may not happen at all.

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
| Recorded workspace refused on the async path (revised 2026-09-16) | Warn-logged by the goroutine, below a rotation that returned; no client reply — § Revisions |

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
   **Answered "yes" in this plan and RE-RESOLVED TO "no" — see § Revisions,
   2026-09-16.** The original answer, kept here as the record of what was tried:
   yes, by evaluating `resolveSpawnDir` synchronously before the hand-off, with the
   semantic shift recorded in § Design. That shift was not a softening the type
   permits — its doc states the completed rotation as a precondition and forbids the
   value outright when the rotation failed — so the arm now withholds the reply and
   records the refusal below a rotation that returned.
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

## Revisions

### 2026-09-16 — Open Question 2 re-resolved: the wrap-up arm withholds #2443's reply

**Driven by:** the verifier's MUST FIX on PR #2482.

**What was wrong.** The first implementation evaluated `resolveSpawnDir`
synchronously and returned `*relay.RotatedWithoutWorkspaceError` from
`StartNewSession`'s wrap-up arm, and § Design recorded the shift as a semantic
softening — "the rotation has been *committed to* rather than *completed*". That
reading was too generous to be true. `RotatedWithoutWorkspaceError`'s own doc does
not state the completed rotation as a connotation but as a precondition with a
tense — the pool re-keyed, the conversation rebound, the `session_transition`
already broadcast "by the time this value exists" — and then forbids the value
outright for a rotation that failed, as "a lie the client cannot check".
`handleNewSession` repeats it: exactly one outcome replies, a rotation that
*completed*.

The plan's justification — the rendered outcome is identical — holds only when the
rotation succeeds, and the prohibition is about the case where it does not. That
case is not hypothetical here. `startFreshRunner`'s own doc calls losing the race
to a concurrent `new_session` "the ORDINARY way to land here", this ticket
*widens* that race window from milliseconds to ninety seconds (§ Security review,
Concurrency, already said so), and the implementation ships the arm that logs it.
So the arm was returning a claim that was unproven at the moment it sealed and
routinely false thereafter.

**What now happens.** The wrap-up arm returns `nil`. `resolveSpawnDir` is still
evaluated synchronously — that placement is about containment, keeping it below
every inert arm and below the in-progress guard so a repeated frame cannot drive
`MkdirAll` — but its refusal now travels into `resetThenRotate` and is recorded
there, *below a rotation that actually returned*, as
`v2.new_session.workspace_refused`. The record inherits the reply's precondition
rather than merely replacing it: a rotation that failed logs `rotate_failed` and
says nothing about the workspace.

**Why withholding rather than deferring.** Deferring is the better answer and this
ticket cannot reach it. `SessionStarter.StartNewSession` is synchronous and
`handleNewSession` seals its one reply from the return value, so a late reply needs
a channel the async tail does not have. Minting one here would mean inventing a
client-visible outcome for the reset, which is the sibling's subject — the
`resetting` status frames that give the reset a lifecycle. Withholding is the
honest option available today: the operator still learns of the refusal from the
daemon log, and the client still learns the rotation happened from the
`session_transition` broadcast. What it loses is the coded reply on the wrap-up
path, and a shipped behaviour lost is strictly better than a shipped behaviour
made false.

**Pinned by** `TestActiveSessionStarter_WrapUpArmWithholdsTheWorkspaceReply` (the
arm answers nil, and the refusal still reaches the log) and
`TestActiveSessionStarter_RefusedWorkspaceIsNotRecordedWhenTheRotationFails` (a
failed rotation reports the failure and makes no workspace claim).

### 2026-09-16 — the idle wait and the mid-turn mark are pinned

**Driven by:** the verifier's SHOULD FIX on PR #2482.

Every fixture left `conversationReset.busy` nil, so `WaitIdle`, the
`reset.wrapup.not_idle` arm, `openForDelivery` and its `undo` never ran under test
— the two steps § Design calls load-bearing were the two with no coverage, and the
happy path's asserted order was missing the idle wait § Testing strategy named.

A **real** `turnBusyTracker` is now wired into `newResetFixture` on every row
rather than opted into, and the capture's `next` forwards into its `observe`, so
the fixture is production's chain in miniature: the wrap-up turn's own `TurnEnd`
clears the mark `openForDelivery` placed. Four rows added —
`WaitsForTheTurnToEnd` (the prompt follows the turn's end, asserted as call order
with an asynchronous idle arriving from the interrupt),
`AbandonsAConversationThatNeverGoesIdle` (the `not_idle` return: nothing written,
previous note standing), `MarksTheConversationMidTurn` (the mark stands at the
instant the payload moves, and does not outlive the turn), and
`UndoesTheMarkWhenTheWriteFails`. `WithoutATrackerStillDelivers` pins the nil
branch that was previously the only one reachable. All five were mutation-checked:
removing the wait, the mark, or the undo reddens the row that names it.

### 2026-09-16 — Open Question 2 re-resolved AGAIN: the reply is DELIVERED LATE, not withheld

**Driven by:** the verifier's regression finding on PR #2482 — `make check` red at
the e2e tier, `TestRelayV2_NewSessionRefusedWorkspaceRepliesToRequester` failing
with `error reply seen = false (AC-1), session_transition seen = true (AC-2)`.

**What the previous revision missed.** Withholding the reply was defended above as
"a shipped behaviour lost is strictly better than a shipped behaviour made false",
and that trade was weighed without noticing that the lost behaviour *has an e2e
pin*: #2443's AC-1 is asserted end-to-end against a real spawned daemon, and the
scenario it drives — a named frame, a live child, a resetter wired — takes the
wrap-up arm. The branch shipped two contradicting pins, a unit test asserting the
arm answers nil and an untouched e2e test asserting the client gets the reply on
the same path. Retiring another ticket's acceptance criterion is not a builder's
call, and it certainly is not one made by not noticing it.

**What was actually wrong with the reasoning.** Both previous revisions accepted
the seam's synchrony as fixed and then argued about which of two bad answers to
give inside it. The real constraint was never "the reply must seal at dispatch
time" — it was that `SessionStarter.StartNewSession(conversationID string) error`
is the only shape the seam has, and it carries no frame id to correlate a late
reply on. That is a seam limitation, not a protocol one, and this package already
solved the identical problem once: #1491's debug bundle assembles off Run and
funnels its outcome back through `V2SessionManager.bundleReady`, where
`handleBundleReady` streams it *or sends the deterministic error reply* — "so
every `s.send.Encrypt` stays on the single-owner Run goroutine". An off-Run
producer that owes a reply is a shape this manager already has.

**What now happens.** The seam grows an OPTIONAL widening, asserted at
`handleNewSession` and falling back to today's method when absent:

```go
type LateSessionStarter interface {
        SessionStarter
        StartNewSessionLate(conversationID string, outcome func(error))
}
```

`outcome` is called exactly once with the same value `StartNewSession` would have
returned — synchronously on every arm but the wrap-up one, which calls it from the
reset goroutine *after* `startFreshRunner` has returned. So
`RotatedWithoutWorkspaceError`'s precondition is not softened, deferred or
narrated around: by the time the value exists the pool is re-keyed and the
`session_transition` is broadcast, which is exactly what its doc fixes as the
tense. A rotation that failed reports the plain error and makes no workspace claim,
as it always did.

The closure the manager passes is a **blocking send with escapes** onto a new
`newSessionDone chan newSessionResult`, buffered at `wakeBufferSize` — the
`assembleBundle` hand-off verbatim, with `s.done` and the run context as the two
escapes. Run's new arm calls `handleNewSessionDone`, which drops a result whose
session is no longer `V2StateOpen` (`handleBundleReady`'s staleness guard, for its
reason: sealing under a dead session burns a send-nonce for a frame no live peer
awaits) and otherwise hands it to `answerNewSession` — arms 4 and 5 of today's
handler, extracted so the synchronous and late paths discriminate in ONE place
rather than in two that can drift.

**`cmd/pyry` keeps one implementation of the arms**, not two. `start(convID,
outcome) (err error, deferred bool)` holds every arm; `StartNewSession` calls it
with a nil outcome and returns `err`, `StartNewSessionLate` calls it with the real
one and reports `err` itself unless the arm deferred. A nil outcome is a real
state — the PTY posture and every test literal — and on the wrap-up arm it means
b4c850cc's behaviour exactly: rotate asynchronously, answer nothing, record the
refusal below a rotation that returned. Nothing about the inert arms or #2099's
reject set moves.

**Why not the other two directions the review named.** Keeping the wrap-up
synchronous only when the workspace was refused makes whether a conversation gets
a handoff note depend on an unrelated filesystem error — the ticket's entire
deliverable, disabled by a stale symlink. Retiring the e2e assertion needs
Juhana's sign-off and would trade a shipped, proven client behaviour for an
implementation convenience. This direction is the one the review called "the only
direction that keeps both contracts whole", and the cost is one optional interface
in a package that already has the machinery behind it.

**Pinned by** `TestActiveSessionStarter_WrapUpArmDeliversTheWorkspaceReplyLate`
(the refusal arrives through `outcome`, and only after the rotation returned),
`TestActiveSessionStarter_LateOutcomeIsPlainErrorWhenTheRotationFails`,
`TestActiveSessionStarter_SyncSeamStillWithholdsOnTheWrapUpArm` (the nil-outcome
arm is unchanged), `TestV2Session_NewSession_LateRefusalRepliesToRequester`,
`TestV2Session_NewSession_LateOutcomeAfterConnClosedIsDropped`, and the e2e pin
that found this, `TestRelayV2_NewSessionRefusedWorkspaceRepliesToRequester`,
which passes as written.
