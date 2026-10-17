# #3538 — Activate shared channel pins for paired clients

## Files read

- `cmd/pyry/relay.go` → `startRelayV2`: handler map, shared registry path and conversation announcer.
- `internal/relay/handlers/set_conversation_pinned.go` → `SetConversationPinned`: validated mutation, eager save, correlated reply then fan-out.
- `cmd/pyry/channel.go` → `channelCreator`: projects the stored row after creation.
- `cmd/pyry/conversation.go` → `conversationCreator`: projects the stored row after creation.
- `cmd/pyry/agent_switch_relay.go` → `announceSwitchedConversation`: publishes stored metadata after a switch.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`: deterministic dispatch inventory.
- `cmd/pyry/channel_test.go` → `installIdentityTrustMark`, `newChannelTestRegistry`: isolated creator fixtures.
- `cmd/pyry/conversation_test.go` → `conversationMinterFunc`: creator adapter fixture.
- `internal/e2e/relay_v2_rename_test.go` → `testV2DaemonRenameRoundTrip`: authenticated installed daemon coverage.
- `internal/e2e/handshake_interactive_helpers_test.go` → `driveHandshakeToOpenDaemonInteractive`: paired interactive clients without a new capability.
- `docs/knowledge/features/conversations-package.md` → `PinnedAt`: stable pin time, immutable timestamp pointers, nil storage compatibility.
- `docs/knowledge/features/relay-package.md`: shared announcer avoids a separate broadcast path.
- `docs/knowledge/features/e2e-harness.md` and `e2e-harness-restart-pattern-startin-stop.md`: same-home restart and cleanup.
- `docs/knowledge/features/development-verification-protocol-boundaries.md`: inspect raw JSON to distinguish absent from explicit null.
- `docs/protocol-mobile.md` → Security model: authenticated encrypted remote control boundary.

## Context

Storage, wire declarations and the handler have landed. The installed daemon
still leaves the pin verb undispatched, and three command producers lose pins
when announcing other metadata. Activate the existing host-owned contract.
No new decision record is needed.

## Design

Register `handlers.SetConversationPinned` beside mute using the same registry,
resolved registry path and `announceConversationHook`. Move its guard inventory
entry from pending to map-dispatched. Copy `PinnedAt` from each producer's
stored snapshot into its `ConversationUpdatedPayload`. Keep the existing
non-omitempty wire field so an unpinned snapshot explicitly publishes null.
No capability, exported type, interface or consumer signature changes.

An authenticated fake-daemon test uses two separately paired interactive clients.
Each operation must first yield the requester's correlated reply, then the same
record as an uncorrelated push to both clients. Reload the registry and restart
the daemon after pin and unpin, checking the stored value through list replies.
Repeat pin/unpin to exercise idempotence and re-pin after clear.
Producer regression tests cover stored timestamps and explicit null. A logging
callback at the existing creation-success boundary can pin the new stored row
before its announcement, without introducing a production test seam.

Concurrent #3519 adds unrelated dispatch inventory entries in
`cmd/pyry/relay_guard_test.go`; keep this edit local and additive.
Sizing: approximately 430 written lines, 0 exported types, 0 signature consumers,
2 acceptance criteria and 0 new rejection branches; all five limits pass.

## Concurrency model

No new production goroutines or locks. Existing per-connection dispatch owns
requests; the registry owns mutation/save locking; the existing emitter fans out.
Tests use harness Stop and phone Close before restart and on cleanup.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Pin and repeated pin preserve one stored timestamp | `TestRelayV2_ChannelPins` |
| Unpin and repeated unpin publish explicit null | `TestRelayV2_ChannelPins` |
| Restart/reconnect under the same host and conversation IDs preserves each state, with fresh Noise cipher states | `TestRelayV2_ChannelPins` |
| Re-pin after clear publishes a fresh timestamp | `TestRelayV2_ChannelPins` |
| Other metadata announcement preserves stored pin/null | `TestCommandConversationPins` |

## Error handling

Reuse handler errors and best-effort persistence policy unchanged: malformed
payloads are rejected, missing rows report not-found, and pinning an unpromoted
row reports not-promoted. Unpin accepts any existing row. No new failure modes.

## Testing strategy

Write regressions first and observe failures before wiring/projection changes.
Run focused authenticated e2e coverage with `-tags e2e -race -count=1`, command
package race checks through test admission, then `go vet ./...` and
`go build ./cmd/pyry`. Test on this Linux host. The verifier owns the full-module
suite. Raw payload assertions check timestamps and explicit null independently
of typed decoding.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage: `docs/protocol-mobile.md`, “Application
message types”, “set_conversation_pinned”, “Error codes” and “Changelog”: replace
pending wording with implemented requests, reply/broadcast, idempotence,
channel-only pin/any-row unpin and older-host compatibility behavior.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `startRelayV2` installs the handler behind existing Noise and device authentication; `SetConversationPinned` validates the required boolean and registry eligibility. A conversation ID never becomes a filesystem path.
- Tokens/secrets: reuse paired-device authentication; no credential generation, storage or logging changes.
- File operations: handler receives only the daemon-resolved registry path; `Registry.Save` retains atomic replacement and existing permissions. No caller-selected path or new file operation.
- Subprocesses: metadata mutation has no runner or shell surface. Tests use the existing isolated fake-daemon harness.
- Cryptography: no changes to Noise or nonce handling; reconnect tests create fresh cipher states.
- Network/I/O: reuse bounded transport framing and deadlines; no new listener or network parser.
- Errors/logs: existing static refusal vocabulary and content-free handler logs remain; the wiring adds no payload or secret logging.
- Concurrency: no new locks; existing registry setter replaces immutable timestamp pointers. Reply precedes shared fan-out and restart tests close clients and stop the daemon.
- Threat model: metadata requests never become prompts or subprocess input. Noise authentication preserves the relay-MITM boundary; existing relay availability limits remain unchanged by this wiring.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17

## Revisions

- 2026-10-17: The installed authenticated regression exposed shared-relay bug
  #3545: `Conn.Reply` enqueues on the per-frame outbound buffer, while the
  announcer uses the manager push path; the push can reach the wire first.
  Repair requires production changes outside this ticket's wiring scope.
  Retain the strict ordering regression as `TestRelayV2_ChannelPinsReplyOrdering`,
  skipped with the bug reference per the out-of-scope bug policy. The active
  `TestRelayV2_ChannelPins` buffers early pushes and still requires the correlated
  reply, identical uncorrelated pushes to both clients, idempotence, reload and
  restart persistence, and re-pin timestamps. Wire ordering remains unproved
  pending #3545; no shared dispatch/relay production code changes here.
