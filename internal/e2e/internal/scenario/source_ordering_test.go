package scenario

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testSourceEntry(t *testing.T, id uint64, kind string, ts time.Time, value any) history.Entry {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return history.Entry{ID: id, Type: kind, TS: ts, Payload: raw, Session: &history.SessionProvenance{Kind: "claude", SessionID: "owner"}}
}

func testSourceOpening(t *testing.T, id uint64, turn string, occurred time.Time) history.Entry {
	t.Helper()
	return testSourceEntry(t, id, "main_turn_opened", occurred, map[string]any{"conversation_id": "conv", "turn_id": turn, "occurred_at": occurred})
}

func testSourceContent(t *testing.T, id uint64, turn, text string, ts time.Time) []history.Entry {
	t.Helper()
	return []history.Entry{
		testSourceEntry(t, id, protocol.TypeAssistantDelta, ts, protocol.AssistantDeltaPayload{ConversationID: "conv", TurnID: turn, Text: text}),
		testSourceEntry(t, id+1, protocol.TypeTurnEnd, ts.Add(time.Second), protocol.TurnEndPayload{ConversationID: "conv", TurnID: turn, StopReason: "end_turn"}),
	}
}

func testSourceIdle(ts time.Time) protocol.Envelope {
	return protocol.Envelope{Type: protocol.TypeTurnState, TS: ts, SessionID: json.RawMessage(`"owner"`), ConversationID: json.RawMessage(`"conv"`), Payload: json.RawMessage(`{"conversation_id":"conv","state":"idle"}`)}
}

func TestSourceDelayedPredecessorOpening(t *testing.T) {
	for _, accepted := range []string{"send_accepted", protocol.TypeMessage} {
		t.Run(accepted, func(t *testing.T) {
			boundary := time.Now().UTC()
			var turn sourceTurn
			turn.feed(t, "conv", []history.Entry{testSourceEntry(t, 1, accepted, boundary, map[string]string{"conversation_id": "conv"})})
			entries := []history.Entry{testSourceOpening(t, 2, "predecessor", boundary.Add(-time.Second))}
			entries = append(entries, testSourceContent(t, 3, "predecessor", "old", boundary.Add(time.Second))...)
			turn.feed(t, "conv", entries)
			if turn.idle(t, testSourceIdle(boundary.Add(2*time.Second)), "conv") || turn.turn != "" || turn.text.Len() != 0 {
				t.Fatal("delayed predecessor opening certified a command that never executed")
			}
			entries = append(entries, testSourceOpening(t, 5, "successor", boundary.Add(3*time.Second)))
			entries = append(entries, testSourceContent(t, 6, "successor", "fresh", boundary.Add(5*time.Second))...)
			turn.feed(t, "conv", entries)
			if turn.idle(t, protocol.Envelope{}, "conv") {
				t.Fatal("predecessor terminal certified successor execution")
			}
			if !turn.idle(t, testSourceIdle(boundary.Add(4*time.Second)), "conv") || turn.text.String() != "fresh" {
				t.Fatal("owned successor idle before history append did not complete")
			}
		})
	}
}

func TestSourceObserverDelayedPredecessorOpening(t *testing.T) {
	for _, acceptance := range []bool{false, true} {
		name := "watch-only"
		if acceptance {
			name = "accepted-command"
		}
		t.Run(name, func(t *testing.T) {
			predecessor := time.Now().UTC().Add(-time.Second)
			var entries []history.Entry
			o := NewSourceObserver(t, t.TempDir())
			o.snapshot = func(string) []history.Entry { return entries }
			o.Watch("conv")
			boundary := time.Now().UTC()
			if acceptance {
				entries = append(entries, testSourceEntry(t, 1, "send_accepted", boundary, map[string]string{"conversation_id": "conv"}))
			}
			entries = append(entries, testSourceOpening(t, 2, "predecessor", predecessor))
			entries = append(entries, testSourceContent(t, 3, "predecessor", "old", boundary.Add(time.Second))...)
			ends := 0
			o.OnEntry = func(e history.Entry) {
				if e.Type == protocol.TypeTurnEnd {
					ends++
				}
			}
			read := func(env protocol.Envelope) {
				o.Next(func(time.Time) protocol.Envelope { return env }, time.Now().Add(time.Second))
			}
			read(testSourceIdle(boundary.Add(2 * time.Second)))
			read(protocol.Envelope{})
			if o.Ended("conv") || ends != 0 || o.Text("conv") != "" {
				t.Fatal("observer emitted predecessor completion for missing successor")
			}
			entries = append(entries, testSourceOpening(t, 5, "successor", boundary.Add(3*time.Second)))
			entries = append(entries, testSourceContent(t, 6, "successor", "fresh", boundary.Add(5*time.Second))...)
			read(protocol.Envelope{})
			if o.Ended("conv") || ends != 0 {
				t.Fatal("observer reused predecessor idle")
			}
			read(testSourceIdle(boundary.Add(4 * time.Second)))
			if !o.Ended("conv") || ends != 1 || o.Text("conv") != "fresh" {
				t.Fatal("observer lost actual owned completion")
			}
		})
	}
}

