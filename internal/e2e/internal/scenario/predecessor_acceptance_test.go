package scenario

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"path/filepath"
	"testing"
	"time"
)

func TestVerifier3740PredecessorBeforeAcceptance(t *testing.T) {
	for _, adapter := range []string{"SourceSince", "ObserveSource"} {
		t.Run(adapter, func(t *testing.T) {
			const conv = "37340000-0000-4000-8000-000000000001"
			home := t.TempDir()
			store := history.New(filepath.Join(home, ".pyry", "test"))
			owner := &history.SessionProvenance{Kind: "claude", SessionID: "same-session"}
			clock := time.Now().UTC()
			appendFact := func(kind string, ts time.Time, p any) {
				t.Helper()
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.AppendWithMetadata(conversations.ConversationID(conv), kind, raw, ts, history.Metadata{Session: owner}); err != nil {
					t.Fatal(err)
				}
			}
			appendTurn := func(turn, text string, opened time.Time) {
				appendFact("main_turn_opened", clock, map[string]any{"conversation_id": conv, "turn_id": turn, "occurred_at": opened})
				appendFact(protocol.TypeAssistantDelta, clock, protocol.AssistantDeltaPayload{ConversationID: conv, TurnID: turn, Text: text})
				appendFact(protocol.TypeTurnEnd, clock, protocol.TurnEndPayload{ConversationID: conv, TurnID: turn, StopReason: "end_turn"})
			}
			source := SourceSince(t, home, conv)
			reads := 0
			send := func(string, any) uint64 {
				// Already executing predecessor drains after the command's source watermark,
				// before the newly submitted command's acceptance is retained.
				appendTurn("predecessor", "OLD predecessor reply", clock.Add(-time.Minute))
				appendFact("send_accepted", clock, map[string]any{"conversation_id": conv, "message_id": "successor"})
				return 42
			}
			next := func(time.Time) protocol.Envelope {
				reads++
				ts := clock.Add(time.Millisecond)
				if reads == 2 {
					appendTurn("successor", "FRESH successor reply", clock.Add(time.Second))
					ts = clock.Add(2 * time.Second)
				}
				return protocol.Envelope{Type: protocol.TypeTurnState, TS: ts, SessionID: json.RawMessage(`"same-session"`), ConversationID: json.RawMessage(`"` + conv + `"`), Payload: json.RawMessage(`{"conversation_id":"` + conv + `","state":"idle"}`)}
			}
			if adapter == "ObserveSource" {
				wire := ObserveSource(t, home, Wire{Send: send, Next: next})
				source = wire.Source
				send = wire.Send
			}
			send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: conv, MessageID: "successor"})
			got := CompletedTurn(t, next, conv, time.Second, false, source)
			if got != "FRESH successor reply" || reads != 2 {
				t.Fatalf("completed successor with %q after %d live read(s); successor has not executed", got, reads)
			}
		})
	}
}

