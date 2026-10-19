//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestQuestionAnswerTurnProof(t *testing.T) {
	t.Parallel()
	const witness = "answer-witness-71bc"
	delta := func(conv, text string) protocol.Envelope {
		return protocol.Envelope{Type: protocol.TypeAssistantDelta, Payload: mustJSON(t, protocol.AssistantDeltaPayload{ConversationID: conv, Text: text})}
	}
	idle := protocol.Envelope{Type: protocol.TypeTurnState, Payload: mustJSON(t, protocol.TurnStatePayload{ConversationID: "conv", State: "idle"})}
	dismiss := func(batch, source, outcome string) protocol.Envelope {
		return protocol.Envelope{Type: protocol.TypeQuestionDismissed, Payload: mustJSON(t, protocol.QuestionDismissedPayload{QuestionBatchID: batch, Source: source, Outcome: outcome})}
	}
	done := dismiss("batch", "remote", "answered")
	for _, tc := range []struct {
		name   string
		events []protocol.Envelope
		want   bool
	}{
		{"varied prose", []protocol.Envelope{done, delta("conv", "Rather than FIFO, you selected LRU: "+witness), idle}, true},
		{"overlapping labels", []protocol.Envelope{delta("conv", "LRU and LRU-K: "+witness), done, idle}, true},
		{"split deltas", []protocol.Envelope{delta("conv", "answer-wit"), done, delta("conv", "ness-71bc"), idle}, true},
		{"idle before dismissal", []protocol.Envelope{delta("conv", witness), idle, done}, true},
		{"restated question", []protocol.Envelope{done, delta("conv", "FIFO, LRU, LRU-K?"), idle}, false},
		{"ignored answer", []protocol.Envelope{done, delta("conv", "I will proceed."), idle}, false},
		{"receipt only", []protocol.Envelope{done, idle}, false},
		{"wrong batch", []protocol.Envelope{dismiss("other", "remote", "answered"), delta("conv", witness), idle}, false},
		{"wrong source", []protocol.Envelope{dismiss("batch", "local", "answered"), delta("conv", witness), idle}, false},
		{"unanswered", []protocol.Envelope{dismiss("batch", "remote", "unanswered"), delta("conv", witness), idle}, false},
		{"wrong conversation", []protocol.Envelope{done, delta("other", witness), idle}, false},
		{"missing idle", []protocol.Envelope{done, delta("conv", witness)}, false},
		{"stale idle before reply", []protocol.Envelope{idle, done, delta("conv", witness)}, false},
		{"missing dismissal", []protocol.Envelope{delta("conv", witness), idle}, false},
		{"stale pre-answer text only", []protocol.Envelope{done, idle}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof := questionAnswerTurn{convID: "conv", batchID: "batch", witness: witness}
			var err error
			for _, env := range tc.events {
				if err = proof.observe(env); err != nil {
					break
				}
			}
			if got := err == nil && proof.complete(); got != tc.want {
				t.Fatalf("complete=%v, want %v; err=%v", got, tc.want, err)
			}
		})
	}
}

func testQuestionProofBatch() protocol.QuestionShownPayload {
	return protocol.QuestionShownPayload{ConversationID: "conv", QuestionBatchID: "batch", Questions: []protocol.Question{
		{Text: "Cache policy?", Options: []protocol.QuestionOption{{Label: "LRU"}, {Label: "LRU-K"}}},
	}}
}

func TestQuestionAnswerWitness(t *testing.T) {
	t.Parallel()
	batch := testQuestionProofBatch()
	seen := make(map[string]bool)
	for _, multi := range []bool{false, true} {
		batch.Questions[0].MultiSelect = multi
		entries, choice := chooseQuestionAnswers(t, batch, multi)
		if choice.witness == "" || seen[choice.witness] {
			t.Fatal("witness missing or reused")
		}
		seen[choice.witness] = true
		values := entries[0].Values
		if (!multi && len(values) != 1) || (multi && len(values) != 3) || !strings.Contains(strings.Join(values, " "), choice.witness) {
			t.Fatal("answer lost witness or selection cardinality")
		}
		if !strings.Contains(strings.Join(values, " "), "LRU-K") || (multi && (values[0] != "LRU-K" || values[1] != "LRU")) {
			t.Fatal("offered selection lost")
		}
		for _, field := range []string{"trigger", "prior", "question", "option"} {
			t.Run(field, func(t *testing.T) {
				b := testQuestionProofBatch()
				trigger, prior := questionAnswerTrigger(1), "Earlier assistant words"
				switch field {
				case "trigger":
					trigger += choice.witness
				case "prior":
					prior += choice.witness
				case "question":
					b.Questions[0].Text += choice.witness
				case "option":
					b.Questions[0].Options[0].Description += choice.witness
				}
				if err := checkQuestionWitness(choice.witness, b, trigger, prior); err == nil {
					t.Fatal("accepted a pre-answer witness")
				}
			})
		}
		if err := checkQuestionWitness(choice.witness, batch, questionAnswerTrigger(1), "Earlier words"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQuestionAnswerHistoryProof(t *testing.T) {
	t.Parallel()
	batch := testQuestionProofBatch()
	batch.Questions = append(batch.Questions, protocol.Question{Text: "Second question?", MultiSelect: true})
	answers := []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"LRU-K; answer-witness-71bc"}}, {QuestionIndex: 1, Values: []string{"one", "two"}}}
	for _, change := range []string{"valid", "conversation", "batch", "session", "index", "question", "answer", "selection", "missing", "truncated"} {
		t.Run(change, func(t *testing.T) {
			ts := time.Now().UTC()
			questions := []map[string]any{
				{"index": 0, "text": batch.Questions[0].Text, "multi_select": false, "values": []map[string]string{{"text": answers[0].Values[0]}}},
				{"index": 1, "text": batch.Questions[1].Text, "multi_select": true, "values": []map[string]string{{"text": "one"}, {"text": "two"}}},
			}
			fact := map[string]any{"conversation_id": "conv", "correlation_id": "batch", "session_id": streamModalBootstrapUUID, "context": map[string]any{"questions": questions}}
			switch change {
			case "conversation":
				fact["conversation_id"] = "other"
			case "batch":
				fact["correlation_id"] = "other"
			case "session":
				fact["session_id"] = "other"
			case "index":
				questions[0]["index"] = 1
			case "question":
				questions[0]["text"] = "Other?"
			case "answer":
				questions[0]["values"] = []map[string]string{{"text": "LRU"}}
			case "selection":
				questions[0]["multi_select"] = true
			case "missing":
				questions[1]["values"] = nil
			case "truncated":
				fact["truncated"] = true
			}
			raw, err := json.Marshal(fact)
			if err != nil {
				t.Fatal(err)
			}
			e := history.Entry{Payload: raw, TS: ts, Session: &history.SessionProvenance{Kind: "claude", SessionID: streamModalBootstrapUUID}}
			if err := checkQuestionAnswerHistory(e, batch, answers); (err == nil) != (change == "valid") {
				t.Fatalf("validation=%v for %s", err, change)
			}
		})
	}
}
