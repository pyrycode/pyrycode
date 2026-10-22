# Retire raw agent and roster publication (#3735)

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `emit`, `handleForSource`: source append precedes publication; task reports do not mutate main-turn state.
- `cmd/pyry/agent_history.go` → `recordAgentFact`, `observeAgentHistory`, `observeGoneRoster`: durable lifetime/link/outcome facts must survive publication removal.
- `cmd/pyry/agent_session_endings.go` → `closeAgentHistory`, `endAgentWork`: retain idempotent source endings and original observations.
- `cmd/pyry/relay.go`, `cmd/pyry/main.go` → `startRelayV2`, `runSupervisor`: connect-time roster composition.
- `cmd/pyry/session_background_task_list.go` → `retainedBackgroundTaskRosters`, `resolveBoundBackgroundTaskRoster`: remove publication-only resolution/enumeration; move retained join assertions to the surviving runner/hold boundary.
- `internal/relay/v2session_handshake.go`, `internal/relay/v2session_seams.go`, `internal/relay/v2session_rosterreconcile.go` → `handleNoiseInit`, `V2SessionConfig`, `reconcileBackgroundTaskRosters`: retire connect-time roster sending and its exclusive configuration.
- `cmd/pyry/interactive_content_retired_test.go` → `TestInteractiveContentRetired`, `testRetiredThreadUpdate`: reuse real Store publication/catch-up comparison, checking revisions rather than counting frames as rows.
- `cmd/pyry/agent_history_test.go`, `cmd/pyry/agent_session_endings_test.go`, `cmd/pyry/interactive_turn_v2_task_test.go`: retain facts/lifecycle/security assertions; remove obsolete positive delivery/receipt assertions.
- `internal/e2e/relay_v2_stream_background_task_frames_test.go`, `internal/e2e/relay_v2_stop_background_task_test.go`: retain parser/source/progress and addressed task-stop coverage.
- `internal/e2e/realclaude/stop_background_task_completion_test.go` → `TestRealClaudeStopBackgroundTaskCompletion`: retain physical FIFO hold and exact task identity; canonical termination must have actual saved outcome/gone evidence.
- `docs/knowledge/features/thread-package.md`, `thread-package-agents-and-background-work.md`, `thread-package-shadow-lifecycle-and-evidence.md`: task links distinguish launch results from finals; main end and roster absence alone cannot replace saved endings.
- `docs/knowledge/features/streamsup-package.md`, `development-verification.md`, `CODING-STYLE.md`: source consumers, counted focused evidence and symbol citations.
- Parent #3718, its archived contract, and `docs/knowledge/decisions/042-daemon-built-thread.md`: source facts remain; content publication is retired for all apps.

## Context

One observable behaviour: agent/task/roster content and its synthetic receipt banners reach apps only as authoritative thread items. Live publication, reconnect roster enumeration and receipt fan-out are paths of the same retirement. Durable recording and supported task progress/actions remain. Other banners remain owned by #3736. No new decision record is needed; this implements ADR 042 cleanup.

The sketch and final plan forecast about 950 added/modified lines including tests and this plan, with substantial obsolete code/test deletions, zero new exported types, at most ten simultaneous consumers for any removed symbol (`retainedBackgroundTaskRosters` has ten indexed callers), four current refined criteria and no new reject branches. The line ceiling is exceeded; actual parentage #3714 → #3718 → #3735 invokes the split-depth continue-building rule. Existing `needs-human:sizing` remains recorded. A possible live-path/connect-path split would duplicate the same removal's verification; no independently useful smaller behaviour is proposed. Constructor compatibility is retained. Remote feature #3240 touches `internal/relay/v2session.go`, outside the planned edits; no causal dependency was found. Sibling #3736 owns other session/content notices; #3737 owns shared unused adapters and channel publication.

## Design

Keep `emit`'s original history append, payload, timestamp and source provenance. Return after appending `background_task_started`, `background_task_updated` and `background_task_roster`, before receipt projection, replay or fan-out, for every capability/storage mode. Keep `background_task_progress` and other supported live state on their existing gated path.

Remove only receipt creation/publication from `recordAgentFact` and `closeAgentHistory`; keep their append, ordering, lifetime and ending logic. Remove roster reconcile, its handshake call/configuration and daemon resolution/enumeration/wiring. Keep runner roster retention and task-stop routing. Replace positive compatibility tests with source and canonical assertions; do not create synthetic legacy delivery to preserve obsolete suites. Replace exclusively raw-roster connect tests with retirement coverage and preserve shared helpers. Migrate the live held-task proof to retained source identification and canonical terminal evidence, without releasing the FIFO early or inferring completion from stop acceptance.

## Concurrency model

No new goroutines, locks or ownership changes. The existing stream drain serializes emitter state. Store workers retain cancellation and joined shutdown; relay owns authenticated connections and ordered delivery. Test fixtures join Store workers before cancelling their load context.

## State transitions and identity reuse

