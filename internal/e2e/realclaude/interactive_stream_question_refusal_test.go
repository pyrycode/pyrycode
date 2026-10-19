//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamQuestionRefusal is the #1995 deliverable (split from #1987,
// itself split from #1907): one clarifying-question batch driven out of a live
// claude, REFUSED through the daemon's own inbound question_refused path from a
// paired phone, and asserted to produce the behaviour the deny wording was written
// for rather than merely the deny.
//
// It is the refusal arm of interactive_stream_question_answer_test.go and stands in
// the same relation to it as interactive_stream_permission_deny_test.go does to
// interactive_stream_modal_resolution_test.go: same harness, same trigger scaffold,
// opposite verdict, and a behaviour on the far side the other arm's run cannot tell
// you. #1990's RefuseQuestion resolves a refused batch as a deny carrying a fixed
// INSTRUCTION rather than a reason string (reasonQuestionRefused: do not answer it
// yourself, do not assume an answer, do not continue with the work it was blocking,
// stop and wait). The hermetic tier proves the deny goes out with those bytes. Only a
// live claude can show what it does with them, and until this ran nothing had.
//
// # MEASURED: what claude actually did
//
// Observed 2026-09-02, the first run of this gate, under askQuestionCaptureModel.
// Claude called AskUserQuestion with one question offering four options and
// multiSelect false; the refusal produced question_dismissed{refused, remote} for that
// batch; and then CLAUDE STOPPED. It raised no further permission modal, did not
// re-ask, wrote nothing, and replied with a single sentence inviting the discussion
// the wording asked for — "I'll hold off — what would you like to discuss about the
// eviction policy choice?" — before the turn reached terminal idle. Whole turn: 6.5s.
// So reasonQuestionRefused achieves what #1990 wrote it for: claude neither answered
// its own question nor continued into the work it was blocking.
//
// TWO HONEST LIMITS ON THAT OBSERVATION, worth more to a later reader than the result:
//
//   - Because claude stopped cleanly, the allow arm below NEVER FIRED — zero modals
//     were raised after the refusal. The absence check's non-vacuity is therefore
//     structural rather than demonstrated by this run: had claude pressed on, its
//     Write would have raised a permission modal (writeFileTrigger's whole premise on
//     the stream path) and this loop would have allowed it. A run in which the arm
//     fires and the file still does not appear would be strictly stronger evidence,
//     and there is no way to arrange one without breaking the behaviour under test.
//   - The permission and re-ask caps were both untaken for the same reason.
//     They stay: without the re-ask arm a re-asked batch parks the turn until the
//     daemon's ten-minute approval window elapses, and the resulting wall-clock
//     diagnostic would name nothing.
//
// This is a standing real-claude gate in preship, not a deterministic RED/GREEN
// oracle. A future model that reasons differently about the deny is the thing it
// exists to catch, and the sentence above is the baseline it would be caught against.
//
// # Why the absence check ALLOWS the modals it meets
//
// The natural proof that claude did not press on with the blocked work is that the
// work left no trace — requireTriggerFileAbsent finds nothing. The obvious version of
// that check is vacuous: a Write is itself permission-gated on this harness (it is
// the whole reason writeFileTrigger raises a modal), so absence would be guaranteed
// by the permission gate whether or not the refusal did anything. The check binds
// only if a permission modal raised after the refusal is ALLOWED, so that a claude
// which guessed an answer and pressed on genuinely could have produced the artefact.
// Same "different fabric" discipline requireTriggerFileAbsent was written under: a
// deterministic filesystem observable backing a stochastic one, never a second
// stochastic check. Gating the allow on the modal's CONTENT would put a matcher in
// the way and hand back a green whenever claude pressed on in a shape it did not
// recognise, which is the vacuity this arm exists to remove.
//
// What bounds that allow is therefore deterministic and not a matcher: a cap on how
// many modals are answered, the Class == "permission" assertion (so no trust or
// onboarding modal is ever auto-approved), the harness's isolated
// WithWorktreeAuthenticated HOME with the daemon workdir beneath it, and a bounded
// log of each allowed modal's title so an allow of something unexpected is legible in
// a salvaged run record instead of silent.
//
// # Why attribution is the load-bearing first assertion
//
// "Claude did not proceed" is also what a batch the approval window abandoned looks
// like, and what a frame dropped at the device gate looks like. Only a
// question_dismissed for THIS batch carrying source "remote" AND outcome "refused"
// pins the resolution to the frame this test sent. retireQuestion's backstop
// broadcasts {unanswered, no_answer} when the approval window elapses — exactly what
// a refusal that never produced a verdict looks like from out here — so it fails loud
// rather than counting as "the batch resolved".
//
// # What it does NOT drive
//
// No per-device DENIAL arm: this harness pairs WITH --allow-remote-permissions
// because questionResolverV2.admit applies the same MayAnswerRemotePermission() bit to
// a refusal as to an answer, and the gate must pass for the round trip to be
// observable at all. That arm stays with admit's hermetic tests. No client-supplied
// wording: QuestionRefusedPayload carries a batch id and an answer token and nothing
// else, and the deny message is a compile-time constant on the daemon side — a test
// that tried to send wording would be testing something that does not exist.
//
// # Running it
//
//	go test -tags e2e_realclaude -count=1 -v \
//	  -run '^TestInteractiveStreamQuestionRefusal$' ./internal/e2e/realclaude/
//
// It costs one live claude turn and needs real credentials; a skip without them is the
// correct outcome and carries no signal. READ THE COUNT OF TESTS EXECUTED, NEVER THE
// EXIT CODE: with no credentials every test skips and exits 0, and when the package
// fails to build zero tests run and it still exits 0 through a shell wrapper. `make
// check` never compiles this package. Count the `=== RUN` lines.
//
// This slice captures no artefact, so nothing needs `git add`ing beyond this file.

