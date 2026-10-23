//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestResetWindowSourceTransitionOrdering(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	raw, err := json.Marshal(protocol.SessionTransitionPayload{ConversationID: "conv", PreviousSessionID: "old", NewSessionID: "new", Reason: "clear", OccurredAt: now})
	if err != nil {
		t.Fatal(err)
	}
	w := newResetWindow()
	w.sourceConv = "conv"
	w.source = func(string) []history.Entry {
		return []history.Entry{{ID: 1, Type: protocol.TypeSessionTransition, TS: now, Payload: raw}}
	}
	var frames []protocol.Envelope
	w.receive = func(time.Time) protocol.Envelope {
		if len(frames) == 0 {
			return protocol.Envelope{}
		}
		env := frames[0]
		frames = frames[1:]
		return env
	}
	deadline := now.Add(time.Second)
	if env, ok := w.next(t, nil, nil, deadline); !ok || env.Type != "" || len(w.transitions) != 1 || w.transitions[0].edgesBefore != 0 {
		t.Fatal("source observation manufactured a live frame or preceding edge")
	}
	for i, ts := range []time.Time{now.Add(-2 * time.Second), now.Add(-time.Second), now.Add(time.Second)} {
		p, err := json.Marshal(protocol.ResettingPayload{ConversationID: "conv", Active: i < 2})
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, protocol.Envelope{Type: protocol.TypeResetting, TS: ts, Payload: p})
	}
	for range 3 {
		w.next(t, nil, nil, deadline)
	}
	if len(w.transitions) != 1 || w.transitions[0].edgesBefore != 2 || len(w.edges) != 3 {
		t.Fatal("source polling lost ordering or duplicated the transition")
	}
}

// Family initialization and rotation can each clear reset state. Their combined
// count cannot replace the missing restarting phase or certify an old owner.
func TestResetWindowCurrentStateCompletion(t *testing.T) {
	now := time.Now().UTC()
	transition := windowTransition{payload: protocol.SessionTransitionPayload{ConversationID: "conv", PreviousSessionID: "old", NewSessionID: "new", OccurredAt: now}}
	clear := protocol.Envelope{Type: protocol.TypeResetting, SessionStateCleared: true, ConversationID: json.RawMessage(`"conv"`), SessionID: json.RawMessage(`"new"`), TS: now}
	for _, name := range []string{"coalesced-phase", "duplicate-clear", "old-session", "foreign-conversation", "early-clear", "source-only", "duplicate-rotation"} {
		t.Run(name, func(t *testing.T) {
			w := newResetWindow()
			w.transitions = []windowTransition{transition}
			w.edges = []protocol.ResettingPayload{{ConversationID: "conv", Active: true, Phase: protocol.ResetPhaseWrappingUp, Handoff: protocol.ResetHandoffPending}}
			w.edgeTimes = []time.Time{now.Add(-time.Second)}
			w.clears = []protocol.Envelope{clear}
			want := false
			switch name {
			case "coalesced-phase":
				want = true
			case "duplicate-clear":
				w.clears = append(w.clears, clear)
				want = true
			case "old-session":
				w.clears[0].SessionID = json.RawMessage(`"old"`)
			case "foreign-conversation":
				w.clears[0].ConversationID = json.RawMessage(`"other"`)
			case "early-clear":
				w.clears[0].TS = now.Add(-time.Second)
			case "source-only":
				w.edges = nil
				w.clears = nil
			case "duplicate-rotation":
				w.transitions = append(w.transitions, transition)
			}
			if got := w.resetSettled(); got != want {
				t.Fatalf("settled=%v, want %v", got, want)
			}
			if want {
				w.assertProgress(t, "conv", protocol.ResetHandoffWritten)
				w.assertClear(t, "conv")
			}
		})
	}
}
