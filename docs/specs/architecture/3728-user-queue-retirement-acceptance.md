# User-message and queued-receipt retirement acceptance

## Files read

- `cmd/pyry/operator_message_history.go` → `operatorMessageHistory`: safe source append embeds committed acceptance before readers observe delivery.
- `cmd/pyry/queued_send_history.go` → `accepted`, `beforeDelivery`, `written`, `delivered`, `terminal`: binding/readiness and durable outcomes survive retirement.
- `cmd/pyry/queued_message_placement.go` → `bindQueued`, `writeTarget`; `cmd/pyry/stream_turn_drain.go` → `streamTurnSink`: retained placement ordering and cancellation.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyRuntimeFact`, `legacyRuntimeReceipt`: queued facts cannot regain publication through shared adapters.
- `cmd/pyry/operator_publication_retired_test.go` → `TestOperatorPublicationRetired`: stable canonical identity, safe sender/content, isolation and reopened agreement.
- `cmd/pyry/queued_canonical_history_test.go` → completed-turn, startup, delayed-confirmation and storage-failure tests: silent retired transport with retained canonical behavior.
- `cmd/pyry/queued_send_history_test.go` → lifecycle, placement, startup and failure tests: hidden drop, visible loss, readiness and diagnostic privacy.
- `docs/specs/architecture/{3693-queued-attachment-facts,3725-live-accepted-delivery,3727-remove-queue-snapshots,3732-retire-user-publication,3733-retire-queued-receipts}.md`: original design, repairs and historical evidence.
- `docs/knowledge/decisions/042-daemon-built-thread.md`, **Sessions, agents, messages, read marks**: authoritative own/queued identity contract; this ticket's explicit retirement supersedes the earlier compatibility delay.
- `docs/knowledge/features/msgqueue-package.md`, `msgqueue-package-lifecycle.md`, `history-package-producers-legacy-compatibility.md`: successful write alone is not Claude consumption; retain recording seams when deleting transport.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`, `docs/knowledge/INDEX.md`: focused verification and evidence ownership.
- Issue #3728 archive and refined body, native relations, PRs #3696/#3726/#3753/#3826/#3827, authoritative stage records: contract and actual completion rather than closure alone.

## Context

This is one evidence-review deliverable, with no implementation changes. Review the
merged children against archived contract SHA-256
`7b4ca0a8d911bcad9c1ed65ff0ed6d1d122c7dc49dcba5559e5f123dc7192a0e`
at issue comment `6323295893`. Keep every historical failure and hold intact.
No new decision record, deployment, live-data migration or credential access.

Linux base is `3c01097672fff84fe9f63caa5c715fb32ae617f6`, also current origin/main.
#3732 and #3733 have no descendants; all four causal blockers are closed and have
merged implementations. #3718/#3734–#3737 are downstream, not prerequisites.
The intermediate receipt preservation in #3732 is superseded by #3733's retirement.
Retain historical branches/evidence rather than rebuilding their changes.

Sizing rechecked: about 150 written lines in this plan/review record, zero production
files, exported types, simultaneous consumer updates or new reject branches;
three acceptance criteria. Only this unique plan path changes; shared-code branch
overlap cannot require a redesign of this evidence-only change.

## Design

Commit this plan first. Then append an evidence mapping with exact merged/candidate
revisions, accepted run IDs, named assertions, counts and unresolved later-stage
work. Inspect applicability using candidate-to-merge source comparisons, retained
gate manifests and machine/toolchain evidence. Earlier child green does not itself
certify the final parent revision. Do not repeat implementation or widen diagnosis.

## Concurrency model

No runtime changes or new goroutines. Review acceptance readiness, per-reference
serialization, placement dispatch and worker removal against the merged evidence.

## State transitions and identity reuse

No state is introduced; retain these existing race-detector witnesses in the map:

| Existing event | Assertion family |
| --- | --- |
| Accepted → written → delivered; failed source/companion append | `TestOperatorPublicationRetired`, `TestAcceptedDeliveryLiveRefusal`, `TestAcceptedDeliveryAppendFailures` |
| Retry, late callback/echo, interleaved ordinary and Send now | `TestQueuedCanonicalSendNowDelayedConfirmation`, `TestOrdinaryQueuePlacement_FailedWriteRetriesOnce`, `TestOrdinaryQueuePlacement_IdenticalWritesInterleaveWithSendNow` |
| Explicit removal/give-up, refusal and shutdown | `TestQueuedSendHistoryLifecycle` |
| Restart, repeated recovery and queue-ID reuse | `TestQueuedCanonicalStartup`, `TestQueuedSendHistoryStartup`, `TestQueuedSendHistoryLossRetryAndUnbound` |
| Receiving-child/session rotation and unread pipe | `TestOperatorDeliveryProvenance_ReceivingSessionRotation`, `TestQueuedPlacementOwnership`, `TestQueuedPlacementUnreadPipe` |
| Attachment metadata changes and reopened catch-up | `TestQueuedAttachmentWire`, `TestQueuedAttachmentUnavailableAfterValidation` |

