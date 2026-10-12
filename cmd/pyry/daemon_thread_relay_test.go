package main

import (
	"context"
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadChanges(t *testing.T) {
	c := thread.Change{ID: 1, PreviousRev: 1, Rev: 3, Version: 4, Fields: map[string]json.RawMessage{"Summary": json.RawMessage(`"combined"`), "Active": json.RawMessage(`false`), "Content": json.RawMessage(`{"text":"before","other":true}`)}, TextAppend: "after"}
	updates := daemonThreadUpdates("conv", "epoch", c)
	if len(updates) != 2 {
		t.Fatalf("combined update count %d", len(updates))
	}
	change := updates[0].(protocol.ThreadItemChangedPayload)
	append := updates[1].(protocol.ThreadTextAppendPayload)
	if change.BaseRev != 1 || change.Rev != 3 || append.BaseRev != 3 || append.Rev != 3 || append.Text != "after" {
		t.Fatal("revision chain lost")
	}
	if !reflect.DeepEqual(change.Changes, map[string]json.RawMessage{"summary": json.RawMessage(`"combined"`), "active": json.RawMessage(`false`), "content": json.RawMessage(`{"text":"before","other":true}`)}) {
		t.Fatal("wire fields changed")
	}
}

func TestDaemonThreadReadiness(t *testing.T) {
	s, _, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvIDB})
	b := testControlBindings(t, "conv", "a")
	cfg := relay.V2SessionConfig{}
	installDaemonLiveRelay(&cfg, relayWiring{streamSink: b.sink}, nil)
	installDaemonThreadRelay(&cfg, s)
	if cfg.ThreadReady != nil && cfg.ThreadReady() {
		t.Fatal("missing publisher granted thread")
	}
	if _, ok := cfg.ThreadLastShownVersion(testConvIDB); ok {
		t.Fatal("not-loaded watermark")
	}
	if err := store.Load(t.Context(), testConvIDB); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Snapshot(testConvIDB).State == thread.StateUsable })
	if v, ok := cfg.ThreadLastShownVersion(testConvIDB); !ok || v != 0 {
		t.Fatal("usable zero lost")
	}
	if _, ok := cfg.ThreadLastShownVersion("missing"); ok {
		t.Fatal("missing member watermark")
	}
	if err := store.Unload(testConvIDB); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonThreadContinuity(t *testing.T) {
	_, h, reg, store := testShadow(t)
	id := conversations.ConversationID(testConvID)
	reg.Create(conversations.Conversation{ID: id})
	testShadowAppend(t, h, id)
	if err := store.Load(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Snapshot(id).State == thread.StateUsable })
	var frames []protocol.Envelope
	var retired []string
	p := &daemonThreadPublisher{store: store, active: func(context.Context, string) []relay.ActiveConn {
		return []relay.ActiveConn{{ConnID: "phone", Interactive: true, Thread: true}}
	}, push: func(_ context.Context, _ string, e protocol.Envelope) error { frames = append(frames, e); return nil }, retire: func(_ context.Context, id string) error { retired = append(retired, id); return nil }, log: discardLogger()}
	w := &shadowConversation{}
	p.publish(t.Context(), id, w)
	if len(frames) != 1 {
		t.Fatalf("baseline count %d", len(frames))
	}
	before := store.Snapshot(id)
	if err := store.Unload(id); err != nil {
		t.Fatal(err)
	}
	testShadowAppend(t, h, id)
	if err := store.Load(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Snapshot(id).Version == 2 })
	if store.Snapshot(id).Epoch != before.Epoch {
		t.Fatal("fixture did not reuse epoch")
	}
	p.publish(t.Context(), id, w)
	if !reflect.DeepEqual(retired, []string{"phone"}) {
		t.Fatalf("gap did not retire: %v", retired)
	}
	if len(frames) != 3 {
		t.Fatalf("fresh complete baseline count %d", len(frames))
	}
	for _, e := range frames {
		if e.Type != protocol.TypeThreadItemAdded {
			t.Fatal("unproven delta emitted")
		}
	}
}

