# Compact retry completion boundary

## Files read

- `internal/relay/v2session_compact_test.go` → `TestThreadCompactLiveInFlight`, `testCompactReply`: failing fixture and projection/join assertions.
- `internal/relay/v2session_compact_live_test.go` → `testDeliverLive`, `testCompactUpdate`, `TestThreadCompactLiveQueryRace`: bounded delivery helpers and adjacent ordering proof.
- `internal/relay/v2session_catchup.go` → `admitThreadQuery`, `cancelThreadQuery`, `threadQueryGate`, `runThreadQuery`, `sendThreadReply`: query admission, both cancellation cuts and final join installation.
- `internal/relay/v2session_push.go` → `pushSource`, `drainOnce`: producer cancellation precedes asynchronous lane admission.
- `internal/relay/v2session_publication.go` → `FlushPushes`: push markers finish without awaiting hosted completion.
- `internal/relay/v2session_hosted.go` → `hostedEnqueue`, `hostedDeliver`, `handleHostedDone`: queued live work can remain behind an active refusal writer.
- `internal/relay/v2session_hosted_changes.go` → `takeHostedChanges`: pending delivery is appended to the ordered lane.
- `internal/relay/v2session_thread_delivery.go` → `deliverThread`: return acknowledges delivery/withholding, unlike a push marker.
- `internal/relay/v2session_hosted_test.go` → `testHostedStartCaps`: configurable sender and cancellable manager cleanup.
- `internal/relay/v2session_catchup_test.go` → `testCatchUpError`: correlated, retryable, content-free refusal contract.
- `docs/knowledge/features/relay-package.md`, `v2-session-manager.md`, `v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md`: Run owns lane state; asynchronous sender completion is distinct from enqueue.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md`: refusal may precede queued live work; both must finish before retry.
- `docs/knowledge/features/development-verification-prove-that-tests-distinguish-the-change.md`: prove cross-goroutine completion using an acknowledged boundary.
- `docs/protocol-mobile.md` § Security model: authenticated, encrypted, bounded ordered delivery remains authoritative.

## Context

One behaviour: retry a cancelled complete re-initialization over a compact join after live work and refusal completion. The historical full-gate failure at `9e11af5bced5542a5308485ad468f178c4bef8d8` remains failed (30,770/30,741/2/27 executed/passed/failed/skipped including parents). Its same-tree focused comparison remains a separate pass (2/2/0/0 including parent). Evidence bundle `896524d986137018c7c72e4e47e58a5f8f1d693271b0b657a99ed612c00c4b37`, digest `99a343bc4a8e5a2baebf7d47afd9217b926ca5c280e718617c12b2ccd68a634f`, and the issue's refinement comment retain paths and commands. No attribution to that PR or inherited main failure is established.

Source suggests an invalid fixture boundary: producer cancellation can enqueue its refusal before the live row enters the hosted lane. Flush only acknowledges push-queue progress. A retry admitted while that row is queued can be cancelled again at its hosted head. No protocol change or decision record is needed.

## Design

Keep production cancellation, query slots and join fencing intact. Add a controlled regression separating producer cancellation from push admission: hold the first refusal's sender after recording its wire frame, enqueue and flush the complete live row behind it, then admit a deliberately premature query. Prove flush returned while its context remains live, release the writer, and require that queued row to cancel the premature query with a correlated retryable `thread.query_failed`. This diagnoses the invalid completion assumption; it is separate from the valid retry success assertion.

Repair `TestThreadCompactLiveInFlight` by following Push/FlushPushes with an acknowledged `DeliverThread` fence. Use a compact progress frame for the other, unjoined conversation: its `ErrThreadWithheld` completion proves all preceding hosted work was selected without cancelling the tested query, changing a join or adding wire output. Direct DeliverThread cases already acknowledge handling. Require the repaired case's one version-zero retry to return a final complete `thread_items` at epoch `epoch`, version 90. Preserve all compact load cases and the trailing wire sentinel. Improve unexpected error diagnostics in `testCompactReply` to include correlation and the fixture error payload.

Sizing: one inseparable fixture repair/regression; approximately 300 written lines including plan/tests, zero production files, zero exported types, zero consumer signature changes, three acceptance criteria, no new production rejection branches. Recounted before plan commit: all five limits hold. Feature branch #3240 overlaps read-only `v2session_push.go`; the touched test file has no overlap.

## Concurrency model

Run remains the sole session/lane owner. Tests observe query contexts through buffered channels and hold one sender on a channel with context cancellation as its exit path. Cleanup releases the sender before stopping the manager. Existing three-second helper bounds detect stranded work; timers are failure bounds, never the repair. No sleeps or repeat-until-pass loops.

## State transitions and identity reuse

| Event | Race-detector proof |
| --- | --- |
| Producer cancellation, refusal selected before push admission, flush before hosted handling, premature query cancelled at hosted head | `TestThreadCompactPushCompletionBoundary` |
| Refusal plus conclusive live handling, then one successful version-zero complete retry replacing compact checkpoint | `TestThreadCompactLiveInFlight` (both delivery paths) |
| Complete live row during compact load, including a prior complete join | `TestThreadCompactLiveInFlight` (four compact cases) |
| Compact live cancellation, complete/compact isolation and post-final stale fencing | `TestThreadCompactLiveQueryRace`, `TestThreadCompactLiveFencing`, `TestThreadCatchUpLiveRace`, `TestThreadCatchUpJoinedLive` |

No connection ID is reused and no new lifecycle state is introduced.

## Error handling

First refusal and controlled premature refusal must retain exact correlation, retryability and content-free `thread.query_failed` semantics via `testCatchUpError`. Properly ordered retry errors remain failures. The lane fence must return exactly `ErrThreadWithheld`; any transport failure fails the test. Successful final markers alone replace the join.

## Testing strategy

First run the named failure and preceding compact live/negotiation/refusal/reply sequence under Linux race detection. Add controlled proof and observe the old fixture assumption fail under a diagnostic variant before repairing it. Run named repaired tests and affected compact/catch-up ordering tests with `python3 scripts/test-admission.py -- go test -timeout=30m -race -count=1 -json -run '<selection>' ./internal/relay`; retain each log and test counts separately under `/tmp/builder-3731/`. Run `go vet ./...` and `go build ./cmd/pyry`. Complete race/integration acceptance belongs to the dispatcher; macOS is outside this Linux ticket's verification scope.

## Open questions

- Confirm the controlled premature query gets `thread.query_failed` from delivery-time cancellation, rather than admission: wait for its provider to enter and verify its initially live context before releasing the held sender.
- Confirm the foreign compact fence is withheld without join/query mutation; the existing full-map and provider-result assertions cover these effects.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md`, completion/live ordering: explain that Push/FlushPushes alone does not acknowledge asynchronous hosted handling; record the controlled fixture lesson. No protocol reference change is required.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: test-only change; authenticated dispatch, capability/agent gates and `threadQueryGate` remain unchanged. Assert no unsolicited complete row and no join advancement on refusal.
- Tokens/secrets: existing fake paired registry/Noise helpers only; no credentials copied or logged.
- File operations: only plan/test source and scratch evidence; no caller-derived filesystem operations.
- Subprocesses: no new subprocess invocation in product or fixture.
- Cryptography: preserve the existing real Noise test peer and single Run-owned sealing; no primitive or nonce changes.
- Network/I/O: existing in-memory transport and envelope limits; held sender observes cancellation and existing bounded test waits.
- Errors/logs: exact content-free refusal assertions retained; additional test diagnostics contain only synthetic fixture envelopes.
- Concurrency: channel-owned release plus context cancellation prevent stranded senders; inspect query/join state only under `hostedMu` or through context APIs. No production lock/order changes.
- Threat model: no change to remote authorization, prompt delivery, replay protection, token revocation or resource limits. Existing protocol deferrals remain outside this test-only change; no new deferred finding.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-21