## Error handling

Missing evidence remains pending with its owner; no missing or skipped execution
is a pass. A newly discovered implementation omission belongs in its owning child.
Preserve earlier red gates, failed controls and zero-execution attempts separately
from repaired evidence. Clear no operator, privacy, recording or approval hold.

## Testing strategy

Use existing assertions only: a focused `cmd/pyry` race selection for retirement,
canonical lifecycle, delayed confirmation, refusal/append failure and attachments.
Retain JSON/stdout/stderr and executed/passed/failed/skipped counts outside the
worktree, identifying the exact source revision. No new test bodies or production
edits. Validate the final diff with `git diff --check`. No packages change, so
repeating module vet/build/tagged compilation is unnecessary; preserve child
compilation evidence separately from runtime proof. Complete configured gates and
actual live `TestRealClaude_OrdinaryQueuedPlacement` /
`TestRealClaude_SendQueuedNowPlacement` acceptance remain dispatcher-owned.
Other operating systems are outside this Linux ticket's verification scope.

## Open questions

- Settle candidate/environment applicability and exact obligation mapping in the
  review record; final complete/live acceptance remains with the dispatcher.

## Documentation handoff

Pending documentation stage: reconcile already merged wording, preserving correct
child edits rather than repeating them. Carry forward the exact requirements:

