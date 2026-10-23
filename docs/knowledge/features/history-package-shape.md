# `internal/history` — entry shape and metadata

Part of [the history package overview](history-package.md).

## Shape

Eight production files (`log.go`, `segment.go`, `cursor.go`, `forward.go`,
`sync.go`, `cache_dir.go`, `lookup.go`, `lookup_cache.go`), a leaf importing only
`internal/conversations` (for `ValidID`/`ConversationID`)
and stdlib —
never the wire-payload types and never decoding a payload. `New(instanceDir)` builds
`conversations/<id>/history/` beneath the given root exactly as
[`attachments.EnsureDir`](attachments-package-directory-resolution-and-creation.md)
does; the root is a constructor argument, never resolved inside the package.

```go
type SessionProvenance struct{ Kind string; SessionID string }
type Metadata struct{ Session *SessionProvenance; Shown *bool }
type Entry struct {
    ID uint64
    Type string
    Payload json.RawMessage
    TS time.Time
    Session *SessionProvenance
    Shown *bool
}
type Page struct{ Entries []Entry; Cursor string; AtStart bool }
func (s *Store) Append(convID conversations.ConversationID, typ string, payload json.RawMessage, ts time.Time) (uint64, error)
func (s *Store) AppendWithMetadata(convID conversations.ConversationID, typ string, payload json.RawMessage, ts time.Time, metadata Metadata) (uint64, error)
func (s *Store) Sync(convID conversations.ConversationID) error
func (s *Store) Page(convID conversations.ConversationID, cursor string, limit int) (Page, error)
func (s *Store) Lookup(convID conversations.ConversationID, ids []uint64) ([]Entry, error)
func (s *Store) LatestEntryID(convID conversations.ConversationID) (uint64, error)
func (s *Store) LatestDisplayableEntryID(convID conversations.ConversationID) (uint64, error)
func (s *Store) LogDir(convID conversations.ConversationID) (string, error)
func (s *Store) Forward(convID conversations.ConversationID, afterID uint64) (*ForwardReader, error)
func (r *ForwardReader) Walk(ctx context.Context, throughID uint64, consume func([]Entry) error) error
func (r *ForwardReader) Tail(ctx context.Context, consume func([]Entry) error) error
func (r *ForwardReader) LastEntryID() uint64
```

