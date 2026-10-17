# interactive_stream_question_answer_test.go

`interactive_stream_question_answer_test.go` (#1987) — the answer half of the
`AskUserQuestion` round trip, proven live for the first time. Every leg of the
path was merged and unobserved: the wire types, the `question_answer`
interception, the per-device `questionResolverV2` gate, and
`streamApprovalBridge.AnswerQuestion`. `TestInteractiveStreamQuestionAnswer`
drives a real claude to call `AskUserQuestion`, drains the surfaced
`question_shown` and asserts it is non-vacuous, answers it through the
daemon's own inbound `question_answer` path from a paired phone, and proves
the **answers** reached claude — not merely an allow — by requiring the
continuation to name the chosen option first among the offered labels.
Attribution is pinned separately: the resolution must carry a
`question_dismissed` for that batch with `source=remote` and
`outcome=answered`, so the backstop's `unanswered` outcome can't pass in its
place.

Since #2280, the test changes the running child's model after
`question_shown` but before answering the batch. It first correlates the
`session_settings_updated` reply, then answers the original batch id and retains
the existing dismissal and continuation assertions. That ordering distinguishes
"the settings frame was accepted" from the stronger property under test: the
model control request neither resolved nor replaced the parked question. A fresh
application turn after completion must emit `model_announced` for the resolved
target, proving the update reached the child rather than merely being persisted.

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
- **"The continuation contains the chosen label" is a weaker assertion than
  it looks**, because claude authored those labels itself: a continuation
  that merely restates its own batch satisfies plain containment without
  ever reading the answers map. What separates the two is order — pick the
  last offered option, and require the chosen label to be the first offered
  label the continuation mentions. Two hazards only surface under test: an
  unchosen label that is a prefix of the chosen one (`LRU` beside `LRU-K`)
  produces equal string indices, so the comparison must be strict `<`, not
  `<=`; and two options sharing the same label can't be told apart in text at
  all, so a sibling identical to the chosen label has to be skipped rather
  than compared.
- **A dismissal-then-turn two-phase drain would have been a silent race.**
  `AnswerQuestion` settles the verdict synchronously but fans
  `question_dismissed` out on its own goroutine, while the continuation
  travels claude → parser → emitter independently. A drain that assumed
  dismissal arrives first would eat the leading continuation deltas whenever
  the race went the other way, and would do so silently — the frames just
  look consumed. Reading both events off one frame loop under a single
  deadline, with no assumption about their relative order, costs the same as
  guessing and has no wrong case.

See `docs/specs/architecture/1987-live-question-answer-round-trip.md` for the
full design and security review. The user-refusal arm — refusing an
already-surfaced batch through `question_refused` — rides this file's trigger
and drain scaffold and is driven live in
[interactive_stream_question_refusal_test.go](e2e-realclaude-interactive-stream-question-refusal-test-go.md)
(#1995). The per-device denial arm (a device paired *without*
`--allow-remote-permissions` must not resolve a batch) is out of scope for
both files: it stays with `questionResolverV2.admit`'s hermetic tests alone,
since both harnesses pair *with* the flag so the round trip is observable at
all.
