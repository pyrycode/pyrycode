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
pairs, send claims or main-work state. The owner serializes `Feed`, `Items` and
`Version`; there are no locks or goroutines. Payloads are copied on ingestion,
and `Items` returns detached items with copied content, so input or snapshot
mutation cannot change fold state.

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
as `Parent`; see [parent repair](#parent-repair) for unresolved evidence and
ancestor closure.
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

Claude Agent/Task calls own [agent lifecycle items](#agent-lifecycle).
Parented text, ordinary tools and nested agents fold in [child lanes](#parent-repair).
Linked Claude/legacy Bash calls retain ordinary identity with
[background shell lifecycle](#background-shells). Cache,
persistence integration, epochs, daemon wiring, thread protocol and read-mark
migration remain downstream.

### Accepted sends and durable outcomes

`sendFact` folds the [raw accepted-send facts and linked outcomes](history-package-producers.md#accepted-sends-and-linked-outcomes-2972),
including hidden outcomes. A valid `send_accepted` creates one permanent
`user_message` with acceptance-entry ID/revision, status `queued`, active true
and `Order == 0`. It is shown by default, subject to explicit `Entry.Shown`.
Content retains recorded safe text, attachment IDs, `device_id`, app
`message_id`, `accepted_at` and `client_sent_at`, when present. Normal attribution
applies: absent provenance stays unknown or uses recorded successor fallback;
explicit `kind: none` remains distinct and blocks that fallback.

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

Delivery keeps the acceptance ID/kind and starts content from the linked
message's safe delivered text, attachments and client metadata. Recorded
acceptance `device_id`, `message_id`, `accepted_at` and `client_sent_at` override
the corresponding delivered fields only when present. `deliveredSendContent`
decodes those sender fields in JSON order with case-insensitive matching before
merging. Selecting them by map iteration would make duplicate case variants
nondeterministic; the last matching recorded value wins. It removes delivered
case variants when replacing a field, keeping one canonical sender key.

The linked message supplies the summary and receiving `Session`/`Agent`/`NoChild`
already resolved under the existing attribution rules, including any successor
fallback at message ingestion. Acceptance-time or outcome-time bindings and
later session switches cannot replace that snapshot. See
[operator receiving provenance](history-package-producers.md#operator-delivery-provenance-2983).
Drop/loss retain acceptance content and provenance. Every valid terminal stores
its copied raw payload under `outcome`, retaining reason, `occurred_at` and
links, and sets revision to the outcome entry ID. Outcome visibility cannot
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
apply to foreground calls; [linked background shells](#background-shells) use
independent task lifecycle.

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
`message` closes open main text runs throughout the conversation; a matching turn
ending closes its own run. Subsequent text creates a new item. Queued acceptance,
child traffic and tool reports alone do not split text. Saved text retains its
recorded whitespace; only the summary is normalized.

Parent-attributed text and tools fold in [child lanes](#parent-repair), without
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
[background shell lifecycle](#background-shells).

### Scoped joins, pending outcomes and closure

Join keys use recorded `Entry.Session` (including its agent kind) plus `turn_id`,
and add the recorded call ID for tools. With no entry metadata, they use private
legacy boundary scope plus turn/call IDs. Tagged and untagged work never join,
even when successor fallback gives them the same displayed session and agent.
Other sources, turns and conversation owners remain independent. Empty or
explicitly truncated/dropped join identities cannot create joins.

Tool reports can precede creation and remain pending across feed chunks. The
first valid foreground terminal outcome in entry-ID order wins; later duplicate or
conflicting outcomes change neither content, state nor revision. The creating
call retains its input and gets the saved report when it arrives. Applying an
earlier report keeps revision at least the creating ID.

The first matching turn ending closes running text as `done` and unfinished
foreground ordinary calls as `interrupted`, storing the raw ending under
`ending` on each call. **A turn closure supplies no successful tool result.**
Already terminal calls retain their outcome; child work is unaffected. Endings
received before creation remain effective across chunks. Late text may coalesce
but stays inactive; late foreground calls are inactive and later reports cannot
replace their ending. A linked background shell outlives that turn; a late task
link can repair its provisional closure, and late launch results stay retained.

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
[their recorded lifetime or legacy scope](#identity-and-recovery).

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

## Agents and background work

### Agent lifecycle

`agentWork` creates one immutable-kind `agent` only from a valid mapped Claude
`tool_use` named `Agent` or `Task`, including a parent-attributed launch.
Metadata-free legacy launches qualify; explicitly non-Claude sources do not.
The mapped call entry supplies `ID`, `Order`, recorded `Turn`, retained input,
creation attribution and visibility. A preceding `agent_call_observed` supplies
lifetime/parent evidence, never the creating ID. Observations, links and reports
create no rows. The item starts `running` and active unless pending evidence
already changes it. Creation attribution and visibility stay fixed on updates.
Parent call evidence remains in content; [parent repair](#parent-repair) resolves
`Item.Parent` to the older creating ID and folds child text/tools beneath it.

`applyAgent` retains the launch JSON and adds copied `result`, `denial`,
`task_id`/`task_link`, `task_report` and `ending` fields as evidence arrives.
Status-empty mapped reports can enrich content without closing work; after a
final they are retained under `task_update`. Payloads, including patch strings,
remain inert data. A patch containing a status cannot supply that status.

| Evidence | Status and activity |
| --- | --- |
| Unlinked successful/failed tool result | `finished` / `failed`, inactive. |
| Tool denial, even with a task link | `denied`, inactive. |
| Linked task update with `completed` | `finished`, inactive. |
| Linked task update with `stopped`, `failed` or another nonempty word except `stopping` | Retains that status, inactive. |
| Supplied task `stopping` | `stopping`, active, no final. |
| Saved `background_task_gone` | `gone`, inactive; outcome unknown. |
| Saved `agent_ended_with_session` | `ended_with_session`, inactive, saved cause retained under `ending`. |

**Keep launch results separate from task endings.** A complete task-to-call link
makes tool results launch fields, even when received before the link. Recompute
the earliest genuine final in entry-ID order after linking. A late link can
remove a provisional foreground final, returning the item to active with no
ending if no genuine final remains, or expose an earlier pending task ending.
Neither `run_in_background` nor result text establishes the link. See
[producer launch/result separation](history-package-producers-runtime-lifecycle.md#keep-launch-results-separate-from-task-outcomes).

The first genuine final wins; later conflicting finals cannot replace its
outcome or order. `agentAddReport` reconciles matching durable/mapped companions
within the same scoped identity, status and report kind as one logical outcome:
the earliest evidence ID owns terminal order and the mapped payload supplies
content. Thus a later mapped companion may enrich the winning report without
moving its ending. `Item.EndedOrder` is zero before a final; content omits
`ended_order`. Afterward both use the winning evidence ID floored at creation,
so early reports cannot place an ending before its item. Observable content,
link or state changes advance `Rev`, also floored at creation; redundant reports
do not. Entry-ID order, never timestamps or map iteration, selects the final.

Progress, roster absence and main-turn end supply no ending in the fold. Gone
requires the [saved producer fact](history-package-producers-runtime-lifecycle.md#gone-requires-a-later-complete-roster-3031).
Session endings retain the recorded cause, including `daemon_restart`,
`child_exit` or `capacity_eviction`; a divider alone cannot end an agent.
`stopping` is consumed only when supplied. Current producers record no stop
acceptance fact, so an inbound stop request cannot imply either `stopping` or
`stopped`. See [ADR 042](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks).

### Background shells

`foldWork` creates Claude/legacy `Bash` and `local_bash` as ordinary `tool_call`
items; `registerShell` retains their owners for task lifecycle enrichment. A
complete usable task-to-call link enriches that existing item with copied
`task_id`, `task_link`, launch `result`, `task_report` and saved `ending` evidence
as available. It creates neither an `agent` nor a task row. Creating input,
`ID`, `Kind`, `Order`, `Turn`, attribution, visibility and existing child
parenting remain intact. Tasks without a usable matching call create no item;
early reports wait for their link and creation. Unlinked shells keep ordinary
foreground result and turn-closure behavior. Explicitly non-Claude calls,
including Codex Bash calls, keep ordinary semantics.

**A launch result cannot finish linked shell work.** `applyAgent` uses the
[first genuine final and companion rules](#agent-lifecycle) on the ordinary
item: `completed` becomes inactive `finished`; `failed`, `stopped`, `gone`,
`ended_with_session` and unfamiliar nonempty terminal words are inactive;
supplied `stopping` is active and nonfinal. Denial remains genuine terminal
evidence. A late link reclassifies an earlier result as launch evidence and
removes provisional main-turn closure, restoring active work when no genuine
final remains. A result arriving after turn closure is still saved, even when
the link arrives later. Neither a background flag nor launch-result text
establishes the join.

The winning report and actual saved session-ending cause stay fixed against
later conflicting finals. `Item.EndedOrder` and content `ended_order` use the
earliest valid terminal evidence ID, floored at creation; before a final the
item field is zero and content omits it. Actual content/state changes alone
advance `Rev`, also floored at creation, and snapshots remain detached.
Status-empty summary/patch reports enrich `task_report` or, after a final,
`task_update`; patch strings stay inert. Progress, roster omission and main-turn
end do not finish linked work. The fold never infers shell `gone` from a roster:
only validated saved lifecycle evidence supplies that status. A saved
`agent_ended_with_session` can end the linked shell with its recorded cause even
after the main turn ended; a divider by itself supplies no such ending. See
[independent session endings](history-package-producers-runtime-lifecycle.md#unfinished-agenttask-session-endings-3032).

### Identity and recovery

Joins belong only to this conversation and the recorded source, using saved
`lifetime_id` when available. Durable observations establish the active producer
lifetime used by their mapped reports. Without a lifetime, joins use recorded
session-transition/non-restart-divider scopes; restart dividers do not split
them. Tagged and metadata-free evidence stay distinct even if successor fallback
gives items the same displayed attribution. Reused call/task IDs in another
source, lifetime or legacy scope cannot mutate predecessor items. Results, links
and endings received before creation remain pending across feeds.

**Shell calls can predate the first saved producer lifetime.** Unlike Agent/Task
launches, ordinary Bash creation has no preceding `agent_call_observed`.
`promoteShellEvidence` reconciles earlier call/result/task evidence only from
the same recorded source and uninterrupted pre-lifetime scope when a validated
durable fact establishes a lifetime. It preserves original observation
references and never adopts established successor lifetimes or
boundary-separated predecessors, even when routing/call/task IDs are reused.
Current session bindings cannot supply the missing identity. Retain pending
ordinary owners as well as reports: moving reports alone loses early results
when creation follows the first lifetime fact.

Supplied `call_observed_entry_id` and `task_observed_entry_id` on a saved session
ending must resolve to earlier original observations with matching identity,
recorded source and available lifetime. Legacy mapped observations, including
individual tasks in a roster, can supply that original identity when durable
observations are absent. All supplied references must agree on the original
group; recovery uses its scope, never the ending's append-time scope. Unresolved,
null, zero, out-of-range, forward, wrong-kind or mismatched references join
nothing, with no fallback. See
[original-observation recovery](history-package-producers-runtime-lifecycle.md#recover-agenttask-work-and-original-identities-3032).

**Matching a task observation alone is insufficient when an ending also names
a call.** Validate the available saved task-to-call association before applying
either identity. Otherwise a correct task reference with a contradictory call ID
could end a second agent. `TestAgentRecoveryMismatchedLink` pins whole-fact
rejection. Validate that association before lifetime promotion as well:
otherwise a rejected ending still changes joins.
`TestShellInvalidEndingBeforeFirstLifetime` checks whole-state neutrality for
this ordering.

### Parent repair

`childWork` folds parent-attributed assistant deltas as `assistant_message` and
ordinary launches as `tool_call`. Claude/legacy nested Agent/Task launches stay
`agent` items with the same [lifecycle rules](#agent-lifecycle). Text coalesces
only within its recorded parent call, source, producer lifetime (or original
legacy scope) and turn lane. Any tool launch, including a nested agent, closes
only that lane's text run. Known child traffic stays outside main-turn state,
including parent evidence saved before a mapped launch or supplied only on a
report. Main Agent/Task launches still split main text; queued sends do not,
and delivered user messages retain their main-text closure behavior.

`resolveChildren` joins only to an `agent` whose creating ID is strictly less
than the child's, in this conversation and the same recorded source/lifetime or
legacy scope. `childGroup` pins pending evidence to that original group; reused
call IDs cannot steal predecessor children. Tagged and metadata-free evidence
remain distinct regardless of displayed successor attribution. Self, newer,
non-agent or mismatched-source/lifetime/scope parents cannot join. Current
bindings, timestamps and tool names provide no fallback. See
[durable producer lifetimes](history-package-producers-runtime-lifecycle.md#join-within-a-durable-producer-lifetime)
and [ADR 042's item model](../decisions/042-daemon-built-thread.md#item-model).

Unresolved parent-attributed creations and reports stay retained internally;
`Items()` omits unresolved children and descendants whose parent is omitted.
They never appear as main work. Supported late parent/link enrichment can
resolve an already older parent or move an existing child/nested agent to a
different older parent. Repair keeps `ID`, `Kind`, `Order`, creation attribution
and visibility fixed; a changed parent advances `Rev` to the repairing entry
ID. Ancestors resolve before descendants, and snapshots remain detached and
creation-ID ordered. A repaired ordinary call leaves its old lane's call map,
so a later ending there cannot interrupt it. Saved agent enrichment takes
precedence over stale lane evidence.

**Keep a shell's original parent lane separate from promoted lifecycle evidence.**
`resolveChildren` retains ordinary child parenting when the first lifetime
adopts the shell's task evidence. Moving its parent group too could hide a
child whose parent remains in the original legacy group. The retained ordinary
owner must also accept later foreground results when no task link exists;
`TestShellFirstLifetimePendingCreation` and `TestShellUnlinkedChildAfterLifetime`
pin these cases.

**Retain early ordinary reports independently of main-turn slots.** An explicit
main opening replaces its turn state; storing an early result only there would
lose it before child creation. `childReports` keeps copied first-result/denial
evidence by original source/lifetime-or-legacy-scope, turn and call, including
reports whose parent is not yet known. A new producer lifetime reusing turn/call
IDs therefore cannot have its report suppressed by, or replaced with, a
predecessor terminal.
For an uncreated child call, adoption compares that evidence only with its own
lane ending, excluding main-turn closure. Results wait for creation, retain the
call's original input, and produce `done`, `failed` or `denied` in place for
foreground work. The first terminal wins;
later conflicting reports cannot change its outcome. An earlier report floors
revision at creation, and pending payloads remain detached from caller input.

A parent-attributed `turn_end` creates no row. It closes matching child text as
`done` and unfinished foreground ordinary calls as `interrupted`, retaining raw
`ending` evidence on the text and calls it closes. An ending received before creation
remains effective for later calls in that lane. Already terminal outcomes stay
fixed, and neither this ending nor a main-turn ending completes a nested agent
or linked background shell.

**Derive ancestor closure from genuine agent finals, keeping intrinsic child
state separate.** `restoreChildren` and `resolveChildren` recompute the effect
on each feed. An ancestor ending makes unfinished text/tools `interrupted` and
inactive, retaining `parent_ending`; unfinished nested agents become inactive
without changing their intrinsic status or inventing `EndedOrder`. Closure
propagates through nested agents and preserves already terminal children.
A late background task link can reclassify a provisional foreground result as
launch evidence, remove its derived closure and reopen still-unfinished
children. Independently closed text and terminal calls remain closed. Eagerly
storing ancestor closure as a child's own terminal would make this repair
impossible.

### Agent validation and testing

Malformed/foreign facts and unusable identities consume version without changing
items or joins. Mapped launches, results and denials reject truncated/dropped
call, turn or parent identities before mutating pending state. A truncated or
dropped call link cannot join, but independently usable task evidence survives
for a later complete link, including its original ending order. Roster rows
validate task and call loss markers separately.

**Validate identity support per event kind.** A shared decoder must not promote
extra progress/update/roster envelope fields into call or parent evidence.
Started reports supply their supported call link; roster links come from usable
rows. Unsupported fields can remain in saved raw content without becoming join
keys. `TestAgentTaskExtraIdentity` covers this boundary; the identity-marker
tests compare whole fold state, catching damaged pending joins even while
visible rows remain unchanged.

`TestAgentLifecyclePermutations`, `TestAgentDurableCompanions` and
`TestAgentCompanionPendingFinals` cover early reports, late links and companion
boundaries. `TestAgentFirstFinal`, `TestAgentTaskEnrichment` and
`TestAgentSavedGoneAndEnding` cover conflicting finals, stopping/stopped,
summary-only enrichment and saved causes. Scope/reference, child-launch and
invalid-fact tests cover reused IDs, Codex exclusion, retained parents and
detached inputs/snapshots. `testMainReplay` compares every two-chunk partition
with full replay.

`TestShellLifecycleOrders` compares all creation/result/link/outcome orders
with full replay, every two-chunk partition and single-entry feeds.
`TestShellLateLinkAndEnrichment` covers turn-end-before-link, late results,
stopping, inert patches and omission; `TestShellFinalsAndRecovery` covers
conflicting finals, durable/mapped companions and original recovery references.
`TestShellLifetimeScopes` and `TestShellTaggedBoundaryReuse` pin source,
lifetime and boundary isolation, including sleep/reactivation with reused
routing IDs. Identity/invalid-evidence tests check retained input and creation
provenance, saved causes, Codex exclusion and detached content.

`TestChildLanes`, `TestChildMainIndependenceAndSend` and `TestChildScopes` cover
lane isolation, main/send behavior and invalid joins. `TestChildRepair`,
`TestChildLateRepairAndClosure` and `TestChildNestedLinkRepair` cover retained
unresolved work, stable repair and old-lane removal; `TestChildNested` covers
synthetic text/nesting and ancestor closure. `TestChildTurnEndBeforeCreation`
checks pending lane endings. `TestChildEarlyReportInPreviousMainTurn` and
`TestChildNewLifetimeEarlyReport` exercise first-report retention with recorded
Claude and legacy sources, conflicting outcomes and copied payloads;
`TestChildLifetimeReuse` and `TestChildEarlyResultAfterBoundary` pin original
groups. `TestChildMalformedNeutrality` compares state after malformed/foreign
and identity-loss facts, which change version only. These fixtures use
`testMainReplay` to compare full replay with every two-chunk partition.
**A reclassification test needs unfinished children at the provisional ending:**
already terminal children alone cannot prove reopening. `TestChildReclassification`
keeps active text and an unfinished ordinary call there, then checks both
reopen after the late link and close on a genuine task final.

`cmd/pyry.TestThreadAgentRecordedReplay` runs offline in the standard gate. It
passes the committed `parent_tool_use_v2.1.259.json` through `streamsup.NewParser`
and interactive raw-history emission into `Fold`. The test requires #2191's
Claude 2.1.259 capture provenance from 2026-09-09 and fails if the recording is
missing. Its one foreground Agent call has `run_in_background: false` but also
a recorded task link: the task completion supplies `finished`, while the tool
result remains retained launch content. Its two recorded `Read` calls become
`done` ordinary children beneath that single agent. The test checks their
creating IDs/order against emitted raw-history calls, recorded `file_path`
inputs (`$WORKDIR/alpha.txt` and `$WORKDIR/beta.txt`), result revisions and both
marker strings, then compares every replay partition. All evidence comes from
recorded frames. The recording contains no forwarded subagent text and
establishes no nesting-depth guarantee; synthetic child fixtures prove text and
nesting separately. Assert the saved prompt, background flag and both result
markers before comparing retained fields:
comparing two absent fields could pass while proving no retention. See
[capture evidence requirements](development-verification.md).

`cmd/pyry.TestThreadShellRecordedReplay` also runs in the standard offline gate.
It feeds every verbatim `frames` payload in recorded order through production
session parsing and interactive raw-history emission into `Fold`, requiring
these committed files under `internal/e2e/realclaude/testdata/`:

| Recording | Required capture provenance | Frames |
| --- | --- | --- |
| `task_notification_v2.1.259.json` | #2247, Claude 2.1.259, `2026-09-09T21:09:37+03:00` | 2 |
| `roster_after_finish_v2.1.280.json` | #2525, Claude 2.1.280, `2026-09-22T22:02:30+03:00` | 6 |

Missing files or changed provenance fail the proof. Both recordings omit
ordinary Bash creation/result context: the test supplies explicitly synthetic
creation, launch result and main-turn end using each recording's own call ID.
Recorded task evidence then proves one ordinary call with its task link and
`finished` completion, with no agent row. Creation ID comes from emitted
`tool_use`; `EndedOrder` comes from the saved `background_task_outcome`.
In #2525, the empty roster before completion leaves the shell active with zero
ending order. Filtering to completion alone would miss that behavior. Full
replay, every partition and single-entry feeds agree. These recordings prove
shell completion and omission behavior; they supply no agent-gone proof. See
[the producer's narrower gone evidence](history-package-producers-runtime-lifecycle.md#gone-requires-a-later-complete-roster-3031).
