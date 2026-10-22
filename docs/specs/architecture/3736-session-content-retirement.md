# Retire raw session and content notice publication

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `emit`: source append precedes publication and replay.
- `cmd/pyry/session_transition_v2.go` → `broadcast`, `capture`, `publishSwitch`, `Run`: captured ownership, source recording and switch completion lanes.
- `cmd/pyry/runtime_history.go` → `runtimeDivider`: retained runtime boundary contract.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyRuntimeReceipt`: shared adapters still have consumers owned by #3737.
- `cmd/pyry/interactive_content_retired_test.go` → `testRetiredThreadUpdate`: canonical live/catch-up comparison and storage controls.
- `cmd/pyry/session_transition_v2_test.go`, `session_transition_v2_history_test.go`, `agent_switch_publication_test.go`: source, observer, attribution and switch-order assertions to retain.
- `cmd/pyry/interactive_turn_v2_status_test.go`, `interactive_turn_v2_session_test.go`: notice lifecycle neutrality and session settings.
- `docs/knowledge/features/thread-package.md` § Shadow lifecycle and evidence: raw retirement must apply independently of capability and storage.
- `docs/knowledge/features/history-package-producers.md` § Producers and Legacy transition provenance: exact-session capture precedes queueing; runtime closure precedes dividers.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: cleanup-phase source/live-state separation.
- `docs/knowledge/features/development-verification.md`: focused proof, retained failures and dispatcher acceptance boundaries.

## Context

#3736 is the grandchild #3714 → #3718 → #3736. Its two useful slices are session/divider publication retirement and content-notice retirement. Refinement already recorded `needs-human:sizing` under the split-depth rule; build both here. #3734/#3735 are merged. No overlapping remote feature branch touched the planned production/test paths at planning time. #3737 owns remaining banners, runtime/prompt receipts, channel posts and exclusive shared adapter removal. No new decision record is needed.

Forecast: about 650 written lines including tests, helpers and this plan, plus deletion of obsolete positive publication assertions; two production files, zero new exported types/interfaces, zero simultaneous consumer API migrations, four acceptance criteria, no new reject branches. Constructor APIs stay stable. The written plan remains within those limits; the grandchild exception applies if necessary assertion migrations exceed the line forecast.

## Design

Extend `interactiveTurnEmitterV2.emit`'s post-append retirement cases with both refusal types, compaction boundaries and unrecognized messages. Preserve marshalled source bytes, timestamp, visibility and captured source. Nil/failed history must never restore publication or replay.

`sessionTransitionEmitterV2.broadcast` retains its runtime divider append and validated/resolved transition append. Remove its divider receipt and transition fanout, plus exclusive envelope counter/import. Keep `switched` after recording, and keep `publishSwitch`/`Run` flush/completion barriers. Rewrite comments on touched declarations to describe source recording. Keep the broadcaster constructor/field because completion still flushes the manager.

Replace raw-positive assertions with retained source/canonical assertions. Async tests use source history or completion barriers rather than a removed frame. Reuse canonical publisher/catch-up helpers to prove dividers, notices and compaction agree without duplicate IDs or cross-conversation/source attribution. Preserve mixed control, cancellation, busy-state and predecessor/successor checks.

## Concurrency model

No new goroutine, lock or queue. Existing emitter drains serialize state. Runtime boundaries stay on the stream sink lane; switches retain their daemon-context cancellation and completion paths. Canonical store workers and publisher shutdown remain joined by existing owners. Tests must not inspect unsynchronized broadcaster state while a drain is active.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Reset, clear, agent switch, recovery, sleep/eviction and repeated rotation | `TestSessionContentRetired`, existing runtime/agent-ending and switch publication tests |
| Delayed predecessor output, captured ownership after rebinding, reused session IDs | `TestSessionTransitionHandoff_CapturesBeforeDelay`, `TestSessionTransitionHandoff_CommittedSwitch`, existing daemon live and runtime boundary tests |
| Refusal outside/inside a turn, compaction and unsupported content | `TestSessionContentRetired`, retained interactive status/session tests |
| Repeated canonical publication and explicit reconnect catch-up | `TestSessionContentRetired` |
| Nil/failed storage, no clients, capability differences | `TestSessionContentRetiredStorage` |
| Switch cancellation and concurrent observer teardown/busy release | `TestSessionTransitionSwitchPublicationCancellation`, `TestStartSessionTransitionStreamV2_ConcurrentFires` |

## Error handling

Retain unknown-reason and unresolved-owner drops, marshal failure handling and content-free append diagnostics. Storage failure does not restore raw publication or prevent switch completion. Canonical unavailable/gap behaviour and supported live-state error handling remain unchanged.

## Testing strategy

Write retirement regressions first and observe their failure on the unchanged publishers. Run admitted Linux race tests for affected `cmd/pyry` tests/package, then `go vet ./...`, `go build -o /tmp/builder-3736/pyry ./cmd/pyry` and tagged `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...` when helper migrations affect tagged tests. Record exact candidate, commands and Go JSON test-event counts in the PR. Full-module/fake integration acceptance and configured live-Claude acceptance remain pending for dispatcher/verifier; no extra live suite is requested.

## Open questions

None. Current refined issue and parent archive supply the cleanup contract; old recovery waits were superseded by merged prerequisites.

## Documentation handoff

Pending for documentation stage:

- `docs/protocol-mobile.md` § Application message types, § Daemon thread updates (v2, supplied delivery contract), and the five retired message subsections: session transitions, both refusal types, compaction boundaries, unrecognized messages and divider receipts are retired for every app. Retained source records feed authoritative thread items; supported live state/controls remain separate.
- `docs/knowledge/features/thread-package.md` § Shadow lifecycle and evidence and `docs/knowledge/features/history-package-producers.md` § Producers: record the same source/publication distinction and counted evidence. Do not claim #3737 residual cleanup or #3718 final acceptance is complete.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `emit` suppresses raw child-authored notices after recording for every capability/storage mode. `capture` and `broadcast` retain exact-session ownership and unresolved-owner rejection; no replacement binding may attribute delayed output.
- Tokens/secrets: no credential generation/storage changes. Notice prose and unsupported raw JSON stay out of diagnostics; storage-failure tests assert private sentinels are absent.
- File operations: existing `appendConversationHistory` and history-store validation/permissions remain the only writes. No new path or file operation is introduced.
- Subprocesses: no spawn, argv, environment or process termination changes.
- Cryptography: no keys, nonces, randomness or cryptographic comparisons change.
- Network/I/O: remove content sends without changing relay authentication, size caps, host isolation or supported controls. `FlushPushes` still orders committed-switch rows/live state.
- Errors/logs: retain content-free reason/event diagnostics; remove obsolete push-error logging with fanout. No raw payload/error text is added.
- Concurrency: preserve serialized runtime boundary recording, closure, busy release and switch completion; failed history must not bypass completion. Existing cancellation and race tests remain required.
- Threat model: authenticated canonical thread routing remains authoritative; no live deployment/data migration. OUT OF SCOPE: residual legacy banners/receipts/adapters belong to #3737, final family acceptance to #3718.

**Reviewer:** builder (self-review per checklist)
**Date:** 2026-10-22
