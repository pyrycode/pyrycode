# #1987 — drive one question answer round trip through a live claude

One new test under the `e2e_realclaude` build tag. No production file changes; the
only edits to existing files are a `model` parameter threaded through two shared
test harness helpers.

## Files read

- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` →
  `TestInteractiveStreamModalResolution`, `startStreamModalResolutionHarness` — the
  analogue this slice rides: the harness pairs the phone
  `--allow-remote-permissions` (the same `MayAnswerRemotePermission()` gate the
  question resolver applies), writes the stream-json toggle, and spawns without
  `--dangerously-skip-permissions` so claude's tool calls genuinely park.
- `internal/e2e/realclaude/interactive_stream_permission_deny_test.go` →
  `TestInteractiveStreamPermissionDeny`, `denyModalsUntilIdle` — the attribution
  lesson (source + outcome, not just "the turn continued") and the bounded
  frame-loop drain shape this slice's post-answer drain copies.
- `internal/e2e/realclaude/harness_modal_test.go` → `spawnPermissionDaemon`
  (hardcodes `--model haiku` — the model decision below), `raiseRealPermissionModal`
  (the trigger-then-drain-then-assert scaffold), `drainForControlEvent` (generic on
  the wanted envelope type, so it serves `question_shown` unchanged).
- `internal/e2e/realclaude/interactive_stream_multiturn_continuity_test.go` →
  `drainForCompletedTurnText` — the two-milestone drain that accumulates delta text,
  and the planted-token precedent for asserting content rather than liveness.
- `internal/e2e/realclaude/ask_user_question_capture_test.go` →
  `askQuestionCapturePrompt`, `askQuestionCaptureModel`, `askQuestionToolName` — the
  only live `AskUserQuestion` call ever measured in this tree, its prompt shape and
  its model, both reused here rather than re-derived.
- `internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json` — the measured
  batch: one question, a 14-rune header, two options with a one-line description
  each, `multiSelect: false`. Sets the expectation for what a real batch looks like.
- `internal/questionbridge/questionbridge.go` → `Parse`, `maxInputBytes`,
  `minOptions`/`maxOptions`, `inputQuestion.MultiSelect` — the constraint that shapes
  the trigger prompt: a batch whose `multiSelect` key is ABSENT is rejected outright,
  and a rejected batch falls through to a permission modal this gate never answers.
- `cmd/pyry/modal_resolve_v2.go` → `AnswerQuestion`, `answerVerdict`,
  `answerVerdictInput`, `surfaceQuestion`, `retireQuestion`, `outcomeQuestionAnswered`,
  `sourceQuestionRemote` — the path under test and the two dismissal literals this
  test transcribes (package `main`, not importable).
- `cmd/pyry/question_resolve_v2.go` → `questionResolverV2.admit`, `ResolveAnswer` —
  the per-device gate the harness's `--allow-remote-permissions` pairing satisfies.
- `internal/relay/v2session_question.go` → `handleQuestionAnswer` — confirms the
  inbound frame is intercepted and forwarded with no reply and no broadcast, so the
  test must not wait for an ack.
- `internal/protocol/questions.go` → `QuestionShownPayload`, `Question`,
  `QuestionOption`, `QuestionAnswerPayload`, `QuestionAnswerEntry`,
  `QuestionDismissedPayload` — the wire shapes, and the rule that answer values are
  client-authored and never checked against the offered labels.
- `docs/knowledge/features/e2e-realclaude.md` — the suite's tag, make target and
  "runs during the code-review phase of every dispatched ticket" cadence, which is
  what makes gate reliability (the model choice) worth more than the token delta.

## Context

Every leg of the answer path is merged — #1983's wire types, #1984's interception,
#1986's per-device gate, #1991's `AnswerQuestion` — and none of it has been observed
against a live claude. The outbound half was pinned by #1938's committed capture; the
answer half is built from the vendor page alone. This slice is the observation.

The failure it exists to catch is named in `answerVerdictInput`'s own doc block: a
verdict assembled by re-marshalling the daemon's wire-shaped batch instead of
splicing claude's bytes hands claude a key it does not read (`multi_select` where
claude writes `multiSelect`) — *silently*, because the call is allowed either way. A
test that asserts only "the turn continued" goes green against exactly that. So the
load-bearing assertion is that the ANSWERS reached claude, and the only way to get it
is for the test's choice to be unpredictable from the trigger prompt.

No ADR is warranted; this adds a test to an existing family.

## Design

One new file, `internal/e2e/realclaude/interactive_stream_question_answer_test.go`,
build tag `e2e_realclaude`, holding `TestInteractiveStreamQuestionAnswer` and three
helpers. Plus a `model` parameter on two shared harness helpers (below).

### The model this gate runs under

`spawnPermissionDaemon` hardcodes `--model haiku`. The only live `AskUserQuestion`
call measured in this tree was produced under `askQuestionCaptureModel`
(`claude-sonnet-5`), chosen there because "tool-selection reliability is worth more
than the token delta" — and this gate's failure mode is the same one: a model that
will not reach for the tool deadlines the `question_shown` drain with a message about
modals. Nothing in the tree measures haiku's willingness to call it.

So this gate runs under `askQuestionCaptureModel`, and the constant is REUSED rather
than a second one minted, so the two live `AskUserQuestion` runs cannot drift apart.
Threading it takes a `model string` parameter on `spawnPermissionDaemon` (2 call
sites) and on `startStreamModalResolutionHarness` (2 call sites), with the existing
haiku literal hoisted to a named constant beside the spawn helper and passed at every
pre-existing site — behaviour for #1154 and #1175 is unchanged. The alternative,
copying the harness into this file for one differing argv element, is ~95 duplicated
lines; the parameter is ~15.

**The parameter must be a compile-time constant at every call site.** It lands in
claude's argv; a caller passing a runtime-derived string would put arbitrary text
there. Stated in the parameter's doc.

### The trigger

`questionAnswerTrigger(nonce) string` — `askQuestionCapturePrompt`'s shape, with its
constraints carried and one addition. Load-bearing properties:

- It NAMES THE TOOL, legitimate for #1938's reason: this slice measures the round
  trip, not claude's spontaneous propensity to reach for the tool.
- It asks for ONE question, a short header, FOUR options with a one-line description
  each, and SINGLE selection. Four rather than two widens the space a continuation
  would have to guess from. Single selection is what makes claude emit the
  `multiSelect` key at all — `questionbridge.Parse` rejects a question whose key is
  absent, and a rejected batch falls through to a permission modal that nobody in
  this test answers, so the turn parks until the approval window elapses.
- It carries NO PATH, NO FILENAME and NO "in this repo" — #1938's constraint, kept
  for a different reason here: a question quoting the worktree path would put a
  `/var/folders/…` string into a pipeline-salvaged run log.
- It NAMES NO OPTION AND NO PREFERENCE (AC 4). What it does add is the continuation
  instruction: after the answer, reply with only the exact label chosen and nothing
  else. That instruction is what makes the continuation readable; it cannot leak the
  choice, because the labels do not exist until claude writes them.

### The test body

```go
func TestInteractiveStreamQuestionAnswer(t *testing.T)
```

1. `startStreamModalResolutionHarness(t, askQuestionCaptureModel)`.
2. `batch := raiseRealQuestionBatch(t, h, 2, convID, questionAnswerTrigger(nonce))` —
   sends the trigger and drains to `question_shown` (AC 1).
3. `entries, choice := chooseQuestionAnswers(t, batch)` — the selections (AC 4).
4. Seal one `question_answer` envelope (id 3) carrying `batch.QuestionBatchID`, a
   fixed `answer_token` and `entries` (AC 2). No reply is expected:
   `handleQuestionAnswer` emits none.
5. `text := drainAnsweredQuestionTurn(t, h, convID, batch.QuestionBatchID, perTurnReplyBudget)`
   — attribution plus the completed turn (AC 2, AC 3).
6. `requireContinuationNamesChoice(t, text, choice)` (AC 4).

### `raiseRealQuestionBatch` — AC 1's non-vacuity gate

Signature `(t, h *perConvHarness, reqID uint64, convID, triggerPrompt string) protocol.QuestionShownPayload`.
Mirrors `raiseRealPermissionModal`: `sealSendMessage`, then
`drainForControlEvent(..., protocol.TypeQuestionShown, questionSurfaceBudget)`, then
the assertions BEFORE anything is answered — non-empty `question_batch_id`, at least
one question, at least two options on the focus question (index 0, the one whose
label the continuation is checked for). Logs each question's option count and
`multi_select` so the run records whether a multiSelect arm was produced (the
ticket's note), and logs no claude-authored string except under `%q` (below).

`drainForControlEvent` is reused unchanged; its deadline message names modals because
it predates this family. The helper's doc records the two readings of that deadline:
claude never called the tool, or the daemon never surfaced the batch.

### `chooseQuestionAnswers` — the choice AC 4 turns on

Signature `(t, batch protocol.QuestionShownPayload) ([]protocol.QuestionAnswerEntry, questionChoice)`,
where `questionChoice` holds the focus question's chosen label and its unchosen
siblings.

- ONE ENTRY PER QUESTION, not just the focus one. `answerVerdict` rejects an answer
  whose entry count is not exactly the parked question count — checked first, before
  anything is assembled — so a partial map never reaches claude. The prompt asks for
  one question; answering all of them is what keeps a two-question batch from
  reddening on the daemon's own validator instead of on the behaviour under test.
- THE LAST OPTION IS CHOSEN, per question. The choice must be unpredictable from the
  prompt, and last is not the position a continuation that ignored the answers map
  would lead with.
- The value sent is claude's own label, one value per entry — legal for a
  single-select question and equally legal for a multiSelect one, which
  `answerVerdict` emits as a one-element array from the parked `MultiSelect` alone.

### `drainAnsweredQuestionTurn` — AC 2 and AC 3 in one pass

Signature `(t, h *perConvHarness, convID, batchID string, timeout time.Duration) string`.
ONE drain rather than a `question_dismissed` drain followed by a turn drain, because
the two events race: `AnswerQuestion` fans the dismissal out on its own goroutine
while the continuation travels claude → parser → emitter. A dismissal-first drain
that guessed wrong would silently eat the first continuation deltas and weaken AC 4.

It reads frames in receive order under one wall-clock deadline — decrypting every
`noise_msg` to keep the receive nonce in sync, skipping non-`noise_msg` control frames
WITHOUT decrypting — and:

- `question_dismissed`: batch id must match, `source` must be `remote` and `outcome`
  must be `answered`. Anything else fails loud and names what it means: `unanswered`
  from `retireQuestion`'s backstop says the answer never reached the verdict, and any
  other source says something other than this frame resolved the batch.
- `assistant_delta` for convID with non-empty text: accumulate (M1).
- `turn_state{idle}` for convID: terminal. Returns the accumulated text, having first
  required that the dismissal was seen and the text is non-empty — so "idle without a
  dismissal" and "dismissed but no continuation" each fail with their own message.
- `error`: hard fail, as in the sibling drains.

The two dismissal literals are transcribed constants (`"answered"`, `"remote"`),
their definitions named by symbol in a comment: `outcomeQuestionAnswered` and
`sourceQuestionRemote` in `cmd/pyry`, package `main` and so not importable. A rename
there does not reach this file; the symptom is this assertion failing, which is the
right place to notice it.

### `requireContinuationNamesChoice` — AC 4

The chosen label must appear in the continuation, and it must be the FIRST offered
label the continuation mentions. Case-insensitive substring search on both halves.

The first-mention rule is what closes the vacuity hole a bare containment check
leaves: a claude that never read the answers can restate its own batch, and a
restatement lists the options in offer order — where the chosen one is last. A
compliant reply ("Write-behind") passes; "you chose Write-behind rather than
Write-through" passes; "the options were Write-through, Write-behind" fails. A
sibling label identical to the chosen one is skipped, since the two cannot be told
apart in text.

Every claude-authored string in a failure message goes through `%q` and
`truncateString`, never `%s`: those bytes crossed the subprocess trust boundary,
nothing on this path strips terminal escapes, and the pipeline salvages run logs.

## Concurrency model

The test spawns no goroutines. All frames are read on the test goroutine through
`fakephone.Client.ReceiveBytes` with a deadline derived from a single wall clock per
drain. The package's standing rule is kept in every loop: a `noise_msg` MUST be
decrypted even when it is skipped (the receive nonce is sequential), and a
non-`noise_msg` control frame MUST NOT be (it does not advance the nonce). Getting
either backwards desyncs the CipherState and every later frame fails to decrypt.

Daemon-side concurrency is unchanged and only observed: `AnswerQuestion` settles the
verdict synchronously and detaches only the dismissal fan-out, which is exactly the
race the single combined drain is written not to depend on.

## Error handling

Every path out of the test body is a skip (no `claude`, no credentials — both from
the existing harness), or a `t.Fatalf` naming one defect. No branch falls through
silently, and no assertion is conditional on what claude produced:

| Failure | Where it fails | What it means |
|---|---|---|
| No `question_shown` before the budget | `raiseRealQuestionBatch` | claude never called the tool under this model, or `Parse` rejected the batch (an absent `multiSelect`, out-of-bounds counts) and it fell through to a permission modal |
| Empty batch id / no questions / one option | `raiseRealQuestionBatch` | the batch surfaced but is not answerable — a vacuous green if it were tolerated |
| `question_dismissed` with `outcome=unanswered` | `drainAnsweredQuestionTurn` | the backstop resolved it: the answer never produced a verdict |
| `question_dismissed` with another `source` | same | something other than this frame resolved the batch |
| Idle with no dismissal | same | the turn ended without the batch ever resolving |
| Dismissal but no continuation | same | claude was allowed and produced nothing |
| Continuation without the chosen label first | `requireContinuationNamesChoice` | the answers did not reach claude, or reached it in a shape it does not read |

Every drain is wall-clock bounded, so a stalled daemon fails loud rather than hanging
the suite. The approval window (10 minutes, re-armed while somebody can still answer)
is not the binding constraint; the drain budgets are — `questionSurfaceBudget` for
the cold spawn plus the tool call, `perTurnReplyBudget` for the continuation.

## Testing strategy

This IS the test. Verification of the slice itself:

- `go vet ./...` and `go build ./cmd/pyry` — unchanged production tree.
- `go test -tags e2e_realclaude -run TestInteractiveStreamQuestionAnswer ./internal/e2e/realclaude/`
  compiles and runs; `make check` never compiles this package, so a build break here
  is invisible to it and the package must be built under the tag explicitly.
- AC 5 is read from the `=== RUN` count, never the exit code: no credentials means
  every test skips and exits 0, and a build failure runs zero tests and exits 0 too.

No artefact is written. Nothing under `testdata/` is added, so nothing needs
`git add`ing beyond the test file — and if the run suggests capturing the answered
tool result the way #1938 captured the call, that is its own ticket.

## Open questions

1. **Does claude comply with "reply with only the exact label"?** If a live run shows
   it reliably prefixes the label with prose that mentions the other options first,
   the first-mention rule reddens on a correct daemon. Resolution: the rule tolerates
   every mention AFTER the chosen label, which covers the common "you chose X rather
   than Y" shape; only a leading restatement fails. Revisit only against an observed
   red, and record it under `## Revisions`.
