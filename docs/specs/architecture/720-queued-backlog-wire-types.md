# Spec #720 — v2 `queue_state` + `dequeue_message` wire types (SSOT)

**Ticket:** #720 — feat(protocol): queued-backlog v2 wire types — `queue_state` + `dequeue_message` (SSOT)
**Size:** S (PO-confirmed; 2 production files, 3 new exported types, 2 new constants — see § Scope)
**Split from:** #705. Sub-issue of epic #597 (Phase 3). Consumed by producer **#722** / handler **#723** / `send_message` routing **#721** (all `security-sensitive`).
**Security-sensitive:** no (label absent). Wire vocabulary only — no producer, no inbound handler, no trust boundary, no nonce/token. `queued_msg_id` is a plain per-conversation counter from `internal/msgqueue` (contrast #701's `modal_id` nonce, which is why #701 *was* labelled). The trust binding (convID → authorized-conversation), the capability-gated fan-out, and the live inbound handler accepting an untrusted phone frame all live in the labelled siblings #722/#723/#721.

This slice mirrors **#656** (`session_transition`) and **#701** (modal) — the established type-slice rhythm. Where they diverge, this spec follows #701: like the modal cluster, this slice ships an **outbound** type (`queue_state`, like `modal_shown`) *and* an **inbound** v2-control type (`dequeue_message`, like `modal_answer`/`modal_cancel`), and one payload carries a **nested struct array** (`queued []QueuedItem`, exactly like `options []ModalOption`).

---

## Context

Epic #597 Phase 3 (split from #705) adds a queued-message backlog: a phone that types while claude is busy has its turn buffered in `internal/msgqueue` (the engine — #704, extended #719) and drained one-at-a-time on turn-end. The phone needs to **see** that backlog and **cancel** an entry it no longer wants:

- **View** — the daemon pushes `queue_state` (daemon → phone) = `{conversation_id, queued:[{queued_msg_id, text, ts}]}` (ADR 025 line 118), the wire form of `msgqueue.Snapshot(convID)`.
- **Cancel** — the phone sends `dequeue_message` (phone → daemon) = `{conversation_id, queued_msg_id}` (ADR 025 line 126), driving `msgqueue.Remove(convID, id)`.

ADR 025 (`docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md`) fixes both shapes and the trust model: **viewing and dequeuing are an ungated capability for any paired phone** — only *answering permission-class modals* is gated (§ Security model, line 143). This is the notable contrast with the modal cluster (`modal_answer` is gated per-device; `dequeue_message` is not).

The engine already exists. **This slice adds only the wire vocabulary** that the producer (#722) and handler (#723) consume — no producer, no handler, no live queue, no fan-out, no policy. Same SSOT-first rhythm as #656/#701: define the type, round-trip it, partition it v2-only, document it.

### Why no `turnevent` / `turnbridge` neutral hop (mechanism decision)

The original split body proposed neutral-model types (`QueueState`/`DropQueued`) routed through `turnevent` → `turnbridge.MapEvent`. **This spec declines them**, per the ticket's Technical Notes and a re-read of the code:

- Queue backlog is daemon **state**, not part of claude's **turn stream**. It follows the `session_transition`/`modal` precedent (producer builds the `protocol.*Payload` directly from engine state; inbound frame intercepted at `internal/relay/v2session.go`'s `dispatchAppFrame` and decoded to the `protocol.*Payload`) — **neither direction routes through `turnevent` or `turnbridge`**. The `turnevent → turnbridge.MapEvent` path is reserved for tui-driver turn-stream events (`assistant_delta`, `tool_use`, `turn_end`, `stall`, permission).
- The original AC's "`turnbridge` decodes `dequeue_message` → `DropQueued`, exhaustive decoder" does **not** match the codebase: `turnbridge` has no inbound wire-frame decoder (`mapper.go` maps tui-driver `Event` → `turnevent.Event`; `outbound.go`'s `MapEvent` maps `turnevent.Event` → wire payload). Inbound v2 control frames are intercepted at `dispatchAppFrame`; that inbound decode + handling is **#723's** job, not this slice's.

