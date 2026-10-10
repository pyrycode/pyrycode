# `internal/history` — entry shape and metadata

Part of [the history package overview](history-package.md).

## Shape

Four production files (`log.go`, `segment.go`, `cursor.go`, `forward.go`), a
leaf importing only `internal/conversations` (for `ValidID`/`ConversationID`)
and stdlib —
never the wire-payload types, never decoding a payload, the same posture
[`eventring`](eventring-package.md) has. `New(instanceDir)` builds
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
func (s *Store) Page(convID conversations.ConversationID, cursor string, limit int) (Page, error)
func (s *Store) LatestEntryID(convID conversations.ConversationID) (uint64, error)
func (s *Store) LatestDisplayableEntryID(convID conversations.ConversationID) (uint64, error)
func (s *Store) LogDir(convID conversations.ConversationID) (string, error)
func (s *Store) Forward(convID conversations.ConversationID, afterID uint64) (*ForwardReader, error)
func (r *ForwardReader) Walk(ctx context.Context, throughID uint64, consume func([]Entry) error) error
func (r *ForwardReader) Tail(ctx context.Context, consume func([]Entry) error) error
func (r *ForwardReader) LastEntryID() uint64
```

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
first touch, deliberately not `eventring`'s ring-wide shape: every cursor here
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
containment violations, oversized segments, corrupt complete entries, non-increasing
or zero stored IDs, and unknown versions return explicit errors. A positioned log
that disappears returns a read error. `decodeSegment` retains its trailing-write
tolerance, including an incomplete header or final entry; complete corruption is
never skipped to report successful completion.

Committed/durable retains the append contract: completed writes and recovered
complete entries survive process restart. There is no new fsync or machine-crash
guarantee, and the storage version, backward pages and opaque cursors are unchanged
([failed-write recovery](history-package-failed-write-recovery.md)).

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

`TestForwardBoundedChunksAndPosition` damages every completed segment, including
the sealed final segment: damaging only the oldest leaves a needless reread of
the final one undetected. `TestForwardCommitNotifications` obstructs the segment
the next append will actually use; a full active segment rolls, bypassing an
obstruction at its old filename. These fixtures prove positioned reads and
failed-append silence rather than relying only on successful delivery counts.

## Memory transcript view

[`startMemoryTranscripts`](../../../cmd/pyry/memory_transcripts.go) consumes
registered hosted-chat history to produce the user-only Markdown view described
in [Recent conversation transcripts](../../guide.md#recent-conversation-transcripts).
`runSupervisor` supplies saved settings and the `resolveStartupWorkspaceBase`
result; `resolveEffectiveMemory` validates the effective destination. Absent
settings create no storage, and invalid settings disable export with fixed,
content-free diagnostics. Export is independent of clients, capture schedules
and search installation.

Discovery runs immediately and every `memoryTranscriptInterval` (ten seconds),
starting a serialized worker per registered conversation, including new chats.
Each worker owns a positioned `ForwardReader`, a `thread.Fold` and delivery
timestamps. It polls with `Walk` to catch commits from other store instances
that cannot wake this store's `Tail`, including commits during replay or export.
The scheduling contract is eligible text within 60 seconds with readable history
and writable storage. Replay, publication and worker joins stay outside chat
delivery/publication paths; startup cleanup cancels and joins discovery and all
workers. History or fold failure retries from a fresh reader without publishing
an incomplete replay.

### Recorded attribution and stable identity

[`memoryTranscriptReader.files`](../../../cmd/pyry/memory_transcript_fold.go)
selects delivered `user_message` items with nonzero `Order`, excluding explicit
`NoChild` records. It decodes only `text` from inert `Content`, preserving recorded
whitespace; normalized `Summary` and whole payload JSON cannot supply the text.
Assistant output, notices, injected instructions and background capture work do
not become conversational user messages. Registry membership and title are
display inputs; the current binding supplies no provenance. Attribution comes
from saved delivery metadata or the fold's
[recorded successor fallback](thread-package-main-thread-folding.md#recorded-provenance-and-successor-fallback).
Unresolved attribution remains explicitly unknown, grouped in a namespace
separate from named sessions even when an opaque routing ID resembles that
namespace.

Message identity is conversation ID plus delivery `Item.Order`; source time is
the corresponding raw delivery `Entry.TS`, retained by `memoryTranscriptReader.feed`.
Using `Item.ID` would change an already exported message when a later
[acceptance outcome](thread-package-main-thread-folding.md#accepted-sends-and-durable-outcomes)
suppresses the standalone delivery row in favor of the acceptance item. A
persisted delivered message is independently eligible while its linked outcome
is absent; later reconciliation yields the same single exported message,
identity and timestamp. Queued, dropped and lost acceptances export no text.

The transcript basename hashes structured conversation, recorded routing session,
replacement-generation start and unknown-attribution state. Titles, text and
opaque session IDs never form paths. Replaying surviving history reconstructs
the same transcript/message identities and complete text without duplicates.

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
represented. Late predecessor text updates its own transcript and delivered
boundary, even beyond the closing ID, without moving closure or entering/closing
the successor. Treating closure as the exported-message watermark would miss
late text and leave an older downstream capture receipt looking current.
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

`TestMemoryTranscriptReplay` covers identity/timestamp stability across split
reconciliation and exact text; `TestMemoryTranscriptSessions` covers pairing,
unknown attribution, replacements and late predecessor boundaries.
`TestMemoryTranscriptWorker` exercises production discovery/ticks, interleaved
Claude/Codex histories, direct-writer catch-up, reconstruction with unchanged
source bytes and joined cancellation during replay/publication.
`TestMemoryTranscriptActivation` covers inactive settings and storage redirects;
`TestMemoryTranscriptStorage` covers permissions, target rejection and interrupted
publication followed by retry.
