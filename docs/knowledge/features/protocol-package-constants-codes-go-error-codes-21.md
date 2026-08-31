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

**The seven `attachment.*` codes (#1751) are vocabulary declared ahead of any consumer** — #1741 (reassembly), #1743 (storage), #1744 (inbound dispatch) and #1746 (retrieval) are all wired blocked-by this ticket precisely so none of the four invents its own name for the same condition. Two decisions are worth carrying forward past this ticket:

- **One receiver-resource condition split into two codes on retryability, not one.** "Too many concurrent uploads" (`attachment.too_many_uploads`) clears when *other* uploads finish, so a client retry succeeds; "this upload is over the receiver's byte bound" (`attachment.too_large`) is permanent for that file. A single code marked retryable would tell a client to hot-loop re-uploading an oversized file forever; marked non-retryable, it would tell a client to give up on a bound that clears in seconds. When one plain-language condition ("a receiver-side resource bound is hit") actually names two different retry contracts, that's a signal to split the code, not average the retryability.
- **`attachment.not_found` deliberately merges "unknown id" and "resolves outside the conversation's directory" into one code** — a disclosure decision, not an imprecision. Two distinguishable codes would make the retrieval verb a path-existence oracle for a traversal probe. The merge is only real if the receiver also uses a static message (never the requested id or resolved path) and does the same resolution work on every request regardless of which sub-case it hit, so the two don't separate on response timing either. A shared reject code with a leaky message or a shortcut fast-path defeats the merge it's supposed to enforce.
- **A `retryable: yes` code needs an explicit back-off obligation stated beside it, or a conforming client turns it into a hot loop.** All three retryable rows here (`too_many_uploads`, `storage_failed`, `stream_aborted`) carry "retry after a backoff" in their spec Notes rather than leaving `yes` to imply "resend immediately" — `too_many_uploads` is the sharp case, since an immediate retry both fails and consumes the capacity it's waiting for.

Docs-and-Go review lesson from #1751 (a documentation-only ticket, no consumer code): **when a spec's prose gives per-constant comment guidance, check it against the same spec's own design table before writing the comment** — this ticket's spec text asked for a trailing comment marking `attachment.too_many_uploads` as "the group's only retryable member," but the spec's own table two paragraphs up marked three of the seven codes retryable. Following the prose instruction literally would have shipped a false claim into `codes.go` that no test catches (the pins in `TestErrorCode_Constants_MatchSpec` check wire *values*, not comment prose). Where a spec's prose and its own table disagree on a count, the table is the artifact that was reasoned about row by row — trust it.
