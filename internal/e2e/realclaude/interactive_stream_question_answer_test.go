//go:build e2e_realclaude

package realclaude

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/history"
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

// The trigger contains no preference or answer witness.
func questionAnswerTrigger(nonce int64) string {
	return fmt.Sprintf("Before writing anything, use the AskUserQuestion tool to ask me one "+
		"clarifying question: for an in-memory key-value cache, which eviction policy should it use? "+
		"Give the question a short header, offer four policies as options with a one-line description "+
		"each, and explicitly allow only a single choice (multiSelect false). After I answer, "+
		"confirm your interpretation of my selection and include the supplied answer witness verbatim. "+
		"Do not write code or use any other tool. run=%d", nonce)
}

func TestInteractiveStreamQuestionAnswer(t *testing.T) {
	runParallel(t)
	// The #1154 harness, unchanged except for the model: it pairs the phone WITH
	// --allow-remote-permissions (questionResolverV2.admit gates on the same
	// MayAnswerRemotePermission() bit the modal path does), writes the stream-json
	// toggle, and spawns WITHOUT --dangerously-skip-permissions so claude's tool
	// call genuinely parks on the approve path.
	h, convID := startStreamModalResolutionHarness(t, askQuestionCaptureModel)
	nonce := time.Now().UnixNano()

	// AC 1: a real batch surfaced, and non-vacuous BEFORE anything is answered.
	trigger := questionAnswerTrigger(nonce)
	var prior strings.Builder
	batch := raiseRealQuestionBatch(t, h, 2, convID, trigger, &prior)

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
	updated := awaitQuestionControl(t, h, protocol.TypeSessionSettingsUpdated, questionModelUpdateBudget, &prior)
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
	entries, choice := chooseQuestionAnswers(t, batch, false)
	if err := checkQuestionWitness(choice.witness, batch, trigger, prior.String()); err != nil {
		t.Fatal(err)
	}

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

	drainAnsweredQuestionTurn(t, h, convID, batch.QuestionBatchID, choice.witness, perTurnReplyBudget)
	requirePromptAnswerHistory(t, h, convID, batch.QuestionBatchID, "answer", true, func(e history.Entry) error {
		return checkQuestionAnswerHistory(e, batch, entries)
	})

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

// raiseRealQuestionBatch asserts a nonempty batch before any answer is sent.
func raiseRealQuestionBatch(t *testing.T, h *perConvHarness, reqID uint64, convID, triggerPrompt string, prior ...*strings.Builder) protocol.QuestionShownPayload {
	t.Helper()
	sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID), triggerPrompt)
	var collected *strings.Builder
	if len(prior) > 0 {
		collected = prior[0]
	}
	env := awaitQuestionControl(t, h, protocol.TypeQuestionShown, questionSurfaceBudget, collected)

	var batch protocol.QuestionShownPayload
	if err := json.Unmarshal(env.Payload, &batch); err != nil {
		t.Fatalf("decode question_shown payload: %v", err)
	}
	if batch.ConversationID != convID {
		t.Fatal("question surfaced for another conversation")
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

// Single-select carries the offered label and witness in one legal free-text
// value. Multi-select retains two offered labels and adds a tied free-text value.
type questionChoice struct{ witness string }

func chooseQuestionAnswers(t *testing.T, batch protocol.QuestionShownPayload, multiple bool) ([]protocol.QuestionAnswerEntry, questionChoice) {
	t.Helper()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	choice := questionChoice{witness: fmt.Sprintf("answer-witness-%x", random)}
	var entries []protocol.QuestionAnswerEntry
	for i, q := range batch.Questions {
		if len(q.Options) == 0 {
			t.Fatal("question offers no options")
		}
		selected := q.Options[len(q.Options)-1].Label
		values := []string{selected}
		if multiple {
			if !q.MultiSelect || len(q.Options) < 2 || selected == q.Options[len(q.Options)-2].Label {
				t.Fatal("question cannot prove multiple selections")
			}
			values = append(values, q.Options[len(q.Options)-2].Label)
			selected = strings.Join(values, ", ")
		}
		if i == 0 {
			answer := fmt.Sprintf("%s (answer witness: %s)", selected, choice.witness)
			if multiple {
				values = append(values, answer)
			} else {
				values[0] = answer
			}
		}
		entries = append(entries, protocol.QuestionAnswerEntry{QuestionIndex: i, Values: values})
	}
	return entries, choice
}

func checkQuestionWitness(witness string, batch protocol.QuestionShownPayload, trigger, prior string) error {
	raw, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	if witness == "" || strings.Contains(trigger, witness) || strings.Contains(prior, witness) || strings.Contains(string(raw), witness) {
		return errors.New("answer witness is empty or already present before answering")
	}
	return nil
}

const questionLabelLogCap = 256
const questionTextLogCap = 2048

// One owner consumes independently delivered dismissal, continuation and idle.
// No pre-answer text is seeded into this accumulator.
type questionAnswerTurn struct {
	convID, batchID, witness string
	text                     strings.Builder
	dismissed, idle          bool
}

func (p *questionAnswerTurn) observe(env protocol.Envelope) error {
	switch env.Type {
	case protocol.TypeQuestionDismissed:
		var d protocol.QuestionDismissedPayload
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			return err
		}
		if d.QuestionBatchID != p.batchID || d.Source != wantQuestionSource || d.Outcome != wantQuestionOutcome {
			return errors.New("question dismissal does not match original remote answer")
		}
		p.dismissed = true
	case protocol.TypeAssistantDelta:
		var d protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			return err
		}
		if d.ConversationID == p.convID {
			p.text.WriteString(d.Text)
			if d.Text != "" {
				p.idle = false
			}
		}
	case protocol.TypeTurnState:
		var st protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &st); err != nil {
			return err
		}
		if st.ConversationID == p.convID {
			p.idle = st.State == "idle"
		}
	}
	return nil
}

