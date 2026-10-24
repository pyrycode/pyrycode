# Reliable retired-agent reconnect proof

## Files read

- `internal/e2e/relay_v2_agent_content_retired_test.go` → `TestRelayV2_AgentContentRetiredReconnect`: retained roster, producer close and both reconnect modes.
- `internal/e2e/internal/scenario/source_observer.go` → `SourceObserver.Next`: disk retention is independent of completed transport delivery.
- `internal/e2e/internal/fakerelay/fakerelay.go` → `handlePhone`, `handleBinary`, `WaitBinary`, `ForceCloseBinary`: close notice, registration lifetime and HTTP 503.
- `internal/e2e/internal/fakerelay/retirement_test.go` → `retirementGate`: controlled delay between joined pumps and deregistration.
- `internal/relay/v2session.go` → `handlePeerClose`, `teardown`: phone departure retires its delivery lane.
- `internal/relay/v2session_hosted.go` → `hostedDeliver`, `retireHosted`: connection cancellation reaches outstanding acknowledged delivery.
- `internal/relay/v2session_hosted_transport_test.go` → `testHostedRealTransport`, `testHostedPumpGate`: real transport admission can be held deterministically.
- `internal/transport/wssclient.go` → `sendContext`, `sendPump`: cancelled accepted ciphertext retires the binary socket before acknowledgment.
- `docs/knowledge/features/e2e-harness.md` § Readiness Signal; `fakerelay-harness.md` § Surface: readiness is scoped to a registration, not future liveness.
- `docs/knowledge/features/transport-package.md` § Wire contract: retiring accepted cancelled output is intentional Noise fencing.
- `docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence-retirement.md` § Shadow lifecycle and evidence: preserve retained roster and explicit catch-up assertions.
- `docs/knowledge/features/relay-package.md`, `development-verification.md`, `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: routing, evidence and security boundaries.

## Context

At `6536abd938`, the #3857 gate logged producer departure, binary generation 1
deregistration and reconnect HTTP 503 within two milliseconds. Its same-tree rerun
passed. The fixture assumes phone closure leaves the binary registration available.
Source retention does not guarantee that all transport deliveries have completed.
The production cancellation contract deliberately permits binary retirement when a
phone departure cancels an accepted encrypted write. Preserve that safety contract.
Confirm this causal path with held real transport admission and retain the original
failure/rerun evidence in the issue. No decision record is needed.

## Design

Make the content-retirement fixture explicitly cross a binary transport generation
boundary after each phone departure, while keeping the same daemon and retained
history. Add `Server.ReconnectBinary(ctx, serverID) bool` in a separate fakerelay
file: capture the currently registered binary under the mutex, force-close that
exact socket if present, then wait for a different nonempty registration. If the
phone departure already removed it, wait for its successor. Never accept the old
registration, retry phone authentication, or change production cancellation.
Observe cancellation before side effects and on each bounded wait. One caller in
the reconnect loop uses this barrier before dialing; all existing protocol
assertions remain. A failed dial also reports captured daemon diagnostics.

One useful result: reliable reconnect content-retirement proof. Estimate ~400 total
written lines including plan and controlled tests, one exported method, one consumer,
four acceptance criteria, fewer than ten reject branches. No concurrent feature
branch overlaps the intended files. #3240 touches manager teardown instrumentation
only; this ticket adds a focused test beside existing real-transport tests.

## Concurrency model

The helper snapshots registration identity under `Server.mu`, closes outside the
lock, then observes identity under the lock using a stopped-on-return ticker.
Existing relay handlers own registration removal and replacement. No production
goroutine is added. Test goroutines have bounded contexts, release cleanup and joins.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Phone closes while an encrypted write is accepted | `TestPeerCloseAcceptedDeliveryRetiresTransport` holds pump admission, sends peer close, then proves socket retirement |
| Old pumps stopped but old registration retained | `TestReconnectBinaryRetiringRegistration` holds deregistration and rejects old identity |
| Same server ID registers a successor, including repeated reconnect | `TestReconnectBinaryRetiringRegistration` routes a frame through each replacement |
| No registration, cancellation, deadline, server shutdown | `TestReconnectBinaryBounds` proves bounded termination |
| Producer closes, interactive reconnect, thread reconnect | `TestRelayV2_AgentContentRetiredReconnect`, at least 20 uncached executions |

## Error handling

The fixture helper returns false on context expiry or relay shutdown; the caller
fails with context and daemon diagnostics. A missing registration is transitional,
not permission to skip the reconnect. Socket retirement remains best effort while
observed successor registration is mandatory. No sleeps, timeout increases or dial
retries substitute for the registration transition.

## Testing strategy

Write the helper regression first and see it fail without the new barrier. Hold old
deregistration, prove it cannot satisfy readiness, release it, register a successor
and verify actual frame routing. Exercise cancellation/deadline/shutdown. Separately
force an accepted real transport write to overlap peer teardown and prove the
intentional socket retirement causally. Run focused race tests through admission,
20 uncached target e2e executions, `go vet ./...` and a build to scratch. Full-module
unit and fake-daemon acceptance remain pending for the dispatcher on Linux.

## Open questions

- Confirm that the held-admission peer-close sequence accounts for the historical
  binary loss. If evidence instead identifies another defect, revise before repair.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The helper exists only inside the fake integration relay; it takes the fixture's server ID and never alters device authorization or parses app content. `handleNoiseInit` remains the authentication boundary.
- [Tokens and credentials] No new credential handling. Failure diagnostics use the existing daemon's sanitized logs; tests must never log pairing tokens, private keys or plaintext wire bodies.
- [File operations] No new product file operations. Existing isolated fixture homes retain their permissions and cleanup.
- [Subprocesses] Existing harness owns and joins the daemon process group; no new commands or inherited environment are introduced.
- [Cryptography] MUST preserve `sendPump`'s accepted-cancellation socket retirement. The design restarts routing transport rather than relaxing nonce fences or replaying ciphertext.
- [Network and I/O] Existing fake relay and phone retain their frame limits. The helper force-closes only the captured loopback fixture socket and waits under the caller deadline. Production HTTP/TLS policy is untouched.
- [Errors and logs] Add only content-free assertions and existing sanitized daemon diagnostics; never copy request headers into logs.
- [Concurrency] Identity checks and map access use `Server.mu`; close and waits run outside that lock. Cleanup releases every held test gate and joins worker completion.
- [Threat model] Relay denial of service and metadata exposure retain the protocol's existing residual risks. Noise authentication, replay protection and revocation are unchanged; no new product threat is introduced.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-24

## Revisions

- 2026-10-24: The first causal test attempt timed out waiting for a handshake
  acknowledgment callback. The minimal real-transport harness sends that handshake
  through admission-only `Outbound`; it does not call `OutboundContext`. The held
  scenario now sends an unsupported `list_apps` request to enter the acknowledged
  delivery lane on a peer without hosted-app capability, then closes the peer
  while that exact write is accepted. The production cancellation contract and
  fixture-repair design are unchanged. Preserve the failed setup log as evidence.
