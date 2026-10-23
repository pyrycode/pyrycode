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

- 2026-10-23 (verifier rework, finding 1): PR #3838's `Live tests` section must contain only `TestInteractiveStreamMultiTurnContinuity` and `TestInteractiveConversationSystemPrompt_ReachesTheReply`, one name per line. Move the pending-evidence explanation under `Live evidence status` so the dispatcher recognizes both required tests. Validate the published body after editing; this routing-only repair changes no code or evidence disposition and requires no repeated Go checks. The existing security review remains applicable: no runtime, trust-boundary or credential changes.

- 2026-10-23 (prospective): The retained verifier finding explicitly asks for the live reader. Supplement the daemon broadcaster assertion with `TestRelayV2_AgentContentLifecycleNeutral` in `internal/e2e/agent_content_lifecycle_test.go`: observe an actual encrypted main running/idle sequence, release a second fake-Claude fragment containing only roster/task/child traffic, require its retained sources and actual task-progress envelope, and drain through a correlated ordered reply while rejecting any turn-state envelope. Existing two-fragment replay provides the release barrier and owns child shutdown; no new helper or production changes. This reads `writeTaskRosterRows` and the fake-Claude replay path in `stream_json.go`. Forecast remains under 500 written lines, zero exports or production call-site changes. Security self-review PASS: isolated mode-0600 fragments, bounded existing encrypted reader/deadline, no credential/logging/crypto changes. The precise system-prompt consumer name is `TestInteractiveConversationSystemPrompt_ReachesTheReply`; its live proof remains dispatcher-owned.

## Requirement-to-evidence map

