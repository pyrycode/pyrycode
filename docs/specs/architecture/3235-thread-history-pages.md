# Bounded relay thread history pages

## Files read
- `internal/relay/v2session.go` → `dispatchAppFrame`: authenticated request interception.
- `internal/relay/v2session_appframe.go` → `appFrameWorker`: connection-owned off-Run work.
- `internal/relay/v2session_seams.go` → `V2SessionConfig`: additive provider seam.
- `internal/relay/v2session_catchup.go` → `admitThreadCatchUp`, `runThreadCatchUp`, `threadQueryGate`: shared admission, cancellation, slot and completion watermark.
- `internal/relay/v2session_thread_delivery.go` → `deliverThread`: serial outcomes and generation fencing.
- `internal/relay/v2session_hosted.go` → `hostedDeliver`, `hostedAuthorized`: late eligibility and pairing checks.
- `internal/relay/v2session_catchup_test.go` → `testCatchUpPeer`, interrupted/live/joined tests: authenticated fixtures and synchronization.
- `internal/protocol/thread_query.go`, `thread_query_encoding.go` → `ThreadPagePayload`, `NormalizeThreadQueryLimit`, `EncodeThreadReply`: page fields and bounded full-item encoding.
- `internal/thread/queries.go` → `HistoryPage`, `QueryResult`: detached backend selection and parent closure.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`: independent dispatch classification.
- `docs/knowledge/features/thread-package-background-store.md` § Bounded queries: ties/parents exceed limits; neither extras nor short pages establish coverage.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § Thread delivery and summary projection: late authorization, shared completion fence and legacy refusal lane.
- `docs/knowledge/features/relay-package.md`, `protocol-package-thread-messages.md`, `development-verification.md`, `docs/protocol-mobile.md` § Thread query messages / Security model: package contracts, wire presence and threat boundaries.

## Context
Apps need older pages forming an unbroken ordered range, with authoritative older-exists evidence. The backend and codec already implement selection and framing. This ticket supplies relay handling; production providers remain #3236. ADR 042 already defines this contract; no new decision record is needed.

## Design
Add `ThreadPage(context.Context, protocol.ThreadPagePayload) (thread.QueryResult, error)` to `V2SessionConfig`. Generalize catch-up admission and its worker to both query types. Require explicit string conversation and unsigned before-order (including zero), validate optional signed limit presence/types, normalize before provider access, then reuse canonical/registered conversation, thread/interactive, pairing and Codex gates. Uncorrelatable identity closes; other denials return content-free correlated errors.
Both requests reserve the same generation-specific query token and bounded join map. The existing worker selects the configured provider, maps every supplied full item without filtering or trimming, and encodes through `EncodeThreadReply`. Pages forward all four backend page fields, omit from-version and never infer bounds from rows. Reset results are invalid for the page provider and fail. Deliver serially, with exactly one separate empty final marker only after all prior deliveries succeed. Check cancellation during mapping and around encoding/delivery; no new goroutine, buffer, history read or fold engine.
Keep the existing live admission cancellation and atomic final-marker watermark cut. Late withholding and transport failure cannot certify partial replies. Update the command guard's dispatch classification. Legacy raw history remains unchanged.
Overlap: #3240 adds local permission instrumentation to `v2session.go`; no shared block rewrite or dependency.
Sizing, checked against this plan: one deliverable; about 140 production + 480 tests/helpers + 65 plan lines (<800 written lines), no new exported type/interface, zero migrating consumers, three acceptance criteria, at most ten rejection/error categories inherited from catch-up.

## Concurrency model
Run owns admission, session identity, Noise and output stamps. The existing connection worker owns provider/mapping/encoding and waits serially for transport acceptance. Query cancellation derives from the hosted inbox lifetime. `hostedMu` protects slot/join state and fences live cancellation against completion; no new lock or nested queue locks. Provider contexts must be honored; worker teardown follows existing connection/manager cancellation.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Repeated pages using returned continuation; empty and short captures | `TestThreadPageResults` |
| Page/catch-up competing for one slot and join capacity | `TestThreadPageSharedAdmission` |
| Live update during capture; retry after refusal | `TestThreadPageLiveRace` |
| Completion watermark, represented updates and later same-version patches | `TestThreadPageJoinedLive` |
| Interrupted fragments, late eligibility loss, transport failure/down-up, disconnect and reused conn ID | `TestThreadPageInterrupted` (shared catch-up scenario helper) |
| Provider shutdown | `TestThreadPageProviderShutdown` (shared catch-up scenario helper) |

## Error handling
Malformed fields, ineligibility/missing provider, busy/join capacity, nonusable/error/reset provider result, codec failure, live cancellation, withheld delivery and transport/retirement use existing generic terminal failure semantics. Invalid envelope identity closes. Source errors/content are never logged or echoed; no partial success marker.

## Testing strategy
Write authenticated page admission/result tests before implementing. Assert complete supplied fields/content, lower-order ties, hidden/dropped rows and deduplicated multi-level parent extras, independent authoritative bounds/older flag, empty arrays and successive exclusive continuations. Reuse existing catch-up lifecycle scenarios for both request types, and assemble oversized escaped replies spanning >16 envelopes while checking correlation and the stamped envelope cap. Run relay race tests, the command dispatch guard, module vet and binary build; full-module gate belongs to the verifier.

## Open questions
None. Selection and deduplication belong to the backend; relay forwards its detached result.

## Documentation handoff
Pending documentation stage: `docs/protocol-mobile.md`, `thread_page` message-table row and “Thread query messages”: mark page handling implemented; document exclusive upper/inclusive lower bounds, continuation, authoritative older-exists, parent extras and terminal failures; extend query/live ordering to pages. Keep production provider installation pending #3236.
Pending documentation stage: `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, “Thread delivery and summary projection”: link the implemented page handler and unchanged legacy path.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: shared admission validates required types, canonical registry identity and negotiated/current access before provider calls; `threadWithheld` rechecks every delivered content envelope.
- Tokens/secrets: reuse authenticated pairing and revocation checks; no credential generation/storage or content-bearing diagnostics.
- File operations: detached provider results only; no path construction, filesystem access or writes. Production installation belongs to #3236.
- Subprocesses: no subprocess or shell operation in this path.
- Cryptography: existing single-owner Noise sealing/stamping and retirement after interrupted sealed sends remain authoritative.
- Network/I/O: existing inbound cap, bounded worker/output queues and 256-conversation join cap remain; codec reserves all stamps and bounds every escaped envelope at `MaxThreadEnvelopeBytes`.
- Errors/logs: generic correlated errors only, with no raw request/provider errors or content.
- Concurrency: both query kinds share the generation-owned slot, cancellation and atomic final-marker cut under `hostedMu`; serial delivery stops at the first failed part.
- Threat model: Noise protects relay confidentiality; current pairing/registry/agent checks prevent unauthorized query disclosure. Agent content stays inert data for client rendering; queries execute no content. Production provider/storage responsibility remains #3236.
**Reviewer:** builder self-review. **Date:** 2026-10-12.

## Revisions
- 2026-10-12: Generalized names are `admitThreadQuery`, `runThreadQuery` and `appFrameThreadQuery`; the existing completion fence is unchanged. Page providers returning catch-up-only reset state fail explicitly.
- 2026-10-12: Authenticated malformed-ID tests exposed that a full-envelope dispatch probe bypassed query admission for wrong-type IDs. Probe the type independently so query admission applies its connection-close policy; other malformed types retain dispatch fallback. Current device revocation already closes at the authenticated inbound gate before query admission.
- 2026-10-12: Reuse catch-up requested-scope and lifecycle scenarios for both query kinds, retaining original assertions and adding explicit failure-code checks. Additional coverage: `TestThreadPageContinuation`, `TestThreadPageLargeReply`, `TestThreadPageLegacyRefusal` and `TestThreadPageRequestedScope`. Final written work remains below 800 lines with no exported types or migrating consumers.
- 2026-10-12: The shared interrupted-send fixture must record its accepted prefix before signaling the cancellation gate, and signal callback exit on every return. Otherwise cancellation can beat the recording callback and strand the test's completion wait; this changes test synchronization only.
