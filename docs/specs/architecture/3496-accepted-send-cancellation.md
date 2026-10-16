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

## Revisions

### 2026-10-16 — diagnosis resolved and Linux validation

No design departure. The open question is resolved by two independent controls:

- With unchanged production, the committed test's `testAcceptedPropagation`
  holds descendant cancellation after its original caller is cancelled. Cancel
  and deadline each returned nil and stalled retirement; shutdown passed.
  `go test -race -json -count=1 -run
  '^TestClientSendContextAcceptedCancellation$' ./internal/transport` executed
  four test/subtest records: one passed, three failed, zero skipped, 6.134 s.
  With the direct caller check, the identical control passed all four in 1.124 s.
- A scratch Go overlay of baseline `send_accepted_test.go` numbered upgrades
  before `Accept`, held upgrade 1 immediately after `Accept` until upgrade 2
  registered, and retained the original post-`Accept` retirement selection.
  `go test -race -json -overlay
  /tmp/builder-3496/registration-overlay.json -count=1 -run
  '^TestClientSendContextAcceptedCancellation/cancel$' ./internal/transport`
  logged `retirement observer owns upgrade 2`, then stalled awaiting retirement.
  Both records failed, zero skipped, 3.024 s. The fix's registration handshake
  prevents cancellation/reconnect until the original peer owns that observer.

These establish the two failure mechanisms, rather than inferring reliability
from a passing rerun. The natural deadline gap is possible because native
`cancelCtx.cancel` closes its own Done channel before iterating its children;
waiting for the parent Done does not join descendant cancellation. The test
wrapper holds that same boundary explicitly through the supported AfterFunc
contract. Baseline native runs reproduced both the nil deadline completion and
retirement stalls before adding the controls.

Environment: Linux/amd64, Go 1.26.9, eight online logical CPUs. GOMAXPROCS and
GOFLAGS were unset; `-p` and `-parallel` used Go defaults, with no affinity or
injected workload. Candidate batches ran concurrently with each other and
vet/build. Counts below include named parents and subtests; timings are the
JSON package elapsed values, not separate shell wall-clock measurements.

| Revision | Command | Executed | Passed | Failed | Skipped | Seconds |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| `4d98f075` | `go test -race -json -count=20 -run '^TestClientSendContextAcceptedCancellation$' ./internal/transport` | 80 | 74 | 6 | 0 | 11.258 |
| `85fa7ca8` | `go test -race -json -count=100 -run '^TestClientSendContextAcceptedCancellation$' ./internal/transport` | 400 | 400 | 0 | 0 | 12.734 |
| `85fa7ca8` | `go test -race -json -count=20 ./internal/transport` | 1620 | 1620 | 0 | 0 | 13.809 |

Baseline failed three parent iterations: cancel stalled once, deadline returned
nil and stalled twice. Shutdown passed all 20 repetitions. The candidate named
batch executed and passed cancel, deadline and shutdown 100 times each; the
complete package batch executed and passed each mode 20 times. All original
disposal, error, no-publication, reconnect marker and pre-admission health
assertions remain, with an added distinct-generation assertion.

`go vet ./...` and `go build -o /tmp/builder-3496/pyry ./cmd/pyry` both exited
zero at `85fa7ca8`. Raw JSON and control overlays were retained in
`/tmp/builder-3496/` for this run; the counts and diagnoses above are the durable
record. No test retry, widened wait, sleep or skipped assertion was added.
The ordinary `make check` aggregate is pending the dispatcher-owned gate; it
has not been claimed green. Other operating systems are outside verification
scope. The documentation handoff remains pending the documentation stage.
