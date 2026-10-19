# Request-driven model catalog admission

## Files read

- `cmd/pyry/session_model_list.go` → `modelListResultFor`, `modelListResolver`: bounded resolution and capability projection.
- `cmd/pyry/daemon_live_inventory.go` → `cachedModelVocabularySource`, `inventoryResultSource`: payload/evidence pairing and persisted fallback.
- `cmd/pyry/daemon_control_live.go` → `liveResolveSource`, `liveInventoryResult`, `daemonLiveOperation.complete`: reservation, provenance and retention boundaries.
- `cmd/pyry/daemon_live_relay.go` → `daemonModelListReadingFor`, `daemonLiveCurrent`: supplied replies and unsolicited-current checks.
- `internal/relay/v2session_modelrequest.go` → `handleRequestModelList`: hosted membership and unavailable refusals.
- `internal/relay/v2session_live_replies.go` → `pushLiveReply`: correlation without recapture.
- `cmd/pyry/daemon_inventory_ingress_test.go` → `testInventoryIngress`: real parser/hold/source harness.
- `cmd/pyry/daemon_live_relay_rework_test.go` → `TestDaemonLiveRelayBorrowedBootstrapVocabulary`: authenticated borrowed-reply and reused-ID proof.
- `docs/knowledge/features/protocol-package-model-list-payload.md`: inventory is a report; selection still requires independent settings validation.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-model-list-modellistfor-seam.md`: membership must permit hosted conversations without sessions.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md`: foreign replies must not replace target retention; capability projection follows source capture.
- `docs/knowledge/features/development-verification-protocol-boundaries.md`: inspect raw source presence and re-encode decoded payloads.

## Context

A fresh model-list request can resolve retained vocabulary from the target's predecessor. Replacing the captured request source with that cached source incorrectly gives this newly resolved reply predecessor ordering. The useful deliverable is request-driven catalog admission, including its replacement and stale/clear proofs. Reconnect recovery remains #3611. No new decision record is needed.

Sizing: approximately 30 production, 310 tests/helpers and 85 plan lines; zero exported types, zero consumer signature changes, four acceptance criteria, no new reject branches. This is one behaviour and its regression proof, within all five ceilings. Fetched feature branches have no overlapping changes in the planned production file.

## Design

For `model_list` only, `liveInventoryResult` retains the operation's captured target conversation/generation/revision and copies only the resolved inventory's provenance. When cached conversation or generation differs, completion is reply-only: no target retention or publication authority. A newly resolved predecessor catalog can answer; an already captured reply keeps its old ordering forever. Matching-current and unresolved persisted results retain existing behaviour. `inventoryResultSource` and producer caches remain unchanged.

Top-level conversation routing stays authoritative and must match payload identity. Omitted/null/string session tags retain their distinct source meanings. Catalog provenance does not admit a session, change settings, reset prompts or identify every capability-merged row. Slash-command inventory and other reading families keep existing contracts. Relay access, bounds, watermarks, clear generation and projection gates are reused without relaxation.

## Concurrency model

No new goroutine or lock. `liveResolveSource` reserves under `offerMu` before resolving; owner state remains mutex-protected. Detached reply completion can race a transition, but its captured generation/revision remains subject to relay ordering. Existing publisher and authenticated harness workers retain cancellation and join paths.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Persisted unresolved catalog resolved before first turn, then replaced by a live source | `TestDaemonLiveRelayRequestedCatalog` |
| Same-conversation predecessor resolved after reused-ID clear | `TestDaemonLiveRelayRequestedCatalog` |
| Already captured predecessor delivered after successor | `TestDaemonLiveRelayRequestedCatalog`, `TestDaemonLiveRelayBorrowedBootstrapVocabulary` |
| Same-generation source replacement and stale/equal-revision delivery | `TestDaemonLiveRelayRequestedCatalog` |
| Distinct target conversations with colliding session tags; target-only clear | `TestDaemonLiveRelayRequestedCatalog` |
| Foreign bootstrap borrowed without changing cache or target retention | `TestDaemonLiveRelayBorrowedBootstrapVocabulary` |

## Error handling

No new failure mode. Existing unknown-conversation and unavailable responses remain; malformed supplied identities and envelope bounds remain relay-owned refusals. Failed or superseded completion never rewrites cache evidence.

## Testing strategy

Write authenticated request-path regression tests first and observe predecessor ordering fail before the fix. Reuse real inventory ingress and Noise wire harnesses, both capability projections, source-tag tri-state assertions, typed payload re-encoding, target clears, unchanged legacy bytes/refusals and unrelated readings. Run affected `cmd/pyry` race tests, `go vet ./...` and build the binary on macOS. Capture emitted examples in test output; record exact committed revision and built binary SHA-256 in the PR. Dispatcher owns complete suites/live gates; Mobile #2245 owns its consumer and unchanged chosen-effort scenario rerun. Original native failure stays 1 executed / 0 passed / 1 failed / 0 skipped.

## Open questions

None. No reconnect implementation or consumer change belongs to this ticket.

## Documentation handoff

Pending documentation stage: update `docs/protocol-mobile.md`, “Message envelope”, “Session-scoped live state (v2, supplied delivery contract)”, “model_list” and “Asking for a model list on demand”. Distinguish target routing/order from actual catalog provenance; publish admission, clear-before-fresh, unresolved-source and full source-replacement rules and the emitted example. Qualify generic current-session wording only for model catalogs. Reconnect fallback implementation belongs to #3611. Pending Mobile #2245: consumer admission and unchanged chosen-effort native rerun with exact daemon revision/digest and executed/passed/failed/skipped counts; preserve the original failure.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: model catalog provenance cannot authorize live settings or prompts. The `TypeModelList` branch is the sole changed boundary; `handleRequestModelList` still requires authenticated interactive access and hosted membership. Reply-only completion forbids foreign/predecessor inventory from installing target state.
- Tokens/secrets: no credential handling changes; harness credentials are synthetic. Emitted examples contain only synthetic model inventory and identifiers.
- File operations: no production file I/O added; persistence is read through the existing bounded store interface. Plan/test files are within role scope.
- Subprocesses: no production subprocess changes; existing test factory is used without starting a real child.
- Cryptography: existing Noise harness/manager own sealing; no primitive, nonce or key lifecycle changes.
- Network/I/O: existing supplied-envelope bound and capability projection apply unchanged. No new endpoint, socket or unbounded collection.
- Errors/logs: no production log or error changes; inventory bytes are emitted only by synthetic tests for the PR evidence.
- Concurrency: reservation stays before resolution; no lock-order changes. Test stale generation and revision rejection so re-resolving cannot promote an already captured reply.
- Threat model: authenticated callers may read available catalog vocabulary but gain no session ownership or settings authority. Subprocess-authored labels remain untrusted rendering data. Reconnect fallback is OUT OF SCOPE here and owned by #3611; Mobile admission is owned by #2245.

**Reviewer:** builder (self-review per checklist)
**Date:** 2026-10-19
