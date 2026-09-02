# #2038 — name a message's stored attachments in claude's prompt

**Ticket:** [#2038](https://github.com/pyrycode/pyrycode/issues/2038) · size `s` · `security-sensitive` · `needs-human:sizing`
**Parent chain:** #1684 → #1745 → #2038 (grandchild — the split-depth gate forbids a further split)

## Files read

| Path | Symbols that matter | Why it matters |
|---|---|---|
| `internal/relay/handlers/send_message.go` | `SendMessage`, `Enqueuer`, `SessionRouter`, `msgSendMessageBacklogFull` | The composition site. Its reject-then-enqueue ordering, its static-message idiom and its "never log `payload.Text`" discipline are the shapes this ticket extends. |
| `internal/protocol/messaging.go` | `SendMessagePayload`, `SendMessagePayload.UnmarshalJSON`, `MaxAttachmentIDsPerMessage` | Declares the field, the 32 bound as **unchecked** and **element-counting**, and normalises the three empty wire forms so AC 2 has one value to test rather than three. |
| `internal/protocol/codes.go` | `CodeAttachmentNotFound`, `CodeProtocolMalformed` | `attachment.not_found` is already widened to span both verbs (#2036) with a static, which-id-silent message; no code is published for an over-bound list. |
| `internal/attachments/storage.go` | `ResolvePath`, `ErrNotFound` | One sentinel for unknown / non-canonical / foreign-conversation alike; carries the confinement **precondition** this call site must discharge, and the "do not log the returned path" obligation. |
| `internal/attachments/filename.go` | `SanitizeFilename` | The only client-authored byte in a resolved path is this function's output, and its allowlist is `[a-zA-Z0-9_.-]` — load-bearing for the prompt-injection finding below. |
| `internal/msgqueue/queue.go` | `Enqueue`, `queued`, `QueuedMessage`, `Queue.drain`, `Snapshot`, `SnapshotAll` | `drain` is the only reader of the stored text and `QueuedMessage` the only projection out; that asymmetry is what the delivery-payload seam exploits. |
| `cmd/pyry/queue_state_v2.go` | `toQueueStatePayload`, `outstandingQueues`, `queueStateEmitterV2.broadcast` | Both AC-4 read-back paths — the enqueue push and the connect-time reconcile — go through one mapping over `QueuedMessage`. |
| `cmd/pyry/relay.go` | `attachmentIntake` construction, `resolveInstanceDirPath`, the `Handlers` map's `TypeSendMessage` entry | Where the instance directory and the follow-active cursor already are, and the one place `SendMessage` is constructed. |
| `cmd/pyry/main.go` | `newInboundDeliver`, `resolveInstanceDirPath` | Confirms the drain-to-`WriteUserTurn` leg is payload-opaque, so **no change is needed here** despite the ticket's estimate listing it. |
| `docs/protocol-mobile.md` § *Naming a message's attachments*, § *Error codes*, § *Security model* | — | Publishes both decisions as this ticket's to make, and threat 1 (prompt injection), which lands on `attachment_ids` because they become prompt content. |
| `docs/knowledge/features/msgqueue-package.md` § *Security*, § *Exported surface* | — | The `text`-is-opaque-transit and `convID`-is-a-map-key-only stance the new method must not weaken; also the #869 precedent for adding behaviour at the single insertion point without touching ~45 call sites. |
| `docs/knowledge/features/attachments-package-sentinels-and-discard-semantics.md` | — | The package's single-sentinel posture behind the comma-ok seam decision. |

## Context

The upload leg is complete (#1743/#1895/#1897) and `attachments.ResolvePath` (#2037) answers "id → on-host path". `send_message` carries `attachment_ids` (#2036). Nothing joins them: a client can upload a file, name it on a message, and claude never hears about it. This slice is that join, and it is the slice that delivers the original user story.

The decision on record since 2026-05-16 is unchanged: the daemon puts the **path** into claude's prompt and claude opens it with its ordinary `Read` tool. No native image content block — once the bytes are on disk, inlining them duplicates them into the JSONL transcript, and the path form keeps replay cheap.

Two decisions both blockers deliberately pushed here land in this slice (AC 6), and both must be published back into `docs/protocol-mobile.md`, where the contract currently records them as unmade.

**No ADR is warranted.** Every decision below is either an enforcement of a contract another ticket already published, or a local seam with one consumer. The documentation phase should fold the delivery-payload seam into `docs/knowledge/features/msgqueue-package.md` § *Exported surface* and the composition into the relay-package overview.

**Size — the ceiling is exceeded deliberately.** Re-counted against this written plan: 3 production source files (`internal/relay/handlers/send_message.go`, `internal/msgqueue/queue.go`, `cmd/pyry/relay.go`) against a ceiling of 5; 1 new exported type (`AttachmentResolver`) against 5; 2 consumer call sites (the `Handlers` map entry and `fakeEnqueuer`) against 10; 6 reject branches against 10. Two lines are exceeded: **6 acceptance criteria against 5**, and **~1100 lines of total written work against 800**. Both were declared by the refiner with the split declined, and both hold up here. The only available seam — `msgqueue` carrying a delivery payload distinct from the queued text — has exactly one consumer, the handler in this same ticket, so the sizing guide's **floor** merges it rather than cutting it; and the split-depth gate forbids a split independently (grandchild of #1684). `needs-human:sizing` is already applied. Per the floor-beats-ceiling rule the overage is stated and the ticket is built.

Note that this plan needs **one fewer production file than the estimate**: `cmd/pyry/main.go` does not change (the drain-to-`WriteUserTurn` leg is payload-opaque) and `internal/protocol/codes.go` does not change (decision D5 mints no code).

## Design

### D1 — the delivery payload seam in `msgqueue`

`msgqueue` gains one method:

```go
func (q *Queue) EnqueueDelivery(convID, text, delivery string) uint64
```

`text` is what a client reads back; `delivery` is what reaches claude's stdin. `queued` gains a `delivery` field, `Queue.drain` writes `[]byte(head.delivery)` instead of `[]byte(head.text)`, and `Enqueue` becomes a one-line `return q.EnqueueDelivery(convID, text, text)` — **its signature is unchanged**, so its one production and 108 test call sites are untouched, the #869 posture of adding behaviour at the single insertion point.

`delivery` is *not* an "empty means use text" sentinel. `Enqueue` passes `text` explicitly, so an empty delivery is a real (if useless) value and no branch anywhere distinguishes the two fields' provenance. This is what keeps the drain a single unconditional expression.

**AC 4 falls out of the type rather than out of a check.** `QueuedMessage` gains no field, so `Snapshot` and `SnapshotAll` are structurally incapable of projecting `delivery`, and `toQueueStatePayload` — the one mapping both the enqueue push and the connect-time reconcile go through — has nothing to leak. A future consumer that wants the host path on the wire has to change the engine's exported type to get it, which is exactly the friction wanted.

Bound, ordering, cap-rejection, notification and drain-spawn behaviour are unchanged: the cap check, the id mint and the append all move into `EnqueueDelivery` verbatim.

### D2 — composition at the handler, not the drain

AC 3's refusal is synchronous and only the handler holds the conn that answers the `send_message` frame. So resolution, refusal and composition all happen in `SendMessage`, and the composed prompt travels to the drain as `EnqueueDelivery`'s third argument. The drain is unchanged and stays ignorant of attachments.

### D3 — the resolver seam is comma-ok, deliberately

```go
// AttachmentResolver resolves one attachment id named by a message to the
// on-host path of its stored file, confined to conversationID.
type AttachmentResolver func(conversationID, attachmentID string) (path string, ok bool)
```

Consumer-defined in `handlers`, mirroring `SessionRouter` and `Enqueuer`, so the package stays free of an `internal/attachments` import. `cmd/pyry/relay.go` adapts `attachments.ResolvePath` over `resolveInstanceDirPath(w.instanceName)` — the same value `attachmentIntake` already gets.

**Comma-ok rather than `(string, error)`, for an AC-5 reason and not an aesthetic one.** `ResolvePath` answers exactly one sentinel, `ErrNotFound`, for every refusal, so an error carries no information the handler could branch on. It carries something the handler must *not* have: the shape-invalid refusal formats the raw client-supplied id into its message, and a raw element is exactly the log-injection value AC 5 forbids. Dropping the error at the adapter makes it impossible for the handler to log it, rather than merely inadvisable. The adapter is the one place the error exists, and it logs nothing.

A `nil` resolver refuses every id (fail-closed): a message naming attachments is answered `attachment.not_found`, and a message naming none is unaffected. Production always wires one; this is the typed-nil-style inertness `questionResolver`'s construction comment argues for, expressed as a plain nil-func check.

### D4 — the order of the checks, and why it is load-bearing

`SendMessage` gains two gates, and their positions are the security argument:

1. **decode** → `protocol.malformed` *(unchanged)*
2. **bound check** on `len(p.AttachmentIDs)` → `protocol.malformed` *(new, D5)*
3. **`router.Route(p.ConversationID)`** → `conversation.not_found` / `server.binary_offline` *(unchanged)*
4. **resolve each distinct id** → `attachment.not_found` *(new)*
5. **compose** *(new, D7)*
6. **`queue.EnqueueDelivery`** → `server.binary_busy` on a 0 *(cap arm unchanged)*

**Step 3 strictly precedes step 4, and that is what discharges `ResolvePath`'s precondition.** Its doc block requires `conversationID` to be the conversation the authenticated session is already on, never one a client asserted — *"a caller that gets it wrong defeats every check below."* `Route` validates the client-supplied id against the server-stored registry binding and stamps the follow-active cursor (#687), so past step 3 it **is** the conversation this session is on. Confinement then needs no check of its own: an attachment is filed under whatever conversation the cursor named when its last chunk landed (`attachments.NewIntake`'s resolver in `cmd/pyry/relay.go`), and an id filed under a different one simply has no directory beneath this one. AC 3's third arm is a test, not new logic.

The one variable `p.ConversationID` is threaded unchanged into `Route`, into every resolve and into `EnqueueDelivery` — the same single-variable discipline `queueStateEmitterV2.broadcast` documents for `convID`, so the validated conversation and the resolved-against conversation cannot desync.

**Step 2 precedes step 3** because the bound is a pure frame-shape rule needing no state, and `Route` has a side effect: it stamps the cursor. A frame that violates a published composition contract should not move daemon state. It also discloses less — an over-bound frame naming an unknown conversation learns nothing about that conversation's existence.

### D5 — an over-bound list is **refused**, with `protocol.malformed`, not retryable

A list of more than `protocol.MaxAttachmentIDsPerMessage` elements is rejected before any other work; nothing is enqueued, `Route` is not called, no id is resolved.

- **Refuse, not truncate.** Truncation silently drops files a person attached and gives the client no signal that it happened; the message ships looking complete. Reject-never-drop is this handler's existing posture for a full backlog and the whole family's posture for a bound.
- **`protocol.malformed`, not a new code.** 32 is a *published producer-side contract a client knows before it sends*. A frame naming 33 ids is not a conforming `send_message`, in the same sense that an ill-typed `attachment_ids` is not — and that already answers `protocol.malformed` from this handler. Minting a code would add `internal/protocol/codes.go` plus its two `compat_test.go` value pins, for a client repair (*send fewer ids*) identical to every other malformed-frame repair. It is not `message.too_long`, which § Error codes reserves for one oversized **envelope** — 32 ids are under 2% of the envelope cap, so the two conditions are independent. It is not `attachment.not_found`: nothing failed to resolve, and answering it would be a false statement about the ids.
- **Not retryable.** Resending the same frame reproduces it — `attachment.invalid_chunk`'s stated posture.
- **The boundary is inclusive:** exactly 32 is accepted.

### D6 — a repeated id is **deduplicated**, on first occurrence, *after* the bound is counted

- The bound is checked against the **raw element count**, because `MaxAttachmentIDsPerMessage` publishes that it counts elements, not distinct ids. 33 copies of one id is over bound. Enforcing on the post-dedup count would silently contradict the published counting rule.
- Distinct ids are then resolved once each, in **first-occurrence order**, and each path is named once in the prompt. Refusing a repeat punishes a client for something harmless; naming a path twice tells claude to read one file twice. Dedup also bounds the resolution work to at most 32 directory reads regardless of the list.
- AC 1's "in the order the client listed them" is preserved as first-occurrence order — the only reading of "the order the client listed them" that survives a repeat.

Both decisions are published back into `docs/protocol-mobile.md` § *Naming a message's attachments*, replacing the two paragraphs that currently name them as this ticket's, plus a § Changelog entry.

### D7 — the prompt shape

Composition is a pure unexported function in `handlers` taking the user's text and the ordered paths and returning the delivery string. Contract:

- **Empty path list ⇒ the text is returned byte-for-byte.** AC 2 is an identity, not a formatting rule, and it holds for all three empty wire forms because `SendMessagePayload.UnmarshalJSON` has already collapsed them to one nil value.
- **Non-empty ⇒ the user's text verbatim, then a blank line, then a daemon-authored block** naming each path on its own line and directing claude to read them. The user's text is never quoted, wrapped or escaped — quoting would mangle a text containing the quote character and buys nothing.
- **Empty text with attachments ⇒ the block alone**, with no leading blank line.

The ticket's sketch (`User text: '<msg>'. Attachments: [...]`) is explicitly a starting point; this shape is preferred because it makes the empty case a literal identity and keeps the transformation purely additive. One path per line also means no delimiter a path could contain — see § Security review, finding 1.

### D8 — logging

The handler's new branches log **counts, never ids**: the enqueue Info gains an attachment count; the over-bound Warn logs the count it refused; the not-found Warn logs the count and nothing else. No id is logged even after validation — the failing one may be shape-invalid by definition, and a per-id line would rebuild by log what the static message refuses to say on the wire. No path and no filename is logged anywhere, by anyone: the adapter drops `ResolvePath`'s error and logs nothing, and the composed prompt is never logged (it is the queued text's own never-log discipline, one field over).

## Concurrency model

No new goroutine, no new lock, no lifecycle. `ResolvePath` is documented stateless, lock-free and safe for concurrent use; resolution happens inline on the per-conn dispatch goroutine that already runs `SendMessage`, alongside `Route`'s in-memory lookup.

`EnqueueDelivery` takes and releases `q.mu` exactly where `Enqueue` did — the same single hold covering the cap check, the id mint, the append and `maybeSpawnDrainLocked`, with `notify` fired strictly after the unlock. `Enqueue` delegating to it adds no second acquisition. `drain` reads `head.delivery` from the value copy it already took under the lock, so the field introduces no new shared read.

The bounded work per frame is a concern worth naming: up to 32 directory reads on a dispatch goroutine before the ack. That is `MaxAttachmentIDsPerMessage`'s stated purpose — *"bounds that work at a number a receiver does inline without a queue"* — and D5 enforcing it is what makes the bound true rather than aspirational.

## Error handling

| Condition | Reply | Retryable | Enqueued? |
|---|---|---|---|
| Payload will not decode (incl. ill-typed `attachment_ids`) | `protocol.malformed` | no | no |
| More than 32 elements | `protocol.malformed` | no | no |
| Unknown conversation | `conversation.not_found` | no | no |
| Conversation has no bound session | `server.binary_offline` | yes | no |
| Any named id does not resolve under this conversation | `attachment.not_found` | no | no |
| Backlog at cap | `server.binary_busy` | yes | no |
| Otherwise | `ack` | — | yes |

Every reject is all-or-nothing: a message whose ids do not all resolve enqueues nothing, so a partially-attached turn never reaches claude. The `attachment.not_found` message is a package-level static constant in the `msgSendMessageMalformed` idiom, and it names no id, no count and no path — a per-id answer would turn one `send_message` into a batch existence probe for up to 32 ids, which is the oracle the code's merge exists to prevent.

`ResolvePath`'s error is consumed by the adapter and discarded (D3). Nothing in this slice wraps or re-surfaces it.

## Testing strategy

Hermetic only. That claude, handed a path, actually opens the file is #2039's.

**`internal/relay/handlers/send_message_test.go`** — a fake resolver recording its calls, and `fakeEnqueuer` reshaped to record `(convID, text, delivery)` triples:

- No attachments, table-driven over the three empty wire forms (key absent, `null`, `[]`): ack, `delivery == text` byte-for-byte, resolver never called.
- One id resolving: queued `text` is `p.Text` verbatim, `delivery` names the path and directs a read.
- Two ids: both paths present, in the client's listed order.
- Empty text with one id: the block alone, no leading blank line.
- One of two ids failing: `attachment.not_found`, not retryable, nothing enqueued, and the reply message contains neither named id nor any path.
- Nil resolver with a non-empty list: `attachment.not_found`, nothing enqueued (fail-closed).
- Exactly 32 ids: accepted (the inclusive boundary).
- 33 ids: `protocol.malformed`, not retryable, nothing enqueued, and **neither the router nor the resolver is called** — the assertion that pins D4's step-2-before-step-3 ordering.
- 33 copies of **one** id: still refused. Without this row D6's "count elements before dedup" is unpinned — a post-dedup count would pass every other row.
- A repeat inside the bound: the resolver is called once for that id and the path appears once, at its first-occurrence position.
- A resolve failure on a conversation with no bound session answers `server.binary_offline`, not `attachment.not_found` — the other half of D4's ordering.
- Log discipline, over the success, over-bound and not-found paths: capture the handler's `slog` output into a buffer and assert it contains none of the ids used, none of the resolved paths, and not the filename leaf. The fixture filename must differ from its sanitised form so the assertion cannot pass by accident.

**`internal/msgqueue/queue_test.go`:**

- `EnqueueDelivery` with differing text and delivery: `DeliverFunc` receives the delivery bytes while `Snapshot` reports the text. The two strings must differ, or the test is vacuous.
- `Enqueue` remains delivery-equals-text: the delivered bytes equal the snapshot text (the engine-side pin for AC 2).
- `SnapshotAll` reports the text for an entry enqueued with a differing delivery.
- The per-conversation cap rejects an `EnqueueDelivery` past it identically: returns 0, appends nothing, mints no id.

**`cmd/pyry/queue_state_v2_test.go`:**

- Over a real `*msgqueue.Queue` whose `Deliver` never succeeds, enqueue via `EnqueueDelivery` with a delivery carrying a distinctive host-path-shaped string, then assert **both** AC-4 read-back paths — `toQueueStatePayload` over `Snapshot` (the enqueue push) and `outstandingQueues` (the connect-time reconcile) — carry the user's text and do not contain that string.

## Open questions

1. **Does the composed block belong before or after the user's text?** *Resolved in D7: after.* It keeps the empty-list case a literal identity and puts the daemon's instruction last, where it is the most recent thing claude reads.
2. **Should the handler validate id shape itself before calling the resolver, to get a loggable id?** *Resolved: no.* `ResolvePath` already validates shape before touching the filesystem, and re-deriving the check to win a log field trades a duplicated security check for a field D8 declines to log anyway.
3. **Should `Enqueuer` keep `Enqueue` alongside `EnqueueDelivery`?** *Resolved: no.* The handler calls only the latter, and CODING-STYLE wants consumer-side interfaces at one or two methods. `*msgqueue.Queue` satisfies either shape; the single method is the honest one.

Any of these reopening during Phase B is recorded as a `## Revisions` entry in the same commit as the code that departs.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The untrusted→path crossing is a single explicit boundary — `ResolvePath`'s two `conversations.ValidID` calls, which run before any `filepath.Join` and before the filesystem is touched. This slice **constructs no path of its own**, so it cannot bypass that boundary; it inherits it rather than re-deriving it. The one place a raw element is touched before validation is D6's dedup, which compares raw bytes as map keys — a pure equality, neither a path nor a log line — and is deliberately non-normalising, since a normalising compare could merge ids that `ValidID` treats differently.

- **[Tokens, secrets, credentials]** Not applicable — nothing is minted, stored, rotated, revoked or compared. Stated positively because the temptation here is specific: the plan nowhere argues an attachment id is unguessable. D4's **confinement** is the whole mechanism, matching the published stance that the id is not a capability; an entropy claim would quietly promote it into one.

- **[File operations]** No findings, three properties named. *Traversal* is the shape check plus `ResolvePath`'s full-path-equality test after `EvalSymlinks`, which is strictly stronger than a "is it under the root" test and is what refuses a conversation directory symlinked at a sibling. *It creates nothing*, so a refused resolve leaves no directory behind for a traversal probe to observe. *TOCTOU:* the check-then-use window is **widened** by this slice — the retrieval leg opens the path immediately, whereas here the path sits in the backlog and may wait a whole claude turn before claude opens it. The attacker capability required is unchanged (write access inside the daemon's own `0o700` state directory, which already permits rewriting `devices.json`), so `ResolvePath`'s accepted bound still covers it, but the widening is recorded rather than assumed. No file is created and no mode is chosen anywhere in this slice.

- **[Subprocess / external command execution]** No findings. The composed prompt reaches claude as a JSON-encoded stdin line through `WriteTurn`; it is never an `exec.Command` argument and no `sh -c` is involved, so a resolved path's leaf never meets a shell. Verified along the whole leg rather than assumed from the nearest symbol.

- **[Cryptographic primitives]** Not applicable — no randomness, no key, no nonce, and no comparison against a secret. D6's dedup compares non-secret ids, so a constant-time compare would be theatre rather than hardening.

- **[Network & I/O]** No findings, and one strict improvement. D5 is the **first** enforcement of `MaxAttachmentIDsPerMessage`: before it, a frame well inside the 65519-byte envelope cap could name roughly 1680 ids and cost that many directory reads on a dispatch goroutine. The bound is now checked before `Route` and before any resolve, capping the work at 32 reads per frame — which is the number the constant was chosen to make safe inline. Per-conn frame concurrency and the transport's read ceiling are inherited unchanged.

- **[Error messages, logs, telemetry]** No findings, and this is where the pass dug hardest: the change puts a **host path** into a payload that previously carried only user text, so any pre-existing site echoing that payload would become a *new* disclosure without this diff touching it. Traced the whole delivery leg — `newInboundDeliver` → `boundSession.WriteUserTurn` → `Session.WriteUserTurn` → `streamRunner.WriteUserTurn` → `Runner.WriteUserTurn` → `WriteTurn` — and the payload is never logged and never formatted into an error: the three wrappers are pure passthroughs and `WriteTurn`'s only two errors are static-prefixed wraps of a marshal/write failure carrying no payload byte, so `msgqueue`'s drain Warn and `giveUp`'s reason cannot acquire one either. Also confirmed that nothing echoes a user turn back to other devices (`TypeMessage` has had no production emitter since #699) and that `Snapshot`/`SnapshotAll` have exactly one consumer, `cmd/pyry/queue_state_v2.go`. The remaining hazard is removed structurally by D3: `ResolvePath`'s shape-invalid refusal formats the **raw client id** into its message, and the comma-ok seam means the handler never holds that error to log it.
  - **SHOULD FIX** — the log-discipline test's fixture filename must differ from its own sanitised form. With `report.pdf` the "filename absent from logs" assertion passes whether or not anything is disclosed, exactly the vacuity `TestResolvePath_RoundTrip`'s two-row table exists to defeat. It is in the testing strategy; the verifier should check it landed that way.

- **[Concurrency]** No findings. No goroutine is spawned and no lock is added. The one implementation constraint is that `Enqueue` must delegate to `EnqueueDelivery` **without** taking `q.mu` first — a `sync.Mutex` is not re-entrant — and a violation would hang all 108 existing `Enqueue` call sites immediately rather than shipping silently. The resolve-then-deliver window can hand claude a path whose file was removed in between; claude reports a missing file, which is graceful degradation and the same ack-then-drain asymmetry `SendMessage` already documents for a conversation that unbinds post-ack.

- **[Threat model alignment]** § Security model **threat 1 (prompt injection, `high` / `partial`)** lands squarely here, since `attachment_ids` become prompt content, and it is answered on two questions rather than waved through.
  - *Can a filename inject or forge a path line?* No. The only client-authored bytes in a resolved path are `SanitizeFilename`'s output, whose allowlist is `[a-zA-Z0-9_.-]` and which guarantees exactly one path component containing no `/` and no control character. A filename therefore cannot add a line to D7's one-path-per-line block, cannot escape the attachment directory in the rendered text, and cannot carry any delimiter that block relies on. The property is pinned by that function's own package tests; this slice re-derives none of it.
  - *Does the daemon-authored block confer authority?* No. A client already controls `Text` in full and can already instruct claude to read any path, so it can forge an identical-looking block today, with or without this ticket. The block adds no capability claude did not already have from arbitrary user-role text, which is why threat 1's severity and mitigation are unchanged rather than downgraded.
  - Threats 2 and 3 are untouched: AC 4 keeps the composed prompt off the wire entirely, so the relay's view is byte-identical to today's.
  - **OUT OF SCOPE** — the composed prompt lands in claude's session JSONL, which `debugbundle` may stream to a paired phone. Host paths already saturate any claude transcript (every `Read` tool call records one), so this adds no class of disclosure and no follow-up ticket is named; bundle scrubbing, if ever wanted, is a whole-transcript policy and not this slice's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03

## Revisions

### 2026-09-03 — implementation

Two departures from the plan as committed. Neither changes a design decision; both are recorded because the plan states the thing they depart from explicitly.

- **The log-discipline test's non-vacuity guard is different, and stronger.** The plan (and the security review's SHOULD FIX) asked for a fixture filename differing from its own sanitised form. That guard is the right one for `ResolvePath`'s round trip and the wrong one here: the handler test uses a *fake* resolver, so no sanitiser runs and the filename's form proves nothing. What actually makes "the log does not carry the path" vacuous is the handler never having held the path. `TestSendMessage_AttachmentPathsNeverLogged` therefore asserts the positive first on the success branch — the composed delivery *does* carry the path — and only then asserts the log's silence. A build that dropped the path entirely fails the first assertion instead of passing the second.

- **`internal/protocol/messaging.go` is edited, making four production files rather than the three the plan counted.** Doc comments only; no behaviour, no wire change. `MaxAttachmentIDsPerMessage` and `SendMessagePayload` both said enforcement and consumption were this ticket's still-future work, which stops being true the moment this lands, and a declaration that points at a ticket already closed is the stale-citation shape the whole family works to avoid. The blocks now name the enforcing symbol (`SendMessage` in `internal/relay/handlers`), and the "unchecked" claim is narrowed to what stays true — that *this package* still counts nothing. Four files remains inside the five-file ceiling.