func TestDaemonThreadPublication(t *testing.T) {
	b := testControlBindings(t, testConvID, "a")
	b.reg.Create(conversations.Conversation{ID: testConvIDB})
	h := history.New(t.TempDir())
	store := thread.NewStore(h)
	s := newThreadShadow(h, b.reg, store, discardLogger())
	s.quiet = func(conversations.ConversationID) bool { return false }
	t.Cleanup(func() { s.shutdown() })
	w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) {
		installDaemonThreadRelay(cfg, s)
		cfg.CodexConversation = func(id string) bool { return id == testConvIDB }
	}, func(ctx context.Context, m *relay.V2SessionManager) func() {
		stop := startDaemonThreadPublisher(ctx, s, m)
		live := startDaemonLivePublisher(ctx, b.sink.live, m.ActiveConns, m.PushLiveState, func(ctx context.Context, id string) { _ = m.RetireLiveState(ctx, id) }, discardLogger())
		return func() { stop(); live() }
	})
	t.Log("phase: handshake phone")
	w.open("phone", false, protocol.CapabilityThread)
	t.Log("phase: handshake legacy")
	w.open("legacy", false)
	t.Log("phase: handshake again")
	w.open("again", true, protocol.CapabilityThread)
	var cfg relay.V2SessionConfig
	installDaemonLiveRelay(&cfg, relayWiring{streamSink: b.sink}, nil)
	installDaemonThreadRelay(&cfg, s)
	if cfg.ThreadReady == nil || !cfg.ThreadReady() {
		t.Fatal("installed production publisher unavailable")
	}
	// Each recipient keeps separate conversation state and continuation assembly.
	rows := map[string]map[string]map[uint64]protocol.ThreadItem{}
	fragments := map[string]string{}
	updates := map[string][]protocol.Envelope{}
	continued := false
	apply := func(peer string, e protocol.Envelope) string {
		t.Helper()
		raw, _ := json.Marshal(e)
		if len(raw) > protocol.MaxThreadEnvelopeBytes {
			t.Fatal("final envelope exceeds cap")
		}
		var part protocol.ThreadUpdatePart
		if err := json.Unmarshal(e.Payload, &part); err != nil {
			t.Fatal(err)
		}
		if part.Continuation != nil {
			continued = true
			key := peer + part.Continuation.UpdateID
			if uint64(len(fragments[key])) != part.Continuation.Offset {
				t.Fatal("continuation offset discontinuity")
			}
			fragments[key] += part.Data
			if !part.Continuation.Final {
				return ""
			}
			if uint64(len(fragments[key])) != part.Continuation.TotalBytes {
				t.Fatal("incomplete logical update")
			}
			e.Payload = json.RawMessage(fragments[key])
			delete(fragments, key)
		}
		var added protocol.ThreadItemAddedPayload
		updates[peer] = append(updates[peer], e)
		var changed protocol.ThreadItemChangedPayload
		var appended protocol.ThreadTextAppendPayload
		_ = json.Unmarshal(e.Payload, &added)
		conv := added.ConversationID
		if rows[peer] == nil {
			rows[peer] = map[string]map[uint64]protocol.ThreadItem{}
		}
		if rows[peer][conv] == nil {
			rows[peer][conv] = map[uint64]protocol.ThreadItem{}
		}
		items := rows[peer][conv]
		switch e.Type {
		case protocol.TypeThreadItemAdded:
			items[added.Item.ID] = added.Item
		case protocol.TypeThreadItemChanged:
			if err := json.Unmarshal(e.Payload, &changed); err != nil {
				t.Fatal(err)
			}
			prior := items[changed.ItemID]
			if prior.Rev != changed.BaseRev {
				t.Fatalf("change base %d != %d", prior.Rev, changed.BaseRev)
			}
			raw, _ := json.Marshal(prior)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			for k, v := range changed.Changes {
				fields[k] = v
			}
			fields["rev"], _ = json.Marshal(changed.Rev)
			raw, _ = json.Marshal(fields)
			_ = json.Unmarshal(raw, &prior)
			items[prior.ID] = prior
		case protocol.TypeThreadTextAppend:
			if err := json.Unmarshal(e.Payload, &appended); err != nil {
				t.Fatal(err)
			}
			prior := items[appended.ItemID]
			if prior.Rev != appended.BaseRev {
				t.Fatal("append base mismatch")
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(prior.Content, &fields)
			var text string
			_ = json.Unmarshal(fields["text"], &text)
			fields["text"], _ = json.Marshal(text + appended.Text)
			prior.Content, _ = json.Marshal(fields)
			prior.Rev = appended.Rev
			items[prior.ID] = prior
		default:
			t.Fatalf("unsolicited content reached thread: %s", e.Type)
		}
		return conv
	}
	appendFact := func(conv, typ string, payload any) uint64 {
		raw, _ := json.Marshal(payload)
		id, err := h.Append(conversations.ConversationID(conv), typ, raw, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	await := func(conv string, version uint64, peers ...string) {
		t.Helper()
		t.Logf("phase: fold/publication conversation=%s version=%d peers=%v", conv, version, peers)
		testShadowWait(t, func() bool { return store.Snapshot(conversations.ConversationID(conv)).Version == version })
		expected := map[uint64]protocol.ThreadItem{}
		for _, i := range store.Observe(conversations.ConversationID(conv)).Items {
			expected[i.ID] = daemonThreadItem(i)
		}
		pending := map[string]bool{}
		for _, p := range peers {
			pending[p] = true
		}
		for len(pending) > 0 {
			peer, e := w.read()
			if peer == "legacy" {
				t.Fatal("thread leaked to legacy")
			}
			if conv == testConvIDB && peer == "phone" {
				t.Fatal("thread granted agent access")
			}
			if apply(peer, e) == conv && reflect.DeepEqual(rows[peer][conv], expected) {
				delete(pending, peer)
			}
		}
	}

	// Linked delivery is present in the complete baseline; split public delivery
	// publications are continuity loss and are covered by the gap test.
	initialSends := testSendHistory(h)
	initialMessage := msgqueue.QueuedMessage{ID: 2, MessageID: "delivered", Text: "delivered", TS: time.Now().UTC()}
	initialSends.accepted(testConvID, initialMessage)
	operatorMessageHistory(h, func(operatorMessage) {}, nil, discardLogger(), initialSends)(testConvID, initialMessage)
	v := appendFact(testConvID, protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: testConvID, TurnID: "turn", Text: "first"})
	s.start()
	await(testConvID, v, "phone", "again")
	started := time.Now()
	previousRev := v
	updates = map[string][]protocol.Envelope{}
	v = appendFact(testConvID, protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: testConvID, TurnID: "turn", Text: " second"})
	await(testConvID, v, "phone", "again")
	if time.Since(started) > 250*time.Millisecond {
		t.Fatal("publication waiting exceeded 250 ms")
	}
	for _, peer := range []string{"phone", "again"} {
		got := updates[peer]
		if len(got) != 2 || got[0].Type != protocol.TypeThreadItemChanged || got[1].Type != protocol.TypeThreadTextAppend {
			t.Fatalf("%s combined delivery did not replace before appending: %#v", peer, got)
		}
		var changed protocol.ThreadItemChangedPayload
		var appended protocol.ThreadTextAppendPayload
		if err := json.Unmarshal(got[0].Payload, &changed); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(got[1].Payload, &appended); err != nil {
			t.Fatal(err)
		}
		if changed.BaseRev != previousRev || changed.Rev != v || appended.BaseRev != v || appended.Rev != v || changed.ItemID != appended.ItemID || string(changed.Changes["summary"]) != `"first second"` || appended.Text != " second" {
			t.Fatalf("%s combined wire revision chain: %#v / %#v", peer, changed, appended)
		}
	}
	v = appendFact(testConvIDB, protocol.TypeMessage, map[string]any{"role": "user", "text": "other"})
	await(testConvIDB, v, "again")

	sends := testSendHistory(h)
	message := msgqueue.QueuedMessage{ID: 1, MessageID: "dropped", Text: "queued", TS: time.Now().UTC()}
	sends.accepted(testConvID, message)
	v, _ = h.LatestEntryID(testConvID)
	await(testConvID, v, "phone", "again")
	sends.terminal(testConvID, message, msgqueue.TerminalRemoved)
	v, _ = h.LatestEntryID(testConvID)
	await(testConvID, v, "phone", "again")
	dropped := false
	for _, i := range rows["phone"][testConvID] {
		if i.Status == "dropped" {
			dropped = !i.Shown
		}
	}
	if !dropped {
		t.Fatal("dropped acceptance item was removed or left shown")
	}
	transitions := newSessionTransitionEmitterV2(historyOnlyBroadcaster{}, constResolver(testConvID, true), discardLogger())
	transitions.hist = h
	transitions.broadcast(t.Context(), sessions.SessionTransition{ConversationID: testConvID, PreviousID: "accepted-source", NewID: "next", PreviousAgent: "claude", NextAgent: "claude", Reason: sessions.ReasonClear, OccurredAt: time.Now().UTC()})
	v, _ = h.LatestEntryID(testConvID)
	await(testConvID, v, "phone", "again")
	delivery := testDelivery(t, t.TempDir(), h, nil)
	testAccept(t, delivery, testConvID, "post")
	delivery.drain()
	v, _ = h.LatestEntryID(testConvID)
	await(testConvID, v, "phone", "again")
	// An escaping-heavy full addition uses the reviewed continuation contract.
	v = appendFact(testConvID, protocol.TypeMessage, map[string]any{"role": "user", "text": strings.Repeat("<\\\"\n", 24000)})
	await(testConvID, v, "phone", "again")
	if !continued {
		t.Fatal("oversized fixture did not exercise continuation")
	}
	t.Log("phase: live reading")
	b.sink.sinkForSessionTag(newStreamSessionTag("a"), "claude")(turnevent.ModelAnnounced{Model: "current"})
	seen := map[string]bool{}
	for len(seen) < 2 {
		peer, e := w.read()
		if e.Type != protocol.TypeModelAnnounced {
			t.Fatal("wrong current reading")
		}
		if !e.SessionStateCleared {
			if string(e.ConversationID) != `"`+testConvID+`"` || string(e.SessionID) != `"a"` {
				t.Fatal("live source routing lost")
			}
			seen[peer] = true
		}
	}
	// Withheld families still reach the simultaneous legacy recipient unchanged.
	families := []string{protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd, protocol.TypeSessionTransition, protocol.TypeMessage, protocol.TypeBackgroundTaskStarted, protocol.TypeBackgroundTaskUpdated, protocol.TypeBackgroundTaskRoster, protocol.TypeQueueState, protocol.TypeBanner, protocol.TypeCompactionBoundary, protocol.TypeModelRefusalFallback, protocol.TypeModelRefusalNoFallback, protocol.TypeUnrecognizedMessage}
	for _, typ := range families {
		e := protocol.Envelope{ID: 80, Type: typ, TS: time.Unix(1, 0).UTC(), Payload: json.RawMessage(`{"conversation_id":"` + testConvID + `"}`)}
		for _, peer := range []string{"phone", "legacy", "again"} {
			if err := mgr.Push(t.Context(), peer, e); err != nil {
				t.Fatal(err)
			}
		}
		peer, got := w.read()
		if peer != "legacy" || !reflect.DeepEqual(got, e) {
			t.Fatalf("legacy stream changed: %s %s", peer, got.Type)
		}
	}
	if err := mgr.FlushPushes(t.Context()); err != nil {
		t.Fatal(err)
	}
	barrier := protocol.Envelope{Type: protocol.TypeError, Payload: json.RawMessage(`{}`)}
	if err := mgr.Push(t.Context(), "phone", barrier); err != nil {
		t.Fatal(err)
	}
	peer, e := w.read()
	if peer != "phone" || e.Type != protocol.TypeError {
		t.Fatal("withheld content escaped behind legacy delivery")
	}
	b.reg.Delete(testConvID)
	p := protocol.ThreadItemAddedPayload{ConversationID: testConvID, Epoch: "epoch", Item: protocol.ThreadItem{ID: 9, Rev: 9, Kind: "notice", Content: json.RawMessage(`{}`)}}
	raw, _ := json.Marshal(p)
	if err := mgr.Push(t.Context(), "phone", protocol.Envelope{Type: protocol.TypeThreadItemAdded, Payload: raw}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Push(t.Context(), "phone", barrier); err != nil {
		t.Fatal(err)
	}
	_, e = w.read()
	if e.Type != protocol.TypeError {
		t.Fatal("removed member received thread item")
	}
}

func TestDaemonThreadRetirement(t *testing.T) {
	for _, event := range []string{"delete", "stop"} {
		t.Run(event, func(t *testing.T) {
			s, h, reg, store := testShadow(t)
			s.quiet = func(conversations.ConversationID) bool { return false }
			id := conversations.ConversationID(testConvID)
			reg.Create(conversations.Conversation{ID: id})
			reg.Create(conversations.Conversation{ID: testConvIDB})
			stop := startDaemonThreadPublisher(t.Context(), s, &relay.V2SessionManager{})
			t.Cleanup(stop)
			p := s.publisher
			var absentLive relay.V2SessionConfig
			installDaemonThreadRelay(&absentLive, s)
			if absentLive.ThreadReady() {
				t.Fatal("missing live provider granted thread")
			}
			duplicate := startDaemonThreadPublisher(t.Context(), s, &relay.V2SessionManager{})
			duplicate()
			if s.publisher != p {
				t.Fatal("duplicate publisher replaced live owner")
			}
			entered := make(chan struct{})
			var once sync.Once
			p.active = func(context.Context, string) []relay.ActiveConn {
				return []relay.ActiveConn{{ConnID: "phone", Interactive: true, Thread: true}}
			}
			p.push = func(ctx context.Context, _ string, e protocol.Envelope) error {
				var payload struct {
					ConversationID string `json:"conversation_id"`
				}
				_ = json.Unmarshal(e.Payload, &payload)
				if payload.ConversationID == testConvID {
					once.Do(func() { close(entered) })
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}
			p.retire = func(context.Context, string) error { return nil }
			testShadowAppend(t, h, id)
			s.start()
			awaitSwitchPublication(t, entered)
			// Both this writer and another fold make progress while delivery is held.
			testShadowAppend(t, h, id)
			testShadowAppend(t, h, testConvIDB)
			testShadowWait(t, func() bool { return store.Observe(id).Version == 2 && store.Observe(testConvIDB).Version == 1 })
			done := make(chan struct{})
			go func() {
				defer close(done)
				if event == "delete" {
					s.remove(id)
				} else {
					stop()
				}
			}()
			awaitSwitchPublication(t, done)
			if event == "delete" {
				if store.Observe(id).State != thread.StateNotLoaded {
					t.Fatal("removed worker survived")
				}
				testShadowAppend(t, h, id)
				s.discover()
				if store.Observe(id).State != thread.StateNotLoaded {
					t.Fatal("late commit reopened removed publisher")
				}
			} else {
				if s.publisher != nil {
					t.Fatal("stop returned before releasing installation")
				}
				next := startDaemonThreadPublisher(t.Context(), s, &relay.V2SessionManager{})
				next()
			}
		})
	}
}

func TestDaemonThreadUnusable(t *testing.T) {
	s, h, reg, _ := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	testShadowAppend(t, h, testConvID)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	store := thread.NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return thread.ErrUnavailable
	})
	s.store = store
	var cfg relay.V2SessionConfig
	installDaemonThreadRelay(&cfg, s)
	p := &daemonThreadPublisher{store: store}
	if err := store.Load(t.Context(), testConvID); err != nil {
		t.Fatal(err)
	}
	awaitSwitchPublication(t, entered)
	for _, state := range []thread.SnapshotState{thread.StateRebuilding, thread.StateUnavailable} {
		testShadowWait(t, func() bool { return store.Observe(testConvID).State == state })
		if v, ok := cfg.ThreadLastShownVersion(testConvID); ok || v != 0 {
			t.Fatal("nonusable watermark fabricated")
		}
		// Any recipient enumeration or delivery would call an absent function.
		p.publish(t.Context(), testConvID, &shadowConversation{})
		if state == thread.StateRebuilding {
			close(release)
		}
	}
}

func TestDaemonThreadIdleCommit(t *testing.T) {
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	stop := startDaemonThreadPublisher(t.Context(), s, &relay.V2SessionManager{})
	t.Cleanup(stop)
	entered, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	var published atomic.Uint64
	p := s.publisher
	p.active = func(context.Context, string) []relay.ActiveConn {
		return []relay.ActiveConn{{ConnID: "phone", Interactive: true, Thread: true}}
	}
	p.retire = func(context.Context, string) error { return nil }
	p.push = func(ctx context.Context, _ string, e protocol.Envelope) error {
		first.Do(func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		var payload protocol.ThreadItemAddedPayload
		_ = json.Unmarshal(e.Payload, &payload)
		published.Store(payload.Version)
		return ctx.Err()
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	testShadowAppend(t, h, testConvID)
	s.start()
	awaitSwitchPublication(t, entered)
	testShadowAppend(t, h, testConvID)
	testShadowWait(t, func() bool { return store.Observe(testConvID).Version == 2 })
	close(release)
	testShadowWait(t, func() bool { return published.Load() == 2 })
}
