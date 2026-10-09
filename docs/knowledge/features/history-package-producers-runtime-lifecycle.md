# `internal/history` — runtime and startup lifecycle

Part of [history producers](history-package-producers.md#producers-2114-2115).

## Runtime boundaries and main-work closure (#3013)

### Divider causes and reset outcomes

Runtime facts use `SessionTransition.Cause` independently of legacy `Reason`.
`runtimeDivider` accepts a captured owner, a nonempty predecessor and a changed
routing pair; first creation, equal-ID/no-op and refused transitions produce no
divider. Unknown causes and unresolved ownership do not invent one. Each actual
boundary appends one `session_divider`, retaining `conversation_id`, `cause`,
`occurred_at`, known `previous_session_id` / `new_session_id` and
`previous_agent` / `next_agent`. A switch retains both actual agents. Divider
metadata names the captured predecessor when its routing ID and harness are
known; unavailable source metadata remains absent. The facts use daemon routing
IDs, never native agent thread IDs or a later active binding.

| Runtime divider cause | Explicit visibility | Legacy delimiter |
| --- | --- | --- |
| `operator_reset` | Shown | `clear` |
| `claude_clear` | Shown | `clear` |
| `agent_switch` | Shown | `clear` |
| `recovery` | Shown | None |
| `workspace_change` | Shown when supplied | None |
| `idle_sleep` | Hidden | `idle_evict` |
| `capacity_eviction` | Shown | `idle_evict` |

Capacity eviction retains its own closure cause even though its legacy delimiter
is the same hidden `idle_evict` as sleep. Both lack a successor in the raw
divider; legacy wire payloads still mirror the evicted ID into both fields.
Workspace change is vocabulary with no current producer. Recovery onto a new
session can record a divider, but `RotateBootstrapForSelfHeal` remains uncalled
in production: this history path adds no recovery or self-heal policy.

`activeSessionStarter.resetThenRotate` passes the completed wrap-up's actual
boolean to `Pool.RotateForNewSessionWithHandoff`: `written` when a note was
written and `skipped` otherwise. Only an operator-reset divider carries the
optional `reset_handoff_outcome`. Callers without that observation, including
`RotateForNewSession`, leave it absent/unknown. Invalid values such as `pending`
or `assumed` are omitted; neither timing nor a successful rotation proves that
a handoff note was written. The outcome carries no note text.

### Hidden openings and interrupted main work

`startTurnIfNeeded` writes hidden `main_turn_opened` once with the conversation,
minted turn ID, occurrence time and captured source metadata. Thinking alone can
open the turn; the fact preserves its identity without persisting thought text,
tool inputs or prompts. Live-state readings still do not become thread items.
`trackRuntimeTool` retains unfinished ordinary main-thread tools by their existing
tool call ID and name. Successful/failed results and denied calls retire them;
an ordinary turn end retires the turn. Agent/Task launchers and child tools are
excluded from this closure vocabulary; their lifecycle and background-task
endings remain [#2969](https://github.com/pyrycode/pyrycode/issues/2969).

The drain's `closeRuntimeSource` flushes buffered predecessor text, writes shown
`main_tool_interrupted` for each unfinished ordinary main call, then writes one
shown `main_turn_interrupted` for the open turn before the divider. Endings retain
the existing tool/turn identities, actual closure cause, occurrence time and
source metadata. Repeated closure adds no second ending; already ended turns,
completed/failed tools and denied calls are not interrupted again. A child crash
retaining the session closes preceding work with `child_exit` and no divider.
Exit epochs protect newer same-session work and delivery reservations from stale
exits, including reservations made before replacement output. Rotation retains
the retiring source for the exit callback: reading only the current routing tag
or last output source can instead identify the successor.

### Publication ordering

Ordinary callbacks capture and retain boundaries under the short
output-acceptance lock, without storage,
network I/O or waiting for the drain. Each boundary waits for its predecessor's
accepted-output watermark; delayed A→B→C facts retain both routing pairs and their
captured owners. Evictions also wait for the matching consumed producer stop,
including late parsed tails, so closure retains the eviction cause rather than
falling back to `child_exit`. Interactive output, confirmation-only operator
placement and channel posts share drain/publication coordination: old buffered
text precedes interruption, interruption precedes the divider, and successor
entries follow it in durable entry-ID order. Confirmation placement retains its
whole-queue fence. Never wait for the drain while holding the post gate.

A pending channel hold lasts until boundary publication finishes, even after
the boundary leaves its pending queue; releasing it at dequeue lets a post pass
the divider. Delivery release checks retained open main turns, not just running
phase projection: a denial can open successor work without a running phase.
Committed switches wait through transition publication, row publication and
transport sealing before reset exclusion releases. Both relay configurations
install this runtime path, including history-only operation.

### Producer incarnation isolation

Idle sleep and capacity eviction reactivate under the same routing ID.
`beginRuntimeProducer` allocates a fresh
daemon-local incarnation before each Claude/Codex `Run` starts its output
goroutines. Envelopes, retained stops, source state, accepted-output watermarks
and captured boundaries carry it; durable/wire provenance keeps the existing
routing identity. Sealing only the ID would suppress the replacement's hidden
opening and terminal idle. `streamSessionTag.Rotate` and successful
`CompareAndSwap` register every routing alias under the same acceptance lock as
boundary capture, before any output. Registering only on first output leaves a
chained boundary or pre-output eviction targeting incarnation zero, reopening
old work or waiting forever for the wrong stop. Refused announced clears preserve
another live tag's destination ownership; older unbound callers retain zero.

Delayed sealed-predecessor events keep their captured provenance and closed turn
address without opening work or changing successor busy/idle placement. Late text
retains chunk bounds and sequence progression. Other conversations stay untouched.

### Legacy exclusion and write failures

All four runtime types remain raw history facts; their payloads never enter
legacy transports. Validated facts instead project to nonvisual info-banner
receipts in history and, when recorded while running, live/replay (#3026).
See [legacy eligibility](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965).
Visibility never grants raw facts transport eligibility.
Best-effort nil/failed storage does not change closure, sealing, eligible legacy
publication, payload meanings or recipient gates. Append failures use the existing
content-free discriminants, never payloads or filesystem error text. Daemon-start
reconciliation uses the [startup closure point](#startup-reconciliation-3014).
See [ADR 042](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks)
and [drain source lifecycle](streamsup-package-draining-turnevents-into-the-interactive-emitter.md).

## Startup reconciliation (#3014)

`runSupervisor` calls `reconcileStartupHistory` synchronously after instance
ownership and construction of the single history store, before channel delivery,
queue, relay or pool producers can write. Both relay-enabled and history-only
daemons use this path. Discovery snapshots `Registry.List()` without a filter,
including archived conversations, and reads their existing raw `Store.Page`
logs. Missing and empty logs receive no divider; discovery creates no empty
history directories. Conversations created after this snapshot receive none.
No Claude transcript or second durable log supplies recovery evidence.

`readStartupMainWork` walks newest-first across raw pages and segments, passing
opaque cursors unchanged and terminating only on `AtStart`. It completes each
conversation's read before appending to that conversation, retaining identities
and outcomes rather than historical text or tool inputs. Legacy receipts omit
the opening and interruption identities needed for reconstruction.

For each unfinished main turn, `closeStartupMainWork` appends shown
`main_tool_interrupted` facts for unfinished ordinary calls, then exactly one
shown `main_turn_interrupted`, all with cause `daemon_restart`. Turns are ordered
by their earliest recorded opening evidence and tools by tool ID. Thinking-only
openings count. Successful/failed results, denials and recorded tool interruptions
retire calls; normal/interrupted turn endings close the turn even if later facts
survive. Parent-attributed child work is excluded. Agent/Task launchers can
identify the main turn but receive no ordinary-tool interruption; agent and
background-task session-ending inference remains
[#3032](https://github.com/pyrycode/pyrycode/issues/3032). Explicit reported endings
are retained by [the agent/task observer](#agenttask-attribution-and-reported-endings-3030).

After closure, every surviving nonempty raw log receives one `session_divider`
per startup, even when all its work already ended. It has cause `daemon_restart`,
explicit `shown: false`, and daemon-authored session metadata `kind: none`.
All startup facts share the captured startup occurrence time as `occurred_at`
and entry timestamp. Their durable entry-ID order places tool closures before
turn closure, then the divider, then new traffic. `closeStartupMainWork` is the
composable startup closure-before-divider point, alongside `closeRuntimeSource`
at runtime. Legacy history projects these facts as nonvisual receipts; startup
does not backfill the in-memory replay ring or publish live frames. The restart
divider intentionally has no predecessor/successor session IDs. Receipt validation
accepts its captured conversation and occurrence identity; requiring a predecessor
would leave a restart read hole that running-turn tests cannot detect.

**Recover identity without inventing provenance.** Tagged turns match recorded
source and turn ID. Untagged turns match recorded turn/tool IDs within legacy
session-transition and non-restart divider scopes; restart dividers do not split
these scopes. Existing entries stay unchanged. Closures retain known old-source
metadata, while unavailable legacy provenance stays absent. The registry's
current session/agent cannot attribute old work, and missing IDs are not minted.
Legacy denials lack parent attribution: parent-attributed evidence excludes
child-only denial candidates, while an identifiable denial-only main turn can
still be closed.

**A recovery reference targets only its original opening.** Startup interruptions
carry optional `runtimeHistoryFact.TurnOpenedEntryID` (`turn_opened_entry_id`),
the earliest recorded main-opening evidence's durable ID; runtime facts omit it.
Recovery may append after a legacy delimiter, outside the opening's scope.
`readStartupMainWork` applies referenced interruptions exclusively to that opening,
never also to the append-time scope. Otherwise a later turn or tool reusing the
same IDs would be falsely closed. Recorded interruptions suppress repeated
closures across starts and late facts cannot reopen the interrupted turn; each
start still adds its own divider. A saved tool-only interruption also prevents
repeating that tool's closure when the turn interruption did not survive.

A read failure skips both closure and divider for that conversation: partial
evidence cannot establish unfinished work. Append failures remain best effort.
Neither blocks startup nor reconciliation of other conversations. Failure logs
contain only event, validated canonical conversation ID when available, and
content-free classifiers; payloads, cursors, source IDs and filesystem error text
stay out. Invalid registry IDs are rejected without logging their value.

## Agent/task attribution and reported endings (#3030)

### Retain identities independently of report prose

`interactiveTurnEmitterV2.observeAgentHistory` runs on the captured-source drain
when runtime facts are enabled and the producer is explicitly `claude`. Codex
and unknown producers create no agent/task facts. The existing mapped tool,
denial and background reports retain their payloads and routing; attribution
adds only identities, links and supplied outcomes to the same raw history log.
Inputs, descriptions, summaries and patches stay in their existing reports,
outside `agentHistoryFact` and attribution memory.

| Observed report | Additional raw evidence |
| --- | --- |
| `ToolStart` named `Agent` or `Task` with a nonempty call ID | `agent_call_observed`: call ID, tool name and supplied parent call ID. |
| Recognized launcher `ToolUpdate` with `completed` or `failed` | `agent_call_result`: call ID, supplied parent call ID and reported status. |
| Recognized launcher `ToolCallDenied` with a usable call ID | `agent_call_denied`: call ID and `denied` status. |
| Background start, update, progress or roster row with a usable task ID | `background_task_observed`: task ID, even when this is its first report. |
| Background start or enriched roster row with complete task and call IDs | `background_task_linked`: task-to-call association. |
| Background update with a usable task ID and nonempty status | `background_task_outcome`: task ID and supplied terminal status. |

A usable task ID survives independently of an unavailable launching call ID.
`observeTaskIdentity` refuses an empty task ID or one named in `TruncatedFields`;
an empty/truncated call ID suppresses only the link. Denials refuse call IDs named
in either `TruncatedFields` or `DroppedFields`. Description-only truncation and
dropped denial prose do not invalidate usable identities. Repeated observations
retain evidence within the same lifetime rather than minting a new identity.

### Join within a durable producer lifetime

Every fact carries `conversation_id`, `lifetime_id` and `occurred_at`; entry
metadata retains the captured Claude source. Join call/task IDs only within that
conversation and producer lifetime, with the recorded source. The emitter retains
one minted `conversations.NewID` lifetime in its conversation/source/incarnation
state. Different sources, child incarnations and conversations remain isolated
even when Claude reuses call/task IDs.

**A daemon-local incarnation counter is not a durable join key.** Counters restart
after daemon restart; joining on a routing session plus that counter would merge
old and new work after reopening. Minted lifetime IDs persist in the raw facts,
so a fresh emitter or reactivated child has a different durable identity even
when local numbering and routing IDs repeat. `endTurn` retains lifetime and
launcher attribution because background work can outlive the main turn.
An unsealed producer exit or conversation teardown retires it through
`retireAgentHistory`, clearing retained call facts, task links/outcomes and
terminal call markers together with the lifetime. A child respawn inside one
runner can reuse its source/incarnation: clearing only the lifetime would let an
old join infer gone under a new lifetime, or an old ending suppress a replacement
task using the same IDs. `closeRuntimeSource` and `closeForConversation` share
this cleanup. Sealed runtime closure retains predecessor attribution and terminal
evidence for captured late reports; a successor incarnation has its own lifetime.

**A sealed source can first observe a launcher after its boundary.**
`rememberLauncher` must run before the sealed-path return, and recognition of
later results/denials uses `launcherTurns` membership. A nonempty originating-turn
address is insufficient: background-only sources may never have opened main
work. Retaining those calls permits their later reports to join the predecessor
without opening a main turn or child lane or changing successor phase/lifetime.
`releaseConversation` also retains usable denial evidence received before the
sealed source's first launcher, even before a lifetime is minted. Otherwise
empty-state cleanup would forget the denial and allow a later roster to infer
gone. Unsealed denial handling opens main work, so ordinary ordering cases alone
do not exercise this retention boundary.
`recordAgentFact` flushes preceding buffered text before the append, preserving
accepted output order without opening or changing a main turn.

### Keep launch results separate from task outcomes

An unlinked foreground Agent/Task result establishes finished/failed work from
its `completed`/`failed` status. A complete task-to-call link in the same lifetime
makes that result a launch report. The background task's reported ending comes
only from `BackgroundTaskUpdated` with nonempty status, including `completed`,
`failed`, `stopped` and unfamiliar supplied values. Status is an open report
vocabulary, not a verification that the subprocess stopped.

Preserve the result, link and outcome independently. A link can arrive after
either report and still determine launch meaning; eagerly folding a tool result
into a permanent task ending would lose this distinction. Denial remains
independent terminal evidence, including when a failed result also arrives.
Status-empty updates, summary/patch-only reports, progress and main-turn end
supply no background ending. Only a later complete roster can establish
[gone](#gone-requires-a-later-complete-roster-3031); session-ending inference
remains [#3032](https://github.com/pyrycode/pyrycode/issues/3032).

Identity/link facts are explicitly hidden; results, denials, task outcomes and
gone facts are explicitly shown. Neither value grants foreground presentation
or raw legacy transport eligibility. Successful validated facts use nonvisual receipts at
their original durable IDs/timestamps, counted toward the legacy unread target
independently of raw visibility under #3029. Receipt-only traffic does not confer
sight or advance a read mark by itself. See
[receipt validation and visibility](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965),
[ADR 042](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks)
and [the attribution contract](../../specs/architecture/3030-agent-task-history.md).

### Gone requires a later complete roster (#3031)

`observeGoneRoster` records `background_task_gone` with `status: gone` when a
received complete Claude roster omits a previously linked task whose Agent/Task
call was observed in the same conversation, captured source and durable child
lifetime. Gone means **unknown outcome**, never a reported finish or evidence
of success/failure. It retains the task and call IDs, tool name, known parent
call ID, lifetime, captured source metadata and occurrence time. A parent first
supplied by a launch result enriches the retained call for this later fact.

A complete roster has `DroppedTasks == 0` and a nonempty, untruncated `task_id`
on every row; an empty reported roster qualifies. Compare roster task IDs with
linked task IDs, never tool-call IDs. A complete link requires both nonempty,
untruncated task and call IDs. Description-only truncation invalidates neither
the link nor roster completeness.

**Validate the originating identity before enriching a later row.**
`sessionBackgroundTaskHold.retainStart` refuses a missing/truncated task ID;
`enrichedRoster` preserves call-ID truncation on enriched rows. Checking only
the later row would let a shortened originating task ID gain a complete-looking
call link after its truncation marker was lost. `observeTaskIdentity` still
rejects incomplete identities at the observer boundary.

Inference checks retained links before observing the incoming roster's rows.
A refresh before a usable link or observed call is known cannot be applied
retroactively; another qualifying refresh is required. A usable denial or a
reported nonempty task status suppresses inference even when received before
the link. Linked tool results remain launch reports, and main-turn completion
retains eligibility. Once inferred, both task and call retain terminal markers:
repeated refreshes, starts, links and late reports cannot duplicate gone or
reopen ended work within that lifetime.

No refresh supplies no ending. Incomplete rosters, retained-roster reads, bounded
join eviction/pruning and child-reset housekeeping alone never establish gone.
The hold's enrichment cache is separate from the emitter's retained eligibility;
pruning the cache does not discard already observed links. Only forwarded fresh
roster events reach inference, after the hold releases its leaf mutex. Inference
and its state remain owned by the single drain, including captured late rosters
for sealed predecessors; other conversations, sources and lifetimes remain
unaffected. Codex and unknown producers infer nothing. No extra live refresh,
session-ending inference or second durable log is introduced. See
[the roster contract](../../specs/architecture/3031-agent-roster-gone.md).
