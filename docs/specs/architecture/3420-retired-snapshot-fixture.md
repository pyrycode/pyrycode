# Retired-snapshot connection and completion proof

## Files read

- `cmd/pyry/daemon_live_relay_test.go` → `TestDaemonLiveRelayRetiredSnapshot`, `testDaemonRelayWire`: current assertion and configuration seam.
- `cmd/pyry/agent_switch_frames_test.go` → `switchWire.open`, `read`: response receipt precedes admission; reads preserve Noise nonce order.
- `cmd/pyry/daemon_relay_wire_test.go` → `TestDaemonRelayWireQueuedPushDuringHandshake`: established peers must retain queued output.
- `cmd/pyry/daemon_live_relay_rework_test.go` → `TestDaemonLiveRelayPublisherConnectGap`: bounded callback synchronization pattern.
- `cmd/pyry/daemon_live_relay.go` → `daemonRelaySnapshot`, `daemonLiveCurrent`, `startDaemonLivePublisher`: detached readings and owner-removal eligibility.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: outbound response, queue registration, then cursor capture.
- `internal/relay/v2session_push.go` → `pushSource`, `drainOnce`: queue lookup can precede admission; queued pushes overtake snapshot reads.
- `internal/relay/v2session_livestate.go` → `drainLiveOnce`, `forwardLiveState`: cursor exhaustion follows evaluation of earlier entries.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: join producers before inserting an absence barrier.
- `docs/knowledge/features/v2-session-manager.md` and `v2-session-manager-concurrency.md`: Run owns cursor evaluation and ciphers.
- `docs/protocol-mobile.md` § Security model: stale-state and replay boundaries remain protected by authenticated wire assertions.

## Context

The fixture pushes immediately after receiving the handshake response. That response precedes push registration, so a successful handshake alone cannot establish that Push will succeed. Its queued marker can also overtake an unvisited snapshot and falsely prove absence. These are demonstrated fixture gaps; the historical failure's exact schedule is not known. No decision record is needed.

## Design

Keep changes local to the retired-snapshot test, using existing configuration callbacks. Hold the first outbound response after publication but before the callback returns. Once `open` receives it, prove one early Push returns `ErrConnNotFound`; do not retry it. Release the handshake, then hold the snapshot callback before capture to exercise delayed capture independently. Capture the real cursor, remove the owner, and signal completion of that setup. Wrap cursor iteration to count and validate the removed model reading and signal exhaustion. Only after exhaustion, enumerate the admitted phone and push the trailing marker. Decrypt output on that same phone, rejecting every model frame and accepting only the exact marker ID/type/payload.

No shared helper, production behavior, exported type, or consumer signature changes. No overlapping feature branch touches the target test file. Estimated written work including plan: about 180 lines; 0 exports, 0 consumer updates, 3 acceptance criteria, under 10 fixture failure branches.

## Concurrency model

Run alone invokes the outbound and snapshot callbacks and advances the cursor. Test goroutine owns wire cipher reads. Channel gates are bounded by a test-local timeout context, cancelled before fixture cleanup so held callbacks cannot strand Run. Cursor observations become visible after its exhaustion channel closes. Existing manager and publisher shutdown remain joined by `testDaemonRelayWire`.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Response published before phone push admission | `TestDaemonLiveRelayRetiredSnapshot` holds outbound return and requires early Push rejection. |
| Admission followed by delayed capture | Same test holds capture separately and observes setup completion. |
| Captured owner removed before cursor evaluation | Same test requires the actual removed reading to be visited and cursor exhausted. |
| Same phone remains usable after snapshot rejection | Same test requires admitted identity and decrypts its exact trailing marker. |

No reconnect or connection-ID reuse is introduced.

## Error handling

Each wait has a deadline. Unexpected early Push success, missing/empty snapshot, missing admission, failed barrier push, stale output or unrelated output fails the test. Callback timeout returns without blocking teardown. Production error handling is unchanged.

## Testing strategy

First run the strengthened schedule with the old immediate barrier placement and observe the admission failure. Then order the trailing marker after the preconditions. Run a negative control with `LiveStateCurrent` disabled to prove stale snapshot publication still fails. Run the required 100 focused race-enabled repetitions and the uncached daemon package suite, plus `go vet ./...` and `go build ./cmd/pyry` (binary output outside the worktree). The verifier owns the full-module race gate. Shared helpers remain unchanged; the daemon suite includes their existing regressions.

## Open questions

None. A controlled admission failure is evidence of a fixture race, not attribution of the historical failure to a proven production defect.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/development-verification.md`, under **Prove that tests distinguish the change**, extend the existing handshake/snapshot guidance to state that Noise response receipt also precedes push-queue registration, and that a retired-snapshot absence assertion must establish snapshot evaluation completion before ending at its identified wire barrier.

## Security review

**Verdict:** PASS

- Trust boundaries: real `switchWire.open` authentication and `read` decryption remain in use; no manufactured wire success bypasses them.
- Tokens/credentials: existing in-memory test tokens and random keys only; no new persistence or logging.
- File operations/subprocesses: the fixture adds neither filesystem access nor subprocess execution.
- Cryptography: existing Noise ciphers remain single-owner and output stays in nonce order; no primitive or key lifecycle changes.
- Network/I/O: bounded in-memory channels through `testDaemonRelayWire`; callback waits use cancellation and existing bounded wire reads.
- Errors/logs/telemetry: failures contain synthetic fixture values only; no production telemetry changes.
- Concurrency: callback gates escape through context cancellation before manager cleanup; no new goroutines or lock ordering.
- Threat model: strengthens retired-state rejection proof through the authenticated phone; production authentication, confidentiality and replay protection are unchanged.

**Reviewer:** builder (self-review)
**Date:** 2026-10-14