So the firm deliverable is exactly the wire vocabulary below. **No `turnevent`/`turnbridge` edits.** (If a future producer slice does want neutral types, the homes are: outbound `QueueState` → the `turnevent.Event` sum, inbound `DropQueued` → the `turnevent.Inbound` sum — they are two opposite-direction sealed sums, not one; do not put `DropQueued` on `Event`. That is out of scope here.)

---

## Design

Two production-surface additions, both additive, both mirroring an existing precedent. Nothing is dispatched, wired, or fanned out — pure leaf-data wire vocabulary.

### 1. `internal/protocol/codes.go` — the type constants

Add a **new `const` block at the end of the file** (after the modal block, ~193), mirroring the modal cluster's "two natures in one cluster" shape. The doc comment above the block must state:

- (a) **what they are** — epic #597 Phase 3 queued-backlog vocabulary; the lifecycle (`queue_state` daemon→phone view, `dequeue_message` phone→daemon cancel) backed by `internal/msgqueue` (`Snapshot` / `Remove`);
- (b) **two natures in one cluster** — `queue_state` is an *outbound* binary → phone event an old phone must never receive; `dequeue_message` is an *inbound* phone → binary **control** envelope the v2 session manager intercepts at `internal/relay/v2session.go`'s `dispatchAppFrame` before `internal/dispatch.Route` (like `TypeModalAnswer` / `TypeRequestSnapshot`) — there is **NO** `dispatch.Route` handler for it;
- (c) the **trust contrast** — unlike `modal_answer`, dequeuing is **ungated** for any paired phone (ADR 025 § Security model); no nonce, no per-device gate;
- (d) the **MUST NOT be added to `v1TypeSet`** paragraph (verbatim shape from the modal comment) — the drift detector in `compat_test.go` partitions `Type*` constants between `v1TypeSet` and `v2OnlyTypes`; these two live in the latter;
- (e) the producer is **#722** / the handler is **#723**; this ticket (#720) is wire vocabulary only.

Contract (constant values fixed by ADR 025 verbatim):

```go
const (
	TypeQueueState      = "queue_state"      // binary → phone, outbound v2 queued-backlog snapshot
	TypeDequeueMessage  = "dequeue_message"  // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)
```

Keep the comment density at or below the modal block's; do not pad.

### 2. `internal/protocol/messaging.go` — the payload structs

