# Inbound new_session (#831, widened #2099) — `SessionStarter` seam

`new_session` is a v2 **control** envelope (phone → binary), intercepted in
`dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (beside
`TypeInterrupt` / `TypeDequeueMessage`) — there is **no** `dispatch.Route`
handler. **Since #2099** it carries an optional `NewSessionPayload{ConversationID
string}`: present and canonical, the daemon rotates *that* conversation's
session id and respawns its child; absent, empty, or a body that fails to
decode are one wire meaning — rotate the conversation the daemon's cursor
points at, the pre-#2099 behaviour verbatim. On the stream path this is a
**kill and respawn under a freshly minted session id**, never a `/clear`
keystroke — the terminal-era framing this doc previously carried is gone from
both the code and `docs/protocol-mobile.md` § New session (v2).
**`security-sensitive`**: `conversationID` is untrusted phone input and a
registry-validated **lookup key**, never authorization — the same posture
`request_attachment` and, since #2142, `attachment_chunk` publish (spec-stage
security review, verdict PASS; see [`codebase/831.md`](../codebase/831.md) and
the ticket's `docs/specs/architecture/2099-new-session-names-conversation.md`).

The frame stays **fire-and-forget with no reply**, matching `dequeue_message`'s
no-op on an unknown id — answering would make the verb an existence oracle over
conversation ids, which the merged-refusal design below is what actually
prevents.

## Trust boundary: the relay is a courier, `cmd/pyry` is the boundary

`internal/relay` imports neither `internal/conversations` nor
`internal/sessions` by design, so `handleNewSession` can neither shape-check
`ConversationID` nor resolve it against the registry — it decodes the payload
tolerantly (`_ = json.Unmarshal(env.Payload, &p)`; a decode failure leaves the
zero value, which is the cursor path) and passes the raw string straight to
`SessionStarter.StartNewSession(conversationID string) error`. Every check
lives in `cmd/pyry`'s `activeSessionStarter`, the sole implementation, which is
why the seam's own doc states the contract in the interface rather than
leaving a future caller to assume the string arrived validated.

Order in `handleNewSession` is load-bearing — capability gate before decode, so
a non-interactive conn's bytes are never parsed:

1. `!s.interactive` → inert (AC-4 negative path; a one-line check, not a shared
   abstraction, per CODING-STYLE over-DRY — `interrupt` / `dequeue_message`
   each keep their own).
2. `m.cfg.SessionStarter == nil` → debug `v2.new_session.inert` (foreground /
   pre-wire).
3. Tolerant decode, then `StartNewSession(p.ConversationID)`, best-effort: an
   error is `Warn`-logged (`v2.new_session.keystroke_err` — the event name is
   the one surviving `/clear`-era label, kept rather than churned for a string
   with no behavioural meaning) with `conn_id` + the sentinel only, never
   payload bytes.

## `activeSessionStarter.StartNewSession` — the resolution order is the design

The empty-string branch runs **before** `conversations.ValidID`, not after:
`ValidID("")` is false by the function's own doc, so reversing the two would
fail every un-upgraded client's bare frame — the shape `new_session` had from
\#831 until #2099 — and silently break backward compatibility. From there,
every ambiguous state is inert (no rotation, no respawn, no spawn of a child
that didn't exist, never a fall-through to the bootstrap session):

1. `conversationID == ""` → the pre-#2099 path, verbatim: rotate
   `currentConv()`'s conversation. The cursor's own id is daemon-authored and
   deliberately **not** shape-checked, since checking it would change behaviour
   on the one path this branch exists to preserve.
2. named but `!conversations.ValidID` → inert, never reaches the registry.
3. `resolveBound` (over `resolveBoundSession`) fails → inert. **Unknown id and
   known-but-unbound id share one debug record** (`v2.new_session.no_bound_session`):
   `resolveBoundSession` refuses both identically and returns `(nil, "", false)`,
   never falling through to `Pool.Lookup("")`'s bootstrap session (the #678
   isolation point). The non-distinction is deliberate, not an oversight — it
   is also what denies a hostile paired client an existence oracle.
4. bound runner exposes no `RestartFresh` → inert (`v2.new_session.no_restart`).
5. **named** conversation whose runner reports `State().ChildPID == 0` → inert
   (`v2.new_session.no_live_child`). Named-only: see below.

Every refusal logs at **debug**, because the id is client-supplied — matching
`handleDequeueMessage`'s posture for its own client-named `conversation_id`,
and deliberately not `activeInterrupter`'s Info (which records a
daemon-authored arm identity, not a client string).

### A capability probe answers "could", not "is" — the arm that shipped broken

Row 4 and row 5 look like one guard and are not, and conflating them is exactly
how AC-4's fourth case ("named conversation with no live child") shipped unmet
in the first PR (#2157) despite the plan reasoning it was free. `interface{
RestartFresh(string) }` is a **type** assertion — it asks what the runner
*can* do, and production's only implementation, `streamRunner`, answers yes
**unconditionally**: `RestartFresh` is exposed regardless of whether a child
has ever spawned, and `Pool.buildSession` assigns it at mint time. Since
\#2085, `create_conversation` binds a conversation's `CurrentSessionID` without
spawning a child — the spawn waits for the conversation's first message — so a
created-but-unmessaged conversation sailed through rows 1–4 with a real,
capable runner and reached `Pool.RotateForNewSession`, which checks only pool
membership. The result was observable damage with nothing to show for it: the
pool rekeyed, `sessions.json` persisted, the conversation rebound, and a
`session_transition` broadcast telling every client to render a delimiter for
a chat that had never had a turn, while `RestartFresh` itself spawned nothing.

The fix is a second, **runtime-state** guard below the capability probe:
`State().ChildPID == 0` (unstarted, backing off, evicted, and stopped all read
0 — every state with nothing to restart fresh; a spawn that started but has
not yet published its pid also reads 0 and is refused, which is the same
fail-safe direction the whole reject set takes, and the frame is re-sendable).
**Generalizes:** a "can this be done" interface assertion and a "has this
already happened" state read are different questions, and a reject set that
needs the second will pass every test that only drives the first.

**The test double is what let the wrong conclusion stand, so it changed too.**
The original `TestActiveSessionStarter_InertArms` row for "no live child"
injected a fake lacking `RestartFresh` entirely — a shape production never
produces for a bound conversation — so it exercised row 4, not row 5, and
greened on a fiction. `restartFreshRunner` (the test double) now offers
`RestartFresh` **always** and carries liveness in `State()`, exactly as
`streamRunner` does, splitting what had been one table row into two: a
capability row (`baseRunner{}` → `no_restart`) and the actual liveness row (a
capable runner with `childPID == 0` → `no_live_child`). **Generalizes:** a test
double that omits a method the production type always provides doesn't just
under-test — it can make the wrong arm look green. When a reject set has two
adjacent refusals, the double must be able to reach both.

### The no-live-child guard is named-only — a known, accepted asymmetry

AC-3 requires the bare (cursor) frame to keep rotating an evicted conversation
exactly as it did before #2099; AC-4 requires a *named* childless conversation
to be inert. A guard applied to both paths would buy the second at the cost of
the first, so `named && runner.State().ChildPID == 0` is scoped to the frame
half this ticket introduced. `TestActiveSessionStarter_UnnamedFollowsTheCursor`
drives a runner with **no** live child specifically to pin the bare path still
rotates it; both directions are mutant-verified (dropping the guard reddens
only the AC-4 row; dropping the `named &&` conjunct reddens only this pin).

The accepted cost: `Pool.Lookup` is plain map membership, so an **idle-evicted**
session stays in `p.sessions` with its conversation still bound — the same
`ChildPID == 0` shape as a never-messaged one. A bare frame on that
conversation still rotates it and it comes back up under the fresh id; the
identical conversation addressed by **name** is now inert. Both halves are
individually published (`docs/protocol-mobile.md` § New session states the
inert set, and separately promises the absent-field path is the pre-#2099
behaviour verbatim), but the document does not say the two disagree on this
one state, and pyrycode-desktop#1087 / pyrycode-mobile#625 always name their
conversation — so on a chat idle long enough to evict, **New session** becomes
a silent no-op with no reply to explain it, where the bare frame would have
worked. Flagged at PR review (verdict PASS; a should-fix for the ticket owner,
not a defect) and left as shipped rather than re-litigated here.

**Answered by #2103, for `interrupt`: no asymmetry arises there, and no guard was
added.** `new_session`'s guard exists because `RestartFresh` on a childless runner
mutates before it can discover there is nothing to restart; `interrupt`'s actuation
(`WriteInterrupt`) checks for a nil writer first and returns `ErrNoLiveChild`
having written nothing, so it is already inert by construction on both the named
and bare paths — adding a guard would have introduced the asymmetry rather than
closed it. See [interrupt § No liveness
guard](v2-session-manager-state-machine-inbound-interrupt-interrupter-seam-esc.md#no-liveness-guard--a-blockers-late-fix-is-not-automatically-the-twins-requirement)
for the generalized rule this ticket pair settled: a guard copied from a sibling
is warranted only when the sibling's actuation can leave damage behind with
nothing to show for it, not merely because the sibling needed one.

## Log-bounding: only the invalid-shape arm needs it

Every arm past `conversations.ValidID` logs a string already known to be 36
bytes. The shape-check-failure arm is the **only** place an arbitrary,
attacker-controlled string reaches a log call, bounded upstream only by the
application-envelope byte cap. `boundedConvID` (`cmd/pyry/main.go`) renders it
as-is under 64 bytes or a **copied** prefix plus an elision marker otherwise —
the copy matters as much as the truncation: `s[:n]` on the decoded payload
would pin the whole frame's backing array in a buffered log record, and an
unmarked truncation would read as a complete id and send a reader chasing a
conversation that was never named. `handleDequeueMessage` has the identical
unbounded exposure today and was the thing this would have copied verbatim had
it not been caught in security review.

## Multi-phone / scoping

Any interactive paired phone can send `new_session`; naming a conversation
widens *which* conversation one frame can reach (from "the cursor's" to "any"),
not the trust boundary — a hostile-but-paired device could already reach any
conversation via a two-frame dance (route a message to move the cursor, then
send the bare frame). The named form removes that dance, which is also the
misfire a *benign* client could not previously avoid: opening a chat without
sending is the desktop sidebar's normal case, and the old bare frame restarted
whichever conversation the shared cursor pointed at. The cursor itself is
**never written** by any of this — only a successful `sessionRouter.Route`
stamps it — so a named rotation leaves the active conversation exactly where
it was.

## Related

- [`v2-session-manager-state-machine-inbound-dequeue-message-queueremover-sea.md`](v2-session-manager-state-machine-inbound-dequeue-message-queueremover-sea.md)
  — the tolerant-decode / debug-no-op shape this handler mirrors.
- [`v2-session-manager-state-machine-inbound-interrupt-interrupter-seam-esc.md`](v2-session-manager-state-machine-inbound-interrupt-interrupter-seam-esc.md)
  — `activeInterrupter`'s logger-field precedent, and #2103, which gave
  `interrupt` the identical optional `conversation_id` and settled the
  cursor-vs-named liveness-guard question this doc raised (answer: no guard,
  no asymmetry — `interrupt`'s actuation refuses without writing, unlike
  `RestartFresh`).
- [e2e-harness.md § Build Helper](e2e-harness.md#build-helper) — the
  `-overlay` mutation-testing pitfalls this ticket's verification hit again,
  in a new shape: see that section's note on `go test` caching a daemon-spawning
  suite even with the source edited in place.
- [fakephone-harness.md § Library trade-off](fakephone-harness.md#library-trade-off-timed-out-receive)
  — why the e2e case asserts "nothing rotated" from the daemon's log rather
  than by waiting on a `Receive` that will never arrive.