func TestConsumerIdleBeforeHistory(t *testing.T) {
	for _, consumer := range []string{"reply", "attachment", "outside-read", "permission", "refusal", "answer", "suggestion"} {
		orderings := []string{"poll", "clear", "running", "foreign-clear", "other-reading-clear", "foreign-idle", "old-idle", "omitted-idle"}
		if consumer == "permission" || consumer == "refusal" || consumer == "answer" {
			orderings = append(orderings, "dismissal-after-clear", "dismissal-after-running")
		}
		for _, ordering := range orderings {
			t.Run(consumer+"/"+ordering, func(t *testing.T) {
				start := time.Now().UTC()
				entries := []history.Entry{testSourceOpening(t, 1, "turn", start)}
				entries = append(entries, testSourceContent(t, 2, "turn", "fresh", start.Add(3*time.Second))...)
				reads, sends := 0, 0
				clearing := false
				lateDismissal := ordering == "dismissal-after-clear" || ordering == "dismissal-after-running"
				wantReads := 5
				if ordering == "clear" || ordering == "running" || ordering == "foreign-idle" || ordering == "old-idle" || ordering == "omitted-idle" || lateDismissal {
					wantReads = 6
				}
				frame := func(kind string, value any) protocol.Envelope {
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					return protocol.Envelope{Type: kind, Payload: raw}
				}
				wire := Wire{Source: func(string) []history.Entry {
					if reads < 5 && !(lateDismissal && reads >= 2) {
						return nil
					}
					return entries
				}, Send: func(kind string, value any) uint64 {
					sends++
					if consumer != "suggestion" || kind != protocol.TypeSendMessage {
						t.Fatal("unexpected consumer action")
					}
					clearing = sends == 2
					return uint64(sends)
				}, Next: func(deadline time.Time) protocol.Envelope {
					if clearing {
						return frame(protocol.TypeReplySuggestion, map[string]any{"conversation_id": "conv", "revision": 2, "suggested_reply": nil})
					}
					reads++
					if lateDismissal && reads == 1 {
						return protocol.Envelope{}
					}
					dismissal := func() protocol.Envelope {
						if consumer == "permission" {
							return frame(protocol.TypeModalDismissed, protocol.ModalDismissedPayload{ModalID: "modal", Source: "remote", Outcome: "allow_once"})
						}
						outcome := "answered"
						if consumer == "refusal" {
							outcome = "refused"
						}
						return frame(protocol.TypeQuestionDismissed, protocol.QuestionDismissedPayload{QuestionBatchID: "batch", Source: "remote", Outcome: outcome})
					}
					if lateDismissal && reads == 5 {
						return dismissal()
					}
					switch reads {
					case 1:
						switch consumer {
						case "permission", "refusal", "answer":
							return dismissal()
						case "suggestion":
							return frame(protocol.TypeReplySuggestion, map[string]any{"conversation_id": "conv", "revision": 1, "suggested_reply": "next"})
						}
					case 2:
						idle := testSourceIdle(start.Add(time.Second))
						if ordering == "foreign-idle" {
							idle.SessionID = json.RawMessage(`"foreign"`)
						}
						if ordering == "old-idle" {
							idle.TS = start.Add(-time.Second)
						}
						if ordering == "omitted-idle" {
							idle.SessionID = nil
							idle.TS = start.Add(5 * time.Second)
						}
						return idle
					case 3:
						env := protocol.Envelope{Type: protocol.TypeTurnState, SessionStateCleared: true, ConversationID: json.RawMessage(`"conv"`)}
						switch ordering {
						case "clear", "dismissal-after-clear":
							return env
						case "foreign-clear":
							env.ConversationID = json.RawMessage(`"other"`)
							return env
						case "other-reading-clear":
							env.Type = protocol.TypeThinkingProgress
							return env
						case "running", "dismissal-after-running":
							return frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: "responding"})
						}
					case 6:
						if wantReads == 6 {
							return testSourceIdle(start.Add(5 * time.Second))
						}
					}
					if reads > 6 {
						time.Sleep(time.Until(deadline))
					}
					return protocol.Envelope{}
				}}
				budget := 100 * time.Millisecond
				var text string
				switch consumer {
				case "reply":
					text = CompletedTurn(t, wire.Next, "conv", budget, true, wire.Source)
				case "attachment":
					text = AttachmentTurn(t, wire.Next, "conv", budget, wire.Source)
				case "outside-read":
					text, _, _, _ = OutsideRead(t, wire, "conv", time.Now().Add(budget))
				case "permission":
					text = permissionTurn(t, wire, "conv", map[string]bool{"modal": true}, "allow_once", 4, budget)
				case "refusal":
					text = RefusedTurn(t, wire, "conv", "batch", budget).Continuation
				case "answer":
					text = AnsweredTurn(t, wire.Next, "conv", "batch", budget, wire.Source)
				case "suggestion":
					checked := false
					NativeSuggestions(t, wire, NativeSuggestionConfig{ConversationID: "conv", Prompts: []string{"prompt"}, TurnBudget: budget, Window: budget, CheckNative: func() {
						checked = true
						if reads != wantReads {
							t.Fatalf("native check before owned completion: reads=%d want=%d", reads, wantReads)
						}
					}})
					if !checked || sends != 2 {
						t.Fatal("suggestion native proof or clear lifecycle missing")
					}
					text = "fresh"
				}
				if text != "fresh" || reads != wantReads {
					t.Fatalf("completion text=%q reads=%d want=%d", text, reads, wantReads)
				}
			})
		}
	}
}
