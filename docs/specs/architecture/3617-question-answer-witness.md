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
