# Spec — #607: v2 interactive wire types + capabilities negotiation in hello/hello_ack

**Part of EPIC #596 (Phase 2 structured streaming).** Sibling tickets: #606 (neutral
internal turn-event model — merged; the source these envelopes represent), #608
(event-stream bridge — the consumer that maps internal events → these envelopes → push).

**Scope, held to the line:** wire **vocabulary only.** Pure protocol structs + their
(de)serialization, five new `Type*` constants in the v2-only partition, and an additive
`capabilities` field on the handshake. **No bridge logic, no push, no capability
enforcement, no validation, no dispatch.** Every trust decision (intersecting the phone's
advertised set with the daemon's supported set, handling malformed advertisements,
capability-gated fan-out) lives in #608. This ticket is the data layer #608 builds on.

---

## Files to read first

Generated from the package surface + ADR 025; pruned to what this change touches.

- `internal/protocol/messaging.go:5-50` — **the per-type template.** `SendMessagePayload` /
  `MessagePayload` / `BackfillSincePayload`: json-tagged struct + doc comment pointing at the
  `docs/protocol-mobile.md §`. Mirror this exactly for the five new payloads.
- `internal/protocol/handshake.go:25-41` — `HelloClientPayload` (lines 25-32) and
  `HelloAckPayload` (lines 37-41): the two structs that gain the `capabilities` field.
  Note the `Token string json:"token,omitempty"` precedent (line 31) — the byte-identical lever.
- `internal/protocol/codes.go:36-85` — two const blocks. Lines 36-62 = v1 types (in `v1TypeSet`).
  Lines 70-85 = the **v2-only** block holding `TypeRekeyRequest`. The five new `Type*` constants
  join the v2-only partition (a new sibling const block, see Design).
- `internal/protocol/envelope.go:80-125` — `IsV1Compatible` (91-99) returns `ErrUnknownType`
  for any type not in `v1TypeSet`; `v1TypeSet` literal (108-125). **Do not add the new types here.**
- `internal/protocol/compat_test.go:50-119` — `TestV1TypeSet_CoversAllExportedTypeConstants`
  (50-71, unaffected — touches only v1) and `TestTypeConstants_V1V2Partition` (91-119) with the
  test-local `v2OnlyTypes` map (80-82). The partition drift-check that must keep passing.
- `internal/protocol/compat_test.go:8-48` — `TestIsV1Compatible`: the rejection-cases table
  (27-47) where the five `ErrUnknownType` assertions land.
- `internal/protocol/messaging_test.go:11-43` — the round-trip test shape (fixture decode → field
  asserts → re-marshal → canonical byte-equal). Mirror for `interactive_test.go`.
- `internal/protocol/handshake_test.go:52-141` — `TestHelloClientPayload_RoundTrip` (52-99) and
  `TestHelloAckPayload_RoundTrip` (101-141): the existing fixture round-trips that MUST keep
  passing unchanged (byte-stability regression guard for omitempty).
- `internal/protocol/envelope_test.go:11-27,127-184` — `canonical` / `readFixture` helpers (11-27)
  used by every test; `TestRoutingEnvelope_TokenOmitempty` (127-155) and `CloseCodeOmitempty`
  (157-184) — the **programmatic omitempty pattern** to copy for the capabilities tests.
- `internal/protocol/testdata/hello_client.json`, `hello_ack.json`, `message.json` — fixture
  shape and value conventions. The two hello fixtures stay **byte-identical** (no edits).
- `internal/turnevent/taxonomy.go:34-43` — `TurnEndReason` (`end_turn` / `max_tokens` /
  `max_turn_requests` / `refusal` / `cancelled`). The `turn_end.stop_reason` wire field carries
  **these exact strings** so #608 maps via `string(reason)`. **Do not import this package** (see
  Design § no-import rule).
- `internal/relay/auth.go:100-104` and `internal/relay/v2session.go:688-691` — the only two
  production `HelloAckPayload{...}` construction sites. Both **keyed** literals → the additive
  field needs zero changes here. Listed so you can confirm, not edit.
