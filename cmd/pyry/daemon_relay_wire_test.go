package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

func testDaemonRelayAdmission(ctx context.Context, mgr *relay.V2SessionManager, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Run services the snapshot after finishing the handshake and queue registration.
	conns := mgr.ActiveConns(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, conn := range conns {
		if conn.ConnID == id && conn.Thread {
			return nil
		}
	}
	return fmt.Errorf("relay wire: connection %q not admitted", id)
}

func TestDaemonRelayWireAdmissionBeforePush(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	held := make(chan struct{})
	published := make(chan struct{})
	release := sync.OnceFunc(func() { close(held) })
	w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) {
		outbound := cfg.Outbound
		cfg.Outbound = func(e protocol.RoutingEnvelope) error {
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(e.Frame, &inner); err != nil {
				return err
			}
			if err := outbound(e); err != nil {
				return err
			}
			if e.ConnID == "again" && inner.Type == protocol.TypeNoiseResp {
				close(published)
				// Run cannot register the push queue until this callback returns.
				select {
				case <-held:
				case <-t.Context().Done():
					return t.Context().Err()
				}
			}
			return nil
		}
	})
	defer release() // Release Run before the fixture joins it, including on failure.
	w.helloCapabilities("again", []string{protocol.CapabilityInteractive, protocol.CapabilityThread})
	select {
	case <-published:
	case <-time.After(3 * time.Second):
		t.Fatal("response callback did not reach admission hold")
	}
	e := protocol.Envelope{ID: 20, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
	// A missing queue here is unfinished admission: no close or teardown was sent.
	if err := mgr.Push(t.Context(), "again", e); !errors.Is(err, relay.ErrConnNotFound) {
		t.Fatalf("push before admission: got %v, want ErrConnNotFound", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := testDaemonRelayAdmission(ctx, mgr, "again"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission while response callback held: got %v, want deadline exceeded", err)
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if err := testDaemonRelayAdmission(canceled, mgr, "again"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: got %v, want context canceled", err)
	}
	release()
	if err := testDaemonRelayAdmission(t.Context(), mgr, "again"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Push(t.Context(), "again", e); err != nil {
		t.Fatal(err)
	}
	if peer, got := w.read(); peer != "again" || got.ID != e.ID || got.Type != e.Type || string(got.Payload) != string(e.Payload) {
		t.Fatalf("admitted connection push: peer=%s envelope=%+v", peer, got)
	}
}

func TestDaemonRelayWireQueuedPushDuringHandshake(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, nil)
	w.open("phone", false)
	if err := testDaemonRelayAdmission(t.Context(), mgr, "phone"); err != nil {
		t.Fatal(err)
	}
	w.open("legacy", false)
	if err := testDaemonRelayAdmission(t.Context(), mgr, "legacy"); err != nil {
		t.Fatal(err)
	}
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
	if err := testDaemonRelayAdmission(t.Context(), mgr, "again"); err != nil {
		t.Fatal(err)
	}
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
