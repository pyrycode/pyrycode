# Accepted-send cancellation and peer identity (#3496)

## Files read

- `internal/transport/wssclient.go` → `Client.sendContext`, `sendRequest`, `Client.sendPump`, `Client.serve`: bounded admission, disposal acknowledgement and exact-socket retirement.
- `internal/transport/send_accepted_test.go` → `TestClientSendContextAcceptedCancellation`, `testAcceptedSendGate`: accepted-write barrier and peer retirement fixture.
- `internal/transport/send_context_test.go` → `TestClientSendContextDelivery`, `TestClientSendContextPumpShutdown`: I/O cancellation and shutdown coverage.
- `internal/transport/generation_test.go` → `TestGenerationAdmission`: stale-generation rejection and reconnect coverage.
- `docs/knowledge/features/transport-package.md` § Lifecycle and goroutines, § Test surface: cancellation before admission must preserve the healthy connection; disposal after acceptance retires the exact socket.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change, § Test execution and artifact survival: controlled schedules and named counts are required.
- `CODING-STYLE.md`: bounded goroutines, contract comments and scoped changes.
- `docs/protocol-mobile.md` § Security model: cancelled sealed frames must not migrate across Noise streams.

## Context

Baseline `4d98f075` reproduces retirement stalls on Linux. The fixture numbers
connections after `websocket.Accept`; its first handler can register after a
reconnect handler, so `retired` can identify the replacement socket. Separately,
closing a caller's context does not synchronously close all descendant contexts:
the deadline waiter can release the pump before cancellation propagates to its
write context. A derived-context check alone is not a caller-cancellation fence.
No decision record is needed for these local ordering repairs.

## Design

Keep the original caller context on admitted requests alongside the existing
timeout-bounded context. `sendPump` checks the caller context directly before its
existing write/pump/generation checks, preserving cancellation/deadline identity
without waiting for descendant propagation. Admission timeout and ordered disposal
remain unchanged; failed accepted sends close their captured socket before ack.

The accepted-send fixture explicitly acknowledges original peer registration
before submission. Keep every disposal, error, zero-publication and reconnect
assertion; additionally compare generation identities. Add a test-only context
wrapper implementing `AfterFunc` which holds descendant cancellation until
released. Exercise that controlled schedule in cancel and deadline modes, with
the caller already cancelled while both derived write contexts remain live.
Shutdown retains its independent pump-context check. Use the wrapper in the named
test so the required repetitions include the regression on every iteration.

Sizing: one deliverable; approximately 250 written lines including plan and
evidence, zero exported types, no consumer changes, four acceptance criteria,
one additional reject check. No concurrent feature branch touches the two target
transport files at planning time.

## Concurrency model

Production retains the existing three pumps and joined `serve` lifetime. The
test context's cancellation callback waits on an explicitly released gate and
signals completion; cleanup releases and joins it. Sender and Connect goroutines
retain bounded joins. Peer registration closes a one-shot channel before a send
can trigger retirement or reconnection.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Accepted caller cancellation while descendant propagation is held | `TestClientSendContextAcceptedCancellation/cancel` |
| Accepted caller deadline while descendant propagation is held | `TestClientSendContextAcceptedCancellation/deadline` |
| Accepted request during pump shutdown | `TestClientSendContextAcceptedCancellation/shutdown` |
| Disposal before completion and original peer retirement | all three modes above |
| Reconnect with distinct generation and no old-request migration | cancel and deadline modes above |
| Cancellation before admission keeps socket usable | `TestClientSendContextPreAdmissionCancellation` |

## Error handling

Return caller cancellation/deadline errors through the existing sanitizer; do not
expose caller data or raw socket errors. Keep shutdown non-nil and stale-socket
rejection unchanged. Fixture timeouts fail and run bounded cleanup, without retries.

## Testing strategy

First run the controlled descendant-propagation regression against unchanged
production and record its failure. Separately force delayed original handler
registration on a scratch overlay to verify the retirement-identity diagnosis.
Then run the fixed named test with `go test -race -json -count=100 -run
'^TestClientSendContextAcceptedCancellation$' ./internal/transport` and the full
package with `go test -race -json -count=20 ./internal/transport`, recording
revision, Go version, concurrency, named counts and timing. Run `go vet ./...`
and `go build ./cmd/pyry` on Linux. The dispatcher owns the ordinary `make check`
gate; its result remains pending until that stage. Other OS checks are outside
this ticket's scope.

## Open questions

- Confirm both proposed causes with controlled schedules before handoff.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/transport-package.md`,
**Lifecycle and goroutines** and **Test surface**, record any durable invariant
established by the repair: the cancellation/disposal/retirement ordering and the
synchronization that proves the test is observing the affected connection.
Describe only a demonstrated cause; retain the distinction between pre-admission
cancellation and accepted-request disposal, and the prohibition on migration to
a reconnect.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `sendPump` retains socket identity checks and only sends admitted bytes; checking the original caller prevents publication during delayed cancellation propagation.
- [Tokens/secrets] No credential changes. Test payloads are fixed markers; no frame bytes enter diagnostics.
- [File operations] No production filesystem operations are added.
- [Subprocesses] No subprocess execution is added.
- [Cryptography] No primitives or nonce handling change. Exact-socket disposal preserves the existing sealed-frame stream boundary.
- [Network/I/O] Existing read caps, dial policy and write timeout remain intact. Loopback fixture registration changes no production network admission.
- [Errors/logs] `safeTransportError` continues rebuilding errors from supported sentinels; cancellation checks do not retain raw errors or payloads.
- [Concurrency] No production goroutines or locks are added. MUST FIX addressed in design: direct caller check must precede writing even when descendant cancellation is held. Fixture callback gates must release and join in cleanup.
- [Threat model] The repair addresses implementation ordering and replay/nonce-stream integrity in the existing transport boundary. Authentication, crypto and relay resource policy remain unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-16
