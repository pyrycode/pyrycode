# #2612 — TestRelayV2_StreamModalPermissionRoundTrip: permission surface dropped by the ack loop

## Files read

- `internal/e2e/relay_v2_stream_modal_test.go` → `TestRelayV2_StreamModalPermissionRoundTrip`: the `nextEnv` closure (single decrypt point, already records `tool_use` via `sawToolUse`), the send_message ack-await loop, the `modalDeadline` loop. The only file this change touches.
- `internal/relay/handlers/send_message.go` → `SendMessage`: `queue.EnqueueAttached` runs before `replyAck`, so delivery (and everything downstream of it) is unordered against the ack by design ("Acceptance is established by the enqueue").
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface`, `streamApprovalBridge.broadcast`: `modal_shown` is pushed from the control-server goroutine that parked the approval, independent of the handler goroutine that writes the ack.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSONApprove`, `dialApproval`: the fake dials as soon as the user turn reaches its stdin; ruled out as a cause (the daemon surfaced the modal in every failing run).
- `internal/e2e/relay_v2_stream_new_session_test.go`, `internal/e2e/relay_v2_stream_interrupt_test.go`: siblings with the same ack-await shape (inline loops, not a shared helper).

## Change

Cause (1) from the ticket, confirmed from failing runs. With a diagnostic that recorded every envelope each loop skipped, 4 of 10 runs of `go test -tags e2e -race -count=10 -run TestRelayV2_StreamModalPermissionRoundTrip ./internal/e2e/` failed (`allow`, `deny` ×2, `extended`), and every failure read:

```
skipped-in-ack=[queue_state modal_shown] skipped-in-modal=[queue_state turn_state tool_use conversation_updated]
```

The daemon surfaced the modal and the phone received it — before the ack. The ack loop discarded it, and the modal loop then waited 20s for a frame that had already gone. The handler enqueues before acking, and the modal rides a different goroutine, so nothing orders the two. Production is correct; the test's ordering assumption (and the comment stating it) is wrong.

Fix: record `modal_shown` / `question_shown` at the single decrypt point in `nextEnv`, exactly as `tool_use` already is — first occurrence only, keyed on `tc.question` as the modal loop is today. The modal loop keeps its deadline and its `TypeError` check but no longer decodes; it just pumps `nextEnv` until one of the two is recorded. The wrong comment above the ack loop is rewritten to say the ack and the surface are unordered. The diagnostic instrumentation from the reproduction is removed. No production file changes; no deadline changes.

Sibling tests with the same ack-loop shape (`relay_v2_stream_new_session_test.go` M1/M4, `relay_v2_stream_interrupt_test.go`) drop non-ack frames too, but they wait for an `assistant_delta` rather than a broadcast, and they use inline loops rather than a shared helper, so per the ticket they are listed in the PR and filed as a follow-up rather than changed here.

## Testing strategy

The existing subtests are the proof: `allow`, `deny` and `extended` exercise the race. Acceptance is `go test -tags e2e -race -count=15 -run TestRelayV2_StreamModalPermissionRoundTrip ./internal/e2e/` passing 15/15, against 4/10 failing on the pre-fix tree. No new test.

## Documentation handoff

None — the ticket has no documentation section and this change is test-only.

## Revisions

### 2026-09-25 — Security review added (verifier finding, PR #2615)

The ticket carries `security-sensitive`, and the plan was committed without the `## Security review` section the gate requires (verifier MUST FIX on PR #2615). The section below is the adversarial pass, run against this plan and the diff already on the branch. It found nothing that changes the design, so the code is unchanged.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The change sits entirely in the test's phone-side reader. The daemon → phone boundary is still the Noise transport, and `nextEnv` remains the single decrypt point: the permission-surface payloads are decoded there with the same `json.Unmarshal` into `protocol.ModalShownPayload` / `protocol.QuestionShownPayload` the modal loop used before. The only difference is when they are decoded. No production file is touched. `streamApprovalBridge.Surface` and the enqueue-before-ack order in `SendMessage` are unchanged and were confirmed correct by the failing-run evidence.
- [Tokens, secrets, credentials] No findings. No key, token or credential is created, stored, logged or compared. The test's Noise session state (`recvCS`) is used as before.
- [File operations] No findings. No file is read or written by the change.
- [Subprocess] No findings. The fake-claude environment (`PYRY_FAKE_CLAUDE_STREAM_APPROVE`) and how the fake is spawned are unchanged.
- [Cryptographic primitives] No findings. Frames are still decrypted strictly in capture order through one receive cipher state, so the nonce sequence is unchanged. Recording a frame in `nextEnv` consumes no extra frame and skips none.
- [Network & I/O] No findings. The 20-second `modalDeadline` and the ack deadline are unchanged. A surface that never arrives still fails the test at the same deadline.
- [Error messages, logs] No findings. The diagnostic that listed skipped envelope types was removed before the fix commit, so no frame contents reach the failure output beyond what the test already printed.
- [Concurrency] No findings. `nextEnv` runs on the subtest's single reader goroutine. The recorded payloads are closure-local to each subtest, so no state is shared across subtests and no lock is needed.
- [Assertion strength] No findings. The test does not relax any check. The modal loop still fails on a `TypeError` envelope, and every assertion on the recorded payload runs after the loop as before: conversation scoping, a non-empty `ModalID`, the fixed option IDs and the question shape. First-occurrence recording matches the old loop, which also stopped at the first matching frame. A `modal_shown` in the question case, or a `question_shown` in a modal case, is still ignored, as before.
- [Threat model alignment] OUT OF SCOPE, informational. The evidence shows `modal_shown` can reach the phone before the `send_message` ack by design, because acceptance is set by the enqueue. That is not a vulnerability: the surface carries its own `ConversationID` and `ModalID`, and a resolve is keyed on `ModalID`, not on the ack. A phone client must not infer that a modal belongs to the last-acked send. Documenting that ordering in the mobile protocol reference belongs to the documentation stage if it wants it. No ticket is required.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