func (p *questionAnswerTurn) complete() bool {
	return p.dismissed && p.idle && p.witness != "" && strings.Contains(p.text.String(), p.witness)
}

func drainAnsweredQuestionTurn(t *testing.T, h *perConvHarness, convID, batchID, witness string, timeout time.Duration) string {
	t.Helper()
	proof := questionAnswerTurn{convID: convID, batchID: batchID, witness: witness}
	deadline := time.Now().Add(timeout)
	for {
		env, ok := receiveQuestionEnvelope(t, h, deadline)
		if !ok {
			t.Fatalf("answer proof incomplete: dismissal=%v idle=%v; continuation=%q", proof.dismissed, proof.idle, truncateString(proof.text.String(), questionTextLogCap))
		}
		if err := proof.observe(env); err != nil {
			t.Fatal(err)
		}
		if proof.complete() {
			return proof.text.String()
		}
	}
}

// receiveQuestionEnvelope decrypts every Noise frame in order. Transport controls
// do not advance the receive nonce and are skipped without decryption.
func receiveQuestionEnvelope(t *testing.T, h *perConvHarness, deadline time.Time) (protocol.Envelope, bool) {
	t.Helper()
	for time.Until(deadline) > 0 {
		raw, err := h.phone.ReceiveBytes(time.Until(deadline))
		if errors.Is(err, fakephone.ErrReceiveTimeout) {
			return protocol.Envelope{}, false
		}
		if err != nil {
			t.Fatalf("question receive: %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatal(err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("question decrypt: %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatal(err)
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("question error envelope: %q", truncateString(string(env.Payload), questionTextLogCap))
		}
		return env, true
	}
	return protocol.Envelope{}, false
}

// Retain pre-answer prose and reject resolution while the question must remain
// outstanding, including during an in-band model update.
func awaitQuestionControl(t *testing.T, h *perConvHarness, want string, timeout time.Duration, prior *strings.Builder) protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		env, ok := receiveQuestionEnvelope(t, h, deadline)
		if !ok {
			t.Fatalf("no %s within %s", want, timeout)
		}
		if env.Type == protocol.TypeQuestionDismissed {
			t.Fatal("question resolved before the answer was sent")
		}
		if env.Type == protocol.TypeAssistantDelta && prior != nil {
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			prior.WriteString(p.Text)
		}
		if env.Type == want {
			return env
		}
	}
}
