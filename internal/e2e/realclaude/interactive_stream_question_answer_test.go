//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamQuestionAnswer is the #1987 deliverable (split from #1907):
// one clarifying-question batch driven out of a live claude, answered through the
// daemon's own inbound question_answer path from a paired phone, and asserted to
// have reached claude as ANSWERS rather than as a bare allow.
//
// Every leg of that path is merged and is called rather than rebuilt: #1983's wire
// types, #1984's interception, #1986's per-device gate, #1991's AnswerQuestion and
// answerVerdict. #1938 pinned the OUTBOUND half against measured bytes (the call is
// committed as testdata/ask_user_question_v2.1.239.json); the answer half has never
// been observed live and until now was built entirely from the vendor page.
//
// # Why "the turn continued" is not the assertion
//
// answerVerdictInput's own doc block records the failure this gate exists to catch:
// a verdict assembled by re-marshalling the daemon's wire-shaped batch instead of
// splicing claude's own bytes hands claude a key it does not read (multi_select
// where claude writes multiSelect) — silently, because the call is allowed either
// way. A test asserting only that claude proceeded goes green against exactly that.
// So the load-bearing assertion is that the ANSWERS arrived, and the way to get it
// is for the test's choice to be unpredictable from the trigger: the prompt names no
// option, the test picks from the batch it was actually shown, and the continuation
// must name that pick FIRST among the offered labels (requireContinuationNamesChoice
// says why first-among rather than merely present).
//
// Attribution is the second non-vacuity leg, and it is the lesson
// interactive_stream_permission_deny_test.go recorded: outcomes that look like ours
// are also produced by paths that have nothing to do with our frame. Only a
// question_dismissed carrying source "remote" AND outcome "answered", for the batch
// that was actually surfaced, pins the resolution to the frame this test sent — a
// backstop dismissal (retireQuestion's "unanswered") or any other source means the
// answer never produced a verdict.
//
// # The model, and why it is not the harness default
//
// spawnPermissionDaemon has always hardcoded --model haiku, and #1987 made it a
// parameter for this file. This gate runs under askQuestionCaptureModel — the model
// #1938 used, and the only one under which a live AskUserQuestion call has ever been
// measured in this tree. Its reasoning transfers verbatim: tool-selection
// reliability is worth more than the token delta, because a model that will not
// reach for the tool DEADLINES the question_shown drain rather than failing with a
// useful message. The constant is reused rather than a second one minted, so the
// package's two live AskUserQuestion runs cannot drift apart. The two modal gates
// keep passing permissionDaemonModel and are unchanged.
//
// # What it does NOT drive
//
// No partial answers map: answerVerdict rejects an entry count that is not exactly
// the parked question count, checked before anything is assembled, so driving one
// live would spend real tokens measuring the daemon's own validator — every reject
// branch is already pinned hermetically by TestStreamApprovalBridge_AnswerQuestion_RejectPaths.
// No refusal arm (#1995). No per-device denial arm — that stays with
// questionResolverV2.admit's hermetic tests; this harness pairs WITH
// --allow-remote-permissions precisely because the gate must pass for the round trip
// to be observable at all.
//
// # Running it
//
//	go test -tags e2e_realclaude -count=1 -v \
//	  -run '^TestInteractiveStreamQuestionAnswer$' ./internal/e2e/realclaude/
//
// It costs one live claude turn and needs real credentials; a skip without them is
// the correct outcome and carries no signal. READ THE COUNT OF TESTS EXECUTED, NEVER
// THE EXIT CODE: with no credentials every test skips and exits 0, and when the
// package fails to build zero tests run and it still exits 0 through a shell
// wrapper. `make check` never compiles this package. Count the `=== RUN` lines.
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
)

// The dismissal vocabulary this gate asserts, transcribed rather than imported:
// outcomeQuestionAnswered and sourceQuestionRemote live in cmd/pyry, package main,
// which a test package cannot import. A rename there does NOT reach these literals;
// the symptom is this file's attribution assertion failing, which is the right place
// for a reader to notice the vocabulary moved.
const (
	wantQuestionOutcome = "answered"
	wantQuestionSource  = "remote"
)

