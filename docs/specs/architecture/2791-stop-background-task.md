# #2791 — declare and route stop_background_task

## Files read

- `internal/protocol/codes.go`, `messaging.go`, `handshake.go` → type constants, `InterruptPayload`, `ErrorPayload`: wire contract and correlation provenance.
- `internal/protocol/compat_test.go`, `messaging_test.go`, `interactive_test.go` → v2 partition guards and `roundTripEnvelope`: fixture must re-marshal decoded fields.
- `internal/relay/v2session.go` → `dispatchAppFrame`, `appFrameWorker`, `forwardToRun`: Run owns gates and sealing; workers own blocking work.
- `internal/relay/v2session_seams.go` → `MCPActuator`, `V2SessionConfig`: consumer interfaces and true-nil posture.
- `internal/relay/v2session_mcpactuate.go`, `v2session_mcpstatus.go` → MCP routing analogue and requester-only reply helper.
- `internal/relay/v2session_mcpactuate_test.go`, `v2session_modal_test.go`, `v2session_interrupt_test.go` → existing handshake, reply and interrupt fakes.
- `cmd/pyry/relay_guard_test.go`, `relay.go` → inbound classification and shipped config construction.
- `docs/knowledge/INDEX.md`, `features/protocol-package.md`, `features/relay-package.md`, `features/v2-session-manager.md`: owning topics.
- `docs/knowledge/features/v2-session-manager-concurrency.md`: interface typed nil defeats a nil gate; leave the config field unset.
- `docs/knowledge/features/v2-session-manager-test-surface-same-package-unit-tests-internal-relay.md`: test raw decoder-error shapes as well as sentinels.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: remarshal typed payload and explicitly check distinct ids.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: Go conventions and authenticated encrypted transport threats.

## Context

A paired client's per-task stop button needs a defined inbound contract before #2792 wires child actuation. This ticket ships one deliverable: the inert-by-default relay verb and its refusal contract. No new capability or production seam implementation. No decision record required.

Sizing: approximately 600–700 written lines including plan, three exported types/interfaces, zero existing consumer signature changes, five acceptance criteria, fewer than ten reject/drop branches. No overlapping feature branches were found for the existing files touched.

## Design

Declare `TypeStopBackgroundTask`, `CodeStopBackgroundTaskRefused`, and `StopBackgroundTaskPayload` with required string keys `conversation_id` and `task_id`. Classify the verb as v2-only in compat guards and switch-intercepted in the cmd guard; v1 continues returning unknown type.

Declare consumer interface `BackgroundTaskStopper` with `StopBackgroundTask(ctx context.Context, conversationID, taskID string) BackgroundTaskStopOutcome`. The outcome enum distinguishes refused (zero value), accepted, and cannot-act-on-conversation. Implementations own conversation validation/resolution and treat ids as untrusted lookup keys. Add an optional config field of that interface type, left as a true nil interface by every shipped construction.

`dispatchAppFrame` consumes the type before v1 routing. Nil seam and missing negotiated interactive return on Run before typed payload decode or enqueue. Otherwise enqueue a dedicated worker job. The handler drops absent/null/undecodable payloads and empty conversation ids, with no cursor fallback or membership check. A nonempty conversation with empty task id produces refusal before the seam. Other valid requests cross the seam once; only refused outcomes reply.

Refusal is exactly one requester-only error: code `stop_background_task.refused`, message `background task stop refused`, retryable false, requested conversation id and request-envelope correlation. Reuse the existing requester reply lane. Update `ErrorPayload` provenance comments to permit this narrow correlation exception while retaining daemon-authored-only provenance for other producers.

## Concurrency model

Use the existing bounded per-connection app-frame queue and worker; add no goroutines or shared mutable state. Pass the worker's cancellation context into the seam. A blocking seam must honor cancellation; manager shutdown cancels its wait. Other connections and inline interrupt continue on Run. All errors go through `forwardToRun` and Run seals them; the worker never touches CipherState.

## Error handling

Malformed typed payloads silently drop. Empty task ids and refused outcomes share the fixed error shape. Accepted and cannot-act outcomes remain silent. No remote ids, payloads, decoder errors or child diagnostics reach logs. Marshal/forward failures use generic diagnostics from the existing reply lane.

## Testing strategy

Write tests first and observe failure before implementation. Commit a JSON fixture and typed round trip with distinct ids; extend literal-code and v2 partition guards. Exercise inert gates, malformed/null/absent payloads, missing ids, all seam outcomes and verb interception using fake seams. Use a same-worker barrier to prove silent outcomes completed. Refusal checks assert exact payload JSON, correlation, one reply, and no peer publication. Capture logs and check decoder shapes and remote sentinels. A blocking seam proves other-connection refusal, inline interrupt, eventual Run-sealed refusal and cancellation on manager shutdown. Run race tests on protocol, relay and cmd/pyry, then go vet and build.

## Open questions

None. Conversation actuation and capability advertisement belong to #2792.

## Documentation handoff

Pending for the documentation stage / #2792: `docs/protocol-mobile.md` message-type table, stop-background-task contract section and Error codes must document the verb, payload, interactive requirement, inert-until-wired state, silent outcomes and exact refusal/correlation shape. #2792 owns the final capability advertisement and production protocol documentation.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The typed decode in `handleStopBackgroundTask` is the only payload boundary. Conversation and task ids remain untrusted lookups; the seam owns validation, with no cursor fallback. Missing task refusal reflects correlation only, including unknown conversations.
- [Tokens] No token generation/storage changes; the existing paired Noise handshake authenticates callers. Request ids and payloads never enter new logs.
- [File operations] No paths, filesystem operations or persistence; ids are never interpreted as paths here.
- [Subprocesses] OUT OF SCOPE: #2792 implements child actuation and must preserve lookup-only ids.
- [Cryptography] Existing Run-owned sealing is reused; no new crypto or nonce writer.
- [Network and I/O] Reuse existing bounded frame intake and per-connection queue; inert gates stay before enqueue. No new listeners or unbounded buffers.
- [Errors/logs] Fixed refusal only, requester-only delivery, no task id or raw error. The reflected conversation id is a deliberate `ErrorPayload` provenance exception, never a membership claim or log field.
- [Concurrency] Existing worker shutdown via context and session done; seam must honor its cancellation context. No new locks or goroutines. Blocking work is isolated from Run.
- [Threat model] Paired devices may stop without tool-permission gating by contract. Nil production seam prevents actuation; typed nil must never be assigned. Replay/MITM protections and size caps remain the existing Noise/transport boundaries; broader rate limiting remains protocol-level deferred work.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-04
