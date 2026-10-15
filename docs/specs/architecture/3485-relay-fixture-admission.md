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

## Revisions

### 2026-10-15 — investigation and validation evidence

No design departure. Both open questions are resolved: the controlled response
hold permits the old helper to return before admission, and the repaired helper
preserves the complete relay and three-package race suites. The original
transport test and its send-decision synchronization are byte-for-byte unchanged.

Source identities:

- Base: `d9d2a7b905ca3be33649977d57de5ae512c415eb`.
- Plan-only revision: `00dc5ff1`; the initial regression ran on its unchanged
  helper plus the new, uncommitted controlled test.
- Repaired source: `e015f98a4a6294a51a3cede27236e3608170ce38`. The first focused
  pass ran immediately before this commit on identical source. All remaining
  candidate checks ran at this revision; subsequent changes add evidence only.
- Negative overlay: this candidate with only the five-line admission wait in
  `driveToOpen` removed; immediate manager cleanup is retained. Scratch overlay
  mapping: `{"Replace":{"/work/Projects/.pyrycode-worktrees/pyrycode/builder-3485/internal/relay/v2session_test.go":"/tmp/builder-3485/v2session-before_test.go"}}`.

Environment: Linux `7.0.0-38-generic` x86_64; Go `go1.26.9 linux/amd64`;
12 available CPUs; `GOOS=linux`, `GOARCH=amd64`, `CGO_ENABLED=1`, empty `GOFLAGS`.
Every Go validation command explicitly used `GOMAXPROCS=8`, with default
test parallelism 8. Go validations below were sequential: no other Go test,
vet or build job launched by this builder overlapped them. Read-only source and
evidence inspection ran alongside tests. External process activity was not inventoried.
Only the synctest regression controls scheduling; ordinary suites use normal
package/test concurrency without artificial stress. Virtual 10 ms advancement
lets the existing recorder poll run; the repair itself waits on a Run snapshot,
with no sleep, failed-push retry or accepted admission error.

Commands (all prefixed `GOMAXPROCS=8`):

1. Initial red: `go test -race ./internal/relay -run '^TestDriveToOpen_WaitsForAdmission$' -count=1 -json`.
2. Focused candidate: `go test -race ./internal/relay -run '^(TestDriveToOpen_WaitsForAdmission|TestWaitForOpenAdmission_Context|TestV2Session_Push_HoldGatedOnProbeNotSendError)$' -count=20 -json`.
3. Complete relay tree: `go test -race ./internal/relay/... -count=1 -json`.
4. Original scoped command: `go test -race ./internal/thread ./cmd/pyry ./internal/relay -json`.
5. Negative control: `go test -race -overlay /tmp/builder-3485/without-barrier.json ./internal/relay -run '^(TestDriveToOpen_WaitsForAdmission|TestV2Session_Push_HoldGatedOnProbeNotSendError)$' -count=20 -json`.
6. Matched candidate: command 5 without `-overlay /tmp/builder-3485/without-barrier.json`.
7. `go vet ./...` and then `go build ./cmd/pyry`, both exit 0 with empty logs.

Counts are named test/subtest records, including parent tests. Every listed
suite executed uncached; the original scoped command reported no cached package.

| Run | Executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| Initial controlled red | 1 | 0 | 1 | 0 |
| Focused candidate, 20 repetitions | 100 | 100 | 0 | 0 |
| Complete relay package | 1534 | 1534 | 0 | 0 |
| Relay handlers in complete tree | 455 | 455 | 0 | 0 |
| Original scoped: thread | 1186 | 1185 | 0 | 1 |
| Original scoped: cmd/pyry | 16566 | 16564 | 0 | 2 |
| Original scoped: relay | 1534 | 1534 | 0 | 0 |
| Negative overlay, 20 repetitions | 40 | 20 | 20 | 0 |
| Matched candidate, 20 repetitions | 40 | 40 | 0 | 0 |

The focused 100 records comprise 20 original tests, 20 controlled regressions
and 60 context-test parent/subtest records. All 20 negative-overlay failures
are `TestDriveToOpen_WaitsForAdmission`; all 20 original transport tests pass
in that same negative run. Every negative diagnostic records `session present,
state=1, queue=false, first push=relay: conn not found: rekey: conn not found`.
State 1 is `V2StateHandshakeComplete`. Outbound is held on Run after publishing
the recorded response and before queue insertion; the session map read is
ordered by that hold, queue presence is read under `pushMu`, and teardown
cannot run until release. Initial red and overlay failures exit normally after
release/join, with no race report. Repaired runs block before return, then
accept both pushes and authenticate/decrypt them in order after release.

This demonstrates an admission-ordering dependency, not the cause of the
historical #3483 failure. The original test's 20/20 negative-overlay pass and
the issue's earlier isolated 20/20 pass are retained contrary results; neither
erases the historical failure or establishes incidental contention as its cause.
A preliminary build iteration had an unused `context` import and executed zero
tests; it was corrected before the initial controlled red summarized above.

All three scoped-command skips are explicit, unrelated opt-ins/platform cases:

- `TestRunAgentRun_RealClaude`: `set PYRY_E2E_REAL_CLAUDE=1 to run`.
- `TestMemoryPathsCaseInsensitive`: `case-sensitive filesystem: alternate component casing cannot resolve`.
- `TestReferenceDiskHeap`: `run separately with -args -measure-thread-heap`.

Standard `make check` outcome: **pending dispatcher/verifier gate; not run by
builder**, per the role's prohibition on duplicating the full-module gate.
No aggregate green, staticcheck result, substrate-guard result or fake-daemon
e2e result is claimed here. The dispatcher must retain its revision, settings,
executed/passed/failed/skipped counts and skip reasons for the final gate record.
No live-Claude or native-platform task is required by this hermetic change.