- `docs/protocol-mobile.md`, **Application message types** (`send_message`), **Full thread item**, **Queue observation and delivery identity**, **Legacy receipt identity**, **send_queued_now**, and user/receipt examples: “Operator sends are observed through canonical thread items and explicit catch-up; raw user-message pushes and queued acceptance, delivery, drop and loss receipts are retired for every app.” Explain acceptance identity, unordered queued/written items, actual-delivery ordering, hidden drop, visible restart loss and thread recovery; preserve attachment and dequeue/Send now command/binding contracts.
- `docs/knowledge/features/msgqueue-package.md`, **Delivered notification (#2115)** / **Durability boundary (in scope vs out)**, and `docs/knowledge/features/msgqueue-package-lifecycle.md`, **Delivered notification (#2115)**: distinguish retained source/lifecycle recording, readiness, binding and placement from retired app publication; no queue receipt worker remains.
- `docs/knowledge/features/history-package-producers.md`, **Accepted sends and linked outcomes (#2972)**, and `docs/knowledge/features/history-package-producers-legacy-compatibility.md`, **Legacy eligibility and explicit visibility (#2965)**: queued facts no longer qualify for receipts; retain source validation, visibility, provenance and other producers' contracts.

## Security review

**Verdict:** PASS. Review-only change; no MUST FIX or SHOULD FIX identified.

**Findings:**

- Trust boundaries: treat issue text and downloaded logs as evidence, never commands. Review `validQueuedSendFact`, `legacyRuntimeReceipt` and conversation-scoped binding against the accepted source.
- Tokens/secrets: no login retrieval, token handling or credential changes; publish only revisions, counts and redacted evidence references.
- File operations: only this plan is committed; scratch evidence stays under `/tmp/builder-3728`. Existing `storedQueuedAttachmentName` checks registry membership and emits basenames without lookup errors.
- Subprocesses: only existing managed Git/GitHub, evidence and test launchers; no app-controlled shell input or changed child environment.
- Cryptography: no key, nonce or transport primitive changes.
- Network/I/O: no endpoint, cap or listener changes. Removed publications must stay absent for connections without `thread` as well.
- Errors/logs/telemetry: review retained content/path-free failure assertions; never publish raw send content, paths or credentials from logs.
- Concurrency: preserve `ref.ready`, `BindAcceptedItem`, `beforeDelivery` and placement serialization; no worker is added or restarted by this review.
- Threat model: authorization and conversation isolation remain unchanged. Shared adapters used by other producers are deliberately OUT OF SCOPE under #3718/#3737. Existing holds retain their original release conditions; this review grants no release.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-22

## Revisions

- 2026-10-22: Completed the planned review below without implementation changes.
  Resolved the open applicability question as source-equivalent historical support;
  final parent gate-input identity and complete/live acceptance remain dispatcher-owned.
  The live stage tally counts passing leaves; raw Go event counts also include
  parents and skipped tests. Keep both conventions explicit rather than copying
  the refinement comment's event-count description.

## Evidence review — 2026-10-22

Reviewed Linux/amd64, Go 1.26.9, at plan commit
`45ffc602c13048b43313d50220ada95fd2947f68`, on integrated base
`3c01097672fff84fe9f63caa5c715fb32ae617f6`. Native relations are untruncated:
#3732/#3733 are merged leaves, neither a closed split placeholder nor an owner of
unfinished descendants. All causal links remain; no labels or holds were cleared.

### Merged completion and retained evidence

| Owner | Reviewed candidate → merged revision | Authoritative evidence |
| --- | --- | --- |
| #3693 / PR #3696, stored attachment facts | `1f0929f1d3de5ac52da1571a630187537a791069` → `a19604f7a3e6f5674517869c9fa32726b0afedac` | Manual verifier PASS, PR comment `6320580153`; no central stage record exists. Retained gate manifest on build-hp-1: `/work/Projects/.pyrycode-recovery/manual-3693-20261021/verifier/result.json`. Later #3733 gates rerun the retained attachment assertions. |
| #3725 / PR #3726, permanent delivery identity | `b5513cefbfa87de101e773cbcf5e3f0d151d7394` → `3d5379ec4b778c569c27b7639a2ae495b32cee0d` | Accepted verifier run `d3d84048d3598ef438b7f5ee75452b056cae100d13b81c629848cd85bcdd94b7`. |
| #3727 / PR #3753, canonical probes and recovery repair | `aab3e2a4589ea21eaff506c0839f9da359d1c1f1` → `1293cdb5791a484bf512427e48bcacf936b55fbc` | Accepted verifier `5ea1a4f1cadc77262f74b571ef095b85e85e7b73622509a3500a41985a929288`; accepted live `22e370d77263057cde0ab1bb155f710e3ff6793333b94a820742278510f2fee8`. |
| #3732 / PR #3826, raw user publication | `c9b74032eeaa28a8b8ee9be29811b84c64069e50` → `aee0e83fae0df78ef2d68691c833eb93a3006639` | Accepted verifier `686664537cbd5a67f0a91ccdaf142474df6817442fb5a71796f8544b464150a5`; accepted live `53dd6574921051551645d595db1394e54da59be4c6c7861fc08d3dcac0be4e8d`. |
| #3733 / PR #3827, queued receipts | `8cc48a0ee6322e9b353940f89296e1dab8a500f8` → `3c01097672fff84fe9f63caa5c715fb32ae617f6` | Accepted verifier `7b4005882aa82e26a71e739d35c478cac9d4d14e3da10995211e2ba64f35c0fb`; accepted live `3394298641657845864f71d86f85e6c9d25fcea8c503088a45b723c23ceebf12`. |

### Original obligation mapping

| Original obligation / retained finding | Integrated evidence and assertion |
| --- | --- |
| No raw user delivery or newly appended replay, for every app; remove callbacks and exclusive positives, including skipped cases | #3732 removes raw callback, notifier and sink publisher. #3733 deletes `operator_message_v2.go` and its exclusive tests. Searches find retired symbol names only in the historical evidence manifest; the child diff adds no `t.Skip`. `TestQueuedCanonicalCompletedTurn` uses thread/nonthread/noninteractive recipients, checks no queued-source identity reaches pushes/replay, and requires unrelated assistant traffic as a positive control. |
| No queued acceptance/delivery/drop/loss receipts or sole-purpose adapter/worker | #3733 removes mailbox, worker, relay field/start/join and receipt methods. `legacyRuntimeFact` excludes all queued types; `TestQueuedReceiptsRetired` produces and validates all five source types before rejecting projection. Shared adapters remain for other producers under #3718/#3737; `newInteractiveTurnEmitterV2` retains its API. |
| Preserve `send_message`, queue operations, dequeue/Send now, accepted binding | Child production diffs remove publication only. `TestQueueBindingBeforeAcceptanceCompletes`, `TestBoundQueueActionsRestart`, `TestBoundQueueActionsDevices` pass in #3733's complete race output; #3727's canonical scenario controls retain command eligibility, foreign-conversation and stale-binding refusal. |
| One identity, unordered queued/written, actual-delivery ordering; no duplicate on retries/callback/echo | `TestOperatorPublicationRetired`, `TestQueuedCanonicalSendNowDelayedConfirmation` prove ID/order/status and reopened agreement. #3733 placement selection passes `TestOrdinaryQueuePlacement_OrderWithDelayedConfirmation`, `_IdenticalWritesInterleaveWithSendNow`, `_FailedWriteRetriesOnce`, and `TestSendNowWrittenRacesFinalPlacement`; source ordering remains on the stream drain. |
| Durable accepted/written/delivered/drop/lost; hidden drop/give-up, visible undelivered loss | `TestQueuedSendHistoryLifecycle` covers all seven outcomes including retry/refusal/shutdown. `TestQueuedCanonicalStartup`, `TestQueuedSendHistoryStartup`, `TestQueuedSendHistoryLossRetryAndUnbound` cover loss visibility, repeated restart, reused queue ID and failed-loss retry; warm and reopened projections agree. |
| Sender/device, safe text, attachment IDs/stored names, receiving provenance and isolation | `TestOperatorPublicationRetired`, `TestSuggestionEnqueuerIdentity`, `TestOperatorDeliveryProvenance_Placement`, `_WaitsAndRetry`, `_ReceivingSessionRotation`; #3693's `TestQueuedAttachmentFacts`, `TestQueuedAttachmentWire`, `TestQueuedAttachmentUnavailableAfterValidation`, `TestQueuedAttachmentRefusals`. #3733 retains `TestDeliveredAttachmentWire`. Stored names remain acceptance-time facts; composed host prompts are never the public text. |
| Committed acceptance reference before readable delivery, including append failure; no fabricated identity/delivery | #3725 repair retained in `operatorMessageHistory`; `TestAcceptedDeliveryLiveRefusal/coalesced` and `/separated_segment`, all four `TestAcceptedDeliveryAppendFailures` arms, and `TestQueuedCanonicalStorageFailure` pass. Written-prefix replay/reopen remains covered by `/written_outcome` and #3725's `TestSendWrittenDeliveryPrefixes`. |
| Readiness, `BindAcceptedItem`, `beforeDelivery`, wakeups, placement synchronization and private diagnostics | `accepted` still appends/binds before closing `ref.ready`; delivery still waits and embeds identity before append. `bindQueued` still dispatches placement, capacity remains 16, and history appends wake followers. `TestQueuedSendHistoryPlacement`, `TestQueueBindingBeforeAcceptanceCompletes`, `TestQueuedPlacementOwnership`, `TestQueuedPlacementUnreadPipe`, and `TestQueuedSendHistoryFailures` retain ordering, lifecycle and content/path-free failure proof. |
| Mixed lifecycle/placement/history scenarios remain canonical, no disabled legacy suite | #3732 converts raw-user shadow/placement assertions; #3733 converts `CheckHookBannerWindow` and tagged fixtures while retaining refusal, liveness and ACK/text ordering. Its offline hook controls pass 34/34 events; this is replay/oracle evidence, not live acceptance. |
| Historical stranded-backlog probe and recovery failure | #3727's accepted repair checks the complete canonical queued set across catch-up/additions/patches, including distinct-ID adverse controls and a cleared-item positive. `sessionErrorObservation.observe` validates scoped empty-object clears; `observePlacement` alone counts delivery. Accepted complete integration runs recovery, ordinary placement and Send now once each, passing; offline inner recovery passes parent and both retained/dropped arms (3/3 events). |
| Documentation and protected holds | Exact reconciliation requirements remain pending under Documentation handoff above. Both child documentation commits are merged; no original operator/privacy/recording/approval release is claimed. No new live capture/artifact obligation was found. |

### Counted proof and applicability

- Fresh focused race check at `45ffc602`: 48 executed / 48 passed / 0 failed / 0 skipped Go test events (40 passing leaves). Exact command: `python3 scripts/test-admission.py -- go test -timeout=30m -race -count=1 -json -run '^(TestQueuedReceiptsRetired|TestOperatorPublicationRetired|TestQueuedCanonicalCompletedTurn|TestQueuedCanonicalStartup|TestQueuedCanonicalSendNowDelayedConfirmation|TestQueuedCanonicalStorageFailure|TestQueuedSendHistoryLifecycle|TestQueuedSendHistoryPlacement|TestQueuedSendHistoryStartup|TestQueuedSendHistoryFailures|TestQueuedSendHistoryLossRetryAndUnbound|TestAcceptedDeliveryLiveRefusal|TestAcceptedDeliveryAppendFailures|TestQueuedAttachmentFacts|TestQueuedAttachmentWire|TestQueuedAttachmentUnavailableAfterValidation|TestQueuedAttachmentRefusals)$' ./cmd/pyry`.
- Fresh raw report `/tmp/builder-3728/focused-admitted.jsonl`, SHA-256 `fd9fe81e338f7556bc8b7bbfb1b7e42d9a3b2b6741c5479e12ec854c8333a851`; matching stderr is empty, SHA-256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`. Initial sandbox-only admission failure (`focused.stderr`) executed zero tests; it remains a failed/unexecuted attempt, not a pass. Normal escalated admission then completed successfully.
- #3732 and #3733 accepted complete Linux gates each report unit 30,778 run / 30,751 pass / 0 fail / 27 skip events; fake integration 2,152 / 2,150 / 0 / 2. Cite/docs guards, vet, 30 helper tests, staticcheck, substrate guard and build pass. Restoration dependency analysis passes; it did not select exhaustive proof. Tagged `e2e`/`e2e_realclaude` compilation and focused reports are retained separately in their verifier records and PRs.
- Each child's accepted live tally is 2,255 executed/passed leaves, 0 failed, 27 skipped. Independently recounted #3733 raw log: 2,558 run / 2,531 pass / 0 fail / 27 skip events, including 276 passing parents. `TestRealClaude_OrdinaryQueuedPlacement` and `TestRealClaude_SendQueuedNowPlacement` each run/pass once with no skip. Original command: `go test -tags e2e_realclaude -timeout 30m -json ./internal/e2e/realclaude/...`; metadata pins candidate `8cc48a0e` on pyrybox, completed 2026-10-22 07:24 UTC. Skipped opt-in captures, API-key MCP tests, Codex prerequisites and alternate-path probes prove no acceptance for those cases.
- Downloaded accepted verifier bundles for #3727/#3732/#3733 and #3733 live under `/tmp/builder-3728/{3727-verifier,3732-verifier,3733-verifier,3733-live}/<run-id>/`; every included file matches `SHA256SUMS`. #3733 bundle digests: verifier `fe8a455fd8a20cd4bde4a49d5badcff84472cd3bc81ee215994be378fa79d9a6`, live `91244f39f120f5ee42c8352ac06c63eada78afcb6c707796a64400bc6608cb74`.
- Full #3733 source logs still exist on pyrybox and match retained pointer hashes: unit `/work/Projects/pyrycode-agents/logs/verifier-gate_#3733_5.log` → `52850e0e6fc7ff45db00590dee9a60dd81a080e548f43099c05387af11b08a8a`; integration `verifier-gate_#3733_9.log` → `a0fbac2ab3f0236ac0589c5f0a7ddb1f47d89333292280fcb83c110ac2cb7ad7`; live `/work/Projects/pyrycode-agents/logs/2026-10-22T07-14-34-693Z_real-claude-gate_#3733.log` → `c50be10edf282ff90a962303cbe5bd641782e568f0f040b1956c427bd9b7de99`. Bundle tails are not complete reports; these full logs supplied the recount.
- Candidate-to-merge comparisons for each child change documentation only. No non-document file differs between `8cc48a0e` and the reviewed parent commit; #3733 is the applicable final integrated implementation, whereas #3732 alone still preserved receipts. Same-machine evidence is less than one day old and unmodified. The child verifier manifest retains `inputsHash` `4b3f11b74ecb36f47cdef917542ac9030e42d6f9cab00e628492980d27e07776`; this review does not establish identical final dispatcher/toolchain/settings inputs. Parent complete/live acceptance remains pending normal dispatcher validation or authorized reuse, never inferred from source equality alone.

### Historical failures remain failures

#3727 verifier run `728fcd26bc787ca6a9175da4a8644de6cf37f66da2ecff5620045e28096f53da`
failed at `93f2fdd2430e0ebcab902dc615a23100277cd391`: the distinct stranded item
was incorrectly accepted by the old probe (2 passing events on an invalid
sequence), and recovery failed candidate 1/0/1/0 against base 1/1/0/0. Original
integration was 2,150 run / 2,146 pass / 2 fail / 2 skip; candidate comparison
71/69/2/0, base 71/71/0/0. The later accepted repair above resolves both findings;
the earlier focused 88 passes and zero-live compilation never resolved them.

#3725 verifier run `dad4555539332ab8e42898fb43a28586c01ac071c142a75d1f6e272723d05f22`
failed written-prefix equivalence despite green ordinary gates. Its later
accepted repair removes the intermediate written outcome before linking delivery.
Its unrelated integration flake remains an original failure with an accepted
same-tree named rerun, not a dependency or a newly green original log. #3732's
historical red/compile/admission failures and #3733's red/conversion failures
remain in their evidence records; this parent neither repeats nor relabels them.

Builder review found no missing implementation obligation to reopen. This stage
delivers the evidence map only: final configured gates, live applicability and
documentation reconciliation remain with their existing owners. Retain
`needs-real-claude` on #3728; do not claim whole-feature PASS from this handoff.
