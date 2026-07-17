# Spec #1065 — scope `modal_shown` per conversation

**Ticket:** [#1065](https://github.com/pyrycode/pyrycode/issues/1065) — split from #1063 (itself split from #1050). This is the **confidentiality-scoping half**; the detection/emission half (making a minted per-conversation session raise the modal at all) is sibling **#1066**, which this **blocks**. Land this first so that when #1066 turns on multi-conversation modal broadcast, every `modal_shown` is already confidentiality-scoped.
**Size:** S. 4 production files (`internal/protocol/messaging.go`, `internal/modalbridge/modal.go`, `cmd/pyry/interactive_modal_v2.go`, `cmd/pyry/interactive_modal_stream_v2.go`). This is the #1062-shaped change (`turn_state` scoping) applied to the modal payload: a **stamp**, not new daemon-side per-conn gating.
**Security-sensitive:** **Yes** (label present). The modal body carries a tool title / input summary; mis-scoping fans conversation A's content to a conn viewing conversation B on the internet-exposed relay. The `## Security review` section is appended last, per `architect/security-review.md`.

> **Sizing note — read before you start.** Production is ~20 LOC across 4 files; the work is dominated by a **mechanical signature cascade**: `Record` gains a `convID` param (14 call sites, 3 test files) and `Handle` gains a `convID` param (15 call sites, 1 test file). Both are trivial `""`-arg threads except at the two production stamp sites and inside the new scoping test. **Execute the cascade first, mechanically** (§ "Complete edit inventory" lists every site) so it does not become turn-1 discovery cost — then write the two substantive new tests. Do not restructure the existing tests or introduce a `Record` helper; thread the arg and move on.

---

## Files to read first

- `internal/protocol/messaging.go:77-98` — `ModalShownPayload` struct + its doc comment. **Add the field here; the doc's "there is no conversation_id" clause (line ~85) becomes false and must be revised** (see § Design 5).
- `internal/protocol/messaging.go:26-54,170-178,196-224` — `SessionTransitionPayload` / `QueueStatePayload` / `SessionErrorPayload`: the sibling payloads that already lead with `ConversationID string json:"conversation_id"` (no omitempty). **Copy that field placement and tag exactly.**
- `internal/modalbridge/modal.go:75-82` — `Outstanding` struct. **Add `ConversationID string` here** so `Snapshot` can re-stamp it.
- `internal/modalbridge/modal.go:138-164` — `Record`: the single mint+build+store site. **Add the `convID` param; stamp `p.ConversationID` and store it in `Outstanding` in one op** (atomic).
- `internal/modalbridge/modal.go:188-214` — `Snapshot`: rebuilds a payload per `Outstanding`. **Add `ConversationID: o.ConversationID`.** This is what makes AC3 (reconcile replay) free.
- `internal/modalbridge/modal.go:216-233` — `buildPayload`: builds the content-only payload. **Leave it unchanged** — `Record` stamps the routing keys (`ModalID`, now `ConversationID`), `buildPayload` owns content only. Do not thread `convID` here.
- `cmd/pyry/interactive_modal_v2.go:80-139` — `Handle` + `handleModalShown`: the surfacer entry and the `Record` call site. **Add `convID` to both signatures; pass it to `Record`.** `handleModalHidden` does not use it.
- `cmd/pyry/interactive_modal_stream_v2.go:76-112` — `boundScreenText`: already reads `active.CurrentConversation()` once to pick the screen host. **Change it to return `(convID, screen)` from that single read** — this is the single-read guarantee (§ Design 4).
- `cmd/pyry/interactive_modal_stream_v2.go:130-157` — `runModalStream`: the sole caller of `emitter.Handle`. **Thread the `convID` from `boundScreenText` into `Handle`.**
- `cmd/pyry/interactive_turn_v2.go:135-160` — the sibling turn emitter's `Handle`: reads the cursor once and uses that single value for both content attribution and the `turn_state` stamp. This spec applies the same single-read discipline to the modal path (screen host + stamp from one read).
- `docs/specs/architecture/1062-fan-turn-state-per-conversation.md` — the direct mirror (turn_state scoping). Read § "Cross-conversation scoping (AC3 — assertion, not new design)" — the client-side-filter model this change extends.
- `internal/relay/v2session_modal.go:290-362` — `reconcileModals`: marshals each `OutstandingModals()` payload **verbatim** into the replay envelope. **Read-only — it needs no change.** Because it replays the stored `Snapshot()` payload byte-for-byte, stamping `Snapshot` (above) scopes replay identically to initial delivery for free.
- `internal/protocol/messaging_test.go:234-276` — `TestModalShownPayload_RoundTrip`. **Add a `payload.ConversationID` assertion** and update the golden.
- `internal/protocol/envelope_test.go:11-18` — `canonical` uses `json.Compact` (whitespace strip, **not** key sort). **The golden fixture's key order must match struct declaration order** — put `"conversation_id"` first in `modal_shown.json` because the field is declared first.
- `internal/relay/v2session_modalreconcile_test.go:16-95` — `sampleModalPayload` + `reconciledModals`: the AC3 reconcile harness. **Give `sampleModalPayload` a `conversation_id` and assert the reconciled payload carries it.**
- `cmd/pyry/interactive_modal_v2_test.go` — the hermetic emitter test file. Home of the 15 `e.Handle` call sites (cascade) and where the new concurrent-conversation scoping test lands.

---

## Context

Today the daemon broadcasts every outbound interactive event to **all** interactive conns; per-conversation scoping is **client-side**, keyed off a `conversation_id` stamp on the payload. `TurnStatePayload` and `QueueStatePayload` both carry `conversation_id`; **`ModalShownPayload` does not** — it carries only `ModalID`/`Class`/`Title`/`Prompt`/`Options`/`DefaultOptionID`. `ActiveConn` is `{ConnID, Interactive}` — there is no per-conn "viewing conversation" state, and `assistant_delta`/`turn_state` already broadcast every conversation's content to all interactive conns, relying on the client-side filter.

With concurrent per-conversation sessions (the world #1066 turns on) each able to raise a modal, that asymmetry is the leak: a `modal_shown` carrying conversation A's tool title / input summary reaches a conn viewing conversation B, whose client has no `conversation_id` to filter on. This change adds the missing stamp so the modal payload joins the same client-side scoping model as its siblings. It is a **stamp-and-filter**, not new daemon-side per-conn gating — the daemon still fans to every interactive conn; the stamp is what confines display to the following client.

---

## Design

### Principle

A modal belongs to the conversation whose bound session raised it. The modal stream **follows the active conversation** (`resolveTarget` + `active.watch()`), so when a bound session surfaces a permission/trust prompt, `active.CurrentConversation()` is precisely that conversation — the same value `boundScreenText` already reads to pick which host's screen to render. The fix stamps that value onto the payload and stores it with the outstanding modal, so both initial delivery and reconnect replay carry it.

### 1. Add the wire field (`internal/protocol/messaging.go`)

Add to `ModalShownPayload`, declared **first** (leading, matching `TurnStatePayload`/`QueueStatePayload`/`SessionErrorPayload`), plain string, **no omitempty** (every field is always present so the golden pins the full shape):

```
ConversationID string `json:"conversation_id"` // outbound routing/scoping key; daemon-asserted, client filters on it
```

Declaration order is load-bearing for the golden roundtrip — see § 6.

### 2. Store + stamp atomically (`internal/modalbridge/modal.go`)

- `Outstanding` gains `ConversationID string`.
- `Record` gains a third param `convID string`. It stamps `p.ConversationID = convID` alongside the existing `p.ModalID = id`, and records `ConversationID: convID` in the `Outstanding`. **One op** — there is never a window where the stored modal lacks its scope (this is what keeps AC3's replay safe; see § Concurrency).
- `Snapshot` adds `ConversationID: o.ConversationID` to each rebuilt payload.
- `buildPayload` is **untouched**: it owns content (`Class`/`Title`/`Prompt`/`Options`/`DefaultOptionID`); `Record` owns the identity/routing stamps (`ModalID`, `ConversationID`). Keeping `convID` out of `buildPayload` mirrors how `ModalID` is stamped in `Record`, not `buildPayload`.

`Record`'s contract (signature + one-line behavior, not a body):

```
func (r *Registry) Record(req turnevent.PermissionRequest, wireClass, convID string) (protocol.ModalShownPayload, error)
// mints modal_id; builds the content payload; stamps modal_id + conversation_id; stores the Outstanding; returns the stamped payload. RNG failure is the only error.
```

### 3. Thread `convID` to the stamp site (`cmd/pyry/interactive_modal_v2.go`)

- `Handle(ctx, ev, convID, screenText)` — `convID` added; passed to `handleModalShown`.
- `handleModalShown(ctx, ev, convID, screenText)` — calls `e.reg.Record(req, class, convID)`.
- `handleModalHidden` — unchanged; it resolves by the tracked `outstandingID` and broadcasts `modal_dismissed` (which carries no `conversation_id`), so `convID` is irrelevant to it. `Handle`'s `EventKindPtyModalHidden` arm passes `convID` through but ignores it.

The emitter is **not** given a cursor reader (unlike the turn emitter). The turn emitter reads the cursor inside `Handle` because its content comes from the event; the modal emitter's content (the rendered screen) is resolved **outside** the emitter in `boundScreenText`, so the scoping key must come from the **same** read that picked the screen — which means it is threaded in as a parameter, not read independently. Reading it independently inside the emitter would be a **second** read of the cursor and could diverge from the screen's read (screen from A, stamp from B → the exact leak). See § 4.

### 4. Single-read guarantee (`cmd/pyry/interactive_modal_stream_v2.go`)

`boundScreenText` today returns `func() string` and internally reads `active.CurrentConversation()` once to select the bound host whose screen it renders. Change it to return `func() (convID, screen string)` from that **same single read**:

- `convID == ""` (pre-route bootstrap) → `("", bootstrapScreen)`.
- `convID != ""`, bound host resolves + satisfies `screenSnapshotter` → `(convID, hostScreen)`.
- host miss / not a snapshotter (defensive) → `(convID, "")`.

`runModalStream` then threads both into `Handle`:

```
case tuidriver.EventKindPtyModalShown:
    convID, screen := screenFor()          // ONE active.CurrentConversation() read
    emitter.Handle(ctx, ev, convID, screen)
case tuidriver.EventKindPtyModalHidden:
    emitter.Handle(ctx, ev, "", "")        // Hidden ignores both
```

**Why this matters (the security core):** the screen host and the stamp now derive from a **single** `active.CurrentConversation()` read, so the content the modal carries and the conversation it is stamped for **cannot diverge** — they are consistent by construction. A design where the emitter re-reads the cursor would open a nanosecond TOCTOU in which conversation A's screen is stamped conversation B. `boundScreenText`'s single read closes that window deterministically. (Rename the closure variable to `screenFor` at the call site for readability; the function name may stay `boundScreenText` — update its doc comment to note it now returns the paired convID.)

`runModalStream`'s param type changes from `func() string` to `func() (string, string)`; its sole caller is `startInteractiveModalStreamV2`.

### 5. Reconcile replay is free (`internal/relay/v2session_modal.go` — no change)

`reconcileModals` marshals each `m.cfg.OutstandingModals()` payload verbatim into the replay envelope; the production seam is `modalReg.Snapshot` (`cmd/pyry/relay.go:482`). Because § 2 makes `Snapshot` stamp `ConversationID`, a reconnecting conn's replayed `modal_shown` carries the same scoping key as the initial broadcast, with **zero** change to `reconcileModals`. This is why AC3 costs no fifth production file — always trace the replay path: it replays the stored payload verbatim, so stamping the stored payload scopes replay for free.

### 6. Anti-forgery clarification (doc revision — required, not optional)

`ModalShownPayload`'s doc comment currently states the modal has *no* `conversation_id` and that "the daemon resolves `ModalID` against its own outstanding-modal state and never trusts a phone-asserted conversation." Adding an **outbound** `conversation_id` makes the literal "there is no conversation_id" clause false. Revise the comment to state:

- `ConversationID` is an **outbound routing/scoping key**: the daemon asserts it (from `active.CurrentConversation()`) so interactive clients filter display by conversation. It is not attacker-derived.
- The **inbound anti-forgery model is unchanged**: `ModalAnswerPayload` / `ModalCancelPayload` still carry **no** `conversation_id`; an answer is authorized by resolving its `ModalID` server-side against the registry (plus the per-device gate #702). Adding an outbound scoping key does **not** let a phone assert which conversation an answer belongs to. Frame the new field explicitly as outbound-only so the change is not read as loosening the inbound guarantee.

### Data flow

```
tui-driver EventKindPtyModalShown on the bound (active) session
        │
        ▼  runModalStream: convID, screen := screenFor()   ◄── ONE active.CurrentConversation() read
        │                                                       (screen host + stamp from the same value)
        ▼  emitter.Handle(ctx, ev, convID, screen)
        │
        ├─ handleModalShown → reg.Record(req, class, convID)
        │        └─ stamps payload.ConversationID = convID; stores Outstanding.ConversationID = convID  (atomic)
        ├─ broadcastInteractive → modal_shown{conversation_id: convID}  fans to every interactive conn
        │        └─ client filters by conversation_id  (scoping = the stamp, not daemon per-conn gating)
        │
        └─ (reconnect) reconcileModals → Snapshot() → modal_shown{conversation_id: convID}  ◄── same key, verbatim
```

---

## Concurrency model

Unchanged from the frozen modal surfacer. `Handle` / `handleModalShown` run only on `runModalStream`'s single goroutine; the emitter's unguarded fields (`nextID`, `outstandingID`, `outstandingClass`) stay single-goroutine. The one shared field is the modal `Registry`, which carries its own leaf mutex; `Record` now writes `Outstanding.ConversationID` under that same lock in the same critical section as the rest of the entry — no new lock, no new field crossing a goroutine boundary.

`active.CurrentConversation()` is read exactly **once** per surfaced modal (in `boundScreenText`), on the stream goroutine. The cursor writer is the router goroutine; the single read means screen-selection and stamp observe the identical value, so a concurrent cursor flip can only move the **next** modal to the new conversation — never split one modal's screen and stamp across two conversations.

---

## Error handling

No new failure modes. `Record`'s only error path stays crypto/rand failure (caller drops the modal — never pushes an id-less/scope-less payload). A `convID == ""` is **not** an error: it is the pre-route bootstrap case, where a single bootstrap session exists and no concurrent per-conversation sessions can be leaked to (see § Security review, Trust boundaries). `Snapshot`/`reconcileModals` marshal paths are unchanged; the added string field cannot fail to marshal.

---

## Testing strategy

Developer writes assertions in the project idiom (table-driven, stdlib only); scenarios below.

### AC1 — field + golden roundtrip (`internal/protocol`)

- Update `internal/protocol/testdata/modal_shown.json`: add `"conversation_id":"<id>"` **as the first key** (declaration order; `canonical` = `json.Compact`, not key-sort, so order must match).
- `TestModalShownPayload_RoundTrip`: add a `payload.ConversationID == "<id>"` assertion. The existing `roundTripEnvelope` byte-equality check then also guards field ordering.

### AC2 + AC4 — concurrent-conversation hermetic emitter test (`cmd/pyry/interactive_modal_v2_test.go`)

New test driving **two** conversations through the single emitter and asserting the stamp never crosses:

- Drive a permission modal for conversation A: `e.Handle(ctx, permEvent, "conv-A", "screen A")`. Assert every fanned `modal_shown` carries `conversation_id == "conv-A"`.
- Drive a second permission modal for conversation B: `e.Handle(ctx, permEvent, "conv-B", "screen B")` (resolve A first via a Hidden, or use a fresh emitter per conv — mirror the existing `TestModalEmitter_*` setup). Assert its `modal_shown` carries `conversation_id == "conv-B"` and **no** conn received a B-stamped payload for A's modal or vice-versa.
- **RED/GREEN boundary:** the genuine RED is the **unwired stamp** — with the field added but `Record`/`Handle` not threading `convID` (stamp `""`), the assertion `conversation_id == "conv-A"` fails. It goes GREEN once § 2/§ 3 thread the value. (Against literal `main` the field does not exist, so the test does not compile there — the field itself is part of the fix; note this in the test's comment rather than pretending it runs on `main`.)
- Reuse `newModalEmitterTestDeps` / `decodeModalShown` / `pushesFor` already in the file.

### AC3 — stored-payload scoping (two levels)

- **modalbridge unit** (`internal/modalbridge/modal_test.go`): `Record(req, class, "conv-X")` then `Snapshot()` → assert the returned payload carries `ConversationID == "conv-X"`. Proves the stored payload (not just the live one) is scoped.
- **relay reconcile** (`internal/relay/v2session_modalreconcile_test.go`): give `sampleModalPayload` a `conversation_id` and assert `reconciledModals(...)[modalID].ConversationID` equals it — proves replay to a reconnecting conn carries the scoping key identically to initial delivery.

### Full suite

`go test -race ./...`, `go vet ./...`, `staticcheck ./...`. Known flakes per `[[known-test-flakes]]` (realclaude SIGTERM, wssclient -race) are re-run, not treated as regressions.

---

## Complete edit inventory (the signature cascade — execute mechanically, first)

**`Record` gains `convID` (3rd param).** Existing tests do not assert scoping → pass `""`; the production site passes the real convID:

| File | Sites | Edit |
|---|---|---|
| `internal/modalbridge/modal.go` | 1 (def) + `Snapshot`/`Outstanding` | signature + stamp + store + Snapshot field |
| `internal/modalbridge/modal_test.go` | 11 (`reg.Record(req, class)` ×9; `reg.Record(permReq, permClass)`; `reg.Record(trustReq, trustClass)`) | append `, ""` (9 collapse via one `replace_all` of `reg.Record(req, class)`; 2 variants by hand) + the new AC3 Snapshot test |
| `cmd/pyry/modal_resolve_v2_test.go` | 2 (`reg.Record(req, wireClass)`) | append `, ""` (`replace_all`) |
| `internal/relay/v2session_modalreconcile_test.go` | 1 (`reg.Record(req, wireClass)`, ~line 316) | append `, ""` (this test uses `sampleModalPayload`, not `Record`, for its scope assertion) |
| `cmd/pyry/interactive_modal_v2.go` | 1 (`e.reg.Record(req, class)`, line 104) | pass real `convID` |

**`Handle` gains `convID` (before `screenText`).** Existing tests pass `""`; the new scoping test passes real ids:

| File | Sites | Edit |
|---|---|---|
| `cmd/pyry/interactive_modal_v2.go` | 2 (`Handle` def + `handleModalShown` def/call) | signature + thread |
| `cmd/pyry/interactive_modal_v2_test.go` | 15 (`e.Handle(...)`) | insert `""` before the screen-text arg (Hidden calls become `e.Handle(ctx, ev, "", "")`) + the new AC2/AC4 test uses real ids |

**Single-read plumbing:**

| File | Edit |
|---|---|
| `cmd/pyry/interactive_modal_stream_v2.go` | `boundScreenText` → `func() (convID, screen string)`; `runModalStream` param `func() (string, string)` + thread convID into both `Handle` arms |

**Wire + golden:**

| File | Edit |
|---|---|
| `internal/protocol/messaging.go` | `ConversationID` field (first) + doc revision (§ 6) |
| `internal/protocol/testdata/modal_shown.json` | `"conversation_id"` first key |
| `internal/protocol/messaging_test.go` | `ConversationID` roundtrip assertion |

---

## Open questions

- **Empty `conversation_id` on the wire pre-route.** A modal raised before any conversation routing (bootstrap) is stamped `""`. This is correct: there are no concurrent per-conversation sessions yet, so there is nothing to leak to, and it mirrors how the bootstrap screen is already selected. #1066 (which mints per-conversation sessions) never surfaces a modal on an unrouted cursor, so post-#1066 every modal carries a non-empty id. Left as-is; no defensive drop.
- **Client-side filtering is out of scope.** "Never fans to a conn viewing a different conversation" is realized by the client filtering on `conversation_id` (the mobile app), exactly as it already does for `turn_state`/`assistant_delta`. The daemon's obligation — and what the hermetic test verifies — is that the stamp is correct. No daemon-side per-conn gating is introduced (consistent with `ActiveConn` staying `{ConnID, Interactive}`).

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The new `ConversationID` is a **daemon-asserted outbound** value, sourced from `active.CurrentConversation()` (`cmd/pyry/interactive_modal_stream_v2.go` `boundScreenText`), never from phone input. It crosses no untrusted→trusted boundary on the way out. The single-read design (§ Design 4) is the boundary control: screen host and stamp derive from **one** cursor read, so the content and its scope label cannot diverge — the specific confidentiality property the ticket exists to enforce is guaranteed by construction, not by timing.
- **[Trust boundaries — inbound, the explicit non-regression]** No MUST FIX. Adding an outbound `conversation_id` must not be read as weakening the inbound anti-forgery model. Verified: `ModalAnswerPayload`/`ModalCancelPayload` (`internal/protocol/messaging.go:113-127`) carry **no** `conversation_id`; `handleModalAnswer`/`handleModalCancel` (`internal/relay/v2session_modal.go:167-209`) resolve the inbound `ModalID` server-side against the registry. A phone still cannot assert which conversation an answer targets. § Design 6 makes the spec revise the now-false doc clause so a future reader does not mistake the outbound key for an inbound-trusted one — this doc revision is a **MUST** part of the change, not cosmetic.
- **[Error messages, logs, telemetry]** No MUST FIX. `conversation_id` is a non-secret routing id and is safe to log as a discriminant (the codebase already logs it: `queue_state`/`dequeue` handlers). The modal **body** (`Title`/`Prompt`/`Options`) remains application content and is still NEVER logged — this change adds no log statement that emits the body, and the existing content-free log discipline in `handleModalShown`/`reconcileModals` is untouched. Developer must not add a log line that echoes the payload when threading `convID`.
- **[Concurrency]** No MUST FIX. `Record` writes `Outstanding.ConversationID` inside the existing registry critical section in the **same op** as the rest of the entry — there is no window where a stored modal is Snapshot-able without its scope key, so a reconnect reconcile (`reconcileModals`, on the relay Run goroutine) can never replay a scope-less payload. The scoping key is read once per modal on the single stream goroutine (§ Concurrency). No new lock, no new cross-goroutine field, no lock-ordering change.
- **[Cryptographic primitives]** N/A — no key/nonce/RNG touched. `ModalID` minting (`crypto/rand`, `newModalID`) is unchanged; `conversation_id` is a plaintext routing id, not a credential, and is not compared against any secret.
- **[Tokens, secrets, credentials]** N/A — `conversation_id` is not a token or secret; it is a display-routing key already present in plaintext on sibling payloads. No storage, rotation, or revocation surface is introduced.
- **[File operations] / [Subprocess] / [Network & I/O]** N/A — this change adds one struct field and threads a value; it opens no file, spawns no process, and adds no socket read. Envelope size/timeout discipline is inherited unchanged from the v2 session transport.
- **[Threat model alignment]** Addresses the ADR-025 § Security model confidentiality property for the modal payload: an interactive event carrying application content must be scoped to the conversation it belongs to so it is not displayed by a client viewing another. This closes the modal-payload gap relative to `turn_state`/`queue_state`, which already carry `conversation_id`. Client-side enforcement (the app filtering on the stamp) is named out of scope and owned by the mobile client, consistent with the existing `turn_state`/`assistant_delta` model.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-17
