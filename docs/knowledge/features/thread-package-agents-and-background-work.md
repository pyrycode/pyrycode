# `internal/thread` — agents and background work

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

`agentWork` deduplicates affected call IDs after validation. Creation, call
reports and supported parent enrichment recompute that call; task reports and
links recompute the owner. Reassociation recomputes both previous and new
owners. A roster reaches only owners affected by usable row evidence, including
previous owners on reassociation; omissions and unusable identities do not
trigger a group sweep. `applyAgent` reads only the affected call's associated
tasks and reports. `agentGroup.taskIDs` retains lexicographic task-ID order per
owner, maintained on ownership changes rather than collected and sorted from
all group tasks during application.

**Index pending owners as well as created calls.** Restricting the ownership
index to visible items would lose links received before launch. Pending tasks,
reports and associations remain available within the scoped group even without
a created call item; they create no public item by themselves.

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
outcome or order. `agentGroup.addReport` reconciles matching durable/mapped
companions within the same scoped identity, status and report kind as one logical outcome:
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

Shell reports and task ownership changes use the same affected-call
recomputation and lexicographic associated-task index as agents; reassociation
recomputes both owners. First-lifetime promotion transfers that index with
pending call/task/report evidence and recomputes the moved owners, preserving
companion enrichment and first-terminal order. The transfer runs once when the
source first acquires a lifetime; subsequent ordinary reports do not scan saved
promotion evidence again. [Identity and recovery](#identity-and-recovery)
defines which pre-lifetime evidence can move.

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

Production assistant text and ordinary child tools use the same
[history recipes and exact-byte renderer](thread-package-main-thread-folding.md#main-work-source-entries)
as main work. Their items and intrinsic/previous copies retain creating/text IDs
and enrichment recipes, with assistant summaries derived only when rendered.
They retain no inline payload, accumulated text, assistant summary or append
buffer. Codex tools remain ordinary calls even with shell/agent names. Agents and
linked Claude/legacy shells still retain inline launch/link/report content, whose
conversion remains [#3453](https://github.com/pyrycode/pyrycode/issues/3453).
Foreground shell terminal content uses history recipes too; pending outcomes and
inherited endings retain references rather than payload copies.

`resolveChildren` requires the parent item's immutable `Kind` to be `agent`
and its creating ID to be strictly less than the child's, in this conversation
and the same recorded source/lifetime or legacy scope. Registration in
`agentGroup.calls` alone cannot establish ownership: `registerShell` shares that
registry for [ordinary shell lifecycles](#background-shells), so `Bash` and
`local_bash` remain `tool_call` items and cannot own children. Testing rejection
with `Read` alone would miss this shared-registry boundary.
`childGroup` pins pending evidence to that original group; reused call IDs
cannot steal predecessor children. Tagged and metadata-free evidence remain
distinct regardless of displayed successor attribution. Self, newer or
mismatched-source/lifetime/scope parents cannot join. Current bindings,
timestamps and tool names provide no fallback. See
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

`TestChildRejectsShellParent` checks both shell names with metadata-free and
recorded Claude sources. After every feed, only the ordinary shell is returned:
attributed text, completed tools, nested Agent/Task rows and their active or
completed descendants stay omitted, including after shell completion.
`TestChildAgentParents` checks genuine Agent/Task ownership and ancestor closure.
Both tests use `testMainReplay` to compare every two-chunk partition with full
replay.

**Keep a shell's original parent lane separate from promoted lifecycle evidence.**
`resolveChildren` retains ordinary child parenting when the first lifetime
adopts the shell's task evidence. Moving its parent group too could hide a
child whose parent remains in the original legacy group. The retained ordinary
owner must also accept later foreground results when no task link exists;
`TestShellFirstLifetimePendingCreation` and `TestShellUnlinkedChildAfterLifetime`
pin these cases.

**Retain early ordinary reports independently of main-turn slots.** An explicit
main opening replaces its turn state; storing an early result only there would
lose it before child creation. `childReports` keeps first-result/denial IDs and
decoded status/field facts by original source/lifetime-or-legacy-scope, turn and
call, including reports whose parent is not yet known. A new producer lifetime
reusing turn/call IDs cannot have its report suppressed by, or replaced with, a
predecessor terminal.
For an uncreated child call, adoption compares that evidence only with its own
lane ending, excluding main-turn closure. Results wait for creation, retain the
call's original input, and produce `done`, `failed` or `denied` in place for
foreground work. The first terminal wins;
later conflicting reports cannot change its outcome. An earlier report floors
revision at creation. Production `childReports` retains no report JSON;
rendering resolves the saved content-source ID. Only legacy `New` folds populate
the detached compatibility payload map. See
[pending outcomes](thread-package-main-thread-folding.md#scoped-joins-pending-outcomes-and-closure)
for distinct terminal-order and content-source IDs on companion reports.

A parent-attributed `turn_end` creates no row. It closes matching child text as
`done` and unfinished foreground ordinary calls as `interrupted`, rendering raw
`ending` evidence from its history ID on the text and calls it closes. An ending
received before creation remains effective for later calls in that lane. Already
terminal outcomes stay fixed, and neither this ending nor a main-turn ending completes a nested agent
or linked background shell.

**Derive ancestor closure from genuine agent finals, keeping intrinsic child
state separate.** `restoreChild` restores only the affected child's intrinsic
item and recipe before mutation, once per entry; resolution captures its updated
base before deriving closure. First registration treats the live item as intrinsic
through that entry's mutations, so late parenting cannot discard a final
received in the same entry. Unrelated children retain their derived state.
`applyAgent` compares recomputed lifecycle state against intrinsic state and
schedules dependents only on actual changes, including ending-content enrichment.
Foreground shell results use the same targeted ordinary mutation hooks.

Intrinsic and previous recipes are independent value snapshots, including on
inline agents with derived reference fields: text-ID lists keep their own
lengths and enrichment maps use copy-on-write. A single mutable
recipe would carry provisional `parent_ending` into the intrinsic base and keep
it after reopening. Resolution compares recipe state as well as item metadata,
without history reads during ingestion; rendering later resolves creating, text
and terminal references through the shared source. Production `parent_ending`
fields name the original ending ancestor item; neither recipes nor child ending
caches copy its JSON. Converted child items and their intrinsic/previous copies
retain no `Content`; `legacyChildEnding` serialization caches exist only for
legacy folds.
Removing only the derived field preserves late text and genuine child endings.

**An ancestor ID alone cannot detect ending-content enrichment.** The inherited
reference also carries the ancestor's revision as a private equality fact, so
late enrichment advances affected descendants' revisions without reading history
during ingestion. Content still resolves solely through the ancestor item ID.
`renderShared` obtains that item's content from the same item/recipe view as the
child, even when the ancestor is outside selected output. A deferred inline agent
can supply that content at render time without becoming a separate copied ending
store; missing dependencies withhold the whole candidate. See
[reference resolution and retry](thread-package-main-thread-folding.md#scoped-joins-pending-outcomes-and-closure).

`saveChild` indexes relationships by original `agentKey` and parent call,
including unresolved links; reparenting removes the old edge. After each entry,
`resolveChildren` expands the dirty set through indexed agent descendants and
processes only those children in creation order, resolving ancestors first.
`Fold.childEndings` caches ancestor item/revision references to avoid walking
unrelated ancestors; intrinsic parent finals supply their own reference.
Parent/link repair, closure, reopening and ending enrichment revisit the affected
tree without inspecting independent trees, even within the same agent group.
Unrelated standalone/main-only entries perform zero child-resolution work.
Replace `childDirty` after resolution: clearing or later ranging over a reused
map can inspect capacity retained from a former large closure, even with few
current children. Counting only yielded keys would hide that historical cost;
see the related [public-touch capacity regression](thread-package.md#cache-and-epochs).
Agent evidence separately recomputes only affected calls and their indexed
tasks/reports, retaining these targeted descendant-repair hooks for late
parent/link enrichment and lifecycle changes. These bounds cover agent and
child bookkeeping; observation and Store publication costs remain separate.

An ancestor ending makes unfinished text/tools `interrupted` and
inactive, rendering the ancestor's content under `parent_ending`; unfinished
nested agents become inactive without changing their intrinsic status or
inventing `EndedOrder`. Closure
propagates the original ending ancestor through provisionally closed nested
agents, rather than treating each intermediate agent as a new ending. Already
terminal children retain their own outcomes.
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

**Assert intermediate finals before later enrichment can conceal their loss.**
Whole/chunked/single-entry replay equality can agree on the same broken state,
and a later task link can recompute a final lost during first child registration.
`TestChildTargetedRepair` asserts the intrinsic `EndedOrder` immediately after
late parenting, then checks reopening, reparenting and removal of the old edge.

**Count inspected work, including rejected candidates.** A filtered full scan
can keep successful-mutation counts constant while its cost grows with history.
`TestChildAffectedWork` grows unrelated history and independent trees in the
same group from four to sixteen with a fixed affected tree. Registration,
restoration/base-copy, expansion and resolution counts stay identical across
ancestor closure, reopening, ending enrichment and child appends; unrelated
main-only appends have zero counts. `TestChildRejectedCandidatesCount` includes
self-parent rejection and omitted descendants in inspected work.

`TestAgentAffectedWork` grows unrelated same-group scenarios from sixteen to
sixty-four while fixing affected calls, tasks/reports and measured entries.
Each unrelated scenario includes both created and uncreated calls: skipping
calls without items must still count as inspected work. The control is large
enough to expose even one filtered group scan despite fixed serialization cost.
Counts include lookups, report candidates/comparisons and ownership-index
maintenance, with less than twice the work allowed for a result, task report,
reassociation and fixed-size roster. `TestAgentOwnershipOrder` checks lexical
order and absence of stale/duplicate edges after repeated roster reassociation.
`TestAgentPromotionWork` counts the one-time transfer separately, then checks
bounded ordinary reports with unrelated saved evidence retained, intact indexes
and unchanged first-terminal order. Setup, snapshots and `recordShown` are
outside these agent-lifecycle counters. The long-history corpus uses separate
scenario lifetimes, so its unchanged digests cannot replace this same-group
scaling proof.

**An ownership rejection test needs completed children as well.** Ancestor
interruption can expose a detached unfinished child through a status mismatch,
while an independently completed child keeps the same status with a missing
parent. `TestThreadShadowPairRejectsChildOwnership` checks done child tools and
finished nested Agent/Task rows against parent calls derived independently from
raw facts; it rejects zero and wrong older parents and a main tool gaining one.
`TestThreadShadowRawOwnerLateRepair` preserves supported report-only repair.
These synthetic controls complement the
[retained authenticated checkpoints](thread-package.md#shadow-lifecycle-and-evidence);
Store/full-replay equality alone cannot detect a shared parenting error.

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

The [reusable long-history corpus](thread-package.md#reusable-fold-regression-corpus)
adds compact golden checkpoints without replacing these independent semantic
and provenance assertions. `testFoldGoldenCheck` in each test package pins the
production baseline commit `a920b90cc6bfd2f1fb8461e9f85ce6f19934afa6`, input
identity, entry count, input SHA-256, canonical history SHA-256 and ordered
checkpoint versions. Generated input identity is `fold-scenarios-v1/100-entries`
with count 30,000; its input hash covers that identity string. Retained inputs
hash the complete capture file bytes. Raw shadow history uses `raw-shadow-v1`
without normalization. Recorded replays use
`recorded-normalization-v1/synthetic-shell-context-v1`, pinning the normalized
history separately from the unchanged recording.

`testNormalizeRecorded` runs before folding only the three parser-derived
histories. Nonempty top-level emitter-created `turn_id` and `lifetime_id` values
map by first occurrence into separate UUID namespaces:
`00000001-0000-4000-8000-<12-digit ordinal>` for turns and
`00000002-0000-4000-8000-<12-digit ordinal>` for lifetimes, starting at one.
Every occurrence of the same identity uses the same mapping, preserving joins.
Entry `TS` and an existing emitter `occurred_at` become
`2026-01-01T00:00:00Z` plus entry ID nanoseconds. These recordings supply none
of those turn/lifetime IDs themselves. Recorded identities, nested content,
durations and timestamps are retained; no folded output field is normalized.
`TestThreadRecordedBaselines` derives each history twice and requires equality
before checking its committed history hash. Shell derivation still supplies the
explicitly synthetic creation/result/turn-end context described above.

Each checkpoint requires all five fields: consumed `Version`, item count
`Items`, `LastShownVersion`, canonical item digest `JSON` and raw byte digest
`Content`. `testFoldDigest` hashes the complete creation-ordered item list,
including all Item fields and Content. `testJSON` decodes with `UseNumber` and
re-encodes with `encoding/json`'s sorted object keys and default string escaping,
preserving numbers such as `9007199254740993` without float64 rounding. The
separate Content SHA-256 concatenates, for each item in that same order,
big-endian uint64 item ID, big-endian uint64 Content byte length, then exact
Content bytes. Canonical JSON erases whitespace differences; the framed raw
digest detects byte corruption that canonical equality would miss.
`TestFoldDigestCorruption` checks that distinction and lossless numbers.

**Required-field presence must be checked before scalar equality.** Decoding
missing/null count or watermark fields directly into scalars can produce zero
and pass an empty checkpoint. Both `testFoldCheckpoint.UnmarshalJSON` readers
require typed non-null pointers before constructing expectations; explicit zero
counts/watermarks remain valid, while omissions, nulls and invalid numeric types
fail. `TestFoldCheckpointRequiredFields` in both packages exercises these cases
through full-manifest decoding. Missing inputs/manifests, incomplete metadata or
checkpoint sets, changed hashes and changed checkpoint values fail ordinary
assertions; ordinary tests neither skip missing fixtures nor write expectations.

Deliberate golden replacement is a separate operation, documented beside the
helpers. The thread command writes the generated and raw-shadow manifests;
the cmd command writes `cmd/pyry/testdata/<recording>.json.golden` for all three
recorded inputs:

```bash
go test ./internal/thread -run '^TestFoldBaselines$' -args -write-fold-baselines
go test ./cmd/pyry -run '^TestThreadRecordedBaselines$' -args -write-fold-baselines
```
