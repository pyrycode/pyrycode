# interactive_stream_question_answer_test.go

`interactive_stream_question_answer_test.go` drives a real claude to call
`AskUserQuestion`, validates a nonempty `question_shown`, and answers the original
batch through the daemon's inbound `question_answer` path from a paired phone.
`TestInteractiveStreamQuestionAnswer` and
`TestInteractiveStreamStdioQuestionAnswer` share the non-bypass stdio harness and
`driveSharedQuestionAnswer`. Delivery requires an exact durable `prompt_answered`
fact for the original conversation, batch, asking session and question/answer
mapping, plus a resumed response containing a fresh answer-only interpretation
witness. Completion also requires that batch's `question_dismissed` with
`source=remote` and `outcome=answered`, and terminal idle for the conversation.
Varied prose and overlapping option labels are valid; label mention order proves
nothing about delivery.

Since #2280, the test changes the running child's model after
`question_shown` but before answering the batch. It first correlates the
`session_settings_updated` reply, then answers the original batch id and retains
the existing dismissal and continuation assertions. That ordering distinguishes
"the settings frame was accepted" from the stronger property under test: the
model control request neither resolved nor replaced the parked question. A fresh
application turn after completion must emit `model_announced` for the resolved
target in the same conversation, proving the update reached the child rather
than merely being persisted. Repeated question surfaces during the update must
retain the original batch/conversation identity; dismissal before answering fails.

The gate runs under `askQuestionCaptureModel` (`claude-sonnet-5`), reused from
[ask_user_question_capture_test.go](e2e-realclaude-ask-user-question-capture-test-go.md)
rather than the `--model haiku` the other modal-gate tests hardcode — haiku's
willingness to call `AskUserQuestion` at all has never been measured, and this
gate's failure mode (a model that won't reach for the tool deadlines the
drain with a message about modals) is the same one that model choice exists
to avoid. `spawnPermissionDaemon` and `startStreamModalResolutionHarness` both
took a `model` parameter to carry it, constrained to a compile-time constant
at every call site since it lands in claude's argv.

## Testing question round trips

