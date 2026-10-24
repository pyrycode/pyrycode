# Disconnected channel-post catch-up reliability

## Files read

- `internal/e2e/channel_post_completion_test.go` → `TestChannelPost_E2E_DisconnectedWakeAndHistory`: complete wake, reconnect and retained-source contract.
- `internal/e2e/handshake_interactive_helpers_test.go` → `catchUpOpenConversations`: implicit discovery and catch-up before the reconnect barrier.
- `internal/e2e/internal/scenario/join.go` → `JoinConversations`: correlated final-marker recovery and explicit retryable query refusals.
- `internal/relay/v2session_hosted.go` → `hostedDeliver`, `handleHostedDone`: ordered output acceptance and 4410 delivery failures.
- `internal/relay/v2session_catchup.go` → `runThreadQuery`, `cancelThreadQuery`, `threadQueryGate`: query cancellation and final-marker checkpoint cut.
- `internal/relay/v2session_thread_delivery.go` → `deliverThread`: delivery outcome acknowledgement and caller cancellation.
- `docs/knowledge/features/e2e-harness.md`: socket readiness is not child readiness; retain failures and use observable barriers.
- `docs/knowledge/features/control-plane-channel-post-live-delivery.md` § Live announcements, bounded replay and durable recovery: recovery uses explicit threads; no raw replay.
- `docs/knowledge/features/development-verification.md`: a focused repeat does not replace full-gate evidence.
- `docs/protocol-mobile.md` § Security model: authenticated remote access and content-blind relay boundaries.

## Context

On `c3399fec95`, the #3844 Linux gate received 4410 during returning-phone catch-up after noise_resp. The named repeat passed. A focused baseline on `368e1580653d29140beb5887a874b45a1a0f4656` reproduced two such closes in 20 executions, plus one separately investigated failure. Evidence is retained in `/tmp/builder-3849-baseline.json`; the original gate remains `/work/Projects/pyrycode-agents/logs/verifier-gate_#3844_9.log`.

One deliverable: repair the demonstrated reconnect failure with a controlled regression, preserving the scenario's wake and history assertions. No decision record is expected. Initial estimate remains approximately 350 written lines, one production file, zero new exported contracts, no consumer migrations, three acceptance criteria and fewer than ten new reject branches. Total work must remain below 800 lines.

Overlap: #3741 changes optional interactive eligibility in `v2session_hosted.go`; its local guard change is independent of this diagnosis. Keep edits local and additive.

## Design

First retain daemon diagnostics when the named scenario fails, then identify the precise close path and triggering ordering. Add a controlled regression using the existing ordered-output test seams before changing production behavior. The repair must retain authenticated dispatch, bounded output, nonce safety, cancellation and final-marker semantics. Do not retry an entire scenario or add a readiness sleep. Record the demonstrated cause and concrete production contract in a prospective revision before implementation.

## Concurrency model

The manager Run goroutine owns session and hosted output selection. Output workers report bounded send results; query workers wait for ordered acceptance. Existing contexts and teardown stop workers. No new long-lived goroutine or shared-helper signature migration is planned.

## State transitions and identity reuse

| Event | Required race proof |
| --- | --- |
| Connected turn, peer close, disconnected post, new authenticated connection and catch-up | `TestChannelPost_E2E_DisconnectedWakeAndHistory`, 20 independent daemon/relay executions |
| Demonstrated query/output ordering causing an unexpected close | Controlled regression to be named in Revisions before implementation |

## Error handling

Keep genuine transport failure, revoked authentication and nonce ambiguity fail-closed. Preserve retryable, correlated query failures and all negative replay/wake checks. Diagnostics may name fixed events and connection identity, never tokens, posted text or raw crypto data.

## Testing strategy