import (
	"fmt"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"testing"
	"time"
)

// wantRefusedOutcome is the dismissal outcome a refusal must produce, transcribed
// rather than imported for the reason the sibling answer gate records:
// outcomeQuestionRefused lives in cmd/pyry, package main, which a test package cannot
// import. A rename there does NOT reach this literal; the symptom is the attribution
// assertion below failing, which is the right place for a reader to notice the
// vocabulary moved. The source is the same value on both arms, so wantQuestionSource
// is reused rather than a second constant spelling "remote" declared beside it.
const wantRefusedOutcome = "refused"

// questionRefusalToken is the client-minted idempotency key on the refusal frame.
// QuestionRefusedPayload's doc block states the token is NOT the authorization (that
// is the device gate) and that the daemon's dedup is the one-shot consume of the
// batch id, so a fixed constant is correct rather than a shortcut.
const questionRefusalToken = "e2e-1995-refusal-token"

// questionRefusalTrigger drives claude to ask a question that GATES REAL WORK, which
// is the one place this file cannot reuse the sibling answer gate's trigger. That one
// ends "do not write code and do not use any other tool", deliberately, because it
// measured only the continuation's wording. This slice needs the opposite: the
// question must block work that a claude which guessed an answer would go on to do,
// and the artefact that work produces has to be the one requireTriggerFileAbsent
// walks for — base name pyrycode-<nonce>.txt, the contract writeFileTrigger already
// satisfies. A trigger whose blocked work writes some other name makes the absence
// check vacuous in a way that greens silently.
//
// Every clause is load-bearing:
//
//   - IT NAMES THE TOOL, legitimate for #1938's reason: this measures the round trip,
//     not claude's spontaneous propensity to reach for the tool.
//   - IT ASKS FOR SINGLE SELECTION, which is what makes claude emit the multiSelect
//     key at all. questionbridge.Parse REJECTS a question whose key is absent, and the
//     rejection is invisible AS a rejection: the batch falls through to a permission
//     modal and the symptom is a deadlined question_shown drain whose message names
//     modals. Four options sit inside Parse's 2-4 bound.
//   - IT GATES THE WRITE ON THE ANSWER, so pressing on has somewhere concrete to go.
//   - IT CARRIES THE BARE BASE NAME AND NO PATH. The sibling trigger carried no
//     filename at all; this one must, so the constraint tightens to the base name —
//     no directory, no absolute path, no "in this repo" — which keeps a
//     claude-authored question from quoting the worktree path (under /var/folders/ on
//     macOS) into a salvaged run log.
//
// The nonce keeps the trigger distinct per run (defeats accidental caching) and makes
// the absence walk unambiguous: no other run's file can false-match.
func questionRefusalTrigger(nonce int64) string {
	return fmt.Sprintf("Before you do anything else, use the AskUserQuestion tool to ask me one "+
		"clarifying question: for an in-memory key-value cache, which eviction policy should it use? "+
		"Give the question a short header, offer four policies as options with a one-line description "+
		"each, and allow only a single choice. Once I have answered, use the Write tool to create a "+
		"file named pyrycode-%d.txt whose only contents are the exact label of the policy I chose. "+
		"run=%d", nonce, nonce)
}

