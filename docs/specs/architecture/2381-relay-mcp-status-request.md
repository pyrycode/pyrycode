# Relay `mcp_status_request` contract (#2381)

## Files read

- `docs/knowledge/INDEX.md` — startup map identifying protocol, relay, and development-verification ownership.
- `docs/knowledge/features/development-verification.md` → `Protocol boundaries`, `Establish the change surface` — fixture, mutation-proofing, and consumer-count requirements for a new wire declaration.
- `docs/knowledge/features/protocol-package.md` → `IsKnownAppType`, `Security posture` — v1 rejection and payload-validation boundaries.
- `docs/knowledge/features/protocol-package-constants-codes-go-envelope-types.md` → v2 MCP-status and request-model-list vocabulary — the existing `TypeMCPStatus` direction and the precedent for declaring a request beside its only relay consumer.
- `docs/knowledge/features/protocol-package-model-list-payload.md` → `RequestModelListPayload` — the nearest one-field conversation request and correlated existing-payload answer.
- `docs/knowledge/features/relay-package.md` → `V2SessionConfig`, application routing — relay ownership, optional-seam posture, and requester-only reply path.
- `docs/knowledge/features/v2-session-manager-concurrency.md` → `appFrameWorker`, `forwardToRun`, `forwardAppReply` — off-`Run` work and Run-owned Noise sealing.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-model-list-modellistfor-seam.md` → `handleRequestModelList`, `ModelListFor` — capability, membership, resolver, error, and reply ordering precedent; this ticket deliberately differs by rejecting malformed payloads and moving a potentially blocking resolver off `Run`.
- `internal/protocol/codes.go` → `TypeMCPStatus`, `TypeRequestModelList`, `CodeModelListUnavailable` — discriminator and reject-code placement.
- `internal/protocol/interactive.go` → `MCPStatusPayload`, `RequestModelListPayload` — the existing answer and nearest request DTO.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`, `TestErrorCode_Constants_MatchSpec` — v1 rejection and registry drift detectors.
- `internal/protocol/interactive_test.go` → `TestMCPStatusPayload_RoundTrip`, `TestRequestModelListPayload_RoundTrip`, `TestRequestModelListPayload_WireKeys` — committed-fixture and exact-key/type proof patterns.
- `internal/relay/v2session_seams.go` → `V2SessionConfig`, `KnownConversation`, `ModelListFor` — consumer-side resolver contract and membership dependency.
- `internal/relay/v2session.go` → `dispatchAppFrame`, `appFrameKind`, `appFrameWorker`, `forwardToRun`, `forwardAppReply` — interception, per-connection work queue, and manager-owned send path.
- `internal/relay/v2session_history_request.go` → `handleRequestHistory`, `historyReplyError` — strict off-`Run` decode, membership-before-resolver order, and correlated error construction.
- `internal/relay/v2session_modelrequest.go` → `emitModelListReply`, `modelListReplyError` — forwarding an existing outbound payload unchanged and keeping replies out of push/replay paths.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`, `TestEveryInboundV2TypeHasHandler` — structural classification of switch-intercepted v2 request verbs.
- `internal/streamsup/runner.go` → `RequestMCPStatus`, `MCPStatusConfigPath` — confirms the live child request exists but is intentionally outside this relay-contract slice.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.MCPStatus` arm — confirms the existing outbound payload already preserves all server fields and the dropped count.

## Context

`TypeMCPStatus` and `MCPStatusPayload` already define the one outbound server-status frame. This ticket adds the v2 inbound request that selects one conversation and gives `internal/relay` an optional resolver contract. The production daemon intentionally leaves that resolver unwired until #2382 adds live-child request-id correlation; an unwired relay must consume the new type without inspecting its payload or claiming that the type is unknown.