// questionAnswerToken is the client-minted idempotency key on the answer frame. A
// fixed constant is correct and not a shortcut: QuestionAnswerPayload's doc block
// states the token is NOT the authorization (that is the device gate) and that the
// daemon's dedup is the one-shot consume of the batch id.
const questionAnswerToken = "e2e-1987-answer-token"

const questionModelUpdateBudget = 30 * time.Second

// questionSurfaceBudget is the budget for a real clarifying-question batch to
// surface after the trigger send: cold claude (spawn + model load) reaching the
// AskUserQuestion call, `pyry mcp-approve` parking it, questionbridge.Parse
// accepting it and surfaceQuestion broadcasting question_shown. Longer than the
// sibling modalSurfaceBudget because this gate runs under a larger model than the
// modal gates do.
const questionSurfaceBudget = 180 * time.Second

// questionAnswerTrigger drives claude to ask. It is askQuestionCapturePrompt's shape
// with #1938's three constraints carried and one addition, and every one of them is
// load-bearing rather than stylistic:
//
//   - IT NAMES THE TOOL, legitimate for #1938's reason: this slice measures the
//     round trip, not claude's spontaneous propensity to reach for the tool.
//   - IT ASKS FOR SINGLE SELECTION, which is what makes claude emit the multiSelect
//     key at all. questionbridge.Parse REJECTS a question whose key is absent, and a
//     rejected batch falls through to a permission modal this test never answers, so
//     the turn would park until the approval window elapsed. Four options rather
//     than the capture's two widen the space a continuation would have to guess
//     from; both counts sit inside Parse's 2-4 bound.
//   - IT CARRIES NO PATH, NO FILENAME AND NO "in this repo". #1938 needed that for
//     its deny-scan; here it keeps a claude-authored question from quoting the
//     worktree path — under /var/folders/ on macOS — into a salvaged run log.
//   - IT NAMES NO OPTION AND NO PREFERENCE. That is AC 4: the choice must be
//     unpredictable from the prompt. What it does add is the continuation
//     instruction, which cannot leak the choice because the labels do not exist
//     until claude writes them.
//
// The nonce keeps the trigger distinct per run (defeats accidental caching) and is
// never asserted on.
func questionAnswerTrigger(nonce int64) string {
	return fmt.Sprintf("Before writing anything, use the AskUserQuestion tool to ask me one "+
		"clarifying question: for an in-memory key-value cache, which eviction policy should it use? "+
		"Give the question a short header, offer four policies as options with a one-line description "+
		"each, and allow only a single choice. After I answer, reply with only the exact label of the "+
		"option I chose and nothing else — do not write code and do not use any other tool. run=%d", nonce)
}

