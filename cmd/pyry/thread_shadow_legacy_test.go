package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testShadowPeerRefused(t *testing.T, w *switchWire, key []byte) {
	t.Helper()
	w.privateKeys = map[string][]byte{"phone": key}
	w.rejectCapabilities("phone", []string{protocol.CapabilityInteractive, protocol.CapabilityThread}, protocol.CodeProtocolUnsupported, 4421)
}

func testShadowLegacyPayload(t *testing.T, typ string, raw []byte) string {
	t.Helper()
	if strings.Contains(typ, "thread") {
		t.Fatal("shadow published thread frame")
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"turn_id", "timestamp", "ts"} {
		delete(object, key)
	}
	data, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return typ + ":" + string(data)
}

func TestThreadShadowLegacy(t *testing.T) {
	type surface struct{ live []string }
	run := func(mode string) surface {
		h := history.New(t.TempDir())
		reg, _ := conversations.Load("")
		id := conversations.ConversationID(testConvID)
		reg.Create(conversations.Conversation{ID: id})
		store := thread.NewStore(h)
		s := newThreadShadow(h, reg, store, discardLogger())
		s.quiet = func(conversations.ConversationID) bool { return false }
		if mode == "disabled" {
			s.store = nil
		} else {
			if mode == "failed" {
				dir, err := h.EnsureLogDir(id)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(dir, "thread-recovery.json"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			s.start()
		}
		defer s.shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		peer, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		paired := &devices.Registry{}
		paired.Add(devices.Device{TokenHash: devices.HashToken("phone-token"), Name: "phone", PairedAt: time.Now()})
		w := &switchWire{t: t, frames: make(chan protocol.RoutingEnvelope, 16), out: make(chan protocol.RoutingEnvelope, 128), phones: map[string]switchPhone{}, pub: key.PublicKey().Bytes()}
		mgr, err := relay.NewV2SessionManager(relay.V2SessionConfig{
			Frames: w.frames, Outbound: func(e protocol.RoutingEnvelope) error { w.out <- e; return nil },
			StaticPriv: key.Bytes(), Devices: paired, ServerID: testConvIDB, Logger: discardLogger(),
			KnownConversation: func(id string) bool { _, ok := reg.Get(conversations.ConversationID(id)); return ok },
		})
		if err != nil {
			t.Fatal(err)
		}
		w.manager = mgr
		e := newInteractiveTurnEmitterV2(&activeConversation{}, mgr, discardLogger())
		e.hist, e.runtimeFacts = h, true

		done := make(chan struct{})
		go func() { defer close(done); _ = mgr.Run(ctx) }()
		defer func() { cancel(); <-done }()
		testShadowPeerRefused(t, w, peer.Bytes())
		result := surface{}
		source := history.SessionProvenance{Kind: "claude", SessionID: "saved-source"}
		e.HandleFor(ctx, testConvID, turnevent.TextChunk{Text: "answer"}, source)
		e.HandleFor(ctx, testConvID, turnevent.TurnEnd{}, source)
		transitions := newSessionTransitionEmitterV2(mgr, constResolver(testConvID, true), discardLogger())
		transitions.hist = h
		transitions.broadcast(ctx, sessions.SessionTransition{ConversationID: testConvID, PreviousID: "saved-source", NewID: "successor", PreviousAgent: "claude", NextAgent: "codex", Reason: sessions.ReasonClear, OccurredAt: occurred})
		sends := testSendHistory(h)
		msg := msgqueue.QueuedMessage{ID: 1, MessageID: "send", Text: "operator", TS: occurred}
		sends.accepted(testConvID, msg)
		operatorMessageHistory(h, nil, discardLogger(), sends)(testConvID, msg)
		posts := newChannelPostEmitterV2(mgr, ctx, discardLogger())
		d := testDelivery(t, t.TempDir(), h, nil)
		d.complete = posts.complete
		testAccept(t, d, id, "post")
		d.drain()
		if err := mgr.FlushPushes(ctx); err != nil {
			t.Fatal(err)
		}
		for len(w.out) > 0 {
			_, env := w.read()
			result.live = append(result.live, testShadowLegacyPayload(t, env.Type, env.Payload))
		}
		if len(result.live) != 0 {
			t.Fatal("rejected peer received content")
		}
		// Admission refusal must not change durable producer behavior.
		page, err := h.Page(id, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range page.Entries {
			if legacyHistoryType(entry.Type) {
				result.live = append(result.live, testShadowLegacyPayload(t, entry.Type, entry.Payload))
			}
		}
		if len(result.live) == 0 {
			t.Fatal("producer history fixture is empty")
		}
		w.frames <- protocol.RoutingEnvelope{ConnID: "phone", CloseCode: 1000}
		testShadowWait(t, func() bool { return len(mgr.ActiveConns(ctx)) == 0 })
		testShadowPeerRefused(t, w, peer.Bytes())
		if len(mgr.ActiveConns(ctx)) != 0 || len(w.out) != 0 {
			t.Fatal("unavailable reconnect admitted a session or published content")
		}
		if mode == "enabled" {
			v, _ := h.LatestEntryID(id)
			testShadowEqual(t, h, store, id, v)
		} else if mode == "failed" {
			testShadowWait(t, func() bool { return store.Snapshot(id).State == thread.StateUnavailable })
		}
		return result
	}
	baseline := run("disabled")
	for _, mode := range []string{"enabled", "failed"} {
		t.Run(mode, func(t *testing.T) {
			if got := run(mode); !reflect.DeepEqual(baseline, got) {
				t.Fatalf("durable producer surface changed: baseline=%v shadow=%v", baseline, got)
			}
		})
	}
}
