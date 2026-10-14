# Large thread-page reply race wait

## Files read
- `internal/relay/v2session_page_test.go` → `TestThreadPageLargeReply`: escaped multi-item fixture, oversized summary, reconstruction and final-marker assertions.
- `internal/relay/v2session_hosted_test.go` → `testHostedPeer.next`: three-second polling deadline, decryption and envelope validation.
- `internal/relay/v2session_test.go` → `v2Recorder.outbound`, `snapshot`: mutex-protected capture; snapshots copy envelope headers, not accumulated frame bytes.
- `internal/relay/v2session_catchup.go` → `runThreadQuery`: provider capture and complete encoding precede serial delivery.
- `internal/protocol/thread_query_encoding.go` → `EncodeThreadReply`: batch encoding and oversized-item fragmentation preserve separate coverage marker.
- `docs/specs/architecture/3235-thread-history-pages.md` § Testing strategy: preserve the large escaped-reply proof.
- `docs/knowledge/features/protocol-package-thread-messages.md` § Query replies and coverage: only the separate empty final marker certifies coverage.
- `docs/knowledge/features/v2-session-manager-test-surface-same-package-unit-tests-internal-relay.md`: authenticated same-package test harness and cleanup ordering.
- `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: compare suite composition, count execution and retain evidence.
- `CODING-STYLE.md`: bounded concurrency, contract comments and scoped changes.

## Context
At baseline `7332d5381bfef46d807ca6a22a2cca564bbc32d2`, the focused race test passed once in 2.32 seconds and ten times in 2.13–2.59 seconds. Historical complete-gate failures hit the three-second wait. The historical logs are absent locally. A repeated baseline CPU profile identifies JSON encoding/decoding and race instrumentation as substantial work; `runThreadQuery` encodes the entire fixture before first delivery. These observations justify investigating scheduling headroom, but do not alone attribute the historical failures. No production defect has been established.

One deliverable: keep the same protocol proof with a finite wait that tolerates measured expensive race work. Expected written work is under 200 lines, zero exported types, zero production consumer migrations, three acceptance criteria and no new production reject branches. No decision record or documentation change is required. Fetched feature branches have no overlapping changes to the four relay files above.

## Design
Keep ordinary `next()` at three seconds. Extract `nextBefore(deadline time.Time)` so the large-reply test can give the entire reply one absolute deadline, initially 15 seconds. This is about three times the historical successful whole-test duration of 5.48 seconds, with no resetting per fragment. Check expiration even when frames remain available. Timeout diagnostics identify consumed recorder position and the bound. Preserve every existing protocol assertion and derive the envelope-count assertion from `hostedQueueLimit`.

Log content-free elapsed provider-ready, first-transport and final-reconstruction times in the large-reply test. Compare the old wait policy and the repair under identical controlled CPU contention, using scratch overlays or prebuilt binaries; correlate delayed first transport with successful complete reconstruction. Record the actual results and revise the design if evidence identifies a different cause.

## Concurrency model
The manager's existing workers and cleanup remain unchanged. The test callback records first transport with an atomic one-shot flag; timestamps are immutable before request admission. No new goroutine or recorder synchronization is introduced. The reader uses one stopped timer per wait.

## State transitions and identity reuse
No new lifecycle state. Repeated test executions create fresh peers and retain `TestThreadPageLargeReply`'s increasing IDs, fragment identity/order and no-extra-marker checks. `TestThreadPageInterrupted`, `TestThreadCatchUpInterrupted`, `TestThreadPageProviderShutdown` and `TestThreadCatchUpProviderShutdown` retain cancellation, transport failure and retirement coverage under the package race run.

## Error handling
Malformed/error envelopes and unexpected transport closes still fail immediately. Missing initial reply, missing fragment or missing final marker fails at the shared finite deadline. The ordinary post-marker cancellation reply retains its three-second wait. Production authorization, queue capacity, envelope cap and sender behavior are unchanged.

## Testing strategy
First capture a red old-policy run under controlled contention, then pass the same workload with the bounded wait. Run at least ten named race executions without skips, complete relay race tests, module vet and binary build. The ticket explicitly requires an uncached complete `make check` with normal package concurrency, overriding the builder's usual scoped gate rule for this ticket. Run all its tiers, record named counts/skips and attribute remaining unrelated failures to follow-ups rather than claim green. Scratch logs and profiles live in `/tmp/builder-3364`; durable evidence goes in this plan's Revisions and the PR.

## Open questions
- Does controlled scheduling delay reproduce the old deadline while the exact same fixture completes correctly with the larger bound? Resolve before handoff; no product fix without evidence.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `admitThreadQuery` retains authenticated device, capability, canonical conversation and provider eligibility checks; test-only waiting does not bypass them.
- Tokens/secrets: existing Noise test credentials remain in the harness; timing diagnostics print durations and counts only.
- File operations: only a build plan and existing test sources change; no runtime file access or path contract changes.
- Subprocesses: no new product subprocesses; validation binaries use existing test fixtures.
- Cryptography: `threadPlain` and the existing sender keep Noise nonce ownership; the reader still checks unique safe sender IDs.
- Network/I/O: `protocol.MaxThreadEnvelopeBytes`, hosted queue capacity and fragment caps remain asserted and unchanged. A single absolute test deadline bounds absent completion.
- Errors/logs: timing diagnostics carry no decrypted content or provider errors; existing malformed-reply failures remain authoritative.
- Concurrency: immutable timestamps, atomic first-send flag and manager cleanup preserve race safety; no new workers or locks.
- Threat model: existing Noise confidentiality, paired-device authorization and inert content treatment remain in force. This change adds no product trust boundary.

**Reviewer:** builder self-review. **Date:** 2026-10-14.
