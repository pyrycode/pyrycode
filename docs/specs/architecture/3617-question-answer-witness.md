# Correlated question-answer delivery proof

## Files read

- `internal/e2e/realclaude/interactive_stream_question_answer_test.go`: `questionAnswerTrigger`, `raiseRealQuestionBatch`, `chooseQuestionAnswers`, `drainAnsweredQuestionTurn`, and `requireContinuationNamesChoice` own the existing round trip.
- `internal/e2e/realclaude/interactive_stream_bypass_question_test.go`: `TestInteractiveStreamBypassQuestions` shares the proof across single/multiple selection and retains ordinary-tool evidence.
- `internal/e2e/realclaude/prompt_answer_history_test.go`: `requirePromptAnswerHistory` verifies ownership but not exact projected answers.
- `internal/e2e/realclaude/prompt_answer_history_reader_test.go`: existing credential-free durable-reader controls.
- `internal/e2e/realclaude/harness_modal_test.go`: `drainForControlEvent` supplies the encrypted receive-loop pattern.
- `cmd/pyry/prompt_answer_history.go`: `recordPromptAnswer` projects original indices/text, selection mode and exact value text; no production changes.
- `cmd/pyry/modal_resolve_v2.go`: `answerVerdict` permits free text, with one value for single selection.
- `docs/knowledge/features/e2e-realclaude-interactive-stream-question-answer-test-go.md`: combined drain lesson requires accepting independently delivered dismissal and continuation.
- `docs/knowledge/features/e2e-realclaude.md`, `development-verification.md`, `CODING-STYLE.md`: tagged compilation, bounded evidence and Go conventions.

## Context

Label mention order confuses varied prose and overlapping labels with failed delivery.
This is one behaviour: proving that the original question's exact answer reached
the asking child. Shared infrastructure and both existing callers form one slice.
No overlapping remote feature branches touch the three existing target files.
Estimate: 600–750 written lines including plan/tests, zero production files,
zero exported types, fewer than ten consumer changes, four acceptance criteria,
and at most ten reject branches in the combined event state machine.

## Design

Generate a random answer-only witness after surfacing the batch. Preserve the
selected label in the free-text answer value; single selection sends one value,
multiple selection retains two offered values plus a selection-specific free-text
witness. Ask the model to return the supplied witness without fixing surrounding
prose. Verify witness absence from trigger, surfaced fields and collected prior
assistant text. Collect pre-answer text while awaiting the batch and model update;
reject dismissal before answering. Both callers use the witness proof instead of
label ordering. Preserve bypass value/ordinary-tool checks.

Extend the existing durable history assertion with an optional exact-question
expectation, leaving unrelated permission/refusal callers unchanged. Require the
original conversation/batch, asking session, answer decision, remote attribution,
untruncated context, original question indices/text/selection modes and exact
sent value arrays. Extract a pure validator for credential-free negative controls.

Extract a small event accumulator used by the live combined drain and offline
controls. It requires the matching remote/answered dismissal, conversation idle
and accumulated response containing the answer witness. Keep one deadline and
accept either event order, including idle before a delayed dismissal. Prior text
is never seeded into the post-answer accumulator. Diagnostics remain quoted and
bounded. No protocol or authorization changes; no decision record needed.

## Concurrency model

No new goroutines. The existing phone receive loop owns all accumulator state;
one deadline bounds reads. Existing harness cleanup owns child shutdown.

## State transitions and identity reuse

| Event | Race-enabled credential-free coverage |
| --- | --- |
| Continuation before/after dismissal, split deltas, idle before dismissal | `TestQuestionAnswerTurnProof` |
| Wrong batch/conversation, absent or wrong witness, stale pre-answer text | `TestQuestionAnswerTurnProof` |
| Repeated batches/selections receive fresh answer-only witnesses | `TestQuestionAnswerWitness` |
| Wrong original question/answer mapping or asking-session ownership | `TestQuestionAnswerHistoryProof` |

## Error handling

Malformed frames, error envelopes, mismatched dismissals and invalid durable
facts fail immediately. Missing milestones or partial durable writes retain
bounded deadlines. A receipt or nonempty text alone cannot complete the proof.

## Testing strategy