## Revisions

- 2026-10-21: controlled diagnosis confirmed `thread.query_failed` (retryable, request 41), after successful admission and delivery-time cancellation. The first diagnostic attempt waited for provider entry and failed on that invalid assumption: `appFrameWorker` is still awaiting the held refusal delivery. Use the subsequent Run-owned control enqueue as an admission acknowledgment and inspect the matching query token under `hostedMu` instead. Before writer release its context is live; afterward the preceding row cancels it. The diagnostic old success assertion then failed with the correlated refusal in `/tmp/builder-3731/controlled-invalid-boundary.json` (1 executed, 0 passed, 1 failed, 0 skipped). Retain a negative boundary regression separately from the repaired fixture's required successful retry. Both open questions are resolved: the foreign fence is withheld without cancelling the tested query or writing joins, and the diagnostic establishes admission independently of provider execution.
- 2026-10-21: Linux checks at implementation `04228ca51d4989cde9691459541053593eca2572` passed. Command: `python3 scripts/test-admission.py -- go test -timeout=30m -race -count=1 -json -run '^(TestThreadCompactPushCompletionBoundary|TestThreadCompactLiveInFlight|TestThreadCompactLiveFencing|TestThreadCompactLiveQueryRace|TestThreadCompactLiveScope|TestThreadCompactNegotiation|TestThreadCompactRefusals|TestThreadCompactReplies|TestThreadCatchUpLiveRace|TestThreadCatchUpJoinedLive|TestThreadCatchUpInterrupted)$' ./internal/relay`. Logs `/tmp/builder-3731/repaired-focused.{json,stderr}`: 87/87/0/0 executed/passed/failed/skipped including parents, 80/80/0/0 leaves; all eleven selected top-level tests executed. `go vet ./...` and `go build -o /tmp/builder-3731/pyry ./cmd/pyry` passed, logs `vet.log` and `build.log` in the same directory. Preliminary named baseline was 7/7/0/0; local predecessor sequence was 76/76/0/0, retained separately as `baseline-named` and `baseline-local-sequence` JSON/stderr. Two earlier grouped-selector commands were refused before execution (`baseline` and `baseline-sequence` logs); explicit full-name alternatives resolved selection without broadening scope. Complete gate acceptance remains pending with the dispatcher, and the historical red and focused comparison remain distinct evidence.
