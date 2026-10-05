# Reply suggestion session-state contract (#2828)

## Files read

- `internal/protocol/interactive.go` → `TurnStatePayload`: adjacent interactive wire vocabulary; required keys omit no values.
- `internal/protocol/codes.go` → `TypeTurnState`: outbound v2 event naming and capability contract.
- `internal/protocol/envelope.go` → `Envelope`, `IsKnownAppType`: unchanged outer shape and v1 rejection boundary.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope`, `TestTurnStatePayload_RoundTrip`: re-marshal decoded payloads before comparing envelopes.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`: independent compatibility classifications.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `TestEveryInboundV2TypeHasHandler`: AST-based type coverage and outbound push classification.
- `docs/knowledge/features/protocol-package.md` and its interactive-event and drift-detector topics: pure DTO boundary; fixtures must pin literal type values.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: distinct IDs, explicit decoded values, and re-marshalled null keys.
- `CODING-STYLE.md`: table-driven stdlib tests and Go formatting.

## Context

Desktop and mobile need one fixed next-reply suggestion contract before later
children emit and enforce it. This ticket declares one outbound state frame;
it introduces no producer, validator, or client state machine. No decision
record is needed. No overlapping feature branches remained after fetching.

## Design

Declare `TypeReplySuggestion = "reply_suggestion"` and `ReplySuggestionPayload`
beside `TurnStatePayload`. Required fields are `ConversationID`, `SessionID`,
`Revision` (`uint64`), and `SuggestedReply` (`*string` without `omitempty`).
Nil emits explicit JSON null; a pointer to an empty string remains a string.
No existing payload or envelope changes.

Document daemon-to-client v2 direction, negotiated `interactive` gating,
positive revisions increasing per conversation within one daemon lifetime,
and inert single-line UTF-8 text bounded to 1024 bytes. Only explicit null
clears; omission and empty string do not. Clients replace state by
conversation/session, ignore lower revisions, and discard cached suggestions
on a fresh handshake. Enforcement remains with later children.

Classify the type as v2-only in both compatibility lists and the v1 rejection
table, and as outbound `push` in the relay guard.

## Concurrency model

Pure data and serialization only; no goroutines or mutable shared state.

## Error handling

Use ordinary `encoding/json` errors. Required-field, revision, and text
validation are deferred; the DTO does not distinguish absent input from null.

## Testing strategy

Add set/clear envelope fixtures with distinct conversation/session IDs and
positive increasing revisions. Table tests compare whole decoded payloads,
re-marshal payloads into envelopes, and inspect all four emitted keys and
explicit null on clear. Also prove an empty string remains a present string.
Run the new test before implementation to observe the missing declarations,
then scoped race tests for `internal/protocol` and `cmd/pyry`, `go vet ./...`,
and `go build ./cmd/pyry` with output outside the worktree.

## Open questions

None. Sizing: approximately 190 written lines, one new exported type, zero
consumer updates, two acceptance criteria, and no new reject branches;
all five builder limits hold.

## Documentation handoff

- Pending documentation stage: `docs/protocol-mobile.md` Message types table
  and `reply_suggestion` section publish the complete contract in Design above,
  both marked **declared, not yet emitted**.
