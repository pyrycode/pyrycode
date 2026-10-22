package scenario

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestSourceObserverAgentFacts(t *testing.T) {
	o := NewSourceObserver(t, t.TempDir())
	o.AllowUnrecognized = true
	o.snapshot = func(string) []history.Entry { return nil }
	o.Watch("conv")
	var entries []history.Entry
	want := []string{protocol.TypeBackgroundTaskStarted, protocol.TypeBackgroundTaskUpdated, protocol.TypeBackgroundTaskRoster,
		protocol.TypeSessionTransition, protocol.TypeModelRefusalFallback, protocol.TypeModelRefusalNoFallback, protocol.TypeCompactionBoundary, protocol.TypeUnrecognizedMessage}
	for i, kind := range want {
		entries = append(entries, history.Entry{ID: uint64(i + 1), Type: kind, Session: &history.SessionProvenance{Kind: "claude", SessionID: "source"}, Payload: json.RawMessage(`{"conversation_id":"conv","task_id":"held"}`)})
	}
	o.snapshot = func(string) []history.Entry { return entries }
	var got []string
	o.OnEntry = func(e history.Entry) { got = append(got, e.Type) }
	next := func(time.Time) protocol.Envelope {
		return protocol.Envelope{ID: 9, Type: protocol.TypeWorkspaceUpdated, Payload: json.RawMessage(`{}`)}
	}
	for range 2 {
		env, ok := o.Next(next, time.Now().Add(time.Second))
		if !ok || env.ID != 9 || env.Type != protocol.TypeWorkspaceUpdated {
			t.Fatal("source observation replaced live output")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source callback order/dedup: %v", got)
	}
	if transitions := o.snapshot.Transitions(t, "conv"); len(transitions) != 1 || transitions[0].ConversationID != "conv" {
		t.Fatal("retained transition unavailable")
	}
}