Retain Linux race JSON, exact source revisions and executed/passed/failed/skipped counts for baseline, controlled red/green regression and 20 final named E2E executions. Run affected package checks through test admission, plus `go vet ./...` and `go build ./cmd/pyry`. Full offline acceptance belongs to the dispatcher; macOS is outside this ticket's scope.

## Open questions

- Which daemon close path and ordering explain 4410? Resolve with failure diagnostics and a controlled test before production edits.
- What caused the distinct baseline failure? Preserve and inspect independently; file an out-of-scope defect if needed.

## Security review

**Verdict:** PASS for bounded diagnosis and regression; review the concrete repair in Revisions before production edits.

**Findings:**

- Trust boundaries and threat model: preserve Noise authentication in `handleNoiseInit`/`handleNoiseMsg` and membership checks in query admission; no new network input or authority.
- Tokens and cryptography: no key, token or nonce generation change; retain fail-closed behavior for ambiguous encrypted delivery.
- File operations and subprocesses: use existing isolated HOME and child cleanup; no new production file paths or subprocess calls.
- Network and I/O: preserve existing queue, envelope and delivery deadline bounds.
- Errors/logs: failure diagnostics are restricted to the existing content-free daemon logs, not message bodies or credentials.
- Concurrency: MUST retain cancellation, session identity fences and shutdown. Concrete ordering fix requires an additional review before implementation.

**Reviewer:** builder (self-review)
**Date:** 2026-10-24

## Revisions

- 2026-10-24, prospective diagnostic refinement: failure-only E2E stderr retention reproduced an EOF at the same returning-phone boundary, with binary transport generation replacement. Add a fixed `v2.hosted.delivery_failed` event in `handleHostedDone`, classifying cancellation, deadline or other send failure without raw error text, plus a controlled assertion on that existing failure path. This distinguishes query cancellation from missing-session rejection without changing close behavior. Security review: PASS; only fixed event/reason and connection identity are logged. The QMD search confirms wake completion and source retention are separate from thread publication.
- 2026-10-24, prospective repair: `/tmp/builder-3849-close-reason.json` reproduces 4410 with `v2.hosted.delivery_failed reason=canceled`, after authenticated catch-up begins. A completed post's asynchronous live publication can arrive during the query's multi-envelope delivery. `cancelThreadQuery` currently cancels even an update already included in the selected reply snapshot; cancellation of that sealed prefix closes the connection by design. Preserve that fail-closed rule for genuinely newer/different-epoch progress. Under `hostedMu`, record a complete query's selected reply epoch/version on its first `thread_items` envelope (including continuation parts). Both producer-side cancellation and Run's live gate must withhold already-covered updates for that conversation/projection. Do not record a completed join until the final marker; cancellation before selection still yields the existing explicit refusal. Check query cancellation again under the selection lock. No off-Run writes to the captured checkpoint.
- Controlled race proof: `TestThreadCatchUpCoveredLiveDuringDelivery` holds a nonfinal selected reply, injects older/same-version live updates before releasing transport, then authenticates the prefix, correlated final marker and a sentinel with consecutive sender IDs. Existing `TestThreadCatchUpInterrupted`, `TestThreadPageInterrupted` and `TestThreadDeliveryActiveCancellation` retain genuinely newer update and active cancellation retirement. The captured checkpoint belongs to the query/inbox generation and disappears at normal query completion or teardown; no identifier is reused across reconnect. Scope reassessed at approximately 450 written lines, two production files, zero exports/migrations, three acceptance criteria and fewer than ten new branches. Open questions resolved: the distinct baseline failure was EOF at the same catch-up boundary, not a separate assertion.
- Concrete security review: PASS. Only authenticated complete-query payloads establish the temporary checkpoint; conversation, epoch, projection and monotonic version all constrain suppression. No successful join is asserted before final selection, no cryptographic counters are reused, genuinely ambiguous active sends still retire, and existing output/time/memory bounds remain. Compact/expansion query semantics stay with their existing gates. No new goroutines, credentials, files or subprocess boundaries.