2. **Does a real batch ever come back multiSelect?** The prompt asks for single
   selection, so probably not. The test logs each question's flag rather than forcing
   the arm, per the ticket's note.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The test sits on both sides of one: claude-authored strings
  (question text, header, option labels and descriptions) arrive over the phone as
  `QuestionShownPayload`, and one of those labels is sent back toward claude as an
  answer value. The inbound side is never executed, never written to a file and never
  used to build a path; it is compared and logged. The outbound side introduces
  nothing new — `QuestionAnswerEntry`'s doc block records that an answer sits at
  exactly `send_message`'s trust tier, and the value sent here is claude's own label.
- [Errors, logs, telemetry] SHOULD FIX — claude-authored strings appear in failure
  messages (the chosen label, the continuation text), and nothing on this path strips
  terminal escape sequences while the pipeline salvages run logs. Phase B: every such
  string goes through `%q` (which escapes non-printables) and `truncateString`, never
  `%s`; the batch is never printed with `%v`/`%+v`. The batch id is logged plain —
  `QuestionDismissedPayload`'s doc block marks it daemon-asserted and safe.
- [Subprocess execution] SHOULD FIX — the new `model` parameter lands in claude's
  argv. Phase B: it must be a compile-time constant at every call site, stated in the
  parameter's doc comment. No `sh -c` anywhere; the environment is the harness's
  existing authenticated-HOME inheritance, unchanged.
