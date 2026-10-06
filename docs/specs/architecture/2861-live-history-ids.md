# Durable history IDs on direct live pushes (#2861)

## Files read

- `cmd/pyry/conversation_history.go` → `appendConversationHistory`: concrete nil guard, append seam and content-free failure logging.
- `cmd/pyry/interactive_turn_v2.go` → `emit`: one ring/history append and timestamp before capability-gated fan-out.
- `cmd/pyry/session_transition_v2.go` → `broadcast`: resolved conversation, one history append, no ring.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory`: safe queued-message projection and placement-time commit.
- `cmd/pyry/operator_message_v2.go` → `operatorMessage`, `broadcast`, `operatorMessageNotify`: existing synchronous/channel handoffs, optional ring and locked fan-out.
- `cmd/pyry/interactive_turn_v2_history_test.go`, `session_transition_v2_history_test.go`, `operator_message_history_test.go`, `operator_message_v2_test.go`: real store, broadcaster and placement test seams.
- `internal/protocol/envelope.go` → `Envelope.HistoryEntryID`: merged optional pointer with omission semantics.
- `internal/e2e/relay_v2_history_test.go`, `relay_v2_stream_send_test.go`: fake-phone history paging, pairing and producer-driven stream setup.
- `docs/knowledge/features/history-package.md` § Producers: preserve queued text projection, synchronous echo placement and content-free append failures.
- `docs/knowledge/features/protocol-package.md`, `protocol-package-types-envelope.md`: independent ID namespaces and timestamp equality across JSON.
- `docs/knowledge/features/e2e-harness.md`, `e2e-harness-stream-interactive-harness-pattern-startstreamin.md`: isolated daemon; collect awaited frames without assuming ack ordering.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`: attribution and startup traffic already reach history.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, `docs/protocol-mobile.md` § Security model: implementation and evidence boundaries.

## Context

Mobile clients need the stored per-conversation entry ID on direct live messages to mark those entries read without fetching history first. #2860 already declares the field; the three producers discard its source today. One deliverable is wiring and proving that provenance. No decision record is needed. No overlapping feature branches were found after fetching origin.

Sizing: approximately 450 written lines including plan, code and tests; zero new exported types/interfaces; three append consumers and one internal handoff; three acceptance criteria; no new reject branches. All five limits remain below their ceilings.

## Design

`appendConversationHistory(...) *uint64` returns the successful `Store.Append` ID, or nil for absent storage or append failure. Retain the existing warning and its discriminant; callers always proceed with live delivery.

Interactive `emit` and transition `broadcast` assign this result to each direct envelope's `HistoryEntryID` after their existing single append. The operator commit carries it through an added unexported pointer field on `operatorMessage`; `operatorMessageEmitterV2.broadcast` copies it into each envelope. Payload, timestamp, ID allocation, ring recording, capability gates and placement ordering stay at their existing points. Update stale comments about discarded IDs and pending emission. Reconnect replay is outside this change.

## Concurrency model

No goroutines or locks are added. Store append remains synchronous. Each returned pointer names an immutable append result, shared across fan-out and carried safely through the existing operator channel or synchronous placement handoff. The operator emitter retains its mutex and shutdown paths.

## Error handling

Absent storage returns nil silently; failed append logs the existing content-free reason and returns nil. Neither path changes ring recording or pushes. Pointer plus `omitempty` omits the JSON key rather than emitting null or zero.

## Testing strategy

Write a table-driven producer test using the existing real store and broadcaster seams: each of the three paths with multiple interactive recipients plus a non-interactive recipient, no recipients, nil store and a failing store. Exercise operator commit through its notification channel and emitter, including nil ring. Seed history and envelope/ring counters differently; compare exact type/payload/timestamp and served entry ID, append counts, recipient order, envelope counters, ring IDs and raw key omission. Verify content-free failure logs. Existing lifecycle and placement tests remain the regression coverage.

Add a fake-phone producer-driven round trip beside the existing history walk: seed a bound conversation and enough prior history to separate namespaces, send a real message, receive its direct operator envelope, request history, and match by type, payload and timestamp before comparing IDs. Run the tests red before implementation, then scoped race tests, focused tagged e2e race tests, `go vet ./...` and `go build` for `cmd/pyry` (binary outside the worktree). The dispatcher owns the full-module verifier gate.

## Open questions

None. Store success IDs are nonzero by contract; no additional validation or emission gate is needed.

## Documentation handoff

Pending for the documentation stage: `docs/protocol-mobile.md`, § Wire shapes, “Application envelope”, and § Conversation history (v2): replace the pending-emitter note with the three direct live producer paths. State that the field is present only after a successful history append, is the served entry's per-conversation `id` and the read-mark `up_to` value, and is independent of connection `id` and ring `event_id`. Document absence for older daemons, absent/failed history storage, non-history-backed frames and current reconnect replay, with history/list fallback when needed.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `Store.Append` alone mints the ID. It is metadata, not authorization; existing conversation resolution and interactive gates remain. Operator content still comes only from `QueuedMessage`.
- [Tokens and cryptography] No credentials, keys, nonce handling or encryption changes; existing authenticated encrypted `Push` transports the added metadata.
- [File operations] No new paths or writes; use the existing concrete store and its canonical-ID/containment checks. No raw filesystem error is exposed.
- [Subprocesses] No process invocation changes; the integration test uses the existing isolated fake-Claude harness.
- [Network and I/O] Only one bounded optional uint64 is added to existing direct pushes. No socket reader, deadline, allocation from remote input or connection limit changes.
- [Errors and logs] Preserve `historyAppendFailure` classification and existing warning attributes; tests assert payload and host-path absence on failed append.
- [Concurrency] Immutable returned ID pointer; no new lock order or goroutine lifetime. Existing placement commit and operator fan-out mutex remain.
- [Threat model] Prompt content and relay/phone compromise boundaries in § Security model stay with the existing safe projection, Noise transport and device revocation. The ID grants no read-mark privilege. Replay reconstruction is explicitly outside this ticket's product scope; its documented fallback belongs to the documentation stage.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
