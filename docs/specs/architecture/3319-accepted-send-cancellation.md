# Accepted send cancellation (#3319)

## Files read

- `internal/transport/wssclient.go` → `SendContext`, `enqueueSend`, `sendPump`, `serve`: admission, acknowledgement and socket generation ownership.
- `internal/transport/send_context_test.go` → `TestClientSendContextDelivery`, `TestClientSendContextPumpShutdown`: existing blocked writer and shutdown proofs.
- `internal/transport/wssclient_test.go` → `newTestRelay`, `newClientForTest`: real socket and reconnect fixtures.
- `internal/relay/v2session_hosted_transport_test.go` → `testHostedPumpGate`, `testHostedRealTransport`, discovery ordering/retirement tests: accepted-before-write gate and publication fence.
- `internal/relay/v2session_hosted_api_transport_test.go` → `TestHostedAPIRealTransport`: cancellation before gate release and no late output.
- `internal/relay/v2session_hosted_asset_retirement_test.go` → `TestHostedAssetRealTransportBlockedWriter`: equivalent asset proof.
- `docs/knowledge/features/transport-package.md` § Lifecycle and goroutines: completion joins and no migration to reconnect.
- `docs/knowledge/features/v2-session-manager-concurrency.md` § Hosted discovery and ordered delivery: sealing order and resource ownership.
- `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: distinguish baseline regressions and read executed counts.
- `CODING-STYLE.md` and `docs/protocol-mobile.md` § Security model: concurrency and encrypted publication constraints.

## Context

An accepted request can be cancelled while the pump pauses at its acceptance event. The WebSocket library can publish despite an already cancelled write context; context cancellation alone is insufficient as a publication fence. Both hosted tests already wait for peer socket retirement, so the repair belongs in transport. No decision record is needed.

Sizing: one deliverable, approximately 300 written lines including plan and tests; zero exported types/interfaces, zero consumer migrations, four acceptance criteria and at most four reject branches. This remains within the refiner's S estimate and all builder limits. No overlapping feature branch touched the planned files at the initial fetch.

## Design

Keep the shared unbuffered admission path and `SendContext(ctx, frame) error` contract. An accepted request for the current socket checks caller and pump cancellation before socket writing, including after the acceptance event. Dispose a cancelled accepted request without writing, close that exact socket generation, acknowledge once with a non-nil sanitized error, and exit the pump. A failed socket write also retires that socket before acknowledgement. A mismatched request identity is acknowledged as disconnected without closing the new socket. Pre-admission cancellation remains local to the caller.

Relay changes are limited to safe assertion diagnostics if needed: preserve cancellation while the gate is held, resource-idle checks, release, socket retirement, then zero sealed output. Do not print encrypted frames.

## Concurrency model

No new production goroutines or locks. `serve` owns and joins its existing three pumps. The send pump owns accepted requests and their single buffered completion; caller cancellation never returns early from this join. Continue cancelling active writes with `context.AfterFunc`, but synchronously inspect the pump context before writing so callback scheduling cannot hide shutdown. Regression fixtures release every gate and join sender/connect goroutines during cleanup.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Caller cancellation after acceptance, before writing | `TestClientSendContextAcceptedCancellation/cancel` |
| Caller deadline after acceptance, before writing | `TestClientSendContextAcceptedCancellation/deadline` |
| Pump shutdown with accepted request and live caller | `TestClientSendContextAcceptedCancellation/shutdown`, existing `TestClientSendContextPumpShutdown` |
| Reconnect after accepted cancellation; old request cannot resume | `TestClientSendContextAcceptedCancellation/cancel`, `/deadline` |
| Cancellation before admission preserves healthy socket | `TestClientSendContextPreAdmissionCancellation` |
| Hosted API/asset cancellation while gate held | `TestHostedAPIRealTransport/blocked_true`, `TestHostedAssetRealTransportBlockedWriter` |
| Uncancelled delivery, shared ordering, retirement before admission | existing API/asset and discovery real-transport tests |

## Error handling

Preserve cancellation/deadline identity and `safeTransportError` sanitization. Best-effort `CloseNow` disposes the affected socket; the pump's error makes `serve` drain and retire the generation. Never retry the accepted request. Pre-admission and mismatched-generation failures retain their existing classifications.

## Testing strategy

First add a controlled acceptance gate regression and run it against unchanged production code; require a publication or retirement failure. After repair, run the new scenarios and existing transport proofs with race detection. Run the API blocked subtest and asset blocked-writer proof 150 times each and record executed/pass/fail counts without payloads. Run `go test -race ./internal/transport/... ./internal/relay/...`, `go vet ./...`, and `go build ./cmd/pyry`.

The builder role forbids duplicating `make check` and the full-module race suite. The dispatcher/verifier must run the ticket's full hermetic `make check` followed by `make build` on the pushed candidate. No live Claude check is required.

## Open questions

None: the accepted-before-write cancellation cut is identified; regression evidence will establish baseline discrimination.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/transport-package.md`, **Lifecycle and goroutines**, to state that cancellation of an accepted context send before socket writing disposes the request without publication, acknowledges completion and retires its socket generation; pre-admission cancellation does not retire an otherwise healthy socket. Preserve ordered admission and the prohibition on migration to a reconnect.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `sendPump` remains the single application socket writer; accepted caller/pump cancellation must be checked before publication and retires only `req.conn`'s matching generation. This is the repair specified in Design.
- [Tokens/secrets] No credential creation or storage. Test diagnostics report late-frame presence only; no ciphertext or headers are logged.
- [Files] No production file operations change; no user-controlled paths enter this design.
- [Subprocesses] No production subprocess changes or shell input are introduced.
- [Cryptography] No key/nonce changes. Socket retirement prevents continuing a stream after discarding a sealed frame, preserving the relay's Noise ordering boundary.
- [Network/I/O] Existing frame limits, TLS policy and write deadlines remain. `CloseNow` must run before error acknowledgement so a cancelled pre-write request cannot leave its socket live.
- [Errors/logs] Preserve `safeTransportError` and fixed acceptance event; do not retain raw WebSocket errors in completion.
- [Concurrency] Synchronous pump-context checks supplement the asynchronous cancellation callback. Every accepted request completes once; existing `serve` joins remain intact.
- [Threat model] Addresses implementation-bug publication/replay ordering from `docs/protocol-mobile.md` § Security model. Authentication, pairing, rate limiting and metadata privacy are unchanged by this transport repair.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-13
