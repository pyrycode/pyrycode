# Installed bounded thread queries (#3236)

## Files read

- `cmd/pyry/daemon_thread_relay.go` → `installDaemonThreadRelay`, `startDaemonThreadPublisher`, `daemonThreadItem`: production readiness, shared store and full-item mapping.
- `cmd/pyry/relay.go` → `startRelayV2`: installs adapters before publisher startup and connection admission.
- `cmd/pyry/thread_shadow.go` → `threadShadow`, `follow`, `shutdown`: existing conversation and publisher ownership.
- `cmd/pyry/daemon_live_relay_test.go` → `testDaemonRelayWire`: authenticated encrypted transport fixture.
- `cmd/pyry/thread_shadow_test.go` → `testShadow`: real history/store fixture with joined cleanup.
- `cmd/pyry/daemon_thread_relay_test.go` → `TestDaemonThreadPublication`: real publisher and independent wire-state reconstruction.
- `internal/relay/v2session_catchup.go` → `admitThreadQuery`, `runThreadQuery`, `threadQueryGate`: authenticated admission, exhaustive mapping and successful-marker cut.
- `internal/relay/v2session_thread_delivery.go` → `DeliverThread`: ordered acceptance and cancellation contract.
- `internal/thread/queries.go`, `internal/thread/catch_up.go` → `HistoryPage`, `CatchUp`: one detached captured publication, without loading or reading history.
- `internal/thread/catch_up_test.go` → `TestCatchUpBoundedRecovery`, `TestCatchUpEvidence`, `TestCatchUpMissingHistory`: bounded work and late-public-appearance evidence.
- `internal/protocol/thread_query.go` → `ThreadItemsPayload`: coverage belongs only to a separate final marker.
- `docs/knowledge/features/thread-package.md` § Shadow lifecycle and evidence; `thread-package-background-store.md` § Bounded queries: recovery and publication preparation are separate from bounded requests.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § Thread delivery and summary projection: preserve recipient eligibility and legacy behavior.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, ADR 042 and `docs/protocol-mobile.md` § Security model: conventions, evidence boundaries and threat model.

## Context

Install the existing bounded query engine on the publisher's actual store. This completes one deliverable: production thread initialization and older-page reads with storage-to-wire evidence. ADR 042 already owns the decision; no new decision record is needed. No other fetched feature branch overlaps the production file.

Sizing: approximately 700 written lines (under 800), zero new exported types/interfaces, one existing installation site, four acceptance criteria, zero new relay state-machine branches. Existing lifecycle/backend proofs remain with their owners.

## Design

`installDaemonThreadRelay` clears query providers alongside readiness when storage is absent or not the real store. Install `ThreadCatchUp(ctx, request) (thread.QueryResult, error)` by forwarding to `Store.CatchUp`, and `ThreadPage(ctx, request) (thread.QueryResult, error)` by forwarding to `Store.HistoryPage`. Forward the supplied context and named conversation unchanged; return the result/error unchanged. No loading, history access, full projection or additional query engine occurs in these adapters.

Readiness requires both installed queries, live-state/current providers, the authoritative watermark provider and a running publisher attached to this store. Authentication, membership, interactive/agent gates, mapping, framing and query/live joining remain in relay. Nonusable store views therefore reach the existing explicit terminal error path.

Add focused tests under `cmd/pyry`, reusing `testShadow` and `testDaemonRelayWire`. Compare wire results with complete real-store states and independently selected expected IDs/ranges. Commit a generated 36,000-entry test scenario; do not commit generated logs. Measure recovery separately; adapter allocations and absence of history access complement the retained backend visit/allocation proofs.

## Concurrency model

No new production goroutines or ownership changes. Queries run on existing relay workers with connection-scoped request contexts; Store captures immutable publications. Existing publisher/shadow cancellation and joined shutdown remain authoritative. Test barriers must release before fixture cleanup and join any test-owned publisher operation.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Clean shutdown/reopen, saved epoch/version and old-revision first appearance | `TestDaemonThreadQueriesRecovery` |
| Query completion followed by live changes; query racing publication fails then retries | `TestDaemonThreadQueriesJoin` |
| Incomplete fragments, reset/error and disconnected query never certify completion | existing relay query/page delivery tests plus `TestDaemonThreadQueriesJoin` |
| Not-loaded/rebuilding/unavailable views and cancelled requests | `TestDaemonThreadQueriesReadiness` |
| Repeated newest/catch-up/page requests over recovered 36,000 entries | `TestDaemonThreadQueriesBounded` |

## Error handling

Return backend validation/cancellation errors and nonusable/reset results intact. Relay translates them to correlated generic errors or `thread_reset`. Reset scenarios include epoch mismatch, future version, gap 1025, missing-ID and suppression evidence; gap 1024 remains successful. No additional rejection branch is added to relay.

## Testing strategy

Write installation tests first and observe missing-provider failures. Exercise nonzero and zero catch-up, page bounds/ties/parents/empty/short metadata, clean reopen, resets, escaped multiframe queries, real live publication and simultaneous legacy delivery. Preserve backend work-count tests and relay lifecycle tests. Run focused integration with `go test -race`, touched-package `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-3236/pyry ./cmd/pyry`. The verifier owns `make check` and the full-module gate; no live Claude turn is required.

## Open questions

None. Existing query/delivery contracts govern all race and reset outcomes.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md`, message table, “Thread query messages”, “Capability negotiation” and “Reconnect / Backfill semantics”: mark catch-up/pages and thread-only replay replacement production-installed; state explicit per-open-conversation requests and successful-final-marker completion, preserving legacy behavior and the migration-step-6 cleanup boundary.
- `docs/knowledge/features/thread-package-background-store.md`, “Bounded queries”: replace the #2963 pending-wire handoff with installed providers; distinguish bounded request cost from recovery/publication preparation.
- `docs/knowledge/features/thread-package.md`, “Shadow lifecycle and evidence”, and `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, “Thread delivery and summary projection”: document complete query readiness and explicit per-open-conversation initialization, including explicit failure for nonusable views and fail-on-race/retry semantics.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `admitThreadQuery` runs after Noise authentication and checks canonical conversation identity, membership, interactive/multi-agent eligibility and current hosted authorization before calling either installed provider. Providers retain the Store validation boundary.
- [Tokens/secrets/cryptography] No new credentials, key storage or primitives. Queries use the existing Noise authenticated encrypted lane and do not emit secrets or executable content.
- [File operations/subprocesses] Request adapters perform no file operations and spawn no processes; recovery remains background Store work. Fixtures use private temporary directories.
- [Network/I/O] Existing admission bounds, single in-flight query, bounded delivery queues and `MaxThreadEnvelopeBytes` remain authoritative. Oversized escaped results must use the existing continuation encoder.
- [Errors/logs] `runThreadQuery` emits content-free generic failures. No raw query content or filesystem error is newly logged.
- [Concurrency] Existing Store capture and relay marker/live cancellation cut prevent mixed publications and false completion. No new lock nesting or goroutine lifecycle.
- [Threat model] Protocol threats 2/3/4/6 retain Noise identity, encryption, device authorization and nonce protections. Query content is inert display data under threat 1. Threat 7's broader rate-limiting posture and threat 8's key-storage roadmap remain with the protocol owners; this installation preserves existing bounded admission.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-12