- **A bypass harness must let the daemon supply the response transport.**
  `spawnPermissionDaemon` formerly supplied `--permission-prompt-tool stdio`
  for operator bypass, masking production's missing argument. The
  `TestInteractiveStreamBypassQuestions` matrix leaves that shaping to the daemon,
  seeds stored bypass before registry loading, and checks real init tool
  availability and effective bypass alongside actual requests, remote answers,
  dismissals and completed continuations. A harmless ordinary-tool witness also
  proves bypass still executes without host approval. Tool-list presence and a
  deny-only capture cannot establish a successful answer round trip. See
  [transport selection](config-package.md#stdio_permission_prompt--interactive-permission-transport-2343).
- **An empty observation channel is not an end-of-stream signal.**
  `startPermissionObserver` forwards observations on a separate goroutine, so a
  nonblocking `default` drain can stop before init, question requests or terminal
  results arrive. The bypass test's final drain still has this limitation, noted
  in [review](https://github.com/pyrycode/pyrycode/pull/3518#issuecomment-6225403140):
  it can falsely fail complete exchanges. An evidence collector needs a bounded
  wait for its expected observations rather than treating temporary emptiness as
  completion.

- **Durable-answer assertions must select history segments before opening
  files.** `readPromptAnswerHistory` follows `history.parseSegmentName` and
  `history.listSegments`: regular files named `segment-` plus a 20-character
  uint64 decimal field plus `.jsonl`. The
  [thread cache, recovery marker and run certificates](thread-package-cache-and-epochs.md#cache-and-epochs)
  share that directory; `thread.Store.writeCacheFile` publishes them through
  `.thread-*` temporary files that can disappear between enumeration and
  opening. Globbing every entry can therefore fail after a successful remote
  answer. Exclude non-segment entries before reading: ignoring missing-file
  errors would also hide a vanished selected segment, whose read failure must
  still fail the assertion. Selection preserves exactly one matching durable
  `prompt_answered` fact, conversation/correlation ownership, asking-session
  provenance, decision, remote source, resolution timestamp, shown visibility
  and the 16 KiB context bound. Missing facts and partial trailing writes retain
  the five-second deadline and 20 ms retry; duplicates fail. The credential-free
  `TestPromptAnswerHistoryReaderPublication` forces rename/removal after a
  directory snapshot without timing sleeps, while
  `TestPromptAnswerHistoryReaderSegmentReadFailure` forces selected-segment
  disappearance at the same boundary. See the
  [reader repair plan](../../specs/architecture/3115-prompt-answer-history-segments.md).
- **`questionbridge.Parse` rejects a question whose `multiSelect` key is
  absent, and the rejection is invisible as a rejection.** A batch missing
  that key falls through to a permission modal that a question-only test
  never answers, so the observed symptom is a deadlined `question_shown`
  drain — not a parse error naming the missing key. A trigger prompt for this
  family must ask for single-vs-multiple selection explicitly, or the gate
  reddens for a reason nothing in its output names.
- **Correlate exact answers and interpretation independently.**
  `requirePromptAnswerHistory` with `checkQuestionAnswerHistory` requires the
  original conversation/batch, asking-session identity in both the fact and its
  provenance, untruncated question context, original indices/text/selection modes
  and every exact sent value. Ownership and an `answer` decision alone would
  leave wrong question/answer mappings green. A receipt or durable fact alone
  still cannot prove the child interpreted the supplied answer.
- **Make the interpretation witness meaningful answer data.**
  After the batch surfaces, `chooseQuestionAnswers` generates a random cache name
  tied to the selected policy and requests it verbatim in the reply. A decorative
  marker was omitted even when the model understood the policy. The trigger asks
  for the supplied cache name without prescribing surrounding prose; its actual
  value appears only in the answer. `checkQuestionWitness` rejects a value already
  present in the trigger, surfaced question/options or collected prior assistant
  text. Single selection carries the label and cache name in one legal free-text
  value; multiple selection retains two offered labels plus the tied free-text
  value. The bypass caller additionally requires each selected label in its
  reply and retains its ordinary-tool execution checks.
  Requiring the chosen label first rejects valid replies such as “Rather than
  FIFO, you selected LRU” and misreads overlapping labels such as `LRU`/`LRU-K`.
  Plain label containment also accepts a restatement of the model's own question.
  `TestQuestionAnswerTurnProof`, `TestQuestionAnswerWitness` and
  `TestQuestionAnswerHistoryProof` cover these false positives, stale pre-answer
  text, witness freshness and exact context mismatches without credentials.
- **A dismissal-then-turn two-phase drain would have been a silent race.**
  `AnswerQuestion` settles the verdict synchronously but fans
  `question_dismissed` out on its own goroutine, while the continuation
  travels claude → parser → emitter independently. A drain that assumed
  dismissal arrives first would eat the leading continuation deltas whenever
  the race went the other way, and would do so silently — the frames just
  look consumed. Reading both events off one frame loop under a single
  deadline accepts either dismissal/continuation order and a witness split across
  deltas. `questionAnswerTurn` starts without pre-answer text and requires the
  witness, matching remote/answered dismissal and conversation idle together.
  Idle may precede a delayed dismissal, but must follow the last nonempty response
  delta: later text clears the idle observation. A leading idle followed by a
  response cannot substitute for terminal idle.
- **A fresh model-confirmation turn needs the full receive contract.**
  Reply/idle liveness alone can pass with another conversation's announcement or
  after an ignored provider alarm. `drainQuestionModelTurn` captures the first
  announcement owned by the driving conversation and checks its expected model
  after that conversation's nonempty reply and terminal idle. Its
  `receiveQuestionEnvelope` path rejects errors, unrecognized provider messages
  and malformed/unknown rate-limit statuses; only the recognized warning is
  allowed. Unexpected modals fail before shared drains can print their payloads.
  `TestQuestionAnswerModelTurnControls` exercises the actual encrypted drain,
  including foreign announcements, alarms and reply/idle ordering. Model, answer,
  provider and modal diagnostics stay quoted and capped.

See the [original round-trip design](../../specs/architecture/1987-live-question-answer-round-trip.md)
and [correlated delivery proof](../../specs/architecture/3617-question-answer-witness.md)
for the design and security reviews. The user-refusal arm — refusing an
already-surfaced batch through `question_refused` — rides this file's trigger
and drain scaffold and is driven live in
[interactive_stream_question_refusal_test.go](e2e-realclaude-interactive-stream-question-refusal-test-go.md)
(#1995). The per-device denial arm (a device paired *without*
`--allow-remote-permissions` must not resolve a batch) is out of scope for
both files: it stays with `questionResolverV2.admit`'s hermetic tests alone,
since both harnesses pair *with* the flag so the round trip is observable at
all.