- [Tokens, secrets, credentials] No findings. `answer_token` is a client-minted
  idempotency key and explicitly not the authorization (`QuestionAnswerPayload`'s doc
  block), so a fixed constant is correct — `TestInteractiveStreamPermissionDeny` uses
  one for `modal_answer`. The real dedup is the batch one-shot. No credential is read
  or written by this file; pairing and the Noise handshake are the harness's.
- [File operations] No findings — this slice writes no file. The trigger prompt
  carries no path or filename, so a claude-authored question cannot quote the
  worktree path (under `/var/folders/` on macOS) into a salvaged log.
- [Cryptographic primitives] No findings — no new primitive. The batch id is the
  daemon's `crypto/rand` nonce and is only echoed back; the test asserts it is
  non-empty and matches, never that it has a shape.
- [Network & I/O] No findings — no listener, no socket, no new connection. Every read
  is a deadline-bounded `ReceiveBytes` and every drain loop is bounded by one wall
  clock, so a stalled daemon fails loud rather than hanging the suite.
- [Concurrency] No findings — the test spawns no goroutine. The one real hazard is
  the receive-nonce discipline, addressed in § Concurrency model. The daemon-side
  dismissal/continuation race is not assumed away: the single combined drain accepts
  either order.
- [Threat model alignment] `docs/protocol-mobile.md` § Security model threat 1
  (prompt injection reaching a render surface) applies to this file as a client of
  that surface; it renders nothing and the finding above bounds what reaches a log.
  OUT OF SCOPE, named: the per-device DENIAL arm (a device paired without
  `--allow-remote-permissions` must not resolve a batch) stays with
  `questionResolverV2.admit`'s hermetic tests, and the refusal arm is #1995.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — implementation

