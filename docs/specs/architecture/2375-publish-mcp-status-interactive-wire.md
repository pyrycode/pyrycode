# #2375 — Publish MCP server status on the interactive wire

## Files read

- `internal/turnbridge/outbound.go` → `MapEvent` — owns the neutral-event to interactive-payload translation and establishes that status-like events ignore turn addressing.
- `internal/turnbridge/outbound_test.go` → `TestMapEventOutbound` — exhaustive mapper table whose distinct expected types prevent another event variant from silently becoming `mcp_status`.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`, `emitMappedAt`, `emit`, `eventKind` — owns delta flush order, lifecycle state, one-per-logical-event ring append, interactive fan-out, and content-free event names.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_ModelListFansOutToEveryInteractiveConn`, `TestInteractiveTurnEmitterV2_ModelListMidTurnDoesNotDisturbOpenTurn`, `TestInteractiveTurnEmitterV2_RingIDIsPerEventNotPerConn` — nearest child-report fan-out, lifecycle-neutral ordering, and ring-cardinality proofs.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant` — already pins `turnevent.MCPStatus` as `turnMarkNone`, so this ticket must agree rather than add a second lifecycle classification.
- `internal/turnevent/event.go` → `MCPStatus`, `MCPServerStatus` — the already-bounded, order-preserving source contract and its five untrusted server strings.
- `internal/protocol/interactive.go` → `MCPStatusPayload`, `MCPServerStatus`, `MCPStatusPayload.MarshalJSON` — the declared wire contract, including nil-to-empty server-list normalization without caller mutation.
- `internal/streamsup/parser.go` → `mcpStatusResponseLine`, `mcpStatusServerLine`, `Parser.emit` — confirms excluded fields never reach the event and that #2374 suppresses ineligible child status before the shared sink.
- `internal/streamsup/runner.go` → `mcpStatusEligible`, `Runner.RequestMCPStatus` — confirms one request per eligible strict daemon-config child is upstream policy, not mapper or emitter policy.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSON`, `decodeControlRequest`, `writeInitializeAck` — fake-child control-response pattern and the rider boundary needed to make the new status reply opt-in.
- `internal/e2e/relay_v2_stream_model_list_test.go` → `driveModelListRespawn`, `TestRelayV2_StreamModelListReachesConnectedPhone` — the same-session crash/respawn pattern that routes a conversation before a child-ready report while keeping the phone connected.
- `docs/knowledge/features/protocol-package-interactive-event-payloads.md` → interactive event delivery contract — all mapped frames are capability-gated binary-to-phone reports.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” — current parser and child lifecycle boundary inherited from #2374.
- `docs/knowledge/features/e2e-harness.md` → `StartStreamInteractiveWithRelay` and readiness guidance — socket readiness is not child-initialize readiness, so the e2e must synchronize on observable turn and child events.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface”, “Prove that tests distinguish the change”, and “Protocol boundaries” — requires explicit type-switch coverage, idle plus mid-turn lifecycle proof, and decoded payload assertions.
- `docs/protocol-mobile.md` → `mcp_status` and “Security model” — declaration-only publication text to hand off, plus the render-boundary and paired interactive-channel threat model.

## Context

#2374 now admits `turnevent.MCPStatus` only for a child proven to use the daemon’s sole strict MCP config, but `turnbridge.MapEvent` and `interactiveTurnEmitterV2.Handle` both still fall through their defaults. Eligible status therefore disappears before it reaches a connected client or the event ring. This ticket completes that live path without moving eligibility, request cardinality, bounds, sanitization, or recovery into the bridge.

This does not warrant an ADR. It adds one variant to the established neutral-event translation and interactive publication contracts; the privacy decision remains at `Parser.emit`, before this package boundary.

## Design

### Mapping

Add a `turnevent.MCPStatus` arm to `MapEvent`. It builds a fresh outer `[]protocol.MCPServerStatus` in source order and copies each row’s `Name`, `Status`, `Error`, `Scope`, and `Version`. It supplies `TurnContext.ConversationID`, copies `DroppedServers`, and ignores `TurnID` and `Seq` because the payload is conversation-scoped.

