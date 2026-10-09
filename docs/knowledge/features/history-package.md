# `internal/history` — versioned, segmented per-conversation log

The durable, append-only, per-conversation message log the daemon writes as it
fans envelopes out, read newest-first by walking backwards on demand. Landed
in #2112 as a storage floor with no caller; #2114 gave it its first two —
the interactive emitter's `emit` chokepoint and session transitions — and
\#2115 gave it a third: the operator's own typed message, written from
the safe `msgqueue.QueuedMessage` projection. Stream Claude delivery commits
at echo placement or idle fallback; Codex/no-stream delivery commits at write
confirmation (see [Producers](history-package-producers.md#producers-2114-2115)). `#2116` is the
wire-serving consumer,
`#2113` declares the cursor as an opaque wire string. Spec:
[`specs/architecture/2112-conversation-history-log.md`](../../specs/architecture/2112-conversation-history-log.md).

## Topic map

| Document | Topics |
| --- | --- |
| [Shape](history-package-shape.md) | Entry and metadata contracts, paging bounds and durable ID allocation. |
| [A cleanup on a failed write is not the guarantee it looks like](history-package-failed-write-recovery.md) | Write rollback and read tolerance after fresh or active segment failures. |
| [Producers (#2114, #2115)](history-package-producers.md) | Captured provenance, runtime/startup dividers and interrupted work, visibility, delivery ordering and producer tests. |
| [`LatestEntryID` shares `Append`'s cursor instead of a second counter (#2779)](history-package-watermarks.md) | Raw durable cursors, displayable unread watermarks and lazy recovery. |

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

## Reader (#2116)

`internal/relay`'s `request_history` handler reads through `newHistoryPager`,
wired at `cmd/pyry/relay.go`. The adapter projects exactly one bounded raw
`Store.Page` result; it never scans ahead to fill a page. `legacyHistoryType`
preserves eligible legacy events regardless of `Entry.Shown`, with their original
durable ID, timestamp and payload bytes, newest-first. `legacyRuntimeReceipt`
projects only validated `main_turn_opened`, `main_tool_interrupted`,
`main_turn_interrupted` and `session_divider` into nonvisual `banner` receipts
(#3026). Their payload is `{conversation_id, level:"info", text:"",
truncated:false, stops_turn:false}` with all five fields present; ID and timestamp
remain those of the raw fact. Raw facts and metadata are unchanged, and neither
metadata field nor runtime payload is forwarded to the legacy wire.

Known facts need matching ownership, occurrence time and required identities;
unknown types and malformed facts remain excluded without being certified
harmless. A valid receipt accounts for its durable ID but grants no presentation
and changes no turn, permission, model or status state. Unknown, malformed or
unidentified receipts and unaccounted numeric holes remain mobile read barriers.
Receipt-only pages and repeated/overlapping delivery cannot establish sight.
Join receipts by conversation and durable ID: closures/dividers may share both
an occurrence timestamp and identical banner bytes, so the legacy (`type`, `ts`)
fallback cannot distinguish them.
See [receipt validation and publication](history-package-producers.md#legacy-eligibility-and-explicit-visibility-2965)
and the [wire contract](../../protocol-mobile.md#a-history-entry).

The adapter forwards that raw page's opaque `Cursor` and `AtStart` unchanged.
An empty filtered page with `AtStart == false` retains a usable cursor to the
next older raw page. Consecutive empty pages and an entirely filtered log are
valid walks; **consumers terminate on `AtStart`, never on an empty entry list**.
A terminal page has `AtStart == true` and an empty cursor. Treating an empty
list as exhaustion loses older eligible content; scanning ahead instead would
turn one bounded request into work proportional to the hidden history.

**Projected counts cannot determine raw page boundaries.** A hidden startup
divider now occupies a nonvisual receipt, while excluded unknown or malformed
facts still shorten pages. An all-runtime page can contain only receipts; it
retains the raw cursor and `AtStart` just as an entirely excluded page does.
`TestRelayV2_ConversationHistory` compares cursors and `AtStart` with each raw
page, covering a nonempty partial terminal page, an exact-fill walk with an
empty terminal page, and a receipt-only nonterminal page containing the startup
divider. Deriving exhaustion from seeded content counts would miss these boundaries.
`TestLegacyRuntimeReceipts_BoundedPages` checks all four facts at page edges and
in the middle, repeated walks and warm/reopened stores; `TestLegacyRuntimeReceipts_StartupDivider`
uses the real startup writer, which intentionally has no predecessor/successor
session identity. Requiring those sessions would leave restart holes while
running-turn tests stayed green.

`TestHistoryProjection_BoundedWalk` compares each projected page with its raw
page in warm and reopened stores, using mixed and entirely excluded logs,
consecutive empty pages and nil/false/true visibility. It verifies cursor
preservation, termination and exactly-once delivery of eligible IDs, timestamps
and payloads. `TestHistoryProjection_UnknownEmitIsDurableOnly` checks one raw
append and no ring/live publication with and without connected clients.

Cursor validation stays in `Store.Page`: the adapter passes cursors through
unparsed and retains the existing error outcomes. It classifies
`Store.Page`'s sentinels with `errors.Is` — the same pattern
[`historyAppendFailure`](history-package-producers.md#producers-2114-2115) established for the write side — into
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
  satisfied by the daemon's legacy view for list and mark-read, and lookup-failure
  wire behavior. [Watermarks](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954)
  distinguishes that view from the raw store API.
- [`conversations-package.md`](conversations-package.md) § `ReadUpTo` — the
  durable read mark stated in this package's same per-conversation id space,
  landed in the same ticket (#2779).
