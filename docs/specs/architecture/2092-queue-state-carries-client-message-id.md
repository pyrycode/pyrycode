# #2092 — carry the client's `message_id` on each `queue_state` item

A client posts an optimistic echo the moment the operator hits send, because in
interactive mode the daemon streams no user-message event. When the daemon parks
the message instead of running it, the same message also arrives as a
`queue_state` item — and nothing on the wire says the two are one message, so
both clients draw it twice. This ticket mints the correlation key: the
client-chosen `message_id` from the `send_message` that produced the item, stored
on the queued record and projected onto both `queue_state` arms.

## Files read

- `internal/msgqueue/queue.go` → `queued`, `QueuedMessage`, `EnqueueDelivery`,
  `Enqueue`, `Snapshot`, `SnapshotAll` — the record, the two projection loops,
  and the `text`/`delivery` split this change must not disturb.
- `internal/msgqueue/queue.go` → package doc comment, `drain`, `giveUp` — the
  never-log discipline: the drain's warn/debug lines carry `conversation_id`,
  `queued_msg_id`, `queued_at` and nothing else, and this change adds no field
  to them.
- `internal/protocol/messaging.go` → `QueuedItem`, `QueueStatePayload`,
  `SendMessagePayload` — the wire item shape, and `SendMessagePayload.MessageID`
  as the source of the value (non-omitempty, handler-unvalidated).
