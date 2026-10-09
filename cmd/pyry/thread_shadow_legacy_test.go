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
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testShadowPeerOpen(t *testing.T, w *switchWire, key []byte, last *uint64) []string {
	t.Helper()
	init, err := noise.NewInitiator(key, w.pub)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(protocol.HelloClientPayload{Role: "client", DeviceName: "phone", ClientVersion: "test", ProtocolVersions: []string{"v2"}, Token: "phone-token", Capabilities: []string{protocol.CapabilityInteractive, "thread"}, LastEventID: last})
	raw, _ := json.Marshal(protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC(), Payload: payload})
	data, err := init.WriteInit(raw)
	if err != nil {
		t.Fatal(err)
	}
	w.frames <- w.wrap("phone", protocol.TypeNoiseInit, data)
	response := w.await()
	if response.ConnID != "phone" || response.CloseCode != 0 {
		t.Fatal("shadow peer handshake rejected")
	}
	ack, send, recv, err := init.ReadResp(w.data(response))
	if err != nil {
		t.Fatal(err)
	}
	w.phones["phone"] = switchPhone{send, recv}
	var env protocol.Envelope
	var hello protocol.HelloAckPayload
	if json.Unmarshal(ack, &env) != nil || env.Type != protocol.TypeHelloAck || json.Unmarshal(env.Payload, &hello) != nil {
		t.Fatal("invalid hello ack")
	}
	for _, cap := range hello.Capabilities {
		if strings.Contains(cap, "thread") {
			t.Fatal("shadow advertised thread capability")
		}
	}
	return hello.Capabilities
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
	type surface struct {
		live, replay, page, capabilities []string
	}
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
			HistoryPage:       newHistoryPager(h, discardLogger()),
		})
		if err != nil {
			t.Fatal(err)
		}
		e := newInteractiveTurnEmitterV2(&activeConversation{}, mgr, discardLogger())
		e.hist, e.runtimeFacts = h, true
		mgr.SetReplaySource(e.ring, func() string { return testConvID })
		done := make(chan struct{})
		go func() { defer close(done); _ = mgr.Run(ctx) }()
		defer func() { cancel(); <-done }()
		result := surface{capabilities: testShadowPeerOpen(t, w, peer.Bytes(), nil)}
		source := history.SessionProvenance{Kind: "claude", SessionID: "saved-source"}
		e.HandleFor(ctx, testConvID, turnevent.TextChunk{Text: "answer"}, source)
		e.HandleFor(ctx, testConvID, turnevent.TurnEnd{}, source)
		transitions := newSessionTransitionEmitterV2(mgr, constResolver(testConvID, true), discardLogger())
		transitions.hist = h
		transitions.broadcast(ctx, sessions.SessionTransition{ConversationID: testConvID, PreviousID: "saved-source", NewID: "successor", PreviousAgent: "claude", NextAgent: "codex", Reason: sessions.ReasonClear, OccurredAt: occurred})
		sends := testSendHistory(h)
		msg := msgqueue.QueuedMessage{MessageID: "send", Text: "operator"}
		sends.accepted(testConvID, msg)
		operator := newOperatorMessageEmitterV2(nil, discardLogger())
		operatorMessageHistory(h, func(m operatorMessage) { operator.broadcast(ctx, mgr, e.ring, m) }, nil, discardLogger(), sends)(testConvID, msg)
		posts := newChannelPostEmitterV2(mgr, ctx, discardLogger())
		posts.ring = e.ring
		d := testDelivery(t, t.TempDir(), h, nil)
		d.announce, d.complete = posts.announce, posts.complete
		testAccept(t, d, id, "post")
		d.drain()
		if err := mgr.FlushPushes(ctx); err != nil {
			t.Fatal(err)
		}
		replayCount := 0
		var retainedLive []string
		for len(w.out) > 0 {
			_, env := w.read()
			result.live = append(result.live, testShadowLegacyPayload(t, env.Type, env.Payload))
			if env.EventID != nil {
				replayCount++
				retainedLive = append(retainedLive, result.live[len(result.live)-1])
			}
		}
		if len(result.live) == 0 || replayCount == 0 {
			t.Fatal("connected peer received no legacy traffic")
		}
		w.frames <- protocol.RoutingEnvelope{ConnID: "phone", CloseCode: 1000}
		testShadowWait(t, func() bool { return len(mgr.ActiveConns(ctx)) == 0 })
		last := uint64(0)
		testShadowPeerOpen(t, w, peer.Bytes(), &last)
		for range replayCount {
			_, env := w.read()
			if env.EventID == nil {
				t.Fatal("reconnect did not replay a legacy event")
			}
			result.replay = append(result.replay, testShadowLegacyPayload(t, env.Type, env.Payload))
		}
		if !reflect.DeepEqual(result.replay, retainedLive) {
			t.Fatal("reconnect differed from retained legacy live frames")
		}
		cursor := ""
		for requestID := uint64(2); ; requestID++ {
			if requestID > 100 {
				t.Fatal("history pages did not terminate")
			}
			payload, _ := json.Marshal(protocol.RequestHistoryPayload{ConversationID: testConvID, Cursor: cursor, Limit: 2})
			raw, _ := json.Marshal(protocol.Envelope{ID: requestID, Type: protocol.TypeRequestHistory, TS: time.Now(), Payload: payload})
			sealed, err := w.phones["phone"].send.Encrypt(raw)
			if err != nil {
				t.Fatal(err)
			}
			w.frames <- w.wrap("phone", protocol.TypeNoiseMsg, sealed)
			_, env := w.read()
			var page protocol.HistoryPagePayload
			if env.Type != protocol.TypeHistoryPage || env.InReplyTo == nil || *env.InReplyTo != requestID || json.Unmarshal(env.Payload, &page) != nil || len(page.Entries) == 0 {
				t.Fatal("history request did not return a legacy page")
			}
			for _, entry := range page.Entries {
				result.page = append(result.page, testShadowLegacyPayload(t, entry.Type, entry.Payload))
			}
			if page.AtStart {
				break
			}
			if page.Cursor == "" || page.Cursor == cursor {
				t.Fatal("history cursor did not advance")
			}
			cursor = page.Cursor
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
				t.Fatalf("legacy client surface changed: baseline=%v shadow=%v", baseline, got)
			}
		})
	}
}