The arm performs no sorting, cap, sanitization, filtering, deduplication, defaulting, or eligibility check. Nil `Servers` stays nil at the struct boundary; `MCPStatusPayload.MarshalJSON` alone normalizes it to `[]` on the wire. The fresh outer slice prevents any future mapper operation from mutating event ordering while nested values are strings only.

`TestMapEventOutbound` gains populated and zero-value rows. Its existing expected-type assertion covers the “no other variant maps to this type” half because every other listed event retains its own discriminator or the unmapped result. A focused JSON assertion proves the empty event still has `"servers":[]` and numeric `dropped_servers`.

### Interactive publication and lifecycle

Add a dedicated `turnevent.MCPStatus` case to `interactiveTurnEmitterV2.Handle` with exactly two actions: `flushDelta`, then `emitMapped`. It calls none of `startTurnIfNeeded`, `transitionTo`, or `endTurn`, agreeing with the upstream `turnMarkNone` classification.

`emit` remains unchanged and supplies the cardinality contract: it marshals once, appends once to the conversation ring, then fans the same logical event and event id to every currently interactive connection. No local empty-list suppression is added. `eventKind` gains the fixed string `mcp_status` so no-cursor and upstream drop diagnostics name only the variant and never a server field or count.

Emitter tests use a two-row fixture with distinct values. One test checks two interactive connections each receive one frame, a non-interactive connection receives none, the ring receives one entry, and ring and wire payloads are identical. Lifecycle tests cover both required states: idle receives only the status and remains idle; mid-turn flushes earlier text before status, preserves the open turn, and lets later text retain the same turn id with the next sequence number. An ordinary turn without an event is also checked for zero synthesized status frames.

### Hermetic child flow

Extend fakeclaude with an opt-in `PYRY_FAKE_CLAUDE_MCP_STATUS` rider. When enabled, `runStreamJSON` recognizes the existing `mcp_status` control request and answers it through `writeJSONLine` with one canned, fully populated server row. When disabled, the request remains unanswered exactly as today, avoiding new unsolicited frames in unrelated e2e tests. The reply includes only inert sentinel text and echoes the request id through JSON encoding.

Add `relay_v2_stream_mcp_status_test.go`, following `driveModelListRespawn` without sharing or refactoring that existing proof. The test seeds and routes one conversation, connects an interactive phone, completes one ordinary turn to stamp the active-conversation cursor, kills the child, waits for a different child PID, and then drains the already-open phone until the replacement child’s single status arrives. It sends and completes a subsequent ordinary turn, counting any `mcp_status` frames through that boundary. Assertions require the preconditions, exactly one total status, the seeded conversation id, and every canned server field.

```text
eligible replacement child initialize reply
  -> #2374 requests and admits one MCPStatus
  -> stream drain routes the active session event
  -> Handle flushes pending delta without lifecycle mutation
  -> MapEvent supplies conversation id and payload rows
  -> emit appends once to the ring and fans out once per interactive connection
```

## Concurrency model

No goroutine, channel, lock, or shutdown path changes. The stream drain continues to call `Handle` serially. `emit` continues to marshal and append before taking a fresh connection snapshot, and the ring owns its own synchronization. The fake child continues its single reader/write loop; the new response is a synchronous JSON-line write on that loop. E2e timeouts bound every control poll and phone receive, and existing harness cleanup terminates daemon and child processes.

## Error handling

- `MapEvent` remains total and non-failing for the new JSON-native payload.
- Marshal failure follows `emit`’s existing content-free drop path; no server value or `json` error is logged.
- Push failure remains per connection and cannot duplicate the ring append or suppress later fan-out attempts except on context cancellation.
- A malformed or missing fake request produces no reply; fake output write failure ends the fake child’s loop on its existing convention.
- E2e transport errors, error envelopes, payload decode errors, missing respawn, and deadlines fail with stage-specific diagnostics that do not print server secrets; the canned values are non-secret sentinels.

## Testing strategy