Write and run credential-free controls first, observing failure before replacing
the proof. Run affected tagged controls with race detection on macOS, tagged
compilation, `go vet ./...`, and `go build ./cmd/pyry` (output outside worktree).
Dispatcher owns full suites and live `TestInteractiveStreamQuestionAnswer` plus
all `TestInteractiveStreamBypassQuestions` variants, recording revision and
executed/passed/failed/skipped counts. Zero executed/skipped scenarios cannot
satisfy acceptance. Linux is outside verification scope.

## Open questions

None.

## Documentation handoff

Pending documentation stage: in
`docs/knowledge/features/e2e-realclaude-interactive-stream-question-answer-test-go.md`,
update the introduction and “Testing question round trips”: replace the
first-mentioned-label proof with exact correlated answer evidence plus an
answer-only interpretation witness. State that varied prose/overlapping labels
are allowed and dismissal and continuation may arrive in either order.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: SHOULD FIX — subprocess prose and durable JSON are untrusted; exact context validation and a fresh answer-only witness must gate success, not label ordering.
- Tokens/secrets: witness uses `crypto/rand`, is public test data and is not an authorization token. Existing paired-device authorization remains unchanged; no credentials read by controls.
- File operations: reuse segment-only reader and harness-owned conversation path; no new file writes except test fixtures in temporary directories.
- Subprocesses: no new subprocess command construction; existing harness owns child cancellation. Model text never becomes shell input.
- Cryptography: reuse Noise decryption in receive order; random witness is independent of transport nonces and permission tokens.
- Network/I/O: existing frame transport bounds remain; one deadline bounds the combined drain. No new server or connection.
- Errors/logs/telemetry: SHOULD FIX — retain quoted, capped text diagnostics, including error envelopes encountered by the new drain.
- Concurrency: accumulator has one owner and no locks or goroutines; neither event ordering is assumed.
- Threat model: tests strengthen remote-answer evidence without changing the production device gate, cryptography or permission semantics.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-19

## Revisions

- 2026-10-19: terminal idle must follow the last response delta. The accumulator
  clears idle when more text arrives; the stale-idle control rejects a leading
  idle as completion evidence. Idle may still precede a delayed dismissal.
- 2026-10-19: prospective live-gate repair after merge `4e4593a5`. The retained
  gate at `7c8a78cd` failed asking-session provenance; its focused rerun reached
  dismissal and idle but omitted the answer witness. Use the existing non-bypass
  stdio harness for `TestInteractiveStreamQuestionAnswer`, preserving exact
  asking-session checks. Main now also carries MCP provenance support and a
  separate stdio live entry point; retain its replay infrastructure and both
  entry points while using this ticket's stronger proof in their shared driver.
  Put an explicit verbatim-witness request in the generated legal free-text
  answer, preserving the selected label and one value for single selection.
  Log bounded, quoted sent values, durable context and resumed text; validate
  history before draining so a missing witness cannot hide the durable context.
  Add `TestQuestionAnswerTransport` as a source contract for the stdio harness,
  plus witness-instruction and missing/mismatched provenance controls. Keep all
  event-order, exact-answer and bypass selection/tool assertions. This remains
  the same delivery-proof behaviour, with roughly 120 repair lines, no exported
  types, two consumers and no new state/reject branches; total planned written
  work stays within 800 lines. Run the named credential-free race controls,
  tagged vet/compilation and the requested authenticated focused
  `TestInteractiveStreamQuestionAnswer`; full suites and counted live acceptance
  for both question entry points and bypass variants stay dispatcher-owned.
  Security re-review: PASS. Transport reuse preserves device admission and
  harness cleanup; witnesses remain public random test data, never credentials.
  SHOULD FIX: generated answer instructions must explicitly request the witness;
  all logged model/value/context bytes must remain quoted and capped. No new
  subprocesses, file paths, crypto, network listeners or goroutines are added.
