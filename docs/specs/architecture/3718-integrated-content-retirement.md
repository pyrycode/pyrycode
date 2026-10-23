# Integrated content-publication retirement acceptance

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `emit`: append precedes retirement guard and supported live publication.
- `cmd/pyry/agent_history.go`, `agent_session_endings.go`, `runtime_history.go` → `recordAgentFact`, `closeAgentHistory`, `recordRuntimeFact`: retained neutral sources without receipts.
- `cmd/pyry/session_transition_v2.go` → `broadcast`: retained dividers/transitions precede switch callback.
- `cmd/pyry/agent_content_retired_test.go` → `TestAgentContentRetired`: canonical lifecycle proof currently checks internal main state only.
- `cmd/pyry/interactive_content_retired_test.go` → `TestInteractiveContentRetired`: canonical revisions, catch-up and scoped identities.
- `internal/e2e/relay_v2_agent_content_retired_test.go` → `TestRelayV2_AgentContentRetiredReconnect`: encrypted reconnect barrier and explicit catch-up.
- `internal/e2e/realclaude/interactive_stream_multiturn_continuity_test.go` → `completedSourceText`, `drainForCompletedSourceText`: two direct helper consumers; live running/idle witness stays required.
- `internal/e2e/realclaude/interactive_conversation_system_prompt_test.go`: second live drain consumer.
- `internal/history/log.go` → `Entry`, `AppendWithMetadata`: producing identity is optional metadata, separate from payload turn IDs.
- `internal/protocol/interactive.go` → `TurnEndPayload`, `AssistantDeltaPayload`: source vocabulary remains; completion selection also decodes an optional parent discriminator.
- `docs/knowledge/features/thread-package.md` and linked retirement evidence: accepted child gates do not establish parent acceptance; preserve failed originals separately.
- `docs/knowledge/features/history-package-producers.md`, `e2e-realclaude.md`, `development-verification.md`, `CODING-STYLE.md`, ADR 042: source/publication separation, independent wire witnesses and counted acceptance.

## Context

One acceptance deliverable remains after merged children #3734–#3737. Preserve archived contract SHA-256 `1500d9c108066e72576389f580c06ebd973d676cf1e67fa337b93429ed2f48ee` in issue comment `daemon-aggressive-original-20261021`. This supersedes ADR 042's transitional compatibility publication rule, without changing its source/live-state contract. No new decision record is needed.