- `docs/protocol-mobile.md:396-440` — "Application message types" table (396-420) + the
  `### hello (v2-specific note)` per-type section style (422-440). The doc amendment target.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md:102-130` —
  § Wire-protocol extension: the authoritative field lists for the five types + capability
  negotiation.

---

## Context

ADR 025 chose **v2-additive + capability negotiation** (§ Decision 2): the Noise transport and
the `noise_msg` frame are untouched; interactive events are additive application message types,
gated by a capability the phone advertises at handshake. An old phone keeps getting the coarse
`message` fan-out; an `interactive` phone gets the structured stream. This ticket lands the
wire vocabulary — the structs, their (de)serialization, the type constants, and the handshake
field. The producer side (mapping `turnevent` events → these envelopes, the push, the
intersection-of-capabilities trust decision) is #608.

The five payloads are the mobile adapter's wire representation of #606's neutral turn-event
model (`TextChunk` / `ThoughtChunk` / `ToolStart` / `ToolUpdate` / `TurnEnd`).

---

## Design

### 1. Five payload structs — `internal/protocol/interactive.go` (new)

One new file, mirroring `messaging.go`'s style: a json-tagged struct per type, each with a doc
comment naming its `Type*` constant, direction (binary → phone), and `docs/protocol-mobile.md §`.

Contract (field name → json tag → Go type). **No `omitempty` on any field** — every field is
always present on the wire so the fixture pins the full shape, and `seq:0` / `is_error:false`
must not vanish:

```
TurnStatePayload      { ConversationID string `json:"conversation_id"`; State string `json:"state"` }
AssistantDeltaPayload { ConversationID; TurnID string `json:"turn_id"`; Seq int `json:"seq"`; Text string `json:"text"` }
ToolUsePayload        { ConversationID; TurnID; ToolUseID string `json:"tool_use_id"`; Name string `json:"name"`; InputSummary string `json:"input_summary"` }
ToolResultPayload     { ConversationID; TurnID; ToolUseID; IsError bool `json:"is_error"`; ResultSummary string `json:"result_summary"` }
TurnEndPayload        { ConversationID; TurnID; StopReason string `json:"stop_reason"` }
```

Field-type decisions:

- **`State string`** (turn_state), allowed values `"thinking" | "responding" | "idle"`: plain
  string, **no named-constant enum.** Follows the deliberate `MessagePayload.Role` precedent
  (`messaging.go:13-18`: role-like enums stay string-typed). Document the three values in the
  doc comment. #608 picks the exact event→state mapping.
- **`Seq int`** (assistant_delta): a per-turn, non-negative delta-ordering counter. `int` matches
  the package's count-field idiom (`BackfillDonePayload.Delivered`, `BackfillSincePayload.MaxMessages`).
  Not `uint64` (that's reserved for the session-monotonic `Envelope.ID`); this seq resets per turn.
- **`StopReason string`** (turn_end): carries the `turnevent.TurnEndReason` string values verbatim
  (`end_turn` / `max_tokens` / `max_turn_requests` / `refusal` / `cancelled`). Plain string,
  **`internal/protocol` does NOT import `internal/turnevent`** — protocol is a leaf data package
  (only stdlib: `encoding/json`, `errors`, `time`), and the Role-is-string precedent applies.
  #608 produces the field via `string(turnevent.TurnEnd.Reason)`; the wire-value/taxonomy
  alignment is documented, not enforced by a shared type.

ADR 025's base `turn_end` shape is `{conversation_id, turn_id}`; this ticket extends it with
`stop_reason` per the title (the "spec follows the code, amended per implementing ticket"
pattern, ADR 025 § Consequences). The doc amendment (§ below) is part of that.

### 2. Type constants — `internal/protocol/codes.go` (modify)

Add a **new const block** in the v2-only region (a sibling to the existing `TypeRekeyRequest`
block, lines 70-85), with a doc comment marking them v2-only additive application events that
MUST NOT enter `v1TypeSet`:

```
TypeTurnState      = "turn_state"
TypeAssistantDelta = "assistant_delta"
TypeToolUse        = "tool_use"
TypeToolResult     = "tool_result"
TypeTurnEnd        = "turn_end"
```

Keep them in their own block (not merged into the `TypeRekeyRequest` block) so the comment can
distinguish: `rekey_request` is a v2 **control** envelope intercepted before `dispatch.Route`;
these five are v2 **application events** (outbound binary → phone, never dispatched inbound).
Both are "v2-only" for the partition's purpose.

### 3. Capability field + vocabulary — `internal/protocol/handshake.go` (modify)

Add to **both** payloads, as the last field, with `omitempty` (the byte-identical lever):

```
// on HelloClientPayload (phone's advertisement) and HelloAckPayload (daemon's supported set)
Capabilities []string `json:"capabilities,omitempty"`
```

`omitempty` drops a nil OR empty slice → the `capabilities` key is **absent, not `null`** →
the existing `hello_client.json` / `hello_ack.json` fixtures round-trip byte-identically. This
exactly mirrors the `Token` precedent (`handshake.go:31`).

Per ADR 025 § Wire-protocol extension, only the **client** `hello` (`HelloClientPayload`) and
`hello_ack` (`HelloAckPayload`) carry capabilities. The binary's server-role `hello`
(`HelloServerPayload`) is **not** in this flow — leave it untouched.

Add one vocabulary constant (pure wire vocabulary, **not** enforcement — the daemon's
intersection logic is #608's):

```
CapabilityInteractive = "interactive"
```

This is the single home for the `"interactive"` string so the round-trip test and #608
reference a constant, not a literal. It is in-scope ("the wire vocabulary for that") and adds
no trust decision. Place it in `handshake.go` next to the field that carries it.

### Why no other types

`screen_snapshot`, `modal_*`, `queue_state`, `stall_detected`, and the phone→binary control
verbs (`modal_answer`, `interrupt`, …) are explicitly **out of scope** — other #596 children
and Phase 3 (#597). Adding them here would breach the S boundary and front-run undesigned trust
surfaces. Five types + the handshake field, nothing more.

### Dependency / data-flow note

`internal/protocol` stays a pure leaf data package: no I/O, no imports beyond stdlib. The two
production `HelloAckPayload{...}` sites (`auth.go:100`, `v2session.go:688`) are keyed literals —
the additive field compiles unchanged with zero edits. No consumer cascade.

---

## Concurrency model

None. Pure data types; no goroutines, no shared state, no context plumbing. (Stated explicitly
because the package contract is "no I/O, no concurrency" — `envelope.go:1-11`.)

---

## Error handling

No new error paths in production code. `json.Unmarshal` of a malformed payload is the consumer's
concern (#608 / the dispatcher), not this layer. The one behavioural contract this ticket
asserts is negative: `IsV1Compatible` returns `ErrUnknownType` for each of the five new types
(they are not in `v1TypeSet`) — pinned by test, not new code.

---

## Testing strategy

stdlib `testing` only, table-driven where it fits, reusing `canonical` / `readFixture`
(`envelope_test.go:11-27`).

### `internal/protocol/interactive_test.go` (new) — five round-trip tests

One per type, mirroring `TestSendMessagePayload_RoundTrip` (`messaging_test.go:11-43`). Each:
- read the fixture; assert `env.Type` == the matching `Type*` constant;
- unmarshal `env.Payload` into the payload struct; assert each field equals the fixture value
  (incl. the boundary values `Seq == 0` and `IsError == false`, and `StopReason` == a real
  taxonomy value e.g. `"end_turn"`);
- re-marshal the envelope; assert `canonical(out)` byte-equals `canonical(raw)`.

### Five new fixtures — `internal/protocol/testdata/`

`turn_state.json`, `assistant_delta.json`, `tool_use.json`, `tool_result.json`, `turn_end.json`.
Each a complete envelope `{id, type, ts, payload:{…}}` with realistic values, in the existing
fixture style (compact, no `in_reply_to` — these are unsolicited binary→phone events, like
`message.json`). Suggested values to pin meaningful round-trips: `assistant_delta` with
`seq:0`; `tool_result` with `is_error:false`; `turn_end` with `stop_reason:"end_turn"`.

### `internal/protocol/handshake_test.go` (modify) — capability round-trips

Two new tests (programmatic, copying the `TestRoutingEnvelope_TokenOmitempty` shape,
`envelope_test.go:127-155`) — **no new fixtures** for capabilities:
- `HelloClientPayload{… Capabilities: []string{"interactive"}}` → marshal → unmarshal →
  `Capabilities` decodes back to `["interactive"]` (use `protocol.CapabilityInteractive`).
- `HelloAckPayload{… Capabilities: []string{CapabilityInteractive}}` → round-trips unchanged.
- For each, also assert the **omitempty absence**: a payload with nil/empty `Capabilities`
  marshals to bytes that do **not** contain `"capabilities"`.

The existing `TestHelloClientPayload_RoundTrip` / `TestHelloAckPayload_RoundTrip` stay
**unchanged** and are the byte-stability regression guard — they must keep passing against the
unedited fixtures, proving the additive field didn't perturb the v1 shape.

### `internal/protocol/compat_test.go` (modify) — partition + rejection

- Add the five constants to the test-local `v2OnlyTypes` map (80-82) and broaden its doc
  comment to cover "v2-only additive application events" alongside control types.
- In `TestTypeConstants_V1V2Partition` (91-119): add the five to the `all` slice; the size
  assertion becomes `len(v1TypeSet)+len(v2OnlyTypes) == len(all)` → `16 + 6 == 22`.
- In `TestIsV1Compatible` (27-47): add five rejection rows, one per interactive type, each
  `want: ErrUnknownType` — pins AC #2's "IsV1Compatible returns ErrUnknownType for any of the five."
- `TestV1TypeSet_CoversAllExportedTypeConstants` (50-71) is **unaffected** (it enumerates only
  v1 types; `v1TypeSet` is not touched). Leave it alone.

### Gate

`make check` (vet + `-race` + staticcheck + `cmd/substrate-guard`). No screen literals are
introduced, so the substrate guard stays green trivially.

---

## Documentation amendment — `docs/protocol-mobile.md`

Part of this ticket (ADR 025 § Consequences: "the spec follows the code"). Match the existing
per-type section style (field table + direction):

1. **Application message types table** (`:401-420`): add five rows — `turn_state`,
   `assistant_delta`, `tool_use`, `tool_result`, `turn_end`, all `binary → phone`, early-data
   `no`, Notes `New in v2 (interactive, capability-gated)`.
2. **New subsection** `### Interactive events (v2, capability-gated)` after the hello note: a
   short intro (gated on the `interactive` capability) + one field table per type listing each
   field, type, and meaning. For `turn_end.stop_reason`, list the taxonomy values and note they
   mirror the ACP turn-end reasons.
