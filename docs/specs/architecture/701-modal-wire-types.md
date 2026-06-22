# Spec #701 — modal/permission v2 wire types + `modal_id` nonce + `answer_token` idempotency (SSOT)

**Ticket:** #701 — feat(protocol): modal/permission v2 wire types + `modal_id` nonce + `answer_token` idempotency
**Size:** S (PO-sized S, confirmed — 2 production files, 5 new exported types, ~0 consumer cascade; see § Scope)
**Epic:** #597 (Phase 3 — remote modal). Siblings build the runtime on this contract: **#703** (modal control loop — mint/emit + route/validate inbound answer + deny-on-timeout), **#706** (two-heads ownership — stale `modal_id` rejected, first-answer-wins), **#702** (remote-permission answer gate, per-device, default OFF).
**Security-sensitive:** **yes** (label present). Security pass at § Security review (verdict **PASS**) — required before commit per the label gate.
**Template:** #656 (`session_transition` wire vocab → #657 producer). This is the same split shape, the same files, the same v2-only partition discipline — applied to four modal envelopes instead of one boundary marker.

---

## Files to read first

Read these before writing anything. Every addition mirrors an existing precedent in this exact package — copy the precedent, don't invent. (Generated from `codegraph_context` + the reads done during this spec; off-topic hits pruned.)

- `internal/protocol/codes.go:108-160` — **the `request_snapshot`/`screen_snapshot` block (108-125) and the `session_transition` block (143-160).** The snapshot block is the precedent for a **mixed inbound+outbound cluster in one `const` block with one rationale comment** — copy its shape for the four modal constants. The `session_transition` block shows the "wire vocabulary only — producer is sibling #N" closing line.
- `internal/protocol/codes.go:64-85` — the `TypeRekeyRequest` block: the **inbound v2 control** rationale paragraph (intercepted at `dispatchAppFrame` before `dispatch.Route`). `modal_answer`/`modal_cancel` are inbound control; reuse this wording.
- `internal/protocol/snapshot.go` (whole file, 51 lines) — **the closest structural template for the new structs.** A cohesive request/response cluster: header comment scoping the file to "wire vocabulary only", the inbound-control struct (`RequestSnapshotPayload`) with the "no `dispatch.Route` handler — the consumer intercepts it" note, and the outbound struct (`ScreenSnapshotPayload`). The modal structs land in `messaging.go` (per AC), but write their doc comments in this voice.
- `internal/protocol/messaging.go:36-58` — `SessionTransitionPayload`: the most recent v2 payload added to **`messaging.go`** (the AC-pinned home), and the "plain string over a closed wire set, not a named enum" precedent for `class` / `source` / `outcome`.
- `internal/protocol/interactive.go:1-14` — the "no field carries `omitempty`; every field always present so fixtures pin the full shape" rule. Modal payloads follow it (all strings + one slice, never `omitempty`).
- `internal/protocol/compat_test.go:39-56, 100-158` — the three test edit sites: `TestIsV1Compatible` rejection cases (39-56), the `v2OnlyTypes` map (100-112), and `TestTypeConstants_V1V2Partition`'s `all` slice + union-count check (121-158). Four constants → +4 in each of the three.
- `internal/protocol/interactive_test.go:9-28` — the `roundTripEnvelope(t, env, payload, raw)` helper. Reuse it; each modal round-trip test is then ~12 lines.
- `internal/protocol/messaging_test.go:120-204` — `TestSessionTransitionPayload_RoundTrip`: the table + per-field asserts + byte-equal regression-guard pattern. Model the modal tests on it (but one func per type — the four shapes differ too much for one table).
- `internal/protocol/envelope_test.go:11-30` — `canonical(t, b)` and `readFixture(t, name)`. **`canonical` compacts but does NOT sort keys** → author every fixture in struct-field order or the byte-equal check fails.
- `internal/protocol/testdata/session_transition.json` — fixture shape reference (single-line, struct-field order).
- `internal/protocol/envelope.go:111-135` — `v1TypeSet`. **Do NOT add any modal constant here.** The partition test enforces their absence; this is the one file you must not touch.
- `internal/relay/v2session.go:1200-1320` — `dispatchAppFrame` + its type switch (cases at 1212 `TypeRekeyRequest`, 1215 `TypeRequestSnapshot`). **Read-only context:** this is where **#703** will add `case protocol.TypeModalAnswer` / `case protocol.TypeModalCancel`. Confirms the inbound-control interception seam your constants slot into. You do **not** edit this file.
- `docs/protocol-mobile.md:402-435` — § Application message types table; add four rows after the `session_transition` row (434).
- `docs/protocol-mobile.md:487-609` — § Interactive events + § Screen snapshot. The **Screen snapshot section (588-608) is the doc template**: a feature section with mixed-direction `####` subsections + field tables. Add a new `### Modal (v2)` section in this style (recommended placement: after § Screen snapshot, before § Backfill semantics).

