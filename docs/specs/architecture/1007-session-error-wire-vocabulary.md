# Spec #1007 — session_error wire vocabulary (v2-only type, terminal code, conversation-scoped payload)

**Size:** XS · **Security-sensitive:** no · Split from #1001 (vocabulary half A). Producer + wiring is sibling **#1008** (blocked-by-this).

## Context

`internal/msgqueue` now bounds persistent-failure drains and, on give-up, invokes the `OnGiveUp` seam (shipped by #1000, PR #1002) carrying the affected `conversation_id` and a daemon-generated human-readable reason. Today that give-up produces **no client-visible frame**. Surfacing it follows the codebase's established two-ticket granularity — **wire vocabulary → producer** (#720→#722, #844→#845, #656→#657). This ticket is the vocabulary half only: declarations, the partition entry the disjoint-partition test forces, and a golden fixture. No producer, no emission, no consumer.

The existing v2 error primitives don't fit an unsolicited, conversation-scoped, terminal give-up (settled by the #1001 split — do not reopen reuse-vs-new):

- `protocol.TypeError` / `ErrorPayload` is a **v1TypeSet** member, and every existing `TypeError` push (`sealError` / `settingsReplyError`, `internal/relay/v2session.go`) is `InReplyTo`-correlated to a client request. The give-up frame is **unsolicited** — no request to reply to — so it carries the conversation identity itself and must never be a v1 type (an old v1 phone must never receive it).
- `TypeStall` / `StallPayload` is a "session may be stuck" hint, not an error-state frame.

So this ticket introduces a **dedicated v2-only** session-error type, a **terminal** code distinct from transient `server.binary_busy`, and a conversation-scoped payload — the exact shape every other v2-only interactive event already follows (`TypeStall`, `TypeQueueState`, `TypeSessionTransition`), each excluded from `v1TypeSet` and carrying `conversation_id` as its routing key.

## Files to read first

- `internal/protocol/codes.go:32-34` — the "Session errors" group (`CodeSessionNotFound`); the new terminal code lands here.
- `internal/protocol/codes.go:227-245` — the `TypeSessionTransition` single-const-block precedent (comment shape + "wire vocabulary only, producer is sibling #N"); the new type mirrors it exactly.
- `internal/protocol/messaging.go:36-65` — `SessionTransitionPayload`: sibling conversation-scoped v2-only payload, doc-comment conventions, `conversation_id` routing-key wording.
- `internal/protocol/messaging.go:197-205` — `QueueStatePayload`: the closest structural mirror (plain `ConversationID` string, no omitempty, resolved daemon-side).
- `internal/protocol/compat_test.go:142-174` — `v2OnlyTypes` allowlist; add the new type here.
- `internal/protocol/compat_test.go:183-240` — `TestTypeConstants_V1V2Partition`: the disjoint-partition check + its `all` drift-detector list (add the type to BOTH the map above and this `all` list; the union-count assertion at line 237 forces both).
- `internal/protocol/compat_test.go:33-92` — `TestIsV1Compatible` `cases`: add a `session_error-rejected` case (mirror `session_transition-rejected`, line 59-61).
- `internal/protocol/compat_test.go:242-281` — `TestErrorCode_Constants_MatchSpec`: add `CodeSessionBlocked` to BOTH the `cases` and `want` maps (the len-equality guard at line 273 forces both).
- `internal/protocol/messaging_test.go:462-509` — `TestQueueStatePayload_RoundTrip`: the round-trip test template to copy.
- `internal/protocol/interactive_test.go:9-28` — `roundTripEnvelope` helper (re-marshals the decoded payload; this is what pins struct→wire shape).
- `internal/protocol/envelope_test.go:11-27` — `canonical` / `readFixture` helpers.
- `internal/protocol/testdata/queue_state.json` — fixture format (single-line envelope-with-payload JSON) to mirror for `session_error.json`.
- `docs/protocol-mobile.md` § Error codes / § Interactive events — add the spec-table row for `session_error` + `session.blocked` (documentation-owned evergreen doc; the developer appends the wire-vocab row here only, no other doc).

**Do NOT touch `internal/protocol/envelope.go`.** The v1/v2 boundary is enforced by *omitting* the constant from `v1TypeSet` — exactly as the sibling v2 types do. Adding it there would fail the disjoint-partition test.

## Design

Three declarations + one partition entry + one fixture + one round-trip test. Names are carried forward verbatim from the #1001 split.

### 1. `internal/protocol/codes.go` — the terminal code

Add to the existing "Session errors" group (alongside `CodeSessionNotFound`):

```go
CodeSessionBlocked = "session.blocked" // terminal give-up; NOT a retry hint (contrast server.binary_busy)
```

- **Terminal, not transient.** The name says "blocked", not "busy" — a client cannot read it as "retry shortly". It is distinct from `CodeServerBinaryBusy` (`server.binary_busy`), the transient/busy code. This distinction is AC #2.

### 2. `internal/protocol/codes.go` — the v2-only type

Add a new trailing const block mirroring the `TypeSessionTransition` block (codes.go:227-245). The doc comment must state: unsolicited binary→phone give-up marker; MUST NOT be added to `v1TypeSet` (an old v1 phone must never receive it); the drift detector in `compat_test.go` partitions Type* constants and this one lives in `v2OnlyTypes`; this ticket (#1007) is wire vocabulary only — the producer that emits it on `msgqueue` give-up is sibling #1008.

```go
const (
    TypeSessionError = "session_error" // binary → phone, outbound v2 unsolicited terminal session-error frame
)
```

### 3. `internal/protocol/messaging.go` — the payload

Add `SessionErrorPayload` next to `QueueStatePayload` / `SessionTransitionPayload`:

```go
type SessionErrorPayload struct {
    ConversationID string `json:"conversation_id"` // routing key; plain string, no omitempty — mirrors the sibling interactive payloads
    Code           string `json:"code"`            // terminal wire code (CodeSessionBlocked); plain string, not a named enum (leaf-data convention)
    Message        string `json:"message"`         // daemon-generated human-readable reason
}
```

- **Field set is exactly three; no `Retryable` / `RetryAfterS`.** Their structural absence is what prevents a client from reading the frame as transient — this is AC #3, enforced by the type declaration itself (nothing to marshal → nothing to misread).
- **No `omitempty` on any field.** All three are always present on the wire so the golden fixture pins the full shape, matching every sibling payload in this file (queue/modal/session_transition). `Code` is a plain string over a closed wire set, not a named enum — the leaf-data convention (`MessagePayload.Role`, `SessionTransitionPayload.Reason`).
- **`ConversationID` is the daemon's own resolved id** (set by the producer #1008 from the `OnGiveUp` `conversation_id`), never attacker-derived — same posture as `QueueStatePayload.ConversationID`. Note this in the doc comment; no enforcement lives here.
- Doc comment follows the sibling convention: what it is, that the producer is sibling #1008, and the never-log discipline for `Message` is #1008's concern (this file is pure vocabulary).

### 4. `internal/protocol/compat_test.go` — partition maintenance (AC #1 forces this)

- Add `TypeSessionError: true` to the `v2OnlyTypes` map (with a `// v2 session-error frame.` comment).
- Add `TypeSessionError` to the `all` list in `TestTypeConstants_V1V2Partition` (v2 section). The union-count assertion (`len(v1TypeSet)+len(v2OnlyTypes) == len(all)`) forces both edits together.
- Add a rejected case to `TestIsV1Compatible`'s `cases`: `{"session_error-rejected", TypeSessionError, false, ErrUnknownType}` (mirror `session_transition-rejected`).
- Add `CodeSessionBlocked` to BOTH the `cases` and `want` maps in `TestErrorCode_Constants_MatchSpec` (`"CodeSessionBlocked": ... "session.blocked"`); the `len(cases) != len(want)` guard forces both.
- **Do NOT** add `TypeSessionError` to the v1-only lists (`allTypes` at line 9, the `all` at line 104 in `TestV1TypeSet_CoversAllExportedTypeConstants`) — those assert a hardcoded count of 26 v1 types and are not the partition list.

### 5. `internal/protocol/testdata/session_error.json` — golden fixture

Single-line envelope-with-payload JSON, mirroring `queue_state.json`:

```json
{"id":701,"type":"session_error","ts":"2026-07-15T10:00:00Z","payload":{"conversation_id":"conv-1","code":"session.blocked","message":"daemon gave up delivering queued messages after repeated failures"}}
```

Field order in the fixture must match the struct's marshal order (`conversation_id`, `code`, `message`) so the byte-equal round-trip passes. `id`/`ts` values are arbitrary but must be valid.

## Data flow

None this ticket. The type is declared and unwired — zero producers, zero consumers, zero call sites. The give-up→frame path (channel from `OnGiveUp` → producer `Run` in `startRelayV2` → broadcaster) is entirely sibling #1008's, and is security-sensitive there (emission surface + no-leak of queued text). This ticket cannot leak content because it emits nothing.

## Error handling

None. Pure data declarations. Malformed-input handling lives with the (inbound) handlers; `session_error` is outbound-only and has no inbound handler, so there is no decode-and-reject path to test (contrast `dequeue_message`, which is inbound and has a malformed test).

## Testing strategy

- **`TestSessionErrorPayload_RoundTrip`** (messaging_test.go, copy the `TestQueueStatePayload_RoundTrip` shape): read `session_error.json`, unmarshal the envelope, assert `env.Type == TypeSessionError`; unmarshal the payload, assert `ConversationID == "conv-1"`, `Code == CodeSessionBlocked`, `Message` non-empty; then `roundTripEnvelope(t, env, payload, raw)`. The byte-equal re-marshal is the regression detector for the stable-field-name AC (AC #4) — a renamed or reordered json tag diverges the bytes.
- **Partition tests already present** (`TestTypeConstants_V1V2Partition`, `TestIsV1Compatible`) enforce AC #1 automatically once the type is added to `v2OnlyTypes` + the `all` list; if the developer forgets either, the union-count / missing-from-both branch fails.
- **`TestErrorCode_Constants_MatchSpec`** enforces the exact wire string `"session.blocked"` for AC #2 once the maps are updated.
- Run: `go test -race ./internal/protocol/...`, `go vet ./...`, `gofmt`.

## Acceptance criteria (from ticket, mapped to design)

1. v2-only `TypeSessionError`, NOT in `v1TypeSet`, added to `v2OnlyTypes` + drift-detector `all`; disjoint-partition test passes → §Design 2, 4.
2. Terminal `CodeSessionBlocked = "session.blocked"`, distinct from `server.binary_busy` → §Design 1.
3. `SessionErrorPayload{ConversationID, Code, Message}` — conversation routing key + terminal code + human message, **no** `Retryable`/`RetryAfterS` → §Design 3.
4. Round-trips with stable json names (`conversation_id` routing key), golden fixture + round-trip test per the per-payload convention → §Design 5, §Testing.
5. Ships **unwired** — declarations, partition entry, fixtures only; no producer/emission/consumer → §Data flow.

## Open questions

None. Names, payload shape, file homes, and the reuse-vs-new decision are all settled by the #1001 split. `Message`-content examples in the fixture are illustrative; the daemon's actual give-up reason wording is #1008's concern.
