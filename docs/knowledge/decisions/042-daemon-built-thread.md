# 042. The daemon builds the conversation thread, and the apps only draw it

## Status

Accepted 2026-10-08 by the owner. Daemon thread delivery and compatibility retirement are implemented: #3718 retired replaced content publication, #3719 retired event replay, #3740 made thread negotiation mandatory, and #3741 removed non-thread delivery/projection and recovery branches. The context below preserves the original rationale; it describes the pre-thread system. App migration history remains separate from this daemon contract. This record is the complete in-repo statement of the design, originally worked out with a catalogue of past edge cases.

## Context

The daemon turns Claude and Codex output into neutral wire events and streams them to the apps. Each app rebuilds the conversation thread by itself. Mobile uses five stacked layers and a history reducer of about 1500 lines. Desktop uses one reducer of about 3700 lines. No code is shared between them, and the mobile protocol spec has grown past 850KB.

Six questions keep returning as bugs on both boards:

- How history joins live rows: pyrycode-mobile#1786, pyrycode-mobile#1913, pyrycode-desktop#1851, pyrycode-desktop#1437.
- Which agent a piece of work belongs to: pyrycode-mobile#1951, pyrycode-mobile#1822, pyrycode-desktop#1789, pyrycode-desktop#1872.
- Whether an agent is still running: pyrycode-mobile#1963, pyrycode-mobile#1752, pyrycode-desktop#1561.
- Which session a piece of state belongs to: pyrycode-mobile#1947, pyrycode-desktop#1749, pyrycode-desktop#1559.
- Which entries are shown, for read marks: pyrycode-mobile#1948, #2954.
- Where own and queued messages land: pyrycode-mobile#1558, pyrycode-mobile#1655, pyrycode-mobile#859, pyrycode-desktop#1213.

The daemon knows every one of these answers and sends none of them. Each app guesses twice, once for live events and once for history pages, and the guesses drift apart.

Facts checked against the code on 2026-10-08:

- **There is one pipeline.** Desktop connects through the relay with the same handshake and event stream as the phone, background-task events included. The separate ACP lane was removed in #1348.
- **History entry ids** from `internal/history` count per conversation from 1, are never reused, and continue past the last id on disk after a daemon restart. Writes are not flushed to disk, so a machine crash can lose the end of the log.
- **Four producers write the log:** the interactive emitter's `emit`, session transitions, the operator message writer at delivery, and the channel post writer.
- **The message queue (`internal/msgqueue`) and the replay buffer (`internal/eventring`, 1024 events per conversation) live in memory only.**
- **A background agent's tool call opens a fresh main turn** when none is open, because `startTurnIfNeeded` in `cmd/pyry/interactive_turn_v2.go` ignores the parent call id. That causes pyrycode-mobile#1951.

## Decision

The daemon builds the thread. Per conversation it keeps an ordered list of **items** and sends finished items. The apps stop rebuilding.

**The daemon owns the facts:** what an item is, its permanent id and place, its session, agent, turn and parent, its status, whether it is shown, the joining of text, results and sub-agent work, where each sent message lands, and what changed since a given version.

**The apps own presentation:** keeping items by id, sorting and drawing, collapsing tool runs, pinning a running agent, scroll anchoring, the markdown reveal, and the status line built from live state. They also keep messages the daemon has not yet accepted, and their offline copy.

**Live state is never an item.** Permission prompts and question batches, turn phase, stalls, API retries, compaction in progress, thinking progress, tool and task progress, reset status, rate limits, context usage, the announced model, session facts, MCP status, slash commands, the model list, reply suggestions and session errors stay live state. They are never stored as items, replayed or included in catch-up. On connect the daemon re-sends current values. Each live-state message gains conversation routing identity alongside its session tag, and retry and compaction status join the connect-time set.

