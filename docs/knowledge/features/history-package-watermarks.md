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

`CachedLatestEntryID` exposes only an already-loaded append cursor, with a
separate presence flag: known empty progress is present zero, while a cold or
missing cursor is unknown. It validates IDs and rechecks directory containment
under the same mutex as appends, without calling `load`, opening segments or
creating directories. `EnsureLogDir` alone does not warm it. Use this API when
a metadata lookup must never initiate history recovery; substituting
`LatestEntryID` silently adds cold segment reads. A saved thread certificate's
consumed version cannot establish current raw progress, because hidden appends
advance the raw cursor without raising the shown watermark. See
[unloaded certificate eligibility](thread-package-cache-and-epochs.md#cache-and-epochs).
`TestCachedLatestEntryID` measures zero segment opens/bytes for warm, cold and
empty reads; `TestCachedLatestEntryIDConcurrent` checks reads alongside appends.

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

**Conversation summaries use shown-thread authority (#3722).**
`ListConversationsWithAgents` projects registry rows without a history reader.
At delivery, `V2SessionManager.threadSummary` supplies `last_shown_version` on
connections that negotiated `thread`, independently for every list row and
`conversation_updated` reply/push. Usable loaded status or a fully unloaded clean
certificate at exact consumed V = independently known raw H supplies the reading.
Missing, rebuilding, unavailable, retiring or stale evidence omits the field;
known zero emits numeric `0`. Every otherwise eligible row survives, including
when source history is unavailable. Listing starts no worker, warms no cursor
and scans no segments. The retired `latest_entry_id` field and legacy content
scan supply no fallback. See
[summary eligibility and quiet-retirement recovery](thread-package-cache-and-epochs.md#scalar-certificates-and-unloaded-metadata).

Unread is `last_shown_version > read_up_to` only with present shown evidence;
absence means unknown. Non-thread connections omit the reading. Raw entry IDs,
`Store.LatestDisplayableEntryID` and maximum item revisions cannot establish
shown-thread progress: hidden/state-only records and completion revisions can
advance consumed progress without new shown content.

`read_up_to` stays in the original durable ID space. The registry persists
`max(held, min(up_to, last_shown_version))`, never lowering a higher held mark,
including at authoritative zero. The read handler uses the installed
`ThreadLastShownVersion` provider for every client. Missing evidence refuses even
a no-op with retryable `history.unavailable`, without mutation, persistence or
announcement. A hidden append can invalidate an unloaded certificate without
raising shown progress; the held mark survives until ordinary folding/retirement
restores authority. Hidden entries retain their durable IDs, source metadata and
accounting role; old marks are never rewritten. See
[read-mark authority and regression fixtures](relay-package-handlers-mark-conversation-read.md)
and the [wire contract](../../protocol-mobile.md#marking-a-conversation-read).

**Keep receipt validation, raw visibility and unread authority separate.**
A valid neutral receipt proves transport accounting, not shown content or
foreground presentation. `TestLegacyRuntimeReceipts_CompletedTurn` retains
source/live/replay identities, payloads and warm/reopened metadata; receipt
validators assert acceptance directly. A legacy checkpoint oracle would test
an independent retired algorithm rather than the daemon's shown-thread authority.

`TestDaemonThreadListShownAuthority` checks actual correlated list delivery with
corrupt source segments, certified nonzero/zero and cold unknown rows together.
Rows and held marks survive repeated negotiated/non-thread lists, without worker
startup or cold-cursor recovery. `TestUnreadThreadHiddenTails` checks completion
revisions above shown progress, durable marks, loaded metadata, stale-certificate
read refusal and recovery after hidden appends. `TestUnreadRestartReconciliation`
checks repeated startup reconciliation and certified unloaded evidence while
preserving higher held marks. `TestDaemonThreadOffscreenShownRecovery` requires
producer-driven recovery after quiet retirement without reopening/catching up
all listed conversations. These replace legacy list-scan/checkpoint assertions;
the internal raw-store tests below retain their separate storage contract.

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