Archive: [original contract](https://github.com/pyrycode/pyrycode/issues/3718#issuecomment-6323312178), digest above. Integrated production revision is `1b879fd10f9c3a419f3970485fe2ec78b3cb3ec0`; this branch changes only acceptance tests/helper and this record. Every count below is Linux evidence; macOS is outside scope. E/P/F/S means executed/passed/failed/skipped, excluding skips from executed unless explicitly labelled RUN events. Authoritative child stage records were read using `fleet-task.py stages`, with retained verifier/live bundles downloaded using `fleet-task.py evidence` into `/tmp/builder-3718/child-<ticket>`.

| Child / merged PR / main merge | Accepted live candidate | Live E/P/F/S (leaf tests) | Accepted live stage run ID |
| --- | --- | --- | --- |
| #3734 / [#3829](https://github.com/pyrycode/pyrycode/pull/3829) / `ac0a13f59ebe` | `4ba56296c0737b4ca25ae874278fab7ffc2e2598` | 2258/2258/0/27 | `7e6286d01798e551902534a01ccd5a46d6ba5bd394e8a44daed9da17ebc6af4b` |
| #3735 / [#3830](https://github.com/pyrycode/pyrycode/pull/3830) / `915a2bcbfe8c` | `ad1cb1aec21452edd41c01ddc76a1c39e566dbcd` | 2267/2267/0/27 | `422989d23273fdae5150c52c59a7653856b22f2045837a65929d8f9fbe102165` |
| #3736 / [#3831](https://github.com/pyrycode/pyrycode/pull/3831) / `287ecbd15727` | `9ebba3462074f1537741a08c06151eae2343693b` | 2268/2268/0/27 | `a986e1249a3674c5f21d012a7761fb696edd1230848b926c03739d950d176b8e` |
| #3737 / [#3832](https://github.com/pyrycode/pyrycode/pull/3832) / `e82a33698d05` | `895f4ff0c9bf84fe5723dacfbedff9f253a9d92d` | original 2250/2249/1/27; named same-tree rerun 1/1/0/0 | `6695439568ef939d61191ec80543f212f5f06376c72a45729f78f3010fff1c65` |

These configured child live runs selected `go test -tags e2e_realclaude -timeout 30m -json ./internal/e2e/realclaude/...`. #3737's accepted disposition is flaky pass for `TestInteractiveStreamForwardsSubagentText`; the original remains failed. The 27 skipped probes include opt-in captures, unconfigured Codex captures and API-key MCP tests; none proves its skipped behavior.

| Archived requirement / retained finding | Applicable integrated evidence and disposition |
| --- | --- |
| Raw assistant/tool/turn-end retirement for every app | `TestInteractiveContentRetired` and `TestInteractiveContentRetiredStorage`: main/child sources, canonical revisions/catch-up, nil/failed storage, no clients and no raw replay. `emit` appends before its content guard. Source types are retained intentionally. |
| Raw agent/roster retirement, neutral source identities | `TestAgentContentRetired`, agent-history/session-ending families and `TestRelayV2_AgentContentRetiredReconnect`: late links, outcomes, empty/incomplete rosters, saved endings, reuse, both capabilities and explicit authenticated catch-up. `recordAgentFact`/`closeAgentHistory` append only. |
| Session/notice content, rotation and busy lifecycle | `TestSessionContentRetired`, storage controls, transition/switch/runtime-boundary families: dividers, refusals, compaction boundaries, unrecognized notices, recorded provenance, switch callback and predecessor sealing. `broadcast`/`recordRuntimeFact` retain sources without publication. |
| Banners, answers, channel posts and exclusive adapters | `TestResidualContentRetired`, storage controls, `TestPromptAnswerPublicationRetired`, `TestPromptAnswerRelayRemainsResponsive`, `TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer`, `TestChannelPost_RecordingReplayLiveWakeOrder`: answer ownership/exactly-once dismissal, no discovery wait, durable post completion before wake/recovery. Integrated symbol inventory finds no `publishLegacyHistory`, `legacyRuntimeReceipt`, `promptAnswerPublisher` or channel content publisher. `historyVisibilityMetadata`/`historyEntryShown` remain consumed. |
| Initial/live/query content only via threads, ordered delivery, isolation | All four retirement families compare applied canonical revisions with catch-up under reused conversation/session identities; reconnect uses an encrypted ordered reply. Existing thread page/query, publisher and live-state ordering/clear/isolation families retain their configured gate coverage. No automatic raw content is restored for non-thread apps. Complete integrated fake-daemon/race gates remain pending dispatcher; focused coverage alone is insufficient. |
| Prompts/actions/progress/retries/settings/catalogs remain supported | Existing mixed scenario/permission/interrupt/queue/reset readers retain source joins separately from actual live frames. Child configured gates include addressed task stops, composer stop preservation, modal allow/deny/question/refusal, hook banners and session control/liveness. Skipped API/MCP probes are not reused as acceptance. Final integrated live gates remain pending dispatcher. |
| Delete positive compatibility fixtures/helpers, preserve supported assertions | Child diffs remove exclusive receipt/post/prompt publishers and raw-positive expectations, including the old roster reconcile test; mixed tests keep busy/rotation/agent/liveness controls via source/thread observation. No disabled raw publisher suite is added. Surviving ring/replay wiring is explicitly downstream #3738/#3739. |
| #3734 roster lifecycle SHOULD FIX | New encrypted `TestRelayV2_AgentContentLifecycleNeutral` counts actual `turn_state` envelopes after a witnessed main idle, while checking retained child/roster/outcome sources and actual progress. `TestAgentContentRetired` adds both-capability emitted-phase witnesses beside internal/canonical assertions. Focused execution recorded below; no source callback stands in for a wire frame. |
| #3734 completion identity SHOULD FIX | `TestCompletedSourceTextIdentity` separates reused sessions, reused kind/session labels, later child completion and trailing uncompleted text; `TestCompletedSourceText` retains untagged compatibility of the reader. `drainForCompletedSourceText` still requires actual running then idle. Continuity and system-prompt counted live acceptance remains pending on the changed reader. |

Reuse is limited: the four child implementations are merged ancestors, and later children remove additional publication paths while preserving their source/fold contracts. #3834, merged at this starting revision, repairs fake-daemon session-resume test observation rather than the production retirement boundary. Thus child evidence remains applicable historical evidence for those contracts, supplemented by current combined controls. It is not a same-tree full acceptance result for this branch, and the changed live reader requires current counted live evidence. Child closure after splitting, tagged compilation, skips and focused passes cannot establish family completion or release any operator/approval hold.

Historical failures remain separate: #3734's live `38b93361` run failed both modal-resolution arms, including the same-tree rerun (2/0/2/0 leaves); the reconnect repair later passed focused live 3/3/0/0 leaves before the accepted full 2258-pass run. #3735's four original replay-equality families failed before 70/70 focused repair events and its accepted configured gates. #3736's original builder package run was 16887/16881/6/4; later metadata/switch controls passed 43/43 events. Staticcheck U1000 at `81831fd4` remained failed before the unused helper removal and all-green gates at `9ebba346`. #3737's original fake gate RUN events were 2167/2163/2/2 (parent plus `TestRecordedClaude/session-resume` failure), then accepted named-family rerun 71/71/0/0; tracking #3833 split to #3834/#3835. Its live failure/rerun remains separately counted above. Retained PRs, child plans/revisions and stage bundles preserve earlier failed attempts; no accepted inherited flake is a dependency here.

Accepted child offline configured counts, in parent/subtest events: #3734 unit 30770 passed/0 failed/27 skipped and fake 2164 passed/0 failed/2 skipped; #3735 unit E/P/F/S 30739/30739/0/27 and fake 2165/2165/0/2; #3736 unit 30743/30743/0/27 and fake 2165/2165/0/2; #3737 unit 30696 passed/0 failed/27 skipped, fake original/rerun above. These differing count units are deliberately not summed with live leaf counts. Restoration selectors reported exhaustive coverage not required on the child inputs; integrated selection remains owned by the dispatcher.

The preserved complete #3734 original log `2026-10-22T16-33-59-621Z_real-claude-gate_#3734.log` was also read from the path in its retained bundle: 2257/2255/2/27 leaves. This is the original full failure, separate from its failed 2/0/2/0 named rerun and later accepted candidate. Its retained bundle run ID is `294c5df79d7a90701ed4d42af6f11dcd10fa1d998ea543c1ac3bf69621adeffd`.

## Integrated builder results

Tested code/test revision: `2882b0f10dfcb9cb02a6a433eda1aa3400c2f063`, on Linux x86_64. Subsequent evidence-record edits change only this plan. Prospective spec commits `7424d301` and `83b4caa5` precede implementation. Total written work remains below 400 lines, with no production edits, exports or production caller migration.

All focused tests used `python3 scripts/test-admission.py -- go test -timeout=30m -race -json`; tags, selectors and packages follow. Counts are completed parent/subtest events; leaf counts are provided separately. No test was left unfinished.

| Selection | E/P/F/S events | E/P/F/S leaves | Retained log under `/tmp/builder-3718/` |
| --- | --- | --- | --- |
| Before correction: `-tags e2e_realclaude -run '^TestCompletedSourceText' ./internal/e2e/realclaude` | 6/1/5/0 | 5/1/4/0 | `source-before.json` |
| Corrected: `-tags e2e_realclaude -run '^(TestCompletedSourceText\|TestCompletedSourceTextIdentity)$' ./internal/e2e/realclaude` | 6/6/0/0 | 5/5/0/0 | `source-after.json` |
| Retirement/answer selection in `./cmd/pyry` below | 27/27/0/0 | 23/23/0/0 | `retirement.json` |
| `-tags e2e -run '^TestRelayV2_AgentContentRetiredReconnect$' ./internal/e2e` | 1/1/0/0 | 1/1/0/0 | `reconnect.json` |
| `-tags e2e -run '^TestRelayV2_AgentContentLifecycleNeutral$' ./internal/e2e` | 1/1/0/0 | 1/1/0/0 | `lifecycle.json` |

The retirement regex was `^(TestInteractiveContentRetired|TestInteractiveContentRetiredStorage|TestAgentContentRetired|TestAgentContentRetiredStorage|TestSessionContentRetired|TestSessionContentRetiredStorage|TestResidualContentRetired|TestResidualContentRetiredStorage|TestPromptAnswerPublicationRetired)$`. Eight top-level tests matched; no `TestAgentContentRetiredStorage` test exists, so no execution or acceptance is attributed to that unmatched alternative. The initial unbounded-pattern selection was refused before admission and executed zero tests; the bounded selection above passed.

The controlled failure retains the legacy fixture pass and four failing identity leaves: cross-session and cross-kind text became `foreignonetwo`, the later child completion selected no main reply, and trailing text became `foreignonetwounfinished`. After correction all five leaves pass; this red-to-green proof exercises no Claude credentials or live turns. The encrypted lifecycle test observes nonempty then empty retained rosters, child text and reported outcome, but checks main phase neutrality only on actual decrypted envelopes through a progress/control-reply barrier. The existing broadcaster regression also requires both capability modes' actual emitted main running/idle phases. Neither witness replaces the other.

`go vet ./...`, `go build -o /tmp/builder-3718/pyry ./cmd/pyry`, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...` and `go vet -tags e2e ./internal/e2e/...` passed; final tagged vet logs are `tagged-vet-final.log` and `e2e-vet-final.log`. These are static/build results, with zero live tests claimed. Formatting and `git diff --check` passed.

Disposition: both retained #3734 coverage findings are addressed by focused proof, with current live acceptance still **pending** for `TestInteractiveStreamMultiTurnContinuity` and `TestInteractiveConversationSystemPrompt_ReachesTheReply` on the corrected reader. Full integrated unit/race, fake-daemon integration, configured/static/restoration selection and applicable counted live gates remain **pending dispatcher/verifier**. Documentation remains **pending documentation stage** at the exact paths/sections above. No extra paired capture, deployment, second-OS result, replay retirement or operator/approval hold release is claimed.
