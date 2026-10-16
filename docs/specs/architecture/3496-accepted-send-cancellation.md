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

### 2026-10-16 — verifier finding 1: complete aggregate rerun handoff

The verifier reviewed `8520bd3069922657298261b65d7434c5e5e37f9b` and found
no transport implementation or security defect. Its dispatcher `make check`
exited 2: `internal/thread` timed out at 600.199 s in
`TestCheckpointResumeCorpus`, with eight cuts still running. Vet passed and
the race tier reported 54 successful packages, including transport at 1.625 s,
but staticcheck, substrate-guard, fake-daemon e2e and the subsequent
`make build` never ran. That aggregate is failed/incomplete; its nonverbose
output establishes package outcomes only, not named test counts.

The dispatcher has since merged main into this branch at
`cdd691085e2bceb846949f4bcf68d9a857503581`. This includes the separate #3494
runtime repair merged by PR #3497 at `f314c499`; #3494 is closed.
There is no transport source change since the reviewed head and no further
thread change in this rework. The earlier failure matches the baseline
timeout tracked by #3494, but does not prove scheduling causation or a pass
on the merged candidate.

Builder-owned Linux checks at `cdd69108`:

| Command | Executed | Passed | Failed | Skipped | Seconds |
| --- | ---: | ---: | ---: | ---: | ---: |
| `go test -race -json -count=100 -run '^TestClientSendContextAcceptedCancellation$' ./internal/transport` | 400 | 400 | 0 | 0 | 12.378 |
| `go test -race -json -count=20 ./internal/transport` | 1620 | 1620 | 0 | 0 | 13.431 |

Each mode executed and passed 100 times in the named batch and 20 times in
the package batch. Counts include parents and subtests; timings are JSON
package elapsed values. Environment: Linux/amd64, Go 1.26.9, eight online
logical CPUs, GOMAXPROCS and GOFLAGS unset, default `-p` and `-parallel`.
The batches ran concurrently with `go vet ./...` and
`go build -o /tmp/builder-3496-rework-pyry ./cmd/pyry`, both of which exited
zero. Raw output is retained under `/tmp/builder-3496-rework-*` for this run.

Mandatory handoff to the next dispatcher/verifier stage: run the complete
ordinary `make check`, followed by `make build`, on the merged candidate
before approval. Retain the full race suite, staticcheck, substrate-guard and
fake-daemon e2e coverage and Go's default timeout; focused passes above do
not substitute for this aggregate. The dispatcher runs configured pre-verifier
gates again after this builder handoff; failed gates are not reusable passes.
The full rerun result remains pending, so verifier finding 1 remains open
until that evidence is available. Documentation remains pending its owning
stage; other operating systems remain outside this ticket's scope.

### 2026-10-16 — recovery: reduce repeated proof serialization

The recovery brief explicitly extends this rework to the shared proof's runtime;
the second aggregate at `b275c8e5` also failed at 600.159 s. Both historical gates
remain failed. Read `internal/thread/checkpoint_resume_test.go` (`testResumeProof.update`),
`checkpoint_benchmark_test.go` (`BenchmarkCheckpointResumePhases`), `render.go`
(`Fold.renderShared`), `checkpoint_export.go` (`checkpointRecord`) and
`docs/knowledge/features/thread-package-main-thread-folding-validation.md`
**Validation and testing**: every import must independently render/hash all rows,
and JSON RawMessage alone cannot prove exact Content bytes. QMD found #3494's
plan; its validation optimization is already present but did not clear this host.
No other remote feature branch touches the two proposed test files.

Keep all production code and corpus scheduling unchanged. Measure the uncached
full-module race tier and isolated corpus, retaining phase logs, CPU/allocation
profiles, CPU quota and Go concurrency. Replace only proof bookkeeping: borrow
immutable rendered bytes through `renderShared`, serialize the complete rendered
metadata and checkpoint/private facts once into worker-owned scratch, and append
a length-delimited exact-content component. Preserve nil-versus-empty Content
identity. No rendered row or hash is reused across imports. Reuse selection and
encoding storage within one proof owner; clear or overwrite it before reuse.
Keep both per-cut JSON round trips, 30,000 entries, 201 cuts, 50,101 comparisons,
eight joined workers and the default timeout. Measure the same phases before/after.

Add proof sensitivity cases before implementation: every Item metadata field,
recipe/private publication facts, exact Content and length boundaries must affect
the proof; scratch reuse must match fresh full recomputation. Retain the existing
exact-byte negative control. Run focused race checks, uncached thread and joint
checks, and the required transport batches; the dispatcher still owns fresh
`make check` followed by `make build` and normal review.

Sizing recheck: one runtime-proof deliverable within the existing acceptance
criteria; approximately 250 additional lines (under 800 total branch-written
lines), zero exported types, zero consumer API changes and no new reject branch.
Security review PASS for this revision: test-only synthetic data, no I/O or
credential/protocol changes; private facts and exact-byte distinctions stay in
the hash. Each existing worker exclusively owns scratch; no new goroutine,
shared cache or cancellation lifetime. Lifecycle coverage remains the existing
resume/cancellation race tests, with scratch reuse checked by the new proof test.

Additional documentation handoff, pending documentation stage: in
`docs/knowledge/features/thread-package-main-thread-folding-validation.md`,
**Validation and testing**, describe the demonstrated proof cost reduction and
worker-owned scratch/length-delimited Content encoding. Retain the independent
per-import full inventory, both JSON round trips and corpus/comparison counts;
distinguish measured runtime from unproved historical contention attribution.