3. **Capability negotiation note** on `hello` / `hello_ack`: the `capabilities: []string` field
   (omitempty), phone advertises `["interactive"]` in `hello`, daemon echoes its supported set
   in `hello_ack`; note the actual intersection/enforcement is the consumer's (forward-ref #608).

Then run `qmd update && qmd embed` (AC #5; `embed` alone won't pick up the doc change but here
it's an edit not a new file — run both to be safe).

---

## Acceptance criteria → deliverable map

| AC | Deliverable |
|----|-------------|
| 1 — five payload types + per-type fixtures | `interactive.go` (5 structs) + 5 `testdata/*.json` + `interactive_test.go` |
| 2 — five `Type*` constants in v2-only partition; partition check passes; `IsV1Compatible`→`ErrUnknownType` | `codes.go` new const block + `compat_test.go` (`v2OnlyTypes`, partition test, rejection rows) |
| 3 — `capabilities []string` omitempty on hello/hello_ack; existing fixtures byte-identical | `handshake.go` (2 fields + `CapabilityInteractive`); unchanged hello fixtures + their unchanged round-trip tests |
| 4 — capabilities round-trip on both payloads | two new tests in `handshake_test.go` |
| 5 — `docs/protocol-mobile.md` amended; `qmd update && qmd embed` | doc edits above |

---

## Scope self-check (recorded so the reviewer sees it was applied, not missed)

- **Production source files (`.go`, excluding `_test.go` / `.md` / spec):** `interactive.go`
  (new), `codes.go` (mod), `handshake.go` (mod) = **3**. Under the ≥5 split gate.
- **New exported types:** exactly **5** (the payload structs). `CapabilityInteractive` and the
  five `Type*` are *constants*, not types. At, not over, the >5 line.
- **Consumer call sites needing simultaneous update:** **0** (additive field, keyed literals;
  confirmed `codegraph_impact HelloAckPayload` + grep of both construction sites).
- **Total written LOC ≈ 370** (≈68 production, ≈220 test, ≈25 fixtures, ≈60 doc). Under 600.
- **Reject branches / state machine:** none.
- The five `testdata/*.json` + `interactive_test.go` are the package's established 1:1-per-type
  testing pattern (every payload type has a fixture + round-trip test), not design sprawl — which
  is why the production-file count (3), not the raw new-file count, is the binding S constraint.
  The PO sizing anticipated this ("Tests roughly double the diff … don't count toward the
  production budget").

---

## Open questions

- **`State` / `StopReason` as named-string-enum types vs plain `string`.** Chosen: plain
  `string`, matching the `MessagePayload.Role` precedent and keeping `protocol` import-free of
  `turnevent`. If #608 finds it wants exhaustiveness checks on the producer side, it can add
  typed constants there; the wire shape is unaffected either way. Not blocking.
- **`Seq` as `int` vs `uint64`.** Chosen `int` (per-turn counter, package count-field idiom).
  If a future cross-turn global-monotonic need appears, that's a #608/#596 concern; the wire
  number is the same either way. Not blocking.
