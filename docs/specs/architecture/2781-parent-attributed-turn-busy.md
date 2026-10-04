# #2781 — Exclude parent-attributed subagent activity from main-turn tracking

## Files read

- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor`, `toolCallDeltaFor`, `observe`, `setBusy`: classification, retention and synchronization contracts.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant`, `TestToolCallDeltaFor_ClassifiesToolVariantsOnly`: exhaustive variant coverage and terminal tool-update semantics.
- `cmd/pyry/inbound_deliver_test.go` → busy delivery tests: queued writes wait on the conversation's main-turn mark.
- `internal/e2e/relay_v2_stream_background_conversation_test.go` → replay release and sealed phone driver patterns.
- `internal/e2e/relay_v2_stream_interrupt_test.go` → named client interrupt requests.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSONConfigured`: replay once, held-open stdin, and optional interrupt-result rider.
- `internal/e2e/relay_v2_stream_model_list_test.go` → runner PID and restart-count observations.
- `docs/knowledge/features/streamsup-package.md` and `streamsup-package-per-conversation-turn-busy-tracking.md`: main-turn opener whitelist and three closure feeds.
- `docs/knowledge/features/e2e-harness.md` and its stream-interactive harness overview: hermetic real-daemon setup.
- `docs/knowledge/features/development-verification.md` → lifecycle-neutral events must be tested from both idle and open states, and regressions must fail before the fix.
- `CODING-STYLE.md` and `docs/protocol-mobile.md` → concurrency conventions and Security model.

## Context

Subagent text and tool events carrying an existing parent ID can arrive after the main result, reopening the main-turn mark indefinitely. Normal queued messages then wait despite the main conversation being idle. Fix only existing attribution; inferring ownership of unattributed events and client status displays remain separate work.

One deliverable: correct main-turn delivery tracking with unit and fake-daemon proof. Estimated total written work: 400–500 lines, no new exported types, no consumer updates, three acceptance criteria, and three attribution guards. This fits every sizing limit and is comparable to #2522's broader lifecycle regression. Rechecked against this plan before committing. No overlapping remote feature branch touches the planned files.

## Design

`turnMarkFor(Event) turnMark` returns `turnMarkNone` for `TextChunk`, `ToolStart`, and `ToolUpdate` with a non-empty `ParentToolCallID`. Their empty-parent forms remain main-turn openers. `ThoughtChunk` and `ThinkingProgress` retain current behavior, and `TurnEnd` remains a closer.

`toolCallDeltaFor(Event) toolCallDelta` returns the zero delta for parent-attributed tool starts and updates. Top-level tool updates continue removing calls independently of status. The spawning Agent/Task call itself remains retained until its top-level result or turn closure. Correct comments that describe classification as depending only on variant types.

Use the committed fake replay harness without changing fakeclaude: fragment one starts a background Agent tool and ends the main turn; a release signal emits parent-attributed subagent tool activity with no result. After observing that activity, send zero, one, or two named client interrupts and confirm each distinct interrupt request in the child stdin log. Submit a normal queued probe and require its stdin receipt within five seconds. Observe the probe's echo before any further turn end. Compare the bootstrap runner's PID and restart count before and after, and reject child exit or additional spawn evidence, since an appended stdin log alone cannot prove continuity.

## Concurrency model

No new production goroutines or locks. Classification stays pure; `observe` retains its existing resolution outside the mutex and mutation under the mutex. E2e setup and cleanup use the harness's existing cancellation and daemon stop paths; all waits are bounded.

## Error handling

No new production errors. Existing empty-parent and closure semantics remain intact. Regression setup, replay writes, protocol errors, missing interrupt receipts, premature turn ends, missed deadlines, and child discontinuity fail the test explicitly.

## Testing strategy

- Classifier tables cover parent-attributed text and both tool variants, alongside empty-parent controls and existing thinking cases.
- Tracker scenarios apply every ignored event from idle and with a main Agent/Task call retained. Child starts cannot add calls; colliding child updates cannot remove top-level calls or alter the open mark. Top-level result and main closure still clear calls as specified.
- Fake-daemon table covers zero, one, and two interrupts with the same still-running child and no releasing result. Run all three against the pre-fix daemon to establish discrimination.
- Run race tests on `cmd/pyry` and the touched e2e package with its `e2e` tag, `go vet ./...`, and `go build` for `cmd/pyry` with output in scratch. The verifier owns the full-module gate.

## Open questions

None. Unattributed thinking events intentionally retain their existing behavior.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-tracking.md`, under “Per-conversation turn-busy tracking”, document that parent-attributed text and tool events do not affect main-turn busy or in-flight state; the top-level spawning call still does. State that unattributed thinking events keep their existing behavior.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `ParentToolCallID` is subprocess attribution, not authority. `observe` still resolves the producing session daemon-side, and `inflight` remains conversation-keyed. MUST FIX addressed by design: both classifiers ignore child tool events, including updates whose IDs collide with retained main calls.
- [Tokens, secrets, credentials] No production token generation, storage or exposure changes; tests reuse `paireddevice.Setup` and existing sealed handshake helpers.
- [File operations] Production file operations unchanged. Replay and stdin evidence use test-owned temporary paths and replay fragments use mode `0600`.
- [Subprocesses] No spawn argument or inherited-environment changes in production. Tests leave the interrupt-result rider unset; harness stop owns cleanup.
- [Cryptography] No primitive, key, nonce or comparison changes; sealed phone helpers preserve sequential CipherState use.
- [Network and I/O] No new listener, payload, size cap or network read path. Tests reuse bounded sealed driver reads and a five-second probe deadline.
- [Errors, logs, telemetry] No production logging added; classification reads attribution only and never logs content or parent IDs.
- [Concurrency] No additional locks or goroutines; current tracker mutex and drain ordering remain intact. Child continuity assertions prevent an exit clear from masking the bug.
- [Threat model] Noise authentication and authorization remain at existing boundaries. A confused child can omit attribution and reopen its own conversation as before; ownership inference is deliberately deferred by this ticket's product contract. Client status work remains pyrycode-desktop#1750 and pyrycode-mobile#1763.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04

## Revisions

- 2026-10-04: Corrected the sizing sketch's guard count: implementation uses five attribution checks across the two classifiers for three event variants. Total written work remains about 400 lines, with no exported types or consumer changes. The design and product contract are unchanged.