**No design departure.** Every interface above landed as specified, both SHOULD FIX
findings included: claude-authored strings reach a message only through `%q` and
`truncateString` (`questionLabelLogCap`, `questionTextLogCap`), and the `model`
parameter's doc comment states it must be a compile-time constant.

Both open questions resolved without changing the design:

1. **Continuation compliance.** The first-mention rule was checked mechanically
   against ten scenarios outside the worktree before shipping, including the two
   overlap hazards the plan did not name: an unchosen label that is a PREFIX of the
   chosen one (`LRU` beside `LRU-K`) yields equal indices, and the strict `<`
   comparison is what keeps that from being a false red. A restated batch fails, a
   compliant bare label passes, and "you chose X rather than Y" passes.
2. **multiSelect.** Not forced. `raiseRealQuestionBatch` logs each question's flag,
   so the run records what claude actually produced.

**Measured size, stated rather than smoothed over.** The file is 484 lines (233 code,
225 comment) against the refiner's ~300-line estimate; with the 23-line harness
parameterisation the slice wrote ~507 lines, above the size-S 400-line boundary. The
code half is in line with the analogue pair (95 and 168 code lines) — this file
carries what those two split across them plus the choice logic neither has — and the
excess is the package's comment density. The boundary was applied in good faith at
both enforcement points; the overrun was only measurable once the file existed, and
splitting a finished, verified single deliverable would cost more than it returns.
