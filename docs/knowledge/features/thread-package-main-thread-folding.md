# `internal/thread` — main-thread folding

## Main-thread folding

| Document | Topics |
| --- | --- |
| [Source entries and accepted sends](thread-package-main-thread-folding-sources.md) | Item identity, order and version; standalone recipes; accepted-send lifecycle. |
| [Main work and scoped closure](thread-package-main-thread-folding-lifecycle.md) | Assistant/tool recipes, scoped joins, pending outcomes and closure. |
| [Validation and testing](thread-package-main-thread-folding-validation.md#validation-and-testing) | Replay and ownership checks, report-marker retention and memory accounting. |

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