- 2026-10-19: focused live source `37ed1f63` failed (1 executed, 0 passed,
  1 failed, 0 skipped): exact stdio provenance/context passed but the resumed
  response omitted the witness. Make the witness meaningful answer data: a fresh
  cache name tied to the selected policy, requested verbatim by the original
  trigger and the free-text answer. This still introduces its value only after
  surfacing, leaves prose unrestricted, and preserves the same exact proof.
  Retain bounded, quoted tool-result diagnostics to distinguish child delivery
  from interpretation if it fails again. Security review remains PASS: only
  synthetic scenario text is logged under the existing cap; no new I/O is added.
- 2026-10-19: prospective repair of verifier findings 1 and 2 at `c3db5a1f`.
  Read `scenario.QuestionAnswer` and `scenario.alarm` in
  `internal/e2e/internal/scenario/question.go`: the shared live driver must retain
  their parked-batch identity and provider-alarm checks. Pass the original batch
  explicitly to `awaitQuestionControl`; reject malformed repeated surfaces and
  changed batch/conversation identities while accepting repeats of that identity.
  Centralize error, unrecognized-message and rate-limit validation at
  `receiveQuestionEnvelope`, permitting only `scenario.WarnRateLimitStatus`.
  Credential-free `TestQuestionAnswerParkedSurface` and
  `TestQuestionAnswerEnvelopeAlarm` cover these checks, including bounded, quoted
  diagnostics. Existing dismissal-before-answer rejection remains in place.
  No overlapping remote feature branches touch the repair files. This restores
  one driver's existing delivery-proof invariant, with no independent consumer
  or useful split. About 120 additional written lines bring the existing 736-line
  branch above the 800-line ceiling; the handback floor preserves this inseparable
  repair and its verification. Zero new exported types, two consumer updates,
  four acceptance criteria and at most ten state-machine reject branches remain.
  Security re-review: PASS. These validators fail closed on untrusted frames and
  cap/quote diagnostics. Existing transport bounds, nonce ownership, credentials,
  file access, subprocess cleanup, cryptography and single-owner concurrency are
  unchanged. Run focused tagged race controls, tagged compilation/vet, full vet
  and the daemon build on macOS. Counted live acceptance and complete suites stay
  dispatcher-owned as the ticket requires; documentation handoff is unchanged.
- 2026-10-19: prospective repair of findings 1–3 at `080870cf`. The fresh
  model-confirmation turn must preserve the same conversation ownership and
  provider alarms as `scenario.QuestionAnswer`/`CompletedTurn`. Extract its
  drain into `drainQuestionModelTurn`, using `receiveQuestionEnvelope` and the
  existing `scenario.CompletedTurn` contract. Capture only the first announcement
  for the driving conversation; require its expected model after nonempty reply
  and terminal idle. Reject unexpected modals before the shared drain can print
  their untrusted payloads; all error/provider/modal diagnostics stay capped and
  quoted. Generalize the existing receiver's arguments to phone/cipher state so
  encrypted credential-free controls exercise the actual drain. Restore the
  bypass settings assertion's bootstrap-session equality alongside mode and YOLO.
  `TestQuestionAnswerModelTurnControls` covers foreign/missing/wrong announcements,
  foreign-before-valid announcements, invalid provider frames (including foreign
  conversations), recognized warnings, unexpected modals, and reply/idle ordering.
  Use the existing helper-process/cipher fixtures to observe fatal rejections;
  retain a source-wiring assertion and verify bounded quoted diagnostics.
  No new goroutines or lifecycle owners; the existing receive loop has one
  deadline and the helper process has context cancellation. No overlapping remote
  feature branches touch the repair files. This restores the same delivery-proof
  invariant, with about 220 written repair lines including controls/plan; the
  existing branch already exceeds 800 lines and the handback floor preserves this
  inseparable repair with its tests. Zero new exported types, three receiver
  call-site updates, four acceptance criteria and no new independent behaviour.
  Security re-review: PASS. SHOULD FIX: fail closed on foreign announcement
  ownership and provider/modal alarms before accepting normal response/idle;
  never print raw provider/modal payloads. No new credentials, file paths,
  subprocess command construction, transport bounds, network listeners or crypto;
  the existing synthetic test-binary helper owns cancellation. Run focused tagged
  race controls, tagged vet/compilation, full vet and daemon build on macOS.
  Current-head counted live acceptance for both question entry points and all
  bypass variants, full suites and documentation remain dispatcher-owned.
