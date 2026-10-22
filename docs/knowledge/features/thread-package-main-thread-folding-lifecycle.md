# `internal/thread` — main work and scoped closure

[Main-thread folding overview](thread-package-main-thread-folding.md).

### Main-work source entries

`mainWork` folds recorded main facts, including hidden ones, without consulting
live state or current session bindings. The ordinary result/closure rules below
apply to foreground calls; [linked background shells](thread-package-agents-and-background-work.md#background-shells) use
independent task lifecycle.

| Raw source type | Item kind | Rendered content and status |
| --- | --- | --- |
| Main `assistant_delta` | `assistant_message` | Creating payload with accumulated `text`; `running` while open, `done` after closure. |
| Ordinary main `tool_use` | `tool_call` | Creating payload, including input; initially `running`. |
| Matching `tool_result` | Updates `tool_call` | Nested raw `result`, including summary/detail; `done` for success, `failed` when `is_error` is true. |
| Matching `tool_denied` | Updates `tool_call` | Nested raw `denial`; `denied`. |
| Matching `main_tool_interrupted` | Updates `tool_call` | Nested raw `interruption`, retaining cause; `interrupted`. |
| `main_turn_opened` | No row | Records opening identity for scoped work and recovery references. |
| Main `turn_end` | `turn_end` | Raw reason, usage and outcome fields; `done` or `failed` according to `is_error`. |
| Main `main_turn_interrupted` | `turn_end` | Raw cause, occurrence time and any opening reference; `interrupted`. |

Assistant deltas coalesce within their source and turn. A main tool launch,
including a Claude Agent/Task launcher, closes that text run. A delivered user
`message` closes open main text runs throughout the conversation; a matching turn
ending closes its own run. Subsequent text creates a new item. Queued acceptance,
child traffic and tool reports alone do not split text. Saved text retains its
recorded whitespace; only the summary is normalized.

Production reference folds keep main and child `assistant_message` content as a
`contentRecipe`: creating-entry ID, ordered text-entry IDs and named enrichment
fields. Each field references a source entry or ancestor item, or holds a small
computed JSON value. Inherited `parent_ending` names the original ending ancestor; see
[parent repair](thread-package-agents-and-background-work.md#parent-repair).
Nonempty subsequent deltas append only their IDs. Retained items and intrinsic
and previous child copies contain no selected payload, accumulated text or assistant
summary, and these items bypass inline append buffers. The shared `renderShared`
path decodes each text payload separately, concatenates the decoded strings and
derives `Summary` with `plainSummary`, using `assistant message` when empty.

Ordinary `tool_call` items use the same renderer after launcher/shell lifecycle
classification. Claude and metadata-free `Bash`/`local_bash` calls use launch
and report references for foreground work, then ID-backed lifecycle recipes
after a background task link. Claude/legacy Agent/Task calls remain agents.
Codex calls with any of those names are ordinary reference-backed tools.
Selecting by tool name alone would skip eligible Codex calls. Result, denial,
interruption and turn-ending fields reference their original report IDs through
`setOutcome`; pending outcomes also retain only IDs and decoded lifecycle facts.

Both modes preserve exact bytes: the creating payload keeps its original
formatting until enrichment or a nonempty append, then JSON keys are sorted with
unknown creating fields and `encoding/json` escaping preserved, including
HTML-sensitive characters. Summary whitespace/control handling matches
`plainSummary`. Decoding each payload independently preserves UTF-8 replacement
behavior rather than recombining invalid byte fragments across entries. Empty
appends leave content, revision and `LastShownVersion` unchanged. Creating
visibility is retained; inputs and returned snapshots remain detached.

Creating, text and enrichment references share the Store's current-chunk,
inclusive 1 MiB recent-cache and authoritative history lookup path. Any required
missing/unreadable reference rejects the entire candidate publication with generic
`StateUnavailable`/`ErrUnavailable`, without usable items, version or epoch.
Repair followed by `Retry` rebuilds the view. See
[snapshot rendering and retry](thread-package-background-store-replay.md#snapshots-and-retry).

Legacy `New` folds maintain main and child text and normalized summaries
incrementally with `appendText` and `appendSummary`. Unpublished appends reuse
private capacity with amortized work proportional to the incoming suffix. Private JSON buffers grow
geometrically, encoding only new text and moving fixed trailing fields; the
summary builder carries pending whitespace across deltas instead of rescanning
accumulated text, preserving the same byte and summary rules as reference rendering.

`publicationCapture.capture` borrows Content bytes without copying them and marks
the exposed row's text buffers shared before publication. `growText` detaches
shared storage before writing any suffix or closing JSON bytes, even when spare
capacity suffices: it copies the existing prefix into a new private allocation,
preserving capacity unless growth is needed. Retained publications and query
roots therefore keep immutable, valid JSON while the serialized fold worker
feeds later text and queries read outside the store lock. This also preserves
the previous text used by `itemDifference`, so a pure append yields exactly its
suffix without a spurious Content replacement. Allocation, prefix copying and
the capture ownership hook are included in ingestion work counters. See
[immutable publication](thread-package-background-store-replay.md#snapshots-and-retry).

Parent-attributed text and tools fold in [child lanes](thread-package-agents-and-background-work.md#parent-repair), without
opening or closing main turns, closing main calls or splitting/coalescing main
text. Claude and metadata-free Agent/Task launches create agent items rather
than ordinary calls.
Only main launches split main text; `agentLaunchIsChild` also consults retained
parent evidence within the launch's recorded source and lifetime or legacy scope
before `mainWork` changes the text run. Looking only at the mapped launch payload
would split text when the preceding durable observation alone saved its parent.
Codex tools named Agent or Task are ordinary calls: the name alone cannot
establish Claude launcher semantics. Agent reports update their call-owned item;
linked Claude/legacy Bash reports enrich their existing ordinary call with
[background shell lifecycle](thread-package-agents-and-background-work.md#background-shells).

### Scoped joins, pending outcomes and closure

Join keys use recorded `Entry.Session` (including its agent kind) plus `turn_id`,
and add the recorded call ID for tools. With no entry metadata, they use private
legacy boundary scope plus turn/call IDs. Tagged and untagged work never join,
even when successor fallback gives them the same displayed session and agent.
Other sources, turns and conversation owners remain independent. Empty or
explicitly truncated/dropped join identities cannot create joins.

Delivered user `message` entries discover only open main text runs throughout
the conversation, including older generations retained after turn-ID reuse and
earlier legacy scopes. Open means still eligible for text coalescing, including
inactive late text; filtering on `Item.Active` would let that text coalesce across
a delivered message. Closure clears its coalescing reference without changing an
inactive row's settled revision, visibility or the shown watermark. Subsequent
text starts a new row and remains inactive if its turn already ended. Main tool
launches and matching turn endings retain their scoped closure rules; queued
acceptance, child traffic and tool reports alone do not split main text.

`Fold.openTexts` tracks only main coalescing runs in a dense slice, independently
of retained recovery openings. `trackText` registers newly created main rows;
`closeText` removes membership by swapping the final entry into the released
slot. Traversal uses current length, so unrelated closed openings and a former
peak in open runs add no closure discovery work. Closed explicit and implicit
openings remain available to their original recovery references after closure,
generation reuse and legacy scope changes; coalescing still uses the source and
scope rules above.

Tool reports can precede creation and remain pending across feed chunks. The
first valid foreground terminal outcome in entry-ID order wins; later duplicate or
conflicting outcomes change neither content, state nor revision. The creating
call retains its creating input reference (inline in legacy `New`) and gets
the saved report when it arrives. Applying an earlier report keeps revision at
least the creating ID.

`mainOutcome` retains terminal-order ID, content-source ID, decoded status and
field, with no raw-payload field. This covers pending main/child reports, turn
endings and outcomes supplied by `applyShellForeground`. Production recipes
reference the content-source entry; only legacy `New` folds use the lazy
`legacyOutcomes` compatibility map. Reports before child creation remain keyed
by original source/lifetime-or-legacy-scope, turn and call independently of
replaceable main-turn slots; see
[parent repair](thread-package-agents-and-background-work.md#parent-repair).

**Terminal order and content provenance are distinct.** `agentGroup.addReport`
combines matching durable/mapped companions with the earliest evidence ID for
terminal selection and the mapped report's own ID for content. For example, a
durable result at ID 1 and mapped result at ID 2 keep order 1 but render payload
2. Resolving content through the order ID would silently select the wrong JSON.
Shell foreground eligibility and turn identity are decoded at report arrival;
reuse preserves that validation and the existing scoped/first-terminal rules.

The first matching turn ending closes running text as `done` and unfinished
foreground ordinary calls as `interrupted`, rendering the raw ending under
`ending` on each call from its source ID in reference mode.
**A turn closure supplies no successful tool result.**
Already terminal calls retain their outcome; child work is unaffected. Endings
received before creation remain effective across chunks. Late text may coalesce
but stays inactive; late foreground calls are inactive and later reports cannot
replace their ending. A linked background shell outlives that turn; a late task
link can repair its provisional closure, and late launch results stay retained.

All production child items keep independent intrinsic and previous recipes
alongside their metadata, including nested agents and shell-backed tools.
`restoreChild` restores the intrinsic recipe before mutation; `resolveChildren` captures the updated base
before deriving ancestor closure. Enrichment maps use copy-on-write and text-ID
lists retain snapshot lengths. Sharing one mutable recipe would leave provisional
`parent_ending` behind after reopening or discard late text received while
inactive. Child comparisons include recipe state without resolving history during
ingestion. `parent_ending` references the original intrinsically ended ancestor,
propagating through provisionally closed agents without copying ancestor JSON.
Its revision is retained for equality so late ancestor enrichment also updates
descendants. Intrinsic `ending` evidence still prevents reopening independently
closed work. Scope, visibility, public touches and revision rules are the same
in both modes.

Outcome and ancestor dependencies share `renderShared`. Ancestors outside the
selected page resolve from the same supplied item/recipe view as the selected
child. Captured Store output stays immutable across later feeds; delayed rendering
must use captured roots and an authoritative source rather than mutable worker
fold/cache state. Worker rendering keeps current-chunk, inclusive 1 MiB recent-cache,
then authoritative `Lookup` precedence. A missing, unreadable or malformed
required dependency withholds the whole candidate with generic
`StateUnavailable`/`ErrUnavailable`, exposing no payload or host path. Repair
followed by Store `Retry` rebuilds the view; returned public content is detached.
See [snapshot rendering and retry](thread-package-background-store-replay.md#snapshots-and-retry).

Legacy inline child text keeps separate intrinsic and ancestor-derived JSON buffers.
`resolveChildren` reuses unchanged `parent_ending` enrichment and advances both
buffers incrementally on text appends; a late background link removes only the
provisional closure, preserving text and normalized summary received while
inactive. Intrinsic `ending` evidence still prevents reopening independently
closed work. Publication capture marks both intrinsic and derived buffers shared,
conservatively protecting either side of provisional closure. Reusing cached
`parent_ending` content preserves its shared flag; newly built enrichment buffers
start private. Subsequent appends detach each shared buffer before writing, so
retained internal publications and query roots, as well as detached public
snapshots, keep their bytes across closure, append and reopening. Optimizing
`foldWork` alone would leave quadratic child serialization and equality work:
a proven nonempty append bypasses accumulated
content comparison, while empty deltas reuse cached closure content even before
append-buffer initialization. See [ancestor closure and parent repair](thread-package-agents-and-background-work.md#parent-repair).

Session reset can let Claude cancel a foreground call before daemon closure:
an error `tool_result` then leaves the call `failed`, and the later cancelled
`turn_end`/divider cannot replace that first terminal with `interrupted`.
Assert settlement against raw terminal facts rather than a checkpoint's name;
the [retained shadow capture](thread-package-shadow-lifecycle-and-evidence-coordination.md#idle-retirement-query-demand-and-evidence)
requires this non-success closure before its divider.

`main_turn_opened` records an explicit opening without a row. In legacy logs,
the earliest valid main text, call or result/denial evidence supplies an implicit
opening. A new explicit opening after an established opening starts a fresh
generation, isolating reused turn/call IDs from predecessor work.

Both startup interruption types can carry `turn_opened_entry_id`. `mainWork`
targets only that original explicit or implicit opening, checking recorded
source and turn even when recovery is appended in a different legacy scope.
**A supplied reference never falls back to append-time scope.** An unresolved,
zero or mismatched reference closes nothing; a valid turn interruption still
creates its ending row. A supplied null reference is malformed, rather than
absent, and consumes version only. Treating null as absent could interrupt a
new generation that reused the old IDs. See
[startup reconciliation](history-package-producers-runtime-lifecycle.md#startup-reconciliation-3014)
and [runtime main-work closure](history-package-producers-runtime-lifecycle.md#hidden-openings-and-interrupted-main-work).

