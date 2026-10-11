# Hook refusal window receipt accounting

## Files read

- `internal/e2e/realclaude/interactive_stream_hook_blocked_banner_test.go` → `hookBannerWindow`, `awaitBanner`, `awaitCompletedTurn`: sole reader, refusal witness, settle budget and liveness assertions.
- `internal/e2e/realclaude/thread_shadow_capture_test.go` → `TestThreadShadowHistoryCapture`: raw history store rooted in the harness instance.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyRuntimeReceipt`: empty info banners preserve durable runtime fact identity.
- `cmd/pyry/queued_send_history.go` → `accepted`, `delivered`, `queuedSendFact`: acceptance identity and linked user-message delivery facts.
- `cmd/pyry/operator_message_history.go` → `operatorMessageHistory`: delivery references a durable `message` with client message identity.
- `cmd/pyry/runtime_history.go` → `recordRuntimeFact`: opening receipt identifies `main_turn_opened` and its turn.
- `internal/history/log.go` → `Store.Page`, `Entry`: bounded raw pagination exposes original fact types, IDs and timestamps.
- `docs/knowledge/features/e2e-realclaude.md` and `e2e-realclaude-interactive-stream-hook-blocked-banner-test-go.md`: timeout destroys the phone; sleep unread before the next send, and quote subprocess prose in diagnostics.
- `docs/knowledge/features/history-package-producers-legacy-compatibility.md` § Legacy eligibility and explicit visibility: legacy pages erase the distinction among runtime receipts.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: test behavior and preserve counted live proof as a separate gate.

## Context

Queued acceptance/delivery receipts can precede the hook refusal. The current first-banner wait and two-banner count mistake intentional runtime accounting for a hook failure. This is one test-only deliverable; production behavior and hook installation stay as they are. No decision record is needed.

## Design

`awaitBanner` waits under its existing deadline for the driving conversation's banner containing `oslcapBlockReason`; empty receipts remain retained. The existing refusal truncation and `blocked` witness checks still run after that wait.

Retain every envelope in `next` through terminal liveness idle. Read the harness's raw durable history through bounded `Store.Page` calls. A test-local validator joins each banner to its raw entry by `history_entry_id` and timestamp. Require six banners: one durable refusal, two acceptances identified by `m-blocked`/`m-live`, their two deliveries linked through accepted entry IDs and matching durable user messages, and one opening identifying the liveness assistant turn. Reject duplicate, absent and unexpected identities. Each receipt must have all five nonnull banner fields, exactly the empty info payload, and nonzero durable/replay IDs and timestamp.

Check each acceptance precedes its delivery in live arrival and durable order. Only the opening must follow the liveness ack, follow the refusal in durable time/order, and precede assistant text. Queued receipts have no ack-order constraint. Check retained pre-ack frames too, including frames consumed while awaiting refusal, for refused assistant text or turn lifecycle. Keep the existing correlated ack, delta, terminal idle, error/unrecognized-frame rejection and `passed` witness proof.

New helper and table tests live beside the original test under `internal/e2e/realclaude/`; no production files. No other fetched feature branch currently edits the original test.

Sizing: approximately 450 written lines including plan and negative controls, zero exported types/interfaces, two local consumers, four acceptance criteria; no new asynchronous state machine. All five builder limits hold.

## Concurrency model

The existing test goroutine owns window state and history reads. No new goroutines or shutdown paths. Keep existing harness cleanup and terminal receive-timeout behavior.

## State transitions and identity reuse

| Event | Race-enabled coverage |
|---|---|
| Receipts precede refusal; two sends share one conversation | `TestHookBannerReceiptWindow` positive cases and refusal predicate controls |
| Repeated receipt identity or absent receipt | `TestHookBannerReceiptWindow` duplicate/missing cases |
| Delivery precedes acceptance or references the other send | `TestHookBannerReceiptWindow` ordering/link controls |
| Opening before ack or after assistant text; refused lifecycle | `TestHookBannerReceiptWindow` ordering/negative-frame controls |

The dispatcher additionally executes `TestInteractiveStreamHookBlockedBannerReachesTheClient` live; it must record 1 executed, 1 passed, 0 failed and 0 skipped.

## Error handling

History read/decode failure or any identity, payload, count or ordering mismatch fails the test with quoted or content-free diagnostics. Empty receipts cannot end the refusal wait. Existing deadlines fail absent refusal/hook execution and incomplete liveness; no receive-timeout retry.

## Testing strategy

Write credential-free table controls first and observe a red before implementing the validator/predicate. Cover accepted receipts on either side of the liveness ack, all required fields/identities, wrong facts, duplicate/missing/extra banners, links and ordering. Run tagged scoped race tests, tagged compilation/vet, module vet and binary build. The verifier owns the full-module gate. The dispatcher owns the named live test; compile-only checks are not live proof.

## Open questions

None.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/history-package-producers-legacy-compatibility.md`, “Legacy eligibility and explicit visibility,” paragraph naming this test: state that the live window separately validates hook refusal and queued-send/opening receipts by durable fact identity; the first banner need not be the refusal, and an early empty witness does not prove the hook never ran.
- Pending documentation stage: `docs/knowledge/features/e2e-realclaude-interactive-stream-hook-blocked-banner-test-go.md`, “Lessons that outlive this ticket”: the same requirement.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: SHOULD FIX — identify receipts from raw fact identity and conversation, never from empty text alone; the validator enforces this and the refusal predicate requires the block reason.
- Tokens/credentials: no changes to authentication or credential handling; no new credential reads or logs.
- File operations: use `Store.Page` under the harness-owned instance and generated conversation ID, retaining store containment checks; no new file writes beyond test fixtures in temporary directories.
- Subprocesses: keep `hookBannerInstallUserSettings` and existing rig/spawn/cleanup unchanged; no new shell interpretation or child invocation.
- Cryptography: existing Noise decrypt/nonce progression in `next` stays unchanged; no new crypto.
- Network/I/O: existing bounded phone reads and per-turn deadline remain; history pagination uses the store's bounded reader.
- Errors/logs: report fact types/IDs, quote any subprocess-controlled prose, never print whole raw history payloads.
- Concurrency: window remains owned by one test goroutine; no locks, goroutines or identity allocation added.
- Threat model: test-only assertion repair; production relay authorization and client render boundaries remain with their existing owners.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11
