# Retire raw user-message publication

## Files read

- `cmd/pyry/operator_message_history.go` → `operatorMessageHistory`: acceptance-linked delivery source append and placement callback.
- `cmd/pyry/operator_message_v2.go` → `operatorMessageEmitterV2`: raw transport and bounded receipt mailbox.
- `cmd/pyry/queued_send_history.go` → `beforeDelivery`, `delivered`, `receipt`: readiness, durable outcomes and neutral receipt projection.
- `cmd/pyry/queued_message_placement.go` → `bindQueued`, `writeTarget`, `observe`: receiving-child placement and synchronization.
- `cmd/pyry/send_now.go` → `attach`, `take`: callback/echo deduplication.
- `cmd/pyry/main.go` → `runSupervisor`; `cmd/pyry/relay.go` → `startRelayV2`; `cmd/pyry/stream_turn_drain.go` → `streamTurnSink`: production wiring.
- Existing operator history, ordinary placement, provenance, ownership, accepted-delivery, thread publication, stored-attachment and receipt tests: retained lifecycle and privacy witnesses.
- `docs/knowledge/features/msgqueue-package.md`, `msgqueue-package-lifecycle.md`, `history-package-producers-legacy-compatibility.md`: successful writes alone do not establish Claude placement; acceptance must precede outcomes, and receipt identity comes only from stored facts.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`, ADR 042 § Sessions, agents, messages, read marks: verification and canonical item contract.

## Context

Apps consume canonical threads. Retire outbound operator/user message publication for every app while retaining history source facts and the receipt contract until #3733. This is one observable retirement across confirmation and placement, with lifecycle preservation as its verification floor. No new decision record is needed beyond ADR 042's approved cleanup.

Sizing: approximately 650–800 written lines including tests and this plan, zero new exports, four acceptance criteria, no new reject branches. The raw helper caller counts are 14 and 16, above ten. Native ancestry #3732 → #3728 → #3717 requires continuing under the existing `needs-human:sizing` label. A hypothetical split would separate confirmation-path retirement from stream-placement retirement, but would temporarily retain half the raw transport. No feature-branch overlap was found after fetching origin.

## Design

Remove the raw push parameter from both history helpers. Keep safe `MessagePayload` source appends, acceptance reference embedding before append, `beforeDelivery`, placement attach and all queue callbacks. After the linked delivered outcome append, submit only its validated neutral receipt through `queuedSendHistory.publish`, the same bounded mailbox used for acceptance/drop receipts.

Remove `operatorMessageNotify`, its channel, the nested receipt carrier and `streamTurnSink` publisher hooks. The remaining emitter drains receipt notifications only. Preserve durable receipt timestamp/identity and replay append before eligible interactive fan-out. Canonical thread followers continue waking from source appends. Do not change `newInteractiveTurnEmitterV2` or shared producer adapters.

Delete exclusive raw positive fixtures, including skipped fixtures; adapt supported mixed scenarios to stored facts and canonical thread assertions. Preserve refusal/written-prefix, receiving-session ownership, retry, attachment and catch-up regressions.

## Concurrency model

Queue callers and the stream drain append source facts using existing ordering. Receipt producers acquire only the short mailbox lock, never the peer-delivery lock. One receipt Run worker drains bounded notifications and exits on parent cancellation; its cleanup joins it. Existing placement write/result, drain boundary and acceptance-ready synchronization remains unchanged. No new goroutine or lock order.

## State transitions and identity reuse

| Event | Race test witness |
| --- | --- |
| Acceptance then delivery, source/outcome append failures | `TestAcceptedDeliveryLiveRefusal`, `TestAcceptedDeliveryAppendFailures`, `TestQueuedLegacyReceipts_StorageFailure` |
| Ordinary echo/no-echo with delayed confirmation and late echo | `TestOrdinaryQueuePlacement_OrderWithDelayedConfirmation` |
| Identical writes across ordinary/Send now and failed retry | `TestOrdinaryQueuePlacement_IdenticalWritesInterleaveWithSendNow`, `TestOrdinaryQueuePlacement_FailedWriteRetriesOnce` |
| Send now placement preceding callback, repeated echo | `TestQueuedLegacyReceipts_SendNowDelayedConfirmation` |
| Child exit, same-ID successor, cancellation and no read | `TestQueuedPlacementOwnership`, `TestQueuedPlacementUnreadPipe` |
| Receiving-session rotation and retry | `TestOperatorDeliveryProvenance_Placement`, `TestOperatorDeliveryProvenance_WaitsAndRetry`, `TestOperatorDeliveryProvenance_ReceivingSessionRotation` |
| Reopened canonical items and stored attachment names | existing queued-send and daemon-thread attachment tests |
| Stalled peer, bounded handoff and cancellation | `TestQueuedLegacyReceipts_NonblockingPublication` and receipt emitter tests |

## Error handling

Failed source or outcome append creates no delivery identity or receipt. A committed delivery retains its acceptance reference even if the outcome append fails. Full receipt mailboxes drop with neutral warnings; source history remains catch-up authority. Failed writes retain existing retry behavior. Transport cancellation stops the worker; no payload or host path enters logs.

## Testing strategy

First change transport assertions to require silence for raw user frames while retaining receipt positives and canonical identity, and observe failure on the old implementation. Run affected `cmd/pyry` race selections through shared admission with JSON reports outside the worktree. Record revision, selector and counts in the PR. Run vet and binary build; retain tagged `e2e` and `e2e_realclaude` compilation after test deletion. Full configured gates and actual `TestRealClaude_OrdinaryQueuedPlacement` / `TestRealClaude_SendQueuedNowPlacement` acceptance remain pending for the dispatcher. Linux only; other operating systems are outside this ticket's verification scope.

## Open questions

None. Receipt-only publication uses the existing mailbox rather than introducing a second handoff.

## Documentation handoff

Pending for documentation stage:

- `docs/protocol-mobile.md`, **Application message types**, **Full thread item**, **Queue observation and delivery identity**, and **send_queued_now**: say “Operator sends are observed through canonical thread items and explicit thread catch-up; raw user-message pushes and their reconnect replay publication are retired for every app.” Describe retained acceptance identity and actual-delivery order; preserve attachment and command/binding contracts. Keep receipt wording applicable until #3733.
- `docs/knowledge/features/msgqueue-package-lifecycle.md`, **Delivered notification (#2115)**, and `docs/knowledge/features/msgqueue-package.md`, **Durability boundary (in scope vs out)**: distinguish retained source/lifecycle recording and placement from retired user-message transport; delivered receipts still publish independently.
- `docs/knowledge/features/history-package-producers-legacy-compatibility.md`, **Legacy eligibility and explicit visibility (#2965)**: replace the user-plus-outcome publication description with retained source appends and receipt-only publication; preserve receipt validation/privacy rules.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: safe `QueuedMessage` still supplies stored client text; composed delivery bytes never enter publication. Receipt projection remains `legacyRuntimeReceipt`'s validated neutral payload.
- Tokens/secrets: no credential handling changes; mailbox logs carry no payloads.
- File operations: existing history append owns storage permissions and path validation; no new path construction or migration.
- Subprocesses: receiving-child selection and writes are unchanged; no command or environment changes.
- Cryptography: existing relay sealing remains unchanged; no key/nonce changes.
- Network/I/O: no new ingress or size allowance; bounded receipt mailbox preserves producer independence from peers.
- Errors/logs: SHOULD FIX during build: retain mailbox overflow and failed-storage tests proving no text, attachment or host-path leak and no fabricated receipt.
- Concurrency: acceptance and outcome use the same mailbox, preserving acceptance-before-outcome order; remove synchronous stream publication so peer stalls cannot hold placement.
- Threat model: existing paired-device authorization and encrypted relay transport remain in force. Receipt retirement is OUT OF SCOPE, owned by #3733; other legacy producer retirement belongs to later family work.

**Reviewer:** builder (self-review)
**Date:** 2026-10-22

## Revisions

- 2026-10-22: Tagged-source review found `shadowWitness` still required an independently pushed raw user message. Remove that requirement and its exclusive positive mismatch control; retain raw acceptance/delivery joins and canonical checkpoint content mutation controls. The capture still records the other producers that its shared adapter validates. Receipt ordering is acceptance before outcome; asynchronous receipts need not precede unrelated assistant traffic. Mixed tests explicitly drain the mailbox where comparing chronological receipt identities.
