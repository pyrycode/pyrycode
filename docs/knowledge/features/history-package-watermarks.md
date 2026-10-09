# `internal/history` — durable and unread watermarks

Part of [the history package overview](history-package.md).

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
[Directory resolution](history-package.md#directory-resolution-re-resolve-every-call-dont-cache-the-answer): `resolveDir(convID, false)` re-resolves and
re-checks every call, even once the cursor is warm, so a symlink planted after
the cursor loaded is still caught on the next lookup.

### Unread state uses a separate, lazily recovered watermark (#2954)

Raw `Store.LatestDisplayableEntryID` is metadata-aware. `displayableEntry` first
uses explicit `Entry.Shown`: true counts even a normally excluded status type,
and false excludes even a normally counted type. With `Shown == nil`,
`displayableType` excludes exactly `turn_state`, `stall`, `api_retry`,
`compacting`, and `session_transition`; every other stored type counts,
including unknown and empty types. Missing, empty and entirely hidden logs
return `0`. A durable idle status with absent visibility after the last
reply must not keep unread set forever. This raw API remains available unchanged
to future thread consumers; it can count shown interruptions and dividers.
Hidden entries still appear in `Page` and `LatestEntryID`, consume durable IDs
and advance the raw cursor; filtering creates no second ID space and decodes
no payloads. Payload fields named `shown` or `session` have no effect on these
metadata rules.

**Legacy list and read clamping share wire-type targets (#3029).** `startRelayV2`
passes `legacyHistoryReader` to both `ListConversationsWithAgents` and
`MarkConversationRead`. Its `LatestDisplayableEntryID` excludes exactly
`turn_state`, `stall`, `api_retry`, `compacting` and `session_transition`, even
with `Shown == true`. Every other `legacyHistoryType` counts regardless of
absent/false/true visibility, including normal, failed/stopped `turn_end` and
nonvisual metadata. Facts validated by `legacyRuntimeReceipt` count as projected
info banners at their original durable IDs, including receipt-only logs and
runtime tails. Unsupported entries, including invalid runtime facts, keep
conservative absent/true visibility counting and false exclusion. Transport
exclusion or fallback counting never certifies a harmless receipt.

The adapter validates through `Store.LatestEntryID`, then walks newest-first raw
pages of 128 entries until it finds qualifying content or `AtStart`. It keeps bounded
memory and adds no cache that could become stale after append or reopen.
**A zero raw-displayable watermark does not prove the legacy target empty.**
Entirely hidden logs can still contain counted completions, metadata or runtime
receipts; only a zero raw durable cursor can short-circuit this scan. This
watermark walk is separate from `newHistoryPager`, which projects exactly one
bounded raw page and retains its opaque cursor and `AtStart`, including pages
containing only [runtime receipts](history-package.md#reader-2116).

`read_up_to` stays in the original durable ID space. The registry persists
`max(held, min(up_to, latest))`; a held mark above the current legacy watermark
is preserved, never lowered. Marks remain monotonic and isolated by host and
conversation. List values and fetched, live or replayed receipts confer no sight;
mobile still requires explicit foreground presentation and refuses unknown,
malformed or unidentified receipts and unaccounted holes. Both handlers share this view
for `latest_entry_id > read_up_to` to clear after a presented reply is marked
read; see the [wire contract](../../protocol-mobile.md#marking-a-conversation-read).

**Reaching the raw-visible watermark can still leave legacy unread set.** In the
runtime-enabled completed reply, IDs 1–6 are message, opening receipt,
responding state, assistant delta, normal turn end and idle state. Raw visibility
ends at 4, client/list latest is 5, and explicit presentation can yield checkpoint
6; the correlated daemon confirmation persists `read_up_to == 5`. With runtime
facts disabled, those values are raw 3, latest 4, checkpoint 5 and confirmed 4.
Assert client-derived latest, presented checkpoint and correlated persisted mark
together: the previous checkpoint-versus-raw-watermark assertion stayed green
while the daemon clamp left mobile unread. Received durable identity enables
joining and deduplication; receipt validation enables accounting. Neither
establishes sight or daemon confirmation. Repeated/overlapping history, live and
replay delivery cannot lower latest/read marks or grant sight.
`TestLegacyRuntimeReceipts_CompletedTurn` checks these controls through the real
emitter, serialized pager and handlers in warm/reopened stores.
`TestLegacyRuntimeReceipts_CompletionTails` checks normal/failed/stopped endings
and nonvisual metadata/runtime tails; failed/stopped endings require their own
presentation before the checkpoint can advance through them.
`TestLegacyRuntimeReceipts_WireTargets` checks absent/false/true visibility for
eligible entries, excluded statuses, validated receipts and unsupported fallback.
`TestLegacyRuntimeReceipts_Watermark` checks warm/reopened stores, raw watermark
preservation and append after lookup; `TestLegacyRuntimeReceipts_ReadMarks`
checks list/clamp agreement, persistence, repeats and held marks above the clamp.

Within the raw store, after `load`, the first filtered lookup walks `listSegments`
newest-first and each `readSegment`'s entries backwards until a qualifying entry
is found.
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
