# interactive_stream_question_refusal_test.go

`interactive_stream_question_refusal_test.go` (#1995) — the refusal arm of the
`AskUserQuestion` round trip, standing beside
[interactive_stream_question_answer_test.go](e2e-realclaude-interactive-stream-question-answer-test-go.md)
the way `interactive_stream_permission_deny_test.go` stands beside
`interactive_stream_modal_resolution_test.go`: same harness, same trigger scaffold,
opposite verdict. `RefuseQuestion` (#1990) resolves a refused batch as a deny
carrying a fixed instruction (`reasonQuestionRefused`) rather than a reason
string — written because a bare deny leaves claude free to answer its own
question and press on. The hermetic tier proves the deny goes out with those
bytes; only a live claude can show what it does with them.

`TestInteractiveStreamQuestionRefusal` drives a real claude to call
`AskUserQuestion`, refuses the surfaced batch through the daemon's own inbound
`question_refused` path from a paired phone, and requires attribution — a
`question_dismissed` for that batch carrying `outcome=refused`, `source=remote`
— before checking that the blocked work was not done.

**Observed 2026-09-02, first run: claude stops.** It called `AskUserQuestion`
with one question and four options, the refusal produced
`question_dismissed{refused, remote}`, and claude then raised no further
permission modal, did not re-ask, wrote nothing, and replied with one sentence
inviting the discussion the wording asked for. `reasonQuestionRefused` achieves
what #1990 wrote it for. This is a standing real-claude gate in preship, not a
deterministic oracle — a future model reasoning differently about the deny is
what it exists to catch, and the observation above is the baseline it would be
caught against.

## Lessons that outlive this ticket

- **An absence check on this harness is vacuous unless the follow-up modals
  are ALLOWED, not rejected.** The instinct coming from the deny arm is to
  keep rejecting. But a `Write` is itself permission-gated here — that is why
  `writeFileTrigger` raises a modal at all — so under a rejecting loop the
  artefact's absence would be guaranteed by the permission gate whether or not
  the refusal did anything. `requireTriggerFileAbsent` only binds as a check
  on the refusal once the loop auto-approves what it meets
  (`turnevent.PermissionOptionKindAllowOnce`, capped by `maxAllowedModals`), so
  a claude that guessed and pressed on genuinely could have produced the file.
- **A successful refusal leaves that non-vacuity arm structurally
  unexercised, not fixably so.** Claude stopped cleanly, so zero modals were
  raised and `maxAllowedModals` was never approached. There is no run that
  demonstrates the arm firing without the model failing to honour the
  refusal — pressing on is the failure mode, not a step on the way to a
  stronger pass. The strongest available evidence is "the arm was in place and
  would have allowed it," and a reader meeting `modalsAllowed == 0` in a green
  run should read that as this structural gap, not as dead code.
- **A re-asked batch left outstanding parks the turn for the full ten-minute
  approval window**, producing a wall-clock diagnostic that names nothing.
  `settleRefusedTurn` refuses every re-asked batch under its own
  `maxExtraRefusals` cap for the same reason `denyModalsUntilIdle` answers
  every retried tool call in the deny arm: keeping the turn moving on the
  test's own explicit actions keeps the failure diagnostic specific instead of
  degrading to the shared wall clock.
- **One assertion reused across the file's two dismissal vocabularies is a
  latent coupling, flagged but not unwound.** `wantQuestionSource` (`"remote"`)
  is transcribed once, from the answer gate, and this file's `modal_dismissed`
  assertion reuses it rather than declaring a second constant for the same
  literal. It is correct today because both vocabularies share the value, but
  a rename on either side would silently redefine what the other's check
  means — worth separating on the next touch of this file rather than now.

See `docs/specs/architecture/1995-live-question-refusal-round-trip.md` for the
full design and security review, including why the allow arm's exposure is
contained by four deterministic bounds (the count cap, the `Class ==
"permission"` assertion, the harness's isolated authenticated HOME, and a
bounded log of every allowed modal's title) rather than by matching on
claude's chosen continuation.