Add **3 new exported types** at the end of the file (after the modal cluster, ~162), with a cluster header comment in the established style (wire vocabulary only; the producer #722 / handler #723 own minting, validation, and fan-out; no `omitempty` — every field always present so the testdata fixtures pin the full shape).

**Placement: `messaging.go`, not `interactive.go`** — `interactive.go` is scoped to "the wire representation of … turn-event model"; a queue snapshot is daemon state, not a turn event. `messaging.go` houses the `time.Time` and nested-array precedents this needs.

#### `QueuedItem` — one element of the `queued` array

The wire form of `msgqueue.QueuedMessage` (the producer #722 maps `QueuedMessage.ID` → `QueuedMsgID`). Named for its role in the array (the `ModalOption` precedent), not after the engine type, to keep it wire-scoped.

| Go field | json tag | Type | Notes |
|---|---|---|---|
| `QueuedMsgID` | `queued_msg_id` | `uint64` | Stable per-conversation counter (≥ 1, monotonic), from `msgqueue`. JSON number, **not** a string/nonce. No `omitempty`. |
| `Text` | `text` | `string` | The queued message text. **Untrusted, phone-originated transit content** (see § Security note). No `omitempty`. |
| `TS` | `ts` | `time.Time` | Enqueue timestamp, RFC3339Nano per the envelope timestamp rule. No `omitempty`. |

#### `QueueStatePayload` — body of a `TypeQueueState` envelope (binary → phone)

| Go field | json tag | Type | Notes |
|---|---|---|---|
| `ConversationID` | `conversation_id` | `string` | The conversation this backlog belongs to. The daemon's own resolved id (#722); never attacker-derived. No `omitempty`. |
| `Queued` | `queued` | `[]QueuedItem` | Ordered backlog, FIFO/enqueue order (the `options []ModalOption` precedent). No `omitempty` — see the nil-slice note below. |

#### `DequeueMessagePayload` — body of a `TypeDequeueMessage` envelope (phone → binary)

Inbound v2 **control** envelope, structurally like `ModalAnswerPayload` / `RequestSnapshotPayload`: intercepted at `dispatchAppFrame` before `dispatch.Route`; there is **NO** `dispatch.Route` handler. The doc comment must say so.

| Go field | json tag | Type | Notes |
|---|---|---|---|
| `ConversationID` | `conversation_id` | `string` | The conversation to dequeue from. **Untrusted phone input** — the handler (#723) is responsible for resolving it to an authorized conversation; this slice only defines the shape. No `omitempty`. |
| `QueuedMsgID` | `queued_msg_id` | `uint64` | The id to remove (`msgqueue.Remove(convID, id)`). No `omitempty`. |

**`queued` nil-slice rendering (firm contract + one Open question).** `[]QueuedItem(nil)` marshals to JSON `null`, a non-nil empty slice to `[]`. The *type* cannot force non-nil. This slice's contract is: **`queued` is always present** (no `omitempty`), and the round-trip test exercises the N≥1 case the AC names. Whether an *empty* backlog emits `[]` or `null` is the producer's (#722) call — see § Open questions; document the recommendation in the docs (§ Design 3) but do not encode an enforcement the leaf type can't carry.

### 3. `docs/protocol-mobile.md` — the SSOT

Two edits, mirroring the modal (#701) entries:

- **§ Application message types table** (after the `modal_dismissed` row, ~438): add two rows —
  - `| **`queue_state`** | binary → phone | no | **New in v2** (interactive, capability-gated). Queued-message backlog snapshot (#597 Phase 3). See [Queue](#queue-v2). |`
  - `| **`dequeue_message`** | phone → binary | no | **New in v2.** Inbound control — phone cancels a queued message. See [Queue](#queue-v2). |
- **New § Queue (v2)** subsection (place after § Modal, line ~659, before § Backfill semantics, ~661), mirroring § Modal's structure:
  - intro paragraph: what the backlog is, the `queue_state` (view) / `dequeue_message` (cancel) pair, and — the key contrast with Modal — **both viewing and dequeuing are ungated for any paired phone** (ADR 025 § Security model); there is no per-device gate and no nonce. Note this section is wire vocabulary only; the producer/handler runtime is #722/#723.
  - `#### queue_state` — direction binary → phone (outbound; not in `v1TypeSet`); field table (`conversation_id`, then `queued` as `array of {queued_msg_id, text, ts}`); note `queued` is **always present** and **recommend** the producer emit `[]` (not `null`) for an empty backlog so the mobile decoder stays simple; producer is #722.
  - `#### dequeue_message` — direction phone → binary (inbound v2 control; intercepted before `dispatch.Route`, no handler); field table (`conversation_id`, `queued_msg_id`); note it is ungated and that resolving `conversation_id` to an authorized conversation + applying the removal is the handler's (#723) job. *When* the daemon emits `queue_state` and *how* the handler applies `dequeue_message` is documented by #722/#723, not here.

After editing docs, run `qmd update && qmd embed` (per CLAUDE.md) so the SSOT is searchable.

---

## Data flow

```
msgqueue.Snapshot(convID) []QueuedMessage          dequeue: phone sends dequeue_message
        │ producer #722 maps → QueueStatePayload            │  intercepted at dispatchAppFrame (#723)
        ▼                                                   ▼  decode → DequeueMessagePayload
Envelope{Type: TypeQueueState, Payload}  ← this        msgqueue.Remove(convID, id)  ← #723
        │ AEAD-sealed, binary → phone,                       (this slice defines only the
        ▼ interactive-capability-gated                        DequeueMessagePayload shape)
mobile decode → queued-backlog view
```

This ticket owns only the two boxed wire shapes. No emitter, no transport, no dispatch, no engine call.

---

## Concurrency model

None. Pure data types and JSON (de)serialization; no goroutines, channels, or shared state.

## Error handling

None beyond JSON (un)marshal, which the `encoding/json` stdlib handles. `internal/protocol` is a leaf data package with no failure modes of its own. The AC's "malformed `dequeue_message` rejected cleanly (returns an error, no panic)" is exactly stdlib `json.Unmarshal` behaviour: a `queued_msg_id` that is not a JSON number (e.g. a string) fails to unmarshal into the `uint64` field and returns a `*json.UnmarshalTypeError` — no panic. The test asserts this; no new code path is added. Forward-compat (unknown-field tolerance) is already the envelope-level contract.

## Security note (informational — no enforcement in this slice)

`QueuedItem.Text` and `DequeueMessagePayload.ConversationID` are **untrusted, phone-originated** content. This slice stores neither and inspects neither — it only defines the wire shape. The discipline that matters downstream (never log `Text`; resolve `ConversationID` to an authorized conversation before acting) lives in the producer/handler (#722/#723), which carry the `security-sensitive` label. Recorded here so the consumer-slice authors inherit the contract.

---

## Testing strategy

Three new tests in `messaging_test.go`, plus three compat edits in `compat_test.go`. Run `go test -race ./internal/protocol/...`, `go vet ./...`, `gofmt`.

### Round-trip tests (`messaging_test.go`)

Templated on `TestModalShownPayload_RoundTrip` (nested array) and `TestModalAnswerPayload_RoundTrip` (inbound control); both reuse `roundTripEnvelope` from `interactive_test.go`.

- **`TestQueueStatePayload_RoundTrip`** — driven by `testdata/queue_state.json` (N=2 queued items, per the AC's "N queued items"). Unmarshal envelope → `Type == TypeQueueState`; unmarshal payload → assert `ConversationID`, `len(Queued) == 2`, and per-item `QueuedMsgID` / `Text`; compare each item's `TS` via **`.Equal`** (never `==` / `reflect.DeepEqual` — monotonic-clock + RFC3339Nano discipline); then byte-equal round-trip via `roundTripEnvelope`. Byte-equality is what catches an accidental `omitempty` re-introduction — mirror the explanatory comment from the modal round-trip tests.
- **`TestDequeueMessagePayload_RoundTrip`** — driven by `testdata/dequeue_message.json`. Unmarshal → assert `ConversationID` and `QueuedMsgID`; byte-equal round-trip.
- **`TestDequeueMessagePayload_Malformed`** — inline a malformed payload (no fixture needed), e.g. `queued_msg_id` as a JSON string. Assert `json.Unmarshal` into `DequeueMessagePayload` returns a **non-nil error** and does not panic. (Write as a small table if the developer prefers covering a second malformed shape, e.g. `queued_msg_id` as `null` into a non-pointer `uint64`.)

### Fixtures (`testdata/`)

Author both in **struct-field order** (`canonical()` compacts, does not sort keys). Developer may pick the literal values; keep the field order and a fixed RFC3339Nano `ts`.

- `testdata/queue_state.json` — example:
  `{"id":601,"type":"queue_state","ts":"2026-06-23T10:00:00Z","payload":{"conversation_id":"conv-1","queued":[{"queued_msg_id":1,"text":"first queued","ts":"2026-06-23T09:59:58Z"},{"queued_msg_id":2,"text":"second queued","ts":"2026-06-23T09:59:59Z"}]}}`
- `testdata/dequeue_message.json` — example:
  `{"id":602,"type":"dequeue_message","ts":"2026-06-23T10:00:01Z","payload":{"conversation_id":"conv-1","queued_msg_id":2}}`

### Compat tests (`compat_test.go`) — three edits

- Add `TypeQueueState: true,` and `TypeDequeueMessage: true,` to the `v2OnlyTypes` map (under a `// v2 queue vocabulary.` comment).
- Add both constants to `TestTypeConstants_V1V2Partition`'s `all` slice (under the same `// v2 queue vocabulary.` comment). The union-count check `len(v1TypeSet)+len(v2OnlyTypes) == len(all)` self-balances (+2 to `v2OnlyTypes`, +2 to `all`).
- Add two rejection cases to `TestIsV1Compatible`'s `cases`: `{"queue_state-rejected", TypeQueueState, false, ErrUnknownType}` and `{"dequeue_message-rejected", TypeDequeueMessage, false, ErrUnknownType}`.

`TestV1TypeSet_CoversAllExportedTypeConstants` (asserts `len(all)==16` over v1-only types) is **unaffected** — neither new constant is a v1 type; do not touch that test or `v1TypeSet`.

> **gofmt heads-up:** the repo may be gofmt-dirty at HEAD under a newer local Go than CI ([[pyrycode-gofmt-dirty-at-head-go1.26]]). Check `git show HEAD:<f> | gofmt -l` before "fixing" any file you didn't change; reformatting untouched files sprays spurious diffs and fights CI.

---

## Acceptance criteria → design mapping

1. v2 wire types exist (`queue_state` carrying `{conversation_id, queued:[{queued_msg_id, text, ts}]}`; `dequeue_message` carrying `{conversation_id, queued_msg_id}`), json field names verbatim per ADR 025. → § Design 1 (constants) + § Design 2 (`QueueStatePayload`, `QueuedItem`, `DequeueMessagePayload`).
2. Both partitioned **v2-only**: not in `v1TypeSet`, in `v2OnlyTypes`; `dequeue_message` never reaches the v1 handler chain (inbound v2 control, intercepted pre-`dispatch.Route` — documented, enforced by the partition); `compat_test.go` drift detector stays green. → § Design 1 (codes.go comment) + § Testing (compat edits) + envelope.go untouched.
3. Round-trip + malformed: `queue_state` with N items round-trips byte-for-byte (`time.Time` via `.Equal`); `dequeue_message` round-trips; malformed `dequeue_message` rejected cleanly (error, no panic). → § Testing (three tests, two fixtures) + § Error handling.
4. `docs/protocol-mobile.md` documents both shapes (schema + direction); behaviour (when emitted / how applied) deferred to #722/#723. → § Design 3.

---

## Scope (size self-check)

Production source files modified (excluding `*_test.go`, `*.md`, `testdata/*.json`, this spec): **`codes.go`, `messaging.go` = 2.** Below the ≥5-file gate. New exported types: **3** (`QueueStatePayload`, `QueuedItem`, `DequeueMessagePayload`) — below 5. New constants: 2. No consumer call-site cascade — additive wire vocabulary; nothing dispatches or imports it yet (producer #722, handler #723). Total written LOC (two-constant block + three structs + cluster comments + two round-trip tests + one malformed test + two ~1-line fixtures + 3 small compat edits + ~30 docs lines) ≈ 150. Solidly S. No red line tripped.

**File-overlap (architect-time branch check):** the only flagged sibling branch touching `internal/protocol/codes.go` was `feature/449`, whose issue is **CLOSED** and whose work (`TypeRekeyRequest`) already landed on `main` via a different path — a stale branch (last touched 2026-05-17, main 530 commits ahead) that will never merge. **False positive; no live overlap; no block set.** No other in-flight branch touches `messaging.go`, `compat_test.go`, or `docs/protocol-mobile.md`.

## Open questions

- **Empty-backlog `queued` rendering (`[]` vs `null`).** The leaf type cannot force a non-nil slice, so `QueueStatePayload{Queued: nil}` marshals to `"queued":null`. Resolve in the producer (#722): recommend it pass a non-nil empty slice so an empty backlog emits `[]`, keeping the mobile decoder's `queued` field a plain (possibly empty) array rather than a nullable one. Documented as a recommendation in § Queue (v2); not enforced here. If #722's design finds `null` acceptable to the mobile client (#429), no change is needed — the wire type round-trips both.
- **`queued_msg_id` width on the wire.** Kept `uint64` to match `msgqueue.QueuedMessage.ID` exactly (JSON number; ids ≥ 1, monotonic per conversation). The mobile client (#429) must decode it as an integer, not a string. If mobile needs a string id, that is a cross-repo contract change to raise on #429 — not a Go-side change here.
