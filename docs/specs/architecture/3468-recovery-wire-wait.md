# Recovery wire workload coordination (#3468)

## Files read
- `cmd/pyry/daemon_thread_queries_test.go` → `TestDaemonThreadQueriesRecovery`, `testDaemonThreadQueryWire`, `testDaemonThreadReply`, `testDaemonThreadLoad`: unchanged recovery fixture, provider installation and reply assembly.
- `cmd/pyry/agent_switch_frames_test.go` → `switchWire.open`, `receive`, `read`: authenticated handshake and ordered receive defaults.
- `cmd/pyry/daemon_live_relay_test.go` → `testDaemonRelayWire`: buffered capture, manager startup and joined cancellation.
- `internal/relay/v2session_catchup.go` → `runThreadQuery`: provider and complete reply encoding precede first delivery.
- `docs/knowledge/features/thread-package.md` § Shadow lifecycle and evidence: preserve reopened epoch, bounded-query coverage and inert escaped content.
- `docs/knowledge/features/development-verification.md` § Diagnosing reply timeouts: matched contention, absolute completion bound and buffered-output negative controls.
- `CODING-STYLE.md`; builder `security-review.md`: concurrency and authenticated-wire review requirements.

## Context
The shared three-second per-frame wait includes expensive provider rendering and encoding before first transport. At base `07b6b35e14aad0de688326fb36d81c54303c691c`, a diagnostics-only overlay failed four of four recovery executions under four race binaries and 32 CPU contenders pinned to CPU 0. Recovery finished and both handshakes completed (legacy completion 650–748 ms after wire setup); first catch-up entered at 748–784 ms, without returning before frame expiry. This demonstrates insufficient provider scheduling headroom in this experiment. The downloaded #3453 builder evidence contains truncated excerpts, not complete phase logs, so historical attribution remains unknown. No production repair or ADR is proposed.

One deliverable, estimated 350 written lines including plan/evidence, zero exported types, seven reply-helper consumers, three acceptance criteria, zero production reject branches. All five limits hold before committing this plan. No fetched feature branch changes either target test file. #3467 metadata readiness is independent.

## Design
Keep the shared wire's default waits and handshake behavior. Extract its existing authenticated envelope decoder so query helpers can consume the same ordered pending/output stream under a query-specific absolute deadline. `testDaemonThreadReply` uses one workload deadline for every fragment and the final coverage marker, checking expiry before buffered frames and after reconstruction. Do not reset the deadline on progress. Select the finite bound from the matched contention measurement (provisional measurement ceiling 30 seconds).

Add content-free provider-entry/completion, handshake and transport/reconstruction phase diagnostics. Retain every current fixture and assertion. Add a focused recovery variant that holds the first provider behind a bounded release gate beyond the old frame wait, then requires the identical complete recovered reply. Add focused deadline controls for absent output and already-buffered output; scratch overlays will also remove the final marker and suppress all transport while retaining the complete expected fixture.

## Concurrency model
The manager and provider workers keep their existing cancellation/join paths. Cipher receive ownership stays on the test goroutine. Diagnostic timestamps are immutable; no payload is logged. Controlled provider delay uses a cancellable gate with bounded release and cleanup; no worker may survive test cleanup. Pending frames retain their original nonce order.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Clean shutdown/reopen reuses saved epoch | `TestDaemonThreadQueriesRecovery` |
| Repeated catch-up/page requests on the same peer | `TestDaemonThreadQueriesRecovery` |
| First provider held then released; complete recovery reply | `TestDaemonThreadQueriesRecoveryDelayedProvider` |
| Missing/expired buffered frames preserve one bound | `TestDaemonThreadReplyDeadline` |

## Error handling
Timeouts identify the query and frame/reconstruction phase with elapsed duration/counts. Missing transport, partial replies and missing final marker fail at the same finite deadline. Malformed, unauthenticated or uncorrelated output continues to fail immediately. Other shared-wire callers retain three-second defaults.

## Testing strategy
Write the controlled-delay and deadline regression first and observe failure against old behavior. Compare baseline and candidate using the same four processes, race instrumentation, fixture, CPU 0 and 32 CPU-only contenders, joining all contenders before ordinary checks. Record exact revisions, commands, named counts and phase timings here. Run uncached focused repetitions, all seven helper consumers, complete `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`, and the requested `go test -race -timeout=15m ./internal/thread ./cmd/pyry ./internal/relay`. Report unrelated failures separately; no live Claude is needed.

## Open questions
- Resolve the workload bound from complete provider/transport/reconstruction measurements before handoff.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries/cryptography: `switchWire.open` still authenticates test peers using Noise; the extracted decoder keeps the existing sequential receive cipher, with no key or nonce reuse.
- Tokens/errors/logging: generated test keys and fixture tokens are unchanged. New phase diagnostics contain only operation names, durations and counts, never plaintext or ciphertext.
- Files/subprocesses: only allowed test sources and this plan change. Scratch overlays, logs and fixed-command contention drivers stay under `/tmp/builder-3468`; contenders are terminated and joined.
- Network/I/O: hermetic buffered channels retain existing size caps. Query completion gains an absolute finite bound including buffered output; shared handshake behavior remains unchanged.
- Concurrency: no shared cipher access is added; provider gate cancellation must release before the manager's joined cleanup. No new product locks or goroutines.
- Threat model: protocol confidentiality, pairing and inert-content assertions remain intact; no product security boundary changes or deferred security findings.

**Reviewer:** builder self-review. **Date:** 2026-10-15.
