# #3845 Ordered producer-gap close and accepted-turn recovery

## Files read

- `internal/relay/v2session_livestate.go` → `RetireLiveState`, `pushRetired`: first-cause quarantine before Run teardown.
- `internal/relay/v2session_push.go` → `handlePushOverflow`, `drainOnce`: terminal cause and pre-seal admission.
- `internal/relay/v2session_hosted.go` → `hostedDeliver`, `handleHostedDone`, `retireHosted`: one active bounded write and cancellation.
- `internal/relay/v2session.go` → `Run`, `closeWith`, `teardown`: completion wake, timeout sweep and terminal adapter.
- `internal/relay/v2session_hosted_changes.go` → `hostedCloseContext`: replacement-generation cancellation.
- `internal/relay/connection.go` → `SendContext`: ordered transport adapter.
- `internal/transport/wssclient.go` → `sendPump`: admitted cancellation retires the shared socket.
- `internal/relay/v2session_hosted_transport_test.go` → `testHostedPumpGate`: hold acceptance before actual write.
- `internal/relay/v2session_recovery_test.go` → `TestThreadRecoveryAfterReconnect`: explicit reset/window recovery and consistent continuation bounds.
- `internal/e2e/relay_v2_handshake_test.go` → `startV2Harness`: authenticated real-socket fixture.
- `internal/e2e/internal/fakerelay/fakerelay.go` → `binaryRecvPump`, `phoneSendPump`: frame-before-close routing.
- `docs/knowledge/features/transport-package.md` → Test surface: cancellation must still retire a failed sealed stream.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md` → Retirement quarantines before teardown: no dependent drain or nonce spending.
- `docs/knowledge/features/v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md` → bounded delivery and replacement fencing.
- `docs/knowledge/features/relay-package.md`, `docs/knowledge/features/e2e-harness.md`, `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`, `docs/protocol-mobile.md`: owning contracts and test conventions.

## Context

The retained #3740 log shows gap retirement followed by binary disconnect and failed terminal delivery. Teardown cancels hosted output; cancellation after transport admission closes the binary socket, stranding the following terminal envelope and unrelated phones. The controlled proof must distinguish that boundary from relay routing. This is one behavior: retiring a genuinely discontinuous consumer while preserving accepted-turn recovery. Its regression and recovery proof stay with the repair. Ordinary continuity repair remains #3844. No decision record is needed.

Size: approximately 700 written lines including plan, tests and local helpers; zero new exported types, zero changed consumer signatures, three acceptance criteria, fewer than ten reject branches. The verification floor keeps close and accepted-turn recovery together. Recount before plan commit gives the same limits. Remote #3240 overlaps `v2session_push.go` in separate instrumentation blocks; build through with local edits. #3740's helper migration is unnecessary for the new local fixture.

## Design

`handlePushOverflow` defers genuine-gap teardown while an already-selected hosted write is still live. The existing deadline and completion wake bound this wait. `hostedDeliver` checks the retirement latch before selecting any further output and resumes terminal handling instead. Completion preserves the latched terminal cause even on delivery failure. The timeout sweep uses that same cause for a retired queue. Actual overflow keeps its immediate 4413 behavior.

After the selected write completes, ordinary `closeWith` removes the session and sends the terminal envelope through `Connection.SendContext`. No transport cancellation semantics, authentication gates, query protocol, or publication continuity changes. Reconnect uses a new Noise session, current transient reconciliation, and explicit named catch-up; accepted input is never replayed by this path.

## Concurrency model

Run continues to own sessions, cipher state and selection. Producers only latch queues under `pushMu`. Existing hosted and terminal sender goroutines keep their deadlines and Run cancellation. Waiting for a selected write never blocks Run or unrelated peers. No new goroutine or lock order is introduced.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Gap while selected output is admitted but unwritten; repeated retirement; pending dependent output | `TestProducerGapOrderedClose` and `TestRelayV2ProducerGapRecovery` |
| Selected write fails or times out; transport unavailable | `TestProducerGapDeliveryFailure` plus existing hosted transport cancellation tests |
| Fresh authenticated reconnect, retained checkpoint and reset/window recovery, no accepted-input replay | `TestRelayV2ProducerGapRecovery` |
| Reused routing ID fences old terminal work | existing `TestHostedLifecycleReconnect` |
| Overflow before gap or gap before overflow | existing `TestPushRetirementCause` |

## Error handling

A failed or expired active delivery permits bounded teardown with the original terminal cause; terminal delivery failure stays a failure. Unavailable transport cannot claim receipt. Replacement handshakes cancel old terminal contexts. Queue quarantine suppresses all subsequent selection, including detached current readings, before encryption.

## Testing strategy

Write a held-pump regression first and retain its failing JSON log on the unchanged production revision. Verify actual phone 4414 receipt, no dependent output, and an unrelated admitted peer using the same binary socket. Exercise explicit catch-up from a completed first checkpoint after one accepted second input, with retained and reset/window recovery, source identity, completion and current idle. Use existing fake fixtures and focused Linux race admission. Run affected relay/transport tests, the focused offline integration, `go vet ./...` and `go build ./cmd/pyry`; the dispatcher owns full acceptance. Retain commands, exact revisions and counted results in the PR/evidence.

## Open questions

Resolve the precise held-write failure in the regression before implementing; resolve fixture source and recovery assertions during the test build without changing the product contract.

## Documentation handoff

- Pending documentation stage: `docs/protocol-mobile.md`, **Daemon thread updates (v2, supplied delivery contract)**: ordered 4414 delivery on available transport, bounded failure on unavailable transport, reconnect/catch-up without resending accepted input.
- Pending documentation stage: `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md`, **Retirement quarantines before teardown**: proved delivery boundary and regression.

## Security review

**Verdict: PASS.** Builder self-review, 2026-10-23.

- Trust boundaries: `hostedAuthorized`, thread eligibility and authenticated Noise dispatch remain prerequisites; the repair admits no new plaintext.
- Tokens/secrets: no credential generation, storage or logging changes. Logs retain content-free discriminants.
- Files/subprocesses: no production changes; fixtures retain existing isolated temporary storage and child cleanup.
- Cryptography: Run remains the only cipher owner. Only the already-selected sealed frame can complete; quarantined output consumes no further nonce. Do not weaken transport cancellation retirement.
- Network/I/O: reuse bounded hosted delivery, terminal deadlines and transport frame limits. A stalled socket cannot create an unbounded graceful wait.
- Errors/logs: unavailable terminal delivery remains logged as failure, never proof of phone receipt.
- Concurrency: completion and timeout both re-enter first-cause handling; replacement terminal contexts remain fenced by `hostedCloseContext`.
- Threat model: untrusted relay still carries only encrypted content; this change neither extends device privileges nor trusts client checkpoint/source assertions. No new security finding is deferred.
