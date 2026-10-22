# `internal/thread` — source entries and accepted sends

[Main-thread folding overview](thread-package-main-thread-folding.md).

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
references described below for all item kinds. Both rendering paths return
detached items with copied content, so snapshot mutation cannot change fold state.

Each item also has recorded `Session`/`Agent`, `NoChild`, optional `Turn`/`Parent`,
`Status`, `Active`, `Shown`, a plain one-line `Summary` and kind-specific
`Content`. Standalone items leave turn and parent unset and are inactive.
Accepted sends also leave turn and parent unset, but remain active while queued
or written and become inactive at a valid linked delivery message or terminal
outcome.
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
under [main work](thread-package-main-thread-folding-lifecycle.md#main-work-source-entries); accepted sends use the
[send-merge recipes](#accepted-sends-and-durable-outcomes) below. Agents and
shell-lifecycle calls use [lifecycle recipes](thread-package-agents-and-background-work.md#agent-lifecycle).
Pending outcomes and inherited `parent_ending` content use
[history and ancestor references](thread-package-main-thread-folding-lifecycle.md#scoped-joins-pending-outcomes-and-closure). See
[snapshot rendering and retry](thread-package-background-store-replay.md#snapshots-and-retry)
for source resolution and separate Store publication budgets.

| Raw source type | Item kind | Rendered content and status |
| --- | --- | --- |
| `message` with `role: user` | `user_message` | Text, attachment IDs and recorded client/device/send metadata; `Status: delivered`. A valid acceptance reference updates its permanent send item instead of exposing another row. |
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
[Cache persistence and epochs](thread-package-cache-and-epochs.md#cache-and-epochs) belong to the
background store. [Daemon shadow wiring](thread-package-shadow-lifecycle-and-evidence-publication.md#background-discovery-and-publication)
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

New operator delivery messages carry a durable `accepted_entry_id` (#3725).
After ordinary user-message validation and main-text closure, `sendMessage`
validates this reference and merges delivery into its queued or written
acceptance before the message can appear as a separate public row. The
acceptance keeps its ID/kind and visibility, takes the message's order and
receiving provenance, and becomes delivered/inactive with the message ID as
revision. The committed message alone proves this state; folding never waits
for `send_delivered` or fabricates its payload. The first matching final outcome
can then enrich that same item and advance its revision once.

`deliveryReference` requires a nonzero JSON integer strictly earlier than the
message; all case-insensitive field variants must agree. It must identify a
valid acceptance in this conversation still queued or written. Malformed,
forward, wrong-kind, foreign or already-claimed references cannot change
ownership. An invalid explicit reference leaves the otherwise valid
message standalone and cannot be rescued by a later legacy outcome; an absent
reference retains the older join path below. App IDs, text, timestamps and
queue-handle reuse provide no fallback identity.

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
cannot claim another message. A message that already established delivery only
accepts its matching `send_delivered`; a later drop/loss cannot replace it.
Text, app IDs, daemon-local queue IDs and timestamps never establish the join.
Equal app IDs across devices still create distinct acceptance items.

Delivery keeps the acceptance ID/kind. At render time, `renderShared` applies
`deliveredSendContent` to the acceptance and delivery entries, starting content
from the linked message's safe delivered text, attachment IDs and client metadata,
then enriches it with a committed matching outcome when present. This preserves
legacy JSON encoding and unknown fields. Recorded acceptance `device_id`, `message_id`,
`accepted_at` and `client_sent_at` override
the corresponding delivered fields only when present. `deliveredSendContent`
decodes those sender fields in JSON order with case-insensitive matching before
merging. Selecting them by map iteration would make duplicate case variants
nondeterministic; the last matching recorded value wins. It removes delivered
case variants when replacing a field, keeping one canonical sender key.

Attachment names have acceptance-owned presence as well as value (#3694).
`deliveredSendContent` removes every delivered `attachments` case variant, then
projects only usable recorded acceptance facts: string `attachment_id` and
`filename`, with the ID in the acceptance's `attachment_ids` and a nonempty
basename excluding `.`, `..`, slash, backslash and drive prefixes. Entries are
decoded independently so one malformed fact cannot discard usable neighbors or
the accepted send. Only the two display fields survive; exact strings and
relative array order are retained. Missing, null, non-array or wholly unusable
metadata omits `attachments`; delivery supplies no fallback, and IDs or host
paths never supply invented names. Both inline delivery and
`renderSharedLookup` use this helper, preserving the same contract after source
cache eviction and reopening. See the
[full thread item contract](../../protocol-mobile.md#full-thread-item).

History owns these display names independently of current attachment storage.
`TestDeliveredAttachmentWire` renames stored files before reopening, then checks
the original acceptance facts through full/compact catch-up and pages while
authenticated retrieval returns identical bytes with the current storage names.
A byte-only retrieval assertion would miss lost or forged thread filenames.
`TestSendAttachmentFacts` checks independent filtering, conflicting delivery
names, recorded order and inline/source-backed agreement through repeat outcomes.

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

Before a reference-backed delivery changes either identity, `deliverSend` renders
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
[snapshots and retry](thread-package-background-store-replay.md#snapshots-and-retry).

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

Linking delivery removes any intermediate `send_written` outcome in both inline
and source-backed folds. A message-only delivery prefix has no `outcome` field;
only a committed matching `send_delivered` supplies the final object. Clone
recipe fields before removing the written reference so retained observations
stay immutable. Comparing only queued prefixes or final successful delivery
would miss a written-prefix mismatch when the final append fails.
`TestSendWrittenDeliveryPrefixes` compares incremental observations and full
replay at every prefix; `TestAcceptedDeliveryAppendFailures/written_outcome`
also checks real append failure, source-backed reopening and catch-up.
`foldingRules` invalidates derived caches from older interpretations.

After delivery, `Items` omits the claimed message's public row; unmatched
messages retain their delivered identity and order. **Keep internal item indexes
stable:** removing a reconciled message from storage would shift indexes held
by assistant/tool state and corrupt later updates. The claim suppresses only
the public row. For newly linked messages this happens before publication, so
every committed prefix retains one permanent acceptance item, without a
join-induced baseline or retirement. Older messages lacking the explicit
reference can still expose both rows in a chunk ending before the outcome;
later suppression of that public message retains genuine-withdrawal safety.
See the [live publication regression](thread-package-shadow-lifecycle-and-evidence-publication.md#background-discovery-and-publication).
Acceptance alone does not split assistant text; the delivered message still
closes main text runs when ingested.

Without a valid delivery message or terminal, acceptance stays queued or written.
**Loss requires an explicit recorded `send_lost`; absence never infers it.**
Recorded loss identifies missing
durable resolution after restart, not proof that the agent never received the
bytes. A successful write without a committed linked delivery or final outcome
can leave unresolved history; a committed linked message remains delivered even
if the final outcome is missing. See the
[producer durability boundary](history-package-producers.md#accepted-sends-and-linked-outcomes-2972)
and [ADR 042](../decisions/042-daemon-built-thread.md#the-six-approved-decisions).
