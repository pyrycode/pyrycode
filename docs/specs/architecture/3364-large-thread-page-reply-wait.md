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

## Revisions
- 2026-10-14: Resolved the scheduling question with four race-test processes pinned to allowed CPU 0. Diagnostic-only old-policy binaries failed 4/4 in 3.21–3.23 seconds, with provider readiness at 88–102 ms and no first transport. Identical repaired binaries passed 4/4 in 11.64–11.66 seconds: provider readiness 60–70 ms, first transport 4.01–4.06 seconds, complete reconstruction 11.53–11.56 seconds (30 envelopes, 10 fragments, 20 items). Commands: compile using `go test -race -c -o /tmp/builder-3364/{old-policy,repaired}-relay.test ./internal/relay`, then launch four `taskset -c 0 <binary> -test.run=^TestThreadPageLargeReply$ -test.count=1 -test.v` processes concurrently. Source was baseline `7332d538` plus the committed plan and diagnostic test additions; repaired variant adds the deadline helper. The profile from baseline `go test -race -count=10 -v -run '^TestThreadPageLargeReply$' -cpuprofile=/tmp/builder-3364/baseline-cpu.pprof -o /tmp/builder-3364/baseline-relay.test ./internal/relay` shows substantial JSON work and race overhead, consistent with full encoding preceding delivery. The test recorder copies only envelope headers while polling, so it does not recopy accumulated ciphertext. This demonstrates insufficient old scheduling headroom; it does not claim exact phase measurements from unavailable historical logs or a product defect.
- 2026-10-14: Retained 15 seconds for the complete reply: measured stressed reconstruction is under 11.6 seconds, leaving about 30% headroom. It never resets at a fragment. Also explicitly assert the oversized item's fragment `ItemID` and `Rev` alongside existing conversation/epoch/version/index/offset checks.
- 2026-10-14: Scratch Go overlays independently dropped all hosted replies and omitted the final encoded frame. Each named race test executed once and failed as intended at 15.04/15.06 seconds, consuming only the handshake / handshake plus 29 content frames. Commands: `go test -race -count=1 -v -overlay=/tmp/builder-3364/{missing-reply,missing-final}-overlay.json -run '^TestThreadPageLargeReply$' ./internal/relay`. No overlay modifications enter the worktree or PR. Missing reply/completion remain bounded failures.
- 2026-10-14: Candidate `cad10621bae891dddc982f4074def5483b563db3` passed `go test -race -count=10 -json -run '^TestThreadPageLargeReply$' ./internal/relay`: ten executions, ten passes, zero skips (2.13–4.40 seconds). `go test -race -count=1 -json ./internal/relay/...` passed all 1,461 relay and 455 handler tests/subtests with zero skips; large reply passed in 2.28 seconds. Both query interruption and provider-shutdown families passed. `go vet ./...` and `go build -o /tmp/builder-3364/pyry ./cmd/pyry` passed. Test sources in these runs match the candidate commit.
- 2026-10-14: Complete `make -k check GO=/tmp/builder-3364/go-uncached` at candidate `cad10621` exited 0. The scratch wrapper adds only `-count=1 -json` to every test invocation; `GOFLAGS` is empty and package concurrency remains the default. All tiers passed: vet, full-module unit race tests (26,096 tests/subtests started, 26,079 passed, 17 skipped), staticcheck, substrate guard and fake-daemon race e2e (694 started, 692 passed, two skipped). The large-reply test passed without a skip in 2.59 seconds: provider ready at 27 ms, first transport at 1.346 seconds, complete 30-envelope/10-fragment reconstruction at 2.558 seconds. Unit skips are unrelated platform/case-sensitivity, opt-in live/slow checks, ownership privilege and hosted Node/SQLite runtime requirements; inspect existing skip messages rather than infer those capabilities passed. No remaining gate failures require a follow-up. `ensurePyryBuilt` builds from this candidate worktree; `PYRY_E2E_BIN` and `PYRY_E2E_FAKE_CLAUDE_BIN` were unset. Live-Claude, live-relay and separate native tiers were not run or claimed. Only plan evidence was appended while the gate ran; all tested production/test sources match the candidate.
- 2026-10-14: E2e skips were `TestInnerFatalChild` (helper child invoked by its parent leak test) and `TestHostedAppsRecoverySQL` (requires `HOSTED_APP_NODE` and `HOSTED_APP_TEMPLATE_BUILD`). SQL runtime proof is outside this relay-only repair and is not claimed.
