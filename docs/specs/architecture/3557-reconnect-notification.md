# Reconnect notifications independent of frame forwarding

## Files read

- `internal/relay/connection.go` → `run`, `forwardFrames`, `Connect`, `Reconnected`: the sole transport event observer blocks inside forwarding.
- `internal/relay/connection_admission_test.go` → `TestAdmissionHeadersAndReconnect`, `testConnectAdmission`, `assertAdmissionRequest`, `admissionLogCapture`: preserve exact headers, frames, bounded recovery and private-data exclusion.
- `internal/relay/connection_test.go` → `newTestConnection`, `TestTransportReconnect_SignalsReconnected`: existing reconnect and shutdown fixtures.
- `internal/transport/wssclient.go` → `ReceiveGeneration`, `serve`, `Connect`, `Generation`, `recvPump`: receives snapshot the current disconnect lifetime; replacement is independent of relay scheduling.
- `internal/relay/connection_feedback.go` → `consumeWakeResult`: receive ownership must remain pump-stamped and ordered.
- `docs/knowledge/features/relay-package.md` § Reconnect semantics and Reconnect-state accessors: single-observer, cap-one signals and a stable application channel.
- `docs/knowledge/features/transport-package.md` § Socket generation ownership and Pump lifecycle and shutdown: do not infer receive ownership from reconnect events.
- `docs/knowledge/features/development-verification.md` and its controlled-regression guidance: later green reruns cannot establish historical cause.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model, and shared builder security review: concurrency cleanup and credential boundaries.

## Context

The original Linux failure shows transport disconnected at 03:55:04.136217029Z
and connected at 03:55:04.137460128Z. Relay forwarding did not exit until
03:55:11.105864538Z, then logged establishment after context expiry. The source
permits forwarding to resume after socket replacement and snapshot its new
disconnect lifetime, leaving the new Connected event unread. A controlled barrier
will establish this ordering without claiming to reconstruct the historical scheduler.
No decision record is needed.

Sizing: one recovery deliverable, four acceptance criteria; approximately 350
written lines including this plan, no exported types, no consumer updates, and
fewer than ten error/shutdown branches. No other fetched feature branch touches
the planned production or admission-test files.

## Design

`Connection.run` remains the sole observer of transport Connected events and
re-broadcasts them with the existing nonblocking cap-one contract. It wakes one
separate frame-forwarding worker through another cap-one signal. The worker
invokes the existing `forwardFrames(ctx)` serially and waits for a wake after a
disconnect. A queued wake covers replacement while forwarding is active; frames
may transparently cross generations, without preventing event consumption.
The public API, transport receive semantics, routing parsing and credential
selection remain unchanged. Preserve the existing admission test assertions.

## Concurrency model

One new worker per Connection owns all forwarding and feedback consumption;
there are never competing receivers. `run` owns lifecycle classification and
notifications. A derived cancellation context, client Close and a worker-done
channel stop and join the worker before closing Frames or publishing done.
No new locks or unbounded queues. Frame backpressure cannot block notifications.

## State transitions and identity reuse

| Event | Required behavior and race test |
| --- | --- |
| Initial 503 then upgrade, with and without token | Existing `TestAdmissionHeadersAndReconnect` retains every header/frame/security assertion. |
| Replacement while forwarding is paused | `TestReconnectWhileForwardingPaused` holds a malformed-frame diagnostic, replaces the transport generation, then requires the notification before sending new application traffic. |
| Two consecutive replacements using the same routing ID | `TestReconnectWhileForwardingPaused` repeats the barrier and exact echo over three socket generations on the same Frames channel. |
| Close or cancellation while forwarding waits | `TestReconnectForwardingShutdown` exercises both with bounded Wait and closed Frames. |
| Terminal admission rejection | `TestAdmissionUnauthorizedTerminal` retains initial and post-drop shutdown, classification and private-data checks. |

## Error handling

Retain all sanitized transport and relay classifications. The worker returns on
existing receive failures and awaits another connection wake; terminal lifecycle
results cancel and join it. No peer/header/payload text enters new diagnostics.

## Testing strategy

Write the controlled barrier regression first and run it against the old code:
it must fail waiting for the reconnect event despite a live replacement. Then
implement and run the same test, shutdown cases and 30 uncached repetitions of
both original admission subtests under admitted race tests. Run relay/transport
package race tests, `go vet ./...`, and build `./cmd/pyry` to scratch. Record Linux
commands, named counts and evidence paths in the PR. The full gate belongs to
the verifier; the historical failure remains failed evidence.

## Open questions

None; the controlled test must confirm the causal ordering before acceptance.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `forwardFrames` retains JSON validation and generation-stamped feedback ownership; event notifications grant no phone authorization.
- [Tokens] `Connect` alone constructs admission headers. Preserve `assertAdmissionRequest`, `assertAdmissionSafeError` and log scans for both synthetic secrets.
- [File operations / subprocesses] The production change opens no files and starts no subprocesses.
- [Cryptography / threat model] Noise, pairing, identity claims and admission enforcement are unchanged; notifications only wake the existing push drain.
- [Network and I/O] Reuse the existing transport pumps, frame size bounds and cancellation. Hermetic listeners use only synthetic credentials and bounded test lifetimes.
- [Errors, logs, telemetry] Add no production diagnostics or peer-controlled values; barrier diagnostics use the existing static malformed-envelope message.
- [Concurrency] SHOULD FIX: every lifecycle return must cancel, close and join forwarding before Frames closes; shutdown tests cover Close and cancellation explicitly.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17
