# Spec #638 — Stall vocabulary: internal `turnevent.Stall` + v2 mobile `stall` wire type

**Part of EPIC #596** (Phase 2 structured streaming). Split from #624, child A of 2. The sibling bridge slice (#624 child B) depends on this one and carries the `security-sensitive` label; **this slice does not** — it is pure additive data vocabulary with no trust boundary, no inbound parsing, no fan-out.

**Size: S (held).** Three production `.go` files, ~60 LOC total written work, two new exported types + one exported constant, zero consumer cascade. The S→XS hedge in the ticket body was declined: the production diff is trivial, but the wire ceremony (full-shape fixture + round-trip test + 3-spot compat-partition lockstep + doc in two places + three count-drift comment bumps) spans seven files and is more than an XS. The label does not change dispatch, so S is the honest call.

---

## Files to read first

| Path / range | What to extract |
|---|---|
| `internal/turnevent/event.go:1-18` | Package doc. The "out of scope" line (`internal-only events (BusyState, Stall, …) … get a home in a later ticket`) is the line to amend — remove `Stall`, keep `BusyState`. |
| `internal/turnevent/event.go:22-27` | `Event interface{ isTurnEvent() }` + the variant enumeration in its doc comment (add `Stall`, annotated internal-only). |
| `internal/turnevent/event.go:78-92` | The exact pattern to mirror: value-receiver `isTurnEvent()` block + the `var ( _ Event = …{} )` assertion block. |
| `internal/turnevent/event_test.go:46-81` | `eventKind` type-switch (has a `default`, so adding a variant is non-breaking) + `TestEvent_StreamTypeSwitch`. Optional: add a `Stall` case so the switch stays exhaustive-by-intent. |
| `internal/protocol/interactive.go:1-25` | File header — the **no-`omitempty`** invariant — and `TurnStatePayload` (lines 22-25), the exact peer to mirror (`conversation_id` only, plus one field; `StallPayload` drops the second field). |
| `internal/protocol/codes.go:87-105` | The v2 interactive partition const block. Add `TypeStall = "stall"`; bump the trailing comment "these **five** live in the latter" → "**six**". |
| `internal/protocol/interactive_test.go:9-53` | `roundTripEnvelope` helper + `TestTurnStatePayload_RoundTrip` — the template for `TestStallPayload_RoundTrip`. Reuses `readFixture` / `canonical` (already defined in the package's test files). |
| `internal/protocol/compat_test.go:27-58` | `TestIsV1Compatible` rejection cases (add a `stall-rejected` case). |
| `internal/protocol/compat_test.go:93-143` | `v2OnlyTypes` map (add `TypeStall: true`) and `TestTypeConstants_V1V2Partition`'s `all` list (add `TypeStall` to the v2-interactive group). The union-count assertion `len(v1TypeSet)+len(v2OnlyTypes) == len(all)` balances automatically once both grow by one. |
| `internal/protocol/testdata/turn_state.json` | The fixture shape to mirror — a single-line envelope `{"id":…,"type":…,"ts":…,"payload":{…}}`. |
| `docs/protocol-mobile.md:419-425` | Message-types table, interactive rows — add a `stall` row. |
| `docs/protocol-mobile.md:466-514` | "Interactive events (v2, capability-gated)" section — bump intro "These **five** envelope types" → "**six**", add a `#### stall` subsection. |
| `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` | ADR-025 rationale: internal model is ~90% ACP-shaped; an internal-only event (a stall) is *dropped* by the future ACP adapter and *sent* by the mobile adapter. Background only — no code change here. |

---

## Context

`tui-driver` raises a one-shot `stall_detected` signal (`EventKindStallDetected`, no payload, no clearing edge — shipped in tui-driver v1.3.0), and the mobile UI already knows how to surface a stall (#373). The signal has nowhere to live in Pyrycode's daemon: the neutral internal turn-event model (#606) explicitly deferred the `Stall` variant, and the v2 mobile wire (#607) added only the five ACP-shaped turn events.

This ticket adds the **data vocabulary** for a stall in two layers — the neutral internal model and the v2 mobile wire — with **no bridge wiring**. It compiles and tests standalone; the bridge that actually carries `stall_detected` → `Stall` → `stall` to the phone is #624's child B, which depends on this slice.

**Onset-only — no recovery edge** (resolved at architect stage, carried into implementation). The producer is a one-shot rising-edge marker with no payload and no clearing event. So `Stall` is **onset-only**: do NOT model a "cleared" state — there is no producer for it. The phone self-clears on the next turn activity.

---

## Design

Two layers, mirroring the two layers their five-event predecessors already occupy. The internal layer carries **no** conversation identity (consistent with every existing `turnevent` variant — the bridge injects `conversation_id` at wire-mapping time); the wire layer carries `conversation_id` only.

### Layer 1 — internal neutral model (`internal/turnevent/event.go`)

`Stall` is a new **empty marker struct** added to the sealed `Event` sum:

```
type Stall struct{}            // onset marker; no fields, no clearing state
func (Stall) isTurnEvent() {}
var _ Event = Stall{}          // add to the existing assertion block
```

- **Empty struct is deliberate.** The producer's `stall_detected` carries no payload, and — like `TextChunk`, `TurnEnd`, etc. — the internal variant carries no `conversation_id` (the bridge knows which conversation it is draining and injects identity when mapping to the wire). Onset-only ⇒ no fields to carry.
- **It stays a first-class member of the same `Event` sum.** The sum is the stable internal contract; per-variant emit/drop decisions belong to the *adapters*, not the sum. The mobile adapter **sends** `Stall`; the future ACP adapter (#600) **drops** it (no ACP equivalent). This is exactly the asymmetry #606's package doc anticipated ("internal-only events (BusyState, Stall, …)").
- **Doc edits (two spots in `event.go`):**
  1. Package doc (lines 13-17): remove `Stall` from the "out of scope … get a home in a later ticket" list; **keep `BusyState`** there. Optionally note that `Stall` is internal-only (mobile-sent, ACP-dropped).
  2. `Event` interface doc (lines 22-26): extend the variant enumeration to include `Stall`, annotated as internal-only so a reader does not assume it maps to ACP.

### Layer 2 — v2 mobile wire (`internal/protocol`)

**`codes.go`** — add to the v2-only interactive partition const block (append after `TypeTurnEnd` for uniform ordering across every parallel list):

```
TypeStall = "stall"
```

Bump the block's trailing comment "these **five** live in the latter" → "**six**".

**`interactive.go`** — add `StallPayload`, the peer of `TurnStatePayload` minus the `state` field:

```
type StallPayload struct {
    ConversationID string `json:"conversation_id"`   // no omitempty (file invariant)
}
```

Doc comment should state: wire form of the internal-only `turnevent.Stall`; conversation identity only; onset-only (no clearing field); **no `turn_id`** — like `turn_state`, a stall is a coarse conversation-level signal, not turn-scoped; the bridge (#624-B) supplies `conversation_id` because the internal `Stall` marker carries none.

### Layer 3 — protocol doc (`docs/protocol-mobile.md`)

1. **Message-types table** (~line 423): add a row in the interactive group, after `turn_end`:
   `| **`stall`** | binary → phone | no | **New in v2** (interactive, capability-gated). |`
2. **Interactive events section** (line 468): bump "These **five** envelope types" → "**six**".
3. **`#### stall` subsection** after the `turn_end` subsection (after line 514): one-field table (`conversation_id`), plus one sentence that `stall` is the wire form of an internal-only signal (no ACP equivalent) and, like `turn_state`, carries no `turn_id`.

### Ordering invariant (state it for the developer)

`stall` appends **at the end of the interactive group** in every parallel structure — `codes.go` const block, `interactive.go` struct order, the three `compat_test.go` lists, and the doc (table row + subsection). Uniform append order keeps the five-becomes-six edit mechanical and prevents a misplaced compat-partition entry.

### Count-drift checklist (easy-to-miss comment/string bumps)

These are the spots where a literal "five" or a length assertion must move in lockstep — call them out so none is skipped:

- `codes.go` partition-block comment: "these five" → "six".
- `docs/protocol-mobile.md` intro: "These five envelope types" → "six".
- `compat_test.go` `v2OnlyTypes` map: +`TypeStall` (the `TestTypeConstants_V1V2Partition` union assertion rebalances automatically — no hand-counted literal there).
- `compat_test.go` `TestV1TypeSet_CoversAllExportedTypeConstants`: **unchanged** — its `len(all)==16` covers v1 types only; `stall` is v2-only and must NOT be added to that list or to `v1TypeSet`.

---

## Concurrency model

None. Pure value types and a JSON struct — no goroutines, no channels, no shared state, no I/O. (`internal/protocol` is a stdlib-only leaf data package and does **not** import `internal/turnevent`; the two layers are decoupled, bridged only by #624-B at string value.)

---

## Error handling

None to design. There are no failure modes: no constructor, no validation, no inbound parsing. The only behavioral contract is serialization shape, which the round-trip test and the no-`omitempty` invariant pin. The compat drift detector enforces that `stall` is correctly classified as v2-only (an old phone never receives it).

---

## Testing strategy

Bullet scenarios — the developer writes them in the package's existing idiom (table-driven where natural; reuse `roundTripEnvelope`, `readFixture`, `canonical`).

**`internal/protocol/interactive_test.go` — `TestStallPayload_RoundTrip`** (mirror `TestTurnStatePayload_RoundTrip`):
- Read `testdata/stall.json`; unmarshal the envelope; assert `env.Type == TypeStall`.
- Unmarshal payload into `StallPayload`; assert `ConversationID == "c1"`.
- Call `roundTripEnvelope` to prove the *decoded struct* re-marshals byte-equal to the fixture (this is what pins the `json` tag and the no-`omitempty` shape).

**`internal/protocol/testdata/stall.json`** — full-shape single-line fixture, same skeleton as `turn_state.json`:
`{"id":…,"type":"stall","ts":"…","payload":{"conversation_id":"c1"}}`. One field, present and non-empty.

**`internal/protocol/compat_test.go`:**
- `TestIsV1Compatible`: add a rejection case `{"stall-rejected", TypeStall, false, ErrUnknownType}` — an old phone must never receive `stall`.
- `v2OnlyTypes`: add `TypeStall: true`.
- `TestTypeConstants_V1V2Partition`: add `TypeStall` to the v2-interactive group of `all`; the disjoint-partition and union-count assertions then prove `stall` is classified v2-only and in exactly one set.

**`internal/turnevent/event_test.go`** (optional, recommended): add a `case Stall: return "stall"` to `eventKind` and a `Stall{}` entry to the `TestEvent_StreamTypeSwitch` stream, so the type-switch stays exhaustive-by-intent. The AC for the internal layer is satisfied by the variant + marker method + `var _ Event = Stall{}` assertion alone (compile-time); this test addition is for intent-documentation, not correctness.

**Build/verify:** `go build ./... && go test -race ./internal/turnevent/... ./internal/protocol/...` plus `go vet ./...`. No race surface, but keep `-race` per project convention.

---

## Open questions

None blocking. Two judgment calls already resolved in this spec, recorded so the developer does not re-litigate:

1. **`Stall` is an empty struct, not a `{ConversationID}` carrier.** Internal `turnevent` variants never carry conversation identity; the bridge injects it. (If a future producer ever attaches onset detail, that is a new ticket — do not speculatively add fields now; evidence-based design.)
2. **`stall` lives in the "Interactive events" doc section** alongside the five ACP-shaped events, even though it is internal-only. On the wire it is just another v2 capability-gated event; the ACP-vs-internal distinction is an *adapter* concern, invisible to the phone. The subsection's one-line note records that `stall` has no ACP equivalent and no `turn_id`.

---

## Acceptance criteria → deliverables

- [ ] **Internal `Stall` variant** — `internal/turnevent/event.go`: empty marker struct `Stall`, `isTurnEvent()` method, `var _ Event = Stall{}` assertion; package doc's "out of scope" line drops `Stall` (keeps `BusyState`); `Event` interface doc enumeration extended (annotated internal-only). Onset-only, no clearing state.
- [ ] **Wire `stall` type** — `internal/protocol`: `TypeStall = "stall"` constant in the v2-only interactive partition (`codes.go`); `StallPayload{ConversationID}` with no `omitempty` (`interactive.go`); `testdata/stall.json` full-shape fixture; `TestStallPayload_RoundTrip` re-marshalling the decoded struct (`interactive_test.go`); `compat_test.go` updated — `TypeStall` in `v2OnlyTypes` and the partition `all` list, plus an `IsV1Compatible` rejection case. Count-drift comments bumped (five → six in `codes.go`).
- [ ] **Doc** — `docs/protocol-mobile.md`: a `stall` row in the application-message-types table; a `#### stall` subsection under "Interactive events (v2, capability-gated)"; intro count bumped five → six.

*(No knowledge-base doc AC: `docs/knowledge/codebase/638.md` is the documentation phase's deliverable, written from this spec + the merged diff — not the developer's.)*
