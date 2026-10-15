# Queued-handshake admission synchronization

## Files read
- `cmd/pyry/daemon_relay_wire_test.go` → `TestDaemonRelayWireQueuedPushDuringHandshake`: four queued frames and the final addressed push.
- `cmd/pyry/agent_switch_frames_test.go` → `switchWire.open`, `receive`, `read`: response receipt preserves other peers' ciphertext order but does not await admission.
- `cmd/pyry/daemon_live_relay_test.go` → `testDaemonRelayWire`: configuration injection and joined manager/publisher cleanup.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: successful response precedes persistence, open state and queue registration.
- `internal/relay/v2session_conns.go` → `ActiveConns`, `activeConns`: cancellation-aware snapshot serialized on Run.
- `internal/relay/v2session_push.go` → `pushSource`: missing queue returns `ErrConnNotFound` without inspecting session state.
- `docs/knowledge/features/v2-session-manager.md` and `v2-session-manager-concurrency.md`: Run owns session state and Noise ciphers; Push accesses queues under a leaf lock.
- `docs/knowledge/features/development-verification.md` → shared wire output and admission lessons: response receipt alone cannot prove admission; retain cipher order and do not retry Push.
- `CODING-STYLE.md`; `docs/protocol-mobile.md` → Security model: preserve authentication, secret handling and nonce ownership.

## Context
The historical failure establishes a missing queue, not teardown. The concrete
candidate is an immediate off-Run push after successful response receipt but
before admission. A controlled experiment must distinguish that possibility
from a formerly admitted connection disappearing. No production repair is
planned, and the historical failure's precise scheduling remains unknown.
No decision record is needed.

## Design
Keep all edits local to `cmd/pyry/daemon_relay_wire_test.go`. Add a test-only
`testDaemonRelayAdmission(ctx, mgr, id) error` check with a three-second upper
bound and the caller's earlier cancellation. After `switchWire.open`, a Run-owned
`ActiveConns` snapshot must contain the exact connection before an addressed
push. A missing ID or expired context fails; no push retry is allowed. Apply the
check for phone, legacy and again in the existing test without changing its
queued-frame assertions or shared wire helpers.

The controlled regression wraps the existing Outbound callback, publishes the
successful `noise_resp` for again, and holds its return until released. The raw
wire helper can finish and a direct probe must return `ErrConnNotFound` while
Run remains held before queue creation. A bounded admission check must expire
in that held interval. Release the callback, require admission and then decrypt
the exact final push. This establishes unfinished admission as a sufficient
cause; it does not attribute the historical failure or demonstrate teardown.

## Concurrency model
Run and the existing publisher retain their owners and cleanup. The injected
Outbound hold selects between an explicit release and test-context cancellation.
A deferred idempotent release runs before fixture cleanup joins Run. The test
thread alone owns wire reads and phone cipher state; no new cipher goroutine or
production synchronization is added.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Response received while admission is held; early queue lookup fails | `TestDaemonRelayWireAdmissionBeforePush` |
| Admission wait expires while Run is held; release then admits same connection | `TestDaemonRelayWireAdmissionBeforePush` |
| Caller cancellation ends admission wait | `TestDaemonRelayWireAdmissionBeforePush` |
| Previously admitted phone/legacy output survives again's handshake in order | `TestDaemonRelayWireQueuedPushDuringHandshake` |
| Newly admitted again accepts and receives envelope 20 | Both tests |
No ID replacement or reconnect is introduced; each peer opens once per fixture.

## Error handling
The admission helper returns cancellation/deadline errors or an explicit missing
connection error. The experiment expects a failed *diagnostic* early push;
readiness-dependent delivery must still succeed without retry. Outbound decode
errors return to the manager. Failure always releases held work before joining.
Any demonstrated production defect requires refinement before a production fix.

## Testing strategy
Write the controlled experiment first, initially leaving admission unsynchronized,
and observe its deadline assertion fail. Then add the snapshot check and its
calls in the existing test. Run at least 20 uncached race repetitions of each
named test, the scoped thread/cmd/relay race suite, `go vet ./...`, and
`go build -o /tmp/builder-3483/pyry ./cmd/pyry`. The dispatcher owns `make check`;
record its pending outcome rather than duplicating its full-module gate.
Record commands, revisions, platform/Go/concurrency settings, counts and skip
reasons in this plan's investigation evidence and the PR. Incidental process
overlap is not controlled scheduling or historical attribution.

## Open questions
- Does held pre-admission scheduling reproduce the missing queue? Resolve by the controlled experiment.
- Does the snapshot wait preserve the original queued wire assertions? Resolve by repetition and scoped race validation.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] `switchWire.open` still exercises real Noise authentication and paired fake tokens; the new check cannot admit a production peer.
- [Tokens, secrets, credentials] Existing ephemeral random keys and fake tokens remain test-only; evidence contains test names/counts, never key material or token hashes.
- [File operations] Only this spec and test source change; existing fixtures use `t.TempDir`, and validation scratch files stay outside the checkout.
- [Subprocesses] No new subprocess invocation in the test; ordinary Go checks retain existing process cleanup.
- [Cryptography] `switchWire.read` remains the sole test-thread cipher consumer; pending output is not decrypted or reordered by readiness checks.
- [Network and I/O] In-memory routing channels only; the response hold is explicitly released and cancellation-aware, with no new network surface.
- [Errors, logs, telemetry] Diagnostics contain only fake connection identifiers and classified errors; no payload or credential logging is added.
- [Concurrency] `ActiveConns` is Run-owned and context-bounded; the injected hold is released before the existing fixture joins Run and publisher.
- [Threat model] The protocol Security model's authentication, replay and relay-MITM boundaries are unchanged; this test repair does not alter production trust or resource limits.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15

## Sizing and overlap
One deliverable: reliable admission ordering in the queued-handshake fixture,
with its controlled regression and evidence. Expected total written work about
300–400 lines, no exported types/interfaces, three local check insertions, four
acceptance criteria and no new production reject branches; all five limits hold.
Fetched feature branches have no overlap in the three fixture files inspected.