Delivered supplied thread readings and family clears carry top-level
`conversation_id` from the retained source. Session tags may be equal, null or
omitted across conversations, so clients use that identity to route a clear to
its conversation and family. Clears remain empty `{}` payloads with
`session_state_cleared: true` and no request correlation. Conversation routing
adds no item, history/replay identity or read evidence; see the
[live-state wire contract](../../protocol-mobile.md#session-scoped-live-state-v2-supplied-delivery-contract).

### Item model

An item is one row-shaped fact. Its kind never changes.

| Kind | What it is |
| --- | --- |
| `user_message` | Text the operator sent, with attachments. Queued messages are these too. |
| `assistant_message` | One run of assistant text. A new one starts after a main-thread tool call, a delivered user message or a turn end. |
| `tool_call` | A call with input, result and denial. Background shell work adds its task lifecycle. |
| `agent` | An Agent or Task call and its lifecycle. Everything the agent does sits under it. |
| `session_divider` | A session started, ended or changed, with the reason. |
| `compaction` | Context was compacted, with token counts when known. |
| `turn_end` | A turn's stop reason and usage. Shown only when the turn stopped abnormally. |
| `notice` | Banners, refusals, Codex reroutes, unrecognized output, attachment offers, answered prompts. A sub-kind says which. |

Every item has `id`, `kind`, `order`, `rev`, `session`, `agent` (Claude or Codex), `turn` if any, `parent` (empty for the main thread), `status` (an open set of words such as running, done, failed, denied, queued, delivered), `active` (true while it can still change by itself, so no app reads meaning from an unknown status word), `shown`, and `summary` (one plain line for apps that do not know the kind). Kinds add their own fields.

- **Identity.** The id is the history entry id of the entry that created the item. It is opaque to apps, never reused, and stays below 2^53 so JavaScript reads it exactly. Changes happen in place. A user message also carries the sending device and the app's own message id. A match needs both, because the app's id alone is not unique.
- **Ordering.** `order` is the history entry id that created the item, and the only sort key. A queued message has no order until delivery, then gets the delivery entry's id, so it lands where Claude actually read it. The counter only grows, so nothing is inserted mid-thread. Children sort among main items but are drawn inside their parent. An agent's `ended_order` marks where it ended.
- **Versioning.** A conversation's version is the newest entry id folded into its items. Each change sets the item's `rev` to the entry that caused it. A random per-conversation `epoch` changes when old ids may no longer mean the same thing: after a refold with new folding rules, or after an unclean stop. A new epoch makes apps reload.
- **Storage.** See decision 1 below.

### Update messages

Each carries the conversation, epoch and new version.

| Message | Meaning |
| --- | --- |
| `thread_item_added` | A new item, in full. |
| `thread_item_changed` | Named fields of one item changed. Carries `base_rev`. |
| `thread_text_append` | Text added to a message item. Carries `base_rev`. |
| `thread_catch_up` | App asks for every item changed since a version. |
| `thread_page` | App asks for items older than an order value. |
| `thread_items` | Reply to either: a batch of full items, then a final marker. |
| `thread_reset` | The daemon cannot catch the app up. The app drops that conversation and loads the newest window. |

- Text is still coalesced for up to 250 ms. An app applies an append or change only if the item's rev equals `base_rev`, and otherwise asks for catch-up. A silent hole becomes a repair.
- There is no remove message. A dropped queued message becomes hidden with status dropped.
- **Catch-up.** On connect and on opening a conversation, the app sends its version and epoch. The daemon walks its log back to that version and sends the current state of every touched item, plus every active item and the parents of everything sent. A changed epoch or a too-large gap gets `thread_reset`. Versions survive restarts, so this replaces the replay buffer, resync and every app's history-to-live join. Catch-up runs per open conversation only.
- **Newest window.** Catch-up from version 0 returns the newest page plus all active items and their parents.
- **Pages** hold every item between their lower bound and the requested order, with no holes, plus parents, and say whether older items exist.
- **Size.** Every message fits 65519 bytes after worst-case escaping. Batches span several messages. An oversized item goes as a first part plus appends. Every request ends in a final reply or an explicit failure. Page cost is bounded by page size, not conversation length, so a 36000-entry channel opens like a short one.

### Sessions, agents, messages, read marks

- **Sessions.** Every item and live-state message carries its session, and the conversation summary names the current one. Before writing a divider the daemon closes what died with the session: running tool calls become interrupted, running agents become ended with session with the cause, and an open turn gets a turn end. The divider and the switch of where later events are filed happen in one step, as #2135 taught. Divider reasons are reset, clear, agent switch naming both agents, recovered, workspace change, sleep and daemon restart. A Claude crash mid-turn that keeps its session gets an interrupted turn end. At a new session the daemon sends each session-scoped reading as empty, then fresh values.
- **Agents.** An `agent` item comes from an Agent or Task call, and the agent's work sits under it by the parent call id. Status is running, stopping, finished, failed, stopped, ended with session with its cause, or gone. A background agent's instant launch result is a field, never its status. **Sub-agent events never open or close a main turn.** Claude's own task reports decide background or foreground. The task-to-call link is stored on the item. Codex has no sub-agents, so its conversations get no agent items.
- **Own and queued messages.** The daemon creates one `user_message` item per accepted send, visible on every device. It is queued and unordered during a turn, becomes delivered with an order at delivery, and becomes hidden and dropped on a drop. The item carries the text Claude reads without host paths, attachment ids and the reported send time.
- **Read marks.** The read mark stays one number per conversation (#2749), in the same number space as versions. The daemon tracks the last shown version, raised only by adding a shown item or appending text to one. A conversation is unread when that number is above the read mark.

### Compatibility

Every admitted app session negotiates `thread` through the authoritative handshake intersection described in ADR 037. Authentication/static-key privacy rejection takes precedence, then existing client-version checks, then omitted thread (`client.update_required`/4412) or advertised-but-ungrantable thread (`protocol.unsupported`/4421), all before binding, redemption, workspace disclosure or opening. See the [handshake contract](../../protocol-mobile.md#handshake).

The original unchanged-stream path for apps without thread was a migration measure: old mobile builds treated unknown history entries as read-mark barriers. That daemon path is now removed. Replaced unsolicited content stays suppressed, event replay/resync and raw history recovery are retired, and durable source history still feeds the fold. Apps explicitly catch up each open conversation by epoch/version and use older pages; only a separate successful final marker certifies a query.

Complete-thread queries, pages and eligible updates require no `interactive` grant. Supplied live state, prompts and controls retain interactive gates; expansion requires interactive and expansion readiness, and compact retains its expansion/interactive/readiness prerequisites and projection-aware joins. Missing optional grants never select legacy delivery. Pushes, correlated replies and continuations preserve supplied session attribution and clear flags; supplied live-state clears also preserve source conversation identity. Summary projection uses authoritative `last_shown_version`, including present zero and omission when unavailable, without discarding unrelated fields.

New meaning gets a new kind or field, never a new meaning for an old field (#2576). A field is present or absent, never empty with another meaning. Apps keep unknown kinds by id and draw their summary if shown. A folding rule change bumps the epoch.

## The six approved decisions

1. **Storage.** Items are folded from the existing history log. There is no separate item log. The log stays the one durable record and gains entry types for the facts it lacks. Folded items live in a store beside the log as a cache, and losing it costs a refold, never correctness. A separate durable item log would avoid refolds, but it doubles storage and can drift from the log. The proposed [ADR 045](045-thread-index-over-history.md) refines this cache into a small index over history with resumable checkpoints.
2. **Queued messages after a restart.** The queue stays in memory. On start the daemon marks every waiting message that was lost as not delivered, visibly in the thread. A durable queue waits until losing messages is observed in practice.
3. **Agents with no report.** An agent that drops off Claude's task list without a finish report gets status gone, meaning the outcome is unknown. This reverses the rule that absence from the list never means an end. Claude does not always refresh its list (#2525), so an agent with neither a report nor a refresh still stays running until its session ends.
4. **Old event stream.** The original migration allowed apps to keep their old path for one release after the daemon shipped `thread`, then ask the operator to update a daemon without it. The daemon originally retained the old stream pending cleanup. That daemon transition is complete (#3718/#3719/#3740/#3741): thread is required for admission independently of configured app-version minimums, and non-thread delivery and recovery branches are removed. This record does not assert that either app has deleted its older-daemon code.
5. **Dividers.** Dividers for idle sleep and daemon restart are written with `shown` false. The deliberate operator reset divider is shown. Clear, agent switch, recovery and workspace change are shown too.
6. **Prompts.** An open permission or question prompt stays live state, re-sent on reconnect, and is never a saved item. Once answered, the thread keeps a saved record of the answer, folded from a history entry like everything else.

## Consequences

- One fold serves both apps, so a folding bug hits both at once and is fixed once. Three codebases change. The original two-path migration explains the staged rollout; the daemon now has one admitted content route.
- A thread-only client can read complete content without receiving interactive supplied state or controls. Admission, publication recipient selection and final sealing must use the same complete-content eligibility; coupling any one to interactive silently excludes otherwise valid clients. There is no non-thread fallback or metadata-stripping projection.
- The daemon gains a store, a fold and memory per conversation, which must be dropped on delete and on idle, as #1502 taught. Refolds need a forward reader over the log, and the first open after a rule change waits for one.
- Every new message type needs a measured size budget and a test, because a message over the cap vanishes whole (#2428). Catch-up adds connect traffic through a relay whose 16-frame outbox has overflowed before (pyrycode-mobile#1051).
- A failed history write still loses that change. A machine crash can lose the log's end, and the new epoch makes apps reload.
- Users see hidden sleep dividers, gone agents and visible lost messages.
- **Not fixed by this design:** scroll jumps, open speed, status ranking, Codex agent support, and a session change that cannot name its conversation in time.
- **Minor follow-ups:** there is no total size limit yet on what a phone stores for one conversation, and the slower, costlier first reply after switching between Claude and Codex is left for the app to show.

## Migration order

The original sequence put mobile first. Steps 1–5 record the staged design, not evidence that every app migration is complete. Daemon cleanup is implemented as described in step 6.

1. **Daemon, invisible.** Session tags on log entries and live events. The missing facts in the log, hidden from old apps. Sub-agent events stop opening turns, which fixes pyrycode-mobile#1951 for today's apps too.
2. **Daemon, shadow.** The fold and store run on live traffic and old logs without sending anything. Invariants are checked against what the apps draw for real conversations, using recorded real output.
3. **Daemon, protocol.** The capability, the update messages, catch-up, pages, the last shown version and session tags on live state.
4. **Mobile.** A new thread layer behind the capability. Dogfood it, then delete the old path after one release.
5. **Desktop.** The same.
6. **Daemon cleanup — complete.** Replaced content publication (#3718), event replay/ring storage (#3719), and raw history pages (#3715) are retired. #3740 requires negotiated thread after authentication and version policy, rather than relying on a raised numeric app-version minimum; #3741 removes the remaining non-thread projection, forwarding and connect-recovery branches. Supplied current-state recovery remains interactive-gated, complete content remains independent of interactive, and the source log stays. See [current delivery](../../protocol-mobile.md#daemon-thread-updates-v2-supplied-delivery-contract).

Quick app fixes for pyrycode-mobile#1947, pyrycode-mobile#1948 and pyrycode-mobile#1949 go ahead meanwhile. The agent-switch handoff note (#2673) should read items, so there is one definition of an exchange.

## Related

- [ADR 037](037-capability-strings-not-version-numbers.md): how the `thread` capability is detected.
- [history-package.md](../features/history-package.md): the log items are folded from, its producers and the unread watermark from #2954.
- [msgqueue-package.md](../features/msgqueue-package.md): the in-memory queue behind decision 2.
- [eventring-package.md](../features/eventring-package.md): the replay buffer catch-up replaces.
