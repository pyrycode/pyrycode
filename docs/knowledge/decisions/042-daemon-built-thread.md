# 042. The daemon builds the conversation thread, and the apps only draw it

## Status

Accepted 2026-10-08 by the owner. Not yet implemented. The design was worked out outside this repository, together with a catalogue of every edge case found in past bugs. This record is the complete in-repo statement of it, so implementation tickets cite this file rather than those notes.

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

**Live state is never an item.** Permission prompts and question batches, turn phase, stalls, API retries, compaction in progress, thinking progress, tool and task progress, reset status, rate limits, context usage, the announced model, session facts, MCP status, slash commands, the model list, reply suggestions and session errors stay live state. They are never stored as items, replayed or included in catch-up. On connect the daemon re-sends current values. Each live-state message gains a session tag, and retry and compaction status join the connect-time set.

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

A new capability string, `thread`, is negotiated in the handshake as ADR 037 describes. A connection with it gets items and live state, and stops getting the content events items replace: text deltas, tool events, turn ends, session changes, user message pushes, task events, queue state, banners, compaction marks, refusals and unrecognized output. A connection without it gets today's stream unchanged, and the new log entry types are filtered out of its history pages and replay, since old mobile builds treat unknown entries as read-mark barriers. New meaning gets a new kind or field, never a new meaning for an old field (#2576). A field is present or absent, never empty with another meaning. Apps keep unknown kinds by id and draw their summary if shown. A folding rule change bumps the epoch.

## The six approved decisions

1. **Storage.** Items are folded from the existing history log. There is no separate item log. The log stays the one durable record and gains entry types for the facts it lacks. Folded items live in a store beside the log as a cache, and losing it costs a refold, never correctness. A separate durable item log would avoid refolds, but it doubles storage and can drift from the log.
2. **Queued messages after a restart.** The queue stays in memory. On start the daemon marks every waiting message that was lost as not delivered, visibly in the thread. A durable queue waits until losing messages is observed in practice.
3. **Agents with no report.** An agent that drops off Claude's task list without a finish report gets status gone, meaning the outcome is unknown. This reverses the rule that absence from the list never means an end. Claude does not always refresh its list (#2525), so an agent with neither a report nor a refresh still stays running until its session ends.
4. **Old event stream.** Apps keep their old path for one release after the daemon release that ships `thread`. After that an app connected to a daemon without the capability asks the operator to update it. The daemon keeps the old stream for apps without the capability until cleanup raises the minimum app version.
5. **Dividers.** Dividers for idle sleep and daemon restart are written with `shown` false. The deliberate operator reset divider is shown. Clear, agent switch, recovery and workspace change are shown too.
6. **Prompts.** An open permission or question prompt stays live state, re-sent on reconnect, and is never a saved item. Once answered, the thread keeps a saved record of the answer, folded from a history entry like everything else.

## Consequences

- One fold serves both apps, so a folding bug hits both at once and is fixed once. Three codebases change, and each app runs two paths for a while.
- The daemon gains a store, a fold and memory per conversation, which must be dropped on delete and on idle, as #1502 taught. Refolds need a forward reader over the log, and the first open after a rule change waits for one.
- Every new message type needs a measured size budget and a test, because a message over the cap vanishes whole (#2428). Catch-up adds connect traffic through a relay whose 16-frame outbox has overflowed before (pyrycode-mobile#1051).
- A failed history write still loses that change. A machine crash can lose the log's end, and the new epoch makes apps reload.
- Users see hidden sleep dividers, gone agents and visible lost messages.
- **Not fixed by this design:** scroll jumps, open speed, status ranking, Codex agent support, and a session change that cannot name its conversation in time.
- **Minor follow-ups:** there is no total size limit yet on what a phone stores for one conversation, and the slower, costlier first reply after switching between Claude and Codex is left for the app to show.

## Migration order

Mobile switches first.

1. **Daemon, invisible.** Session tags on log entries and live events. The missing facts in the log, hidden from old apps. Sub-agent events stop opening turns, which fixes pyrycode-mobile#1951 for today's apps too.
2. **Daemon, shadow.** The fold and store run on live traffic and old logs without sending anything. Invariants are checked against what the apps draw for real conversations, using recorded real output.
3. **Daemon, protocol.** The capability, the update messages, catch-up, pages, the last shown version and session tags on live state.
4. **Mobile.** A new thread layer behind the capability. Dogfood it, then delete the old path after one release.
5. **Desktop.** The same.
6. **Daemon cleanup.** Raise the minimum app version. Stop the replaced events, the content replay buffer, resync and raw history pages. The log stays.

Quick app fixes for pyrycode-mobile#1947, pyrycode-mobile#1948 and pyrycode-mobile#1949 go ahead meanwhile. The agent-switch handoff note (#2673) should read items, so there is one definition of an exchange.

## Related

- [ADR 037](037-capability-strings-not-version-numbers.md): how the `thread` capability is detected.
- [history-package.md](../features/history-package.md): the log items are folded from, its producers and the unread watermark from #2954.
- [msgqueue-package.md](../features/msgqueue-package.md): the in-memory queue behind decision 2.
- [eventring-package.md](../features/eventring-package.md): the replay buffer catch-up replaces.