`Store.Sync` is an explicit conversation-scoped durability barrier. Success
covers successful appends ordered before its captured store-mutex boundary,
including writes in earlier segments after multiple rolls; appends ordered
afterwards remain pending. Required segments are opened read-only and synced
with ordinary `os.File.Sync` on Linux and macOS. After all required file syncs
and closes succeed, the history directory is synced and closed if a segment
was created since the previous successful sync, or on the first sync of an
existing log. This includes new segment directory entries in the durability
guarantee, assuming the filesystem honors successful file and directory syncs
([ADR 045](../decisions/045-thread-index-over-history.md#reliability)).

The first successful sync conservatively covers all existing segments, even on
a reopened Store or one already loaded by reads or appends. Later syncs cover
this Store's pending writes; a repeat without writes performs no file or
directory sync. Missing logs and directories without segments are successful
no-ops that create nothing only when no pending writes are known; they do not
suppress a later conservative first sync. Every call validates ID shape and
rechecks exact directory containment, including calls with no pending work.
Segment-named symlinks and nonregular leaves, or missing segments/directories
known to hold pending writes, return errors rather than being omitted.

Any required open, file sync, directory sync or close failure propagates without
advancing the certified sync boundary; retry covers the entire pending batch.
Sync decodes no payloads and changes no segment bytes, entry IDs, read counters
or follower notifications. `Append` and `AppendWithMetadata` still write and
close without fsync: their machine-crash durability improves only when `Sync`
is explicitly called. The API has no production caller yet; checkpoint
integration belongs to [#3336](https://github.com/pyrycode/pyrycode/issues/3336).
Sync shares the authenticated-conversation precondition below.

`Store.LogDir` (#2673) returns the absolute, symlink-resolved, existing log
directory for a conversation. It validates the ID and uses
`resolveDir(convID, false)` under the store's mutex, rechecking exact containment
on every call; it creates nothing, caches no path and refuses a missing path or
a path that is not a directory. A sibling-conversation redirect is refused just
like a redirect outside the instance root (see [Directory resolution](history-package.md#directory-resolution-re-resolve-every-call-dont-cache-the-answer)).

Switch handover reads this daemon-owned log through `Store.Page` and uses
`LogDir` for the incoming agent's on-demand file pointer. Payload decoding and
completed-exchange selection belong to `cmd/pyry`, preserving this package's
opaque-payload boundary; see [agent-switch handover](conversation-session-binding.md#switching-to-the-other-agent-2672).
The `segment-<number>.jsonl` files are JSON Lines: a schema header
(`{"format":"pyrycode.history","version":1}`) followed by one JSON event per
line. An agent reading the log directory may need permission because the
daemon's instance directory is outside its workspace; handover adds no
permission round-trip.

`AppendWithMetadata` persists `Entry.Session` and `Entry.Shown` alongside `id`,
`type`, `payload` and `ts`, outside the opaque payload. `session` is an object
with `kind` and optional `session_id`; `shown` is a JSON boolean. Nil fields
are omitted, never written as null, and explicit `shown: false` is retained.
The header stays version 1: older entries decode with nil metadata without
rewriting their bytes or inventing sessions. `Store.Page` returns the metadata
across segments and after reopening. The unchanged `Append` signature delegates
to this same locked path with empty `Metadata`, writing absent metadata.

Session provenance has three meanings:

| Stored `session` | Meaning |
| --- | --- |
| `{"kind":"claude","session_id":"..."}` or `{"kind":"codex","session_id":"..."}` | Known producing child; its session ID must be nonempty. |
| `{"kind":"none"}` | Explicitly no producing child, such as a daemon-authored fact without a bound child; `SessionID` must be empty and is omitted. |
| Key absent (`Session == nil`) | Unknown provenance, including legacy entries; absence does not assert that no child produced the fact. |

Other kinds and inconsistent session IDs return ID zero and
`ErrInvalidMetadata` before directory creation, without disclosing metadata in
the error. Session IDs are opaque strings, never paths or authorization.
Visibility is independent of provenance: `Shown == nil` uses the legacy type
rule, while explicit true or false overrides it (see
[the unread watermark](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954)).
Callers must not mutate payload or metadata during an append; the store retains
neither caller pointers nor payload bytes after the call, only scalar cache
values. These storage facts support
[ADR 042's history-backed thread](../decisions/042-daemon-built-thread.md);
the existing producers now declare visibility and filter legacy delivery
(see [Producers](history-package-producers.md#producers-2114-2115) and [Reader](history-package.md#reader-2116)). Channel posts
capture session attribution at acceptance (#2984); interactive Claude/Codex
output captures its producing runner before fan-in (#2981). Session transitions
capture source facts at the observer/publication handoff (#2982). Delivered
operator messages capture the successful receiving session at the final writer
call and retain it through delayed placement or confirmation
([operator provenance](history-package-producers.md#operator-delivery-provenance-2983));
legacy and unknown provenance stay absent.

`limit` is **clamped** to `MaxPageEntries`, never refused above it — a page is
"up to `limit` entries", so an over-large ask from #2116 pages rather than
fails and can never be broken by the ceiling. Entry ids are a
**per-conversation** counter recovered from the newest segment's last entry on
first touch: every cursor here
names its own conversation and is refused on any other, so there is no shared
scalar for one conversation's cursor to mute a different one's stream, and no
reason to pay for a global counter recovered by reading every conversation at
startup.

Both append paths allocate IDs starting at `1`, strictly increasing per
conversation and below `2^53`. `MaxEntryID = 2^53 - 1` is the final successful
allocation, keeping newly minted IDs exact for JSON clients using IEEE-754
numbers. Once the last durable ID reaches that bound, further appends return
ID zero and `ErrIDExhausted` before encoding, segment rolling or writing. They
leave segment files and raw/displayable watermarks unchanged, never wrapping
or reusing an ID. Reopening preserves refusal, including logs whose recovered
last ID is already at or above the bound; raw queries and pages still return
those stored IDs unchanged. Exhaustion affects only that conversation.

**PRECONDITION on the store methods:** `convID` must
be the conversation the authenticated session is already on, never one a
client asserted on the wire. `ValidID` is a *shape* predicate only — a
client-supplied id of canonical shape genuinely resolves inside the
conversation it names and defeats every check beneath it. This is
`ResolvePath`'s precondition verbatim, and it is `#2116`'s to satisfy by
reaching the conversation through the session, never off the wire.

### Selective entry lookup

`Store.Lookup` returns exact stored entries in requested order, including opaque
payloads, timestamps and optional metadata. Input IDs must be strictly ascending
and unique; duplicates or decreasing IDs return `ErrInvalidEntryIDs`, and invalid
conversation-ID shape returns `ErrInvalidID`. Empty input returns an empty result
after directory containment validation. An absent or empty log makes every
requested ID missing and creates nothing. `MissingEntriesError.IDs` identifies
all missing requested IDs, including zero, removed-segment IDs, torn-tail IDs and
IDs beyond the end. Any error returns nil entries, never a usable partial result.
Returned payload bytes, provenance structs and visibility booleans are detached;
caller mutation cannot change later answers. Lookup shares the authenticated
conversation precondition above. It currently has no production caller;
thread adoption belongs to [#3333](https://github.com/pyrycode/pyrycode/issues/3333)
under [ADR 045](../decisions/045-thread-index-over-history.md).

Per-conversation `convLog.lookupBounds` retains segment first IDs and sizes,
never resolved paths. Unknown or resized segments discover their first IDs from
bounded header/first-entry prefixes through `segmentFirstID`; empty/header-only
segments and torn first entries have no range until a complete entry exists.
`lookupSegmentBound` shares these checks with nonzero Forward resumes. A zero
first-ID bound needs provenance: `load` can remember zero from a complete invalid
entry, so a recovery-derived zero bound larger than the header must be
rediscovered. Prefix discovery certifies that no complete first entry exists;
that certificate is reusable while regular-file membership and size remain
valid. Rediscovering every zero bound would reread an unchanged torn first entry
to EOF on every warm call. Resizing invalidates certification, so completing a
zero-ID entry still returns `ErrCorruptSegment`.

Successful appends and rolls keep known bounds current. Lookup avoids append-cursor
recovery through `load`, which would fully decode an unrelated newest segment.
Lookup groups requested IDs by these ranges and fully reads/decodes only selected
segments. Those reads
reuse `readSegment`'s `segmentCeiling` and `decodeSegment`'s version/corruption
rules; complete entries before a torn final line remain readable. Stored IDs
must be nonzero and strictly increasing, with no overlap into the next range.

Every call re-runs `resolveDir` containment and lists current regular segments;
nonempty lookups recheck their regular-file status and size before consulting
cached data. Removed or symlink-replaced segments cannot supply cached entries,
and directory redirects fail containment, including on empty requests. These
checks retain the existing single-call check/use boundary: selected reads use
ordinary opens, not `O_NOFOLLOW` (see
[Directory resolution](history-package.md#directory-resolution-re-resolve-every-call-dont-cache-the-answer)).

One Store-wide LRU shares decoded rolled segments across conversations. Identity
is conversation ID, segment number and current file size. The newest segment
always reads fresh, including when removal of later segments makes a formerly
cached segment newest, so successful active-segment appends are visible. The
default retention limit is **8 MiB (8 × 1024 × 1024 bytes)**; least-recently-used
segments are evicted before retention would exceed the accounted-byte limit.
A decoded segment larger than the cache limit is read without retention.

`lookupDecodedBytes` accounts for retained `segEntry` backing-array capacity
(including each `Entry` and line offset), payload allocation capacity, type-string
bytes, pointed-to `SessionProvenance` structs and their kind/session-ID strings,
and pointed-to visibility booleans. It also reserves 256 bytes per timestamp
whose location is neither `time.UTC` nor `time.Local`, for the decoded fixed-offset
location and its zone/transition arrays. JSON timestamp decoding can allocate
these per entry, so counting only payloads and metadata would understate retained
data; the reservation conservatively includes shared fixed zones too. Allocator
rounding, cache/index bookkeeping and process-global UTC/Local locations are
excluded. No encoded segment buffer is retained. This bounds accounted retained
decoded data, rather than total process memory or temporary reads/results.

### Lookup verification

`readStats` includes successful discovery opens and actual prefix read bytes,
including buffered read-ahead, as well as selected full reads. Discovery decodes
at most the header and first entry, bounded by `segmentCeiling`; a long torn
first entry can require reading to EOF. Prefix discovery does not certify the
unread remainder of an unselected segment.

A warm lookup served entirely from cached segments adds no segment opens or
bytes, while still checking filesystem membership. A warm Forward resume with
valid unchanged bounds reads only selected full segments; Forward retains no
decoded cache. `TestLookupSelectiveReads` and `TestForwardResumeSelectiveReads`
use segments longer than discovery read-ahead and corrupt an unselected segment
after its valid first entry. A tiny fixture can be read entirely by buffering,
and correct callbacks alone cannot prove selective I/O. The Forward test counts
construction plus Walk on a fresh reopened Store without watermark or lookup
priming, then checks warm bounds separately: cold reads include all discovery
bytes/opens plus selected full reads; warm reads exclude old segments entirely.

`TestForwardResumeTornFirstEntryReads` checks both Forward and Lookup against an
unselected torn first entry, asserting exact cold/warm bytes and opens and
rejection after resizing completes a zero-ID entry. This catches needless warm
rediscovery even when delivery remains correct. `TestLookupRoundTrip` seals the
metadata segment before mutating returned data, so a later answer proves cache
detachment rather than merely rereading fresh disk bytes. `TestLookupCacheAccounting` exercises
unused array/payload capacity, pointed-to metadata and timestamp locations.

## Forward consumption

Capture a conversation's high-water ID H with `LatestEntryID`, construct
`Store.Forward(convID, afterID)`, then call `Walk(ctx, H, consume)` to replay
raw entries in `(afterID, H]`, exactly once after successful delivery and in
strictly increasing ID order across segments. Resume is exclusive; zero starts
from the beginning, and the resume ID need not exist in storage. Appends above
H remain for a later walk or tail. Nil return explicitly completes the range,
including an empty or initially missing log; reopening preserves this contract.
Every entry retains its type, opaque payload, timestamp and optional session
and visibility metadata, including hidden and unknown types. Payload interpretation
and thread reduction remain the consumer's responsibility
([ADR 042](../decisions/042-daemon-built-thread.md)).

`Forward` construction performs no I/O. On the first useful Walk with a nonzero
resume ID V, `forwardSegments` uses current regular-file membership and
size-checked `convLog.lookupBounds`, refreshing unknown or resized bounds through
prefix discovery. Filenames encode segment sequence numbers, not entry IDs, so
a cold Store must discover prefixes before selecting a range. Nonempty first
IDs must be nonzero and strictly increasing. Selection chooses the last first
ID <= V; a gap uses its predecessor range, and V before the log uses the earliest
nonempty range. V beyond the readable end retains the final range so later
appends greater than V remain consumable. Zero-start readers keep sequential
reading and validation; H <= V delivers nothing and performs no segment I/O.

Only the selected boundary segment and subsequent segments needed for the walk
are fully read/decoded. The boundary read can include entries <= V. Before
delivery, every complete entry in a fully read segment is validated, including
entries filtered at or below V or above H: zero, duplicate/decreasing IDs and
overlap into the next known nonempty range return `ErrCorruptSegment`. Unselected old
segments receive prefix discovery, not full integrity validation; corruption
after their valid first entry does not prevent suffix replay. Bounds are
ephemeral; no persistent index, segment-format change or consumer migration is
required ([ADR 045](../decisions/045-thread-index-over-history.md#decision)).

Callbacks receive at most `MaxPageEntries`. A successful callback advances
`LastEntryID`; an error is returned and leaves that chunk retryable, so callbacks
that partially apply a chunk before failing must handle its redelivery. Reads use
the existing `segmentCeiling`, holding only one decoded segment and a delivery
chunk. The reader retains scalar progress across calls, skips fully consumed
sealed segments, and may reread the current appendable segment. Tail catch-up
can list the directory but never reopens or decodes earlier completed segments.

Keep the same reader for `Tail(ctx, consume)`. It registers a capacity-one
wakeup before catching up from `LastEntryID`, then waits for successful appends
through that same `Store`. Catch-up recovers commits made during replay or
before registration; buffered, coalescing notifications cover commits during
catch-up and the read-to-wait transition. Notifications indicate work, while
the log supplies every newer committed entry in order. Both `Append` and
`AppendWithMetadata` return the successful committed ID and notify only after
updating the append cursor; failures return ID zero and publish no success
notification. Another `Store` or an external writer supplies no wakeup; external
writers and deletion during consumption are unsupported.

Reader methods require serialized caller use. `Walk` and `Tail` execute
synchronously, check cancellation between bounded segment reads and before
callbacks, and `Tail` also waits on context cancellation. Cancellation returns
`ctx.Err()`; callbacks must return promptly or honor the context while blocking.
No decoded buffers remain on the reader after return or while tailing waits,
and tail registrations are removed on every exit. Consumer processing and
waiting run outside the store mutex (see [Concurrency](history-package.md#concurrency)).

`Forward` validates conversation-ID shape and shares the authenticated-conversation
precondition above. Each listing and segment read re-resolves directory containment;
listing filters regular files and the read rechecks the leaf. Read/listing failures,
containment violations, oversized reads, corruption in discovered prefixes or
fully read segments, and unknown versions return explicit errors. A positioned log
that disappears returns a read error. `decodeSegment` retains its trailing-write
tolerance, including an incomplete header or final entry; complete corruption is
never skipped within a fully read segment to report successful completion.

Committed/durable in the reader retains the append contract: completed writes
and recovered complete entries survive process restart. Reading performs no
fsync; machine-crash durability requires the explicit `Store.Sync` barrier
described under [Shape](#shape). The storage version, backward pages and opaque
cursors are unchanged ([failed-write recovery](history-package-failed-write-recovery.md)).

### Ordering validation and verification

Stored-ID validation must survive skipped sealed segments across walks and tail
catch-ups. A single uninterrupted walk rejects duplicate/decreasing segment
boundaries even when separate calls would silently skip them. `ForwardReader`
therefore retains a validation boundary independently of the caller's resume ID
and delivery progress: it excludes the current segment while that segment is
reread or retryable, then includes it once sealed and consumed. Using the last
delivered ID instead would reject valid rereads or miss corruption below an
arbitrary resume ID. `TestForwardOrderingAcrossCalls` and
`TestForwardOrderingResumeAndRetry` cover both sides of this boundary.

Initial selective positioning seeds `boundaryID` from the preceding nonempty
range's first ID, never V; subsequent calls retain the stored-order boundary.
`TestForwardResumeRejects` covers invalid first-ID ranges, filtered entries and
overlap beyond H. `TestForwardResumeRanges` and `TestForwardResumeFreshness`
exercise missing IDs, empty/torn segments and changed membership/sizes.
`TestForwardPositionedContinuity` carries the same reader through callback retry,
successive Walk calls and Tail, including beyond-end resumes and appends before
registration and during catch-up. Successful deliveries are neither omitted nor
duplicated.

`TestForwardBoundedChunksAndPosition` damages every completed segment, including
the sealed final segment: damaging only the oldest leaves a needless reread of
the final one undetected. `TestForwardCommitNotifications` obstructs the segment
the next append will actually use; a full active segment rolls, bypassing an
obstruction at its old filename. These fixtures prove positioned reads and
failed-append silence rather than relying only on successful delivery counts.

## Memory transcript view

[`startMemoryTranscripts`](../../../cmd/pyry/memory_transcripts.go) consumes
registered hosted-chat history to produce Markdown with delivered users and
completed main replies, as described in
[Recent conversation transcripts](../../guide.md#recent-conversation-transcripts).
`runSupervisor` supplies saved settings and the `resolveStartupWorkspaceBase`
result; `resolveEffectiveMemory` validates the effective destination. Absent
settings create no storage, and invalid settings disable export with fixed,
content-free diagnostics. Export is independent of clients, capture schedules
and search installation.

Discovery runs immediately and every `memoryTranscriptInterval` (ten seconds),
starting a serialized worker per registered conversation, including new chats.
Each worker owns a positioned `ForwardReader`, a `thread.Fold`, raw user/text
timestamps and private `memoryReplyEvidence`. It polls with `Walk` to catch commits
from other store instances that cannot wake this store's `Tail`, including commits
during replay or export.
The scheduling contract is user text within 60 seconds of persistence and main
reply text within 60 seconds of its qualifying recorded `turn_end`, with readable
history and writable storage. Replay, publication and worker joins stay outside
chat delivery/publication paths; startup cleanup cancels and joins discovery and all
workers. History or fold failure retries from a fresh reader without publishing
an incomplete replay.

### Recorded attribution and stable identity

[`memoryTranscriptReader.files`](../../../cmd/pyry/memory_transcript_fold.go)
selects delivered `user_message` items with nonzero `Order`, excluding explicit
`NoChild` records. It decodes only `text` from inert `Content`, preserving recorded
whitespace; normalized `Summary` and whole payload JSON cannot supply the text.
Completed main `assistant_message` runs additionally require the scoped evidence
below. Reasoning, tool inputs/results, child-agent traffic, injected/reset-wrap-up
instructions and their assistant-only replies, system/status events and background
capture executions supply no conversational export text. Registry membership and
title are display inputs; the current binding supplies no provenance. Attribution comes
from saved delivery metadata or the fold's
[recorded successor fallback](thread-package-main-thread-folding.md#recorded-provenance-and-successor-fallback).
Unresolved attribution remains explicitly unknown, grouped in a namespace
separate from named sessions even when an opaque routing ID resembles that
namespace.

User message identity is conversation ID plus delivery `Item.Order`; source time is
the corresponding raw delivery `Entry.TS`, retained by `memoryTranscriptReader.feed`.
Using `Item.ID` would change an already exported message when a later
[acceptance outcome](thread-package-main-thread-folding-sources.md#accepted-sends-and-durable-outcomes)
suppresses the standalone delivery row in favor of the acceptance item. A
persisted delivered message is independently eligible while its linked outcome
is absent; later reconciliation yields the same single exported message,
identity and timestamp. Queued, dropped and lost acceptances export no text.

Assistant identity is conversation ID plus the run's creating entry ID; its
timestamp is that first text entry's raw `Entry.TS`. Deltas extending that run,
tools and completion cannot move either. Text after a closed run creates a new
row with its own first-text identity/time; subsequent deltas can extend that new
row even after the lifetime's terminal. Assuming all late text extends the
pre-terminal row would impose a false identity contract. See
[main-work text splitting](thread-package-main-thread-folding-lifecycle.md#main-work-source-entries).
Render only `Content.text`: all main text runs
before/between/after tools retain exact whitespace and coalesce deltas once, in
durable creation/delivery order. `Item.Rev` can advance for closure without any
new text, so it cannot serve as a text watermark.

The transcript basename hashes structured conversation, recorded routing session,
replacement-generation start and unknown-attribution state. Titles, text and
opaque session IDs never form paths. Replaying surviving history reconstructs
the same transcript/message identities and complete text without duplicates.

### Scoped reply completion and recorded ownership

`memoryReplyEvidence.feed` tracks recorded source (agent and session), turn and
opening generation (explicit or implicit), with boundary scope for legacy evidence.
Tagged and metadata-free evidence never join through displayed successor
fallback. Every valid explicit opening creates a fresh lifetime, including after
terminal-only evidence; reused turn IDs cannot carry earlier completion into it.
A supplied recovery opening reference must match its original source and turn,
without falling back to the current scope. Malformed/foreign facts, child endings
and unresolved references release no reply. See
[main-work joins](thread-package-main-thread-folding-lifecycle.md#scoped-joins-pending-outcomes-and-closure).

The first valid terminal in entry order owns the lifetime. `turn_end` permits
export, including error/cancellation endings; `main_turn_interrupted` permanently
withholds its text even if a conflicting ending follows. All runs remain withheld
until qualifying completion: `Fold.closeText` also closes runs on tools or user
delivery, so `Item.Status`/`Active` cannot certify it. Restart alone supplies no
completion. `memoryReplyEvidence.rows` uses final fold ownership for main text,
but immutable recorded openings and terminals for permission to export it.

Each reply lifetime must associate with a delivered conversational user in its
own recorded turn interval and source scope. Consuming the pending exchange on a
new lifetime prevents an earlier completed exchange from qualifying assistant-only
wrap-up work. Logical replacement clears unmatched pending exchanges, including
when switching back to a previously used source. Established lifetime-to-user
associations survive for late predecessor text; tagged pending exchanges survive
background unloading or same-session restart, which do not close the session.
The associated user's existing group anchors late replies to the original logical
generation rather than a reused routing ID.

### Causal main-work acceptance

Main-work acceptance must be decided at each history position from validated
recorded ownership, before later facts alter it. Agent/Task public rows retain
their creating ID across turn reuse and can change ownership after parent repair;
neither a new row nor a final snapshot proves whether a launch opened a main
lifetime. Classifying it incorrectly can hide the opening targeted by a valid
interruption and then export withheld text. Child ownership uses recorded source
and producer lifetime, or legacy boundary scope, with ordinary child calls also
isolated by turn. Runtime opening/interruption facts retain their turn authority
despite incidental call IDs; raw-parented runtime facts remain inert. Check event
kind and applicable DTO/identity fields before any call-map mutation: a rejected
runtime fact must not transfer an ordinary creation into a child lane.

`memoryReplyEvidence.ordinaryWork` distinguishes ordinary call creation from
call-scoped observations. Renaming a reused Read/Agent/Task call to Bash does not
create an original shell observation. Validate shell creation with its main DTO;
applying the durable agent DTO instead can reject unrelated null extensions and
lose valid evidence. Both invented and lost observations can change recovery
ownership and bypass interruption withholding. Promotion may adopt only pending
calls and created ordinary shells from the same uninterrupted pre-lifetime source
scope; destination collisions, created Agent/Task owners and boundary-separated
evidence cannot transfer. See
[background shell ownership](thread-package-agents-and-background-work.md#background-shells).

`memoryReplyEvidence.agentFact` validates task-to-call links before changing
ownership or promoting a lifetime. `recoveryKey` requires every supplied call/task
reference to agree on its original recorded group. Valid recovery enriches that
group's parent ownership without replacing the active producer lifetime; duplicate
observations cannot replace the first reference identity. An invalid fact must
leave both ownership and active-lifetime state intact, rather than merely being
discarded after mutation.

### Closure and exported-message boundaries

A divider with distinct recorded predecessor and successor routing IDs closes
the predecessor and starts a new logical generation, including when a previously
used routing ID returns. The fold's
[raw/legacy pairing](thread-package-main-thread-folding.md#boundary-pairing-and-legacy-scope)
counts companion dividers once. Idle/capacity eviction, disconnect, restart
without a recorded successor and same-ID respawn do not establish closure;
a restart with a recorded distinct successor does. Unknown groups without a
proven predecessor boundary stay open.

`Closing entry` records the fixed closure ID and is absent while open.
`Last delivered entry` independently records the greatest delivery `Order`
represented by a user row. `Last text entry` records the greatest entry
contributing exported user or eligible assistant text. `memoryReplyEvidence.rows` retains
nonempty raw delta IDs within each run's creating-ID interval instead of using
revisions; completion/tool/status facts alone advance neither text nor delivery.
Late eligible predecessor text, including a delta extending an already exported
assistant run, updates only its file and text boundary; late user delivery also
updates its user-delivery boundary. Both can exceed the closing ID without
moving closure or changing the successor's contents or closure. Treating closure
as the exported-message watermark would miss late text and leave an older
downstream capture receipt looking current.
Open transcripts retain the complete session without age or intermediate-capture
cutoffs; closed files remain available.

### Derived publication and storage confinement

Each successful replay renders complete snapshots, independently of publication
progress. Missing files rebuild; publication failure preserves the previous
complete file and retries the full snapshot on a later pass without skipping text.
[`publishMemoryTranscript`](../../../cmd/pyry/memory_transcript_files.go) writes
a private temporary file in the destination directory, syncs and closes it, then
atomically renames it. Directory traversal and leaf operations use held directory
descriptors without following symlinks. Unsafe existing targets, including
symlinks, nonregular files, shared-mode files and hard links, are rejected. New
directories are 0700 and files 0600.

Canonicalizing a destination can erase evidence of a storage symlink before a
no-follow publisher sees it. Startup therefore also compares the effective path
with the fixed transcript location beneath the canonical service-user home;
a redirect disables export. This keeps derived publication out of source history,
vault notes and additional roots. Neither replay nor export modifies history,
and diagnostics expose only fixed event/reason strings, never text, payloads,
paths or opaque IDs.

### Testing replay and publication

`TestMemoryTranscriptReplay` covers identity/timestamp stability across split
reconciliation and exact text; `TestMemoryTranscriptSessions` covers pairing,
unknown attribution, replacements and late predecessor boundaries.
`TestMemoryTranscriptWorker` exercises production discovery/ticks, interleaved
Claude/Codex histories, direct-writer catch-up, reconstruction with unchanged
source bytes and joined cancellation during replay/publication.
`TestMemoryTranscriptActivation` covers inactive settings and storage redirects;
`TestMemoryTranscriptStorage` covers permissions, target rejection and interrupted
publication followed by retry.

`TestMemoryTranscriptReplies`, `TestMemoryTranscriptReplyTerminals` and
`TestMemoryTranscriptReplyScopes` cover exact runs, stable first-text identity/time,
first-terminal precedence and source/generation isolation.
`TestMemoryTranscriptReplyExclusions`, `TestMemoryTranscriptReplyBoundaries` and
`TestMemoryTranscriptReplyReplacementPermission` cover exchange qualification and
late predecessor reconstruction, including immutable closure and unchanged
successor files. Ordinary ownership/recovery matrices retain every source, tool,
ownership/recovery/validation and completion/interruption scenario.
`testReplyPartitions` selects the distinct union of whole-history replay,
single-entry replay and every possible single-cut two-chunk replay: at most n+1
partitions for n entries, with linear enumeration. `testReplyLauncherReplay`
creates a fresh reader per partition and applies the same independent assertions
at every selected intermediate prefix and the final prefix. These check accepted
opening/user identity, first-terminal precedence, permanent interruption
withholding, fold closure, first-text identity/time, source/generation isolation
and separate delivery/text boundaries. Child-text controls beside runtime
rejection prevent a guard from silently disabling legitimate transfer.

`memoryTranscriptReader.feed` reduces entries individually regardless of chunk
size; partitions change the observable prefixes. Counting scenarios alone hides
exponential replay work. Bound partition selection rather than caching exporter
snapshots, which would bypass chunk replay and could certify equally incomplete
views. The helper's `replay-work` summaries report completed partitions, replayed
entries and checked prefixes separately for each completion/interruption control.

Only `PYRY_TRANSCRIPT_EXHAUSTIVE=1` enables all original 2^(n-1) partitions with
the same readers and assertions. Ordinary checks need no opt-in. Run the full
hermetic proof from the repository root, without credentials, agent execution or
the live suite, under Go's unchanged default ten-minute package timeout:

```sh
PYRY_TRANSCRIPT_EXHAUSTIVE=1 go test -count=1 -json -run '^(TestVerifier(RepeatedMainLauncher|RejectedRuntimeTransfer)|TestMemoryTranscriptReply(ReusedLauncherOwnership|ShellObservation|RecoveryOwnership|RecoveryValidation))$' ./cmd/pyry
```

At `e097c4b303f6d43222bef01e5ff8810e2de62cdc` on pyrybox's Linux amd64 container
`ced36cb6ca66`, this command passed without race in 129.585 package seconds /
140.997 command seconds, exercising 594 helper scenarios and 92,544 partitions
([verified execution evidence](https://github.com/pyrycode/pyrycode/pull/3179#issuecomment-6106380990)).

Replay absence assertions must isolate the exchange under test: an earlier valid
delivered exchange can already own the same transcript before a chosen cut.
The controlled-tick `TestMemoryTranscriptReplyWorker` checks withholding followed
by production publication within 60 seconds. `TestMemoryTranscriptPersistedReplay`
remains enabled in ordinary checks and extends that gate through persisted
callbacks across `history.MaxPageEntries`,
interleaved chats and separate-Store commits during replay/publication.
`TestMemoryTranscriptPublicationRecovery` also remains enabled and checks
later-tick retry on the same worker, preservation of prior complete files and
joined replacement/reconstruction. These persisted worker/publication proofs
are separate from the in-memory partition matrices.

A tick send proves only discovery consumed it, and `beforeRename` fires before
replacement completes. Hold all participating workers at that hook before
inspecting staged files, then release and await exact disk contents; notification
counts alone can falsely certify publication. `testTranscriptGate.publish` uses
this ordering. Specify complete rows, whitespace, first-text identities/times and
delivery/text/closure boundaries independently, as `testTranscriptExpected` does,
before comparing chunked and uninterrupted reconstruction: two exporter snapshots
can agree on the same omission. Snapshot source paths and bytes after fixture
commits finish so recovery assertions also detect source-history changes.
