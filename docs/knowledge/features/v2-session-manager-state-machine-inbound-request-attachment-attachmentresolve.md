# Inbound `request_attachment` (#2054) — `AttachmentResolve` seam

`request_attachment` is a v2 **control** envelope (phone → binary), intercepted
in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route`, and
routed to the conn's `appFrameWorker` rather than handled inline on `Run` —
the second arm to need that route after [`attachment_chunk`](v2-session-manager-state-machine-inbound-attachment-chunk-attachmentintake-seam.md)
(#1897), and for the same reason: answering it reads a stored file of up to
the per-upload byte bound, hashes it and base64-marshals one envelope per
chunk. It is the join between three landed-but-unwired pieces: the wire type
(#2052), [`StreamAttachment`](v2-session-manager-state-machine-outbound-attachment-stream-streamattachm.md)
(#2053), and `attachments.ResolvePath` (#2037) behind a new
`V2SessionConfig.AttachmentResolve` seam. `security-sensitive`; architect +
code-review security passes both **PASS**. See
[`docs/specs/architecture/2054-attachment-retrieval-handler.md`](../../specs/architecture/2054-attachment-retrieval-handler.md).

## Ordering is the security property, not the presence of the checks

`handleRequestAttachment` runs seven steps and their **order**, not merely
their existence, is what keeps a conversation id from becoming a capability:
nil-seam inert, envelope decode (unreachable), payload decode (rejected, not
tolerated), the `KnownConversation` membership gate, an empty-id check, the
`AttachmentResolve` lookup, then the stream call. The membership gate fires
**before either id becomes a path component** — `attachments.ResolvePath`'s
stated precondition, and this handler is the caller that discharges it.
`handleRequestSnapshot` is the in-package ordering precedent (membership gate
first); its reject *code* is deliberately not copied, because it answers
`conversation.not_found` and a distinguishable code here would rebuild the
path-existence oracle the merge below exists to prevent.

**`KnownConversation`, never a session router.** `SessionRouter.Route` lives
in a package the v2 manager does not import, and — more importantly — it
additionally refuses a *known* conversation that has *no bound session*,
which is precisely the reopened-conversation case this verb's whole user
story is about. `KnownConversation` is a pure membership check: addressable,
not bound, which is the correct reading for retrieval.

## Six causes, one code — and the resolver's comma-ok is why a dispatch site can't leak which one fired

Every no-bytes cause (undecodable payload, empty id, unknown conversation,
resolver miss, non-canonical id, id resolving outside the conversation's
directory, or a pre-emission read failure) answers the same
`CodeAttachmentNotFound` with the same static message and the same
`retryable:false`. That merge is structural, not a convention the handler
has to remember: `AttachmentResolve` returns `(path string, ok bool)`, one
bool behind which `attachments.ResolvePath` folds an unknown id, a
non-canonical id and an out-of-directory id into a single sentinel — the
dispatch site *cannot* branch on what it must not distinguish, because the
information to distinguish never crosses the seam.

**That collapse has a sharp edge for logging, and getting the reasoning
wrong here is the real lesson this ticket's code review caught.** The
resolver-miss arm logs `attachment_id` — matching `handleAttachmentChunk`'s
posture on the sibling upload leg — and the first-drafted comment justified
it by saying the resolver had *just shape-checked* the id. That claim is
false on exactly that arm: `!ok` is also the outcome when
`ResolvePath`'s own `conversations.ValidID` check fails, and the comma-ok
seam collapses "shape invalid" into the same `false` as "no such file" *by
design* — the handler cannot tell the two apart, because telling them apart
is what the merge forbids. So the id reaching the log line on a resolver
miss can be an **arbitrary client string**, not a validated one. The
behaviour (log it anyway) is still correct — it's the most diagnostic field
available on a refusal, and the actual bound is the one
`handleAttachmentChunk`'s header already names: `slog`'s `TextHandler`
escapes control bytes, so an escape-bearing id cannot forge log structure.
The lesson generalizes past this one field: **when a seam collapses several
distinguishable failure causes behind one `(value, ok bool)` or one
sentinel error — deliberately, for a disclosure reason — a comment
justifying a downstream decision about that seam's `false`/error case must
not claim more about the input than the collapse actually proves.** "I
already validated this" is a claim about a *specific* branch, not about
every branch that can produce the same falsy return.

The conversation id, by contrast, is **never** logged on any arm — this
handler validates its *membership*, not its *shape*, and shape validation is
`docs/protocol-mobile.md` § Attachments' stated precondition for logging a
client-supplied string at all.

## The two-code partition reads the error's identity, because identity is all there is

`StreamAttachment(ctx, connID, attachmentID, path, inReplyTo) error` carries
no count of what it enqueued, so "how many chunks went out" is not a
question the handler can ask directly. `attachmentStreamAborted(err)`
answers it instead by naming the **abort set** — `ErrConnNotFound`,
`context.Canceled`, `context.DeadlineExceeded` — and defaulting everything
else to `CodeAttachmentNotFound`. That direction is deliberate: `Push`'s own
contract closes its error set (those three causes, full stop, and a
drop/overflow is not an error), so the abort set is small and enumerable,
while the pre-emission set (`os.ReadFile`, `attachmentEnvelopes` marshal) is
open — any `errno` a filesystem can produce. Defaulting the *open* set to
`not_found` means a future pre-emission failure mode classifies correctly
with no edit here; defaulting the other way would claim emission began when
it hadn't. One accepted consequence: a `Push` failure at index 0 answers
`stream_aborted` where `not_found` would also have been defensible — the
correct trade, since `stream_aborted` is the retryable code and a torn-down
conn is a retryable condition, whereas `not_found` would tell a client its
file is gone when it is not.

## `AttachmentResolve` discharges no registry check, and its doc block says so

The seam mirrors `KnownConversation`'s primitive-in/primitive-out shape so
`internal/relay` imports neither `internal/attachments` nor
`internal/conversations`. Its doc block on `V2SessionConfig` states plainly
that it wraps `ResolvePath` and validates *nothing* — the caller (this
handler) already did that via `KnownConversation`. It also names, ahead of
being asked, the two consequences of a non-conforming implementation:
**no containment guarantee** for a path from anywhere else, and a
**wedged `appFrameWorker`** if that path names a non-regular file (`os.ReadFile`
blocks forever on a FIFO). No runtime guard was added against either — both
are unreachable through the sanctioned wrapper, and a guard here would be a
second, weaker copy of a check `ResolvePath` already performs.

## An ordering claim needs a non-wire assertion, because the wire can't tell orders apart

Both orderings of "check membership" vs. "resolve the path" produce the
**identical reject frame** on a foreign conversation id — that
indistinguishability is the whole point of the `attachment.not_found` merge
above, so a test that only inspects the reply cannot tell "validated the
conversation first" from "built a path out of the client's ids and then
happened to refuse." Proving the ordering the design section calls
load-bearing needs a different observable: the unit suite asserts the
**resolver stub's call count** stays zero when the conversation id is
unknown, so a handler that reordered the two checks (resolve first, gate
second) would still answer the same wire frame but call the resolver once —
the mutation the assertion exists to kill. The general point: when a design
depends on step ordering but the two orderings are wire-observably
identical, the test needs an assertion on a side effect internal to a
fixture (a call count, a mock invocation), not on the reply.

## A retrieval e2e doesn't need the upload leg or a routed turn

`TestRelayV2_AttachmentUploadMultiChunk` (#1898) has to drive a
`send_message` first, because the upload's completing chunk resolves its
destination conversation off the daemon's follow-active cursor. Retrieval
reads its conversation off the request frame itself and validates it against
the registry — it never touches the cursor — so the e2e for this handler
writes the fixture file **directly onto the host** under the conversation's
attachment directory (same mode, `0600`, that a real upload would leave)
instead of routing an upload through the daemon first. That makes the run
independent of the upload leg and cuts it from tens of seconds to under one.
Worth remembering for any future retrieval-shaped e2e in this family: check
whether the read path actually depends on the write path's side channel
before assuming a round-trip fixture has to be built by driving the write
path through the daemon.

## Widened the tag, not the probe

`appFrameJob.attachment bool` (#1897) widened to a typed `appFrameKind`
enum with this ticket's second member — see
[Concurrency](v2-session-manager-concurrency.md) for the routing-tag design
and the staticcheck trap the zero-value member (`appFrameRoute`) hit on the
first cut: a `switch` arm can lean on a zero value implicitly, but the
compiler cannot see that as intentional unless something actually names the
identifier — `staticcheck`'s `U1000` will flag it dead otherwise.

## Related

- [Outbound attachment stream (#2053)](v2-session-manager-state-machine-outbound-attachment-stream-streamattachm.md) — `StreamAttachment`, the PRECONDITION this handler discharges, and the error-stripping that keeps a `PathError` from leaking the host path through `errors.Is`.
- [Inbound `attachment_chunk` (#1897)](v2-session-manager-state-machine-inbound-attachment-chunk-attachmentintake-seam.md) — the upload-leg sibling this handler's nil-seam guard, reject-not-tolerate decode, and never-log posture are copied from verbatim.
- [Concurrency](v2-session-manager-concurrency.md) — the `appFrameKind` widening and the zero-value-enum staticcheck lesson.
- [Attachment envelope types § retrieval](protocol-package-constants-codes-go-envelope-types-attachments.md) — `RequestAttachmentPayload`, the guard-filing move this ticket made mandatory, and the wire vocabulary this handler answers.
- [Error codes § the seven `attachment.*` codes](protocol-package-constants-codes-go-error-codes-21.md) — the retryability reasoning behind `CodeAttachmentNotFound` / `CodeAttachmentStreamAborted`.
- [Directory resolution and creation § `ResolvePath`](attachments-package-directory-resolution-and-creation.md) — the containment discipline and the merged `ErrNotFound` sentinel this handler's `AttachmentResolve` closure wraps.