// Every completion consumer uses the real history adapters, including a poll
// that sees a predecessor before acceptance has even been appended.
func TestCommandWindowConsumerOwnership(t *testing.T) {
	for _, adapter := range []string{"SourceSince", "ObserveSource"} {
		for _, split := range []bool{false, true} {
			for _, consumer := range []string{"reply", "attachment", "outside-read", "permission", "refusal", "answer", "suggestion", "cancel"} {
				name := adapter + "/combined/" + consumer
				if split {
					name = adapter + "/split/" + consumer
				}
				t.Run(name, func(t *testing.T) {
					const conv = "37340000-0000-4000-8000-000000000001"
					home := t.TempDir()
					store := history.New(filepath.Join(home, ".pyry", "test"))
					owner := &history.SessionProvenance{Kind: "claude", SessionID: "same-session"}
					clock := time.Now().UTC()
					reads, sends, stage := 0, 0, 0
					prefix := consumer == "permission" || consumer == "refusal" || consumer == "answer" || consumer == "suggestion" || consumer == "cancel"
					wantReads := 2
					if prefix {
						wantReads++
					}
					if split {
						wantReads++
					}
					frame := func(kind string, p any) protocol.Envelope {
						raw, err := json.Marshal(p)
						if err != nil {
							t.Fatal(err)
						}
						return protocol.Envelope{Type: kind, Payload: raw}
					}
					appendFact := func(kind string, p any) {
						raw, err := json.Marshal(p)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := store.AppendWithMetadata(conversations.ConversationID(conv), kind, raw, clock, history.Metadata{Session: owner}); err != nil {
							t.Fatal(err)
						}
					}
					appendTurn := func(turn, text string, at time.Time) {
						appendFact("main_turn_opened", map[string]any{"conversation_id": conv, "turn_id": turn, "occurred_at": at})
						appendFact(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: conv, TurnID: turn, Text: text})
						appendFact(protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: conv, TurnID: turn, StopReason: "end_turn"})
					}
					accept := func() {
						appendFact("send_accepted", map[string]string{"conversation_id": conv, "message_id": "successor"})
					}
					wire := Wire{Source: SourceSince(t, home, conv)}
					wire.Send = func(kind string, p any) uint64 {
						if kind != protocol.TypeSendMessage {
							return 42
						}
						sends++
						if sends == 1 {
							clock = time.Now().UTC()
							appendTurn("old", "OLD", clock.Add(-time.Minute))
							if !split {
								accept()
							}
						}
						return uint64(sends)
					}
					wire.Next = func(time.Time) protocol.Envelope {
						if sends == 2 {
							return frame(protocol.TypeReplySuggestion, map[string]any{"conversation_id": conv, "revision": 2, "suggested_reply": nil})
						}
						reads++
						if prefix && reads == 1 {
							switch consumer {
							case "permission":
								return frame(protocol.TypeModalDismissed, protocol.ModalDismissedPayload{ModalID: "modal", Source: "remote", Outcome: "allow_once"})
							case "refusal":
								return frame(protocol.TypeQuestionDismissed, protocol.QuestionDismissedPayload{QuestionBatchID: "batch", Source: "remote", Outcome: "refused"})
							case "answer":
								return frame(protocol.TypeQuestionDismissed, protocol.QuestionDismissedPayload{QuestionBatchID: "batch", Source: "remote", Outcome: "answered"})
							case "cancel":
								return frame(protocol.TypeModalDismissed, protocol.ModalDismissedPayload{ModalID: "modal", Source: "remote", Outcome: "cancelled"})
							case "suggestion":
								return frame(protocol.TypeReplySuggestion, map[string]any{"conversation_id": conv, "revision": 1, "suggested_reply": "next"})
							}
						}
						stage++
						if split && stage == 2 {
							accept()
							return protocol.Envelope{}
						}
						at := clock.Add(time.Millisecond)
						if stage == 2 && !split || stage == 3 && split {
							appendTurn("new", "FRESH", clock.Add(time.Second))
							at = clock.Add(2 * time.Second)
						}
						if reads > wantReads {
							t.Fatal("consumer failed to finish the actual successor")
						}
						return protocol.Envelope{Type: protocol.TypeTurnState, TS: at, SessionID: json.RawMessage(`"same-session"`), ConversationID: json.RawMessage(`"` + conv + `"`), Payload: json.RawMessage(`{"conversation_id":"` + conv + `","state":"idle"}`)}
					}
					if adapter == "ObserveSource" {
						wire = ObserveSource(t, home, wire)
					}
					if consumer != "suggestion" {
						wire.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: conv, MessageID: "successor"})
					}
					var text string
					switch consumer {
					case "reply":
						text = CompletedTurn(t, wire.Next, conv, time.Second, false, wire.Source)
					case "attachment":
						text = AttachmentTurn(t, wire.Next, conv, time.Second, wire.Source)
					case "outside-read":
						text, _, _, _ = OutsideRead(t, wire, conv, time.Now().Add(time.Second))
					case "permission":
						text = permissionTurn(t, wire, conv, map[string]bool{"modal": true}, "allow_once", 4, time.Second)
					case "refusal":
						text = RefusedTurn(t, wire, conv, "batch", time.Second).Continuation
					case "answer":
						text = AnsweredTurn(t, wire.Next, conv, "batch", time.Second, wire.Source)
					case "suggestion":
						NativeSuggestions(t, wire, NativeSuggestionConfig{ConversationID: conv, Prompts: []string{"test"}, TurnBudget: time.Second, Window: time.Second, CheckNative: func() {
							if reads != wantReads {
								t.Fatal("suggestion certified predecessor")
							}
						}})
						text = "FRESH"
					case "cancel":
						CancelPermission(t, wire, conv, protocol.ModalShownPayload{ConversationID: conv, Class: "permission", ModalID: "modal"}, time.Second, time.Second)
						text = "FRESH"
					}
					if text != "FRESH" || reads != wantReads {
						t.Fatalf("consumer accepted %q after %d observations, want fresh successor after %d", text, reads, wantReads)
					}
				})
			}
		}
	}
}