---

## Context

Epic #597 Phase 3 puts a **modal over the encrypted mobile wire**: when the supervised `claude` surfaces a modal (a permission prompt, a plan-approval, a tool-confirmation), the daemon describes it to the phone, the phone answers, and the daemon drives that answer back into `claude`. None of that vocabulary exists on the wire yet.

`internal/protocol` is a **stdlib-only leaf-data package** (`envelope.go:1-11`). This ticket therefore defines **types and documented semantics only** — four envelope-type constants, five payload structs, the v2-partition classification, round-trip tests, and the `docs/protocol-mobile.md` SSOT. **No runtime ships here.** The minting of `modal_id` nonces, the dedup of answers by `answer_token`, the inbound-answer validation, and the fan-out gate all live in siblings (#703/#706/#702). This is the wire-vocabulary slice of an established split — identical in shape to #656 (`session_transition` vocab) → #657 (producer).

### Single-path constraint (#597, 2026-06-08)

Single structured path only. No coarse/legacy modal path, no backward-compat. `modal_shown` rides the existing `interactive` capability (#607, `CapabilityInteractive = "interactive"`) negotiated in hello/hello_ack — **receiving/viewing a modal is ungated**. **Answering** is gated separately, per-device, default OFF, in the security model (#702); that gate is *not* a wire capability and is *not* part of this ticket.

---

## Design

Three production-surface additions, all additive, all mirroring an existing precedent in this package. Nothing is dispatched, intercepted, or fanned out — pure leaf-data wire vocabulary.

### 1. `internal/protocol/codes.go` — four type constants (one block)

Add a **single new `const` block at the end of the file** (after the `TypeSessionTransition` block), covering all four modal types — mirroring the **`request_snapshot`/`screen_snapshot` block (108-125)**, which is the precedent for grouping a mixed inbound+outbound cluster under one rationale comment.

```go
const (
	TypeModalShown     = "modal_shown"     // binary → phone, outbound v2 modal-surfaced event
	TypeModalAnswer    = "modal_answer"    // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeModalCancel    = "modal_cancel"    // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeModalDismissed = "modal_dismissed" // binary → phone, outbound v2 modal-resolution event
)
```

The doc comment above the block must state:
- **what the cluster is** — the v2 modal vocabulary for epic #597 Phase 3; `modal_shown` is interactive-capability-gated (#607), answering is gated separately per-device (#702);
- **the two natures** — `modal_shown`/`modal_dismissed` are outbound binary→phone events an old phone must never receive; `modal_answer`/`modal_cancel` are inbound phone→binary **control** envelopes the v2 session manager intercepts at `dispatchAppFrame` before `dispatch.Route` (like `TypeRekeyRequest`/`TypeRequestSnapshot`) — there is no `dispatch.Route` handler;
- **the `MUST NOT be added to v1TypeSet`** paragraph (verbatim shape from the snapshot/`TypeRekeyRequest` comments) — pointing at `compat_test.go`'s partition as the enforcement;
- **the producer is #703** (with #706/#702 building ownership/gating); **this ticket is wire vocabulary only.**

Keep the comment at the density of the existing snapshot block — do not exceed it.

### 2. `internal/protocol/messaging.go` — five payload structs

Add the five structs to **`messaging.go`** (AC-pinned). `messaging.go` is also the architecturally consistent home: it already houses the most recent non-turn-stream v2 payload (`SessionTransitionPayload`), and a modal is a control/boundary concern, not a turn-stream event (so `interactive.go`, scoped to "the turn-event model", is the wrong file). *(A dedicated `modal.go` à la `snapshot.go` was considered and rejected: the AC names `messaging.go`, it adds zero new production files, and it keeps modal payloads beside their `session_transition` sibling. Write the doc comments in `snapshot.go`'s voice regardless.)*

**No field carries `omitempty`** (interactive.go/snapshot.go rule): every field is always present so fixtures pin the full shape and boundary values (empty `default_option_id`, empty `option_id`) don't silently vanish. No `time.Time` field — the envelope's `ts` covers timing — so **no import change** (`messaging.go` already imports `time` for other structs).

**`ModalOption`** (a single ordered option):

| Go field | json tag | Type | Notes |
|---|---|---|---|
| `ID` | `id` | `string` | Stable option identifier; what `modal_answer.option_id` and `modal_shown.default_option_id` reference. |
| `Label` | `label` | `string` | Human-readable display text for the option. |

**`ModalShownPayload`** (binary → phone):

| Go field | json tag | Type | Notes |
|---|---|---|---|
| `ModalID` | `modal_id` | `string` | One-time opaque nonce minted per surfaced modal (by #703). Sole correlation key — see § Security review. |
| `Class` | `class` | `string` | Modal kind, plain string over a closed wire set (e.g. `permission`). Not a named enum (leaf-data convention; matches `MessagePayload.Role`). The exhaustive class set is the producer's (#703) to finalize; the type carries it. |
| `Title` | `title` | `string` | Short modal title. |
| `Prompt` | `prompt` | `string` | The modal's body/question text. |
| `Options` | `options` | `[]ModalOption` | **Ordered.** JSON-array order **is** the canonical display/selection order. |
| `DefaultOptionID` | `default_option_id` | `string` | The `ModalOption.ID` of the default/highlighted option. Documented invariant: MUST equal one of `Options[].ID`. |

**`ModalAnswerPayload`** (phone → binary, inbound control):

| Go field | json tag | Type | Notes |
|---|---|---|---|
| `ModalID` | `modal_id` | `string` | The modal being answered; validated against the daemon's current outstanding `modal_id` by #703/#706. |
| `OptionID` | `option_id` | `string` | The selected `ModalOption.ID`. |
| `AnswerToken` | `answer_token` | `string` | Idempotency key (phone-minted; stable across retries of the same logical answer). Lets the daemon dedup a replayed/reordered `modal_answer` to a no-op (#703). Not a secret — see § Security review. |

**`ModalCancelPayload`** (phone → binary, inbound control):

| Go field | json tag | Type | Notes |
|---|---|---|---|
| `ModalID` | `modal_id` | `string` | The modal to cancel/dismiss from the phone. |

**`ModalDismissedPayload`** (binary → phone):

| Go field | json tag | Type | Notes |
|---|---|---|---|
| `ModalID` | `modal_id` | `string` | The modal that was resolved. |
| `Outcome` | `outcome` | `string` | Resolution: the selected `ModalOption.ID` when answered, or a producer-defined sentinel (cancel/timeout). Plain string; the sentinel vocabulary is #703's, documented not enforced. |
| `Source` | `source` | `string` | What resolved it. **Closed set `{remote, local, timeout}`**: `remote` = a phone `modal_answer`/`modal_cancel`; `local` = answered/cancelled at the desktop TTY; `timeout` = deny-on-timeout fired. Plain string, not a named enum. |

Each struct's doc comment names its direction, the `TypeModal*` it pairs with, the `docs/protocol-mobile.md § Modal` SSOT anchor, and (for `modal_answer`/`modal_cancel`) the "inbound control — no `dispatch.Route` handler; #703 intercepts at `dispatchAppFrame`" note (mirror `RequestSnapshotPayload`).

### 3. `docs/protocol-mobile.md` — the SSOT

Two edits, mirroring how `session_transition` (#656) and the screen-snapshot pair are documented:

- **§ Application message types table** (after the `session_transition` row, ~434): add four rows —
  - `| **`modal_shown`** | binary → phone | no | **New in v2** (interactive, capability-gated). Modal surfaced to the phone (#597 Phase 3). See [Modal](#modal-v2). |`
  - `| **`modal_answer`** | phone → binary | no | **New in v2.** Inbound control — phone answers a modal. See [Modal](#modal-v2). |`
  - `| **`modal_cancel`** | phone → binary | no | **New in v2.** Inbound control — phone cancels a modal. See [Modal](#modal-v2). |`
  - `| **`modal_dismissed`** | binary → phone | no | **New in v2.** Modal resolution notice. See [Modal](#modal-v2). |`
- **New `### Modal (v2)` section** (after § Screen snapshot, before § Backfill semantics), in the screen-snapshot section's style:
  - a short intro: the modal lifecycle (`modal_shown` → `modal_answer`/`modal_cancel` → `modal_dismissed`); `modal_shown` is interactive-capability-gated and **viewing is ungated**, but **answering is gated per-device, default OFF (#702)**;
  - four `####` subsections, each with a direction line + field table (per § Design 2), `modal_answer`/`modal_cancel` carrying the "inbound v2 control — intercepted before `dispatch.Route`; no handler" note like `request_snapshot`;
  - state the **`default_option_id` ∈ `options[].id`** invariant and that **array order is option order**;
  - state the **`source` closed set `{remote, local, timeout}`**;
  - a **"Security & validation contract"** note (inline, like `resync`'s untrusted-input note): `modal_id` is a one-time **opaque, unguessable** nonce minted per surfaced modal — it is the **sole correlation key** (no `conversation_id` on these payloads; the daemon resolves `modal_id` against its own outstanding-modal state, never trusting a phone-asserted conversation). The daemon **rejects** an inbound `modal_answer`/`modal_cancel` whose `modal_id` is not the current outstanding one (#703/#706, first-answer-wins). `answer_token` is a **client-minted idempotency key** (uniqueness/stability matter, secrecy does not) that the daemon uses to collapse replayed/reordered answers. The minting + dedup + validation runtime is **#703/#706**; the answer gate is **#702**.

After editing docs, run `qmd update && qmd embed` (per CLAUDE.md) so the SSOT is searchable.

---

## Data flow

```
claude surfaces a modal (producer #703, future)
        │  mints modal_id nonce; builds ModalShownPayload{modal_id, class, title, prompt, options, default}
        ▼
Envelope{Type: TypeModalShown, Payload}  ← this ticket defines this shape
        │  AEAD-sealed, binary → phone, interactive-capability-gated (#703 fan-out)
        ▼
phone renders modal → user answers → Envelope{Type: TypeModalAnswer, Payload{modal_id, option_id, answer_token}}
        │  inbound; intercepted at v2session.go dispatchAppFrame (#703) — validated vs current modal_id (#706),
        │  answer gate per-device (#702), deduped by answer_token (#703) → tui-driver keystroke
        ▼
Envelope{Type: TypeModalDismissed, Payload{modal_id, outcome, source}}  ← this ticket defines this shape
        │  binary → phone
        ▼
phone clears the modal
```

This ticket owns only the two boxed lines — the wire shapes (plus the inbound shapes). No minting, no transport, no dispatch, no gate.

---

## Concurrency model

None. Pure data types and JSON (de)serialization; no goroutines, channels, or shared state.

## Error handling

None beyond `encoding/json` (un)marshal, which the stdlib handles. `internal/protocol` is a leaf-data package with no failure modes of its own. Forward-compat (unknown-field tolerance) is the envelope-level contract already; nothing new here.

---

## Testing strategy

Four new round-trip tests in **`messaging_test.go`** (one per type — the shapes differ too much for a single table), each templated on `TestSessionTransitionPayload_RoundTrip` and using `roundTripEnvelope` (interactive_test.go). Four single-line fixtures under `testdata/`, authored in **struct-field order** (`canonical` compacts but does not sort keys → json key order must equal Go field order or the byte-equal check fails).

Fixtures (developer may pick literal values; **keep field order**):

- `testdata/modal_shown.json` — ≥2 options to prove ordering; `default_option_id` referencing one of them.
  `{"id":501,"type":"modal_shown","ts":"2026-06-22T10:00:00Z","payload":{"modal_id":"mdl-7f3a","class":"permission","title":"Allow Bash?","prompt":"claude wants to run: rm -rf build/","options":[{"id":"allow","label":"Allow"},{"id":"deny","label":"Deny"}],"default_option_id":"deny"}}`
- `testdata/modal_answer.json` — exercises `option_id` + `answer_token` round-trip.
  `{"id":12,"type":"modal_answer","ts":"2026-06-22T10:00:05Z","payload":{"modal_id":"mdl-7f3a","option_id":"allow","answer_token":"atk-91c2"}}`
- `testdata/modal_cancel.json` — single-field.
  `{"id":13,"type":"modal_cancel","ts":"2026-06-22T10:00:06Z","payload":{"modal_id":"mdl-7f3a"}}`
- `testdata/modal_dismissed.json` — `source:"remote"`, `outcome` = an option id.
  `{"id":502,"type":"modal_dismissed","ts":"2026-06-22T10:00:07Z","payload":{"modal_id":"mdl-7f3a","outcome":"allow","source":"remote"}}`

Test scenarios (bullet form — write in the project's idiom):

- **`modal_shown`:** unmarshal envelope → `Type == TypeModalShown`; unmarshal payload → assert `ModalID`, `Class`, `Title`, `Prompt`; assert `len(Options) == 2` and `Options[0].ID == "allow"`, `Options[1].ID == "deny"` (**order**); assert `DefaultOptionID == "deny"`; then `roundTripEnvelope` byte-equal.
- **`modal_answer`:** `Type == TypeModalAnswer`; assert `ModalID`, `OptionID`, **`AnswerToken`** round-trip (AC-pinned); byte-equal.
- **`modal_cancel`:** `Type == TypeModalCancel`; assert `ModalID`; byte-equal.
- **`modal_dismissed`:** `Type == TypeModalDismissed`; assert `ModalID`, `Outcome`, `Source == "remote"`; byte-equal.

The byte-equal round-trip is the regression detector for the no-`omitempty` discipline (mirror the explanatory comment from `TestSessionTransitionPayload_RoundTrip`).

**Compat tests — three edits in `compat_test.go`:**

- Add the four constants to `v2OnlyTypes` (under a `// v2 modal vocabulary.` comment).
- Add the four to `TestTypeConstants_V1V2Partition`'s `all` slice (same comment). The union-count check `len(v1TypeSet)+len(v2OnlyTypes) == len(all)` self-balances (+4 each).
- Add four rejection cases to `TestIsV1Compatible`'s `cases`: `{"modal_shown-rejected", TypeModalShown, false, ErrUnknownType}` and the analogous three. (An old phone never receives an outbound modal event; an inbound modal control is never a v1 type — all four are not v1-compatible.)

`TestV1TypeSet_CoversAllExportedTypeConstants` (asserts `len(all)==16` over v1-only types) is **unaffected** — no modal type is v1; do not touch that test.

Run `go test -race ./internal/protocol/...`, `go vet ./...`, `gofmt`. (Heads-up: the repo may be gofmt-dirty at HEAD under a newer local Go than CI — check `git show HEAD:<f> | gofmt -l` before "fixing" any file you didn't change; reformatting untouched files sprays spurious diffs. See [[pyrycode-gofmt-dirty-at-head-go1.26]].)

---

## Acceptance criteria → design mapping

1. `codes.go` declares four `Type*` constants (`modal_shown` + `modal_dismissed` daemon→phone; `modal_answer` + `modal_cancel` phone→daemon) in the v2 partition, each carrying the `MUST NOT be added to v1TypeSet` rationale in house style. → § Design 1.
2. `messaging.go` declares the payload structs with the documented field sets (modal_shown: modal_id/class/title/prompt/ordered options[id,label]/default option id; modal_answer: modal_id/option id/answer_token; modal_cancel: modal_id; modal_dismissed: modal_id/outcome/source∈{remote,local,timeout}). → § Design 2.
3. All four classified in `v2OnlyTypes` (never `v1TypeSet`); disjoint-partition + every-constant-classified assertions stay green → deterministically guarantees each is a v2-only event a non-interactive phone is never offered. → § Testing (compat edits).
4. `docs/protocol-mobile.md` documents the four shapes + field semantics; `modal_id` = one-time nonce, `answer_token` = idempotency key for no-op-on-replay (minting/dedup is #703/#706). → § Design 3.
5. A test asserts each payload marshals/unmarshals to its documented wire shape, including `modal_id` and `answer_token` round-tripping. → § Testing (four round-trip tests).

---

## Scope (size self-check)

**Production source files modified** (excluding `*_test.go`, `*.md`, `testdata/*.json`, this spec): **`codes.go`, `messaging.go` = 2.** Well below the ≥5-file gate. **New exported types: 5** (`ModalOption`, `ModalShownPayload`, `ModalAnswerPayload`, `ModalCancelPayload`, `ModalDismissedPayload`) — at the S ceiling (≤5), not over. **New constants: 4.** **Consumer cascade: zero** — additive wire vocabulary, nothing imports or dispatches it yet (the producers are #703/#706/#702; the `dispatchAppFrame` switch they touch is read-only context here). **Reject branches: 0** (leaf data, no state machine). **Total written LOC** (4 const + ~5 structs/tables + 4 round-trip tests + 4 one-line fixtures + 3 small compat edits + ~70 docs lines) ≈ **~265**. Below the ~400 S line and the ~600 split line. No red line tripped — solidly S, the #656 shape scaled to four envelopes.

## Open questions

- **No `conversation_id` on modal payloads.** Per the AC, `modal_id` is the sole correlation key; the daemon hosts one supervised `claude` with a single `CurrentConversation()` cursor and all paired devices are the one operator's (#632 § per-conversation confinement), so a modal belongs to the one active conversation and `modal_id` suffices. If multi-active-conversation modals arrive later, `modal_shown` may need `conversation_id` for client-side routing — revisit in the producer (#703), not here. (Adding it now would be scope creep beyond the AC and a second correlation key the daemon would have to reconcile against `modal_id`.)
- **No deadline/timeout field on `modal_shown`.** Deny-on-timeout is #703's runtime; the wire carries no countdown today (AC omits it). If the phone needs to render a countdown, that's a follow-up field on `modal_shown` — flag to #703.
- **`class` / `outcome` wire-value sets.** Left as plain strings with the producer (#703) owning the exhaustive vocabularies (documented, not enforced — leaf-data convention). Only `source` is pinned to a closed set here `{remote, local, timeout}`, because it is fully determined by the resolution mechanism, not by what modals `claude` happens to surface.

---

## Security review

**Reviewer:** architect (self-review; `agents/architect/security-review.md` is not synced into this worktree — performing the pass inline using the standard adversarial categories, per the #487/#209 precedent).
**Date:** 2026-06-22
**Verdict:** PASS

Run adversarially against the spec above, assuming it has holes. The ticket's stated focus (Technical Notes § "Security pass scope — the shape"): **bless the wire shape — `modal_id` opaqueness, no cross-conversation `modal_id` confusion, `answer_token` sufficiency — before #703 builds minting/dedup/validation on it.** The adversarial question for a vocab-only slice is not "does this code introduce a vuln" (no code runs) but **"does the *shape* make a secure runtime expressible, and does it foreclose a secure runtime anywhere?"**

**Findings:**

- **[Trust boundaries — the inbound surface].** No findings; the shape is correct. `modal_answer`/`modal_cancel` are the new *inbound* (phone→daemon) control surface, and answering a modal is a high-consequence action (it injects a decision — e.g. "Allow Bash `rm -rf`" — into `claude`). The shape keeps the daemon as the sole authority: the payload carries **`modal_id` only**, no `conversation_id`, no option-text, no action verb. The daemon resolves `modal_id` against its **own** outstanding-modal state (the resync/screen_snapshot "daemon's own resolved id; never attacker-derived" discipline) and maps `option_id` → its own recorded option list. A phone can never assert *what* it is answering — only *which* modal and *which* of the daemon's offered options. This makes #706's "stale `modal_id` rejected, first-answer-wins" and #702's per-device gate expressible without any wire change. The interception seam exists and is read-only here: `v2session.go:1200 dispatchAppFrame` (cases for `TypeRekeyRequest`/`TypeRequestSnapshot` at 1212/1215) — #703 adds the modal cases before `dispatch.Route`, so these inbound controls never reach the handler chain. **Enforced by** `compat_test.go`'s partition (all four out of `v1TypeSet`).
- **[Trust boundaries — cross-conversation `modal_id` confusion].** No findings — structurally eliminated. Because `modal_id` is the **sole** correlation key and is resolved daemon-side, there is no `conversation_id`/`modal_id` pair that can *disagree*. Contrast a shape carrying both: a phone could send `{conversation_id: A, modal_id: <B's modal>}` and create an ambiguity the daemon must adjudicate. The single-key shape forecloses that class. The one precondition this pushes onto #703 (named, not a wire concern): `modal_id` must be **globally unique across concurrently-outstanding modals**, not merely unique per conversation — otherwise the single-key resolution is ambiguous. Recorded as a producer obligation; the wire shape supports it (an opaque string is unconstrained in width).
- **[`modal_id` opaqueness / unguessability].** No findings — and correctly **deferred to the producer with the contract pinned here.** The type is an opaque `string`; the wire cannot enforce unguessability (that is a minting property). The spec (§ Design 1, § Design 3, this pass) documents the binding contract: `modal_id` is a **one-time, opaque, unguessable nonce** minted per surfaced modal. #703 MUST mint it from `crypto/rand` (the project's `conversations.NewID()` UUIDv4 precedent is acceptable — a UUIDv4 is an unguessable 122-bit nonce). Guessability matters because an unguessable `modal_id` is a second, independent barrier behind #702's answer gate: even a device that has somehow been shown one modal cannot blind-answer a *different* outstanding modal it was never shown. The string type does not constrain entropy, so the secure choice is available; documenting it is the most this slice can do.
- **[`answer_token` sufficiency].** No findings. `answer_token` is an **idempotency key, not a credential** — its security-relevant properties are *uniqueness* (distinct logical answers → distinct tokens) and *stability* (a retry of the same answer → the same token), **not secrecy**. The shape (a distinct opaque `string` field, separate from `modal_id` and `option_id`) is sufficient for #703 to dedup `(modal_id, answer_token)` and collapse replayed/reordered `modal_answer`s to a no-op. Crucially, the token is **not** the authorization — authorization is `modal_id` validity (#706) + the per-device gate (#702); `answer_token` only deduplicates among *already-authorized* answers. So even a guessed/forged `answer_token` grants nothing: a fresh token on a valid `modal_id` is just a (rejected, because the modal is already resolved or gated) answer; a replayed token is a no-op. This separation of concerns (validity gate vs. dedup key) is exactly right and is documented so #703 does not conflate them.
- **[Output redaction / logs / telemetry].** No findings — N/A in this slice (no code runs, nothing logged). **Named obligation for #703:** `prompt`/`title`/`option label`/`input_summary`-style fields are operator content and MUST follow the #632/#589 logging discipline (never log modal body text; log only `modal_id`, `class`, `source`, `option_id`, `conn_id`, `err`). Flagged so the producer's security pass inherits it; not enforceable on a leaf-data type.
- **[Secrets / credentials].** No findings — N/A. No token, key, or nonce-counter is handled here. `modal_id`/`answer_token` are correlation/dedup identifiers, not credential material; AEAD sealing of these envelopes happens in the transport (#571), not in this package.
- **[Cryptographic primitives].** No findings — none performed here. The only crypto-adjacent obligation (`modal_id` from `crypto/rand`) is the producer's, named above.
- **[File ops / subprocess / network / DoS].** No findings — N/A. No I/O, no path handling, no execution, no sockets, no unbounded allocation introduced by these types. `options` is an unbounded slice on the wire, but it is **daemon-minted outbound** (the daemon describes its own modal), not attacker-supplied inbound — no inbound amplification. (#703 should still bound it when constructing from `claude` output; a wire concern only if it became inbound, which it is not.)
- **[Threat model alignment].** Aligned with `protocol-mobile.md § Security model` and ADR 025. The new inbound control surface (#1) is gated by the same v2 funnel as `rekey_request`/`request_snapshot` (authenticated `V2StateOpen` session, intercepted pre-`dispatch.Route`); the partition test keeps all four off the v1 path; the high-consequence "answer" action is doubly fenced (unguessable `modal_id` + per-device #702 gate), with `answer_token` adding replay-idempotency on top. No threat is *introduced* by the vocabulary; the shape makes every required mitigation expressible and forecloses the cross-conversation-confusion class outright.

**Producer obligations surfaced (carry into #703/#706/#702's specs):** (a) mint `modal_id` from `crypto/rand`, globally unique across concurrently-outstanding modals; (b) reject inbound `modal_answer`/`modal_cancel` whose `modal_id` ≠ the current outstanding one, first-answer-wins (#706); (c) dedup by `(modal_id, answer_token)`; (d) per-device answer gate, default OFF (#702); (e) never log modal body text. None is a wire-shape gap — the contract here supports all five.
