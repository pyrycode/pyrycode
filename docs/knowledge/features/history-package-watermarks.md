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