func TestSourceTurnLateAcceptanceInvalidatesSelection(t *testing.T) {
	now := time.Now().UTC()
	var turn sourceTurn
	old := []history.Entry{testSourceOpening(t, 1, "old", now.Add(-time.Minute))}
	old = append(old, testSourceContent(t, 2, "old", "OLD", now)...)
	turn.feed(t, "conv", old)
	turn.liveIdle = &protocol.Envelope{Type: protocol.TypeTurnState}
	turn.feed(t, "conv", []history.Entry{testSourceEntry(t, 4, "send_accepted", now, map[string]string{"conversation_id": "conv"})})
	if turn.turn != "" || turn.ended || turn.text.Len() != 0 || turn.liveIdle != nil {
		t.Fatal("later acceptance retained an earlier turn")
	}
	fresh := []history.Entry{testSourceOpening(t, 5, "new", now.Add(time.Second))}
	fresh = append(fresh, testSourceContent(t, 6, "new", "FRESH", now.Add(2*time.Second))...)
	turn.feed(t, "conv", fresh)
	if !turn.idle(t, testSourceIdle(now.Add(3*time.Second)), "conv") || turn.text.String() != "FRESH" {
		t.Fatal("actual successor did not complete")
	}
}

func TestCommandSourcePreservesChildrenAndAlarms(t *testing.T) {
	const conv = "37340000-0000-4000-8000-000000000001"
	home := t.TempDir()
	store := history.New(filepath.Join(home, ".pyry", "test"))
	source := SourceSince(t, home, conv)
	now := time.Now().UTC()
	owner := &history.SessionProvenance{Kind: "claude", SessionID: "owner"}
	appendFact := func(kind string, p any) {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendWithMetadata(conversations.ConversationID(conv), kind, raw, now, history.Metadata{Session: owner}); err != nil {
			t.Fatal(err)
		}
	}
	appendFact("main_turn_opened", map[string]any{"conversation_id": conv, "turn_id": "old", "occurred_at": now.Add(-time.Minute)})
	appendFact(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: conv, TurnID: "old", Text: "old main"})
	appendFact(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: conv, TurnID: "child", ParentToolUseID: "tool", Text: "child"})
	appendFact(protocol.TypeUnrecognizedMessage, map[string]string{"conversation_id": conv, "turn_id": "old", "message_type": "unknown"})
	entries := source(conv)
	if len(entries) != 2 || entries[0].ID != 3 || entries[0].Type != protocol.TypeAssistantDelta || entries[1].Type != protocol.TypeUnrecognizedMessage {
		t.Fatalf("command filter removed child or alarm facts, or retained old main content: %+v", entries)
	}
	if all := SourceSnapshot(t, home)(conv); len(all) != 4 {
		t.Fatal("command observation changed retained history")
	}
}