- RED: add mapper rows and empty-list JSON proof; run the focused `internal/turnbridge` tests and observe `MapEvent` refuse the event.
- RED: add fan-out/ring and idle/mid-turn emitter tests; run focused `cmd/pyry` tests and observe the status take `Handle`’s unknown-event branch.
- RED: add the rider-gated fake response and hermetic respawn test; run the focused e2e under the `e2e` tag and observe no `mcp_status` frame until the production mapping/emitter arms exist.
- GREEN: implement the mapper, emitter, content-free kind, and fake response, then run `go test -race ./internal/turnbridge/... ./cmd/pyry/... ./internal/e2e/...`, `go vet ./...`, and `go build ./cmd/pyry` per the builder touched-scope gate.

## Open questions

None. The ticket and #2374 fix the policy boundary, and the existing model-list respawn proof fixes the only viable hermetic observation lane.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md` in the message-type table’s `mcp_status` row and the `#### mcp_status` section from declaration-only to the shipped publication contract. State that the live frame is emitted once per eligible daemon-configured strict child, enters the conversation event ring, and never reports user- or project-scoped MCP servers from a bypass child. Preserve the existing per-field definitions and the requirement that every Claude-authored server string is rendered as inert text.

## Scope re-check

- Deliverables: 1 — publish the already-admitted MCP status snapshot through the established interactive live lane, with seam and end-to-end proof.
- Production source files: 3 — `internal/turnbridge/outbound.go`, `cmd/pyry/interactive_turn_v2.go`, and the test-only stand-in `internal/e2e/internal/fakeclaude/main.go`.
- Total written work: approximately 730 lines including tests and this plan, within 800.
- New exported types or interfaces: 0.
- Consumer call sites requiring simultaneous update: 0; no signature changes.
- Acceptance criteria: 4.
- Distinct reject/error branches: 0 in the production state machine; existing mapper, emitter, and transport failures are reused unchanged.

The refiner estimated two production files. The current e2e stand-in does not answer `mcp_status`, so one opt-in fake-child production file is additionally required to exercise the requested hermetic process boundary. The ticket remains within every quantitative boundary.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `mcpStatusResponseLine` and `mcpStatusServerLine` remain the sole subprocess decode boundary, #2374’s `Parser.emit` policy remains the sole eligibility boundary before the shared sink, and `MapEvent` only translates the resulting bounded projection. Every copied string remains explicitly untrusted display data.
- [Tokens, secrets, credentials] No findings — the live payload types structurally exclude MCP config, commands, environments, tools, request metadata, and raw responses. The fake reply echoes only the daemon-minted request id through `writeJSONLine`, and neither it nor any server field is logged.
- [File operations] No findings — production mapping and publication add no file read, write, path construction, permission change, or symlink handling. The existing event ring is memory-only; the fake-child change reads no new file.
- [Subprocess / external command execution] No findings — child argv, environment inheritance, signal behavior, and shutdown are unchanged. The new fake-child rider is test-only response selection and no reported server string becomes an argument, environment value, or shell input.
- [Cryptographic primitives] No findings — no randomness, secret comparison, key, nonce, hash, or cryptographic primitive changes. Publication continues through the existing Noise-sealed interactive connection.
- [Network & I/O] No findings — no socket reader or connection limit changes. The event was bounded before this layer, `emit` reuses the application-envelope path, and the once-per-child request bound remains upstream in #2374. An empty list is data, not a retry or amplification trigger.
- [Error messages, logs, telemetry] No findings — `eventKind` returns only the fixed discriminator, and existing drop diagnostics omit payload and marshal-error text. Server names, statuses, errors, scopes, versions, counts, config paths, and request ids do not enter logs or telemetry.
- [Concurrency] No findings — no goroutine, channel, lock, or shared-state mutation is added. `Handle` remains single-goroutine, `emit` performs one ring append before fan-out, and mapping constructs a fresh outer slice without mutating the event.
- [Threat model alignment] No findings — pairing, interactive capability negotiation, Noise AEAD, and relay metadata exposure are unchanged. A paired client receives only the daemon-configured strict child’s bounded report because bypass-child suppression occurs before the bridge; the output grants no authority and every server string remains inert at the client render boundary.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-12
