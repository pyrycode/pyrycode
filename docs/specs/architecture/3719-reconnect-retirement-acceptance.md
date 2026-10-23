# Reconnect retirement: integrated acceptance

## Files read

- `internal/relay/v2session_recovery_test.go` → `TestV2Session_LastEventIDIgnored`, `TestThreadRecoveryAfterReconnect`: authenticated cursor shapes, connection reuse and explicit scoped recovery.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: token/static-key admission and selection of supported reconciliation.
- `internal/protocol/handshake.go` → `HelloClientPayload`: obsolete cursor is outside the decoded contract.
- `internal/relay/v2session_push.go` → `forwardEnvelopeSource`, `mergedForConn`: delivery gates and explicit model projection provenance.
- `internal/relay/v2session_modelreconcile.go` → `reconcileModelLists`: projected retained lists and the inherited nonblocking stale-comment finding.
- `cmd/pyry/interactive_live_publication.go` → `publishLiveState`: supported fanout preserves durable history IDs without ring IDs.
- `cmd/pyry/relay.go` → `cleanupOnConversationDelete`, `startRelay`, `startRelayV2`: composed surviving cleanup in both relay postures.
- `docs/specs/architecture/3738-retire-event-replay.md`, `3739-remove-event-ring.md`: child contracts, security reviews, revisions and proof selections.
- `docs/knowledge/features/v2-session-manager.md`, its reconnect and model-reconcile topics: explicit recovery differs from current-state reconciliation; projection cannot depend on removed event IDs.
- `docs/knowledge/features/thread-package.md`, `protocol-package.md`, `relay-package.md`, `eventring-package.md`: retained thread/history/schema boundaries and package retirement.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`: current source controls applicability; compilation and skips prove no live acceptance.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → **Update messages**, and `docs/protocol-mobile.md` → **Security model**: scoped epoch/version recovery and existing trust boundaries.
- Issue #3719's archived contract and authoritative accepted stage records/bundles for #3718, #3738 and #3739: obligations, exact revisions, historical failures and pending gates.

## Context

One deliverable records integrated acceptance after publication retirement
(#3718 / PR #3838), replay retirement (#3738 / PR #3839) and storage retirement
(#3739 / PR #3840). A closed split parent alone establishes no delivery.
The inspected integrated base is `1ac43b7e8d9346f77e7dbfac004c3e743a27b1a7`.
The archived original body hashes to
`9db44af7cb86ea951e6bfceb020216a9d7f582a36333a680cc6747c212d55fb5`.
ADR 042 already owns the design; no new decision record is needed.

Scope: approximately 150 written plan/evidence lines, zero production files,
exported types, consumer migrations or new reject branches; three acceptance
criteria. No implementation behaviour is bundled into this evidence review.
Fetched feature branches have no overlap with this plan's path. Adjacent
mandatory-thread migration remains with #3740/#3741/#3743.

## Design

Compare accepted child revisions with this base and confirm that later changes
cannot invalidate reused checks. Read authoritative stage acceptance and
hash-checked bundles rather than treating comments as verdicts. Map every
archived obligation and current criterion to named retained tests and source.
Record OS, exact candidate, selections and executed/passed/failed/skipped counts;
executed excludes skipped events and includes parent/subtest events.

The #3739 candidate already includes both removals. Prefer its integrated
evidence; use #3738 to explain replay-specific proof and #3718 only as ancestor
publication/live evidence. Retain generic optional `Envelope.EventID` and durable
`HistoryEntryID`; neither restores event-ring recovery. Add focused proof only
if the combined behaviour lacks an existing meaningful oracle. Record the
stale `reconcileModelLists` comment explicitly as inherited nonblocking work,
as permitted by the issue, without claiming its correction.

## Concurrency model

No goroutines, locks, queues or cipher ownership change. Review the surviving
Run-owned delivery and composed cancellation/join evidence without adding work.

## State transitions and identity reuse

None: this change records evidence and introduces no lifecycle or identity state.
The evidence map must still name coverage for reconnect/reused connection IDs,
transport-down/rekey and deletion/sweep/recreation of surviving owners.

## Error handling

Missing required proof stays pending with its current owner; a focused pass,
compilation, skipped test or historical failure is never relabelled acceptance.
Preserve approval/operator holds and original failed results. Necessary denied
operations stop the run under the shared working practice.

## Testing strategy

Reuse accepted Linux race, fake-integration, build/static and tagged-compilation
evidence when its production/test inputs remain unchanged. No package is edited,
so no new regression test or duplicate package/full/live run is planned. Validate
the final documentation-only diff with `git diff --check`. The dispatcher/verifier
retains its configured acceptance, including slow/release/live checks; this
review does not declare a current live pass or order a new paired capture.

## Open questions

None about the product contract. Final evidence mapping must document any
applicability gap and disposition before handoff.

## Documentation handoff

Completed in the documentation stage; see [final disposition](#documentation-stage-disposition). Reconciled child handoffs:

- `docs/protocol-mobile.md` → **hello (v2-specific note)**, **Reconnect replay & resync**, **Reconnect / Backfill semantics**, **Compatibility**, hello fields and reconnect/error recovery: ignore obsolete cursors; explicitly catch up each open conversation by completed epoch/version, fetch older pages and recover the newest window after `thread_reset`; preserve prompt/live-state reconciliation. Remove remaining production ring claims in **Application message types**' `mcp_status` row, **mcp_status**, and envelope metadata wording.
- `docs/knowledge/features/eventring-package.md` → **Where the durable id is assigned**, **Ownership & wiring**, **Files**: retain retirement wording, durable source-history/`HistoryEntryID` distinctions and no ring-derived IDs. `docs/knowledge/features/conversations-registry-crud.md` → **SetOnDelete**: composed thread, stream/live, suggestion and pending-inference cleanup in both relay postures, without ring Drop.
- Reconcile affected links/wording in `docs/knowledge/features/thread-package.md` → **Shadow lifecycle and evidence**, its linked lifecycle/evidence topics, `docs/knowledge/features/protocol-package-types-envelope.md`, `docs/knowledge/features/v2-session-manager.md` and `v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md` → **Content recovery**/**Current-state reconciliation and delivery**. Preserve generic envelope metadata and source-history distinctions.
- Reuse `docs/knowledge/features/v2-session-manager-state-machine-connect-time-turn-phase-reconcile-running.md` and `v2-session-manager-state-machine-inline-reply-seals-gated-behind-transportdow.md` handoffs: remove retired watermark/pump ordering claims while preserving publish-before-fanout and transport-down nonce safety. Preserve model projection provenance in `v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md` → **Projection provenance**. Record final evidence/dispositions without claiming pending gates passed.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Treat issue text and downloaded logs as untrusted evidence. Authoritative accepted stage records plus candidate ancestry and bundle hashes determine reuse; `handleNoiseInit` still validates token/static-key binding before app admission.
- [Tokens/secrets] No credential access or changes. Record only revision, test and verdict metadata; never copy environment, tokens or decrypted production payloads into the plan/PR.
- [File operations] Only this architecture plan is written in the worktree; evidence downloads stay in `/tmp/builder-3719`. No history migration, credential storage, permissions or path validation changes.
- [Subprocesses] No application subprocess or environment changes; existing child test results are reviewed without launching live Claude.
- [Cryptography] Reused nonce/rekey tests must apply to unchanged delivery inputs. `Run` remains sole cipher owner; ignored cursor data cannot authorize replay or suppress thread output.
- [Network/I/O] No endpoint/frame-cap changes. Retain evidence for requested conversation/host scope, bounded ordered queues, `livePending` and hosted admission; absence of replay alone does not prove those properties.
- [Errors/logs] Record failures separately from accepted reruns; no new runtime errors or logs. Snapshot refusals remain generic and source-history failures still omit `HistoryEntryID`.
- [Concurrency] No shared state changes. Retain direct-delete/sweep/recreation, joined thread removal and pending-inference cancellation evidence in both relay postures.
- [Threat model] Existing Noise confidentiality/authentication, nonce safety and host isolation remain binding. Mandatory-thread admission is OUT OF SCOPE under #3740/#3741/#3743; deployment, live-data migration and releasing existing holds are outside this ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-23

## Evidence review — 2026-10-23

Reviewed integrated base `1ac43b7e8d9346f77e7dbfac004c3e743a27b1a7` and its
documentation-only descendant on `feature/3719`. PR #3838 merged at `ecc719bf`,
PR #3839 at `9e1882b1`, and PR #3840 at the integrated base. All three causal
dependencies are closed and their accepted sources are ancestors. No new
integration correction or uncovered combined behaviour was found.

Accepted stage sources:

- **R38:** #3738 verifier run `f2db2a930e80c154dae1ad88281a0e81fa0c7ab776763f83c6b39154e7ab0ff6`, candidate `ce0f0169ae4b6aa18d642d6f931ecc2d604439e3`, PASS without findings; bundle digest `7837dbf70006efdd2e911f46101959568e3b5ef2442863fb6cda1fd1234d1c78`.
- **R39:** #3739 verifier run `f26e1e0f1e599c9285b9d1c1081cd7dc26aaa0ba46a1ee803b0da913ca49f86e`, candidate `52fdaeca7a4d664cbe3c05c75f961995e368f694`, PASS with the inherited comment finding; bundle digest `0dde91191674cacca738b4cd2530be4a9f03befed74b8b93034c9c7bfff28c64`.
- **L18:** #3718 accepted live run `a601bc69c25946658ebd6237376d4340b3dbcaf27dea31051f3c24e889a46ba5`, candidate `74387269216c2ce891c4436e8956c33218d6d456`. Both required reader consumers passed: `TestInteractiveStreamMultiTurnContinuity` and `TestInteractiveConversationSystemPrompt_ReachesTheReply`. This is ancestor publication evidence, not current #3719 live acceptance.

All downloaded bundle checksums match; full unit/fake logs also match each
bundle's retained-pointer SHA-256. L18's full retained log hash matches too.
Counts were independently read from those full logs, not their truncated tails.
R39 already contains both removals and the projection repair. Its entire diff to
the integrated base contains only knowledge documentation; this ticket changes
only this plan. Production, tests, dependencies and build inputs are identical,
so R39's combined-behaviour proof remains applicable. R38 explains the earlier
retirement; R39 supersedes its old storage fixtures. This applicability review
does not bypass the dispatcher's separate exact-candidate gate/retention rules.

| Obligation from original archive/current criteria | Integrated source and retained proof |
| --- | --- |
| Ignore absent/zero/stale/max/string/object/array/null cursor; no replay/resync or later muting | `HelloClientPayload` lacks the field; `handleNoiseInit` ignores unknown JSON keys. R39 `TestV2Session_LastEventIDIgnored` covers both capability postures, same/other conversation updates, another connection and fresh-cipher reuse of a closed connection ID. |
| Explicit per-open-conversation catch-up, pages, reset and final completion | R39 `TestThreadRecoveryAfterReconnect`, `TestThreadCatchUpRequestedScope`, `TestThreadPageRequestedScope`, `TestThreadCatchUpAdmission`, `TestThreadPageAdmission`: no implicit provider call on hello; unusable checkpoint resets; explicit newest window/page returns scoped content and final boundary. |
| Remove pump/watermarks/guards, raw resync, setter, storage/wiring/config and exclusive assertions/helpers | Merged #3738 deletes `v2session_replay.go` and replay-only tests while moving snapshot handling; #3739 deletes `internal/eventring` and ring-only fixtures. Removed-symbol/source inventory finds no surviving production replay consumer or raw resync assertion. Remaining legacy cursor injection is test-only; generic `Envelope.EventID` remains intentionally. |
| Durable source history and `HistoryEntryID`, including zero recipients and nil/failed storage | Ring-free `publishLiveState`; R39 `TestLiveProducers_HistoryEntryID` and retained daemon history/provenance tests compare durable records with actual fanout and retain envelope ordering. |
| Outstanding prompts/current live truth and snapshot refusals | R39 modal/question/model/slash/turn-phase reconciliation families, `TestLiveStateConnect`, `TestLiveStateGates`, `TestV2Session_OpenState_RequestSnapshot`; no obsolete ring tail gates supported state. |
| Conversation/host isolation and bounded ordered delivery | R39 requested-scope/admission tests, `TestHostedLifecycleHostIsolation`, `TestLiveStateOrdering`, `TestHostedPushCapacityPendingFence`, `TestHostedPushCapacityTeardown`, `TestV2Session_Push_ByteCeilingTearsDownSession`. |
| Transport-down nonce safety, reconnect flushing, rekey and teardown | R39 `TestV2Session_InlineReply_TransportDown_BurnsNoNonce`, `TestV2Session_Push_ReconnectWhileDownDoesNotSeal`, `TestV2Session_Push_FlushesOnReconnectSignal`, `TestV2Session_RekeyInitiator_Emit_ReArmViaResponder`, hosted teardown and session-sweep/reconnect checks. |
| Surviving deletion observers with/without relay; sweep/cancellation/recreation | `cleanupOnConversationDelete` composes surviving owners in both wiring paths. R39 `TestConversationDeleteCleanup`, `TestConversationDeleteCleanup_NilRegistry`, `TestThreadShadowRemoval`, `TestReplyFallbackRegistryRemovalCancellation`, `TestDaemonLiveConversationRemoval`, `TestDaemonLiveNoRelayRemoval`. |
| ID-free canonical model pushes project once; retained readings/correlated replies do not merge twice | `forwardEnvelopeSource`, `mergedForConn`, `reconcileModelLists` carry explicit projection provenance. R39 `TestV2Session_PushedModelList_MergedForMultiAgentConn`, `TestV2Session_ModelListReconcile_PassesTheConnsMultiAgentDecision`, `TestLiveStateReplies` and daemon catalog/reconnect regressions. |

Linux x86_64 retained counts below are executed/passed/failed/skipped. Offline
rows count parent/subtest terminal events; L18 explicitly counts leaves.

| Selection | Executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| R38 `go test -timeout=30m -race -json ./...` through admission | 30690 | 30690 | 0 | 27 |
| R38 `check-restoration.py` separate required proof | 202 | 202 | 0 | 0 |
| R38 `go test -timeout=30m -tags e2e -race -count=1 -json ./internal/e2e/...` through admission | 2174 | 2174 | 0 | 2 |
| R39 same admitted unit/race selection | 30641 | 30641 | 0 | 27 |
| R39 same admitted fake-integration selection | 2174 | 2174 | 0 | 2 |
| L18 configured live selection, leaf counts | 2264 | 2264 | 0 | 27 |

R39's cite/docs guards, vet, 30 helper tests, staticcheck, substrate guard and
build passed. R39's restoration selector required no repeat; R38's exhaustive
proof remains separate evidence. Child tagged checks for `e2e`, `e2e_realclaude`
and `e2e_liverelay` compiled successfully and executed zero live tests. No tests,
native task, full gate, paired capture or live suite was launched by this review.

Dispositions: preserve R38's original relay fixture failure (1834 RUN / 1832
passed / 2 failed / 0 skipped), its repaired 2/2 selection, R39's controlled-red
and fixture-build attempts, and #3718's original PR live-selection FAIL separately
from their accepted reruns. No historical failure or hold is erased. The
`reconcileModelLists` ring/dedup comment remains an explicit inherited nonblocking
SHOULD FIX from R39; no functional defect or correction is claimed here.

Pending at builder handoff (superseded by the final disposition below): this PR's configured verifier/dispatcher acceptance; required slow checks
including `TestDaemonThreadMetadataReadiness` and
`TestPushWakeFeedbackExpiryIntegration`; other skipped opt-in/environment checks
under their existing owners; configured release/live acceptance if selected;
and the documentation handoff above. No skip is a pass. macOS is outside this
Linux ticket's verification scope. No deployment, migration or hold release.

## Revisions

- 2026-10-23: Evidence inspection distinguished the live gate's leaf tally (2264 passed) from its raw terminal-event tally (2544 passed, including parents). Report its accepted leaf counts separately from the offline parent/subtest counts; both have zero failures and 27 skips. This changes evidence units, not product scope or acceptance requirements.

## Documentation-stage disposition

The [final verifier review](https://github.com/pyrycode/pyrycode/pull/3841#issuecomment-6358552426)
returned PASS on Linux x86_64 for `630a921ad9a36e9c5d671a96bfba3d52035d192e`.
All ten configured deterministic gate steps passed; the restoration dependency
selector required no repeat. The supplied dispatcher report records unit/race
counts of **28029 executed / 28029 passed / 0 failed / 27 skipped**, and fake
integration **1971 / 1971 / 0 / 2**. The verifier separately reports raw Go JSON
terminal-event counts including parents of **30641 / 30641 / 0 / 27** and
**2174 / 2174 / 0 / 2**, plus **30 / 30 / 0 / 0** helper tests. Documentation
inspection reproduced those raw terminal-event totals from the retained full
logs. These are separate reported tallies, not additional test executions; no
conversion or equality with the dispatcher's summary tally is asserted. The
[execution guidance](../../knowledge/features/development-verification-test-execution-and-artifact-survival.md)
explains why counting units must accompany evidence.

All documentation handoffs are satisfied: `docs/protocol-mobile.md` reconciles
hello, envelope metadata, reconnect/backfill, compatibility, error recovery and
MCP status without production ring claims. The eventring retirement, registry
`SetOnDelete`, protocol envelope, thread overview/lifecycle/evidence, v2 session
reconnect, turn-phase, inline-reply and model-projection topics were checked
against retained code/tests; already-correct child wording remains. Durable
source history, generic envelope metadata, projection provenance,
publish-before-fan-out ordering and pre-seal transport-down guards are preserved.

The inherited `reconcileModelLists` ring/dedup comment remains nonblocking SHOULD
FIX; this documentation stage changes no production code or tests. Earlier
failed attempts above remain failed. `TestDaemonThreadMetadataReadiness` and
`TestPushWakeFeedbackExpiryIntegration` are still skipped, confirmed in the
current unit log, and pending with dispatcher-owned slow checks. Other opt-in
skips and any configured release/live acceptance retain their owners. Ancestor
L18 evidence is not a current live pass; tagged compilation executes no live
tests. macOS is outside this Linux ticket's verification scope. No deployment,
migration or operator/approval hold release is authorized or claimed.


Documentation validation on Linux: `make docs-guard` and `git diff --check`
passed. No new document or deletion required a catalog/index change. No product
test, release/live check or QMD index refresh was launched by documentation.
