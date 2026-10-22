# Retire interactive assistant/tool/completion publication

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `emit`, `flushDelta`, `HandleFor`: append precedes publication; buffers retain producing-source identity.
- `cmd/pyry/interactive_turn_v2_history_test.go` → `historyEntries`, history fixtures: durable payload/order evidence independent of transport.
- `cmd/pyry/interactive_turn_v2_test.go`, chunk/parent-lane/provenance/drain tests → buffering, phase, source and lifecycle guarantees to retain.
- `cmd/pyry/daemon_thread_relay_test.go`, `daemon_thread_offscreen_test.go`, `thread_shadow_legacy_test.go` → actual publication/query wiring and retirement evidence.
- `cmd/pyry/queued_canonical_history_test.go` → `TestQueuedCanonicalCompletedTurn`: previous retirement's source-backed assertion pattern.
- `internal/e2e/relay_v2_stream_parent_events_test.go`, `internal/e2e/realclaude/interactive_stream_multiturn_continuity_test.go` → mixed legacy assertions also protect queue liveness and child continuity.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Item model and Sessions, agents, messages, read marks: canonical content/identity contract.
- `docs/knowledge/features/thread-package.md` and linked Shadow lifecycle and evidence → publication acquisition is silent; explicit catch-up initializes peers.
- `docs/knowledge/features/history-package-producers.md`, `history-package-producers-interactive-provenance.md` → source changes flush the preceding buffer; phases and source lifecycles differ.
- `docs/knowledge/features/streamsup-package.md`, `e2e-harness.md`, `e2e-realclaude.md`, `development-verification.md`, `CODING-STYLE.md` → focused checks, tagged compilation and test admission.

## Context

The archived #3718 contract allocates assistant/tool/turn-end retirement here. Main at `1d37ba65` includes prerequisite #3728; all five recorded prerequisites are closed. Recovery/refiner evidence contains no retained implementation or candidate test proof. No open feature branch overlaps the production file in the fetched inventory. Shared adapters remain needed by #3735–#3737. No new decision record is needed.

One behavior: interactive content has only the canonical thread publication path. The five source values remain necessary fold inputs. Forecast: approximately 760 written lines including plan, focused proof and assertion migration, with legacy-only deletions reported separately; zero new exported types, zero production constructor updates, three criteria, one publication rejection branch. Recount before implementation and at handoff.

## Design

In `interactiveTurnEmitterV2.emit`, after the existing single timestamp/history append, return for `assistant_delta`, `tool_use`, `tool_result`, `tool_denied`, and `turn_end`. This applies to every recipient capability and storage result, before receipt projection or replay insertion. Preserve constructor and mapping APIs, source structs, coalescing/chunking, phase/progress, agent/runtime consumers and shared adapters.

Tests needing content inspect real stored history or authoritative thread items. Delete exclusive raw-delivery positives/helpers, including skipped compatibility cases; retain mixed lifecycle, ordering, provenance, recipient isolation, busy-state, rotation, closure and liveness guarantees. Do not synthesize compatibility transport to keep old assertions green.

## Concurrency model

No new goroutine, lock or lifecycle. The existing drain exclusively owns emitter state and buffers. History/thread workers and publisher keep their current cancellation/join paths. Test readers wait on committed versions before checking canonical output.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Interleaved main/child text and tool completion; duplicate completion | `TestInteractiveContentRetired` |
| Reused message/tool IDs in another conversation or producing session | `TestInteractiveContentRetired`; existing interactive provenance and parent-lane tests |
| Timer/boundary/shutdown flush and repeated turns | existing emitter coalescing/chunking and drain tests, migrated to history |
| Nil/failed history, no recipients, non-thread recipients | `TestInteractiveContentRetiredStorage`; history tests |
| Explicit catch-up and live canonical updates after publication acquisition | `TestInteractiveContentRetired` |
| Offscreen completion/retirement | `TestDaemonThreadOffscreenShownRecovery` |

## Error handling

Marshal and append failures keep content-free diagnostics. Failed/nil storage never restores retired publication. Supported live-state delivery and push-failure handling continue independently. Unsupported source content retains its existing canonical notice; supported source types do not become unsupported notices.

## Testing strategy

Write the retirement regression first and observe raw frames/replay fail it. Drive real emitter producers with interleaved attributed events; verify source payloads/provenance, ordered settled canonical items, live publication and catch-up, and absent retired frames/receipts/replay. Cover nil/failed storage and no clients. Run affected `cmd/pyry` race checks on Linux through test admission, plus `go vet ./...` and `go build -o /tmp/builder-3734/pyry ./cmd/pyry`. Compile affected e2e/tagged suites if their assertions/helpers change. Record exact candidate and executed/passed/failed/skipped counts in the PR. Full configured acceptance and inherited live/release checks remain dispatcher-owned and pending.

## Open questions

None. Actual raw delivery assertions determine the local migration surface; sibling production removals remain excluded.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md`, **Application message types**, **Daemon thread updates (v2, supplied delivery contract)** and **assistant_delta**, **tool_use**, **tool_result**, **tool_denied**, **turn_end** subsections/examples: “The interactive emitter retains these values as source history for canonical thread updates and explicit catch-up; it sends no raw assistant/tool/turn-end frames or newly appended reconnect copies to any app. Supported live state remains separate.” Distinguish channel-post publication still awaiting #3737.
- `docs/knowledge/features/thread-package.md`, **Shadow lifecycle and evidence**, and linked `thread-package-shadow-lifecycle-and-evidence.md`, same heading: distinguish retained source folding from retired interactive compatibility publication; describe focused regression without presenting replay or compilation as live acceptance.
- `docs/knowledge/features/history-package-producers.md`, **Producers (#2114, #2115)**, and `history-package-producers-interactive-provenance.md`, **Interactive provenance (#2981)**: interactive assistant/tool/completion recording, source attribution and flush order survive transport retirement, including nil/failed-storage behavior for retained live state; raw content has no fallback publication.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `emit` classifies daemon-owned mapped type names after recording untrusted child payloads. MUST FIX avoided by placing the guard before all receipt/fan-out paths, including non-thread peers and failed history.
- [Tokens/secrets] No credentials are introduced or read. Thinking/signature and content bytes remain excluded from operational diagnostics; test storage failures with content markers.
- [File operations] Reuses `appendConversationHistory` and history's validated conversation boundary; no production path/permission/atomic-write changes.
- [Subprocesses] No spawn, argument, environment or termination changes. Tagged test migration must retain child continuity and queue liveness assertions.
- [Cryptography] No keys, nonces or cryptographic operations change; recipient eligibility remains with the existing authenticated relay.
- [Network/I/O] No new endpoint or socket. Returning before `publishLegacyHistory` prevents raw replay insertion as well as fan-out; thread delivery retains existing bounds and access gates.
- [Errors/logs] Append remains attempted without clients; failures retain discriminants only, never fallback content. Regression checks content-free failure logs.
- [Concurrency] Single-drain ownership and existing worker cancellation/join paths remain; no new locks.
- [Threat model] Cross-conversation/session identity reuse must not reattribute thread content. Shared agent/session/channel retirement is OUT OF SCOPE here, owned by #3735–#3737; no live deployment/migration or hold release.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-22
