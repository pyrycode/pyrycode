# Chunk intake driver (#1896)

`Intake` (`intake.go`) is this package's composite entry point: one type
sequencing `Admit`, `Deliver`, `EnsureDir` and `Store` behind two methods,
`Receive` and `ReleaseConn`, so the wire layer has one call and one set of
sentinels to map rather than four primitives to order itself. It was the
package's first production caller (see § "In-flight upload registry" and
the top-level overview) before it had one of its own: #1897 declared the
`AttachmentIntake` seam interface `Receive`/`ReleaseConn` satisfy, built the
production conversation resolver (retired by #2143 — see § "The conversation
is resolved once, on the completing chunk only" below), and wired `internal/relay`'s
`appFrameWorker` as this package's first caller from *outside* it — see
[Inbound `attachment_chunk`](v2-session-manager-state-machine-inbound-attachment-chunk-attachmentintake-seam.md).
It also picked the two open sentinel→code mappings this package's doc blocks
left for it: `ErrUnknownUpload` below, and `ErrNoConversation`, deleted by
\#2143 along with the resolver that raised it — see
[Error codes § the seven `attachment.*` codes](protocol-package-constants-codes-go-error-codes-21.md)
for `ErrUnknownUpload`'s retryability rationale.

`Intake` **owns** its `Registry` rather than taking one, and that ownership
is a security property, not an ergonomics choice: `maxInFlightUploads` is a
daemon-wide ceiling, so one `Intake` per daemon is the only configuration
under which that bound means what it says. An injected registry would
publish a second way to reach the same daemon-wide budget from two places.

## The fork: `Lookup` first, `Deliver` fed the same chunk

`Receive` reads `Lookup(connID, chunk.AttachmentID)` and admits only on a
miss, rather than calling `Deliver` and falling back to `ErrUnknownUpload`.
The second shape looks equivalent and is not: it reinterprets a refusal
sentinel as a routing decision, which turns a genuinely-gone transfer (a
reaped pair, or one released by a raced `ReleaseConn`) into "admit it fresh"
by way of an `errors.Is` check on the wrong axis, and it costs an extra
registry pass on every first chunk. `Lookup`'s own doc names this dispatch
site as its reason to exist — a pure, non-stamping, non-reaping read built
for exactly this fork.

**Admission never reads `chunk.Index`.** The published attachment contract
(`docs/protocol-mobile.md` § Attachments) states chunks may arrive in any
order — deliberately weaker than `debug_bundle_chunk`'s strict `seq`, named
there as the rule a reader would wrongly copy. A fork gated on
`index == 0` refuses a conforming client whose opening chunk carries a
different index; this was the sole red of the mutant built to check it.

**`Admit`'s returned accumulator is discarded; `Deliver` is called with the
same chunk, looking the pair back up.** Feeding the returned accumulator
directly bypasses every release `Deliver` owns and re-opens the lockout for
single-chunk transfers — measured as a mutant that reddened both storing
tests and both no-conversation rows, more broadly than the single sole-red
the spec predicted for it.

## Three answers, not two

`ErrIncomplete` is `Deliver`'s answer for "accepted, not yet complete," and
it neither latches nor discards — it is what most chunks of a healthy
upload get. `Receive` is the one place in this package that *interprets*
that sentinel rather than passing it on: `err != nil` is the caller's whole
refusal test, and `stored` (the second return) separates a completed upload
from an accepted-but-incomplete one, so #1897 never has to test for a
particular sentinel to decide what to emit. Every other refusal crosses
`Receive` untouched — no `%w`, no annotation — so `errors.Is` still reaches
each layer's own sentinel.

## The conversation is resolved once, on the completing chunk only

`Registry` has nowhere to stash a per-transfer conversation id, so `Receive`
still only *consumes* the conversation on the completing chunk, when it calls
`EnsureDir`/`Store` — unchanged since #1896. What #2143 changed is where that
value comes from and when it is validated.

Through #2142, `Receive` took no conversation argument at all: `NewIntake`
closed over a construction-time resolver (`func() (conversations.ConversationID,
bool)`) reading the daemon's follow-active cursor, and the doc block argued
this made the security property `docs/protocol-mobile.md` § Attachments states
(a client cannot steer bytes into another conversation, because
`attachment_chunk` carries no `conversation_id`) hold structurally rather than
by a runtime check. A resolver answering `!ok`, one answering `("", true)`, and
a `nil` resolver all collapsed into one sentinel, `ErrNoConversation`, rather
than reaching `EnsureDir` as `""` — which would have told a client its *own*
attachment id was malformed when the real fault was that the daemon had routed
to no conversation. That resolver could only name a conversation a
`send_message` had already routed to, so an attachment added before a
conversation's first message could never be stored, and *send in A, send in B,
return to A and attach* silently filed the bytes under B — #2143 exists to fix
both.

\#2143 deleted the resolver, `ErrNoConversation`, and the `conversation` field
together — there is no other producer of that sentinel left to re-scope.
`Receive` now takes `conversationID string` as a per-transfer parameter and
validates none of it itself: that precondition belongs to the caller, in
exactly the words `V2SessionConfig.HistoryPage` already uses for its own
caller. `internal/relay`'s `handleAttachmentChunk` discharges it by gating
every `attachment_chunk` through the `KnownConversation` membership seam
**before** the frame reaches this package (see
[Inbound `attachment_chunk`](v2-session-manager-state-machine-inbound-attachment-chunk-attachmentintake-seam.md))
— run once per chunk rather than once per transfer, so a transfer naming an
unusable conversation is refused on its first chunk instead of after every
byte has crossed the wire. `KnownConversation` is a pure membership check,
which matters here specifically: a session router additionally refuses a
known conversation with no bound session, and a never-messaged conversation
is exactly the case this ticket exists to accept.

The one consequence #2143 deliberately left standing: since the destination
is still only *consumed* on the completing chunk, a phone that switches
conversations mid-upload still files under the *new* one — now
registry-validated rather than cursor-read. Closing that needs the registry
to record a destination at admission, which is #2146's.

**`EnsureDir`'s `conversations.ValidID` check moved from belt to backstop.**
Before #2143 the conversation id it validated always came from the daemon's
own trusted cursor, so the check was defense in depth. Since #2143 it is the
client-asserted id off the wire, one layer below the `KnownConversation`
gate — so it is now the deterministic barrier standing between a remote
string and a path component if that gate is ever removed or miswired, not a
redundant second opinion on a value the daemon already trusted.

**Two conns sharing one `attachment_id` in one conversation collide
last-writer-wins, atomically** — see § "Writing attachment bytes" above for
the doc-comment assumption this falsifies. Not a privilege boundary: both
conns are already-paired devices of one account, and `attachment_id` is
published as not-a-capability.

## The gap the package overview parked on `Deliver` is only partly closed

Code review on #1784 found `Deliver`'s never-log claim unpinned: a
`fmt.Errorf` wrap interpolating the client's `attachment_id` passed green
across all 147 of `Deliver`'s own tests, because the AC 5 fixture there
asserts sentinel identity via `errors.Is`, which a wrap still satisfies.
\#1896 rolled the equivalent assertion (`assertNoBannedStrings`) into every
refusal row `intake_test.go` drives through `Receive` — a sole red on the
same class of wrap, this time via `TestIntake_Refusals_ComeBackAsTheirOwnSentinel`. That closes the property **at this seam**, wherever `Deliver` is
reached through `Intake`. It does **not** close the property in
`Deliver`'s own suite (`registry_test.go`) — nobody added the three lines
there, so the identical wrap measured directly against `Deliver`, bypassing
`Intake`, is still unmeasured. See § "In-flight upload registry" above for
the fuller note. Lesson: a new caller's own fixture can retire a
package-overview-parked gap's *symptom* without retiring the gap at the
layer where it was originally parked — check which layer a new test
actually measured before crediting it against an older doc's TODO.

## Mutation testing: three sole reds, three over-determined

All six of the spec's mutants ran red under `go test -overlay`. Three were
sole reds as predicted (the index-gate fork, the no-conversation refusal,
and the banned-string wrap). Three were over-determined — caught by
multiple tests rather than the one predicted — because an incomplete chunk
is fed by almost every fixture in this suite, and storage is reached by
every completing one, so a mutant on either path is broadly caught by
construction. Over-determination here is not a gap: each mutant's design
decision is still pinned by a *named* test even where other tests also
happen to redden.
