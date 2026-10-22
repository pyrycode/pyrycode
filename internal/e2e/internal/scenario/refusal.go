package scenario

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// RefusalObservation describes Claude's strategy, without pinning its wording or
// requiring text. Live callers separately prove blocked work did not execute.
type RefusalObservation struct {
	ModalsAllowed int
	ExtraRefusals int
	Continuation  string
}

// RefusedTurn drains independent dismissal and completion broadcasts together.
// Re-asked batches are refused (at most two); permission modals are ALLOWED (at
// most four), preserving the non-vacuous live filesystem witness. Every decision
// must have a matching remote dismissal before this drain can succeed.
func RefusedTurn(t *testing.T, w Wire, convID, batchID string, budget time.Duration) RefusalObservation {
	t.Helper()
	obs := RefusalObservation{}
	questions := map[string]bool{batchID: true}
	seen := map[string]bool{batchID: true}
	modals := map[string]bool{}
	sawDismissal, idle := false, false
	var turn sourceTurn
	text := &turn.text
	retries, lastAttempt, maxRetry := 0, 0, 0
	other := map[string]int{}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		env := w.Next(pollDeadline(deadline))
		turn.feed(t, convID, sourceFacts(t, []SourceFacts{w.Source}, convID))
		alarm(t, env)
		if sawDismissal && env.Type != protocol.TypeApiRetry && env.Type != protocol.TypeAssistantDelta {
			other[env.Type]++
		}
		switch env.Type {
		case protocol.TypeQuestionDismissed:
			var dis protocol.QuestionDismissedPayload
			if json.Unmarshal(env.Payload, &dis) != nil || !questions[dis.QuestionBatchID] || dis.Source != "remote" || dis.Outcome != "refused" {
				t.Fatal("question refusal dismissal lost correlation, source or outcome")
			}
			delete(questions, dis.QuestionBatchID)
			if dis.QuestionBatchID == batchID {
				sawDismissal = true
			}
		case protocol.TypeQuestionShown:
			var shown protocol.QuestionShownPayload
			if json.Unmarshal(env.Payload, &shown) != nil || shown.QuestionBatchID == "" || shown.ConversationID != convID {
				t.Fatal("invalid re-asked question ownership")
			}
			if seen[shown.QuestionBatchID] {
				continue
			}
			if obs.ExtraRefusals >= 2 {
				t.Fatal("claude re-asked past the two-refusal cap")
			}
			obs.ExtraRefusals++
			seen[shown.QuestionBatchID], questions[shown.QuestionBatchID] = true, true
			idle = false
			w.Send(protocol.TypeQuestionRefused, protocol.QuestionRefusedPayload{QuestionBatchID: shown.QuestionBatchID, AnswerToken: fmt.Sprintf("e2e-reask-%d", obs.ExtraRefusals)})
		case protocol.TypeModalShown:
			var shown protocol.ModalShownPayload
			if json.Unmarshal(env.Payload, &shown) != nil || shown.Class != "permission" || shown.ModalID == "" || shown.ConversationID != convID || modals[shown.ModalID] {
				t.Fatal("invalid post-refusal permission")
			}
			if obs.ModalsAllowed >= 4 {
				t.Fatal("claude requested tools past the four-permission cap")
			}
			obs.ModalsAllowed++
			modals[shown.ModalID] = true
			idle = false
			w.Send(protocol.TypeModalAnswer, protocol.ModalAnswerPayload{ModalID: shown.ModalID, OptionID: "allow_once", AnswerToken: fmt.Sprintf("e2e-refusal-allow-%d", obs.ModalsAllowed)})
		case protocol.TypeModalDismissed:
			var dis protocol.ModalDismissedPayload
			if json.Unmarshal(env.Payload, &dis) != nil || !modals[dis.ModalID] || dis.Source != "remote" || dis.Outcome != "allow_once" {
				t.Fatal("post-refusal permission was not explicitly allowed by this client")
			}
			delete(modals, dis.ModalID)
		case protocol.TypeAssistantDelta:
			t.Fatal("retired refusal continuation delivered on wire")
		case protocol.TypeApiRetry:
			var p protocol.ApiRetryPayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid refusal retry status")
			}
			if sawDismissal && p.ConversationID == convID {
				retries++
				if p.Active {
					lastAttempt, maxRetry = p.Current, p.Total
				}
			}
		case protocol.TypeTurnState:
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid refusal turn state")
			}
			if turn.idle(t, env, convID) {
				idle = true
			}
		}
		if idle && sawDismissal && len(questions) == 0 && len(modals) == 0 {
			obs.Continuation = text.String()
			return obs
		}
	}
	t.Fatalf("refused turn did not complete: dismissed=%t idle=%t pending questions=%d permissions=%d allowed=%d reasked=%d; %d api_retry frame(s) (last attempt %d of %d), %d text bytes; other frames=%v", sawDismissal, idle, len(questions), len(modals), obs.ModalsAllowed, obs.ExtraRefusals, retries, lastAttempt, maxRetry, text.Len(), other)
	return obs
}

func RecordedRefusalPrompt() string {
	return RecordedQuestionPrompt() + " If I refuse to answer, do not retry, do not use other tools, and reply with one short sentence."
}
