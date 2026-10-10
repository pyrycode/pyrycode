# Source-bound on-demand live replies

## Files read
- `internal/relay/v2session_seams.go` → `V2SessionConfig`: optional dependencies and legacy contracts.
- `internal/relay/v2session.go` → `dispatchAppFrame`: Run-owned admission and negotiation.
- `internal/relay/v2session_appframe.go` → `appFrameJob`, `appFrameWorker`: immutable negotiation handoff and cancellation.
- `internal/relay/v2session_contextusage.go` → `resolveContextUsageRequest`: bounded asynchronous queries and refusals.
- `internal/relay/v2session_mcpstatus.go` → `resolveMCPStatusRequest`: asynchronous query capacity and teardown.
- `internal/relay/v2session_modelrequest.go` → `handleRequestModelList`: membership and capability projection.
- `internal/relay/v2session_settings.go` → both settings handlers: validation, error mapping and enrichment.
- `internal/relay/v2session_livestate.go` → `PushLiveState`, `forwardLiveState`: source validation, correlated equality and generation ordering.
- `internal/relay/v2session_livestate_test.go` → `liveFixture`, bounds and ordering tests: authenticated wire fixtures and unchanged access proofs.
- `docs/knowledge/features/relay-package.md`, `v2-session-manager.md`, settings/model-request topics: compatibility obligations.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § Supplied live-state ordering: correlation is not authorization.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: inspect decrypted original JSON and whole payloads.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: live readings never enter history.

## Context
Five on-demand paths currently lose producing-session evidence. Add optional relay seams and exercise them without daemon wiring. Provider adapters remain #3163, production readiness #3164, and history/catch-up #2963. No overlapping remote feature branches touch the planned files. No new decision record is needed.

## Design
Add optional `ContextUsageReadingFor(ctx, conversation) (LiveState, bool)`, `MCPStatusReadingFor(ctx, conversation) (LiveState, bool)`, `ModelListReadingFor(conversation, multiAgent) (LiveState, bool)`, `SessionSettingsReadingFor(conversation) (LiveState, bool)` and `UpdateSettingsReading(session, SettingsUpdate) (LiveState, error)` config functions. They are selected only for negotiated thread connections. Missing optional providers and non-thread connections retain legacy paths. False/error results never retry a legacy provider or perform another mutation. Model and settings-update providers retain their existing inline bounded-operation requirement.

A shared reply helper supplies only request correlation and calls `PushLiveState`; it does not derive source fields, family, generation, reading identity or revision. Settings reads supply a base `session_settings` payload. Relay adds effective effort, capabilities and memory search under existing presence/error rules, keyed by the supplied conversation and payload session/model. Preserve other payload fields. An unavailable settings read retains the legacy zero reply. Settings mutation failures retain every existing error mapping and emit no success reading.

## Concurrency model
Copy thread negotiation into `appFrameJob` on Run, beside multi-agent. Pass it through context/MCP workers and their existing bounded query goroutines. Settings reads use the connection-scoped worker context. Queries must honor cancellation; the worker cancels that context on requester teardown or manager shutdown. Supplied output uses the existing bounded Push queue and Run-owned sealing; no new goroutines, locks, watermarks or crypto paths.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Repeated equal reading requests, independent conversations and reading identities | `TestSuppliedLiveReplies` |
| Delayed async response overtaken by a new generation/revision | `TestSuppliedLiveReplyOvertaken` |
| Requester teardown during async query | `TestSuppliedLiveReplyTeardown` |
| Clear/fresh access gates, transitions, transport hold and cancellation | existing `TestLiveStateGates`, `TestLiveStateOrdering`, `TestLiveStatePacing` plus supplied-handler tests |

## Error handling
Preserve request validation and handler-specific unavailable/error outcomes. Invalid supplied state is rejected by `PushLiveState` without payload-bearing diagnostics. Settings enrichment failure preserves unknown/empty-provider memory reports. No fallback after a selected optional provider refuses. Supplied state never advances history/read watermarks.

