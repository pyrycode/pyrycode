package scenario

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestQueueSourceWindowSurvivesQueuedSend(t *testing.T) {
	const conv = "37400000-0000-4000-8000-000000000001"
	home := t.TempDir()
	store := history.New(filepath.Join(home, ".pyry", "test"))
	owner := &history.SessionProvenance{Kind: "claude", SessionID: "producer"}
	var latest uint64
	appendFact := func(kind string, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		entry, err := store.AppendWithMetadata(conversations.ConversationID(conv), kind, raw, time.Now().UTC(), history.Metadata{Session: owner})
		if err != nil {
			t.Fatal(err)
		}
		latest = entry
	}
	open := func(turn string, at time.Time) {
		appendFact("main_turn_opened", map[string]any{"conversation_id": conv, "turn_id": turn, "occurred_at": at})
	}
	end := func(turn string) {
		appendFact(protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: conv, TurnID: turn, StopReason: "end_turn"})
	}
	open("historical", time.Now().Add(-time.Minute))
	end("historical")
	var frames []protocol.Envelope
	frame := func(kind string, payload any) protocol.Envelope {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, TS: time.Now().UTC(), Payload: raw, SessionID: json.RawMessage(`"producer"`)}
	}
	var request uint64
	wire := ObserveSource(t, home, Wire{
		Send: func(kind string, payload any) uint64 {
			request++
			switch kind {
			case protocol.TypeThreadCatchUp:
				response := frame(protocol.TypeThreadItems, protocol.ThreadItemsPayload{ConversationID: conv, Epoch: "epoch", Version: latest, Final: true})
				id := request
				response.InReplyTo = &id
				frames = append(frames, response)
			case protocol.TypeSendMessage:
				message := payload.(protocol.SendMessagePayload)
				appendFact("send_accepted", map[string]string{"conversation_id": conv, "message_id": message.MessageID})
				if message.MessageID == "first" {
					open("first", time.Now().UTC())
				}
			default:
				t.Fatalf("unexpected command %s", kind)
			}
			return request
		},
		Next: func(time.Time) protocol.Envelope {
			if len(frames) == 0 {
				return protocol.Envelope{}
			}
			env := frames[0]
			frames = frames[1:]
			return env
		},
	})
	reader := newQueuePlacementReader(t, wire, conv)
	reader.completionBatch = 2
	wire.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: conv, MessageID: "first"})
	frames = append(frames, frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: conv, State: "responding"}))
	if f, ok := reader.next(time.Now().Add(time.Second)); !ok || f.Kind != "reply_state" {
		t.Fatal("first turn never entered its delivered running state")
	}
	wire.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: conv, MessageID: "queued"})
	end("first")
	open("second", time.Now().UTC())
	end("second")
	terminals := func(source SourceFacts) []string {
		var turns []string
		for _, e := range source(conv) {
			if e.Type != protocol.TypeTurnEnd {
				continue
			}
			var payload protocol.TurnEndPayload
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			turns = append(turns, payload.TurnID)
		}
		return turns
	}
	if got := terminals(wire.Source); !slices.Equal(got, []string{"second"}) {
		t.Fatalf("single-command observer accepted a predecessor: %v", got)
	}
	if got := terminals(reader.w.Source); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("queue observer lost overlapping turns: %v", got)
	}
	if f, ok := reader.next(time.Now().Add(5 * time.Millisecond)); ok && f.Kind == "turn_end" {
		t.Fatal("source-only completion bypassed delivered idle/checkpoint proof")
	}
	for _, state := range []string{"idle", "responding", "idle"} {
		frames = append(frames, frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: conv, State: state}))
	}
	for completed := 0; completed < 2; {
		f, ok := reader.next(time.Now().Add(time.Second))
		if !ok {
			t.Fatal("overlapping source turns did not settle through actual idle and final catch-up")
		}
		if f.Kind == "turn_end" {
			completed++
		}
	}
	if reader.checkpoint < latest {
		t.Fatal("completion lacked final correlated catch-up")
	}
}