func TestInteractiveStreamQuestionAnswer(t *testing.T) {
	// The #1154 harness, unchanged except for the model: it pairs the phone WITH
	// --allow-remote-permissions (questionResolverV2.admit gates on the same
	// MayAnswerRemotePermission() bit the modal path does), writes the stream-json
	// toggle, and spawns WITHOUT --dangerously-skip-permissions so claude's tool
	// call genuinely parks on the approve path.
	h, convID := startStreamModalResolutionHarness(t, askQuestionCaptureModel)
	nonce := time.Now().UnixNano()

	// AC 1: a real batch surfaced, and non-vacuous BEFORE anything is answered.
	batch := raiseRealQuestionBatch(t, h, 2, convID, questionAnswerTrigger(nonce))

	// Change the live child's model while AskUserQuestion is parked. The reply
	// proves the daemon accepted the update; the original batch id remains the
	// subject of the answer and dismissal below, which proves it was not replaced.
	targetModel := "haiku"
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   3,
		Type: protocol.TypeSetSessionSettings,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SetSessionSettingsPayload{
			SessionID: streamModalBootstrapUUID,
			Model:     &targetModel,
		}),
	})
	updated := drainForControlEvent(t, h.phone, h.initRecv, protocol.TypeSessionSettingsUpdated, questionModelUpdateBudget)
	if updated.InReplyTo == nil || *updated.InReplyTo != 3 {
		t.Fatalf("session_settings_updated in_reply_to = %v, want 3", updated.InReplyTo)
	}
	var updatedPayload protocol.SessionSettingsUpdatedPayload
	if err := json.Unmarshal(updated.Payload, &updatedPayload); err != nil {
		t.Fatalf("decode session_settings_updated: %v", err)
	}
	if updatedPayload.SessionID != streamModalBootstrapUUID {
		t.Fatalf("session_settings_updated session_id = %q, want %q", updatedPayload.SessionID, streamModalBootstrapUUID)
	}

	// AC 4's first half: the choice is made from the surfaced batch.
	entries, choice := chooseQuestionAnswers(t, batch)

	// AC 2: answer through the daemon's own inbound path. handleQuestionAnswer
	// emits no reply and no broadcast of its own, so there is nothing to correlate
	// on — the dismissal below is what reports the outcome.
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   4,
		Type: protocol.TypeQuestionAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.QuestionAnswerPayload{
			QuestionBatchID: batch.QuestionBatchID,
			AnswerToken:     questionAnswerToken,
			Answers:         entries,
		}),
	})

	// AC 2's attribution and AC 3's completed turn, in one drain.
	text := drainAnsweredQuestionTurn(t, h, convID, batch.QuestionBatchID, perTurnReplyBudget)

	// AC 4's second half: the continuation shows claude read the answers.
	requireContinuationNamesChoice(t, text, choice)

	// A fresh application turn proves the parked-question update changed the
	// child's model rather than merely persisting it and reporting success.
	sealSendMessage(t, h.phone, h.initSend, 5, convID, "m-after-model-change",
		fmt.Sprintf("Reply with one short word. run=%d after-model-change", nonce))
	announced, seen := drainForAnnouncedModel(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
	if !seen {
		t.Fatalf("the post-question turn emitted no model_announced frame")
	}
	if want := announcedTargetFor(t, targetModel); announced.Model != want {
		t.Errorf("post-question model = %q, want %q after set_model %q", announced.Model, want, targetModel)
	}
}

// --- surfacing --------------------------------------------------------------

// raiseRealQuestionBatch sends triggerPrompt, drains the wire to the resulting
// question_shown broadcast and asserts the batch is ANSWERABLE before returning it:
// a non-empty batch id, at least one question, and at least two options on the focus
// question (index 0 — the one whose label the continuation is checked for). This is
// AC 1's non-vacuity gate, and it sits before the answer for the reason
// raiseRealPermissionModal's does: a batch that never surfaced must deadline the
// drain, and one that surfaced empty must fail here rather than let a later
// assertion pass over nothing.
//
// drainForControlEvent is reused unchanged — it is generic on the wanted envelope
// type. Its deadline message names modals because it predates this frame family; on
// this call the two readings are that claude never called AskUserQuestion under this
// model, or that questionbridge.Parse rejected the batch (an absent multiSelect, or
// counts outside its bounds) and it fell through to a permission modal nothing here
// answers.
//
// It logs the batch's SHAPE and never its content: counts, the multi_select flag per
// question (the ticket asks what a real run produced) and the daemon-asserted batch
// id, which QuestionDismissedPayload's doc block marks safe. Question text, headers,
// labels and descriptions are claude-authored bytes that crossed the subprocess
// trust boundary; nothing on this path strips terminal escapes and the pipeline
// salvages run logs, so where a label must be printed at all it goes through %q.
func raiseRealQuestionBatch(t *testing.T, h *perConvHarness, reqID uint64, convID, triggerPrompt string) protocol.QuestionShownPayload {
	t.Helper()
	sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID), triggerPrompt)
	env := drainForControlEvent(t, h.phone, h.initRecv, protocol.TypeQuestionShown, questionSurfaceBudget)

	var batch protocol.QuestionShownPayload
	if err := json.Unmarshal(env.Payload, &batch); err != nil {
		t.Fatalf("decode question_shown payload: %v", err)
	}
	if batch.QuestionBatchID == "" {
		t.Fatal("question_shown carried an empty question_batch_id — no dismissal could ever correlate to it")
	}
	if len(batch.Questions) == 0 {
		t.Fatal("question_shown carried no questions — an unanswerable batch, and a vacuous green if tolerated")
	}
	if got := len(batch.Questions[0].Options); got < 2 {
		t.Fatalf("question_shown's first question offers %d option(s), want at least 2 — a batch with one "+
			"option cannot show that a CHOICE reached claude", got)
	}
	for i, q := range batch.Questions {
		t.Logf("question_shown %s: question %d has %d options, multi_select=%v",
			batch.QuestionBatchID, i, len(q.Options), q.MultiSelect)
	}
	return batch
}

