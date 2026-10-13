package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestDaemonRelayWireQueuedPushDuringHandshake(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, nil)
	w.open("phone", false)
	w.open("legacy", false)
	peers := []string{"phone", "legacy", "phone", "legacy"}
	for i, peer := range peers {
		e := protocol.Envelope{ID: uint64(10 + i), Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
		if err := mgr.Push(t.Context(), peer, e); err != nil {
			t.Fatal(err)
		}
		if err := mgr.FlushPushes(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(w.out); got != len(peers) {
		t.Fatalf("expected %d queued pushes before handshake, observed %d", len(peers), got)
	}
	w.open("again", false)
	for i, peer := range peers {
		gotPeer, e := w.read()
		if gotPeer != peer || e.ID != uint64(10+i) || e.Type != protocol.TypeError || string(e.Payload) != `{}` {
			t.Fatalf("queued push %d: expected peer=%s id=%d, observed peer=%s envelope=%+v", i, peer, 10+i, gotPeer, e)
		}
	}
	e := protocol.Envelope{ID: 20, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
	if err := mgr.Push(t.Context(), "again", e); err != nil {
		t.Fatal(err)
	}
	if peer, got := w.read(); peer != "again" || got.ID != e.ID {
		t.Fatalf("new connection push: expected peer=again id=%d, observed peer=%s envelope=%+v", e.ID, peer, got)
	}
}
