# `internal/history` — versioned, segmented per-conversation log

The durable, append-only, per-conversation message log the daemon writes as it
fans envelopes out, read newest-first by paging backwards or oldest-first with
snapshot walks and same-Store tailing. Landed in #2112 as a storage floor with
no caller; #2114 gave it its first two —
the interactive emitter's `emit` chokepoint and session transitions — and
\#2115 gave it a third: the operator's own typed message, written from
the safe `msgqueue.QueuedMessage` projection. Stream Claude delivery commits
at a receiving-child echo or normal answering-turn completion (with Send now's
carried-close grace for no-echo completion); Codex/no-stream delivery commits at
write confirmation (see [Producers](history-package-producers.md#producers-2114-2115)).
The raw wire pager is retired (#3715); source storage still feeds
[`internal/thread`](thread-package.md) and list/read watermarks. Spec:
[`specs/architecture/2112-conversation-history-log.md`](../../specs/architecture/2112-conversation-history-log.md).

## Topic map

| Document | Topics |
| --- | --- |
| [Shape](history-package-shape.md) | Entry and metadata contracts, selective lookup and its byte-bounded cache, explicit conversation sync, paging bounds, durable ID allocation and forward replay/tailing. |
| [A cleanup on a failed write is not the guarantee it looks like](history-package-failed-write-recovery.md) | Write rollback and read tolerance after fresh or active segment failures. |
| [Producers (#2114, #2115)](history-package-producers.md) | Map of captured provenance, runtime/startup closure, agent/task attribution and reported endings, visibility, receipts and producer verification. |
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
accident) and the original browsing access is **newest-first, walking backwards
on demand**. Those two facts, not an I/O cost argument, are why the log is segmented
rather than one growing file: the newest segment stays small, browsing opens older
segments only when a client scrolls into them, segment boundaries double as the index
so no sidecar file is needed, and a future retention policy becomes a file
delete rather than a rewrite. [Forward consumption](history-package-shape.md#forward-consumption)
also walks these segments chronologically and skips completed segments while tailing.
An earlier draft justified segmentation by claiming a plain append-only file
can't be opened cheaply at its end — false
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
segment number, its size, the last durable entry id), the filtered watermark,
lookup first-ID/size bounds and independent sync obligations — never the path.
The [lookup LRU](history-package-shape.md#selective-entry-lookup) retains decoded
data, but every lookup still rechecks containment and current regular-file
membership before consulting it.

Containment is a full-path **equality** check against the resolved instance
directory, not a `filepath.Rel`-style "is it under the root" test — equality
is what refuses a history directory symlinked at a *sibling* conversation,
which stays inside the instance directory and would pass a containment test
built the other way. Two more leaf protections exist because containment of
the directory is not containment of the files in it: the append open carries
`syscall.O_NOFOLLOW` (confirmed clean on `linux/amd64`, `linux/arm64` and
`darwin`, so the `os.Lstat` fallback named in the spec was never needed), and
the reader skips any non-regular directory entry rather than following it.

`Store.Sync` cannot reuse that reader filtering: silently skipping a replaced
pending segment would certify data it never synced. It checks every
segment-named leaf, rejects symlinks/nonregular targets and refuses missing
pending targets. Sync opens read-only with `O_NOFOLLOW` and `O_NONBLOCK`, then
checks the descriptor's type; directory opens additionally require
`O_DIRECTORY`. Re-resolution and leaf validation still run on repeated calls
that have no sync work.

## Concurrency

No goroutine is spawned and there is no `Close` — the store is passive, like
`Ring`. One `sync.Mutex` guards the per-conversation state map, read counters,
tail registrations, the lookup bounds/LRU, both append paths, `Sync`, `Lookup`
and `Page`'s storage work. `Lookup` holds it through discovery, selected reads,
cache operations and detaching returned data. `Sync`
holds it through target capture, validation, file/directory syncs and closes,
so concurrent appends ordered after that boundary remain pending for the next
call. The mutex is store-wide: syncing one conversation can delay another's
append. Forward walks release it after listing and after each bounded segment
read/decode, before
callbacks, delivery or waiting; they never hold it across the whole replay.
Reads take the lock rather than racing appends: the alternative (lock-free reads plus a
torn-trailing-line tolerance on the read path) buys a shorter hold at the cost
of reasoning about partial-write visibility across every filesystem the daemon
runs on, and holding a leaf mutex across a bounded read was judged cheaper
than being wrong about that.

Committed-append wakeups use nonblocking sends under the mutex into capacity-one
channels; further commits coalesce while a wakeup is pending. Consumer processing
or waiting cannot hold the store mutex or delay append completion, including for
another conversation. `TestForwardReplayToTail` pauses a multi-segment replay
callback and proves appends to both the same and another conversation finish
before replay resumes. `ForwardReader` methods require serialized caller use;
`Store` remains safe for concurrent callers. See
[Forward consumption](history-package-shape.md#forward-consumption) for catch-up,
cancellation and retry contracts.

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

The remote raw pager and `newHistoryPager` adapter are removed (#3715).
Content recovery uses [Thread query messages](../../protocol-mobile.md#thread-query-messages)
and the publisher Store; raw `history.Entry` records remain the fold source.
`Store.Page`, forward walks/tailing and watermark APIs remain internal storage
operations. Legacy live/replay receipt projection and list/read content policy
also remain; see [producer eligibility and verification](history-package-producers-legacy-compatibility.md).

Storage cursors describe the returned range. Never truncate a returned page and
retain its old cursor: that cursor points before the oldest returned entry, so
the next request would skip the discarded entries. Re-query at a smaller limit
when a consumer needs a smaller range. Entry-count bounds also do not bound bytes;
a downstream storage clamp can be larger than an upstream transport or memory
budget. Check both before exposing a storage query to untrusted callers.
These storage rules do not restore the retired wire protocol.

`historyPageFailure` remains the content-free read-error classifier used by
`reconcileStartupHistory`. It maps sentinels via `errors.Is` to `invalid_cursor`,
`invalid_id`, `invalid_page_size`, `not_contained`, `corrupt_segment`,
`unknown_version` or `read`, without logging the underlying error's absolute
filesystem paths. Keep `TestHistoryPageFailure_NamesEverySentinel` even though
the remote pager is gone. `not_contained` distinguishes a confinement refusal
from ordinary I/O for the operator; public list/read failures still use the
shared retryable `history.unavailable` contract.

## Files

```
internal/history/
├── log.go       Store, New, Append, AppendWithMetadata, Page, metadata types, sentinels, per-conversation cache, directory resolution
├── segment.go   segment naming/ordering, versioned header, entry-line codec, whole-segment read
├── cursor.go    cursor mint + parse + validation
├── forward.go   chronological snapshot walks, progress and committed-append tail wakeups
└── sync.go      explicit conversation durability barrier, pending-target validation and file-before-directory sync
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
