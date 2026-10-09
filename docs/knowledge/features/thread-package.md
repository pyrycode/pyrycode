# `internal/thread` — conversation-owned history fold

`Fold` builds deterministic items from supplied raw `history.Entry` values for
one conversation. It is the supplied-entry folding foundation of
[ADR 042](../decisions/042-daemon-built-thread.md), with no consumer wiring yet.
Use [raw history and its metadata](history-package-shape.md#shape), including
hidden facts; legacy receipt projections discard facts needed by the fold.

## Main-thread folding

### Identity, order and version

Construct each owner with `New(conversationID)`, then call `Feed` with strictly
increasing entry IDs in `1..history.MaxEntryID` (`2^53 - 1`). Gaps are allowed.
`Item.ID` is its creating entry's ID, `Order` equals that ID and `Kind` is
immutable. `Rev` names the latest content or state change, floored at the creating
ID when an earlier pending report is applied. Standalone items keep their
creating revision; main-work items change in place. Redundant terminal reports
do not advance revision.
`Items()` returns items in entry-ID order. Entry timestamps never sort them.

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
pairs or main-work state. The owner serializes `Feed`, `Items` and `Version`;
there are no locks or goroutines. Payloads are copied on ingestion, and `Items` returns detached items
with copied content, so input or snapshot mutation cannot change fold state.

Each item also has recorded `Session`/`Agent`, `NoChild`, optional `Turn`/`Parent`,
`Status`, `Active`, `Shown`, a plain one-line `Summary` and kind-specific
`Content`. Standalone items leave turn and parent unset and are inactive.
Main-work items retain the recorded turn and leave parent unset; running text
and calls are active, while closed work and ending rows are inactive. Updates
retain the creating item's attribution and visibility.
`plainSummary` collapses whitespace and removes control characters, with a kind
label when the result is empty. `Content` retains copied source JSON, including
unknown fields, as inert display data; summary normalization does not rewrite it.

### Standalone source entries

| Raw source type | Item kind | Saved content and status |
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

Accepted-send linkage and delivery reconciliation remain separate extensions in
[#3052](https://github.com/pyrycode/pyrycode/issues/3052); agent/background folding
remains separate in [#3047](https://github.com/pyrycode/pyrycode/issues/3047).
Accepted-send and agent/task lifecycle facts create no items yet. Cache,
persistence integration, epochs, daemon wiring, thread protocol and read-mark
migration remain downstream.

### Main-work source entries

`mainWork` folds recorded main facts, including hidden ones, without consulting
live state or current session bindings.

| Raw source type | Item kind | Saved content and status |
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
`message` closes open text runs throughout the conversation; a matching turn
ending closes its own run. Subsequent text creates a new item. Queued acceptance,
child traffic and tool reports alone do not split text. Saved text retains its
recorded whitespace; only the summary is normalized.

Parent-attributed text and tools create no main items or joins. Claude and
metadata-free Agent/Task launches split main text but create no ordinary call,
and their reports create no ordinary call item. Codex tools named Agent or Task
are ordinary calls: the name alone cannot establish Claude launcher semantics.
Agent/task lifecycle and background facts remain excluded from this fold.

### Scoped joins, pending outcomes and closure

Join keys use recorded `Entry.Session` (including its agent kind) plus `turn_id`,
and add the recorded call ID for tools. With no entry metadata, they use private
legacy boundary scope plus turn/call IDs. Tagged and untagged work never join,
even when successor fallback gives them the same displayed session and agent.
Other sources, turns and conversation owners remain independent. Empty or
explicitly truncated/dropped join identities cannot create joins.

Tool reports can precede creation and remain pending across feed chunks. The
first valid terminal outcome in entry-ID order wins; later duplicate or
conflicting outcomes change neither content, state nor revision. The creating
call retains its input and gets the saved report when it arrives. Applying an
earlier report keeps revision at least the creating ID.

The first matching turn ending closes running text as `done` and unfinished
ordinary calls as `interrupted`, storing the raw ending under `ending` on each
call. **A turn closure supplies no successful tool result.** Already terminal
calls retain their outcome; child work is unaffected. Endings received before
creation remain effective across chunks. Late text may coalesce but stays
inactive; late calls are inactive and later reports cannot replace their ending.

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
Absent it, `standalone`, `boundary` and `mainDecode` follow the relevant
`historyEntryShown` semantics without importing `cmd/pyry`:

| Fact | Default shown |
| --- | --- |
| Delivered user message, compaction, refusal, unrecognized output, saved answer | True |
| Banner | False only for `level: info` with `stops_turn` false |
| Supplied attachment offer | False |
| Raw reset, clear, agent switch, recovery, workspace change, capacity eviction | True |
| Raw idle sleep or daemon restart | False |
| Standalone legacy transition | False for `idle_evict`, true for other supported reasons |
| Main assistant run or ordinary call | True |
| Main `turn_end` | False only for normal successful `end_turn`; abnormal ends are true |
| `main_turn_interrupted` | True |

A normal end has `is_error` false, `outcome` absent/empty or `success`,
`terminal_reason` absent/empty or `completed`, and no `error_category`.
Tool reports update the call's state/content without replacing its creating
visibility. `main_turn_opened` remains row-free even with explicit shown true.

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
legacy join scope for untagged main work and later folding extensions. A matching
pair counts once; `daemon_restart` alone does not delimit that scope. Clearing
successor attribution on restart and delimiting legacy joins are separate
decisions. Agent joins remain a separate extension.

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
