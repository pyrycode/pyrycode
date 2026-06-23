# Spec: add `conversation_id` to `session_transition` wire payload (#740)

**Size:** XS · **Lineage:** split from #738 · **Blocks:** #741 (`addBlockedBy` #740) · **Not** `security-sensitive`.

This is the **wire-vocabulary half only**: add the field to the SSOT struct, the SSOT doc, and the testdata fixtures + round-trip test. No producer wiring, no routing. The producer keeps emitting the zero value (`conversation_id: ""`) until #741 binds it; that is harmless because the mobile consumer (`pyrycode-mobile#336`) is parked.

## Files to read first

- `internal/protocol/messaging.go:36-58` — `SessionTransitionPayload` definition + its doc comment; the sibling `*string`-no-`omitempty` pattern lives just above (`BackfillSincePayload:30-34`). **Extract:** the exact tag/comment idiom; note `WorkspaceCwd` is `*string` (literal-null semantics) but `ConversationID` is plain `string` (no null semantics) — do **not** copy the `*string` shape.
- `internal/protocol/messaging.go:5-24, :184-198` — the four sibling interactive payloads that lead with `ConversationID string` + `json:"conversation_id"`, no `omitempty` (`SendMessagePayload`, `MessagePayload`, `QueueStatePayload`, `DequeueMessagePayload`). **Extract:** the field is always the **first** field and the routing key — mirror that placement.
- `internal/protocol/envelope_test.go:11-27` — `canonical` is **`json.Compact` only** (it does *not* sort keys) and `readFixture`. **Extract (critical):** the round-trip byte-equal check is **sensitive to JSON key order**. Struct field order, fixture-payload key order, and doc-table row order must all line up, or `TestSessionTransitionPayload_RoundTrip` fails. See § Gotcha.
- `internal/protocol/messaging_test.go:120-204` — `TestSessionTransitionPayload_RoundTrip`, table-driven over the two fixtures, per-field assertions + a byte-equal regression guard. **Extract:** add a `wantConvID` column and one assertion in the existing style; the byte-equal check needs no new logic, only correct fixtures.
- `internal/protocol/testdata/session_transition.json` and `session_transition_workspace.json` — single-line envelope fixtures. **Extract:** the exact `payload` key order to edit.
- `docs/protocol-mobile.md:554-568` — §`session_transition` field table + invariant prose; `:540` (`turn_end` table) shows a sibling that leads its table with a `conversation_id` row; `:434` is the one-line events-index row (no field detail). **Extract:** where to insert the new table row, and confirm `:434` needs no change.
- `cmd/pyry/session_transition_v2.go:163-184` — `toWirePayload` (the producer, #657). **Read-only context, do not edit:** it uses **keyed** composite literals, so adding a field compiles untouched and emits `conversation_id: ""`. This is the proof that #740 has zero producer fan-out.
- `internal/protocol/compat_test.go` — **do not edit.** It reads no fixtures; it only asserts `TypeSessionTransition`'s v1-incompatibility and v2 type-set membership, which a payload-field addition does not affect (per the ticket).

## Context

`SessionTransitionPayload` is the only interactive v2 event with no `conversation_id`. Every other interactive payload carries it as a plain `string` routing key and routes by it; `session_transition` does not, leaving `pyrycode-mobile#336` with no key to fold the session-boundary marker into the correct thread. This ticket gives the payload the same routing-key shape as its siblings. The producer-side binding (populating the field from a real conversation↔session binding) is the sibling ticket #741, which is `addBlockedBy` this one.

## Design

Pure leaf-data change in `internal/protocol`. No interfaces, no concurrency, no error paths — it is a struct field plus its serialization fixtures and SSOT doc.

### The contract (the whole production change)

Add one field to `SessionTransitionPayload`, in **first** position, mirroring the sibling routing-key convention:

```go
ConversationID string `json:"conversation_id"` // routing key; plain string (no literal-null semantics), no omitempty — mirrors the sibling interactive payloads
```

- **Type:** plain `string`, **not** `*string`. Unlike `WorkspaceCwd`/`BackfillSincePayload.ConversationID`, it has no "literal null / all-conversations" meaning — it is a present-or-empty routing key exactly like `MessagePayload.ConversationID`.
- **Tag:** `json:"conversation_id"`, **no** `omitempty` — the field is always present on the wire so the fixtures pin its zero value.
- **Placement:** first field of the struct, matching all four sibling interactive payloads (and signalling it as the routing key). This placement is **load-bearing for the test** — see § Gotcha.
- Update the struct's doc comment to mention `ConversationID` is the routing key (one line, in the style of the existing comment block).

### SSOT doc (`docs/protocol-mobile.md`)

- §`session_transition` field table (`:558-564`): insert a **first** row
  `| `conversation_id` | string | Conversation this session-boundary marker belongs to (routing key, matching every other interactive event). |`
  so doc-table order matches struct order matches fixture-key order.
- The events-index row at `:434` documents no fields → **no change** (the ticket's "if applicable" is not applicable here; state this in the PR description).
- The `workspace_cwd`-non-null-iff-`workspace_change` invariant prose (`:566-568`) is **unchanged** — the new field is orthogonal to it.

### Fixtures

Add `"conversation_id":""` as the **first** key of the `payload` object in both fixtures, pinning the zero value:

- `testdata/session_transition.json` → `payload: {"conversation_id":"","previous_session_id":"sess-a",…}`
- `testdata/session_transition_workspace.json` → `payload: {"conversation_id":"","previous_session_id":"sess-b",…}`

Both stay single-line. The `conversation_id: ""` here is the wire shape the producer emits until #741 lands.

## Gotcha (the one thing that breaks the test if missed)

`canonical` (`envelope_test.go:11`) is `json.Compact`, which strips whitespace but **does not reorder keys**. `json.Marshal` emits struct fields in declaration order. The round-trip guard asserts `canonical(marshal(env)) == canonical(fixtureBytes)`. Therefore:

> **Struct field order == fixture `payload` key order == doc-table row order.** Put `conversation_id` first in all three.

If the developer adds the field first in the struct but appends `"conversation_id":""` to the end of the fixture JSON (or vice versa), the bytes differ and `TestSessionTransitionPayload_RoundTrip` fails with a `round-trip bytes differ` diff. This is the same byte-equal mechanism that guards the `*string`-no-`omitempty` decision at `:195-201`.

## Concurrency model

N/A — pure data type. No goroutines, no shared state.

## Error handling

N/A — no new failure modes. Decode of a missing `conversation_id` key yields `""` (the zero value), which is exactly the intended transient shape; the round-trip fixtures pin it.

## Testing strategy

`go test -race ./internal/protocol/...` must stay green. Extend `TestSessionTransitionPayload_RoundTrip` only:

- Add a `wantConvID string` column to the existing case table; both cases expect `""` (the fixtures' zero value).
- After the existing per-field assertions, assert `payload.ConversationID == tc.wantConvID` in the same `t.Errorf` style.
- The existing byte-equal regression guard (`:199-201`) already pins the full wire shape including the new key once the fixtures carry it — no new guard needed.
- No new test function. `compat_test.go` is untouched (it reads no fixtures and asserts only type-set membership).

Edge confirmation (no code needed): the producer at `cmd/pyry/session_transition_v2.go:163-184` uses keyed literals, so `go build ./...` and the existing `cmd/pyry` tests stay green with the producer emitting `conversation_id: ""`.

## Acceptance criteria (mirrors the ticket)

1. `SessionTransitionPayload` carries `ConversationID string` with tag `json:"conversation_id"` (no `omitempty`), plain `string`, placed first to match the sibling interactive payloads' field name / type / tag / routing semantics.
2. `docs/protocol-mobile.md` §`session_transition` (`~:558`) documents the new field as a table row; the events-index row (`~:434`) needs no field-level change.
3. Both `testdata/session_transition*.json` fixtures and `TestSessionTransitionPayload_RoundTrip` include `conversation_id: ""`, pinning the full wire shape (incl. the zero value). `compat_test.go` unchanged.
4. No producer behaviour change; the `workspace_cwd`-non-null-iff-`workspace_change` invariant is unchanged.

## Open questions

None. The shape, type, tag, placement, and the producer's transient `""` emission are all fixed by the sibling convention and the existing keyed-literal producer.
