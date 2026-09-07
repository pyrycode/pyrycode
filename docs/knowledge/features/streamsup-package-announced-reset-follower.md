# Following claude's announced reset — `sessionResetFollower` (#2135)

An in-process `/clear` is invisible to the daemon unless something connects claude's own
announcement (decoded as `turnevent.ConversationReset`, see [`conversation_reset` is consumed by
matching](streamsup-package-conversation-reset-consumed-by-matching.md)) to the pool re-key and the
sink tag. `sessionResetFollower` (`cmd/pyry/session_reset_follow.go`) is that connection: a fifth
decorator of the same shape as the four retention holds `newSessionParser` chains
([`session_model_hold.go`](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md)),
but **not** installed inside `sessionRetentions` — it retains nothing and exposes no accessor, it
*acts* on a runner-level tag and a pool callback that `newSessionParser` has no business knowing
about. The factory chains it at the tail instead, decorating `sink.sinkForTag(tag.ID)` and passed
as `newSessionParser`'s `next`.

## Why this position and no other

Two constraints bind at once, and only the parser's side of the fan-in channel satisfies both:

1. **Not behind the drain's active-session gate** ([draining turnevents](streamsup-package-draining-turnevents-into-the-interactive-emitter.md)), which drops every event whose producing session isn't the active conversation's bound session — a reset observed there would never rotate a *background* conversation.
2. **Upstream of the fan-in send.** `ConversationReset` is droppable class (`turnMarkFor`'s default arm, pinned by `TestTurnMarkFor_TotalOverEveryVariant` — not to be changed), so `sinkForTag` refuses it at `droppableCap` under load. Anything that reads the event back out of the channel loses the reset exactly when the channel is saturated.

`busy.observe`, which runs inside the drain loop on events the sink already admitted, satisfies (1) and fails (2). The parser-side decorator chain satisfies both, which is the same placement argument [`session_model_hold.go`'s own doc](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md) states for its four siblings: what puts a link upstream of the fan-in send is sitting on the parser's side of the channel, not where in the chain it sits.

## What `Sink` does on a reset

1. Read `old := tag.ID()` — the **live** id, never a construction-time one. A closure over `RunnerConfig.SessionID` would be right for exactly one rotation and fail-closed after that, since that field is documented as construction-fixed and does not mirror a `/clear` rotation.
2. `newID == old` → return untouched: no re-key, no transition, no tag rotation (mirrors `AdoptAnnouncedID`'s own equal-id guard — the pool cannot guard a value it doesn't own, so both sides carry the check).
3. `tag.Rotate(newID)` — **before** the pool call, not after. Rotating first closes the window that would otherwise open on every *successful* reset: the pool call's `notifyTransition` rebinds the conversation, and the rebind is what makes the old tag dead, so rotating the tag first means no goroutine can observe the rebind while the tag still reports the old id.
4. `pool.AdoptAnnouncedID(old, newID)` — the pool seam ([`sessions-package-key-types-adoptannouncedid.md`](sessions-package-key-types-adoptannouncedid.md)), reached through `rekeyPool` (below). Its answer decides whether the rotation stands.
5. **The unwind.** The tag ends on `newID` only when the pool's answer means the session is now on that id — `nil` (this call re-keyed) or `ErrSessionNotFound` (the rotation watcher won the race — see below). Every other answer means nothing moved, and the tag is rotated back to `old`.
6. **The runner follows (#2136).** On the far side of the unwind — never before it — `follow` calls `adoptRunner(newID)`, which reaches [`(*streamsup.Runner).AdoptSessionID`](streamsup-package-satisfying-sessions-runner.md): the runner's live spawn id, the value `beginSpawn` puts into the next child's argv and environment. A refused id now reaches the tag, the registry and the runner alike not at all; an accepted one moves all three.

Step 5 is the part that shipped wrong the first time and is why this file exists.

## The lesson: a refusal is not a control until the caller unwinds to match it

The first pass wrote step 3 as unconditional and step 5 as "rotate, call the pool, done" — correct
for `ErrSessionNotFound`, silently wrong for `ErrSessionIDTaken`. `AdoptAnnouncedID` returns that
sentinel *before* `rekeyLocked`, so nothing is re-keyed and no conversation is rebound — but the
tag had already moved onto an id belonging to a **different live session**. From there
`sinkForTag` stamps this runner's later events with that other session's id, the drain's
active-session gate admits them into *that* conversation whenever its cursor is active, this
runner's own conversation goes dark, and `exitForTag` mis-attributes the child's eventual exit.
One line on the supervised child's stdout moved a conversation's output to another conversation —
which is exactly the outcome `ErrSessionIDTaken` was added to the pool to prevent, re-entering by
the half of the seam the pool cannot reach.

The security review that shipped `ErrSessionIDTaken` audited the registry side of the collision —
correctly found that an unchecked `rekeyLocked` would overwrite another session's entry — and
stopped there. It never asked what the *caller's* own state does on that same refusal. Both halves
were needed, because the pool cannot reach the tag: a refusal it returns only *binds* if the
follower honours it.

The rule is now stated **positively over the pool's answer**, not negatively over a list of known
sentinels — "unwind unless the answer means we're on the announced id" rather than "unwind on
`ErrSessionIDTaken`" — so a sentinel added later to `AdoptAnnouncedID` is refused (tag unwound) by
default instead of silently treated as success. The generalisable form: **a refusal returned
across a package boundary is not a control until the caller's own state is unwound to match it**,
and an audit that checks only the refusing side has audited half a boundary. Anywhere else in this
codebase a caller holds a second piece of identity a callee can't see — a tag, a cache entry, a
in-flight marker — the same question is worth asking before trusting the callee's refusal alone.

`TestSessionResetFollower_PoolAnswerDecidesTheTag` pins both directions, one row per sentinel with
opposite expectations, and each row asserts the **consequence** — a later event fed through the
real `sinkForTag`, read back for the session id its envelope actually carries — rather than the tag
field directly, since a mis-tag is only harmful through what it stamps. A tag-field-only assertion
would have passed the very follower that shipped wrong.

## Extending the rule to the runner: one call site, not one per qualifying answer (#2136)

AC 3 of #2136 stated the runner half as a biconditional: the runner's id ends on the announced id
exactly when the tag does. `follow` already expressed the tag half across three returns and one
unwind (steps 2, 3+5, and the equal-id early return); replicating the runner adopt beside each of
the three qualifying returns would work today, but a sentinel added later to `AdoptAnnouncedID`
would need a fourth site remembered to match it — the same shape of defect the unwind above exists
to fix, one seam over.

Instead the pool half of `follow` was extracted into `rekeyPool(oldID, newID) error`, which answers
the one question both remaining steps need — does the session now stand on `newID`? — and collapses
to nil for all three adopting answers (no pool wired, `nil`, `ErrSessionNotFound`) and the pool's
own error otherwise. `follow` then has exactly one call to `adoptRunner`, downstream of `rekeyPool`
returning nil, so a sentinel added later is answered once and the runner and the tag can no longer
drift out of step by one call site being forgotten. The generalisable form: **a rule stated as a
biconditional over an answer wants one call site keyed to that answer, not one call site per
currently-known qualifying value** — the same lesson the unwind above teaches about auditing only
the refusing side, applied to the accepting side instead.

`TestSessionResetFollower_PoolAnswerDecidesTheTag`'s existing per-sentinel rows carry the runner
expectation too, derived from the same row's tag expectation rather than declared independently —
so the table asserts the biconditional itself, and a row added for a future sentinel gets the
matching runner expectation for free.

## The one window this trades for a bigger one

Rotating first and unwinding on refusal leaves one window: between the rotation and the unwind,
the tag reads `newID` while the registry is unchanged. It is strictly smaller than the alternative
(call the pool first, rotate only on a qualifying answer), which would instead open a window on
*every successful* reset spanning the registry save and the observer fan-out — the far more common
path. The refusal-path window is bounded to a lock acquisition and two map probes and closes
before any disk write; `exitForTag` running on the supervision goroutine is the only reader that
could observe it.

## Coexistence with the rotation watcher

Both `sessionResetFollower` and the [rotation watcher](rotation-watcher.md) rotate to the same
announced id when claude self-clears — a double fire is harmless by design: whichever loses the
race finds `oldID` already gone from the pool and gets `ErrSessionNotFound`, which `AdoptAnnouncedID`
answers with no mutation and no transition, keeping "exactly one `session_transition`" structural.
Retiring the watcher, now that this seam covers the same event faster and from the source rather
than from a filesystem CREATE, is tracked separately (#2137) — not done here.

## Not fed here

`turnBusyTracker` gets nothing from this path: `turnMarkFor` answers `turnMarkNone` for
`ConversationReset`, so there is nothing for `busy.observe` to act on. The forwarded event still
carries the **new** tag, since `sinkForTag` reads the tag after `Sink` returns — immaterial for
delivery (the drain has no arm for the variant either), but stated so a reader of a drop record
knows which id to expect.

See [`docs/specs/architecture/2135-follow-announced-reset.md`](../../specs/architecture/2135-follow-announced-reset.md) for the full design, concurrency model, and security review, and
[`docs/specs/architecture/2136-adopt-announced-session-id.md`](../../specs/architecture/2136-adopt-announced-session-id.md) for the runner-side extension.
