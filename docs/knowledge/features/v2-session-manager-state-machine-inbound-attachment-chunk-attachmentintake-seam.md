# Inbound `attachment_chunk` (#1897, #2146) — `AttachmentIntake` seam

`attachment_chunk` is a v2 **control** envelope (phone → binary), intercepted
in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` — the
same boundary every other control arm uses. Unlike those, its work does not
stay on `Run`: it is the second arm (after `debug_bundle`, #1491) to run off
it, and the first to do so through the existing `appFrameWorker` queue rather
than a per-request goroutine — see [Concurrency](v2-session-manager-concurrency.md)
for the `appFrameJob` tagging that makes that possible and the routing-drift
reasoning behind keeping the decision in exactly one place. `security-sensitive`:
an inbound untrusted frame drives disk writes through
[`internal/attachments`](attachments-package.md) over the internet-exposed
relay (spec-stage security review, verdict PASS).

## The three-way answer is not a two-way answer

`AttachmentIntake.Receive(connID, conversationID string, chunk) (attachmentID string, stored bool, err error)`
is `attachments.Intake.Receive` (see
[Chunk intake driver](attachments-package-intake-driver.md)) behind a
consumer-declared seam, alongside `ReleaseConn`. The trap `Receive`'s own doc
names is real at this call site too: **`err != nil` is not "did this chunk
fail"**, because most chunks of a healthy upload return `("", false, nil)` —
accepted, waiting for more — which is a distinct outcome from both the
completing chunk (`stored == true`) and a refusal. A handler that answers
every non-error return is wrong in the other direction: the accepted-but-
incomplete case gets **no reply at all**, not an acknowledgement. Answering it
would mean one `noise_msg` per chunk of every upload, which the wire contract
never asked for.

A decode failure is rejected outright (`attachment.invalid_chunk`), never
tolerated the way `handleModalCancel`/`handleModalAnswer` tolerate one — see
[Inbound question control](v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md#a-tolerant-decode-is-safe-only-for-a-flat-payload)
for why a payload nesting a slice (`AttachmentChunkPayload`'s `data`) can't
safely reuse that idiom, and for the type-mismatch-not-garbled-bytes shape any
fixture for this class of failure has to take.

## The sentinel → wire-code map picked one open code, then #2143 retired it

\#1897 picked the two sentinel→code mappings left open by earlier doc blocks,
both by folding into the existing seven rather than minting new vocabulary:
`ErrUnknownUpload` → `attachment.invalid_chunk` (a transfer that expired or
was released, not a malformed chunk, but the closest existing class — "no
transfer to place this chunk against"), and `ErrNoConversation` →
`attachment.storage_failed` (a verified upload with nowhere to file it,
clears the moment the daemon routes to a conversation). See
[Error codes § the seven `attachment.*` codes](protocol-package-constants-codes-go-error-codes-21.md)
for `ErrUnknownUpload`'s full retryability reasoning — it generalizes past
this ticket and is recorded there, not duplicated here. No code answers no
arm: an unmapped error still gets `attachment.storage_failed` rather than
being dropped.

`ErrNoConversation`'s mapping did not survive #2143. The sentinel and the
cursor resolver that raised it are both gone — see
[Chunk intake driver](attachments-package-intake-driver.md) § "The
destination is fixed by the transfer's first delivered chunk" — and its
condition is refused one step earlier, in this handler, before `Receive` is
even called.

**#2146 answered a third bad-destination case without touching this
handler.** `attachmentRejectFor`'s two arms above refuse before the seam —
an absent id, and one naming a conversation this daemon does not host. A
transfer already admitted under one conversation whose *later* chunk names a
different, equally valid one cannot be caught here: the gate runs once per
chunk and both ids pass it. That case is caught one layer down, inside
`Registry.lookupAndStamp`, and answers `ErrConversationMismatch` through the
same existing `attachment.invalid_chunk` arm as the two above — no arm was
added and no code was minted, so the wire is unable to tell the three causes
apart, which is the posture #2143 chose so the upload leg is not a
conversation-existence oracle.

## The conversation gate runs before the seam, on every chunk

`handleAttachmentChunk` gates `chunk.ConversationID` in two ordered steps
before it reaches `AttachmentIntake.Receive`, copying
`handleRequestAttachment`'s ordering and its two-reasons-behind-one-code
shape: empty (`""`) is refused first, then a non-empty id is checked against
`KnownConversation`. Both arms answer the same wire code,
`attachment.invalid_chunk` — not `attachment.storage_failed`, whose published
row means a *verified* attachment could not be written to the host, which is
not true of a chunk refused before verification has even started. The two
causes are distinguished only in the daemon's own log, by a daemon-authored
`reason` string chosen at the call site; there is no third arm that falls
back to the follow-active cursor.

Because the gate lives in the handler rather than in `Intake`, it runs once
per **chunk**, not once per transfer — a transfer naming an unusable
conversation is refused on its first chunk, before any upload slot is
consumed and before any byte is admitted, rather than after every byte has
crossed the wire and the completing chunk finally reaches `EnsureDir`. The
conversation id is not logged on either refusal arm, matching
`handleRequestAttachment`: it has not passed the gate, so it is not yet
treated as safe to log.

## Teardown releases what the worker might still be holding

`closeWith` calls `AttachmentIntake.ReleaseConn(s.connID)` in the same per-conn
cleanup cluster that closes `s.done` and deletes the conn's `m.queues` entry —
nil-guarded, so every non-production construction site keeps compiling. This
runs on `Run` while the conn's `appFrameWorker` may still be inside `Receive`
for the same conn; it's safe only because `ReleaseConn` is documented as
remove-only under the registry's own lock, touching no accumulator. Without
this call in `closeWith`, a phone that drops mid-upload would hold daemon-wide
upload capacity until the idle reaper's timeout — a capacity-exhaustion gap,
not a tidiness one.

## Logging: the accepted-incomplete path stays at Debug

The loggable set is the union of `AttachmentChunkPayload`'s SECURITY block and
`docs/protocol-mobile.md` § Attachments: conn id, attachment id, index, total
— never bytes, filename, digest, or a host path. `err` itself is never
interpolated, replied, or logged on any arm, because `Receive`'s refusals wrap
host paths and the daemon's own conversation id (`EnsureDir`/`Store`'s
errors) — every reply carries a static per-code message instead.

One risk accepted rather than closed: `attachment_id` is client-chosen and
checked only at `EnsureDir` (the completing chunk), so a paired-but-hostile
device can put many kilobytes of arbitrary bytes in it and have every chunk
log it. The per-chunk accepted-incomplete path logs at **Debug** for exactly
this reason — only the terminal outcomes (stored, refused) reach Info. Capping
the id itself was not done here: the same exposure already ships for
`question_batch_id` (#1984, unbounded, attacker-authored, logged at Info), and
capping a client-chosen id would be a house-wide rule rather than one
handler's to invent.

### An e2e-level never-log scan needs three renderings per `[]byte` field (#1898)

The unit-tier `TestV2Session_AttachmentChunk_CarriesNoBannedStrings` checks
the banned-string set against a fake intake that builds no path; #1898 adds
the same scan at the e2e tier, over the real daemon's captured stderr, and
found that one needle isn't enough to make the bytes claim non-vacuous. A
`[]byte` field like `AttachmentChunkPayload.Data` renders three different
ways depending on what's doing the rendering — base64 through
`encoding/json`, a bracketed decimal slice through `slog`'s default handler,
and never as raw ASCII through either — so a single-form needle greens
against leaks in the other two forms. The base64 needle is additionally
alignment-sensitive: base64 encodes independent 3-byte groups, so the
encoding of a byte range appears inside the encoding of an enclosing buffer
only when both the range's offset and length are multiples of three: off
that boundary the needle can never match, leak or no leak. A positive pin —
asserting the loggable attachment id *does* appear in the captured log — is
what stops the negatives from greening against an empty or wrong-window
buffer instead of a genuinely clean one.

## Related

- [Chunk intake driver](attachments-package-intake-driver.md) — the seam's
  implementation, the `Lookup`-then-`Deliver` fork, and the single-feeder
  precondition this handler's off-`Run` worker discharges structurally
- [Error codes § the seven `attachment.*` codes](protocol-package-constants-codes-go-error-codes-21.md) — the full wire-code table and retryability rationale for both codes chosen here
- [Concurrency](v2-session-manager-concurrency.md) — the `appFrameJob` tagging and why the routing decision lives in exactly one place
- [Inbound question control](v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md) — the nearest seam-shaped precedent (nil-seam inert-but-consumed, reject-not-tolerate on decode failure, type-mismatch fixture shape)
- [Envelope types § v2 attachment-chunk vocabulary](protocol-package-constants-codes-go-envelope-types.md) — the wire types this handler answers
