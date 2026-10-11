# `internal/memorycapture` — immutable capture batches and attempt evidence

`Store` persists capture input and caller-reported note changes under
`~/.pyry/memory/capture`, separate from notes, recent transcripts and credentials.
`New(home)` takes the account home directory, not the vault. This storage slice
ships before acknowledgement and runner wiring: it executes no agent, advances
no progress, indexes no notes and deletes no transcripts. Acknowledgement,
processed-revision queries and completion receipts belong to
[#3178](https://github.com/pyrycode/pyrycode/issues/3178).

## Batch progress

`Store.Publish` durably publishes a pending batch before returning it as
executable. There is at most one pending batch for an `Identity`: the
caller-resolved canonical vault path, conversation and exporter-owned transcript
generation. Hashing that tuple names the record without using its values as path
components. Vaults, conversations and generations remain isolated; a reused
routing session ID alone cannot identify a transcript generation.

The first publication freezes the selected capture agent/model, ordered
`Message` identities, text revisions, exact captured text and attribution,
structured metadata and all boundaries in `Payload`. `Message.Revision` must
change when text changes on the same stable row. Eligibility, ordering,
attribution, revisions and closure belong to the exporter; the store neither
parses Markdown nor rereads history. See
[recent conversation transcripts](../../guide.md#recent-conversation-transcripts).

`Store.Publish` returns an existing pending batch before considering replacement
input. New source text, revised rows or changed configuration cannot alter its
payload or content-derived batch ID. Serialization and fresh decoding detach
message slices, metadata, closure pointers and returned evidence from caller
memory. Mutating an input or returned batch changes no stored state. Failed or
cancelled execution leaves the same batch available for retry; reopening with
`New` and calling `Store.Pending` recovers it. `Store.Pending` returns nil when
no batch exists. Closure-only payloads can be published without messages.

The three exporter boundaries have different meanings and are frozen together:

| Exporter boundary | Payload field | Meaning |
| --- | --- | --- |
| Last text entry | `LastTextEntry` | Greatest history entry contributing eligible user or assistant text, including a late delta extending a stable row. Completion, tool and status facts alone do not advance it. |
| Last delivered entry | `LastDeliveredEntry` | Greatest represented user-delivery entry. Assistant text does not advance it. |
| Closing entry | `ClosingEntry` | Optional immutable logical-session closure entry. Late predecessor text never moves it. |

A delivered-entry watermark would miss later assistant text; a stable row ID
alone would miss changed text on that row. Captured revisions and the eligible
text boundary preserve that distinction without treating closure as text
progress.

## Attempt evidence

`Store.Report` durably adds caller-reported changed note paths to
`Batch.ChangedPaths`, a sorted, deduplicated union separate from the immutable
payload. Paths from failed attempts remain evidence. Reopening, duplicate reports
and empty-change retries preserve the union; evidence updates do not change the
batch ID or payload.

Reports must match the pending identity and batch ID. Every path must be a
canonical relative slash path within the vault namespace; absolute paths, empty
paths, traversal components and backslashes are rejected. One invalid path
rejects the entire update. Unknown or mismatched batches add no evidence. This
validates path names only: the store never reads or writes note files and retains
only supplied changes. Same-vault execution serialization and discovery of
unreported edits across the note-write crash window belong to
[#3174](https://github.com/pyrycode/pyrycode/issues/3174).

## Concurrency and durable publication

Each operation reads disk under a cancellable kernel flock spanning the complete
transaction. Concurrent handles and processes converge on the same first batch
and retain every accepted evidence update. A process-local mutex or cached union
would allow another daemon to publish a competing batch or lose changed paths.
The handle's read lock keeps its directory descriptor alive; `Store.Close` waits
for active operations. Storage locking does not serialize agent execution.

Publication writes an exclusive temporary record, checks write/sync/close,
revalidates the destination, renames and syncs the parent directory. A failure
before rename leaves the prior complete record, or no batch on first publication.
A directory-sync failure can follow a successful rename, so an error does not
prove that old bytes survived. `Store.Publish` and `Store.Pending` return no
executable batch on error. Recovery validates and syncs the complete surviving
record before returning it; retry must retain committed payload and evidence
rather than replace them. Temporary leftovers are never authoritative records.
`openDirectory` also syncs parents of existing private directories: their creator
may have failed after mkdir but before syncing the new directory entry.

## Private records and validation

Private state directories require exact 0700 permissions; records and the lock
require exact 0600, account ownership, regular-file type and a single hard link.
Descriptor-relative no-follow traversal and operation-boundary checks reject
directory, lock, record and temporary-source substitution. Keep validated record
and temporary-source descriptors alive through publication: closing first can
let an identical replacement reuse the inode and defeat inode comparisons.
`publish` checks the published inode and complete bytes after directory sync,
since a safe destination before rename does not prove a safe result afterward.

`readRecord` bounds reads at 64 MiB and checks schema, identity, payload digest,
record checksum and evidence ordering. Unsafe or corrupt state fails visibly
without reset or overwrite. Hashes detect inconsistency; they do not authenticate
records against a malicious writer with the same account access.

`validUTF8` checks each independently encoded string before hashing or JSON
serialization. Invalid bytes in adjacent fields can concatenate into a valid
rune while remaining invalid separately. Raw record bytes must also pass UTF-8
validation before decoding: JSON can replace malformed bytes with U+FFFD, making
corrupt bytes match a valid payload's digest after normalization. Valid multibyte
text and a literal U+FFFD remain valid inputs.

## Testing

`TestImmutableRetry` checks full payloads after input/returned-value mutation,
configuration and text changes, cancellation, reopening and closure-only input.
`TestIdentityIsolation`, `TestConcurrentStores` and `TestCrossProcess` cover tuple
isolation, first-publication convergence and evidence unions. `TestEvidence`
checks failed-attempt evidence followed by an empty retry and all-or-nothing
rejection.

`TestPublicationFailures`, `TestEvidenceFailures` and `TestPartialWriteFailure`
inspect complete reopened state, including recovery after rename. Checking only
the returned error would miss committed new bytes or a partial evidence union.
`TestFilesystemAttacks` and `TestPostRenameSubstitution` exercise path replacement
on both sides of rename. `TestOwnershipValidation` covers ownership validators
without privileges; the real foreign-owner chown fixture requires root.
`TestSplitUTF8InputRejectedBeforePublication` checks adjacent-field boundaries;
`TestRawInvalidUTF8RecordRejectedWithoutOverwrite` checks that `Pending`,
`Publish` and `Report` reject malformed stored bytes without overwriting them.

See the [storage design](../../specs/architecture/3177-memory-capture-batches.md)
and [verification practices](development-verification.md).