The refined estimate is about 1,100 total written lines across five production files, exceeding the normal 800-line ceiling. The ticket is a grandchild (#2202 → #2276 → #2381), so another split is forbidden, and separating the request declaration from its sole relay consumer would create a one-consumer slice. The existing `needs-human:sizing` marker records the overage; implementation continues in place.

No active feature branch overlaps the planned files. The refreshed branch scan found only `origin/feature/449`; issue #449 is closed, so it is stale rather than in flight.

No ADR is needed. The request reuses the established v2 control interception, per-connection application worker, and Run-owned reply patterns without changing their architectural ownership.

## Design

### Protocol declaration

Add `TypeMCPStatusRequest = "mcp_status_request"` beside `TypeMCPStatus`. It is a v2-only phone-to-binary control discriminator, remains outside `inboundAppTypeSet`, joins `v2OnlyTypes`, and is classified as `switch-intercepted` by the relay guard. `TypeMCPStatus` remains the sole outbound `push+reply` discriminator.

Add `MCPStatusRequestPayload` with one unconditional `conversation_id` string field. It has no request-id field because correlation stays on `Envelope.InReplyTo`, and it has no custom marshaler or presence state. A committed `mcp_status_request.json` fixture plus independent key-set and raw JSON-type assertions pin the complete one-key schema. The v1 compatibility table explicitly rejects the request discriminator.

Add `CodeMCPStatusUnavailable = "mcp_status.unavailable"` beside the existing read-result codes. The relay combines an absent current status and a resolver refusal into this retryable answer; an unknown conversation continues to use `CodeConversationNotFound`, and decode failures use `CodeProtocolMalformed`.

### Resolver contract

Extend `V2SessionConfig` with an optional conversation-keyed function:

```go
MCPStatusFor func(context.Context, string) (protocol.MCPStatusPayload, bool)
```

The resolver receives only a payload that decoded successfully and only an id accepted by `KnownConversation`. It must honor cancellation because the call may wait for a child round trip. `false` means there is no current status to send; callers must not inspect the accompanying payload. The returned payload is already bounded and wire-shaped, and the relay forwards it without changing its conversation id, server order, fields, or dropped count.

The contract deliberately does not expose a retained-status fallback, child writer, or request-id correlation. #2382 owns those daemon-side decisions and wiring.

### Interception and request handling

`dispatchAppFrame` recognizes `TypeMCPStatusRequest` before `dispatch.Route`. The Run goroutine applies two inert gates before queuing any work:

1. A nil `MCPStatusFor` consumes the frame immediately, without decoding its payload, consulting membership, replying, or falling through to the unknown-type path.
2. A session that did not negotiate `CapabilityInteractive` is equally inert and consults no dependency.

An accepted request is tagged with a new `appFrameMCPStatusRequest` kind and enqueued on that connection's existing bounded FIFO. `appFrameWorker` sends it to a new handler in `v2session_mcpstatus.go`; the worker does not re-decide the discriminator.

The handler performs the remaining gates in this order:

1. Re-decode the already-probed envelope solely to retain its correlation id. An unexpected envelope decode failure produces no reply because no trustworthy id remains.
2. Decode `MCPStatusRequestPayload`. Any error sends one static, correlated, non-retryable `protocol.malformed` reply and stops before membership or resolver calls.
3. Consult `KnownConversation`. Nil or false sends one static, correlated, non-retryable `conversation.not_found` reply and stops before the resolver.
4. Call `MCPStatusFor(ctx, conversationID)`. A false result sends one static, correlated, retryable `mcp_status.unavailable` reply without reading the returned value.
5. Marshal the successful `MCPStatusPayload` into one `TypeMCPStatus` envelope addressed to the request's connection. Set `InReplyTo` to the request envelope id; leave `EventID` nil.

Success and rejection both marshal an unsealed `RoutingEnvelope` and pass it through `forwardToRun`. They do not call `Push`, a broadcaster, `forwardEnvelope` from the worker, or any event-ring API. Therefore the reply has exactly one audience and Noise encryption remains owned by `Run`.

```text
Run: decrypt → identify request → inert gates → conn worker queue
                                              │
worker: decode → membership → resolver → unsealed reply
                                              │
Run: forwardToRun/appReply → Noise seal → requesting conn only
```

### Logging

Logs contain only daemon-authored event names, static reasons/codes, and `conn_id`. They never contain the decoder error, raw payload, requested conversation id, resolver-returned conversation id, server count, server fields, or resolver error text. External error payloads use fixed messages and fixed retryability flags.

## Concurrency model

No new goroutine or channel is introduced. Each open session already owns one `appFrameWorker`; a potentially blocking `MCPStatusFor` call occupies only the requesting connection's worker. `Run` remains free to dequeue and service frames for other connections. Frames from the same connection remain serialized, which prevents overlapping handler calls from one peer and retains existing queue backpressure.

The resolver receives the manager's derived run context. A compliant resolver unblocks on shutdown; `s.done` and the run context already terminate the worker and unblock `forwardToRun`. The eventual reply crosses the unbuffered `appReply` channel and is sealed only by `forwardAppReply` on `Run`. If the connection closes before that handoff, `forwardToRun` drops the reply without sealing it.

## Error handling

- No resolver configured: consume silently before payload decode.
- Interactive capability absent: consume silently before payload decode.
- Payload decode failure: `protocol.malformed`, non-retryable, correlated; no dependency consulted.
- Unknown conversation or nil membership seam: `conversation.not_found`, non-retryable, correlated; resolver not consulted.
- Resolver returns false: `mcp_status.unavailable`, retryable, correlated; poisoned false-result payload ignored.
- Successful status: exactly one correlated `mcp_status`; no empty/stale fallback.
- Defensive envelope or reply marshal failure: content-free internal log and no wire reply; no unsealed or partially built frame is emitted.
- Session/manager teardown during resolution or handoff: context cancellation and `forwardToRun`'s existing teardown arms abandon the reply.

## Testing strategy

Protocol tests will first fail on the missing discriminator and payload:

- Round-trip the committed request fixture through `Envelope` and `MCPStatusRequestPayload`, asserting the discriminator, request direction (`InReplyTo == nil`), and conversation id.
- Assert the payload's complete key set is exactly `conversation_id`, its raw JSON value decodes as a string, and the zero value retains the key.
- Add the request to the v2 partition and explicit v1 rejection table.
- Pin `mcp_status.unavailable` in the error-code table.
- Add `TypeMCPStatusRequest` to the structural relay guard's `switch-intercepted` registry.

Relay tests will drive encrypted frames through a real `V2SessionManager` rather than call the handler directly:

- RED interception test: without implementation, a configured resolver request reaches `dispatch.Route` and returns an unknown-type response instead of the required status/error contract.
- Nil resolver: send payload bytes that would fail DTO decoding; assert no reply and zero membership consultation, proving the payload was not decoded.
- Non-interactive session with a configured resolver: assert no reply and zero membership/resolver calls.
- Malformed table (wrong JSON value types): assert one correlated non-retryable `protocol.malformed` reply, zero membership/resolver calls, and content-free logs.
- Unknown conversation: assert one correlated non-retryable `conversation.not_found`, zero resolver calls, and no remote-authored id in logs.
- Unavailable status: use a poisoned non-zero false-result payload; assert one correlated retryable `mcp_status.unavailable` and no `mcp_status` frame.
- Success: use distinctive values in every `MCPStatusPayload` field; assert exact preservation, one reply on the requesting connection, matching `InReplyTo`, nil `EventID`, and no frame on a second connection.
- Cross-connection liveness: block connection A's resolver, successfully serve connection B, then release A and receive its eventual reply. Running this under `-race` exercises the manager-owned seal path while resolver work overlaps Run activity.
- Interception registry: `TestEveryInboundV2TypeHasHandler` must see the new `dispatchAppFrame` case.

Touched-scope verification:

- `go test -race ./internal/protocol/... ./internal/relay/...`
- `go test ./cmd/pyry/...` for the structural relay guard changed under `cmd/`.
- `go vet ./...`
- `go build ./cmd/pyry`

## Open questions

None. The ticket fixes the resolver's ownership, nil posture, ordering, error vocabulary, audience, and concurrency path. Live-child resolution and correlation remain explicitly assigned to #2382.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md` to document `mcp_status_request`, its one-field `conversation_id` payload, the existing `mcp_status` answer, and the `protocol.malformed`, `conversation.not_found`, and `mcp_status.unavailable` rejects. State that #2381 declares the relay contract before #2382 wires the live-child resolver, and that a reply is requester-only and never enters the event ring.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `handleMCPStatusRequest` is the single payload-decode boundary; the capability gate precedes it, decode precedes membership, and membership precedes `MCPStatusFor`. Resolver output remains untrusted display data and is forwarded, not treated as authority.
- [Tokens, secrets, credentials] No findings — this read verb creates, stores, rotates, or exposes no credential. Authentication remains the existing Noise handshake; the design adds no owner-device actuator gate because it performs no actuation.
- [File operations] Not applicable — neither the handler nor its resolver contract accepts or constructs a path or performs file I/O.
- [Subprocess / external command execution] OUT OF SCOPE — this slice does not contact a child. Ticket #2382 owns the live-child query and exact response correlation.
- [Cryptographic primitives] No findings — no primitive changes. `forwardToRun` and `forwardAppReply` keep every Noise seal on the manager's single owner; direct worker-side `forwardEnvelope` is forbidden.
- [Network & I/O] No findings — the request inherits the existing v2 application-envelope size limit and bounded per-connection `appFrames` queue. A blocked resolver occupies one connection's worker only, and its context contract supplies the shutdown path.
- [Error messages, logs, telemetry] No findings — all client messages, log reasons, and codes are static. Decoder errors, payload bytes, both conversation-id sources, server fields, counts, and resolver-returned values are excluded from logs.
- [Concurrency] No findings — no new goroutine, channel, lock, or lock-order edge. Cross-connection liveness is preserved by the existing per-connection worker, while replies re-enter Run before encryption. Resolver cancellation and teardown/drop behavior are explicit.
- [Threat model alignment] No findings — unpaired peers remain outside the Noise-open application path; non-interactive peers learn nothing from the verb; paired interactive peers receive only a requester-addressed read result; remote/Claude-authored text is neither logged nor executed; no event-ring or broadcast path widens its audience.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-12

## Revisions

None.
