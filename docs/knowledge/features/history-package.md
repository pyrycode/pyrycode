# `internal/history` — versioned, segmented per-conversation log

The durable, append-only, per-conversation message log the daemon writes as it
fans envelopes out, read newest-first by walking backwards on demand. Landed
in #2112 as a storage floor with no caller; #2114 gave it its first two —
the interactive emitter's `emit` chokepoint and session transitions — and
\#2115 gave it a third: the operator's own typed message, written from
the safe `msgqueue.QueuedMessage` projection. Stream Claude delivery commits
at echo placement or idle fallback; Codex/no-stream delivery commits at write
confirmation (see [Producers](#producers-2114-2115) below). `#2116` is the
wire-serving consumer,
`#2113` declares the cursor as an opaque wire string. Spec:
[`specs/architecture/2112-conversation-history-log.md`](../../specs/architecture/2112-conversation-history-log.md).

## Why not claude's transcripts

Rejected 2026-09-04, and recorded because it is the more obvious design and
will otherwise be re-proposed: claude's transcript format is not a contract
this repo controls or versions, old transcript files are never rewritten so a
serving path over them means supporting every shape that ever existed
permanently, and it would bind history to one harness when `internal/acp` is
already a second front door deliberately decoupled from the mobile envelope
types. The daemon already produces the wire mapping every turn — writing what
already flows is less new code than reading a foreign format back.

## Why segments, and why the premise for them was wrong once

Retention is **none** (deliberately — endless for now, not deferred by
accident) and access is **newest-first, walking backwards on demand**. Those
two facts, not an I/O cost argument, are why the log is segmented rather than
one growing file: the newest segment stays small, older segments are opened
only when a client scrolls into them, segment boundaries double as the index
so no sidecar file is needed, and a future retention policy becomes a file
delete rather than a rewrite. An earlier draft justified segmentation by
claiming a plain append-only file can't be opened cheaply at its end — false
(seek to EOF, read one block backwards) — and was corrected before it shipped.
**A segmentation design should be justified by the access pattern it serves,
not by a plausible-sounding I/O claim that happens to point the same way.**

## Shape

Three production files (`log.go`, `segment.go`, `cursor.go`), a leaf importing
only `internal/conversations` (for `ValidID`/`ConversationID`) and stdlib —
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
```

`Store.LogDir` (#2673) returns the absolute, symlink-resolved, existing log
directory for a conversation. It validates the ID and uses
`resolveDir(convID, false)` under the store's mutex, rechecking exact containment
on every call; it creates nothing, caches no path and refuses a missing path or
a path that is not a directory. A sibling-conversation redirect is refused just
like a redirect outside the instance root (see [Directory resolution](#directory-resolution-re-resolve-every-call-dont-cache-the-answer)).

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
[the unread watermark](#unread-state-uses-a-separate-lazily-recovered-watermark-2954)).
Callers must not mutate payload or metadata during an append; the store retains
neither caller pointers nor payload bytes after the call, only scalar cache
values. These storage facts support
[ADR 042's history-backed thread](../decisions/042-daemon-built-thread.md);
producer adoption belongs to #2966 and legacy transport filtering to #2965.

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

## The cursor does no arithmetic on the number it doesn't trust

Mint: `base64url("1." + convID + "." + segment + "." + offset)`. Parse
refuses one sentinel, `ErrInvalidCursor`, for every one of: bad encoding, wrong
field count, unknown version, a non-numeric segment or offset, an id that
fails `ValidID` **or names a different conversation than the one being read**,
a segment number absent from that conversation's directory, and an offset that
is not the start of an entry line in that segment.

That last check is the one that matters, and it is deliberately **not**
computed. The segment is read and its entry-line start offsets are collected;
the cursor's offset must equal one of them or be refused. This is
`eventring`'s own #2022 lesson carried into a second package: a boundary
inferred from what an untrusted number *implies* (there, `afterID+1` assuming
contiguous ids) is only as sound as the assumption behind the inference, and
that assumption is exactly the thing an attacker controls. Validating against
the real bytes on disk instead of computing from the claimed position is what
makes `math.MaxUint64` simply "not a boundary" here, rather than a wraparound
that reaches the wrong branch — see
[eventring-package.md § `After`](eventring-package.md#after--the-three-way-replay-contract).
**Any future cursor-, offset- or id-shaped untrusted input in this codebase
should default to this discipline, not to arithmetic guarded by a range
check.**

No HMAC: authenticating the cursor was considered and rejected because it buys
nothing the checks above don't. What a forged cursor can name is
`(conversation, segment, offset)` — the conversation must already be the
caller's own, the segment is a fixed-width `uint64` with no traversal
expressible, and the offset must be a real entry boundary. The cursor is **not
a capability**; it names a position inside a conversation the caller must
already be authorised for.

## Directory resolution: re-resolve every call, don't cache the answer

The first draft cached the symlink-resolved history directory per
conversation to save the resolve cost on every append. That was rejected: it
borrows `attachments.EnsureDir`'s accepted check-then-use window — sound
because it spans *one call* and needs write access inside the daemon's own
0o700 state directory to exploit — and silently widens it to the whole daemon
process lifetime. A symlink planted after the first resolve would redirect
every later append and read for as long as the daemon runs. Re-resolving costs
a handful of `lstat`s and is cheaper than the argument for keeping the cache
would have to be. `convLog` therefore caches only the append cursor (active
segment number, its size, the last durable entry id) and the filtered watermark
— never the path.

Containment is a full-path **equality** check against the resolved instance
directory, not a `filepath.Rel`-style "is it under the root" test — equality
is what refuses a history directory symlinked at a *sibling* conversation,
which stays inside the instance directory and would pass a containment test
built the other way. Two more leaf protections exist because containment of
the directory is not containment of the files in it: the append open carries
`syscall.O_NOFOLLOW` (confirmed clean on `linux/amd64`, `linux/arm64` and
`darwin`, so the `os.Lstat` fallback named in the spec was never needed), and
the reader skips any non-regular directory entry rather than following it.

## A cleanup on a failed write is not the guarantee it looks like

This is the lesson that cost two rework rounds, on two branches of the same
`if fresh` in `writeSegment`, and it generalizes past this package.

`writeSegment` creates a fresh segment with `O_CREATE|O_EXCL` before writing
it. The first version cleaned up nothing when the write itself failed: an
ordinary `write(2)` error (ENOSPC, EDQUOT, EIO — a full disk, not a machine
crash) left a zero-length segment on disk. `decodeSegment` read it back as
`ErrUnknownVersion`, and because `load` always reaches the newest segment
first, **that one failed write made the conversation's whole history
unreadable and unappendable forever** — including every entry that had
already landed successfully. The fix that looked complete — unlink the file
when the write or close fails — is necessary but is *not* the guarantee: an
`os.Remove` can itself fail, and a crash between the create and the write
leaves the identical residue with no cleanup to run at all. The property that
actually has to hold — a conversation is never permanently unreadable — has to
rest on the *reader* tolerating whatever a failed write leaves behind, not on
the *writer* successfully cleaning up after itself. `load` now treats any
segment it cannot safely resume writing to (no header, a header torn
mid-write, or a torn entry line) as one to roll past, and `listSegments` was
already a listing rather than a numeric probe — for the unrelated reason of
tolerating retention deletes — so the numbering gap a roll-past leaves was
already a supported shape.

The second rework found the same defect on the *other* branch: a short write
into the **active** (non-fresh) segment — the branch nearly every append
actually takes — leaves a torn final line with no cleanup path at all, and the
first design's own text asserted this was "unreachable while the process
lives... only a machine crash", which is false (`os.File.Write` returns the
bytes already transferred alongside the error). The write-side fix is a
truncate back to the pre-call length, read from `Stat` on the descriptor the
call already opened — **never from the in-memory belief of the size**, since a
truncate to a merely-remembered length can destroy entries that genuinely
landed. The read-side fix is the same tolerance generalized: `ErrCorruptSegment`
now means a *complete* line that fails to decode, and a torn tail is dropped
rather than refused, because those bytes belong to an entry whose `Append`
already returned an error to its caller — refusing them a second time costs
every entry that *did* succeed.

**Belt-and-suspenders means different fabric, and both halves matter for a
different reason.** The write-side undo (remove the orphan / truncate the
overrun) *prevents* the bad state from persisting when the failure is
survivable. The read-side tolerance *survives* the bad state regardless of
what produced it — a failed cleanup, or a crash with no cleanup to run. Ship
only the first and a merely-unlucky `os.Remove` reintroduces the whole defect;
ship only the second and every failed write costs a segment's worth of
already-durable entries for no reason. **For any append-only store, the
durability question is not only "did the bytes land" but "what does a partial
failure leave behind, and can the next reader still make progress past it" —
and that question needs to be asked of *every* branch that writes, not just
the one a first review happened to land on.** This is carried forward
explicitly for `#2114`/`#2115`, whose append call sites are this package's
first production callers.

**Testing a failure that can't be induced.** Neither the ENOSPC nor the short
write above could be triggered from a unit test without adding a fault seam to
production code (`RLIMIT_FSIZE` was tried as an ENOSPC stand-in and measured
not to be enforced on this machine's darwin/APFS). Both are tested instead by
planting the *residue* directly on disk — a zero-length segment file, a
segment whose last line has no trailing newline — and asserting the store
still reads and still accepts appends. **When a failure mode can't be
triggered, test the state it would leave behind, not the syscall that
produces it**; that tests the actual guarantee (the reader tolerates this
shape) rather than a specific trigger for it.

## Concurrency

No goroutine is spawned and there is no `Close` — the store is passive, like
`Ring`. One `sync.Mutex` guards the per-conversation state map, the read
counters, and both append paths and `Page`'s storage work, held only around
bounded file I/O and never across a channel op or another lock. Reads take the lock
rather than racing appends: the alternative (lock-free reads plus a
torn-trailing-line tolerance on the read path) buys a shorter hold at the cost
of reasoning about partial-write visibility across every filesystem the daemon
runs on, and holding a leaf mutex across a bounded read was judged cheaper
than being wrong about that.

**Measured (#2114), against a fixed decision rule.** `Append`'s cost at the
interactive emit chokepoint is dominated by the directory-resolution
syscalls, not the write: `no_store` costs 1 688 ns/op and 3 allocs/op,
`store` costs 51 377 ns/op and 144 allocs/op — a delta of ≈ 49.7 µs and +141
allocations, essentially all of it the ~7 syscalls (`Abs`, `MkdirAll`,
`EvalSymlinks` twice, the `Lstat` walk) plus open/write/close under the
mutex. The allocation count, not the latency number, is what shows the cost
is structural to the store's re-resolve-every-call design rather than an
artifact of one filesystem — `ns/op` alone would not have distinguished the
two. The append stays synchronous: the chokepoint's sustained rate is set by
the *unbatched* envelope variants (turn_state, tool start/update,
turn_end — assistant text deltas are already coalesced behind a 250 ms
window and never reach this path at token rate), tens per turn, so a turn
pays single-digit milliseconds of added serialised time in total — two
orders of magnitude below the 250 ms window. This was measured with one
producer holding the lock alone. #2115 landed a third producer on the
delivery path without re-measuring: contention across three producers
sharing this **single, package-wide** `sync.Mutex` — not a per-conversation
one, so `Append` on one conversation briefly gates another's producer too —
remains open for whichever ticket next cares about append latency under
load.

## Producers (#2114, #2115)

Three call sites in `cmd/pyry` append through one seam,
`appendConversationHistory` (`cmd/pyry/conversation_history.go`): the
interactive emitter's `emit` chokepoint
(`cmd/pyry/interactive_turn_v2.go`), session transitions' `broadcast`
(`cmd/pyry/session_transition_v2.go`), and (#2115) `newOperatorMessageHistory`
(`cmd/pyry/operator_message_history.go`), used by queued stream placement and
`msgqueue.Config.OnDelivered`.
The first two already resolve the four values `Append` wants — conversation
id, wire type, marshalled payload, one hoisted timestamp — for the ring
append or the fan-out itself, so their log append needed no new mapping, only
a nil-guarded call before the per-conn loop in each. The third resolves them
from a safe `msgqueue.QueuedMessage`, available before the write through
`msgqueue.DeliveryMessage` and again at confirmation through `OnDelivered`,
not from an envelope in flight (see below).

**Carry the append result, not a second lookup or another counter (#2861).**
`appendConversationHistory` returns the successful `Store.Append` id as an
immutable `*uint64`, shared by every direct live recipient as
`Envelope.HistoryEntryID`. The operator commit carries it with the safe payload
and placement timestamp through `operatorMessage` to
`operatorMessageEmitterV2.broadcast`; reconstructing it at broadcast would lose
the association with that exact append. Append once before fan-out, even without
recipients. A transition has no ring id, and an operator emitter needs no ring
to carry this durable identity. The connection counter and ring cursor cannot
substitute for the stored per-conversation id. See
[envelope identities](protocol-package-types-envelope.md#replay-cursors-and-durable-read-marks)
for read-mark use and reconnect replay's retained identity.

**History must finish before ring publication (#2909).**
`interactiveTurnEmitterV2.emit` obtains the append result before
`Ring.AppendWithHistoryID` publishes the complete event; publishing first and
adding metadata later lets concurrent replay observe an incomplete record.
`operatorMessageEmitterV2.broadcast` passes the already-committed id through the
same ring seam without another history write. Both paths retain the id even with
no live recipients. Replay uses that original identity and never appends history;
absent/failed storage leaves zero ring metadata and an omitted wire key. Channel
posts retain their missing-id behavior, and transitions remain outside the ring.
See [ring publication](eventring-package.md#concurrency).

**Why this producer reads `text`, never the delivered payload.** Since #2038
a queued message carries two strings — `text` (client-readable) and
`delivery` (what reaches claude's stdin, which for an attachment-bearing
message names an on-host path `docs/protocol-mobile.md` § Error codes
forbids serving to a paired device). `newInboundDeliver` sees only
`delivery` in its payload argument. `OnDelivered` and the attempt-context
accessor `DeliveryMessage` carry `QueuedMessage` instead, which declares no
`delivery` field, making the omission structural rather than a filter this producer could forget.
Full detail:
[msgqueue-package.md § Delivered notification (#2115)](msgqueue-package.md#delivered-notification-2115).

**The stored entry keeps its attachment ids too, by the same structural argument (#2596).** `QueuedMessage.AttachmentIDs` is the ids a `send_message` named, as `internal/relay/handlers.resolveAttachments` resolved them — deduplicated, each past the canonical-shape check — copied onto the queue record independently of `delivery`. This producer sets `protocol.MessagePayload.AttachmentIDs` straight from `msg.AttachmentIDs`; since it still reads only `QueuedMessage`, which has no `delivery` field, no on-host path is reachable here no matter what changes upstream. A message that named none stores nothing (`omitempty` elides the key), so every pre-#2596 entry a client already decoded is untouched.

**The stored entry also keeps who sent it and when they tapped Send (#2704), by the same `QueuedMessage`-only structural argument.** `DeviceName` and `ClientVersion` come straight off `msg.DeviceName`/`msg.ClientVersion` — the paired device record's name and the admitted app version off that connection's hello, both captured by `internal/relay/handlers.SendMessage` at enqueue time, not re-derived here. `ClientSentAt` is `msg.ClientSentAt.Format(time.RFC3339Nano)` when non-zero, else `""` (`omitempty` elides the key) — the daemon's own re-formatting of a value the handler already parsed out of the client's optional `client_sent_at`; this producer never sees the client's raw string, the same way it never sees an on-host attachment path. Every pre-#2704 entry, and every entry from a connection with no paired-device record, stores none of the three.

**Confirmation-only recording is not gap-free across shutdown.** `msgqueue`'s drain
tests `ctx.Err() != nil` before its confirmed-delivery branch, so a delivery
that confirms in the same instant the daemon shuts down leaves the head
queued and fires neither `q.notify` nor `OnDelivered` — the message reached
claude's stdin but a producer relying only on that callback never runs for it.
Stream placement no longer waits for the callback, but it still requires the
drain to process an echo or fallback before shutdown. A client reading this
log after a restart should not assume it is gap-free across that boundary.

**Why the write point is the envelope, not `internal/turnevent`.** An
earlier draft proposed writing the log from `turnevent`'s representation.
That cannot work: `turnevent`'s variant set carries no operator message, no
conversation id, no session transition and no question batch — the operator's
own typed text goes `send_message` → msgqueue → delivery → the child's stdin,
and the content this producer stores and pushes always comes from
`QueuedMessage.Text`, never from `turnevent`. (**Since #2730, claude does echo
the delivered text back** under `--replay-user-messages`, as a digest-only
`turnevent.UserEcho` — see [streamsup-package-turn-io-envelope-write-stdout-parser.md
§ Replayed user echoes](streamsup-package-turn-io-envelope-write-stdout-parser.md#replayed-user-echoes-carry-a-digest-never-the-text-2730).
That echo carries no text at all, so it still cannot be this producer's
source; it only times a queued commit this producer was already going to
make — see below.) A log written purely from `turnevent` would hold
assistant text and tool rows and none of what the operator typed.

**Echo placement must own history, live publication and replay together
(#2730, #2820).** Committing history at an echo while handing the live push
to `operatorMessageEmitterV2.Run` on another goroutine still lets reply
frames overtake the user message in live arrival and replay event-id order.
Queue-backed stream Claude writes therefore prepare their safe commit before
writing, and `sendNowPlacement.echo` commits synchronously on the stream
drain, through `streamTurnSink.publishOperator` and the late-bound
`operatorMessageEmitterV2.broadcast` sharing the interactive emitter's ring.
One placement timestamp and payload serve history, ring and every connection.
The later `OnDelivered` callback acknowledges the managed entry; it cannot
delay, move or repeat placement, even after the answering turn has ended.

An echoed ordinary message opens its answering turn: after the preceding
`turn_end`, if any, and before its first `turn_state`, assistant or tool frame.
Without an echo, `sendNowPlacement.idle` commits a confirmed ordinary write
once before the closing event releases idle; fallback commands also run on
the drain. A late echo or callback adds nothing. Send-now retains placement
after the interrupted tool result or at the next turn's opening, and its
[carry grace window](streamsup-package-per-conversation-turn-busy-track-send-now-carry.md)
keeps the idle fallback from firing between turns. Codex/no-stream and
unregistered callers retain confirmation-at-write recording. A nil placement
commits immediately; legacy send-now callers without queue metadata still
use `expect`/`attach` and commit when both echo and commit are available.

**Match final write bytes, keep only safe queued content.**
`sendNowPlacement.write` serializes registration and the writer call per
conversation, after attachment/channel composition. It matches a private
digest of those final bytes, while the commit reads only `QueuedMessage`.
Equal payloads and repeated client `message_id` values match their own echoes
in actual write order, with distinct `queued_msg_id` values. The producing
session resolves the conversation; another conversation's echo cannot place
an entry here. Failed writes retire their registration, so a retry can record
once without recording the failed attempt. The echo text, composed bytes,
host paths and digest never become client/history payloads or log content.
See [Queue (v2)](../../protocol-mobile.md#queue-v2) for identity and timing,
and [the stream drain](streamsup-package-draining-turnevents-into-the-interactive-emitter.md)
for publication and write-outcome lock ordering.

**The store is nil-tolerant and a concrete pointer, never an interface** —
the same trap `session_transition_v2.go`'s `busy` field already documents:
a typed-nil store boxed into an interface is non-nil at the interface level
and would sail past a `== nil` guard. `Store` is not nil-receiver-safe
on either append path, so `appendConversationHistory` checks
explicitly rather than relying on a nil-receiver method, and every emitter
test that builds an emitter with no store keeps working unchanged. A
failing append never suppresses the wire emit or the ring append. It returns nil
metadata; an absent store also returns nil, omitting `history_entry_id` rather
than encoding null or zero. The failure is logged at `Warn`
with an `errors.Is`-derived discriminant (`invalid_id` / `invalid_payload`
/ `write`), never the error's own text: `history`'s errors format absolute
filesystem paths (`open segment %q`), and the log's own MUST-NOT-log-content
rule would be defeated by relaying them.

**Test-shape traps worth knowing before touching these producers
again:**
- **Separate the id sequences to prove provenance.** When history, ring and
  envelope counters coincide, substituting either live counter for the stored id
  stays green. `TestLiveProducers_HistoryEntryID` keeps history id 8, ring id 4
  and envelope ids 101–103 distinct, then compares the stored type, payload and
  timestamp across all three producers. Include no recipients, absent/failed
  storage and an operator without a ring: tests only with recipients miss skipped
  appends when nobody is connected, while requiring a ring misses the independent
  history-to-push handoff.
- **A shared-timestamp assertion needs three fan-out targets, not two.**
  `broadcast` used to mint `time.Now()` inside its per-conn loop; with only
  two connections, a per-conn timestamp and a correctly hoisted one are
  often indistinguishable, since the clock may not tick between two calls
  on a fast machine. Three targets compared for byte-identical payloads and
  `TS.Equal` is what makes the hoist's absence a deterministic test failure
  instead of an occasional flake.
- **A "nothing was appended" assertion is only meaningful below the drop's
  own log level.** The append failure path logs at `Warn` while
  `broadcast`'s pre-existing drops (unknown reason, unresolvable
  conversation) log at `Debug`; asserting an empty log buffer on a
  default-level handler only proves "no append was attempted" because the
  drop lines are filtered out separately. Two log statements at the same
  level would make that assertion vacuous.

## `LatestEntryID` shares `Append`'s cursor instead of a second counter (#2779)

`LatestEntryID` returns the raw newest durable entry id per conversation,
including status entries. The obvious-looking alternative — a dedicated counter
bumped at append — was rejected before it was written: `convLog.lastID` already
*is* that counter, recovered by `load` on first touch, so a second one would be two
sources of truth for the same fact, with no way to keep them from drifting the
first time one write path forgets to update both.

`LatestEntryID` instead validates the id, re-resolves the directory (uncreated —
a missing conversation reads `0`, never an error), and calls the same `load`
both append paths call, under the same `s.mu`. Once `load` has run once for a
conversation (`convLog.loaded`), subsequent raw lookups return `c.lastID` with
**no segment file opened**: recovery ran at first touch, and both append paths
and this read path share its result. `lastID` starts at `0` for an empty log.
On a cold cursor the first `LatestEntryID` call pays `load`'s existing tail-walk,
reading one bounded segment at a time and stopping at the newest segment with
an entry. Empty, header-only and incomplete trailing segments are walked past
as they are for an append; this method adds no new recovery logic.

**Guard allocation without doing arithmetic during recovery.** `load` retains
the last decoded ID directly. A next-ID cursor would still overflow on a
recovered `math.MaxUint64` even if append allocation checked the `2^53` bound;
the wrapped cursor could then permit reused IDs. `lastID` preserves raw reads
of oversized legacy IDs and lets both append paths refuse `lastID >= MaxEntryID`
before adding one, with no second counter. `TestAppendIDExhaustion` checks the
final successful ID, repeated refusals, stable watermarks and byte-for-byte
segment snapshots for warm and reopened stores.
`TestRecoveredOversizedIDRefusesAppend` also covers stored IDs above the bound
through `math.MaxUint64`, including an incomplete trailing segment that
exhaustion must not roll past by writing.

Containment is **not** cached across calls, on the same reasoning as
*Directory resolution* above: `resolveDir(convID, false)` re-resolves and
re-checks every call, even once the cursor is warm, so a symlink planted after
the cursor loaded is still caught on the next lookup.

### Unread state uses a separate, lazily recovered watermark (#2954)

`LatestDisplayableEntryID` supplies both `ListConversationsWithAgents`'s
`latest_entry_id` and `MarkConversationRead`'s clamp. `displayableEntry` first
uses explicit `Entry.Shown`: true counts even a normally excluded status type,
and false excludes even a normally counted type. With `Shown == nil`,
`displayableType` excludes exactly `turn_state`, `stall`, `api_retry`,
`compacting`, and `session_transition`; every other stored type counts,
including unknown and empty types. Missing, empty and entirely hidden logs
return `0`. A durable idle status with absent visibility after the last
reply must not keep unread set forever. Both handlers must use this same
watermark for `latest_entry_id > read_up_to` to clear after marking that reply
read; see the [wire contract](../../protocol-mobile.md#marking-a-conversation-read).
Hidden entries still appear in `Page` and `LatestEntryID`, consume durable IDs
and advance the raw cursor; filtering creates no second ID space and decodes
no payloads. Payload fields named `shown` or `session` have no effect on these
metadata rules.

After `load`, the first filtered lookup walks `listSegments` newest-first and
each `readSegment`'s entries backwards until a qualifying entry is found.
Reopening already-written logs therefore works even when mixed legacy and
metadata entries have trailing hidden entries spanning segments. The existing
decoder tolerates empty/incomplete tails and torn final lines. The scan holds
at most one segment's entries at a time and caches only successful results,
including entirely hidden zero, under `Store.mu`.
Both append cache updates and cold recovery use `displayableEntry`, so explicit
visibility has the same effect in warm and reopened stores. Subsequent lookups
open no segments. A successful displayable append through either API sets and
initializes the cache; a hidden append leaves it unchanged, including leaving
an uninitialized cache cold so older visible entries can still be recovered.
A failed segment write clears `convLog.loaded`, and the next `load` invalidates
the filtered cache before recovering from disk. A failed scan is not cached, so repair can
be followed by a retry. ID validation and directory containment are checked
on every filtered call, even with a warm cache; the query creates no directory.

**Do not recover the filtered watermark inside raw cursor recovery.** A raw
lookup only needs the newest segment with an entry; finding displayable
content may traverse older segments behind statuses. Combining the recoveries
would make `LatestEntryID` inherit corruption errors from older segments it
previously never opened. `TestLatestDisplayableEntryIDRecoveryError` checks
that raw recovery succeeds while filtered recovery fails on such a segment,
then succeeds after repair. `TestLatestDisplayableEntryIDRecovery` also checks
the raw watermark, complete status-bearing pages and subsequent ID allocation
alongside filtered recovery, so a correct unread value alone cannot hide a
storage regression. `TestMetadataVisibility` exercises absent/true/false for
all five excluded types plus counted, unknown and empty types;
`TestMetadataVisibilityRecoveryAcrossSegments` checks mixed logs, trailing
hidden entries, cached zero and raw/page retention across actual segments.

## Reader (#2116)

The first caller of `Store.Page` outside this package's own tests is
`internal/relay`'s `request_history` handler, adapted at
`cmd/pyry/relay.go` via `newHistoryPager`. That adapter classifies
`Store.Page`'s sentinels with `errors.Is` — the same pattern
`historyAppendFailure` (above) established for the write side — into
`historyPageFailure`, a read-path twin in the same file: `invalid_cursor`,
`not_contained`, `corrupt_segment`, `unknown_version`, `read`, never the
error's own text, for the same reason `historyAppendFailure` doesn't log one
(`open segment %q` and `resolve log directory %q` format absolute
filesystem paths). `ErrNotContained` earns its own discriminant despite
being unreachable past the relay's membership gate, because an occurrence
there is a symlink-containment attack signal rather than a malfunction, and
an operator needs to be able to tell the two apart even when the client
can't. See
[Inbound `request_history`](v2-session-manager-state-machine-inbound-request-history-historypager-seam.md)
for the seam shape this classification feeds and why it carries an outcome
enum rather than a bare error.

## Files

```
internal/history/
├── log.go       Store, New, Append, AppendWithMetadata, Page, metadata types, sentinels, per-conversation cache, directory resolution
├── segment.go   segment naming/ordering, versioned header, entry-line codec, whole-segment read
└── cursor.go    cursor mint + parse + validation
```

## Related

- [eventring-package.md](eventring-package.md) — the retained-triple shape
  (`Type`/`Payload`/`TS` plus a minted id) and the leaf-mutex posture this
  package copies; § "`After` — the three-way replay contract" is the #2022
  lesson this package's cursor validation applies a second time.
- [attachments-package-directory-resolution-and-creation.md](attachments-package-directory-resolution-and-creation.md) —
  the root-as-argument style, the resolve-and-compare-for-equality containment
  check, and the 0o700/0o600 modes this package copies verbatim.
- [relay-package-handlers.md](relay-package-handlers.md) § `ListConversations` —
  `LatestDisplayableEntryID`'s consumers: the `historyLatestReader` interface
  `*Store` satisfies for list and mark-read, and lookup-failure wire behavior.
- [`conversations-package.md`](conversations-package.md) § `ReadUpTo` — the
  durable read mark stated in this package's same per-conversation id space,
  landed in the same ticket (#2779).