func TestInteractiveStreamQuestionRefusal(t *testing.T) {
	runParallel(t)
	// The #1154 harness under the #1987 model: it pairs the phone WITH
	// --allow-remote-permissions (questionResolverV2.admit gates a refusal on the
	// same MayAnswerRemotePermission() bit the answer path does — without it the
	// frame denies at the gate, nothing is dismissed, and the drain deadlines on a
	// batch that is simply still outstanding), writes the stream-json toggle, and
	// spawns WITHOUT --dangerously-skip-permissions so the tool call genuinely parks.
	h, convID := startStreamModalResolutionHarness(t, askQuestionCaptureModel)
	nonce := time.Now().UnixNano()

	// AC 1, first half: a real batch surfaced, and non-vacuous BEFORE anything is
	// refused. Reused verbatim — a batch that never surfaced deadlines this drain
	// rather than letting a later assertion pass over nothing.
	batch := raiseRealQuestionBatch(t, h, 2, convID, questionRefusalTrigger(nonce))

	// AC 1, second half: refuse through the daemon's own inbound path. The payload
	// carries two ids and nothing else — there is no wording to send.
	// handleQuestionRefusal emits no reply and no broadcast of its own, so the
	// dismissal below is the only thing this can correlate on.
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   3,
		Type: protocol.TypeQuestionRefused,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.QuestionRefusedPayload{
			QuestionBatchID: batch.QuestionBatchID,
			AnswerToken:     questionRefusalToken,
		}),
	})

	// AC 1's attribution and AC 3's terminal turn, in one drain. The trigger used
	// request id 2 and the refusal 3, so follow-up frames start at 4.
	const firstFollowUpReqID uint64 = 4
	obs := settleRefusedTurn(t, h, convID, batch.QuestionBatchID, firstFollowUpReqID, perTurnReplyBudget)
	requirePromptAnswerHistory(t, h, convID, batch.QuestionBatchID, "refusal", false)

	// AC 2: claude did not perform the work the question was blocking. Checked AFTER
	// idle so the tool phase is definitively over, and non-vacuous because every
	// permission modal along the way was ALLOWED.
	requireTriggerFileAbsent(t, h.workdir, nonce)

	// AC 3's measured observation, recorded for the run log; the header carries what
	// it said the first time this ran.
	t.Logf("measured: after the refusal claude allowed %d permission modal(s), re-asked %d time(s), "+
		"and produced %d byte(s) of text before the turn reached idle",
		obs.modalsAllowed, obs.extraRefusals, len(obs.continuation))
}

// Compatibility wrapper keeps the live refusal's filesystem witness in its caller.
type refusalObservation struct {
	modalsAllowed, extraRefusals int
	continuation                 string
}

func settleRefusedTurn(t *testing.T, h *perConvHarness, convID, batchID string, startReqID uint64, timeout time.Duration) refusalObservation {
	t.Helper()
	obs := scenario.RefusedTurn(t, modalScenarioWire(t, h, startReqID), convID, batchID, timeout)
	return refusalObservation{modalsAllowed: obs.ModalsAllowed, extraRefusals: obs.ExtraRefusals, continuation: obs.Continuation}
}
