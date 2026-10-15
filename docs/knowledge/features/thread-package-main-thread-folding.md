# `internal/thread` — main-thread folding

## Main-thread folding

### Identity, order and version

Construct each owner with `New(conversationID)`, then call `Feed` with strictly
increasing entry IDs in `1..history.MaxEntryID` (`2^53 - 1`). Gaps are allowed.
`Item.ID` is its creating entry's ID and `Kind` is immutable. `Order` normally
equals that ID; accepted sends keep order zero until delivery, then use the
linked user-message entry ID. Dropped and lost sends stay unordered. `Rev` names
the latest content or state change, floored at the creating ID when an earlier
pending report is applied. Standalone items keep their creating revision;
accepted sends, main work, child work and agent items change in place. A valid
send outcome sets revision to its entry ID. Redundant terminal reports do not
advance revision.
`Items()` returns items in creating-entry ID order, which can differ from
delivery `Order`; timestamps supply neither ordering nor durable linkage.

`Version()` is the newest consumed valid ID, including hidden, unsupported,
malformed and redundant facts that add no item. A zero, excessive, repeated or
decreasing ID returns a generic error before consuming that entry; earlier
entries in the same chunk remain consumed. A malformed object or a payload with
a nonempty `conversation_id` naming another owner consumes version only.
The caller establishes conversation ownership; `New` does not authenticate it.

Full replay and incremental feeds produce the same items and version, including
chunks split between companion boundaries, early reports, creations and endings.
Different conversation owners can
consume the same IDs without sharing items, successor attribution, pending
pairs, send claims or main-work state. The owner serializes `Feed`, `Render`,
`Items` and `Version`; `Fold` itself has no locks or goroutines. Folds constructed
with `New` copy payloads on ingestion. Production Store folds use the history
references described below for converted kinds. Both rendering paths return
detached items with copied content, so snapshot mutation cannot change fold state.