Integrated starting revision: `1b879fd10f9c3a419f3970485fe2ec78b3cb3ec0` (Linux), descendant of all four merges. Production repair, replay admission/pump (#3738), shared ring/SetReplaySource (#3739), deployment and approval/operator hold release are outside this ticket.

Sizing before plan commit: approximately 300–400 written lines including this plan/evidence, two test files, zero production files, zero new exports, zero production consumer migrations, four acceptance criteria and no new production reject branches. The helper and regression stay together under the verification floor. Fetched feature branches have no overlap with the two planned test files.

## Design

`completedSourceText` selects the newest retained main-lane completion, ignoring parented completions. Collect chronological main text only from the same turn and identical optional producing provenance (kind and session), at or before that completion. Retain the bounded source window and nonempty reply checks. Controls include legacy untagged facts, repeated turn IDs across sessions/kinds, later child completion and trailing uncompleted text. No live consumer signature changes.

Extend `TestAgentContentRetired` to require actual emitted main running/idle envelopes and reject any main turn-state envelope during child/start/roster/progress/outcome/session-ending traffic, for both capability modes and reused sessions/conversations. These are actual broadcaster envelopes, not source callback projections. Retain internal state and canonical live/catch-up assertions. No raw publication is restored.

Record an evidence map here, linking archived requirements and retained findings to exact child sources, accepted stage IDs, applicable selections and original/rerun counts. Current focused evidence supplements these records; full integrated acceptance remains with dispatcher/verifier.

## Concurrency model

No new goroutine, lock or reader. The helper synchronously reads isolated history after the existing encrypted running/idle drain. The daemon regression uses its existing synchronous broadcaster and Store shutdown cleanup.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Reused turn ID across source sessions/kinds, child completion after main completion | `TestCompletedSourceText` |
| Child/task/roster after main idle; repeated sessions and conversation routing IDs | `TestAgentContentRetired` |
| Real sequential running/idle and continuity/system prompt | Existing `TestInteractiveStreamMultiTurnContinuity` and `TestInteractiveConversationSystemPrompt`, counted live acceptance pending dispatcher |

## Error handling

Malformed source, missing completion, incomplete window and empty selected reply remain fatal test failures. Failed/nil history never re-enables publication; existing storage controls prove that. Any newly discovered production repair goes to separate refinement.

## Testing strategy

Write source-selection controls first and observe their failure, then correct the reader. Add emitted-envelope lifecycle assertions to the existing canonical regression. Run focused Linux race selections for these controls and integrated retirement/storage/answer families, plus the encrypted reconnect test. Compile/vet tagged real-Claude and e2e packages, `go vet ./...` and build to scratch. The dispatcher owns full-module race/integration, restoration and applicable counted live gates; compilation/skips/focused passes do not discharge them. No extra paired capture or full live run.

## Open questions

None: source provenance equality includes both kind and session; nil matches only nil for legacy fixtures. Retained gate applicability and pending integrated results are recorded below, without granting holds or treating inherited accepted flakes as blockers.

## Documentation handoff

Pending documentation stage:
- `docs/protocol-mobile.md`: **Application message types**, **Daemon thread updates (v2, supplied delivery contract)** and affected raw-content sections/examples: retained recording feeds threads; compatibility content publication is removed; supported live controls remain.
- `docs/knowledge/features/thread-package.md`: **Shadow lifecycle and evidence**, and linked `thread-package-shadow-lifecycle-and-evidence-retirement.md`: reconcile child handoffs and final integrated evidence/dispositions without duplicating inventories or claiming #3738/#3739 complete.
- `docs/knowledge/features/history-package-producers.md`: **Producers** and **Resolved remote prompt answers**: distinguish source recording from removed publication and retain live controls.

## Security review

**Verdict:** PASS

- [Trust boundaries] SHOULD FIX: `completedSourceText` accepts another producing session's text when turn IDs repeat. Planned full-provenance join and regression close this evidence defect before handoff.
- [Tokens/secrets] No credential generation/storage/access changes; tests use synthetic content and existing dispatcher-owned live authentication.
- [Files] Existing history validation and isolated test homes remain; no production path, permission or atomicity changes.
- [Subprocesses] No spawn, arguments, environment or shutdown changes. Existing live consumers retain their actual running/idle checks.
- [Cryptography] No primitive, key or nonce changes; no fabricated encrypted frames.
- [Network/I/O] No new endpoint or reader; source window remains 128 records. Retirement tests retain supported live traffic and canonical delivery checks.
- [Errors/logs] No operational payload logging; existing failed-storage controls retain content-free diagnostics.
- [Concurrency] Existing synchronous collection and Store cleanup remain; no new lock order or goroutine lifetime.
- [Threat model] Cross-source evidence contamination is addressed above. Host/session isolation and permission/action controls remain required child/integrated evidence. OUT OF SCOPE: replay retirement owned by #3738/#3739; no approval hold is released.

**Reviewer:** builder (self-review)
**Date:** 2026-10-23

## Revisions

- 2026-10-23 (prospective): The retained verifier finding explicitly asks for the live reader. Supplement the daemon broadcaster assertion with `TestRelayV2_AgentContentLifecycleNeutral` in `internal/e2e/agent_content_lifecycle_test.go`: observe an actual encrypted main running/idle sequence, release a second fake-Claude fragment containing only roster/task/child traffic, require its retained sources and actual task-progress envelope, and drain through a correlated ordered reply while rejecting any turn-state envelope. Existing two-fragment replay provides the release barrier and owns child shutdown; no new helper or production changes. This reads `writeTaskRosterRows` and the fake-Claude replay path in `stream_json.go`. Forecast remains under 500 written lines, zero exports or production call-site changes. Security self-review PASS: isolated mode-0600 fragments, bounded existing encrypted reader/deadline, no credential/logging/crypto changes. The precise system-prompt consumer name is `TestInteractiveConversationSystemPrompt_ReachesTheReply`; its live proof remains dispatcher-owned.
