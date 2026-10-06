# Retain stream echoes across send-message ack waits (#2614)

## Files read

- `internal/e2e/relay_v2_stream_new_session_test.go` → `TestRelayV2_StreamNewSessionRotatesAndRestartsFresh`: M1/M6 currently lose deltas in the two ack waits.
- `internal/e2e/relay_v2_stream_interrupt_test.go` → `TestRelayV2_StreamInterruptStopsRunningTurn`: M1 loses deltas during its ack wait; `nextEnv` already enforces zero unrecognized frames.
- `internal/e2e/relay_v2_stream_send_test.go` → `TestRelayV2_StreamSendMessageDrainsToStructuredEvents`: precedent for retaining milestones independently of ack ordering.
- `docs/knowledge/INDEX.md` and `CODING-STYLE.md`: owning topic and Go testing conventions.
- `docs/knowledge/features/e2e-harness.md` → Isolation Strategy and test-cache lesson: reuse isolated fake processes and disable cached execution for repeatability.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change and Test execution: require failure evidence and executed counts.
- `docs/protocol-mobile.md` → Security model: preserve Noise ordering and authentication boundaries.

## Context

The send handler enqueues before replying with its ack. A valid echo can therefore
arrive during the ack wait and be discarded, causing a later milestone timeout.
This is one test-reliability deliverable; no production contract changes or decision
record are needed. No other fetched feature branch touches either target file.

## Design

Record and validate assistant deltas when the existing local `nextEnv` decrypts
them. Move the current payload decoding and conversation/text assertions there.
Each milestone waits on the recorded flag rather than requiring a new frame after
the ack. New-session retains independent flags for the first and second needles;
M6 still ignores fragments without turn two's needle, including trailing turn-one
frames. Interrupt retains its M1 flag and zero-unrecognized-message assertion.
Matching ack IDs and all existing ack/milestone deadlines remain unchanged.

Sizing: about 200 total written lines including moved assertions and plan;
0 exported types/interfaces, 0 production consumers, 2 acceptance criteria,
no new state-machine reject branches. Within all five builder limits.

## Concurrency model

All observations stay on the test goroutine through the existing single reader.
No goroutines, locks or shutdown paths are added; harness cleanup remains in use.
Ciphertext is decrypted once in arrival order; retained state consists of booleans.

## Error handling

Retain malformed-payload failures, conversation/text assertions, matching ack
correlation, unexpected-error handling and milestone-specific timeout diagnostics.

## Testing strategy

Before changing the tests, force valid echo-before-ack delivery with a scratch Go
overlay that temporarily holds the already-decrypted ack until the echo is read;
the current milestone waits must fail. Repeat with the fix to prove both orderings
are accepted, without changing ciphertext order. Run the ticket's race-enabled,
20-count invocation and report each named test's executed/passed counts. Run scoped
race tests for the touched package, `go vet ./...` and `go build ./cmd/pyry`;
the dispatcher owns the full-module gate. Scratch overlays/logs stay under `/tmp`.

## Open questions

None. No documentation-only acceptance criteria or live-Claude work is requested.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `nextEnv` keeps `decryptInnerEnvelope` before JSON payload decoding; retained echoes still undergo conversation and text assertions.
- [Tokens, secrets, credentials] Pairing and token handling in `paireddevice.Setup` are unchanged; only fixed fake prompt text is inspected.
- [File operations] No new file access; existing isolated harness and per-child stdin-log paths remain unchanged.
- [Subprocesses] Existing harness startup and cleanup are reused without new commands, environment values or caller-controlled inputs.
- [Cryptography] One `recvA` decrypts every frame exactly once in arrival order; observation flags never replay ciphertext or consume additional nonces.
- [Network and I/O] Existing `ReceiveBytes` uses the same remaining deadline. No extra frame queues, listeners or reads are added.
- [Errors, logs, telemetry] Existing diagnostics inspect fixture payloads only; no tokens or keys are newly logged.
- [Concurrency] Flags belong to the sole reader's test goroutine; no concurrent writes or new goroutines.
- [Threat model] Authentication, relay MITM and replay protections from Security model remain unchanged. Turn two requires its own needle, preventing stale turn-one evidence from hiding a drain regression.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06

## Revisions

### 2026-10-06 — Source-map correction

The send-test precedent's actual symbol is `TestRelayV2_StreamSendMessageDrainsTurn`,
correcting the name in Files read. Also read
`docs/specs/architecture/2612-stream-modal-ack-ordering-flake.md` → Change: confirms the
single-decrypt-point recording precedent and the sibling scope. No design change.