| Event | Race test proof |
| --- | --- |
| Main turn ends while a linked child continues; later task outcome | `TestAgentContentRetired`: canonical agent remains active until actual outcome; attributed child text survives. |
| Complete refreshed roster omits an active linked task | `TestAgentHistory_GoneRosters`: saved gone facts still distinguish incomplete/invalid rosters and repeated refreshes. |
| Repeated closure, idle sleep/reactivation and reused source identifiers | `TestAgentSessionEndings_Runtime`, `TestAgentHistory_Isolation`: existing lifetime/ending isolation remains; focused selection includes the agent test families. |
| Task/call IDs reused across conversations and producing sessions | `TestAgentContentRetired`: separate attributed canonical rows and lifetimes; no raw content or receipt frames. |
| Fresh connection/reconnect after roster retention | `TestRelayV2_AgentContentRetiredReconnect`: handshake completes and an ordered control barrier arrives without roster content; explicit authenticated catch-up completes. |

## Error handling

Nil/failed history does not restore raw task content or receipt banners. Preserve existing content-free append diagnostics and supported progress delivery. No fallback projection or new external error is introduced. Recording without connected clients still works.

## Testing strategy

Write retirement regressions first and observe failure before production edits. Reuse Store live-update application/catch-up equality for mixed main/child lifecycle and attribution. Cover nil/failed storage, absent clients, banners and task progress. Retain addressed task-stop and source retention tests. Run focused Linux race tests for affected Go packages through admission, `go vet ./...`, `go build -o /tmp/builder-3735/pyry ./cmd/pyry`, and tagged e2e/live-package compilation after test deletion/migration. Record exact candidate and executed/passed/failed/skipped counts in the PR. Complete configured acceptance and `needs-real-claude` live proof remain pending with the dispatcher; no live deployment or migration.

## Open questions

None. Shared obsolete receipt/projection adapters are owned by #3737, so their remaining validation-only tests are removed here only where they exclusively assert this retired publication.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md` → **Application message types**, **Daemon thread updates (v2, supplied delivery contract)**, **background_task_started**, **background_task_updated**, **background_task_roster**, **Reconnect replay & resync (consumer, #647)** and **Stop background task (v2)**: “Background-agent lifecycle and roster values remain source history for canonical thread items and explicit catch-up. No app receives their raw content frames, agent receipt banners or connect-time roster snapshots. Task progress and stop controls remain supported; stop acceptance alone does not establish completion.” Remove obsolete publication examples while retaining source-schema descriptions needed by the fold.
- `docs/knowledge/features/thread-package.md` → **Shadow lifecycle and evidence**, and its linked `thread-package-shadow-lifecycle-and-evidence.md`, same heading: distinguish retained mapped/neutral agent evidence from removed compatibility publication; describe focused proof and separately pending/counted real-Claude acceptance.
- `docs/knowledge/features/history-package-producers.md` → **Producers (#2114, #2115)**, and `history-package-producers-runtime-lifecycle.md` → **Publication ordering**, **Legacy exclusion and write failures**, **Keep launch results separate from task outcomes** and **Gone requires a later complete roster (#3031)**: agent facts and source attribution still feed the thread fold, including gone/session-ending inference, but these producers publish no compatibility receipts. Preserve surviving sibling producer contracts.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `observeAgentHistory` continues accepting only Claude source facts with existing identity/truncation checks. `emit` retires publication independently of client capability and append success; no untrusted text becomes authority.
- [Tokens] No credentials are created, read or changed. Removed roster/receipt sends reduce exposure; history and thread access gates remain intact.
- [File operations] Existing `appendConversationHistory` remains the sole writer on the changed paths, with existing store paths/permissions. No new path construction or persistence format.
- [Subprocesses] Task-stop routing is retained without command construction or environment changes. Text in task descriptions remains inert.
- [Cryptography] No key, nonce or cipher changes; authenticated thread/control output stays on existing relay delivery.
- [Network and I/O] Remove handshake roster enqueueing; preserve ordered control/thread delivery and existing caps/gates. Regression uses an ordered barrier so absence is not inferred from a sleep.
- [Errors/logs] SHOULD FIX: retain content-free failure assertions for nil/failed source storage and verify payload/path sentinels never reach logs while removing receipts.
- [Concurrency] Existing serialized emitter ownership and Store shutdown remain. No lock-order or goroutine additions.
- [Threat model] Preserve `docs/protocol-mobile.md` security-model host/device isolation and authenticated task control; no deployment or data migration. Other session/notice retirement belongs to #3736, adapter/channel cleanup to #3737.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-22

## Revisions

- 2026-10-22: `SourceObserver` must expose retained start/update/roster entries to the existing fake/live fixtures while rejecting their raw transport envelopes. `TestBackgroundTaskJoin_CaptureOrders` now reads the surviving hold directly after removal of publication-only resolution. The composer-stop fixture also consumes task completion as source evidence. The held-task stop proof negotiates thread, checks a canonical terminal item against its exact post-stop saved outcome/gone fact and retains the physical FIFO hold. These are fixture migrations required by the planned retirement.
- 2026-10-22: Agent receipt assertions correlate banner history identities with agent source types. Other runtime producers still emit banners until #3736, so a blanket banner prohibition would test sibling scope. The original failing run remains recorded; a controlled old-publisher overlay verifies the refined agent-specific regression.
- 2026-10-22: The affected daemon package run found residual raw-task replay expectations in `assertLogMatchesRing` and `TestHistoryProjection_InteractiveMetadata`, also used by child completion/captured reply tests. Extend the existing retired-source exclusion and zero-publication expectation to the three task content types, retaining all source metadata and canonical completion assertions. Re-run the four named failing families; preserve the original package result.
