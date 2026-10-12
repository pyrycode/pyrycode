# `internal/memorycapture` — capture batches, revision progress and receipts

`Store` persists capture input, caller-reported note changes and processing
receipts under `~/.pyry/memory/capture`, separate from notes, recent transcripts
and credentials. `New(home)` takes the account home directory, not the vault.
The caller supplies eligible messages and authorizes successful execution before
acknowledgement. The store executes no agent, indexes no notes and deletes no
transcripts; capture execution, scheduling and retirement consume this storage
contract downstream.

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
no batch is pending. Closure-only payloads can be published without messages.

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

Processed progress is the union of exact `MessageRevision` identity/revision
pairs in all acknowledged payloads. Opaque revisions have no ordering: the store
retains every processed pair rather than only the latest revision per row.
`Store.Unprocessed` validates a caller-supplied current eligible `[]Message`
snapshot and returns its unprocessed pairs in caller order, preserving text and
attribution. A changed old assistant row below a prior high-water mark and a
concurrently added row both remain work unless their exact pairs were in an
acknowledged payload. Acknowledgement advances only those captured pairs, never
the whole current snapshot or everything below a scalar boundary.

Callers use `Store.Unprocessed` to select subsequent payloads. New publication
preserves committed progress and receipt history. With no pending batch,
publishing an exact already-acknowledged payload returns nil without recreating
work. With pending work, the immutable pending batch still takes precedence.

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

## Completion receipts

`Store.Acknowledge(ctx, identity, batchID)` requires caller-authorized execution
success. It atomically moves the matching pending batch into durable history and
clears pending work. Progress derives from that history, so recovery cannot
advance processed revisions without the corresponding receipt. Failed or
cancelled execution should leave the batch pending for retry. Unknown or wrong
acknowledgements return `ErrMismatch` without changing state. Replay checks
committed history first and returns the same historical receipt after reopening
or while a newer batch is pending; it cannot clear that newer batch.

Each `Receipt` exposes `BatchID`, the full `Identity` (canonical vault,
conversation and transcript generation), `Processed` identity/revision pairs,
the eligible-text boundary `LastTextEntry`, the separate `LastDeliveredEntry`,
optional acknowledged `ClosingEntry` and sorted, deduplicated `ChangedPaths`.
The changed-path union belongs to that batch and includes every durably reported
attempt, including failed attempts before reopening and a successful empty-change
retry. A no-change or closure-only success has an empty union only if no attempt
on that batch reported changes. Reports after acknowledgement reject the batch;
they cannot rewrite its receipt. Receipts acknowledge processing and do not
certify every statement as a fact. They also do not establish note searchability.

`Store.Receipts(ctx, identity)` returns historical receipts in acknowledgement
order, scoped to the complete canonical vault/conversation/generation tuple.
History survives reopening and later batches. Receipt slices and closure
pointers are detached: caller mutation cannot change stored history or another
query's results.

`Store.Complete(ctx, identity, currentMessages, closure)` compares one durable
snapshot against the caller's current eligible revisions. Final completion
requires a non-nil closure matching an acknowledged `ClosingEntry`, every current
identity/revision pair processed and no pending batch. Open transcripts are never
final-complete. Newly observed closure still needs a closure-only publication and
acknowledgement when all text is already processed. Later eligible text or an
edited old row makes completion false until its revision is captured, even after
closure was acknowledged. This neither moves the exporter's original closure
marker nor rewrites historical receipts.

## Concurrency and durable publication

Each operation reads disk under a cancellable kernel flock spanning the complete
transaction. Concurrent handles and processes converge on the same first batch
and retain every accepted evidence update. A process-local mutex or cached union
would allow another daemon to publish a competing batch or lose changed paths.
Acknowledgement uses the same transaction: every report accepted before it is
retained in the receipt, while later reports reject the completed batch.
The handle's read lock keeps its directory descriptor alive; `Store.Close` waits
for active operations. Storage locking does not serialize agent execution.

Publication writes an exclusive temporary record, checks write/sync/close,
revalidates the destination, renames and syncs the parent directory. A failure
before rename leaves the prior complete record, or no batch on first publication.
A directory-sync failure can follow a successful rename, so an error does not
prove that old bytes survived. `Store.Publish` and `Store.Pending` return no
executable batch on error; `Store.Acknowledge` likewise returns no receipt on any
error. After an acknowledgement rename, recovery may retain the complete receipt
and processed progress with pending cleared. Before rename, the prior pending
batch remains retryable. Recovery validates and syncs the complete surviving
record before returning it; replay resolves an ambiguous acknowledgement without
losing history or newer pending work. Temporary leftovers are never authoritative
records.
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

`readRecord` bounds reads at 64 MiB and checks schema, identity, every pending and
completed payload digest, record checksum and evidence ordering. `validRecord`
rejects duplicate completed batch IDs, an already-completed pending ID and
orphaned payload state. Acknowledgement and queries retain these private-storage
checks. Unsafe or corrupt state fails visibly without reset or overwrite. Hashes
detect inconsistency; they do not authenticate records against a malicious writer
with the same account access.

Version-1 records reopen with their exact pending payload and attempt evidence,
initially without processed progress or receipts. Queries preserve their bytes;
acknowledgement or new publication upgrades to version 2. The empty `Completed`
field is omitted from JSON to preserve the version-1 checksum encoding. Empty
pending validation compares encoded values: decoding `json.RawMessage` metadata
turns nil into raw `null`, so `reflect.DeepEqual` against the Go zero value would
reject a valid reopened acknowledgement. The encoded comparison still rejects
nonzero orphan payloads.

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

`TestAcknowledgementHistory` checks receipt fields, retained failed-attempt
evidence after reopening and an empty retry, wrong acknowledgement, replay with
newer pending work, committed-payload suppression and detached historical
queries. `TestRevisionCompletion` checks changed old and concurrently added rows,
retention of historical revisions, closure-only acknowledgement, pending-work
finality and late-text invalidation without rewriting the closure receipt.
`TestCompletionIsolation` and `TestConcurrentCompletion` cover identity isolation,
accepted evidence across processes and replay/acknowledgement across handles.

`TestAcknowledgementFailures` inspects reopened pending, receipt and revision
state for write, file-sync, close, rename, directory-sync and cancellation faults.
`TestVersionOneCompletion` builds the original schema independently to catch
checksum incompatibility, then checks exact pending recovery and retained
evidence after upgrade. `TestCompletionLedgerValidation`,
`TestCompletionRejectsRecords` and `TestCompletionInvalidInputs` check corrupt or
unsafe state and invalid caller revisions without overwrite or false completion.

`TestPublicationFailures`, `TestEvidenceFailures` and `TestPartialWriteFailure`
inspect complete reopened state, including recovery after rename. Checking only
the returned error would miss committed new bytes or a partial evidence union.
`TestFilesystemAttacks` and `TestPostRenameSubstitution` exercise path replacement
on both sides of rename. `TestOwnershipValidation` covers ownership validators
without privileges; the real foreign-owner chown fixture requires root.
`TestSplitUTF8InputRejectedBeforePublication` checks adjacent-field boundaries;
`TestRawInvalidUTF8RecordRejectedWithoutOverwrite` checks that `Pending`,
`Publish` and `Report` reject malformed stored bytes without overwriting them.

See the [storage design](../../specs/architecture/3177-memory-capture-batches.md),
[completion design](../../specs/architecture/3178-memory-capture-completion.md)
and [verification practices](development-verification.md).
