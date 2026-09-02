# #1995 — drive one question refusal through a live claude

One live claude turn in which a real `AskUserQuestion` batch is refused through the
daemon's own inbound `question_refused` path, and the *behaviour on the far side of
the deny* is observed rather than assumed. #1990 chose the deny wording
(`reasonQuestionRefused`) because a bare deny leaves claude free to answer its own
question and press on; whether that wording achieves it has never been seen.

One new file, `internal/e2e/realclaude/interactive_stream_question_refusal_test.go`,
under the `e2e_realclaude` build tag. No production file is touched, no existing test
file is edited, no new exported symbol appears.

## Files read

- `internal/e2e/realclaude/interactive_stream_question_answer_test.go` (#1987) →
  `TestInteractiveStreamQuestionAnswer`, `raiseRealQuestionBatch`,
  `drainAnsweredQuestionTurn`, `questionSurfaceBudget`, `questionLabelLogCap`,
  `questionTextLogCap` — the file this slice rides. `raiseRealQuestionBatch` and the
  three caps are called verbatim; `drainAnsweredQuestionTurn` is the *shape* the
  post-refusal drain follows, not a function this reuses (its milestones are answered
  and continued, which are the wrong milestones here).
- `internal/e2e/realclaude/interactive_stream_permission_deny_test.go` (#1175) →
  `denyModalsUntilIdle`, `requireTriggerFileAbsent` — the deny arm this stands beside.
  `requireTriggerFileAbsent` is called verbatim; `denyModalsUntilIdle` is the
  answer-every-modal-until-idle shape, inverted from reject to allow and folded into
  this slice's single drain.
- `internal/e2e/realclaude/harness_modal_test.go` → `writeFileTrigger` (the
  `pyrycode-<nonce>.txt` base-name contract the absence walk depends on),
  `drainForControlEvent`, `spawnPermissionDaemon` (why the daemon runs *without*
  `--dangerously-skip-permissions`), `permissionDaemonModel`.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` →
  `startStreamModalResolutionHarness` — pairs the phone with
  `--allow-remote-permissions`, which is the bit `questionResolverV2.admit` applies to
  a refusal as much as to an answer.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` →
  `perConvHarness` (the `workdir` field the absence walk roots at), `sealEnvelope`,
  `perTurnReplyBudget`.
- `internal/e2e/realclaude/ask_user_question_capture_test.go` →
  `askQuestionCaptureModel` — the settled model, reused rather than re-minted.
- `cmd/pyry/modal_resolve_v2.go` → `reasonQuestionRefused` (the wording under test),
  `RefuseQuestion` (its dismissal fans out on its own goroutine — the reason the drain
  is single and order-agnostic), `outcomeQuestionRefused` / `sourceQuestionRemote`
  (the vocabulary this transcribes), `outcomeQuestionUnanswered` /
  `sourceQuestionNoAnswer` (`retireQuestion`'s backstop, the near-miss that must fail
  loud).
- `cmd/pyry/question_resolve_v2.go` → `questionResolverV2.ResolveRefusal`, `admit` —
  the per-device gate the harness's pairing flag satisfies, and the reason a refusal
  has no allow conjunction.
- `internal/relay/v2session_question.go` → `handleQuestionRefusal` — emits **no reply
  and no broadcast of its own**, which is why the dismissal is the only thing the test
  can correlate on.
- `internal/protocol/questions.go` → `QuestionRefusedPayload` (two id fields, no free
  text at all), `QuestionDismissedPayload` (batch id marked daemon-asserted and safe
  to log; `Outcome` must never carry a claude-authored label).
- `docs/knowledge/features/e2e-realclaude-interactive-stream-question-answer-test-go.md`
  — three lessons that shape this file: `questionbridge.Parse` rejects a question
  whose `multiSelect` key is absent and the symptom is a deadlined `question_shown`
  drain naming *modals*; a dismissal-then-turn two-phase drain is a silent race; the
  refusal arm was scoped here.
- `docs/knowledge/features/e2e-realclaude.md` — the build tag and the make target;
  no `-race` in this suite by design.

## Context

Every leg between the phone and the deny is merged and hermetically pinned: #1983's
wire types, #1984's interception, #1986's per-device gate, #1990's `RefuseQuestion`.
The hermetic tier proves the deny goes out carrying those exact bytes. What no test in
the tree can show is what a real claude *does* with them, and the wording exists
entirely for its effect on claude's next move.

The sibling family sets the expectation for where the surprise lives. When
`interactive_stream_permission_deny_test.go` was first run live it turned out real
claude retries a denied tool, which nobody knew until the run and which needed a whole
loop (`denyModalsUntilIdle`) the ticket had not budgeted. The same shape of unknown is
expected here, and this design pre-commits to two of its plausible forms — claude
re-asks the question, or claude presses on into the blocked work — rather than
discovering them as a hung suite.

No ADR is warranted: this adds a gate over settled design and introduces no decision.

## Design

### One test, one live turn

`TestInteractiveStreamQuestionRefusal` runs four steps against
`startStreamModalResolutionHarness(t, askQuestionCaptureModel)`:

1. `raiseRealQuestionBatch(t, h, 2, convID, questionRefusalTrigger(nonce))` — called
   verbatim from #1987. It sends the trigger, drains to `question_shown`, and asserts
   the batch is non-vacuous before anything is refused.
2. Seal a `question_refused` envelope (request id 3) carrying only
   `QuestionBatchID` and `AnswerToken`. There is nothing else to carry: the deny
   message is a compile-time constant on the daemon side.
3. `settleRefusedTurn(...)` — one drain that proves attribution, drives the turn to
   terminal `turn_state{idle}` by **allowing** every permission modal and **refusing**
   every re-asked batch, and returns what claude said plus a record of what it did.
4. `requireTriggerFileAbsent(t, h.workdir, nonce)` — called verbatim from #1175,
   after idle, so the tool phase is definitively over.

### The trigger — the crux of AC 2

`questionRefusalTrigger(nonce)` is a new function in this file. It cannot be #1987's,
whose prompt ends *"do not write code and do not use any other tool"* precisely so
that slice measured only the continuation's wording. Here the question must gate
**real work**, and the artefact that work produces must be the one
`requireTriggerFileAbsent` walks for — base name `pyrycode-<nonce>.txt`, the contract
`writeFileTrigger` already satisfies. A trigger whose blocked work writes any other
name makes the absence check vacuous and greens silently.

Contract of the prompt, each clause load-bearing:

- **Names `AskUserQuestion`**, legitimate for #1938's reason: this measures the round
  trip, not claude's spontaneous propensity to reach for the tool.
- **Asks for a single choice among four options.** `questionbridge.Parse` rejects a
  question whose `multiSelect` key is absent, and the rejection is invisible as a
  rejection — the batch falls through to a permission modal and the symptom is a
  deadlined drain naming modals. Four sits inside `Parse`'s 2–4 bound.
- **Gates a Write of `pyrycode-<nonce>.txt` on the answer**, so a claude that guessed
  an answer and pressed on has somewhere concrete to press on to.
- **Carries the bare base name and no path**, so a claude-authored question cannot
  quote the worktree path (under `/var/folders/` on macOS) into a salvaged run log.
- The nonce keeps the trigger distinct per run and makes the absence walk unambiguous.

### `settleRefusedTurn` — one drain, three milestones, two actuating arms

Signature and behaviour, not body:

```go
func settleRefusedTurn(t *testing.T, h *perConvHarness, convID, batchID string,
    startReqID uint64, timeout time.Duration) refusalObservation
```

It reads binary→phone frames in receive order under one wall clock, decrypting every
`noise_msg` to keep the sequential receive nonce in sync and skipping non-`noise_msg`
control frames without decrypting — the package's standing loop. A `TypeError`
envelope at any point is a hard fail. Per frame type:

| Frame | Action |
|---|---|
| `question_dismissed` | Assert `Source == "remote"` and `Outcome == "refused"` on **every** dismissal. The one matching `batchID` sets the attribution milestone. |
| `question_shown` | Claude re-asked. Refuse the new batch with a fresh token and request id, bounded by `maxExtraRefusals`. |
| `modal_shown{permission}` | **Allow** it (`PermissionOptionKindAllowOnce`) with a fresh token and request id, bounded by `maxAllowedModals`. This arm is what makes AC 2 non-vacuous. |
| `modal_dismissed` | Assert `Source == "remote"` — an allow resolved by the daemon's approval timer means our answer was dropped. |
| `assistant_delta` for `convID` | Accumulate, so the header's measured observation has words behind it. |
| `turn_state{idle}` for `convID` | Terminal: return, having required the attribution milestone. |

`refusalObservation` is an unexported struct of counts and text — extra refusals,
modals allowed, continuation length — logged at the end so the run records what claude
actually did. Nothing in it is asserted beyond the milestones above; AC 3 asks for a
*measured observation* written into the header, and inventing an assertion about
claude's chosen strategy would be pinning a stochastic choice.

**Why allowing modals is the correct polarity**, and the whole reason this differs
from the deny arm. A `Write` is itself permission-gated on this harness — that is why
`writeFileTrigger` raises a modal at all — so an absence check run under a rejecting
loop would be guaranteed by the permission gate whether or not the refusal did
anything. Allowing means a claude that guessed and pressed on genuinely *would* have
produced `pyrycode-<nonce>.txt`, and the walk finding it is a real red. Same
"different fabric" discipline `requireTriggerFileAbsent` was written under: a
deterministic filesystem observable backing a stochastic one.

**Why re-asked batches are refused rather than ignored.** A re-asked batch left
outstanding parks the turn until `mcpApprovalTimeout` (10 minutes) elapses, which
overruns `perTurnReplyBudget` and produces a wall-clock diagnostic that names nothing.
Refusing it keeps the turn moving by our explicit refusals and keeps the diagnostic
specific. The ticket names re-asking as one of the plausible outcomes, so this is a
budgeted branch, not speculation.

**Why one drain and not three.** `RefuseQuestion` fans the dismissal out on its own
goroutine — its doc block says why: the caller runs on the relay manager's `Run`
goroutine and `broadcast` funnels back onto it — while anything claude says travels
claude → parser → emitter independently. A dismissal drain followed by a turn drain
would assume an order it need not win and would eat frames silently. This loop assumes
no order and requires the milestones it needs.

### Vocabulary, transcribed not imported

`outcomeQuestionRefused` and `sourceQuestionRemote` live in `cmd/pyry`, package
`main`, which a test package cannot import. This file declares its own two constants,
as #1987 does, with the same note: a rename in `cmd/pyry` does not reach these
literals, and the symptom is this file's attribution assertion failing — the right
place for a reader to notice the vocabulary moved.

## Concurrency model

The test spawns no goroutine of its own. Two hazards, both handled by construction:

- **Receive-nonce discipline.** The Noise receive nonce is sequential, so every
  `noise_msg` frame must be decrypted in arrival order; a non-`noise_msg` control
  frame (rekey) must be skipped *without* decrypting. Every loop in this file follows
  the package's standing shape, and a desync surfaces as a decrypt failure naming it.
- **The daemon-side race** between the detached dismissal fan-out and the turn's own
  frames, resolved by the single order-agnostic drain above.

Shutdown is the harness's: `t.Cleanup` stops the daemon, closes the phone and the fake
relay in the order `startStreamModalResolutionHarness` registers them.

## Error handling

Every wait is bounded and every bound has a diagnostic that names what it means:

- `questionSurfaceBudget` (180s) on the surfacing drain — reused. Its two readings are
  that claude never called `AskUserQuestion` under this model, or that
  `questionbridge.Parse` rejected the batch and it fell through to a modal.
- `perTurnReplyBudget` (120s) on `settleRefusedTurn` — the wall clock. Its message
  distinguishes *no dismissal yet* ("the refusal never resolved the batch: rejected by
  the daemon's validator, denied at the device gate, or never reached the resolver")
  from *dismissed but never idle* ("the turn hung after the refusal").
- `maxAllowedModals` and `maxExtraRefusals` — two caps with diagnostics **distinct
  from the wall clock's**, as AC 3 requires. Exceeding either says claude keeps
  reissuing past the cap, which is a product question about whether a refused turn
  terminates, not a flake to widen the cap for.
- A `question_dismissed` carrying `{unanswered, no_answer}` fails loud rather than
  counting as "the batch resolved": that pair is `retireQuestion`'s backstop reporting
  the approval window elapsed, which is exactly what a refusal that never produced a
  verdict looks like from out here.

Claude-authored bytes reach a message only through `%q` and `truncateString`; see
§ Security review.

## Testing strategy

The deliverable *is* a test, so the strategy is how its own non-vacuity is proven:

- The surfacing gate runs **before** the refusal (`raiseRealQuestionBatch` asserts a
  non-empty batch id, at least one question, at least two options), so nothing later
  can pass over a batch that never surfaced.
- Attribution (`refused` + `remote` for *this* batch id) is what separates the
  behaviour under test from a batch the approval window abandoned or a frame the
  device gate dropped — both of which also end with claude not proceeding.
- The absence check is deterministic (a `filepath.WalkDir` for a per-run unique base
  name) and is made non-vacuous by the allow arm, not by a second stochastic check.
- Local verification: `go vet ./...`, `go build ./cmd/pyry`, and a compile of the
  tagged package (`go test -tags e2e_realclaude -run XXX ./internal/e2e/realclaude/`)
  — `make check` never compiles this package, so a build break there is invisible to
  the standard gate.
- One real run of `-run '^TestInteractiveStreamQuestionRefusal$'` under
  `-tags e2e_realclaude`, whose observed outcome is written into the file header. AC 3
  demands a *measured* observation, so this run is not optional; its `=== RUN` line is
  read, never its exit code.

## Open questions

1. **What claude actually does with `reasonQuestionRefused`.** Stops, re-asks, or
   presses on. The design tolerates the first two and reddens on the third; the answer
   goes in the header and in § Revisions.
2. **Whether a single allow-bounded turn settles inside `perTurnReplyBudget`.** If the
   observed run needs longer, the constant is this file's own to widen with the
   measurement recorded — not the shared `perTurnReplyBudget`.
3. **Whether `maxExtraRefusals` fires at all.** If claude never re-asks, that branch
   is untaken in the observed run; it stays, because its absence turns a re-ask into a
   10-minute park with a diagnostic that names nothing.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] **SHOULD FIX — the allow arm is this slice's genuinely new
  exposure, and it is not the same as the answer arm's.** #1987 sent one
  claude-authored label back toward claude; this file instead **auto-approves whatever
  permission-gated tool a live claude asks for**, in a turn where it has just been
  instructed to stop, for up to `maxAllowedModals` modals. Nothing narrower is
  available: gating the allow on the modal's *content* would decide, stochastically,
  which continuation counts as pressing on, and would hand back a green whenever
  claude pressed on in a shape the matcher did not recognise — the exact vacuity AC 2
  exists to remove. So the containment is three deterministic bounds, all of which
  Phase B must land: the count cap; the existing `Class == "permission"` assertion, so
  a trust or onboarding modal cannot be auto-approved; and the harness's isolated
  `WithWorktreeAuthenticated` HOME with the daemon workdir beneath it. Add a fourth
  that costs nothing: **log every allowed modal's `Title` through `%q` and
  `truncateString`**, so an allow of something unexpected is legible in the salvaged
  record instead of silent. The marginal exposure over the tree as it stands is *N*
  allows rather than the one `TestInteractiveStreamModalResolution` already performs
  against a live claude; the isolation posture is the harness's and is unchanged.
  Inbound, the claude-authored strings (question text, header, option labels) are
  never executed, never written to a file and never used to build a path.
- [Errors, logs, telemetry] **SHOULD FIX.** Claude-authored bytes reach failure
  messages here from three surfaces, one more than the answer arm has: the batch
  (labels/headers), the accumulated continuation, and now `ModalShownPayload.Title`.
  Nothing on this path strips terminal escapes and the pipeline salvages run logs.
  Phase B: every one of them goes through `%q` and `truncateString`
  (`questionLabelLogCap`, `questionTextLogCap`), never `%s`, and no payload is ever
  printed with `%v`/`%+v`. The continuation is logged by **length** on the success
  path and by content only inside a failure message. Batch id and modal id are
  daemon-asserted — `QuestionDismissedPayload`'s doc block marks the former safe — and
  are logged plain.
- [File operations] **SHOULD FIX, and it is the one place this trigger is riskier than
  #1987's.** That trigger deliberately carried no filename; this one must, because the
  artefact is the observable. Phase B: the prompt carries the **bare base name only**
  — `pyrycode-<nonce>.txt`, no directory, no absolute path, no "in this repo" — so a
  claude-authored question cannot quote the worktree path (under `/var/folders/` on
  macOS) back at us; and where claude resolves it to an absolute path in its own
  reply, the log bound above is what keeps that out of a salvaged record. The slice
  writes no file of its own. `requireTriggerFileAbsent` is reused verbatim; it walks
  rather than `os.Stat`s one path, which is what stops a write to a sub-path from
  passing as absence. The walk runs **after** terminal idle, so the check-then-use gap
  is closed on the side that matters — the tool phase is over before the walk starts.
- [Tokens, secrets, credentials] No findings. `QuestionRefusedPayload`'s doc block
  states plainly that `answer_token` is idempotency and **not** authorization (the
  device gate is), and that the dedup is the one-shot batch consume — so a fixed
  constant on the refusal and a per-request-id string on each subsequent frame are
  correct, exactly as `TestInteractiveStreamPermissionDeny` mints them. This file
  reads and writes no credential; pairing and the Noise handshake are the harness's.
- [Subprocess execution] No findings. The one value this slice puts in claude's argv
  is the model, and `askQuestionCaptureModel` is a compile-time constant, which is
  `spawnPermissionDaemon`'s stated requirement. The trigger travels the wire, not the
  argv. No `sh -c`; the environment is the harness's existing authenticated-HOME
  inheritance. The nonce is an `int64` formatted with `%d`, so it cannot carry
  structure into the prompt.
- [Cryptographic primitives] No findings, and one note so a later reader does not
  "fix" it: the filename nonce is `time.Now().UnixNano()` because it needs
  **uniqueness**, not unpredictability — it defeats caching and keeps the absence walk
  from false-matching another run. It guards nothing, so `crypto/rand` would be noise.
  The batch id is the daemon's own nonce and is only echoed back; the test asserts it
  is non-empty and that it matches, never that it has a shape.
- [Network & I/O] No findings. No listener, no socket, no new connection. Every read
  is a deadline-bounded `ReceiveBytes` and the drain is bounded by one wall clock plus
  two counters, so a claude that never stops cannot drive unbounded work out of this
  test — it fails loud on whichever bound trips first, each with its own diagnostic.
- [Concurrency] No findings. The test spawns no goroutine and takes no lock. The two
  real hazards are stated in § Concurrency model: the sequential receive nonce (every
  `noise_msg` decrypted in order, non-`noise_msg` frames skipped without decrypting)
  and the detached dismissal fan-out, which the single order-agnostic drain refuses to
  assume away.
- [Threat model alignment] `docs/protocol-mobile.md` § Security model threat 1 —
  prompt injection reaching a render surface — applies to this file as a client of
  that surface. It renders nothing, and the log bound above is what bounds injected
  bytes reaching a salvaged log. **OUT OF SCOPE, named:** the per-device *denial* arm
  (a device paired without `--allow-remote-permissions` must not resolve a batch)
  stays with `questionResolverV2.admit`'s hermetic tests — this harness pairs **with**
  the flag precisely because the gate must pass for the round trip to be observable at
  all. Whether a claude that keeps reissuing past the caps ever terminates is a
  product question, deliberately not answered here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — implementation

**No design departure.** Every interface above landed as specified. Both SHOULD FIX
findings that constrain code landed: claude-authored bytes reach a message only
through `%q` and `truncateString` (the allowed modal's `Title`, the accumulated
continuation), and the trigger carries the bare base name `pyrycode-<nonce>.txt` with
no directory and no absolute path. The third — logging each allowed modal's title so
an unexpected allow is legible — landed in the allow arm's `t.Logf`.

One constant was dropped as redundant rather than declared: the plan implied a pair of
transcribed vocabulary constants, but `wantQuestionSource` (`"remote"`) already exists
in this package from the answer gate and spells the same `sourceQuestionRemote` value,
so only `wantRefusedOutcome` is new.

**All three open questions resolved by the observed run** (2026-09-02, first run of the
gate, `--- PASS` in 6.51s with exactly one `=== RUN` line):

1. **What claude does with `reasonQuestionRefused`: it stops.** It called
   `AskUserQuestion` with one question, four options, `multiSelect` false; the refusal
   produced `question_dismissed{refused, remote}`; it then raised no further permission
   modal, did not re-ask, wrote nothing, and replied with one sentence inviting the
   discussion the wording asked for before reaching terminal idle. The wording achieves
   what #1990 wrote it for.
2. **`perTurnReplyBudget` is ample.** The whole post-refusal settle took well under a
   second of the 120s budget; the turn end to end was 6.5s. No constant widened.
3. **`maxExtraRefusals` did not fire, nor did `maxAllowedModals`.** Both branches were
   untaken, as anticipated. They stay, for the reason the plan gave: a re-asked batch
   left outstanding parks the turn for ten minutes and yields a wall-clock diagnostic
   that names nothing.

**The limit this run does not clear, stated rather than smoothed over.** Because claude
stopped cleanly, the allow arm never fired, so the absence check's non-vacuity is
structural — had claude pressed on, its `Write` would have raised a modal this loop
would have allowed — rather than demonstrated by this run. There is no way to arrange a
run where the arm fires without breaking the behaviour under test. The file header
carries the same caveat where a reader of the test will meet it.

**Measured size.** The test file is 415 lines; with this spec the slice wrote ~770,
above the size-S 400-line boundary. That was found and recorded before the plan was
committed — the ticket carries `needs-human:sizing` and a comment with the measurement,
the split that was considered, and why it does not stand. The split-depth gate bars a
proposal (`parent 1987 grandparent 1907`), so the ticket was built as it stands.
