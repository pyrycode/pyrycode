//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestIdleEvictionTurnClosed(t *testing.T) {
	const conv = "86148614-8614-4614-8614-861486148614"
	for _, tc := range []struct {
		name         string
		endPoll      int
		idlePoll     int
		idleConv     string
		idleSession  string
		endTurn      string
		wantComplete bool
	}{
		{"source-before-idle", 1, 3, conv, "owner", "turn", true},
		{"idle-before-source-empty-poll", 3, 1, conv, "owner", "turn", true},
		{"source-only", 1, 0, conv, "owner", "turn", false},
		{"idle-only", 0, 1, conv, "owner", "turn", false},
		{"foreign-conversation-idle", 3, 1, "other", "owner", "turn", false},
		{"foreign-session-idle", 3, 1, conv, "other", "turn", false},
		{"mismatched-source-end", 3, 1, conv, "owner", "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			store := history.New(filepath.Join(home, ".pyry", "test"))
			source := scenario.NewSourceObserver(t, home)
			source.Watch(conv)
			start := time.Now().UTC()
			appendFact := func(kind string, payload any) {
				t.Helper()
				_, err := store.AppendWithMetadata(conversations.ConversationID(conv), kind, mustJSON(t, payload), start,
					history.Metadata{Session: &history.SessionProvenance{Kind: "claude", SessionID: "owner"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			appendFact("main_turn_opened", map[string]any{"conversation_id": conv, "turn_id": "turn", "occurred_at": start})
			appendFact(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: conv, TurnID: "turn", Text: "opened"})
			polls := 0
			next := func(deadline time.Time) (protocol.Envelope, bool) {
				polls++
				if polls > 3 {
					return protocol.Envelope{}, false
				}
				return source.Next(func(time.Time) protocol.Envelope {
					if polls == tc.endPoll {
						appendFact(protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: conv, TurnID: tc.endTurn, StopReason: "interrupted"})
					}
					if polls == tc.idlePoll {
						return protocol.Envelope{Type: protocol.TypeTurnState, TS: start.Add(time.Second),
							SessionID: mustJSON(t, tc.idleSession), ConversationID: mustJSON(t, tc.idleConv),
							Payload: mustJSON(t, protocol.TurnStatePayload{ConversationID: tc.idleConv, State: "idle"})}
					}
					return protocol.Envelope{}
				}, deadline)
			}
			if got := idleEvictionTurnClosed(t, source, conv, next, time.Now().Add(time.Minute)); got != tc.wantComplete {
				t.Fatalf("completion=%v, want %v after controlled source/idle ordering", got, tc.wantComplete)
			}
			wantPolls := 4
			if tc.wantComplete {
				wantPolls = 3
			}
			if polls != wantPolls {
				t.Fatalf("completed after %d polls, want %d", polls, wantPolls)
			}
		})
	}
}