// --- the choice -------------------------------------------------------------

// questionChoice is what the continuation is checked against: the label the test
// picked for the focus question, and the labels it did not pick. Both come from the
// surfaced batch — nothing here is derived from the trigger prompt.
type questionChoice struct {
	chosen   string
	unchosen []string
}

// chooseQuestionAnswers builds the answer entries and reports the focus question's
// choice.
//
// ONE ENTRY PER QUESTION, not just the focus one. answerVerdict rejects an answer
// whose entry count is not exactly the parked question count — checked first, before
// anything is assembled — so a batch with two questions answered once would redden
// on the daemon's own validator instead of on the behaviour under test. The prompt
// asks for one question; this does not assume it got one.
//
// THE LAST OPTION IS CHOSEN. The choice has to be unpredictable from the prompt (AC
// 4), and last is not the position a continuation that never read the answers would
// lead with — which is exactly what requireContinuationNamesChoice's first-mention
// rule turns on.
//
// One value per entry, which is legal for a single-select question and equally legal
// for a multiSelect one: answerVerdict decides bare-string vs. array from the parked
// question's MultiSelect alone, never from how many values arrived. Values are NOT
// checked against the offered labels anywhere in the daemon (claude's contract
// permits free text), so sending claude's own label is a choice this test makes to
// keep the continuation checkable, not a constraint it is under.
func chooseQuestionAnswers(t *testing.T, batch protocol.QuestionShownPayload) ([]protocol.QuestionAnswerEntry, questionChoice) {
	t.Helper()
	entries := make([]protocol.QuestionAnswerEntry, 0, len(batch.Questions))
	var choice questionChoice
	for i, q := range batch.Questions {
		if len(q.Options) == 0 {
			t.Fatalf("question %d of batch %s offers no options — nothing can be chosen for it, and an "+
				"entry count short of the question count is rejected before it reaches claude",
				i, batch.QuestionBatchID)
		}
		last := len(q.Options) - 1
		entries = append(entries, protocol.QuestionAnswerEntry{
			QuestionIndex: i,
			Values:        []string{q.Options[last].Label},
		})
		if i == 0 {
			choice.chosen = q.Options[last].Label
			for j, o := range q.Options {
				if j != last {
					choice.unchosen = append(choice.unchosen, o.Label)
				}
			}
		}
	}
	t.Logf("answering batch %s with %d entr(ies); the focus question's chosen label is %q",
		batch.QuestionBatchID, len(entries), truncateString(choice.chosen, questionLabelLogCap))
	return entries, choice
}

// questionLabelLogCap bounds a claude-authored label before it is printed. The
// labels observed are short, but their length is claude's to decide and a run log
// this pipeline salvages is not the place to find out how long they can get.
const questionLabelLogCap = 256

// questionTextLogCap bounds the accumulated continuation before it is printed. Wider
// than a label — a failure message that cut the reply to a few words would not show
// what claude actually said — and still bounded, for the same reason.
const questionTextLogCap = 2048

