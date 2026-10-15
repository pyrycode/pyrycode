# #3485: relay fixture admission barrier

## Files read

- `internal/relay/v2session_test.go` → `driveToOpen`, `startManager`, `v2Recorder`: response capture currently precedes admission; retain all 50 callers and cipher setup.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: successful response precedes persistence, open state and queue insertion.
- `internal/relay/v2session_conns.go` → `ActiveConns`, `activeConns`: bounded caller context and Run-owned snapshots serialize after admission.
- `internal/relay/v2session_push.go` → `pushSource`: absent queue reports `ErrConnNotFound` independently of session teardown.
- `internal/relay/v2session_transport_test.go` → `TestV2Session_Push_HoldGatedOnProbeNotSendError`: retain both transport cases and send-decision synchronization unchanged.
- `docs/knowledge/features/v2-session-manager.md` and its same-package test overview/delivery sections: authenticated capture-order decryption must survive shared fixture changes.
- `docs/knowledge/features/relay-package.md` § Test surface: record the accepted prefix before signaling a held transport.
- `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: passing repetitions cannot attribute a historical failure; retain counts and incomplete gates.
- `CODING-STYLE.md`: context cancellation, goroutine cleanup, symbol citations.

## Context

The historical first-push failure reports an absent queue but cannot distinguish
unfinished admission from teardown. Investigate the concrete response/admission
interval without claiming historical attribution. One deliverable: reliable
open-session test readiness with a controlled regression. No decision record needed.

Sizing before plan commit: approximately 320 written lines including evidence,
zero exported types/interfaces, zero consumer migrations, four acceptance criteria,
no new production state machine. Fits the 800/5/10/5/10 limits. Remote feature
branches checked after fetch: none overlap the planned relay test files.

## Design

Keep `driveToOpen`'s signature, exact handshake-envelope assertion, Noise response
authentication and returned cipher states. After decoding, require this connection
in a single `ActiveConns` snapshot under a two-second context derived from
`t.Context()`. A snapshot requested after response capture is processed by Run
after the handshake handler completes, including queue registration. Fail closed
if missing or cancelled. Register manager cleanup immediately after startup so
an admission failure cannot leave Run behind. No push retries or production edits.

Add a focused admission test file. A test-local outbound records the successful
response and blocks before returning, holding Run before admission. In a
`testing/synctest` bubble, advance only virtual time to let the existing recorder
poll execute, then require the fixture still blocked. If it returns early, inspect
the Run-owned session while the outbound hold synchronizes access, inspect the
queue under `pushMu`, and record the first push result before release. This
distinguishes unfinished admission from teardown. Release the gate and require
the fixture to complete and two pushes to decrypt in order. Separate virtual-time
deadline/cancellation cases cover the bounded snapshot check with no running Run.

## Concurrency model

Run owns sessions. The fixture goroutine owns the returned cipher states until
handoff through a channel. Outbound records before publishing its held signal;
release channels and joins run on all failure paths. Cleanup releases outbound
before stopping/joining Run. `synctest.Wait` establishes durable blocking; virtual
time schedules the recorder poll, not a wall-clock readiness assumption.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Response published while initial admission is held, then release | `TestDriveToOpen_WaitsForAdmission` |
| Repeated pushes on the admitted connection, authenticated nonce order | `TestDriveToOpen_WaitsForAdmission` |
| Snapshot wait expires or caller cancels without a Run receiver | `TestWaitForOpenAdmission_Context` |

No reconnect/rekey/identity reuse is added. Existing shared callers retain those proofs.

## Error handling

Missing admitted connection or expired/cancelled snapshot context fails the fixture.
All held work is released and joined even if the regression detects early return.
No test accepts a failed readiness-dependent push. Historical attribution remains open.

## Testing strategy

Write regression first and record its old-helper failure with synchronized state,
queue and push observations. Implement the barrier, then repeat both the original
transport test and controlled regression at least 20 times uncached under race.
Run complete relay race tests, the original three-package race command, vet and
build. Record revisions, Linux/Go/GOMAXPROCS settings, counts and skip reasons.
Do not introduce artificial stress into ordinary suites; report incidental overlap.
The builder instructions prohibit `make check` and full-module race sweeps: its
outcome is pending the dispatcher's standard gate, not claimed green here.

## Open questions

- Does the controlled hold permit old `driveToOpen` to return before admission?
  Resolve through the initial red run, retaining any contrary result.
- Does the snapshot barrier preserve all shared fixture users? Resolve through
  the complete relay race suite and combined thread/CLI/relay command.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: keep `ReadResp` authentication and open-only `ActiveConns`; observing a response alone never establishes authenticated admission readiness.
- Tokens/secrets: generated ephemeral test keys and fixed fixture token only; diagnostics contain state, queue presence and errors, never keys, token or payload.
- Files: no new persistence/path handling; test code and this plan only.
- Subprocesses: no new subprocess behavior; validation uses existing Go tools.
- Cryptography: preserve Noise implementation and ordered decryption; no nonce or key handling changes.
- Network/I/O: in-memory routing channels only; snapshot wait bounded and cancellation aware.
- Errors/logs: report local fixture readiness failures; do not format `ActiveConn` or authenticated data wholesale.
- Concurrency: held outbound gates require unconditional release before joining Run; `pushMu` protects queue observations and the hold orders direct session observations.
- Threat model: protocol security model's relay confidentiality, authentication and nonce-order properties retain existing production enforcement; no new exposed surface.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-15