- `internal/protocol/messaging.go` → `SendMessagePayload.UnmarshalJSON` — the
  package's one deliberate departure from "pure DTOs"; read to confirm this
  change needs no decode-boundary normalisation (a plain `string` has one empty
  wire form, unlike a slice's three).
- `cmd/pyry/queue_state_v2.go` → `toQueueStatePayload`, `outstandingQueues`,
  `queueStateEmitterV2.broadcast` — the single mapping both arms share, and the
  emitter's never-log discipline.
- `internal/relay/handlers/send_message.go` → `Enqueuer`, `SendMessage` — the
  interface to widen and the sole production call site, which already holds
  `p.MessageID` at the enqueue point.
- `internal/protocol/messaging_test.go` → `TestQueueStatePayload_RoundTrip`, and
  `internal/protocol/interactive_test.go` → `roundTripEnvelope`, `canonical` —
  `roundTripEnvelope` re-marshals the **payload struct** (not `env.Payload`'s raw
  bytes), and `canonical` is `json.Compact` with no key sorting. Together they
  make the fixture a strict positional, tag-exercising pin.
- `cmd/pyry/queue_state_v2_test.go` → `TestQueueState_ComposedDeliveryNeverReachesTheWire`
  — the both-arms test shape this ticket's AC 1 + AC 2 test copies.
- `docs/knowledge/features/msgqueue-package.md` § Security — the `delivery`
  entry: `QueuedMessage`'s shape, not a filter, is what keeps a host path off the
  wire. This change widens that exact type, so the plan must say why the new
  field is safe to project where `delivery` is not.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — two lessons
  that shape the test list: an `env`-round-trip whose helper re-marshals only
  `env.Payload` exercises **no** struct tag (so a tag claim needs a test that
  marshals the bare payload), and a fixture regeneration can turn a round trip
  green while a key-set pin stays red.
- `docs/knowledge/features/protocol-package-types-queue-v2-wire-payloads.md` —
  the "no `omitempty` on any field / fixtures authored in struct-field order"
  rule this field inherits.
- `docs/protocol-mobile.md` § Queue (v2) — the contract both clients read; AC 5's
  target.

## Context

Neither client can merge its optimistic echo with the queued row, because the two
records share no key. Text is not a key (two identical messages are legal) and
position is not a key (the backlog is a suffix that mis-aligns by one the moment
the operator drops something in the middle). The daemon already holds both ids
side by side at the enqueue site — `send_message`'s enqueued log line names
`message_id` and `queued_msg_id` together — it simply never stores the first.

The visible bug is worse than a duplicate row: dropping the queued message
removes only the queued row, leaving the echo in the transcript reading as a
message claude received. It never did.

Downstream, pyrycode-desktop#1075 and pyrycode-mobile#624 each own their half of
the merge; #2091 wants the stored key. All three are blocked on this field
existing.

**No ADR is warranted.** This adds one field to an existing record and projects
it through an existing mapping; it introduces no new pattern, no new trust
boundary, and no rejected alternative worth preserving beyond the parameter-order
note below. The contract change belongs in `docs/protocol-mobile.md` § Queue
(AC 5) and the package-overview mirrors are the documentation phase's job.

## Design

One value flows one way and is read by nothing: `SendMessagePayload.MessageID` →
`Enqueuer.EnqueueDelivery` → `queued.messageID` → `QueuedMessage.MessageID` →
`QueuedItem.MessageID` → the wire. Five hops, all pure carry.

### `internal/msgqueue`

`queued` gains `messageID string`, beside `id`, `text`, `delivery`, `ts`. It is
the client's own id for the message, opaque transit exactly as `text` is: stored,
never inspected, never logged, never used in a control decision.

`QueuedMessage` gains `MessageID string`. This is the type the #2038 security
note calls structurally incapable of projecting `delivery` — widening it is the
deliberate act that note describes ("a future consumer that wants the path on the
wire has to widen the exported type to get it"). The distinction that keeps that
property intact: `delivery` is **daemon-composed** and may name an on-host path,
while `messageID` is **client-authored** and is being returned to the trust
domain that authored it. The plan widens the type for the client-authored value
only; `delivery` stays unprojected and the structural guarantee is unchanged.

`QueuedMessage`'s doc comment currently names `Text` as the untrusted,
phone-originated field. It **must** be extended to name `MessageID` as the second
one — see the Security review's Trust-boundaries finding. A sentence that
enumerates the attacker-controlled fields is false the moment a new one lands
beside it, and a reader who trusts the stale enumeration is the #2038
`attachment_id` failure repeating.

Both projection loops — `Snapshot` (feeds the live push) and `SnapshotAll` (feeds
the connect-time reconcile) — copy the field. They are two separate literals in
two separate methods, which is exactly why AC 2 exists and why the test asserts
both arms rather than trusting one to stand for the other.

`EnqueueDelivery` takes the value and stores it:

```go
func (q *Queue) EnqueueDelivery(convID, messageID, text, delivery string) uint64
```

**Parameter order — ids first, then the content pair.** All four parameters are
`string`, so a transposition compiles. Arity change makes every existing call
site a compile error rather than a silent mis-bind, so the migration itself is
safe either way; the ordering choice is about the *next* caller. `convID` and
`messageID` are both identifiers and `text`/`delivery` are both content, so
grouping them keeps the one adjacency that already carries a security meaning —
the `text`/`delivery` pair whose difference the #2038 note turns on — unbroken.
Threading an id between them would put the least related parameter in the middle
of the most related pair. Rejected alternative: appending `messageID` last, which
preserves the existing prefix but splits the two ids and buries the new
parameter where a reader looking for "who sent this" would not look.

A named `type MessageID string` was considered and rejected: it would be a new
exported type in a leaf package for a value nothing reads, `protocol` keeps the
wire field a plain `string`, and the package has no precedent for id wrapper
types (`convID` is a bare `string` by the same convention).

`Enqueue` keeps its two-argument shape and passes an empty id:

```go
func (q *Queue) Enqueue(convID, text string) uint64 {
    return q.EnqueueDelivery(convID, "", text, text)
}
```

The `""` is not a sentinel and nothing branches on it. It is the true value: a
message enqueued through this path was not minted by a client and carries no
client id. That is the same value AC 3 requires for a `send_message` that sent
`"message_id": ""`, and the two being indistinguishable is correct — nothing
reads the field, so nothing can care which produced it.

Widening `EnqueueDelivery` rather than `Enqueue` is what keeps this a
seven-call-site change: `Enqueue` has ~111 call sites, nearly all in tests.

### `internal/protocol`

`QueuedItem` gains `MessageID string` with tag `json:"message_id"` and **no
`omitempty`** — the section's rule is "All fields are always present", and the
whole package's queue cluster carries no `omitempty` on any field.

**Field position: immediately after `QueuedMsgID`.** Two consequences, both
load-bearing. It groups the daemon id and the client id, mirroring the order
`send_message`'s enqueued log line already prints them in. And because
`roundTripEnvelope` re-marshals the payload struct while `canonical` is
`json.Compact` with no key sorting, struct-field order *is* the fixture's byte
order — so the fixture is authored `{queued_msg_id, message_id, text, ts}` and
the round trip pins the position, not just the presence.

`QueuedItem`'s doc comment names `Text` as untrusted, phone-originated transit
content. It **must** gain `MessageID` in that sentence, for the same reason
`QueuedMessage`'s must, and with extra force here: the chosen field position puts
a client-authored id directly beside a daemon-authored one, and a reader who
groups them by shape rather than by provenance would conclude both are the
daemon's. The doc comment is the only signal that separates them — the type
system gives none, both being `string`/`uint64` primitives. The position stays
(it groups the two ids the way the enqueued log line already prints them, and
field adjacency is a weak provenance signal in either arrangement); the comment
is what carries the distinction.

No `UnmarshalJSON` is added. `SendMessagePayload`'s exists because a slice has
three distinguishable empty wire forms (absent, `null`, `[]`); a `string` has
one, and absent and `""` both decode to `""` with no consumer able to tell them
apart — nor any consumer that would.

### `cmd/pyry`

`toQueueStatePayload` maps `m.MessageID → MessageID`, beside the existing
`m.ID → QueuedMsgID`. This is the single mapping **both** arms use —
`queueStateEmitterV2.broadcast` for the live push, `outstandingQueues` for the
connect-time reconcile — so one line satisfies AC 1 and AC 2 in the mapping
layer. The two-arm test still asserts both, because the two arms differ upstream
of this function (`Snapshot` vs `SnapshotAll`), which is where the field could
be dropped.

### `internal/relay/handlers`

`Enqueuer`'s method widens to match, and `SendMessage` passes `p.MessageID` at
the one call site. Nothing validates, trims, normalises or defaults it — the
handler already treats the field this way for its three log lines, and AC 3
requires the wire value be byte-for-byte what arrived.

**No new log site.** The handler already names `message_id` at three places (the
attachment-not-found warn, the backlog-full warn, and the enqueued info line) and
that stays the only place it appears in logs. `msgqueue` adds none: the drain's
retry warn, hold debug and give-up warn keep carrying `conversation_id`,
`queued_msg_id` and `queued_at` only. Adding one would be a new disclosure of a
client-chosen string into a line-oriented log for a value no operator can act on.

### `docs/protocol-mobile.md` § Queue (AC 5)

The `queued` element shape gains `message_id` in the `queue_state` field table —
the string the client sent on the `send_message` that produced this item, relayed
verbatim, `""` when the client sent none, never minted by the daemon.

The fan-out paragraph gains the multi-device rule. `queue_state` reaches *every*
interactive connection, so a client sees ids it never minted, and the rule has
two halves, both load-bearing:

- An item whose `message_id` matches no local echo renders as a plain queued row
  and is **never dropped**. Without this, a client could silently hide a row it
  did not mint.
- A client merges an item against its **own** echoes only — the ones it minted —
  never against a store shared across devices. `message_id` is client-chosen and
  uniqueness across devices is not enforced anywhere, so a colliding id must not
  let one device attribute another's queued message to itself. See the Security
  review's Threat-model finding.

§ Reconnect / Backfill semantics is deliberately **not** edited: it names
`conversation_id` plus `queued_msg_id` as the queue's match-and-replace key, and
that stays exactly right (see "It addresses nothing" below).

### It addresses nothing

`dequeue_message` is untouched: `DequeueMessagePayload` gains no field and
`Remove` still resolves `conversation_id` + `queued_msg_id`. No code path reads
`message_id` to route, authorize, match or dedupe, and the msgqueue test below
pins that behaviourally rather than by assertion — two messages sharing one
`message_id` both enqueue, both appear, and `Remove` still addresses exactly one.

`docs/protocol-mobile.md` § Reconnect / Backfill semantics keeps naming
`conversation_id` plus `queued_msg_id` as the queue's match-and-replace key, and
that stays correct: `message_id` correlates a queued item with the *client's own
echo*, which is a client-local join, not the reconcile key.

## Concurrency model

No new goroutines, no new locks, no change to any lock hold. `messageID` is
written once inside `EnqueueDelivery`'s existing `q.mu` hold, as part of the same
`queued` literal that already appends under that lock, and is read only inside
`Snapshot`/`SnapshotAll`'s existing `q.mu` holds. It is a value copy at every
boundary — the projections already copy element-wise into freshly allocated
slices, so a consumer still cannot mutate engine state through a snapshot.

The seams are untouched: `DeliverFunc` does not gain the value (see the ticket's
"delivery-side carry is deliberately not here" — nothing would read it, and
`Deliver:` is wired at 46 sites), `ChangeFunc` still carries only `convID`, and
`GiveUpFunc`'s reason is still built only from daemon-generated values.

## Error handling

No new failure mode. The field cannot fail to store (a `string` assignment), and
cannot fail to project (a `string` copy). It cannot fail to marshal: it joins a
payload that already carries an unbounded untrusted `string` in the same array
element, and `broadcast`'s defensive marshal-error branch — which never echoes
the payload or `err.Error()` — is unchanged and still covers it.

No bound is added. The ticket's reasoning holds on inspection: both clients send
a 36-byte UUID, the field rides the same array element as `text`, and `text` is
already unbounded up to the transport's 1 MiB frame ceiling with the
per-conversation backlog cap (`defaultMaxQueuedPerConversation`, 100) as the
memory bound. A `message_id` bound would constrain nothing the frame ceiling and
backlog cap do not already constrain. See the Security review's Network & I/O
finding for the measurement behind that.

## Testing strategy

RED before GREEN in each package. Scenarios, not bodies:

**`internal/protocol`**
- `TestQueueStatePayload_RoundTrip` extended: fixture item 1 carries a real
  UUID-shaped `message_id`, item 2 carries `""`. The positional assertions gain
  the field. The `""` item is the omitempty pin — with `omitempty` the key would
  vanish on re-marshal and `roundTripEnvelope`'s byte comparison reddens — and it
  is simultaneously AC 3's empty-is-legal case at the wire level. One fixture
  edit covers both.

**`internal/msgqueue`**
- `EnqueueDelivery` stores the id and **both** `Snapshot` and `SnapshotAll`
  project it, asserted in the same test so a one-loop implementation fails.
- `Enqueue` yields `MessageID == ""` — the shim passes no id and mints none.
- Verbatim carry: an id with surrounding whitespace, mixed case and non-ASCII
  comes back byte-for-byte. Kills a mutant that trims or normalises.
- Addresses nothing: two messages sharing one `message_id` get distinct
  `queued_msg_id`s, both appear in the snapshot, and `Remove(convID, 1)` removes
  exactly the first.

**`cmd/pyry`**
- `TestToQueueStatePayload`'s table gains the field in its `want` items.
- A both-arms test after `TestQueueState_ComposedDeliveryNeverReachesTheWire`'s
  shape: enqueue through a never-`Run` queue, then assert the live-push arm
  (`toQueueStatePayload(convID, q.Snapshot(convID))`) and the reconcile arm
  (`outstandingQueues(q)()`) carry the **identical** value, and that the value
  equals the string passed in. This is AC 2's whole point — a test exercising
  only the push passes against a `SnapshotAll` that drops the field.

**`internal/relay/handlers`**
- `fakeEnqueuer` records `messageID`; a table asserts `SendMessage` passes
  `p.MessageID` through verbatim, including `""` and a hostile-shaped value
  (newlines, quotes), and that the id is unchanged by the attachment path.

**Gate (§ B2):** `go test -race` on `internal/msgqueue`, `internal/protocol`,
`internal/relay/...`, `cmd/pyry`; `go vet ./...`; `go build ./cmd/pyry`. The
full-module race suite is the verifier's gate, not run here.

## Open questions

1. **Does any existing `protocol.QueuedItem` construction break?** Resolved
   before implementation: the only production construction is
   `toQueueStatePayload`; the two test constructions
   (`internal/relay/v2session_queuereconcile_test.go`) use field names, so a new
   field is additive there. No strict decoder (`DisallowUnknownFields`) exists on
   any `queue_state` path — the two hits repo-wide are in `internal/control` and
   `internal/e2e/realclaude`, neither on this wire.
2. **Does the e2e queue suite assert an exact item shape?** To confirm during
   implementation. `internal/e2e/relay_v2_stream_queue_drain_test.go` reads
   `queued_msg_id` from decoded payloads rather than comparing whole JSON, so an
   additive field should be transparent; if a golden comparison surfaces, it is
   updated in the same commit and recorded under `## Revisions`.
3. **Fixture ordering.** Confirmed, not assumed: `canonical` is `json.Compact`,
   so the fixture must be authored in struct-field order. Recorded in the Design
   above rather than left open.

## Security review

**Verdict:** PASS (first pass FAILED on one MUST FIX; the plan was revised and
re-walked, and the revision is in the Design above.)

**Findings:**

- **[Trust boundaries] MUST FIX — addressed in the revision.** The design puts a
  second client-authored string into two types whose doc comments enumerate
  `Text` as *the* untrusted field: `QueuedMessage` ("Text is untrusted,
  phone-originated transit content") and `QueuedItem` (the same sentence). This
  is #2038's `attachment_id` trap exactly — a blanket provenance sentence is
  scoped to the fields it enumerated, and goes silently false when a new one
  lands beside it. The hazard is sharpened by the chosen field position, which
  seats a client-authored id next to a daemon-authored one where a reader
  grouping by shape concludes both are the daemon's. Not exploitable in this
  diff (nothing reads the field), but it is precisely how a *later* ticket
  acquires a false premise — and #2038's own consumer turned a client string
  into a path component. **Resolution:** the plan now requires both doc comments
  to name `MessageID` as the second untrusted, client-authored field. The
  position stays; the comment, not the adjacency, is the durable signal.
- **[Trust boundaries] No further findings.** There is no untrusted→trusted
  crossing to audit, because the value never becomes trusted: it is stored,
  copied twice, and marshalled, and no branch anywhere reads it (AC 4). The
  boundary is explicit and singular — `SendMessage`'s decode — and every hop
  downstream is a value copy.
- **[Tokens, secrets, credentials] No findings.** `message_id` is not a
  capability, nonce, or secret: it gates nothing, is compared to nothing (so no
  constant-time question), and grants nothing to whoever holds it, because no
  verb accepts one. This is `queued_msg_id`'s ungated-plain-id posture, the
  deliberate opposite of `modal_id`'s unguessable answer-gating nonce (ADR 025
  § Security model). Cross-device visibility is in-model: paired devices are one
  trust domain, and `text` — far more sensitive — already fans out on the same
  array element. A client that mints a meaningful rather than random id
  discloses it only to its own trust domain, and no daemon-side mitigation is
  possible or wanted.
- **[File operations] No findings.** The value never becomes a path component,
  and this is the load-bearing contrast with `attachment_id` (#2038), which did
  and therefore needed canonical-shape validation before use. `message_id`
  reaches no `os.Open`, no directory read, no filename. `msgqueue` has no on-disk
  persistence at all (the package's documented durability boundary), so there is
  no file mode, atomic-write, TOCTOU or symlink question to answer.
- **[Subprocess / external command execution] No findings, and the mechanism is
  structural rather than a rule.** `message_id` never reaches claude's stdin: the
  drain delivers `head.delivery`, and the delivery payload is composed from
  `text` plus resolved attachment paths only. `EnqueueDelivery` stores the id in
  a field `drain` does not read, so a client-chosen string cannot be injected
  into a prompt by omission — the same shape as the `text`/`delivery` split
  itself.
- **[Cryptographic primitives] Not applicable, and the *absence* of minting is
  what makes it so.** The daemon generates nothing here (AC 3 forbids it), so no
  RNG, entropy or primitive choice arises. Worth stating because the natural
  alternative design — "mint an id when the client sends none" — would have
  raised every question in this category and is exactly what AC 3 rules out.
- **[Network & I/O] No findings; the no-bound decision was measured, not waved
  through.** An unbounded client string joins an outbound frame that fans out to
  every interactive connection, which is the right thing to be suspicious of.
  Three checks: (a) *inbound* it is already bounded by the transport's 1 MiB
  frame ceiling, and retained memory is bounded by the existing per-conversation
  backlog cap (`defaultMaxQueuedPerConversation`, 100); (b) *amplification* is
  unchanged — one `send_message` already produces one `queue_state` per
  interactive conn carrying the unbounded `text` on the same array element, so an
  attacker gains strictly nothing by moving bytes into `message_id`; (c) **no
  buffer pinning** — `encoding/json` allocates a fresh string when decoding a
  JSON string value, so the stored field does not retain the whole `Unmarshal`
  input the way a `s[:n]` slice of a capped field would. A bound on this field
  would constrain nothing the frame ceiling and backlog cap do not already
  constrain. No new listener, read path, timeout or TLS surface is introduced.
- **[Error messages, logs, telemetry] No new finding; one pre-existing condition
  named and deliberately untouched.** This change adds **no** log site (AC 4):
  `msgqueue`'s drain warn/hold-debug/give-up warn keep carrying only
  `conversation_id`, `queued_msg_id` and `queued_at`, and `giveUp`'s reason is
  still built from daemon-generated values alone. The emitter's defensive
  marshal-error branch already logs neither the payload nor `err.Error()` —
  precisely so untrusted bytes cannot be quoted into an error — and `message_id`
  inherits that unchanged. **OUT OF SCOPE:** `send_message` already logs
  `p.MessageID` raw at three sites, an arbitrary client string in a
  line-oriented log — the log-injection shape `messaging.go`'s `attachment_id`
  note flags for `filename`. It is pre-existing, this change neither adds a site
  nor alters the value logged, and fixing it is a production edit outside this
  ticket (§ Scope Discipline). Named here so it is on the record as seen, not
  missed.
- **[Concurrency] No findings.** Written once inside `EnqueueDelivery`'s existing
  `q.mu` hold as part of the same `queued` literal that already appends; read
  only inside `Snapshot`/`SnapshotAll`'s existing holds; value-copied at every
  boundary. No new lock, no change to any lock order or hold duration, no
  goroutine, and no TOCTOU — a field nothing reads cannot be checked-then-used.
  Shutdown is unaffected: the field lives and dies with the in-memory record, and
  the package's daemon-restart loss boundary is unchanged.
- **[Threat model alignment] SHOULD FIX — addressed in the revision.**
  `docs/protocol-mobile.md` § Security model puts a user's paired devices in one
  trust domain, and this change stays inside it. But the multi-device rule AC 5
  asks for needs a second half. `message_id` is client-chosen and uniqueness
  across devices is enforced nowhere, so a device that merges a `queue_state`
  item against any local echo — rather than only against echoes **it** minted —
  can attribute another device's queued message to itself on a colliding id. The
  capability consequence is nil (`dequeue_message` is ungated, so any paired
  phone could already drop that item), so this is mis-attribution in the UI, not
  a boundary crossing — hence SHOULD FIX rather than MUST. **Resolution:** the
  plan's doc section now requires both halves of the rule — never drop an
  unmatched item, and merge only against ids the client itself minted. The
  property then holds by construction on the client side, since a client's echo
  store holds only its own.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05
