# #2115 — Record the operator's own message in the conversation log on confirmed delivery

Status: plan (Phase A)
Ticket: [#2115](https://github.com/pyrycode/pyrycode/issues/2115) — `size:s`, `security-sensitive`
Split from #2091. Depends on #2112 (the store) and #2114 (the shared append seam).

## Files read

- `internal/msgqueue/queue.go` → `DeliverFunc`, `ChangeFunc`, `GiveUpFunc`, `PendingFunc`,
  `Config`, `QueuedMessage`, `queued`, `Queue.drain`, `Queue.notify`, `Queue.notifyGiveUp`,
  `Queue.giveUp`, `Queue.commitGate`, `convQueue.advanceLocked`, `Queue.EnqueueDelivery` —
  the whole surface this ticket extends. `drain`'s `err == nil` branch is the fire site;
  `notify`/`notifyGiveUp` are the fire-after-unlock helpers to copy; `QueuedMessage` is the
  projection that structurally omits `delivery`.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`, `historyAppendFailure` —
  the seam to reuse, not re-implement. Carries the nil-store no-op, the never-suppress rule
  and the content-free failure discriminant.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.emit` — the first #2114
  producer: one marshal, one hoisted `time.Now().UTC()`, defensive marshal-error `Debug`,
  then the append. The shape this producer mirrors.
- `cmd/pyry/session_transition_v2.go` → `sessionTransitionEmitterV2.broadcast` — the second
  #2114 producer; the precedent for passing a `protocol.Type*` constant as `typ`.
- `cmd/pyry/main.go` → `runSupervisor` (the `queueChanges` / `giveUps` / `approvalParked`
  pre-`msgqueue.New` block, the `msgqueue.New` config literal, the `conversationHistory`
  mint), `newInboundDeliver`, `resolveInstanceDirPath`, `queueStateNotify`,
  `sessionErrorNotify`. `newInboundDeliver` receives only `payload []byte` — i.e.
  `head.delivery` — which is why it cannot be this ticket's producer.
- `internal/protocol/messaging.go` → `MessagePayload`; `internal/protocol/codes.go` →
  `TypeMessage`; `internal/protocol/envelope.go` → the known-type table (`TypeMessage` is
  already listed). The declared-but-unproduced shape AC 5 names.
- `internal/history/log.go` → `Store`, `New`, `Store.Append`, `Store.Page`, `Entry`, `Page`,
  `ErrInvalidID`, `ErrInvalidPayload` — the append contract and the read-back the tests use.
- `internal/conversations/id.go` → `ValidID` — `Append` refuses a non-canonical conversation
  id, so test fixtures must use a real UUIDv4 shape (`testConvID` in
  `cmd/pyry/interactive_turn_v2_test.go` is the package's existing one).
- `docs/knowledge/features/msgqueue-package.md` § "Introspection, removal, and change
  notification (#719)", § "Security" — **the binding lesson**: `QueuedMessage` carrying no
  `delivery` field is what makes the no-leak property *structural rather than a rule*, and
  a future consumer wanting the path "has to widen the exported type to get it". This plan's
  seam signature is chosen to inherit that property rather than restate it.
- `docs/knowledge/features/history-package.md` § "Producers (#2114)" — records that the
  operator's own message "is #2115's producer, written at delivery against the same store,
  not at emit", and the two test-shape traps (a shared-timestamp assertion needs three
  fan-out targets; a "nothing was appended" assertion is only meaningful below the drop's
  own log level).
- `docs/protocol-mobile.md` § Queue (`message_id` — relayed verbatim, addresses nothing,
  legally `""`, may collide across devices) and § Error codes (the host-path disclosure ban
  that makes `text`-not-`delivery` a security requirement rather than a preference).
- `internal/streamsup/envelope.go`, `internal/agentrun/streamrunner/runner.go` → the
  existing inline `Role: "user"` literals. There is no `roleUser` constant in the repo; the
  literal is the convention.

## Context

A client that opens an existing conversation sees only claude's half of it. #2112 shipped
the durable per-conversation log; #2114 gave it its first producer, writing every
interactive envelope and every session transition. Neither writes what the *operator*
typed, because nothing in the daemon ever observes it a second time: the text goes
`send_message` → `msgqueue` → `newInboundDeliver` → `WriteUserTurn` → claude's stdin, and
claude does not echo it back (`emitUser` in the stream parser maps only `tool_result`).
`protocol.MessagePayload` with role `user` is declared and has no producer in the binary.
So the daemon must write this record itself.

This is the third producer against the same store, and it needs no new store, no new
envelope type and no new append seam — only a point in the queue where `text` and a
confirmed write are both in hand.

**No ADR is warranted.** The additive-seam-versus-signature-widening choice below is a
sizing consequence of an existing documented constraint (the 10-call-site ceiling), not a
new architectural direction, and the security property it turns on is already recorded in
`msgqueue-package.md` § Security. The documentation phase should fold this ticket's lessons
into `docs/knowledge/features/msgqueue-package.md` (a fourth seam beside `OnChange` /
`OnGiveUp` / `Pending`) and `docs/knowledge/features/history-package.md` § Producers (a
third producer).

### In-flight overlap check (§ A2) — one hit, dismissed

`origin/feature/934` touches `internal/msgqueue/queue_test.go`. It is **not** in-flight
work: issue #934 is CLOSED (2026-08-06), its PR #935 is CLOSED and was never merged, and
the branch tip dates to 2026-07-11. A dead branch cannot conflict at integration time, and
`addBlockedBy` against a closed issue resolves immediately, so blocking on it would bounce
this ticket to Backlog for nothing. No other feature branch touches any file in scope.

## Design

Three changes, in dependency order.

### 1. `internal/msgqueue` — a fourth optional seam

A new exported func type and one `Config` field, mirroring `OnChange` / `OnGiveUp` /
`Pending` exactly:

```go
// DeliveredFunc is the injected delivered-notification seam.
type DeliveredFunc func(convID string, msg QueuedMessage)
```

`Config.OnDelivered DeliveredFunc` — optional, `nil` ⇒ disabled. Stored on `Queue` as
`onDelivered`. Fired through a `notifyDelivered` helper that mirrors `notify` /
`notifyGiveUp`: it does the nil-check, and the caller MUST have released `q.mu`.

**Why `QueuedMessage` and not three strings.** The seam's parameter is the *existing*
exported projection, which has no `delivery` field. That is not a convenience — it is the
security property `msgqueue-package.md` § Security states for `Snapshot`/`SnapshotAll`,
inherited verbatim: this seam is *structurally incapable* of handing a consumer the
host-path-bearing payload, so AC 2 cannot be violated by a consumer forgetting a filter.
A `func(convID, messageID, text string)` alternative would satisfy AC 2 today by
discipline only, and (all-strings) would let a transposition compile — the hazard
`EnqueueDelivery`'s own PARAMETER ORDER note already records.

The one cost: `QueuedMessage.TS` is the **enqueue** timestamp, and this ticket must stamp
at confirmation. The seam's doc comment says so explicitly and the producer never reads it.

**Fire site: the drain's `err == nil` branch, after the unlock, unconditional on
`advanced`.** The neighbouring `q.notify` is guarded by `if advanced` because a `Remove`
landing during delivery makes a backlog change that `Remove` already notified. That guard
is about the *backlog*; this seam is about *what was said*. Once the seam returns nil the
text has reached claude's stdin and will be answered, so a `Remove` afterwards cancels
nothing that already happened. Fired before `q.notify` so the durable record is written as
close to the confirmed commit as possible.

In production the two orderings are indistinguishable — `WriteUserTurn` always claims
`commitGate`, so `err == nil` implies the claim won, implies a concurrent `Remove`
no-opped, implies `advanced`. A test double that returns nil without claiming the gate is
what makes the decision observable, and one test pins it (below).

### 2. `cmd/pyry` — the producer

A new file, `cmd/pyry/operator_message_history.go`, holding one closure factory in the
shape of `queueStateNotify` / `sessionErrorNotify`:

```go
func newOperatorMessageHistory(store *history.Store, logger *slog.Logger) msgqueue.DeliveredFunc
```

Behaviour, one line each:

- Build `protocol.MessagePayload{ConversationID: convID, MessageID: msg.MessageID,
  Role: "user", Text: msg.Text}` and marshal it once. `Text` is `msg.Text`; `msg` has no
  other text-shaped field to reach for.
- On a marshal error, `Debug` and return, carrying `event` + `conversation_id` and never
  the payload or `err.Error()` — the defensive branch both #2114 producers carry, for the
  same reason (`encoding/json` quotes invalid input bytes into its error).
- Otherwise call `appendConversationHistory(store, logger, "operator_message.history_append_err",
  convID, protocol.TypeMessage, payloadJSON, time.Now().UTC())`.

The `time.Now().UTC()` is minted here, at confirmation, matching the UTC both #2114
producers hoist so entries from all three are orderable by the field the log stores.

`store` may be nil and the seam stays wired regardless: `appendConversationHistory` already
makes a nil store a silent no-op, so no branch is added here.

### 3. `cmd/pyry/main.go` — hoist and wire

Move the `conversationHistory := history.New(resolveInstanceDirPath(*name))` statement (and
its comment) from below `startRelay` up into the block already built **before**
`msgqueue.New` alongside `queueChanges`, `giveUps` and `approvalParked`. The existing
comment already names `newInboundDeliver` as #2115's producer and the reachability problem;
it gets one sentence updated to name the seam that actually reaches it. `history.New` takes
no lock and touches no filesystem, so the hoist is free and the single-Store-per-instance
rule is preserved — the value is moved, never constructed twice.

Then one added line in the `msgqueue.Config` literal:

```go
OnDelivered: newOperatorMessageHistory(conversationHistory, logger),
```

`relayWiring.hist` keeps reading the same `conversationHistory` variable, now declared
earlier in the same function body. Zero other call sites change.

### Rejected: widening `DeliverFunc`

`msgqueue.DeliverFunc` has 64 `Deliver:` literals across 8 files (1 production, 63 test).
Widening its signature is not separable from those in Go — 6× the size table's 10-call-site
ceiling, and unsplittable. It is also the *wrong* seam on the merits: `newInboundDeliver`
receives `head.delivery`, so a producer there could satisfy AC 1, 3, 4 and 5 while being
structurally unable to satisfy AC 2. The additive seam costs zero call sites and is the
only shape with `text` in hand.

## Concurrency model

No new goroutines. `notifyDelivered` runs **on the drain goroutine**, one per conversation,
strictly after `q.mu` is released — the same rule `notify` and `notifyGiveUp` state, for the
same reason: a re-entrant consumer must be able to call back into
`Snapshot`/`Remove`/`Enqueue` without deadlocking.

The consumer performs a local file append under `history.Store`'s own mutex. Two facts make
that safe: `Store.Append` takes only its own lock and calls nothing back into `msgqueue`, so
no lock cycle exists; and the append blocks only this conversation's drain, delaying its
*next* delivery, never another conversation's. Like `ChangeFunc`, the seam's contract says
it must not block indefinitely — a bounded local write satisfies that, an unbounded one
would not, and the doc comment says so.

`Store` is shared by three producers on different goroutines; its mutex is the arbiter and
this ticket adds no new synchronization.

Shutdown: unchanged. The `ctx.Err() != nil` check in `drain` precedes the `err == nil`
branch, so a delivery racing shutdown leaves the head queued and fires nothing.

## Error handling

- **Marshal failure** → `Debug`, return, no append. Unreachable in practice
  (`MessagePayload` is four strings), kept because both sibling producers keep it.
- **Append failure** → handled entirely inside `appendConversationHistory`: `Warn` with an
  `errors.Is`-derived discriminant (`invalid_id` / `invalid_payload` / `write`), never the
  error text (which formats absolute paths), never the content.
- **A failing append never affects delivery.** The seam fires after the write is confirmed
  and returns nothing, so there is no branch for the drain to take. `newInboundDeliver`'s
  return values and wrapping are untouched, which is what keeps `markApprovalHolds`' and
  `msgqueue`'s `errors.Is` classification of `errStreamTurnHold`, `ErrNoLiveSession`,
  `ErrTrustModalPending` and `turncommit.ErrDropped` working.
- **Nil store** → silent no-op (the seam is still wired; `appendConversationHistory`
  guards). Nil seam → `notifyDelivered` returns immediately.

**One inherited gap, named rather than fixed.** `drain` tests `ctx.Err() != nil` *before*
the `err == nil` branch, so a delivery that confirms exactly as the daemon shuts down
leaves the head queued and fires nothing — no `q.notify`, and now no `notifyDelivered`.
That message reached claude's stdin but produces no log entry. This is the drain's
pre-existing shutdown ordering (the same window in which `queue_state` is already not
notified and the in-memory backlog is already lost), not something this ticket introduces,
and moving the fire above the shutdown check would change #487/#1484 semantics. AC 1 holds
everywhere except this window; #2116 should not assume the log is gap-free across a
restart.

## Testing strategy

### `internal/msgqueue` (new `delivered_test.go`)

Scenarios, one clause each — a fixture that trips two rules pins neither:

- **Fires once on a confirmed delivery, carrying `text` and not `delivery`.** Enqueue via
  `EnqueueDelivery` with the #2038 divergence (a distinctive, non-empty host-path-shaped
  delivery string), drain through a deliver that returns nil. Assert exactly one call whose
  `QueuedMessage` carries the client message id, the queued id, and `Text` equal to the
  queued text. The delivery-payload needle is asserted absent — checked against a non-empty
  literal, since a `strings.Contains` on `""` is unconditionally true.
- **Exactly one call across retries.** Deliver fails twice, then succeeds. Count only.
- **No call on give-up.** Always-failing deliver plus a short `GiveUpAfter`. Zero calls.
- **No call when the head is removed before it commits.** Deliver blocks; the test
  `Remove`s the waiting head, which cancels the delivery ctx; deliver returns that error.
  Zero calls. (This is AC 3's genuinely-not-said case.)
- **Fires even when a `Remove` raced a confirmed delivery.** A deliver that returns nil
  *without* claiming `commitGate`, with the head removed during the wait, so
  `advanceLocked` reports `false`. One call. This is the only test that distinguishes the
  chosen design from an `if advanced` guard.

Nil-seam safety needs no test of its own: every pre-existing test in the package
constructs a `Config` without `OnDelivered`, so a nil-deref would redden the file.

### `cmd/pyry` (new `operator_message_history_test.go`)

End-to-end through a real `msgqueue.Queue` and a real `history.Store` rooted at
`t.TempDir()`, read back with `Store.Page`:

- **The appended entry is a `message` envelope with role `user`.** Enqueue through
  `EnqueueDelivery` with divergent text/delivery, drain, then assert on the decoded entry:
  `Type == protocol.TypeMessage`, `Role == "user"`, `ConversationID`, `MessageID` equal to
  the client's, `Text` equal to the queued text — and the delivery needle absent from the
  entry's **raw payload bytes**, not merely from the decoded `Text`, so a leak into any
  other field is caught too.
- **The entry is stamped at confirmation, not at enqueue.** Deliver blocks on a channel;
  the test reads the enqueue timestamp from `Snapshot`, releases, and asserts the entry's
  `TS` is strictly after it.

`appendConversationHistory`'s own nil-store and failure behaviour is already covered by
`cmd/pyry/conversation_history_test.go` and is not re-tested here.

### Verification gate

`go test -race ./internal/msgqueue/... ./cmd/pyry/...`, `go vet ./...`,
`go build ./cmd/pyry`. The full-module race suite is the verifier's gate, not this run's.

## Open questions

1. **Fire before or after `q.notify` on the confirmed-delivery path?** Resolved in this
   plan: before, so the durable write happens as close to the confirmed commit as possible.
   Functionally either order works; nothing reads the log in real time (#2116 serves it on
   request).
2. **Does the producer need the queued id (`QueuedMessage.ID`)?** No — `MessagePayload` has
   no field for it and AC 1 asks for the *client's* id. It rides along because the seam
   passes the whole projection; the producer ignores it.
3. **Should the marshal-error branch exist at all, given it is unreachable?** Kept, to match
   both sibling producers. Revisit only if a reviewer objects.

Any of these that changes during Phase B gets a `## Revisions` entry in the same commit as
the code.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. Two untrusted, phone-originated values cross into a
  durable record here — `QueuedMessage.Text` and `QueuedMessage.MessageID` — and the
  boundary is a *single* explicit point, `newOperatorMessageHistory`'s closure, which
  builds one closed struct and hands it to `appendConversationHistory`. The third
  untrusted value, `delivery`, **cannot reach this boundary at all**: the seam's parameter
  is `QueuedMessage`, which declares no such field, so the property is structural in
  exactly the way `msgqueue-package.md` § Security describes for `Snapshot`/`SnapshotAll`.
  A future consumer wanting the host path would have to widen the exported type.
  `convID` is not client-asserted at this point — it is the queue's own map key, bound to
  the authenticated session by `SendMessage` upstream of `Enqueue`, which is the
  precondition `Store.Append`'s doc block requires.
- **[Trust boundaries]** No finding, decision recorded: firing unconditional on `advanced`
  means a message whose queued row a second device dequeued *after* the write committed is
  still logged. That is the honest record — claude received the text and will answer it —
  and suppressing it would leave the served page holding an answer to a question it does
  not contain.
- **[Error messages, logs, telemetry]** No findings, and this is the category with the most
  exposure. Neither `Text` nor `MessageID` reaches any log line on any path: the producer's
  marshal-error branch logs `event` + `conversation_id` only, `appendConversationHistory`
  logs `event` + `conversation_id` + an `errors.Is`-derived discriminant and deliberately
  **not** `err` (whose text formats absolute paths), and `notifyDelivered` logs nothing at
  all. This preserves the never-logged discipline `queue.go`'s package comment states for
  all three untrusted values across the new exit from the package. Phase B must not add a
  field to either log call.
- **[File operations]** No findings. This ticket builds no path and opens no file; all
  filesystem work stays inside `internal/history.Store`, whose modes are already
  `0o600` for segments and `0o700` for directories, and whose `Append` validates `convID`
  with `conversations.ValidID` **before** `resolveDir` is reached — so a non-canonical
  conversation id can never become a directory component, it is refused with
  `ErrInvalidID`. Marginal data-at-rest exposure is real but small: the operator's text
  already persists on the same host in claude's own transcripts, and #2114 already writes
  claude's half of the conversation to this same store under the same modes.
- **[File operations — log-entry forgery]** No finding, verified rather than assumed. The
  log is line-oriented, so untrusted text containing a newline would be an entry-injection
  vector. `encodeEntry` uses `encoding/json`'s `Encoder.Encode`, which escapes every
  control character inside a string value and emits exactly one `\n`-terminated line;
  `SetEscapeHTML(false)` does not affect that escaping. A `text` of
  `"\n{\"id\":9999,...}"` is written as escaped bytes on one line and cannot forge a second
  entry.
- **[Network & I/O]** No findings. The seam adds no network surface. It does add a
  synchronous local disk append **on the drain goroutine**, so a flooding client makes the
  daemon do one append per confirmed delivery — but each confirmed delivery already costs a
  whole claude turn, orders of magnitude more, and `MaxQueuedPerConversation` (100) bounds
  the backlog. No new amplification. Payload size is bounded twice over: the v2 application
  envelope caps a `send_message` at 65519 bytes, well under `MaxSegmentBytes` (1 MiB), so
  an oversized-entry refusal is unreachable through the wire path.
- **[Concurrency]** No findings. `notifyDelivered` fires strictly after `q.mu` is released,
  the rule `notify`/`notifyGiveUp` already state, so a re-entrant consumer cannot deadlock.
  No lock cycle is possible: `go list -deps ./internal/history` shows its only in-repo
  dependency is `internal/conversations`, so `Store.Append` cannot call back into
  `msgqueue`. One drain goroutine per conversation means the seam is never concurrent
  *within* a conversation; across conversations `Store`'s own mutex arbitrates. No
  goroutine is spawned, so none can leak.
- **[Concurrency — crash mid-append]** OUT OF SCOPE, inherited. A process killed inside
  `Store.Append` can leave a torn trailing line; recovery is `internal/history`'s
  (`decodeSegment` drops an unterminated trailing line, and `history-package.md` § "A
  cleanup on a failed write is not the guarantee it looks like" records the reasoning).
  This ticket adds no new failure mode there. The shutdown-window gap in § Error handling
  is the related availability limitation, also inherited from `drain`'s existing ordering.
- **[Tokens, secrets, credentials]** Not applicable, with the reason: no value handled here
  is a capability. `MessageID` is client-chosen, addresses nothing, authorizes nothing, is
  legally `""` and may legally collide across devices (`docs/protocol-mobile.md` § Queue),
  so it is untrusted content rather than a credential and gets content treatment.
- **[Subprocess / external command execution]** Not applicable. The text still reaches
  claude's stdin through `newInboundDeliver` → `WriteUserTurn`, unchanged; this ticket adds
  no exec, alters no argv, and deliberately leaves that seam's return values and wrapping
  byte-identical so `markApprovalHolds`' error-identity classification keeps working.
- **[Cryptographic primitives]** Not applicable — no randomness, no keys, no comparisons
  against secrets. No id is minted here; the log's own id comes from `Store.Append`.
- **[Threat model alignment]** The one `docs/protocol-mobile.md` threat this ticket engages
  is the § Error codes ban on disclosing the daemon's filesystem layout, and the seam
  signature is the mitigation. Prompt injection (§ "Phone messages become user-role input
  to claude") is unchanged — nothing about what reaches claude moves. Serving this log to a
  paired device is #2116's gate, not this ticket's; cross-conversation confidentiality
  rests on the per-conversation keying plus that ticket's authorization.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05
