package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadGapPreservesAgentIneligiblePeer(t *testing.T) {
	b := testControlBindings(t, testConvID, "a")
	b.reg.Create(conversations.Conversation{ID: testConvIDB})
	h := history.New(t.TempDir())
	store := thread.NewStore(h)
	s := newThreadShadow(h, b.reg, store, discardLogger())
	t.Cleanup(func() { s.shutdown() })
	wire, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) {
		installDaemonThreadRelay(cfg, s)
		cfg.CodexConversation = func(id string) bool { return id == testConvIDB }
	}, func(ctx context.Context, m *relay.V2SessionManager) func() {
		return startDaemonThreadPublisher(ctx, s, m)
	})
	wire.open("phone", false, protocol.CapabilityThread)
	wire.open("again", true, protocol.CapabilityThread)
	for _, id := range []conversations.ConversationID{testConvID, testConvIDB} {
		testShadowAppend(t, h, id)
		if err := store.Load(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		testShadowWait(t, func() bool { return store.Observe(id).State == thread.StateUsable })
	}
	codex := &shadowConversation{}
	s.publisher.publish(t.Context(), testConvIDB, codex)
	if err := mgr.FlushPushes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(codex.threadPeers) != 0 {
		t.Error("boundary acquisition recorded a live push")
	}
	s.publisher.gap(t.Context(), testConvIDB, codex, "baseline_required")
	closed := wire.await()
	if closed.ConnID != "again" || closed.CloseCode != uint16(relay.StatusProducerGap) {
		t.Fatalf("gap retired an unaffected peer: %#v", closed)
	}
	testShadowWait(t, func() bool { return len(mgr.ActiveConns(t.Context())) < 2 })
	peers := mgr.ActiveConns(t.Context())
	if len(peers) != 1 || peers[0].ConnID != "phone" {
		t.Fatalf("unaffected basic connection disappeared: %#v", peers)
	}
	cursor := &shadowConversation{}
	s.publisher.publish(t.Context(), testConvID, cursor)
	v := testShadowAppend(t, h, testConvID)
	testShadowWait(t, func() bool { return store.Status(testConvID).Version == v })
	s.publisher.publish(t.Context(), testConvID, cursor)
	peer, e := wire.read()
	var added protocol.ThreadItemAddedPayload
	if err := json.Unmarshal(e.Payload, &added); err != nil {
		t.Fatal(err)
	}
	items := store.Observe(testConvID).Items
	if peer != "phone" || e.Type != protocol.TypeThreadItemAdded || added.ConversationID != testConvID || added.Item.ID != items[1].ID {
		t.Fatalf("unaffected conversation stopped publishing: %s / %#v", peer, e)
	}
}
