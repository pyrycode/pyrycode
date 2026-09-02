# Chunk intake driver (#1896)

`Intake` (`intake.go`) is this package's composite entry point: one type
sequencing `Admit`, `Deliver`, `EnsureDir` and `Store` behind two methods,
`Receive` and `ReleaseConn`, so the wire layer has one call and one set of
sentinels to map rather than four primitives to order itself. It was the
package's first production caller (see § "In-flight upload registry" and
the top-level overview) before it had one of its own: #1897 declared the
`AttachmentIntake` seam interface `Receive`/`ReleaseConn` satisfy, built the
production conversation resolver, and wired `internal/relay`'s
`appFrameWorker` as this package's first caller from *outside* it — see
[Inbound `attachment_chunk`](v2-session-manager-state-machine-inbound-attachment-chunk-attachmentintake-seam.md).
It also picked the two open sentinel→code mappings this package's doc blocks
left for it, `ErrUnknownUpload` and `ErrNoConversation` below — see
[Error codes § the seven `attachment.*` codes](protocol-package-constants-codes-go-error-codes-21.md)
for the choices and their retryability rationale.

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

`Registry` has nowhere to stash a per-transfer conversation id, and adding
one is a different slice — so `Receive` cannot resolve a conversation at
admission even if it wanted to. The forced consequence, accepted rather
than mitigated: a phone that switches conversations mid-upload files its
attachment under the *new* one, and an upload begun while the daemon is on
no conversation is refused only once its **last** chunk arrives, after
every byte has already been transmitted. Both are stated in `Intake`'s doc
block rather than engineered around.

The resolver is a **construction-time** dependency (`func() (conversations.ConversationID, bool)`), never a `Receive` parameter — which is what lets
the security property `docs/protocol-mobile.md` § Attachments states
(`attachment_chunk` carries no `conversation_id`, so a client cannot steer
bytes into another conversation) hold **structurally**: there is no
conversation field on `Receive`'s signature for a frame's value to reach,
on any path, rather than a runtime check that could be skipped by a future
edit.

A resolver answering `!ok`, a resolver answering `("", true)`, and a `nil`
resolver are collapsed into the same refusal (`ErrNoConversation`), not
handed to `EnsureDir` as `""`. `EnsureDir("")` would answer `ErrInvalidID`
— telling a client its *own* attachment id was malformed when the actual
fault is that the daemon has routed to no conversation at all.
`ErrNoConversation` is a distinct sentinel from `ErrInvalidID` for exactly
the reason `ErrTooManyUploads` is distinct from `ErrUploadTooLarge`:
retryability inverts. A malformed id never clears by itself; a daemon that
is on no conversation clears the moment it routes to one.

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