## Testing strategy
Write tests first and observe the missing-seam failure. Authenticated decrypted-wire tests cover all five paths with source-only providers, dual/absent-provider fallback, omission/null/string tags, complete payloads, equal requests, two source conversations, Codex withholding, mutation errors, delayed output and teardown. Reuse existing complete-envelope/generated-clear bounds and ordering/access tests. Run `go test -race ./internal/relay/...`, `go vet ./...`, and `go build ./cmd/pyry`; verifier owns the full-module gate. No live Claude turn is required.

## Open questions
None. Recount: about 740 written lines (260 production, 410 tests, 70 plan), zero new exported types/interfaces, zero mandatory legacy-consumer updates, three acceptance criteria and no new delivery state-machine reject branches.

## Documentation handoff
Pending documentation stage: in `docs/protocol-mobile.md`, “Message envelope”, “Session-scoped live state (v2, supplied delivery contract)”, `model_list`, “Asking for MCP status on demand”, “Asking for a context usage reading on demand” and “Session settings (v2)”, describe supplied correlated replies, uncorrelated clear-before-fresh, equal-revision answerability and stale suppression while preserving settings enrichment and legacy fallback. State daemon provider installation remains #3163 and production negotiation remains #3164; catch-up/pages and replay replacement remain #2963.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: request validation remains before provider selection; `liveWithheld` gates supplied conversation even for empty clears and correlated output. Provenance is not inferred from client fields.
- Tokens/secrets: no credential generation/storage changes; no supplied payload/source strings in new diagnostics.
- Files/subprocesses: no new production filesystem or process operations.
- Cryptography: `PushLiveState` returns to Run-owned Noise sealing, preserving nonce ownership.
- Network/I/O: existing bounded ask capacity and queue apply; `validateLiveState` checks complete fresh and clear envelope bounds after enrichment.
- Errors/logs: retain fixed refusal mappings and content-free invalid-reading logs.
- Concurrency: SHOULD FIX incorporated: copy thread negotiation before worker handoff; use connection cancellation for settings enrichment as well as asynchronous asks.
- Threat model: authenticated paired-device trust remains unchanged; hostile rendered content remains the client's responsibility. Actual daemon provenance installation is deliberately #3163 and production activation #3164.
**Reviewer:** builder (self-review)
**Date:** 2026-10-10

## Revisions
- 2026-10-10: Final implementation keeps unrelated settings payload fields through `replaceObjectField`; a content-free queued event lets the withholding test observe completion before its FIFO delivery barrier. Final recount: approximately 765 inserted lines, zero new exported types/interfaces, zero mandatory legacy-consumer migrations and three acceptance criteria. The existing delivery state machine is unchanged; bounds/access/order proofs run in the relay race suite.

### 2026-10-10: Verifier finding 1 — cancel blocked enrichment independently
The original Concurrency model and Security review assumed worker return would cancel the connection context on teardown. A synchronous `EffectiveEffortFor` or `MemorySearchFor` wait prevents that return and also holds outstanding context/MCP asks alive.

**Revised concurrency contract:** `appFrameWorker` starts one connection-scoped watcher. It selects on the immutable `s.done` channel and `connCtx.Done()`; requester teardown cancels `connCtx` independently of handler progress, and manager shutdown cancels it through the parent context. Worker return cancels the context and joins the watcher. All existing ask bounds, queue ownership and Run-owned sealing remain unchanged.

**Lifecycle proof:** `TestSuppliedSettingsEnrichmentTeardown` covers both enrichment providers through authenticated encrypted requests and a decrypted correlated reply. Each case starts context-usage and MCP asks, blocks settings enrichment, closes the requester, and asserts all three contexts cancel before releasing the worker. It then verifies connection removal and absence of readings or clears. Both cases failed before the repair; run them with the race detector alongside `TestSuppliedLiveReplyTeardown`.

**Security re-review:** PASS after this repair. The watcher reads only the stable teardown channel and context, changes no source evidence, and performs no crypto, network, filesystem, credential or payload logging operations. It exits on teardown, parent cancellation or worker return and is joined by the worker. The original trust/access/ordering boundaries remain intact; the concurrency MUST FIX is addressed by independent cancellation and its lifecycle proof.
