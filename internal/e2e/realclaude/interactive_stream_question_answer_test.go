//go:build e2e_realclaude

package realclaude

// The live question gate shares encrypted wire adapters and durable-history
// readers with captured replay, and additionally checks current Claude semantics.

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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The refusal driver uses the same remote-attribution vocabulary as the answer driver.
const wantQuestionSource = "remote"
const wantQuestionOutcome = "answered"

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
		"each, and explicitly allow only a single choice (multiSelect false). My answer will also name "+
		"the cache. After I answer, confirm the selected policy and repeat that supplied cache name "+
		"verbatim as the answer witness. Your entire task is this question and confirmation; "+
		"do not offer implementation, write code or use any other tool. run=%d", nonce)
}

func TestInteractiveStreamQuestionAnswer(t *testing.T) {
	runParallel(t)
	h, convID := startStdioModalResolutionHarness(t, askQuestionCaptureModel)
	driveSharedQuestionAnswer(t, h, convID)
}

func TestInteractiveStreamStdioQuestionAnswer(t *testing.T) {
	runParallel(t)
	h, convID := startStdioModalResolutionHarness(t, askQuestionCaptureModel)
	driveSharedQuestionAnswer(t, h, convID)
}

func driveSharedQuestionAnswer(t *testing.T, h *perConvHarness, convID string) {
	t.Helper()
	nonce := time.Now().UnixNano()
	scenario.WitnessQuestionAnswer(t, modalScenarioWire(t, h, 2), scenario.WitnessQuestionConfig{
		Home: h.home, ConversationID: convID, SessionID: streamModalBootstrapUUID, Prompt: questionAnswerTrigger(nonce),
		SurfaceBudget: questionSurfaceBudget, UpdateBudget: questionModelUpdateBudget, TurnBudget: perTurnReplyBudget,
	})
	sealSendMessage(t, h.phone, h.initSend, 5, convID, "m-after-model-change", fmt.Sprintf("Reply with one short word. run=%d after-model-change", nonce))
	drainQuestionModelTurn(t, h.phone, h.initRecv, convID, announcedTargetFor(t, "haiku"), perTurnReplyBudget)
}

func drainQuestionModelTurn(t *testing.T, phone scenario.Receiver, cs *noise.CipherState, convID, expected string, timeout time.Duration) {
	t.Helper()
	scenario.WitnessModelTurn(t, func(deadline time.Time) protocol.Envelope {
		env, _ := receiveQuestionEnvelope(t, phone, cs, deadline)
		return env
	}, convID, expected, timeout)
}

// raiseRealQuestionBatch asserts a nonempty batch before any answer is sent.
func raiseRealQuestionBatch(t *testing.T, h *perConvHarness, reqID uint64, convID, triggerPrompt string, prior ...*strings.Builder) protocol.QuestionShownPayload {
	t.Helper()
	sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID), triggerPrompt)
	var collected *strings.Builder
	if len(prior) > 0 {
		collected = prior[0]
	}
	env := awaitQuestionControl(t, h, protocol.TypeQuestionShown, questionSurfaceBudget, collected, nil)

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
	entries := scenario.ChooseWitnessAnswers(t, batch, multiple, choice.witness)
	t.Logf("generated answer values: %q", truncateString(string(mustJSON(t, entries)), questionTextLogCap))
	return entries, choice
}

func checkQuestionWitness(witness string, batch protocol.QuestionShownPayload, trigger, prior string) error {
	return scenario.CheckQuestionWitness(witness, batch, trigger, prior)
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
	return scenario.ObserveWitnessAnswer(env, p.convID, p.batchID, &p.text, &p.dismissed, &p.idle)
}

func (p *questionAnswerTurn) complete() bool {
	return scenario.WitnessAnswerComplete(p.witness, p.text.String(), p.dismissed, p.idle)
}

func drainAnsweredQuestionTurn(t *testing.T, h *perConvHarness, convID, batchID, witness string, timeout time.Duration) string {
	t.Helper()
	proof := questionAnswerTurn{convID: convID, batchID: batchID, witness: witness}
	deadline := time.Now().Add(timeout)
	for {
		env, ok := receiveQuestionEnvelope(t, h.phone, h.initRecv, deadline)
		if !ok {
			t.Fatalf("answer proof incomplete: dismissal=%v idle=%v; continuation=%q", proof.dismissed, proof.idle, truncateString(proof.text.String(), questionTextLogCap))
		}
		if env.Type == protocol.TypeToolResult {
			t.Logf("question tool result: %q", truncateString(string(env.Payload), questionTextLogCap))
		}
		if err := proof.observe(env); err != nil {
			t.Fatal(err)
		}
		if proof.complete() {
			t.Logf("answered continuation: %q", truncateString(proof.text.String(), questionTextLogCap))
			return proof.text.String()
		}
	}
}

// receiveQuestionEnvelope decrypts every Noise frame in order. Transport controls
// do not advance the receive nonce and are skipped without decryption.
func receiveQuestionEnvelope(t *testing.T, phone scenario.Receiver, cs *noise.CipherState, deadline time.Time) (protocol.Envelope, bool) {
	t.Helper()
	for time.Until(deadline) > 0 {
		raw, err := phone.ReceiveBytes(time.Until(deadline))
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
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("question decrypt: %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatal(err)
		}
		if err := checkQuestionEnvelopeAlarm(env); err != nil {
			t.Fatal(err)
		}
		return env, true
	}
	return protocol.Envelope{}, false
}

func checkQuestionEnvelopeAlarm(env protocol.Envelope) error {
	switch env.Type {
	case protocol.TypeError:
		return fmt.Errorf("question error envelope: %q", truncateString(string(env.Payload), questionTextLogCap))
	case protocol.TypeUnrecognizedMessage:
		return fmt.Errorf("question exposed an unrecognized provider message: %q", truncateString(string(env.Payload), questionTextLogCap))
	case protocol.TypeRateLimited:
		var p protocol.RateLimitedPayload
		if json.Unmarshal(env.Payload, &p) != nil || p.Status != scenario.WarnRateLimitStatus {
			return fmt.Errorf("question rate-limit status is malformed or unknown: %q", truncateString(string(env.Payload), questionTextLogCap))
		}
	}
	return nil
}

func checkQuestionControl(env protocol.Envelope, parked *protocol.QuestionShownPayload) error {
	return scenario.CheckWitnessQuestionControl(env, parked)
}

// Retain pre-answer prose and reject resolution while the question must remain
// outstanding, including during an in-band model update.
func awaitQuestionControl(t *testing.T, h *perConvHarness, want string, timeout time.Duration, prior *strings.Builder, parked *protocol.QuestionShownPayload) protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		env, ok := receiveQuestionEnvelope(t, h.phone, h.initRecv, deadline)
		if !ok {
			t.Fatalf("no %s within %s", want, timeout)
		}
		if err := checkQuestionControl(env, parked); err != nil {
			t.Fatal(err)
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

// modalScenarioWire leaves encryption and sequence ownership in the live harness.
func modalScenarioWire(t *testing.T, h *perConvHarness, first uint64) scenario.Wire {
	t.Helper()
	sequence := first
	return scenario.Wire{Next: scenario.Receive(t, h.phone, h.initRecv), Send: func(kind string, payload any) uint64 {
		id := sequence
		sequence++
		sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: id, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
		return id
	}}
}
