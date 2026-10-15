# Dormant restart relay readiness

## Files read

- `internal/e2e/relay_v2_stream_new_session_dormant_test.go` → both dormant restart proofs and `dialPairedPhone`: preserve reset-first, persisted binding, predecessor input and A isolation assertions.
- `internal/e2e/harness.go` → `RestartStreamInteractiveWithRelay`, `Harness.Stop`: process collection is not relay deregistration; restart retains HOME and registry.
- `internal/e2e/relay_v2_daemon_test.go` → `waitBinaryHello`: readiness currently means map membership.
- `internal/e2e/internal/fakerelay/fakerelay.go` → `handleBinary`, `serveBinary`, `handlePhone`, `WaitBinary`: registration survives pump shutdown until handler cleanup.
- `internal/e2e/internal/fakerelay/fakerelay_test.go` → raw binary/phone dial helpers and bounded round trips.
- `docs/knowledge/features/fakerelay-harness.md` → registration barrier: client upgrade receipt precedes server map insertion.
- `docs/knowledge/features/e2e-harness.md` and its stream-interactive topic: joined process cleanup and readiness distinctions.
- `docs/knowledge/features/development-verification.md` → controlled evidence cannot attribute an unobserved historical failure.
- Originating #3467 verifier evidence → baseline EOF preceded M4; no historical relay phase evidence establishes cause.

## Context

The same relay and server ID survive daemon restart. Daemon process exit does not
join the relay handler. `WaitBinary` can therefore observe daemon one's retiring
registration when the phone needs daemon two. Demonstrate this dependency without
claiming it caused the historical macOS EOF. No production change or ADR is needed.
Remote feature branches checked: none overlap the two existing files to change.

## Design

Add `Server.WaitBinaryOffline(ctx, serverID) bool` to the test-only fakerelay:
return true only after registration absence is observed before cancellation.
Both dormant tests stop daemon one, await relay absence with the existing five
second readiness bound, then start daemon two and use the unchanged positive
registration barrier. Because daemon two does not start until absence, an old
registration cannot satisfy that barrier. No general helper signatures change.

Add content-free binary generation/lifecycle diagnostics and phone upgrade/Noise
phase logs. A controlled real-WebSocket regression blocks the retiring handler
after its pumps join but before deregistration, proves old membership still
admits a doomed phone, and requires the offline barrier to reject that state.
After release, reuse the ID and prove replacement routing. Separate deadline and
cancellation scenarios cover a missing retirement. No fixed sleeps or EOF retries.

## Concurrency model

The relay retains its existing mutex and joined receive/send pumps. The barrier
polls under that mutex on a stopped-on-return ticker and caller context. The
regression's logging gate is outside the relay mutex, has bounded release and is
released in cleanup before server shutdown. Existing daemon cleanup remains joined.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Pump exit with old registration retained; premature phone upgrade | `TestWaitBinaryOfflineRetiringRegistration` |
| Deregistration then same server ID registered again | `TestWaitBinaryOfflineRetiringRegistration` |
| Missing retirement, deadline, cancellation, already absent | `TestWaitBinaryOfflineBounds` |
| Restart with same HOME, dormant reset then successor input | `TestRelayV2_StreamNewSessionAfterDaemonRestartResetsDormantConversation` |
| Restart with same HOME, dormant typed clear | `TestRelayV2_StreamClearAfterDaemonRestartResetsDormantConversation` |

## Error handling

Missing deregistration fails within five seconds before starting daemon two.
Handshake failures remain fatal with phase evidence; they are never retried away.
The controlled gate releases on timeout as well as cleanup to avoid hanging joins.

## Testing strategy

Write the controlled regression first and run it red against a membership-only
barrier, then implement absence coordination. Run at least 20 uncached repetitions
of both dormant tests and the regression with race, GOMAXPROCS=2 and GOFLAGS=-p=2.
Record unchanged-fixture repetitions too. Run touched test packages with race,
`go vet ./...`, and build. The issue explicitly requests full e2e and make
check/build evidence; record complete versus stopped outcomes separately.
Keep default package timeouts. Request managed macOS evidence rather than treating
Linux/amd64 Go 1.26.9 results as macOS/arm64 Go 1.26.2 results. A platform probe
establishes availability only. Retain exact commands, revisions and counts here.

## Open questions

- Historical attribution remains unknown unless the original ordering recurs.
- Native macOS candidate execution route: requested platform probe; its fixed
  recipe cannot itself execute this ticket's custom checks.

Sizing recheck: one deliverable, four acceptance criteria, about 350 written lines,
zero exported types, one exported fixture method, two updated consumers, fewer
than ten failure branches. No in-flight dependency.

## Security review

**Verdict:** PASS

- Trust boundaries: only test fixtures change; raw sockets use existing header and frame validation in `handleBinary` and `handlePhone`.
- Tokens: lifecycle logs contain generation, phase and routing identity only; pairing tokens, keys and payloads are excluded.
- File operations: no persistence or path behavior changes; existing isolated HOME and registry ownership remain.
- Subprocesses: existing harness launch and joined process-group shutdown remain; no new subprocess commands in product code.
- Cryptography: Noise helpers and nonce order stay unchanged; the raw relay regression uses JSON routing, not altered cryptographic behavior.
- Network/I/O: existing loopback listeners and frame caps remain; new waits and controlled gates have finite contexts.
- Errors/logs: no payload or credential diagnostic capture is introduced.
- Concurrency: no locks held across the controlled gate; polling reads under `Server.mu`, and cleanup releases before joining handlers.
- Threat model: production relay/mobile authentication and authorization contracts do not change; no newly deferred threat.

**Reviewer:** builder self-review, 2026-10-15.
