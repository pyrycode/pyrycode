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