Each item also has recorded `Session`/`Agent`, `NoChild`, optional `Turn`/`Parent`,
`Status`, `Active`, `Shown`, a plain one-line `Summary` and kind-specific
`Content`. Standalone items leave turn and parent unset and are inactive.
Accepted sends also leave turn and parent unset, but remain active while queued
and become inactive at a valid terminal outcome.
Main-work items retain the recorded turn and leave parent unset; running text
and calls are active, while closed work and ending rows are inactive. Updates
retain the creating item's attribution and visibility, except for the send
delivery provenance and drop/loss visibility rules below.
Child work retains its recorded turn and uses an older agent item's creating ID
as `Parent`; see [parent repair](thread-package-agents-and-background-work.md#parent-repair) for unresolved evidence and
ancestor closure.
`plainSummary` collapses whitespace and removes control characters, with a kind
label when the result is empty. Rendered `Content` retains source JSON, including
unknown fields, as inert display data; summary normalization does not rewrite it.

### Standalone source entries

Production Store folds use `newReferenceFold` and private creating-entry-ID
recipes for plain `user_message`, `turn_end`, `notice`, `compaction` and
`session_divider` items, following
[ADR 045](../decisions/045-thread-index-over-history.md#decision). This covers
every notice and boundary source in the table, plus both ending sources handled
by `foldWork`: protocol `turn_end` and `main_turn_interrupted`. The fold keeps
their attribution, order, revision and visibility, but no `Item.Content` payload
copy behind the recipe. `Fold.Render` restores the public `Content` bytes from
history without re-encoding JSON, preserving unknown fields and original
formatting. The public item and wire shapes are unchanged.

Plain user messages retain neither text nor summary in the reference fold.
Rendering derives `Summary` through `plainSummary`, collapsing whitespace and
removing control characters; an empty result falls back to `user message`.
Skip normalization at creation rather than normalizing and then clearing the
summary: clearing it releases retained bytes but still pays for discarded
allocations. `TestReferenceSummaryDeferred` guards that creation cost.

`New`, `Items`, `Observe` and `FeedChanges` remain source compatible for legacy
inline folds. Assistant text and ordinary tools use the richer recipes described
under [main work](#main-work-source-entries); accepted sends use the
[send-merge recipes](#accepted-sends-and-durable-outcomes) below. Agents and
shell-lifecycle calls still use the renderer's inline-content fallback, with
conversion tracked by [#3453](https://github.com/pyrycode/pyrycode/issues/3453).
Pending outcomes and inherited `parent_ending` content use
[history and ancestor references](#scoped-joins-pending-outcomes-and-closure). See
[snapshot rendering and retry](thread-package-background-store.md#snapshots-and-retry)
for source resolution and the remaining Store content retention.

| Raw source type | Item kind | Rendered content and status |
| --- | --- | --- |
| `message` with `role: user` | `user_message` | Text, attachment IDs and recorded client/device/send metadata; `Status: delivered`. |
| `session_divider` or unmatched `session_transition` | `session_divider` | Recorded boundary cause/reason, session IDs, agents and reset handoff outcome; `Status: done`. |
| `compaction_boundary` | `compaction` | Trigger and recorded token counts, preserving absent versus zero; `Status: done`. |
| `banner` | `notice` | Banner text and fields; Codex reroutes retain this banner representation; `Status: done`. |
| `model_refusal_fallback`, `model_refusal_no_fallback` | `notice` | Refusal banner and recorded details; `Status: done`. |
| `unrecognized_message` | `notice` | Recorded raw content and details; `Status: done`. |
| Supplied logged `attachment_offered` | `notice` | Attachment ID, filename and recorded offer fields; `Status: done`. |
| `prompt_answered` | `notice` | Correlation, decision, asking session, saved context and answer values; `Status: done`. |

Notices retain the source type in `Subtype`; other kinds leave it empty.
Attachment offers currently have a live-only producer: accepting a supplied
logged fact does not add persistence to that emitter. Answer notices consume
[resolved prompt facts](history-package-producers.md#resolved-remote-prompt-answers-2973),
including saved selected/free-text answers and any recorded truncation.

Claude Agent/Task calls own [agent lifecycle items](thread-package-agents-and-background-work.md#agent-lifecycle).
Parented text, ordinary tools and nested agents fold in [child lanes](thread-package-agents-and-background-work.md#parent-repair).
Linked Claude/legacy Bash calls retain ordinary identity with
[background shell lifecycle](thread-package-agents-and-background-work.md#background-shells).
[Cache persistence and epochs](thread-package.md#cache-and-epochs) belong to the
background store. [Daemon shadow wiring](thread-package.md#shadow-lifecycle-and-evidence)
folds history without app publication; thread protocol and read-mark migration
remain downstream.

### Accepted sends and durable outcomes

`sendFact` folds the [raw accepted-send facts and linked outcomes](history-package-producers.md#accepted-sends-and-linked-outcomes-2972),
including hidden outcomes. A valid `send_accepted` creates one permanent
`user_message` with acceptance-entry ID/revision, status `queued`, active true
and `Order == 0`. It is shown by default, subject to explicit `Entry.Shown`.
Content retains recorded safe text, attachment IDs, `device_id`, app
`message_id`, `accepted_at`, `client_sent_at` and `queued_msg_id`, when present.
New acceptances copy the actual `QueuedMessage.ID`; `queuedSendHistory.accepted`
rejects zero before recording. The queue handle is a JSON integer in
`1..18446744073709551615`, independent of the history-ID range. Both
`validQueuedSendFact` and `sendFact` accept legacy omission but reject supplied
zero, null, negative, decimal/exponent notation, wrong-type or overflowing
values without float conversion. Case-insensitive variants must all match the
typed decoded handle: accepting conflicting positive variants could expose one
handle while queued and another after delivery. Malformed facts consume fold
version without creating or reserving an item.

Normal attribution applies: absent provenance stays unknown or uses recorded
successor fallback; explicit `kind: none` remains distinct and blocks that fallback.

Production `newReferenceFold` sends retain only history IDs and lifecycle
metadata in their items and `contentRecipe` values: the creating acceptance ID,
optional delivery ID and terminal `outcome` entry reference. Queued, delivered,
dropped and lost items retain no `Content` or `Summary` bytes, and their recipes
contain no copied payload literals. `renderShared` derives summaries from
acceptance text for queued/drop/loss and from delivery text for delivered sends,
using `plainSummary` and the empty-text fallback `user message`. Legacy `New`
folds keep their inline content and summaries with the same public output.

| Raw outcome | Required reason | Acceptance-item result |
| --- | --- | --- |
| `send_delivered` | `delivered` | Inactive, `delivered`, linked message's entry ID as order; acceptance visibility retained. |
| `send_dropped` | `removed` or `give_up` | Inactive, `dropped`, hidden, order zero. |
| `send_lost` | `daemon_restart` | Inactive, `lost`, shown, order zero. |

Each terminal names `accepted_entry_id`; delivery additionally names the valid
user `message` through `delivery_entry_id`. Both references must resolve to
earlier valid entries in this conversation's fold. Missing, zero, out-of-range,
forward, wrong-kind, malformed or foreign-conversation references resolve
nothing. A message claimed by another acceptance cannot be reused. Invalid
links reserve neither acceptance nor message, so a later valid outcome can
still resolve the send. The first valid terminal in entry-ID order wins; later
duplicate or conflicting terminals change no content, state or revision and
cannot claim another message. Text, app IDs, daemon-local queue IDs and
timestamps never establish the join. Equal app IDs across devices still create
distinct acceptance items.

Delivery keeps the acceptance ID/kind. At render time, `renderShared` applies
`deliveredSendContent` to the acceptance and delivery entries, starting content
from the linked message's safe delivered text, attachments and client metadata,
then enriches it with the referenced outcome. This preserves legacy JSON
encoding and unknown fields. Recorded acceptance `device_id`, `message_id`,
`accepted_at` and `client_sent_at` override
the corresponding delivered fields only when present. `deliveredSendContent`
decodes those sender fields in JSON order with case-insensitive matching before
merging. Selecting them by map iteration would make duplicate case variants
nondeterministic; the last matching recorded value wins. It removes delivered
case variants when replacing a field, keeping one canonical sender key.

The queue handle has acceptance-owned presence as well as value.
`deliveredSendContent` removes every delivered `queued_msg_id` case variant
before restoring the recorded acceptance handle when present. Otherwise a
linked message could invent a handle for a legacy acceptance. Drop/loss retain
the acceptance handle unchanged, and legacy acceptances remain handle-free
through every terminal revision. Live additions/changes, catch-up, pages and
reopened history expose the same permanent item ID and recorded handle.

Only a queued, active `user_message` with a valid handle supplies a Drop/Send now
target: `conversation_id` plus `content.queued_msg_id`, guarded by the accepted
`item.id` as optional `accepted_item_id`. Clients should always include the guard:
an acceptance item `100` with queue handle `7` must not act on item `205` when a
new daemon run reuses `7`. The daemon matches only its current-run acceptance
binding on a live queue entry, never a historical fold join, text or app ID.
Valid omission keeps legacy behavior and the reuse risk. Terminal items remain
inactive and keep the handle only as historical data. Queue IDs are
conversation/run-local; reuse cannot reactivate an old item or establish a
history join. See the
[queue wire contract](../../protocol-mobile.md#queue-v2) and
[queue durability boundary](msgqueue-package.md#durability-boundary-in-scope-vs-out).

Before a reference-backed delivery changes either identity, `sendFact` renders
both acceptance and delivery candidates to validate source availability. A
missing, unreadable, malformed or null source fails the feed without changing
either item, recording a delivery recipe or claiming/suppressing the message.
Only after validation does the recipe acquire the delivery ID. The join also
works across chunks after recent-cache eviction: resolution uses the current
chunk, then the inclusive 1 MiB recent cache, then conversation-bound history
`Lookup`. Missing or unusable acceptance, delivery or outcome content rejects
the entire rendered candidate through generic `ErrUnavailable`; Store withdraws
all usable items and watermarks with `StateUnavailable`, without content or path
disclosure. Repair followed by Store `Retry` reconstructs a fresh fold from
authoritative history. Public rendering returns detached content. See
[snapshots and retry](thread-package-background-store.md#snapshots-and-retry).

The linked message supplies the summary and receiving `Session`/`Agent`/`NoChild`
already resolved under the existing attribution rules, including any successor
fallback at message ingestion. Acceptance-time or outcome-time bindings and
later session switches cannot replace that snapshot. See
[operator receiving provenance](history-package-producers.md#operator-delivery-provenance-2983).
Drop/loss render acceptance content and retain acceptance provenance. Every
valid terminal references its history entry for rendering under `outcome`,
retaining reason, `occurred_at` and links, and sets revision to that entry ID.
Only legacy inline folds copy the terminal payload. Outcome visibility cannot
override the table's rules.

After delivery, `Items` omits the claimed message's public row; unmatched
messages retain their delivered identity and order. **Keep internal item indexes
stable:** removing a reconciled message from storage would shift indexes held
by assistant/tool state and corrupt later updates. The claim suppresses only
the public row. A chunk ending between message and outcome can temporarily
expose both rows; later reconciliation leaves the permanent acceptance item.
Acceptance alone does not split assistant text; the delivered message still
closes main text runs when ingested.

Without a valid terminal, acceptance stays queued. **Loss requires an explicit
recorded `send_lost`; absence never infers it.** Recorded loss identifies missing
durable resolution after restart, not proof that the agent never received the
bytes. A successful write followed by a failed outcome append can leave that
same unresolved history. See the
[producer durability boundary](history-package-producers.md#accepted-sends-and-linked-outcomes-2972)
and [ADR 042](../decisions/042-daemon-built-thread.md#the-six-approved-decisions).

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
classification. Claude and metadata-free `Bash`/`local_bash` calls retain inline
launch evidence; `setOutcome` promotes foreground terminal content to a
creating-entry recipe. A later background task link restores inline lifecycle
rendering.
Claude/legacy Agent/Task calls remain agents.
Codex calls with any of those names are ordinary reference-backed tools.
Selecting by tool name alone would skip eligible Codex calls. Result, denial,
interruption and turn-ending fields reference their original report IDs through
`setOutcome`; pending outcomes also retain only IDs and decoded lifecycle facts.

Both modes preserve exact output bytes: the creating payload keeps its original
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
[snapshot rendering and retry](thread-package-background-store.md#snapshots-and-retry).

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
[immutable publication](thread-package-background-store.md#snapshots-and-retry).

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
call retains its creating input through its recipe or inline payload and gets
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

Reference-backed child text and ordinary tools keep independent intrinsic and
previous recipes alongside their item metadata; inline agents also keep separate
recipes for inherited fields. `restoreChild` restores the intrinsic recipe
before mutation; `resolveChildren` captures the updated base
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
See [snapshot rendering and retry](thread-package-background-store.md#snapshots-and-retry).

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
the [retained shadow capture](thread-package.md#shadow-lifecycle-and-evidence)
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

### Visibility and live-state exclusions

For supported item-producing facts, explicit `Entry.Shown` wins over the default.
Absent it, `standalone`, `boundary`, `sendFact`, `mainDecode` and `agentWork` follow
the relevant `historyEntryShown` semantics without importing `cmd/pyry`:

| Fact | Default shown |
| --- | --- |
| Accepted send, delivered user message, compaction, refusal, unrecognized output, saved answer | True |
| Banner | False only for `level: info` with `stops_turn` false |
| Supplied attachment offer | False |
| Raw reset, clear, agent switch, recovery, workspace change, capacity eviction | True |
| Raw idle sleep or daemon restart | False |
| Standalone legacy transition | False for `idle_evict`, true for other supported reasons |
| Main/child assistant run, ordinary call or Agent/Task launch | True |
| Main `turn_end` | False only for normal successful `end_turn`; abnormal ends are true |
| `main_turn_interrupted` | True |

A normal end has `is_error` false, `outcome` absent/empty or `success`,
`terminal_reason` absent/empty or `completed`, and no `error_category`.
Tool reports update the call's state/content without replacing its creating
visibility. `main_turn_opened` remains row-free even with explicit shown true.
Send outcomes update the acceptance rather than adding a row: delivery retains
acceptance visibility, while drop forces hidden and loss forces shown, regardless
of explicit visibility on the outcome.

Public reachability is independent of `Shown`: hidden rows remain in
`Fold.Items` and can supply reachable parents. Unresolved children and claimed
delivery rows are omitted; an omitted ancestor omits its descendants.
`resolveChildren` includes affected descendants when joins or reparenting change
reachability, even when a descendant's final fields and `Rev` stay unchanged.
After joins settle, `recordShown` visits affected indexes in creation order,
requiring each child's older parent to be publicly reachable and reusing cached
reachability for unrelated parents. This captures late first appearances and
descendant withdrawal without scanning independent trees. See
[public touches and read marks](thread-package.md#cache-and-epochs).

Hidden supported facts still create items. Visibility cannot admit an excluded
source type: open permission/question prompts and live readings never create
items, even with explicit shown true. Turn phase, stalls, retries, compaction in
progress, thinking/tool/task progress, rate limits, context usage, model/session
facts, MCP status, model/slash-command lists, reply suggestions and session errors
remain live state, as [ADR 042](../decisions/042-daemon-built-thread.md#decision)
requires. A saved answer is a notice; the prompt awaiting that answer is not.

### Boundary pairing and legacy scope

`boundary` pairs a raw divider with a later legacy companion one-to-one inside
the same fold. The key is the payload's `occurred_at` instant normalized to UTC,
previous/new routing session IDs and mapped legacy reason. Entry `TS` is not the
key: raw and legacy producers can stamp it separately.

| Raw cause | Matching legacy reason and IDs |
| --- | --- |
| `operator_reset`, `claude_clear`, `agent_switch` | `clear`, with the same previous and new session IDs |
| `idle_sleep`, `capacity_eviction` | `idle_evict`, with both legacy session IDs equal to the raw predecessor's evicted ID |
| `recovery`, `workspace_change`, `daemon_restart` | No companion pairing |

Pending queues retain repeated keys across feeds. Each matching transition
consumes the oldest pending raw divider for that key; further transitions without
a pending match create their own divider. Standalone legacy `clear`,
`idle_evict`, `recovered` and `workspace_change` are supported. Other reasons and
unknown raw causes consume version without affecting boundary state.

A companion advances conversation version while preserving the raw item's ID,
order, revision, cause, content and visibility, including both recorded agents
and any `reset_handoff_outcome`. Its own visibility cannot replace the raw
divider's visibility. See
[runtime boundary producers](history-package-producers-runtime-lifecycle.md#divider-causes-and-reset-outcomes).

Each non-restart raw divider or standalone legacy transition delimits private
legacy join scope for untagged main work and agent work without a saved lifetime.
A matching pair counts once; `daemon_restart` alone does not delimit that scope. Clearing
successor attribution on restart and delimiting legacy joins are separate
decisions. Agent joins use
[their recorded lifetime or legacy scope](thread-package-agents-and-background-work.md#identity-and-recovery).

### Recorded provenance and successor fallback

Explicit `Entry.Session` wins for the item. Only recorded `claude` or `codex`
facts set `Agent`; a known routing session ID with an unknown agent remains known
without defaulting to Claude. Explicit `kind: none` sets `NoChild` and means no
producing child. Absent provenance means unknown, leaves `NoChild` false and may
permit successor fallback. Explicit no-child provenance blocks that fallback.

Raw divider attribution describes the predecessor: use its recorded previous
session/agent when entry metadata is absent. Separately, `new_session_id` and
`next_agent` establish fallback for later untagged items. A standalone
non-eviction legacy transition retains its recorded new session ID and can supply
successor fallback; its agent is known only from entry provenance naming that
same successor. See
[legacy transition provenance](history-package-producers.md#legacy-transition-provenance-2982).

A matching legacy `clear` can supplement an unknown successor agent only when
that raw divider is still the latest boundary and the recorded session IDs agree.
It cannot rewrite earlier items or move fallback past a newer boundary. Explicit
provenance on an ordinary item likewise does not replace the boundary fallback
for subsequent untagged items.

**Eviction has no successor.** Its legacy payload mirrors the evicted ID in both
fields for compatibility; treating `new_session_id` as a successor would attribute
later untagged messages to an ended child. Both eviction forms clear fallback,
while the eviction item retains its recorded session ID even if its agent is
unknown. A successor-free daemon restart also clears fallback. Later recorded
boundaries may establish a successor again, including when routing IDs are reused.

**Saved answers retain their asking session.** A `prompt_answered` payload can
record an older `session_id` without harness metadata. Preserve that ID with an
unknown agent ahead of boundary fallback; otherwise an answer arriving after a
switch would be attributed to the successor. Explicit entry provenance still
wins. No fallback or later companion changes an earlier item's attribution.

### Validation and testing

`decode` checks object shape, required discriminating fields and known DTO field
types before items, pending pairs, scope or attribution change. Unsupported or
malformed payloads affect version only. **DTO unmarshalling alone is insufficient:**
Go's JSON decoder accepts `null` into scalar fields and scalar array elements as
zero values, including values inside string-valued maps. Checking only decoded
values would let an invalid `next_agent` alter boundary state, a null saved
answer enter a notice, or `input: {"path": null}` split text and create a tool
call. `validRecordedFields` validates known fields recursively, including
case-insensitive keys, nested saved answers and map elements. Nullable pointers,
maps and slices remain valid; their scalar elements cannot be null. Unknown
fields stay inert. Nullable optional token counts therefore preserve absent,
null and explicit zero source content.

`mainDecode` also validates parent fields, identity-loss markers and supplied
opening references before changing joins or text runs. Decoding only the public
DTO would miss these optional recorded fields and could admit truncated identity
or turn a malformed recovery reference into an unrelated closure.

`TestStandaloneItems` checks whole items with Claude, Codex, unknown and explicit
no-child provenance. `TestFoldReplay` compares every two-chunk partition with
replay and checks independent conversation owners; `TestFoldOwnership` checks ID
bounds and detached snapshots.
`TestBoundaryPairing`, `TestBoundaryAttribution` and
`TestBoundaryMalformedNeutrality` cover pairing, successor changes, old answers
and eviction. `TestMalformedRecordedFields` asserts version-only consumption and
unchanged pending pairs, scope and attribution before later matching and replay;
`TestNullableRecordedFields` protects legitimate nullable content.

`TestReferenceRendering` compares reference and legacy items across feed sizes,
including exact JSON formatting, unknown fields, detached content and the empty
user-summary fallback. `TestReferenceWorkRendering` compares every observation
and public touch in both modes through empty/nonempty appends, early terminals,
child closure/reopening and late text, then resolves old text and terminal IDs
after recent-cache eviction. `TestReferenceToolClassification` checks lifecycle
selection across Claude and Codex tools. `TestReferenceWorkSourceFailure` requires
atomic unavailable rendering/observations for missing creating, text or terminal
entries; `TestStoreReferenceWorkRetry` covers missing/unreadable text and enrichment
reads during replay and affected tail publication, followed by repair and `Retry`.
`TestReferenceEndingOutcomes` checks pre-creation reports, lane endings and creation
revision floors; `TestReferenceShellCompanion` checks distinct order/content IDs
in both companion arrival orders. `TestReferenceInheritedEnding` compares public
touches and observations through nested closure, late text and reopening after
cache eviction, including selected children whose ancestor is omitted.
`TestStoreCapturedInheritedEnding` retains a captured query root and detached
results across late text and reopening, including inline nested agents.
`TestReferenceEndingDependencyFailure` rejects missing/unreadable/malformed
ancestor dependencies atomically; `TestStoreReferenceEndingRetry` checks repair
and `Retry` for an evicted child-ending dependency.

The unchanged
[generated/shadow and recorded corpus checkpoints](thread-package.md#reusable-fold-regression-corpus)
constrain rendered items, revisions, consumed version and last-shown watermark.
`TestReferenceRetainedBytes` compares unchanged `testFoldCorpus(6738)` in both modes,
requiring zero converted item Content, child-copy Content, assistant summaries
including child copies, inline append buffers, copied pending outcomes and copied
parent endings. It checks the production compatibility map remains unused,
child ending caches remain absent, and intrinsic/previous recipe snapshots carry
no ancestor JSON, including recipes on inline agents. User summaries, logical
recipe storage, the bounded recent cache and deferred agent/shell content are
accounted separately. This fold-only measurement excludes Store publication/query
retention; see the separate
[render-on-output verification](thread-package-background-store.md#store-verification).
Zero copied outcome/ancestor bytes do not establish total Store memory use.

`TestMainTextRuns`, `TestMainToolTerminals` and `TestMainEndings` cover text
splits, retained evidence, early and conflicting outcomes, activity and visibility
with Claude, Codex and metadata-free fixtures. `TestMainScopeAndRecovery`,
`TestMainReferenceNeutrality` and `TestMainScopedEndAndLateTerminal` cover reused
identities, original-opening references, source isolation and inactive late work.
`TestMainValidationAndLaunchers` checks child/launcher exclusions;
source-specific launcher fixtures matter because Codex treats the same names as
ordinary tools. `TestMainMalformedStateNeutrality` compares the whole fold state
after malformed input, catching damaged pending joins even when current rows
look unchanged. `testMainReplay` checks every two-chunk partition against full
replay; `TestMainReplayAndOwnership` checks copied pending reports, detached
content and independent owners. Run the offline package checks with
`go test -race ./internal/thread`.

`TestMainTextClosureTracking` checks retained generations, legacy scopes,
inactive late text, original-opening recovery and child isolation, including
unchanged settled revisions and `LastShownVersion`. `TestMainTextClosureWork`
compares fixed ingestion after fourfold growth in closed openings and historical
open-run peaks. Counting only successful closures or yielded map keys would miss
historical-capacity scans. Measure registration/removal, skipped inspections,
tracking growth/copying and public ordering/cleanup too, including repeated
open/close cycles and empty closure. See the related
[public-touch capacity regression](thread-package.md#cache-and-epochs).

`TestTextAppendBytes` compares exact JSON and summary output with the former
map-based append, including case variants, escapes, control characters,
per-payload UTF-8 replacement and input/snapshot ownership.
`TestTextAppendChildren` and `TestTextEmptyProvisionalClosure` check append and
empty-delta behavior through provisional closure and reopening.
`TestAffectedPublicationOwnership` and `TestAffectedPublicationTextLifecycle`
retain internal publications and query roots after append-buffer initialization,
then check immutable JSON, borrowed Content, exact suffix changes, concurrent
reads and detached public results through later appends and child reopening.
Capturing only before initialization misses mutable-buffer corruption because
the first nonempty append replaces the creating payload's allocation; retaining
only detached `Fold.Items` output cannot prove internal publication ownership.
`TestTextBufferPublicationOwnership` forces spare capacity and growth, checks
detachment before writes and subsequent private reuse, and requires the
allocation/copy work to be counted. The fixed-size scenarios in `testFoldCorpus`
alone cannot detect quadratic work within one long
reply. `TestTextAppendScaling` therefore grows delta counts from 256 to 1,024 in
one main run and child runs while open, provisionally closed, reopened and across
all three phases; `TestFoldIngestionScaling` grows the corpus from 1,000 to 4,000
entries. Both require less than twice the work per delta/entry. Counters include
initialization, inspected/skipped candidates, lookups, ordering/maintenance,
JSON/summary bytes, comparisons and buffer allocation/growth/copying; input
generation, explicit snapshots and digests are excluded.

`TestPublicTouchesReference` compares entry-scoped bookkeeping with an independent
full-walk oracle. `TestPublicTouchAffectedWork` grows unrelated items and trees
fourfold while keeping affected entries fixed. A foreground launch result can
close descendants and advance their revisions, concealing revision-only
appearance tracking. Its late-appearance fixture first links the parent to
background work, leaving a descendant's revision unchanged when its ancestor
becomes public; omission and reappearance also preserve the earlier watermark.

`TestSendDelivery`, `TestSendTerminalStates` and `TestSendLinksAndTerminals`
cover permanent identity, delivered content/order, supported reasons,
unresolved acceptances, linked/unmatched messages, conflicting terminals,
message reuse and equal app IDs across devices with Claude, Codex and
metadata-free fixtures. `TestSendOwnershipAndProvenance` checks receiving
fallback across session switches, explicit no-child attribution, independent
owners reusing durable IDs and detached input/snapshots. `TestSendTextRuns`
checks text splits and later main-work updates after message suppression.
`TestSendMinimalAndCasing` covers absent sender fields, nullable attachments and
deterministic duplicate case variants. `TestSendMalformedNeutrality` compares
whole fold state before later valid resolution, catching damaged joins or
attribution even when rows appear unchanged; wrongly typed/null scalars or links
and unsupported reasons change version only. These scenarios use `testMainReplay`
to compare full replay with every two-chunk partition, including the gap between
message and outcome.

`TestReferenceDeliveryEvicted` proves the delivery left both the current chunk
and recent cache and asserts its authoritative lookup before comparing merged
acceptance bytes, provenance, suppression, revisions, touches and watermarks with
legacy folding. Separate message/outcome chunks alone can remain cache hits and
leave the eviction path untested. `TestReferenceDeliveryFailure` checks missing,
unreadable, malformed and null acceptance/delivery sources before claiming either
identity. `TestReferenceSendRendering` compares complete observations, changes,
touches, items and watermarks with legacy through each lifecycle and after
eviction, asserts zero retained send content/summary or recipe payload literals,
and checks detached results. `TestReferenceSendSourceFailure` withholds entire
renders and observations for acceptance/delivery/outcome faults after eviction.

`TestStoreReferenceSendRetry` proves repair and fresh replay after missing or
unreadable acceptance/delivery reads in replay and tail, and outcome reads in
replay. **A terminal outcome is still in the current chunk on its only tail
mutation.** Ignored tail entries reuse unchanged publications and cannot force
an outcome lookup; exercise evicted-outcome failure through replay or explicit
rendering. For tail acceptance/delivery faults, consume 256 ignored entries
before appending the terminal fact. Waiting for that consumed version proves
FIFO eviction independently of tail chunk scheduling, without full-page padding.
