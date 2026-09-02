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
//   - maxAllowedModals and maxExtraRefusals were both untaken for the same reason.
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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
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

// The two bounds on the post-refusal drain, deliberately SEPARATE from the wall clock
// so a non-terminating turn fails loud with a diagnostic that names which way it
// failed to terminate. It is not proven that a refused claude gives up after any
// bounded number of follow-ups — whether a refused turn always terminates is a
// product question to raise separately, not a flake to widen a cap for.
const (
	maxAllowedModals = 4 // permission modals auto-approved after the refusal
	maxExtraRefusals = 2 // re-asked batches refused after the first
)

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

// refusalObservation is what the drain SAW, as distinct from what it asserted. AC 3
// asks for a measured observation of how claude got to a terminal state — stopped,
// re-asked, or something else — and pinning a stochastic choice as an assertion would
// turn a legitimate change of strategy into a red. So these are counted, logged and
// carried into the file header; only the milestones in settleRefusedTurn's doc block
// are enforced.
type refusalObservation struct {
	modalsAllowed int
	extraRefusals int
	continuation  string
}

// settleRefusedTurn drives the post-refusal wire to its end and reports what it saw.
// It enforces two milestones and actuates on two frame kinds.
//
// The milestones:
//
//   - ATTRIBUTION (AC 1): a question_dismissed for THIS batch carrying source
//     "remote" and outcome "refused". That conjunction is the only thing pinning the
//     resolution to the frame the test sent; the backstop's {unanswered, no_answer}
//     is what a refusal that never produced a verdict looks like from out here and is
//     rejected loudly. Every OTHER dismissal seen — a re-asked batch this loop
//     refused — is held to the same pair, so attribution holds for every batch rather
//     than only the first.
//   - TERMINATION (AC 3): the terminal turn_state{idle} for convID.
//
// The actuating arms, each bounded by a counter whose diagnostic is distinct from the
// wall clock's:
//
//   - modal_shown{permission} is ALLOWED with a fresh answer token and request id.
//     This is what makes the caller's absence check non-vacuous; the header's second
//     section carries the full argument. A non-permission modal is a hard fail rather
//     than an auto-approval.
//   - question_shown is a RE-ASK, and is refused with a fresh token and request id. A
//     re-asked batch left outstanding parks the turn until the daemon's approval
//     window elapses (ten minutes by default), which overruns this drain's budget and
//     yields a wall-clock diagnostic naming nothing. Refusing keeps the turn moving by
//     our explicit refusals and keeps the diagnostic specific.
//
// IT IS ONE DRAIN AND NOT SEVERAL, the sibling answer gate's design decision and for
// its reason: RefuseQuestion fans the dismissal out ON ITS OWN GOROUTINE (its doc
// block says why — the caller runs on the relay manager's Run goroutine and broadcast
// funnels back onto it) while anything claude does next travels claude → parser →
// emitter. A dismissal drain followed by a turn drain would assume an order it need
// not win and would eat frames silently. This loop assumes no order.
//
// The frame loop is the package's standing one: read binary→phone frames in receive
// order, decrypt EVERY noise_msg to keep the sequential receive nonce in sync, and
// skip non-noise_msg control frames WITHOUT decrypting. A TypeError envelope at any
// point is a hard fail, as in the sibling drains.
//
// Accepting the first idle-for-conv as terminal is sound for the deny arm's reason:
// this drain is sequenced AFTER raiseRealQuestionBatch, which consumed the leading
// turn_state{responding} and any pre-turn resting idle in order, so no earlier idle is
// left on the wire.
//
// Claude-authored bytes — a modal title here, the accumulated text in the caller's
// failure path — reach a message only through %q and truncateString: nothing on this
// path strips terminal escapes and the pipeline salvages run logs. The batch id and
// the modal id are daemon-asserted and logged plain.
func settleRefusedTurn(t *testing.T, h *perConvHarness, convID, batchID string, startReqID uint64, timeout time.Duration) refusalObservation {
	t.Helper()
	var (
		obs          refusalObservation
		sawDismissal bool
		text         strings.Builder
	)
	reqID := startReqID
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if !sawDismissal {
				t.Fatalf("no question_dismissed for batch %s within %s — the refusal never resolved the "+
					"batch: it was denied at the device gate, lost the one-shot to the backstop, or never "+
					"reached the resolver at all", batchID, timeout)
			}
			t.Fatalf("batch %s was dismissed as refused, but the turn for %q never reached terminal "+
				"turn_state{idle} within %s (allowed %d modal(s), refused %d re-asked batch(es)) — claude "+
				"neither stopped nor finished after being told to stop and wait",
				batchID, convID, timeout, obs.modalsAllowed, obs.extraRefusals)
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the milestone-specific t.Fatalf above
			}
			t.Fatalf("phone receive (refused-turn drain): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (refused-turn drain): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			// A non-noise_msg control frame (e.g. rekey) does not advance the receive
			// nonce — skip without decrypting.
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (refused-turn drain): %v", err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (refused-turn drain): %v", err)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope while draining the refused turn: %s", string(env.Payload))

		case protocol.TypeQuestionDismissed:
			var dis protocol.QuestionDismissedPayload
			if err := json.Unmarshal(env.Payload, &dis); err != nil {
				t.Fatalf("decode question_dismissed payload: %v", err)
			}
			if dis.Source != wantQuestionSource {
				t.Fatalf("question_dismissed for batch %s carried Source = %q, want %q — the resolution "+
					"must be attributable to our explicit refusal, not to the no-answer backstop or any "+
					"other path", dis.QuestionBatchID, dis.Source, wantQuestionSource)
			}
			if dis.Outcome != wantRefusedOutcome {
				t.Fatalf("question_dismissed for batch %s carried Outcome = %q, want %q — %q is "+
					"retireQuestion's backstop reporting that the approval window elapsed with no verdict, "+
					"which is exactly what a refusal the daemon never applied looks like from here",
					dis.QuestionBatchID, dis.Outcome, wantRefusedOutcome, dis.Outcome)
			}
			if dis.QuestionBatchID == batchID {
				sawDismissal = true
				t.Logf("question_dismissed{%s, %s} for batch %s — the resolution is attributable to our refusal",
					dis.Outcome, dis.Source, batchID)
			}

		case protocol.TypeQuestionShown:
			var shown protocol.QuestionShownPayload
			if err := json.Unmarshal(env.Payload, &shown); err != nil {
				t.Fatalf("decode re-asked question_shown payload: %v", err)
			}
			if shown.QuestionBatchID == "" {
				t.Fatal("re-asked question_shown carried an empty question_batch_id — nothing could refuse it")
			}
			if shown.QuestionBatchID == batchID {
				continue // the batch already refused; its one-shot is consumed
			}
			if obs.extraRefusals >= maxExtraRefusals {
				t.Fatalf("claude re-asked more than %d time(s) for %q without reaching idle — it keeps "+
					"reissuing AskUserQuestion past the cap; whether a refused turn always terminates is a "+
					"product question to raise separately, not a test flake to widen the cap for",
					maxExtraRefusals, convID)
			}
			obs.extraRefusals++
			sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
				ID:   reqID,
				Type: protocol.TypeQuestionRefused,
				TS:   time.Now().UTC(),
				Payload: mustJSON(t, protocol.QuestionRefusedPayload{
					QuestionBatchID: shown.QuestionBatchID,
					AnswerToken:     fmt.Sprintf("e2e-1995-reask-token-%d", reqID),
				}),
			})
			t.Logf("refused re-asked batch %s (%d question(s), refusal #%d) for %q",
				shown.QuestionBatchID, len(shown.Questions), obs.extraRefusals, convID)
			reqID++

		case protocol.TypeModalShown:
			var shown protocol.ModalShownPayload
			if err := json.Unmarshal(env.Payload, &shown); err != nil {
				t.Fatalf("decode modal_shown payload (refused-turn drain): %v", err)
			}
			if shown.Class != "permission" {
				t.Fatalf("modal_shown Class = %q, want %q — this loop auto-approves what it meets, so a "+
					"trust or onboarding modal must fail here rather than be allowed", shown.Class, "permission")
			}
			if shown.ModalID == "" {
				t.Fatal("modal_shown carried an empty modal_id")
			}
			if obs.modalsAllowed >= maxAllowedModals {
				t.Fatalf("claude raised more than %d permission modal(s) for %q after the refusal without "+
					"reaching idle — it keeps reissuing gated tool calls past the cap; that is a product "+
					"question about a refused turn, not a test flake to widen the cap for",
					maxAllowedModals, convID)
			}
			obs.modalsAllowed++
			sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
				ID:   reqID,
				Type: protocol.TypeModalAnswer,
				TS:   time.Now().UTC(),
				Payload: mustJSON(t, protocol.ModalAnswerPayload{
					ModalID:     shown.ModalID,
					OptionID:    string(turnevent.PermissionOptionKindAllowOnce),
					AnswerToken: fmt.Sprintf("e2e-1995-allow-token-%d", reqID),
				}),
			})
			// The title is TUI-authored text crossing the subprocess trust boundary;
			// it is logged bounded and quoted so an allow of something unexpected is
			// legible in a salvaged run record rather than silent.
			t.Logf("ALLOWED permission modal %q (allow #%d) for %q, title %q — a claude that guessed an "+
				"answer and pressed on could genuinely produce the artefact",
				shown.ModalID, obs.modalsAllowed, convID,
				truncateString(shown.Title, questionLabelLogCap))
			reqID++

		case protocol.TypeModalDismissed:
			var dis protocol.ModalDismissedPayload
			if err := json.Unmarshal(env.Payload, &dis); err != nil {
				t.Fatalf("decode modal_dismissed payload (refused-turn drain): %v", err)
			}
			if dis.Source != wantQuestionSource {
				t.Fatalf("modal_dismissed Source = %q, want %q — a modal resolved by the daemon's approval "+
					"timeout means our allow was dropped and the gate fell closed on its own, which would "+
					"make the absence check vacuous again", dis.Source, wantQuestionSource)
			}

		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload (refused-turn drain): %v", err)
			}
			if p.ConversationID == convID && strings.TrimSpace(p.Text) != "" {
				text.WriteString(p.Text)
			}

		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload (refused-turn drain): %v", err)
			}
			if st.State != "idle" || st.ConversationID != convID {
				continue
			}
			// Terminal. Attribution must already hold: an idle with no dismissal for
			// our batch means the turn ended without the refusal ever resolving it.
			if !sawDismissal {
				t.Fatalf("turn for %q reached terminal idle with no question_dismissed for batch %s — the "+
					"turn ended without the refusal ever resolving the batch", convID, batchID)
			}
			obs.continuation = text.String()
			t.Logf("terminal turn_state{idle} for %q after the refusal; claude said %d byte(s): %q",
				convID, len(obs.continuation), truncateString(obs.continuation, questionTextLogCap))
			return obs
		}
	}
}
