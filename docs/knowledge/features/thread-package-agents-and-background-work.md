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
