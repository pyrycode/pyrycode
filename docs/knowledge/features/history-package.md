# `internal/history` — versioned, segmented per-conversation log

The durable, append-only, per-conversation message log the daemon writes as it
fans envelopes out, read newest-first by walking backwards on demand. Landed
in #2112 as a storage floor with no caller; #2114 gave it its first two —
the interactive emitter's `emit` chokepoint and session transitions — and
\#2115 gave it a third: the operator's own typed message, written from
`msgqueue`'s `OnDelivered` seam rather than the delivery seam itself, since
only that seam has the client-readable text in hand (see
[Producers](#producers) below). `#2116` is the wire-serving consumer,
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
type Entry struct{ ID uint64; Type string; Payload json.RawMessage; TS time.Time }
type Page struct{ Entries []Entry; Cursor string; AtStart bool }
func (s *Store) Append(convID conversations.ConversationID, typ string, payload json.RawMessage, ts time.Time) (uint64, error)
func (s *Store) Page(convID conversations.ConversationID, cursor string, limit int) (Page, error)
```

`limit` is **clamped** to `MaxPageEntries`, never refused above it — a page is
"up to `limit` entries", so an over-large ask from #2116 pages rather than
fails and can never be broken by the ceiling. Entry ids are a
**per-conversation** counter recovered from the newest segment's last entry on
first touch, deliberately not `eventring`'s ring-wide shape: every cursor here
names its own conversation and is refused on any other, so there is no shared
scalar for one conversation's cursor to mute a different one's stream, and no
reason to pay for a global counter recovered by reading every conversation at
startup.

**PRECONDITION on both methods, stated on both doc comments:** `convID` must
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
segment number, its size, the next entry id) — never the path.

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
counters, and both `Append` and `Page` end to end, held only around bounded
file I/O and never across a channel op or another lock. Reads take the lock
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
(`cmd/pyry/operator_message_history.go`), wired to `msgqueue.Config.OnDelivered`.
The first two already resolve the four values `Append` wants — conversation
id, wire type, marshalled payload, one hoisted timestamp — for the ring
append or the fan-out itself, so their log append needed no new mapping, only
a nil-guarded call before the per-conn loop in each. The third resolves them
from a `msgqueue.QueuedMessage` handed to it by a seam fired on confirmed
delivery, not from an envelope in flight (see below).

**Why this producer reads `text`, never the delivered payload.** Since #2038
a queued message carries two strings — `text` (client-readable) and
`delivery` (what reaches claude's stdin, which for an attachment-bearing
message names an on-host path `docs/protocol-mobile.md` § Error codes
forbids serving to a paired device). `newInboundDeliver` sees only
`delivery`, so it cannot be this producer; `msgqueue`'s `OnDelivered` seam
carries `QueuedMessage` instead, which declares no `delivery` field, making
the omission structural rather than a filter this producer could forget.
Full detail:
[msgqueue-package.md § Delivered notification (#2115)](msgqueue-package.md#delivered-notification-2115).

**The stored entry keeps its attachment ids too, by the same structural argument (#2596).** `QueuedMessage.AttachmentIDs` is the ids a `send_message` named, as `internal/relay/handlers.resolveAttachments` resolved them — deduplicated, each past the canonical-shape check — copied onto the queue record independently of `delivery`. This producer sets `protocol.MessagePayload.AttachmentIDs` straight from `msg.AttachmentIDs`; since it still reads only `QueuedMessage`, which has no `delivery` field, no on-host path is reachable here no matter what changes upstream. A message that named none stores nothing (`omitempty` elides the key), so every pre-#2596 entry a client already decoded is untouched.

**One inherited gap, specific to the third producer.** `msgqueue`'s drain
tests `ctx.Err() != nil` before its confirmed-delivery branch, so a delivery
that confirms in the same instant the daemon shuts down leaves the head
queued and fires neither `q.notify` nor `OnDelivered` — the message reached
claude's stdin but this producer never runs for it. Pre-existing `drain`
ordering (#487/#1484), not introduced by #2115; a client reading this log
after a restart should not assume it is gap-free across that boundary.

**Why the write point is the envelope, not `internal/turnevent`.** An
earlier draft proposed writing the log from `turnevent`'s representation.
That cannot work: `turnevent`'s variant set carries no operator message, no
conversation id, no session transition and no question batch. The
operator's own typed text goes `send_message` → msgqueue → delivery → the
child's stdin and is never echoed back, so a log written from `turnevent`
would hold assistant text and tool rows and none of what the operator
typed. (The operator's own message is #2115's producer — see below — fired
from `msgqueue`'s `OnDelivered` seam once the write to claude is confirmed,
not from an envelope emitted in either direction.)

**The store is nil-tolerant and a concrete pointer, never an interface** —
the same trap `session_transition_v2.go`'s `busy` field already documents:
a typed-nil store boxed into an interface is non-nil at the interface level
and would sail past a `== nil` guard. `Store` is not nil-receiver-safe
(`Append` locks immediately), so `appendConversationHistory` checks
explicitly rather than relying on a nil-receiver method, and every emitter
test that builds an emitter with no store keeps working unchanged. A
failing append never suppresses the wire emit or the ring append — it is a
statement with no branch after it — and the failure is logged at `Warn`
with an `errors.Is`-derived discriminant (`invalid_id` / `invalid_payload`
/ `write`), never the error's own text: `history`'s errors format absolute
filesystem paths (`open segment %q`), and the log's own MUST-NOT-log-content
rule would be defeated by relaying them.

**Two test-shape traps worth knowing before touching either producer
again:**
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
├── log.go       Store, New, Append, Page, sentinels, per-conversation cache, directory resolution
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
