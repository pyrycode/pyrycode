# #2499 — carry a posted channel message into claude's next turn

## Files read

- `cmd/pyry/main.go` → `newInboundDeliver`, `markApprovalHolds`, `runSupervisor`'s
  `msgqueue.New` literal — the delivery seam, the decorator pattern this slice copies,
  and the one wiring site.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory` — the hazard
  note the ticket points at: why a durable producer hangs off `OnDelivered` and not
  off `msgqueue.DeliverFunc`. It is also the seam's incumbent consumer.
- `cmd/pyry/channel.go` → `channelPoster`, `channelCreator` — where a post is
  recorded, and where the channel leg's registry `Save` already happens.
- `internal/msgqueue/queue.go` → `DeliveredFunc`, `notifyDelivered`, `drain`, `queued`
  — the `text`/`delivery` split that makes AC 5 structural, and the serial-per-
  conversation drain the compose/clear window depends on.
- `internal/relay/handlers/send_message.go` → `composeAttachmentPrompt` — #2038's
  daemon-composed-input precedent, including "an empty list is the identity".
- `internal/conversations/registry.go` → `SetLastContextUsage`, `SetSystemPrompt`,
  `Save`, `WorkspaceLabel` — the shipped shape for a daemon-written durable field,
  the validate-at-the-door precedent, and the copy-don't-share-the-collection rule.
- `internal/conversations/conversation.go` → `SystemPrompt`, `LastContextUsage` —
  the `omitempty` "absent decodes as nothing, no migration step" contract.
- `cmd/pyry/relay_context_usage.go` → `contextUsageRecorder`, its `record` — the
  registry-writing recorder shape (nil-inert, best-effort Save, Warn-only logging).
- `internal/control/protocol.go` → `MaxChannelPostBytes` — the per-post admission
  size the accumulation bound is sized against.
- `internal/e2e/channel_post_test.go`, `internal/e2e/relay_v2_stream_send_test.go`,
  `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay`,
  `seedBoundConversation`, `childStdinLog` — the family's fake-daemon coverage and
  the stdin-tee proof the ticket names.
- `internal/debugbundle/bundle.go` → `writeRecordingMember` — checked for the
  security pass: the bundle carries recordings, a manifest and logs, never
  `conversations.json`.

## Context

#2497 gave a post a durable record and #2498 pushed it live to connected clients.
Neither is claude's context. A reply typed after a posted question reaches claude as a
user turn with nothing before it, so the family's first consumer — post a question in
the morning, answer it that evening — produces an answer claude cannot place.

This slice carries text posted into a channel into the next user turn the daemon
delivers for that conversation, so claude sees the question and the reply in the order
the two happened.

**Size.** The refiner declared this ticket over the 800-line ceiling by design
(~850 estimated, floor-beats-ceiling: the durable pending record has exactly one
consumer). My own count agrees and lands a little higher, ~900 including this plan.
The overage stands for the refiner's stated reason. It is not widened by the design
below: the one place it could have grown — a signature change to `newInboundDeliver`,
which has 17 call sites — is avoided by decorating instead (see Design).

No ADR is owed. This adds a field and two seams inside boundaries #2038 and #2115
already drew; it redraws none.

## Design

### Where composition happens, and why not at enqueue

**At delivery.** The ticket names the two candidates and the difference: composing at
enqueue misses a post that lands while a reply is already queued, and the queue's head
can sit through a whole claude turn before it is written. Composing at delivery also
keeps the composed value off `msgqueue.QueuedMessage`, which is what makes AC 5
structural rather than a filter — the same barrier `newOperatorMessageHistory`'s doc
block already argues for.

### Not a signature change — a decorator

`newInboundDeliver` has 17 call sites (`codegraph_callers`), 16 of them tests. The
carry therefore wraps it as a `msgqueue.DeliverFunc` decorator, which is the shape
`markApprovalHolds` already has in the same file. The wiring becomes:

```go
Deliver: postCarry.carryPending(approvalParked.markApprovalHolds(newInboundDeliver(…)))
```

Carry outermost, so the payload is composed once at the boundary with the queue and
`markApprovalHolds` stays adjacent to the seam that produces the hold error it marks.
A nil `*channelCarry` returns the wrapped func unchanged, matching
`approvalParkedReport`'s nil posture.

The decorator composes **before** the seam's idle-gate wait, because that wait lives
inside `newInboundDeliver`. A post landing during the wait is therefore not carried by
that turn — it stays pending and is carried by the next one, or by the next retry of
the same head. Never lost, never doubled.

### Durable pending record

`internal/conversations`:

- `Conversation.PendingChannelPosts []string`, tag `pending_channel_posts,omitempty`.
  Absent decodes as nothing pending, so no migration step is owed —
  `SystemPrompt`'s and `LastContextUsage`'s stated contract.
- `MaxPendingChannelPosts` and `MaxPendingChannelPostsBytes` — the growth bound the
  ticket asks this plan to pick. See "Growth bound" below.
- `Registry.AppendPendingChannelPost(id, text) bool` — appends under `r.mu`; false
  when the row is absent **or** the bound is full. The two are not distinguished
  because the caller's response is identical, and a distinction with no consumer is
  a branch nobody exercises.
- `Registry.PendingChannelPosts(id) []string` — a fresh copy, never the stored slice.
  `WorkspaceLabel`'s argument transfers: handing back a collection a mutator writes
  under `r.mu` is an escape the signature should make impossible.
- `Registry.ClearPendingChannelPosts(id, n) bool` — drops the first n entries by
  reslicing. `n <= 0` mutates nothing; `n >= len` clears the record.

None of the three calls `Save`; persistence is the caller's concern, the convention
every other setter in that file states.

### `cmd/pyry/channel_carry.go` (new)

`channelCarry` in the shape of `contextUsageRecorder`: `reg`, `path`, `logger`, plus a
leaf `mu` guarding `composed map[string]int`.

- `record(id, text)` — the poster's hook. Appends, then `Save`s best-effort. Nil
  receiver or nil registry is inert. The `Save` sits behind the append's bool, so an
  unknown id reaches no disk — `contextUsageRecorder.record`'s structure exactly.
- `carryPending(msgqueue.DeliverFunc) msgqueue.DeliverFunc` — the decorator. Reads
  pending, composes, records the composed count under `mu`, delegates.
- `clearDelivered(convID string, msg msgqueue.QueuedMessage)` — a
  `msgqueue.DeliveredFunc`. Takes and deletes the recorded count, clears that many,
  `Save`s. `msg` is read for nothing; the parameter exists because the seam's shape
  does, and reading `msg.Text` here would be a second record of a turn #2115 already
  logs.
- `composeChannelCarry(posts []string, payload []byte) []byte` — pure, free, the unit
  under test. **An empty list is the identity**, as an early return rather than a
  formatting rule that happens to agree — `composeAttachmentPrompt`'s precedent, and
  the whole of AC 3's second half.
- `channelCarryHeader` — one fixed daemon-authored line introducing the block. It
  names the content as posted messages so claude can attribute them; see the
  security review's trust-boundary finding.

Rendered shape: header line, the posted texts in post order separated by a blank
line, a blank line, then the payload verbatim. The posts go **first** because AC 1
asks for the order the two things happened in, which is the opposite of #2038's block
and the one place this design deliberately departs from it.

### Chaining `OnDelivered`

`msgqueue.Config.OnDelivered` is single-valued and `newOperatorMessageHistory` holds
it. A `deliveredFuncs(...msgqueue.DeliveredFunc) msgqueue.DeliveredFunc` combinator in
the same new file skips nil members and answers nil when nothing is left, so the seam
stays disabled when it should be. History first, then the clear: #2115's doc states
that its record is written as close to the commit as possible, and that ordering is an
existing contract this slice must not quietly reorder.

### `channelPoster`

Gains a `carry func(conversations.ConversationID, string)` parameter, called after the
durable append succeeds and before `announce`. Three call sites (`codegraph_impact`):
the composition root and two test factories. A nil hook is a no-op, `announce`'s
posture. The carry cannot fail the post: #2497/#2498 own what a post delivers, and a
registry save that failed leaves the pending row in memory for the next save.

### Growth bound

`control.MaxChannelPostBytes` admits 64 KiB per post and nothing bounds accumulation.
Two constants, because one does not do the job: a byte cap alone admits 65536
one-byte posts, each of which costs a separator in the rendered block.

- `MaxPendingChannelPostsBytes = 64 << 10` — the summed length of the stored texts.
  Sized at one post's admitted size, so what accumulates can never make the eventual
  stdin write larger than a single post already can. The number is restated rather
  than imported: `internal/conversations` is a leaf and must not import
  `internal/control`, which is `MaxSystemPromptBytes`'s situation exactly.
- `MaxPendingChannelPosts = 64` — entries. Far above any plausible unanswered
  channel; a daily cron would need two months to reach it.

**A full record refuses the NEW post's carry rather than evicting the oldest.** This
is a correctness requirement, not a preference — see the concurrency finding in the
security review. The post itself still records and still pushes; only the carry is
declined, with one content-free log line.

## Concurrency model

No goroutines are added. Three actors touch the pending record:

- The **control-plane goroutine** servicing `channel.post` appends (one lock
  acquisition inside `AppendPendingChannelPost`).
- The **drain goroutine** for that conversation reads at compose time and clears at
  `OnDelivered`. msgqueue runs exactly one drain per conversation and one delivery at
  a time within it, so compose and clear are serialized against each other.
- **Any registry writer** may `Save` concurrently; `saveMu` already serializes
  snapshot→rename so a later snapshot always renames later.

**The compose/clear window is the load-bearing invariant.** Compose and clear are two
separate lock acquisitions, and a post can land between them. Clearing the *first n*
is correct because the only mutation to the head of the slice is a clear, and only the
drain performs one. That is exactly why the bound refuses at the tail instead of
evicting at the head: head eviction would let a post landing mid-delivery shift the
entries a pending clear is counting, and the clear would then drop text that was never
carried.

**Lock order.** `channelCarry.mu` is a leaf guarding one map. It is never held across
a registry call, a `Save`, or the wrapped delivery — take it, mutate the map, release.
`Registry.saveMu → Registry.mu` is unchanged and untouched.

**Retry.** A failed delivery is retried at the same head. The decorator recomposes on
every attempt, so a post that arrived during the failed attempt is picked up by the
retry, and the count recorded is always the one the *succeeding* attempt composed.

## Error handling

| Failure | Behaviour |
|---|---|
| Registry row absent on append | Nothing recorded, nothing saved, one Warn. The post still succeeds. |
| Bound full | Carry declined, one Warn, post unaffected. |
| `Save` fails after append | Warn. The in-memory row keeps the pending text; the next registry save persists it. `contextUsageRecorder.record`'s posture. |
| `Save` fails after clear | Warn. In-memory state is correct; a restart before the next save re-carries the post once. |
| Delivery fails / head dropped | Nothing cleared (the clear only runs on a confirmed delivery), so the text is carried by the retry. |
| Daemon killed between the confirmed write and the clear's save | The post is carried once more on restart. Accepted, and the same class as msgqueue's own in-memory restart boundary. |

Nothing in this slice returns a new error to a caller, and no path can turn a
successful post or a successful delivery into a failure.

## Testing strategy

- `internal/conversations/registry_test.go` — table-driven over the three methods:
  append order; the returned slice is a copy (mutating it does not disturb the
  registry); clear-first-n including `n <= 0` and `n >= len`; both bounds refusing at
  the tail; a miss mutating nothing; `omitempty` round-tripping absent → nil.
- `cmd/pyry/channel_carry_test.go` (new) — `composeChannelCarry`: the empty-list
  identity byte-for-byte, one post, two posts in order, an empty payload. Then the
  carry through its seams: a decorated `DeliverFunc` sees the composed bytes; a
  confirmed delivery clears exactly what was composed; a post landing between compose
  and clear survives; a failed-then-retried delivery carries once, not twice; a
  second reply carries only itself; `deliveredFuncs` fans out in order and answers
  nil for an all-nil set.
- `cmd/pyry/channel_post_test.go` — the poster hands the carry hook the same text it
  recorded, and a refused post calls it not at all.
- `internal/e2e/channel_post_test.go` — the ticket's named proof, against the fake:
  a promoted named channel, a real `pyry channel post` over the control socket, then
  a `send_message` from a paired fakephone, reading the child's stdin tee
  (`PYRY_FAKE_CLAUDE_STDIN_LOG`). Asserts the post text and the reply text land in
  the *same* turn envelope with the post first, and that a second reply carries only
  itself. That covers AC 1, AC 2's first half and AC 3's second half end to end.

AC 5's "clients see no change" needs no new assertion: the composed value exists only
as the `[]byte` handed to `WriteUserTurn`, and the wire/log producer hangs off
`OnDelivered`, whose `QueuedMessage` has no delivery field. The existing
`newOperatorMessageHistory` coverage pins the entry that reaches the log.

`conversationReset.wrapUp` is likewise structural: it calls `WriteUserTurn` directly
rather than through msgqueue, so it never meets the decorator and cannot carry.

## Open questions

1. Whether the bound should refuse the newest post or evict the oldest. **Resolved in
   this plan, ahead of implementation:** refuse the newest, because head eviction
   breaks the compose/clear window (security review, Concurrency).
2. Whether `clearDelivered` should read `msg` at all. **Resolved:** no. The parameter
   is the seam's shape; reading it would duplicate #2115's record.

## Documentation handoff

Owned by the documentation stage, not by this slice's code. Pending:

- `docs/knowledge/features/control-plane.md` — where pending posted text is held,
  when it is carried, when it is cleared, and that the composed text reaches claude
  only. That file is where both siblings' notes went. If the reviewer judges the
  carry to sit on the delivery seam rather than beside the channel-post verb,
  `docs/knowledge/features/msgqueue-package.md` is the named alternative.

## Security review

**Verdict:** PASS (after one MUST FIX, folded into the design above).

**Findings:**

- [Trust boundaries] **Finding, addressed by design.** This slice opens a genuinely
  new boundary: text a `channel.post` caller authored now enters claude's *input*,
  ahead of the operator's own words. Anything that can open the control socket can
  put text into claude's next turn — a prompt-injection surface that did not exist
  when a post only reached the durable log and the wire. It is **not** an escalation:
  the control socket is host-local and confined to the owning user, who can already
  run `claude` directly, and `channel new` already spawns a session at a chosen cwd
  through the same door. The design response is provenance, not sanitising:
  `channelCarryHeader` is a fixed daemon-authored line that names the block as posted
  messages, so claude reads it as quoted material rather than as the operator
  speaking. The text itself is carried **verbatim, never quoted or escaped** —
  `composeAttachmentPrompt`'s recorded reasoning, which holds here for the same
  reason (quoting mangles text containing the quote character and buys nothing
  against a caller who controls all the bytes). A post that contains the header
  string itself can spoof the boundary inside the prompt, but that changes nothing
  about what is authorised: both halves are one prompt from one principal, and claude
  gains no capability from either ordering.
- [Tokens, secrets, credentials] No findings — this slice mints, stores and compares
  nothing secret.
- [File operations] No findings, but one deliberate widening to name:
  `conversations.json` now holds conversation **content** (posted message text),
  which it did not before. Its sensitivity class is unchanged — `SystemPrompt` is
  already operator-authored content in that file — and `Registry.Save`'s existing
  temp-at-0600 → fsync → rename is what writes it, so mode and atomicity need no new
  code. Checked the one path that could carry the file off-host: `internal/debugbundle`
  writes recordings, a manifest and logs, and never `conversations.json`.
  No path in this slice concatenates caller input into a filesystem path; the log and
  the registry both key on the daemon-minted conversation id, which is
  `channelPoster`'s existing property.
- [Subprocess / external command execution] No findings — the composed text reaches
  claude through **stdin** (`WriteUserTurn` → the stream-json envelope), never through
  argv and never through a shell. No environment variable carries it.
- [Cryptographic primitives] Not applicable — no randomness, no comparison against a
  secret. The post's own turn-id mint (`newChannelPostTurnID`, `crypto/rand` via
  `conversations.NewID`) is untouched.
- [Network & I/O] **Finding, addressed by design.** Unbounded accumulation: 64 KiB
  admitted per post × unbounded posts before a reply grows both `conversations.json`
  and the eventual stdin write without limit — a local resource-exhaustion vector
  reachable by any caller of the verb. Bounded by `MaxPendingChannelPostsBytes` and
  `MaxPendingChannelPosts`, with the count cap present because the byte cap alone
  admits 65536 one-byte entries. The composed value never reaches a socket: the wire
  producer hangs off `OnDelivered`, whose `QueuedMessage` declares no delivery field,
  so `docs/protocol-mobile.md` § Error codes' ban on putting host layout on the wire
  is inherited structurally — which matters here because the payload this slice
  prepends to may itself name an attachment's on-host path (#2038).
- [Error messages, logs, telemetry] **Finding, addressed by design.** The carry's log
  lines carry the event name, the conversation id and a registry error only — never
  the posted text, never the composed payload, and **never a count**, because a
  dropped-entry count is a partial proxy for a post's length and `channelPoster`
  already refuses to log its chunk count for exactly that reason. No refusal path
  interpolates the text. No new error string reaches a caller, so nothing new can
  surface on the control socket's stderr or on the wire.
- [Concurrency] **MUST FIX — found and fixed before this plan was committed.** The
  first draft bounded accumulation by evicting the OLDEST pending entries. Compose
  and clear are two separate lock acquisitions with a whole claude turn between them,
  so this sequence loses text: compose reads [A,B] and records n=2; a large post C
  arrives and evicts A and B, leaving [C]; the delivery confirms and the clear drops
  the first 2 — dropping C, which was never carried. The fix is structural rather
  than a lock widened across the delivery: **the bound refuses the newest post's
  carry**, so the only mutation to the head of the slice is a clear, and only the
  serial drain performs one. Entries [0..n) are then provably stable across the
  window. Beyond that: `channelCarry.mu` is a leaf never held across a registry call,
  a `Save` or the wrapped delivery; no goroutine is spawned, so none can leak; and
  every partial state on an interrupted write is recoverable — an interrupted append
  loses one carry, an interrupted clear re-carries one post, and both are named in
  Error handling.
- [Threat model alignment] No findings beyond those above. `docs/protocol-mobile.md`
  § Security model's relevant clause is the host-layout ban, inherited structurally
  as described under Network & I/O. Multi-principal isolation on the control socket
  (who may post into which channel) is **out of scope** and is not this slice's to
  take: the socket is single-user by construction and `channel.post` shipped with
  that posture in #2497. No future ticket is named because none is owed unless the
  control socket's trust model changes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16
