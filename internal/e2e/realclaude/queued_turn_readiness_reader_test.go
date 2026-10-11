//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestQueuedReadinessFold(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reference int
		duplicate bool
		want      bool
	}{
		{"matching delivery", 1, false, true}, {"unrelated acceptance", 9, false, false}, {"duplicate message", 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Now().UTC()
			entry := func(id uint64, kind, payload string) history.Entry {
				return history.Entry{ID: id, Type: kind, TS: at, Payload: json.RawMessage(payload)}
			}
			entries := []history.Entry{
				entry(1, "send_accepted", `{"conversation_id":"c","message_id":"m","accepted_at":"`+at.Format(time.RFC3339Nano)+`"}`),
				entry(2, "message", `{"conversation_id":"c","message_id":"m","role":"user","text":"private prompt"}`),
				entry(3, "send_delivered", `{"conversation_id":"c","accepted_entry_id":`+string(rune('0'+tc.reference))+`,"delivery_entry_id":2}`),
				entry(4, "main_turn_opened", `{"conversation_id":"c"}`),
				entry(5, "assistant_delta", `{"conversation_id":"other","text":"private other reply"}`),
				entry(6, "assistant_delta", `{"conversation_id":"c","text":"PiNg"}`),
			}
			if tc.duplicate {
				entries = append(entries, entries[1])
			}
			r := readinessTurn{Ordinal: 1}
			readinessFoldTurn(&r, entries, "c", "m", "private prompt")
			if r.ExactlyOnce != tc.want || !r.PromptMatches || !r.ReplyMatches {
				t.Fatalf("fold once=%t prompt=%t reply=%t", r.ExactlyOnce, r.PromptMatches, r.ReplyMatches)
			}
			b, err := json.Marshal(readinessCapture{Turns: []readinessTurn{r}, Witnesses: []readinessWitness{{Subject: "private session", Value: 12345, Boundary: "gate_released"}}})
			if err != nil || strings.Contains(string(b), "private") || strings.Contains(string(b), "12345") {
				t.Fatal("retained evidence leaked private join/content")
			}
		})
	}
}