// --- the answered turn ------------------------------------------------------

// drainAnsweredQuestionTurn drives the post-answer wire to its end and returns the
// accumulated continuation text. It proves three things at once:
//
//   - ATTRIBUTION (AC 2): a question_dismissed for THIS batch carrying source
//     "remote" and outcome "answered". Those two together are the only thing that
//     pins the resolution to the frame this test sent. retireQuestion's backstop
//     broadcasts "unanswered" when the approval window elapses, which is what an
//     answer that never produced a verdict looks like from out here, and it is
//     rejected loudly rather than accepted as "the batch resolved".
//   - CONTINUATION (AC 3, M1): a non-empty assistant_delta for convID. Every
//     matching delta is accumulated so the caller sees the whole reply, which is
//     drainForCompletedTurnText's addition over the plain sibling drain.
//   - TERMINATION (AC 3, M2): the terminal turn_state{idle} for convID.
//
// IT IS ONE DRAIN AND NOT TWO, and that is the design decision worth keeping. A
// question_dismissed drain followed by a turn drain would assume the dismissal wins a
// race it need not win: AnswerQuestion fans the dismissal out ON ITS OWN GOROUTINE
// (its doc block says why — the caller runs on the relay manager's Run goroutine and
// broadcast funnels back onto it) while the continuation travels claude → parser →
// emitter. Guessing that order wrong would silently eat the first deltas and weaken
// AC 4's check. This loop assumes neither order and requires both.
//
// The frame loop is the package's standing one: read binary→phone frames in receive
// order, decrypt EVERY noise_msg to keep the sequential receive nonce in sync, and
// skip non-noise_msg control frames WITHOUT decrypting. Bounded by one wall clock so
// a stalled daemon fails loud rather than hanging the suite; a TypeError envelope at
// any point is a hard fail, as in the sibling drains.
func drainAnsweredQuestionTurn(t *testing.T, h *perConvHarness, convID, batchID string, timeout time.Duration) string {
	t.Helper()
	var (
		sawDismissal bool
		text         strings.Builder
	)
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if !sawDismissal {
				t.Fatalf("no question_dismissed for batch %s within %s — the answer never resolved the "+
					"batch: it was rejected by the daemon's validator, denied at the device gate, or never "+
					"reached the resolver at all", batchID, timeout)
			}
			if text.Len() == 0 {
				t.Fatalf("batch %s was dismissed as answered but no continuation followed within %s — "+
					"claude was allowed and produced nothing", batchID, timeout)
			}
			t.Fatalf("batch %s was answered and claude continued, but the turn for %q never reached "+
				"terminal turn_state{idle} within %s", batchID, convID, timeout)
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the milestone-specific t.Fatalf above
			}
			t.Fatalf("phone receive (answered-turn drain): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (answered-turn drain): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			// A non-noise_msg control frame (e.g. rekey) does not advance the
			// receive nonce — skip without decrypting.
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (answered-turn drain): %v", err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (answered-turn drain): %v", err)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope while draining the answered turn: %s", string(env.Payload))
		case protocol.TypeQuestionDismissed:
			var dis protocol.QuestionDismissedPayload
			if err := json.Unmarshal(env.Payload, &dis); err != nil {
				t.Fatalf("decode question_dismissed payload: %v", err)
			}
			if dis.QuestionBatchID != batchID {
				t.Fatalf("question_dismissed QuestionBatchID = %q, want %q — a dismissal for another "+
					"batch cannot attribute this one's resolution", dis.QuestionBatchID, batchID)
			}
			if dis.Source != wantQuestionSource {
				t.Fatalf("question_dismissed Source = %q, want %q — the resolution must be attributable "+
					"to our explicit answer, not to the no-answer backstop or any other path",
					dis.Source, wantQuestionSource)
			}
			if dis.Outcome != wantQuestionOutcome {
				t.Fatalf("question_dismissed Outcome = %q, want %q — %q is retireQuestion's backstop "+
					"reporting that the approval window elapsed with no verdict, which is what an answer "+
					"the daemon rejected looks like from here",
					dis.Outcome, wantQuestionOutcome, dis.Outcome)
			}
			sawDismissal = true
			t.Logf("question_dismissed{%s, %s} for batch %s — the resolution is attributable to our answer",
				dis.Outcome, dis.Source, batchID)
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.ConversationID == convID && strings.TrimSpace(p.Text) != "" {
				text.WriteString(p.Text)
			}
		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State != "idle" || st.ConversationID != convID {
				continue
			}
			// Terminal. Both milestones must already hold: an idle with no
			// dismissal means the turn ended without the batch ever resolving, and
			// an idle with no text means claude was allowed and said nothing.
			if !sawDismissal {
				t.Fatalf("turn for %q reached terminal idle with no question_dismissed for batch %s — "+
					"the turn ended without the batch ever resolving", convID, batchID)
			}
			if text.Len() == 0 {
				t.Fatalf("turn for %q reached terminal idle after batch %s was answered, with no "+
					"continuation at all — claude was allowed and produced nothing", convID, batchID)
			}
			t.Logf("terminal turn_state{idle} for %q after the answer; continuation is %d bytes",
				convID, text.Len())
			return text.String()
		}
	}
}

