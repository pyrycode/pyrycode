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

**Daemon list and read clamping share the producer's content policy (#3463).**
`startRelayV2` passes the same `legacyHistoryReader` to
`ListConversationsWithAgents` and `MarkConversationRead`. Its
`LatestDisplayableEntryID` requires `historyEntryShown(type, payload)` and
rejects explicit `Shown == false`. Absent metadata uses that same payload-aware
classification; explicit true cannot promote metadata or unsupported vocabulary.
Unknown types default to non-content, so adding a hidden type needs no separate
unread exclusion. See the [producer visibility table](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965).

Status/metadata entries such as `turn_state` and `context_usage`, normal successful
turn endings, non-stopping info banners and restart/sleep dividers do not raise
the daemon target. A valid `legacyRuntimeReceipt` establishes delivery/checkpoint
accounting, not visible content. Known raw facts qualify only through the shared
content classifier and explicit-false veto, independently of whether their legacy
projection is a nonvisual receipt. Receipt validation and transport eligibility
remain separate contracts; neither an unread classification nor its exclusion
certifies safe accounting.

The adapter validates through `Store.LatestEntryID`, then walks newest-first raw
pages of 128 entries until it finds visible content or `AtStart`. Empty and
hidden-only histories return `0`. It keeps bounded memory and adds no cache that
could become stale after append or reopen. Only a zero raw durable cursor can
short-circuit the scan: the store's metadata-only displayable API has different
classification semantics. This scan is separate from `newHistoryPager`, which
projects exactly one bounded raw page and retains its cursor and `AtStart`,
including pages containing only receipts.

`read_up_to` stays in the original durable ID space. The registry persists
`max(held, min(up_to, latest))`; a higher held mark is never lowered, including
when latest is zero. Reading the newest visible entry clears
`latest_entry_id > read_up_to` despite hidden tails; reading an older entry
leaves newer content unread. New visible content beyond the mark raises unread
again. Reopening and repeated startup reconciliation preserve both read and
unread results when only hidden restart dividers are added. Visible interruption
or loss notices can still raise the target. Hidden entries retain their durable
IDs, raw history and accounting role; no metadata or old marks are rewritten.
See the [wire contract](../../protocol-mobile.md#marking-a-conversation-read).

**Keep receipt accounting and server visibility oracles separate.** A wire or
checkpoint oracle correctly counts neutral receipts and normal completion frames
without proving they are new visible content. Reusing that oracle for the server
watermark would keep the hidden-tail bug green. In the runtime-enabled completed
reply, IDs 1–6 are message, opening receipt, responding state, assistant delta,
normal turn end and idle state. Client wire latest is 5 and a presented checkpoint
can reach 6, while daemon latest and the confirmed mark from zero are 4. With
runtime facts disabled, those values are wire latest 4, checkpoint 5 and daemon
latest/confirmed mark 3. Assert accounting, explicit foreground presentation and
the correlated persisted mark independently; delivery alone grants no sight.
`TestLegacyRuntimeReceipts_CompletedTurn` retains these distinct expectations
through real emission, serialized paging and list/read handlers in warm/reopened
stores. Receipt validation, delivery and overlap checks must survive a corrected
server-target expectation.

`TestUnreadHiddenTails` covers both reported tails, page-spanning restarts,
absent visibility, future hidden types, hidden-only logs, older reads, higher
held marks and subsequent visible content in warm/reopened stores.
`TestUnreadRestartReconciliation` preserves read/unread results over repeated
reconciliation. `TestUnreadThreadHiddenTails` compares authenticated legacy and
thread routes against the shared mark, including completion revisions beyond
shown content, loaded metadata and eligible unloaded certificates. Hidden
item/live-state updates advance consumed progress without raising shown progress;
a hidden append invalidates stale unloaded evidence, and an unavailable reading
stays unavailable until ordinary loading/folding restores it. Never substitute
the raw cursor or maximum final item revision for `last_shown_version`; see
[thread shown-watermark recovery](thread-package-cache-and-epochs.md#cache-and-epochs).

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
