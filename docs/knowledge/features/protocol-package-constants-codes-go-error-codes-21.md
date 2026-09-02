# Error codes (21)

Wire values for the `code` field of error payloads (spec § Error codes). Naming convention: `Code<Category><Reason>` mirrors the dotted-string `category.reason` shape.

| Constant | Wire string |
|----------|-------------|
| `CodeProtocolUnknownType` | `protocol.unknown_type` |
| `CodeProtocolMalformed` | `protocol.malformed` |
| `CodeProtocolUnsupported` | `protocol.unsupported` |
| `CodeAuthInvalidToken` | `auth.invalid_token` |
| `CodeAuthTokenRevoked` | `auth.token_revoked` |
| `CodeServerBinaryOffline` | `server.binary_offline` |
| `CodeServerBinaryBusy` | `server.binary_busy` |
| `CodeConversationNotFound` | `conversation.not_found` |
| `CodeConversationAlreadyPromoted` | `conversation.already_promoted` |
| `CodeMessageTooLong` | `message.too_long` |
| `CodeRelayNoServer` | `relay.no_server` |
| `CodeRelayServerIDConflict` | `relay.server_id_conflict` |
| `CodeSessionNotFound` | `session.not_found` |
| `CodeSessionBlocked` | `session.blocked` |
| `CodeAttachmentInvalidChunk` | `attachment.invalid_chunk` |
| `CodeAttachmentIntegrityFailed` | `attachment.integrity_failed` |
| `CodeAttachmentTooManyUploads` | `attachment.too_many_uploads` |
| `CodeAttachmentTooLarge` | `attachment.too_large` |
| `CodeAttachmentStorageFailed` | `attachment.storage_failed` |
| `CodeAttachmentNotFound` | `attachment.not_found` |
| `CodeAttachmentStreamAborted` | `attachment.stream_aborted` |

**The seven `attachment.*` codes (#1751) are vocabulary declared ahead of any consumer** — #1741 (reassembly), #1743 (storage), #1897 (inbound dispatch, split from #1744) and #1746 (retrieval, closed `NOT_PLANNED` and split into #2052 the request verb, #2053 the outbound stream, #2054 the handler that answers with these codes) are all wired blocked-by this ticket precisely so none of the four invents its own name for the same condition. Two decisions are worth carrying forward past this ticket:

- **One receiver-resource condition split into two codes on retryability, not one.** "Too many concurrent uploads" (`attachment.too_many_uploads`) clears when *other* uploads finish, so a client retry succeeds; "this upload is over the receiver's byte bound" (`attachment.too_large`) is permanent for that file. A single code marked retryable would tell a client to hot-loop re-uploading an oversized file forever; marked non-retryable, it would tell a client to give up on a bound that clears in seconds. When one plain-language condition ("a receiver-side resource bound is hit") actually names two different retry contracts, that's a signal to split the code, not average the retryability.
- **`attachment.not_found` deliberately merges "unknown id" and "resolves outside the conversation's directory" into one code** — a disclosure decision, not an imprecision. Two distinguishable codes would make the asking verb a path-existence oracle for a traversal probe. The merge is only real if the receiver also uses a static message (never the requested id or resolved path) and does the same resolution work on every request regardless of which sub-case it hit, so the two don't separate on response timing either. A shared reject code with a leaky message or a shortcut fast-path defeats the merge it's supposed to enforce.
  - **The merge widened to a second verb rather than staying retrieval-only, #2036.** A `send_message` naming an `attachment_ids` entry that does not resolve under the message's own conversation answers the same code, not a minted one — same predicate (an id failed to resolve to a file inside that conversation's directory), same retryability, same static message, same client repair (re-list, then re-upload or drop the reference). The merge argument applies with *more* force here: `send_message` is the cheaper probe of the two verbs, and a list field makes a per-id answer a batch existence-probe for up to `MaxAttachmentIDsPerMessage` ids at once, so the static message's "never name which one failed" rule now also has to hold across a list, not just a single id. `internal/attachments/registry.go`'s `ErrUnknownUpload` doc comment still reads "`CodeAttachmentNotFound` is the retrieval leg's" — that sentence is now stale (flagged as a SHOULD FIX on #2036's code review, left unfixed as non-blocking prose) and should not be trusted as the current scope of the code; `codes.go`'s own comment block and the spec's § Error codes row are the current source of truth.
- **A `retryable: yes` code needs an explicit back-off obligation stated beside it, or a conforming client turns it into a hot loop.** All three retryable rows here (`too_many_uploads`, `storage_failed`, `stream_aborted`) carry "retry after a backoff" in their spec Notes rather than leaving `yes` to imply "resend immediately" — `too_many_uploads` is the sharp case, since an immediate retry both fails and consumes the capacity it's waiting for.

**#1897 picked the last two sentinel→code mappings this vocabulary left open, both by folding into an existing code rather than minting one** (`internal/relay`'s dispatch site, `errors.Is` per code — see [Inbound `attachment_chunk`](v2-session-manager-state-machine-inbound-attachment-chunk-attachmentintake-seam.md)). Both choices turned on **which retry advice is actually true**, not on which code reads closest in English:

- `ErrUnknownUpload` (a transfer no longer in the registry by the time a chunk is delivered — reaped for idleness, or released by that conn's own teardown) → `attachment.invalid_chunk`, chosen against *"your transfer expired,"* not against *"you resumed at a non-zero index against a restarted daemon"* — the latter is admitted into a fresh transfer under a separate, already-pinned admission fork and never reaches this sentinel at all. `invalid_chunk`'s published `retryable: no` is correct here, not merely tolerable: naively resending just the one chunk would be admitted as a new transfer that can never complete, so "rebuild the transfer" is the right advice, "resend this frame" is not.
- `ErrNoConversation` (a completing chunk that resolves no destination — the daemon is on no conversation, or no resolver is wired) → `attachment.storage_failed`, sharing a code with unrelated storage-layer sentinels without collapsing the distinction that mattered: the message stays static (no client-visible "you're unrouted"), and `retryable: yes` is right because the condition clears by itself the moment the daemon routes to a conversation, unlike a genuinely malformed id.

**The generalizable point:** when an existing code's retry contract is already true of a new condition, reuse it — minting a code is a protocol-publication step (a `codes.go` constant plus a new spec row), not something a single consumer ticket should do on its own, and #1751 declared this vocabulary in one place precisely so implementations wouldn't each invent a name.

Docs-and-Go review lesson from #1751 (a documentation-only ticket, no consumer code): **when a spec's prose gives per-constant comment guidance, check it against the same spec's own design table before writing the comment** — this ticket's spec text asked for a trailing comment marking `attachment.too_many_uploads` as "the group's only retryable member," but the spec's own table two paragraphs up marked three of the seven codes retryable. Following the prose instruction literally would have shipped a false claim into `codes.go` that no test catches (the pins in `TestErrorCode_Constants_MatchSpec` check wire *values*, not comment prose). Where a spec's prose and its own table disagree on a count, the table is the artifact that was reasoned about row by row — trust it.