// --- the answers actually reached claude ------------------------------------

// requireContinuationNamesChoice is AC 4, and it is the assertion the whole slice
// exists for: the continuation must name the option the TEST chose, and name it
// FIRST among the batch's offered labels.
//
// Presence alone would not do. A claude that never read the answers map can still
// restate its own question — it authored those labels — and a restatement lists them
// in offer order, where chooseQuestionAnswers deliberately put the chosen one LAST.
// So "first offered label mentioned" is what separates "claude read the answers"
// from "claude repeated its own batch": a compliant reply naming only the label
// passes, "you chose <last> rather than <first>" passes, and "the options were
// <first>, <second>, …" fails.
//
// Case-insensitive substring matching on both halves, the shape
// TestInteractiveStreamMultiTurnContinuity uses for its planted token: claude's
// casing is its own, and the label may sit inside a sentence. A sibling label
// identical to the chosen one is skipped — two options spelling the same label
// cannot be told apart in text, and nothing in claude's contract forbids it.
//
// The failure message prints claude's words through %q and truncateString: they are
// subprocess-authored bytes, nothing on this path strips terminal escapes, and the
// pipeline salvages run logs.
func requireContinuationNamesChoice(t *testing.T, continuation string, choice questionChoice) {
	t.Helper()
	haystack := strings.ToLower(continuation)
	chosenAt := strings.Index(haystack, strings.ToLower(choice.chosen))
	if chosenAt < 0 {
		t.Fatalf("the continuation never names the chosen option %q — claude proceeded but the answers "+
			"map did not reach it (or reached it in a shape it does not read, which is silent because the "+
			"call is allowed either way)\ncontinuation: %q",
			truncateString(choice.chosen, questionLabelLogCap),
			truncateString(continuation, questionTextLogCap))
	}
	for _, other := range choice.unchosen {
		if strings.EqualFold(other, choice.chosen) {
			continue // indistinguishable from the chosen label in text
		}
		at := strings.Index(haystack, strings.ToLower(other))
		if at >= 0 && at < chosenAt {
			t.Fatalf("the continuation names the unchosen option %q before the chosen %q — that is what a "+
				"claude restating its own batch looks like, not one reporting what it was told\n"+
				"continuation: %q",
				truncateString(other, questionLabelLogCap),
				truncateString(choice.chosen, questionLabelLogCap),
				truncateString(continuation, questionTextLogCap))
		}
	}
	t.Logf("the continuation names the chosen option %q first among the batch's labels — the answers "+
		"reached claude", truncateString(choice.chosen, questionLabelLogCap))
}
