package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testShadow(t *testing.T) (*threadShadow, *history.Store, *conversations.Registry, *thread.Store) {
	t.Helper()
	h := history.New(t.TempDir())
	reg, err := conversations.Load("")
	if err != nil {
		t.Fatal(err)
	}
	store := thread.NewStore(h)
	s := newThreadShadow(h, reg, store, slog.Default())
	t.Cleanup(func() { s.shutdown() })
	return s, h, reg, store
}

func testShadowAppend(t *testing.T, h *history.Store, id conversations.ConversationID) uint64 {
	t.Helper()
	v, err := h.Append(id, protocol.TypeMessage, []byte(`{"role":"user","text":"private"}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func testShadowWait(t *testing.T, predicate func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for !predicate() {
		select {
		case <-ctx.Done():
			t.Fatal("shadow did not converge")
		case <-time.After(time.Millisecond):
		}
	}
}

func testShadowEqual(t *testing.T, h *history.Store, store *thread.Store, id conversations.ConversationID, version uint64) {
	t.Helper()
	testShadowWait(t, func() bool { return store.Snapshot(id).Version == version })
	f := thread.New(string(id))
	r, err := h.Forward(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Walk(context.Background(), version, f.Feed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Items(), store.Snapshot(id).Items) {
		t.Fatal("shadow differs from full replay")
	}
}

func TestThreadShadowDiscovery(t *testing.T) {
	s, h, reg, store := testShadow(t)
	s.quiet = func(conversations.ConversationID) bool { return false }
	a, b := conversations.ConversationID(testPostID(t)), conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: a})
	testShadowAppend(t, h, a)
	s.start()
	reg.Create(conversations.Conversation{ID: b})
	for range 3 {
		testShadowAppend(t, h, a)
		testShadowAppend(t, h, b)
	}
	testShadowEqual(t, h, store, a, 4)
	testShadowEqual(t, h, store, b, 3)
}

func TestThreadShadowReload(t *testing.T) {
	s, h, reg, store := testShadow(t)
	id := conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: id})
	testShadowAppend(t, h, id)
	s.start()
	testShadowWait(t, func() bool { return s.consumed(id) == 1 && store.Snapshot(id).State == thread.StateNotLoaded })
	s.quietMu.Lock()
	s.quiet = func(conversations.ConversationID) bool { return false }
	s.quietMu.Unlock()
	testShadowAppend(t, h, id)
	testShadowEqual(t, h, store, id, 2)
}

func TestThreadShadowRemoval(t *testing.T) {
	s, h, reg, store := testShadow(t)
	s.quiet = func(conversations.ConversationID) bool { return false }
	a, b := conversations.ConversationID(testPostID(t)), conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: a})
	reg.Create(conversations.Conversation{ID: b})
	testShadowAppend(t, h, a)
	testShadowAppend(t, h, b)
	entered := make(chan struct{})
	var once sync.Once
	store = thread.NewStore(h, func(ctx context.Context, id conversations.ConversationID) error {
		if id == a {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	s.store = store
	ring := eventring.New(10)
	ring.Append(string(a), "test", nil, time.Time{})
	dropRingOnConversationDelete(reg, ring, nil, s)
	s.start()
	<-entered
	deleted := make(chan bool, 1)
	go func() { deleted <- reg.Delete(a) }()
	testShadowWait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.removed[a] })
	if !<-deleted {
		t.Fatal("delete missed")
	}
	testShadowAppend(t, h, a)
	s.discover()
	if store.Snapshot(a).State != thread.StateNotLoaded {
		t.Fatal("removed conversation reloaded")
	}
	testShadowEqual(t, h, store, b, 1)
	if ring.NewestID(string(a)) != 0 {
		t.Fatal("removal lost ring cleanup")
	}
	reg.Delete(b) // joined tail processing also cannot be re-admitted
	testShadowAppend(t, h, b)
	s.discover()
	if store.Snapshot(b).State != thread.StateNotLoaded {
		t.Fatal("removed tail reloaded")
	}
}

func TestThreadShadowShutdown(t *testing.T) {
	s, h, reg, store := testShadow(t)
	s.quiet = func(conversations.ConversationID) bool { return false }
	id := conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: id})
	testShadowAppend(t, h, id)
	s.start()
	testShadowEqual(t, h, store, id, 1)
	for range 4 {
		testShadowAppend(t, h, id)
	}
	if err := s.shutdown(); err != nil {
		t.Fatal(err)
	}
	reopened := thread.NewStore(h)
	defer reopened.Shutdown()
	if err := reopened.Load(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	testShadowEqual(t, h, reopened, id, 5)
}

func TestThreadShadowLongReplay(t *testing.T) {
	s, h, reg, store := testShadow(t)
	s.quiet = func(conversations.ConversationID) bool { return false }
	a, b := conversations.ConversationID(testConvID), conversations.ConversationID(testConvIDB)
	reg.Create(conversations.Conversation{ID: a})
	reg.Create(conversations.Conversation{ID: b})
	for range 36000 {
		if _, err := h.Append(a, "ignored", []byte(`{}`), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var once sync.Once
	store = thread.NewStore(h, func(ctx context.Context, id conversations.ConversationID) error {
		if id == a {
			once.Do(func() { close(entered) })
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
		}
		return nil
	})
	s.store = store
	s.start()
	<-entered
	testShadowAppend(t, h, a) // committed while the scheduled refold is paused
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(nil, bcast, discardLogger())
	e.hist = h
	e.HandleFor(context.Background(), string(b), turnevent.TextChunk{Text: "live while paused"}, history.SessionProvenance{Kind: "claude", SessionID: "source-b"})
	e.HandleFor(context.Background(), string(b), turnevent.TurnEnd{}, history.SessionProvenance{Kind: "claude", SessionID: "source-b"})
	if len(bcast.pushes) == 0 {
		t.Fatal("normal connected live publication blocked")
	}
	version, err := h.LatestEntryID(b)
	if err != nil {
		t.Fatal(err)
	}
	testShadowEqual(t, h, store, b, version)
	unblock()
	testShadowEqual(t, h, store, a, 36001)
}

func TestThreadShadowQuiescence(t *testing.T) {
	h := history.New(t.TempDir())
	sink := newStreamTurnSink(32, discardLogger())
	busy := newTurnBusyTracker(constResolver(testConvID, true), discardLogger())
	d := testDelivery(t, t.TempDir(), h, nil)
	d.bind(busy)
	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	id := conversations.ConversationID(testConvID)
	check := func(want bool) {
		t.Helper()
		if got := shadowQuiescent(d, q, sink, id); got != want {
			t.Fatalf("quiet=%v want %v", got, want)
		}
	}
	check(true)
	d.active[testConvID] = true
	check(false)
	delete(d.active, testConvID)
	d.writes[testConvID] = 1
	check(false)
	delete(d.writes, testConvID)
	sink.runtimeHolds = map[string]int{testConvID: 1}
	busy.runtimeSink = sink
	check(false)
	delete(sink.runtimeHolds, testConvID)
	sink.queued = 1
	check(false)
	sink.shadowProcessed.Store(1)
	check(true)
	q.Enqueue(testConvID, "text")
	check(false)
}

func TestThreadShadowLegacy(t *testing.T) {
	type surface struct{ live, replay, page []string }
	run := func(enabled bool) surface {
		h := history.New(t.TempDir())
		reg, _ := conversations.Load("")
		reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID)})
		store := thread.NewStore(h)
		s := newThreadShadow(h, reg, store, discardLogger())
		s.quiet = func(conversations.ConversationID) bool { return false }
		if enabled {
			s.start()
		} else {
			s.store = nil
		}
		defer s.shutdown()
		b := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}}}}
		e := newInteractiveTurnEmitterV2(&activeConversation{}, b, discardLogger())
		e.hist, e.runtimeFacts = h, true
		source := history.SessionProvenance{Kind: "claude", SessionID: "saved-source"}
		e.HandleFor(context.Background(), testConvID, turnevent.TextChunk{Text: "answer"}, source)
		e.HandleFor(context.Background(), testConvID, turnevent.TurnEnd{}, source)
		transitions := newSessionTransitionEmitterV2(b, constResolver(testConvID, true), discardLogger())
		transitions.hist = h
		transitions.broadcast(context.Background(), sessions.SessionTransition{ConversationID: testConvID, PreviousID: "saved-source", NewID: "successor", PreviousAgent: "claude", NextAgent: "codex", Reason: sessions.ReasonClear, OccurredAt: occurred})
		sends := testSendHistory(h)
		sends.accepted(testConvID, msgqueue.QueuedMessage{MessageID: "send", Text: "operator"})
		operatorMessageHistory(h, nil, nil, discardLogger(), sends)(testConvID, msgqueue.QueuedMessage{MessageID: "send", Text: "operator"})
		d := testDelivery(t, t.TempDir(), h, nil)
		testAccept(t, d, conversations.ConversationID(testConvID), "post")
		d.drain()
		var result surface
		normalized := func(typ string, raw []byte) string {
			var object map[string]any
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			delete(object, "turn_id")
			delete(object, "timestamp")
			delete(object, "ts")
			bytes, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			return typ + ":" + string(bytes)
		}
		for _, p := range b.pushes {
			if strings.Contains(p.env.Type, "thread") {
				t.Fatal("thread frame")
			}
			result.live = append(result.live, normalized(p.env.Type, p.env.Payload))
		}
		events, gap := e.ring.After(testConvID, 0)
		if gap {
			t.Fatal("replay gap")
		}
		for _, ev := range events {
			result.replay = append(result.replay, normalized(ev.Type, ev.Payload))
		}
		for _, entry := range newHistoryPager(h, discardLogger())(testConvID, "", 100).Entries {
			result.page = append(result.page, normalized(entry.Type, entry.Payload))
		}
		if enabled {
			v, _ := h.LatestEntryID(conversations.ConversationID(testConvID))
			testShadowEqual(t, h, store, conversations.ConversationID(testConvID), v)
		}
		return result
	}
	if a, b := run(false), run(true); !reflect.DeepEqual(a, b) {
		t.Fatalf("legacy surface changed:\n%v\n%v", a, b)
	}
}

func TestThreadShadowFailure(t *testing.T) {
	s, h, reg, store := testShadow(t)
	var logs bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	id := conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: id})
	testShadowAppend(t, h, id)
	dir, err := h.LogDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "thread-recovery.json"), 0700); err != nil {
		t.Fatal(err)
	}
	s.start()
	testShadowWait(t, func() bool { return store.Snapshot(id).State == thread.StateUnavailable })
	testShadowAppend(t, h, id) // failed shadow persistence never disables history
	if err := s.shutdown(); err == nil {
		t.Fatal("failed storage certified clean shutdown")
	}
	for _, secret := range []string{"private", dir, "thread_shadow.clean_exit"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("unsafe/clean failure diagnostic: %s", logs.String())
		}
	}
}

type testShadowRetirement struct {
	*thread.Store
	entered, release chan struct{}
	once             sync.Once
}

func (s *testShadowRetirement) Unload(id conversations.ConversationID) error {
	err := s.Store.Unload(id)
	s.once.Do(func() { close(s.entered); <-s.release })
	return err
}
func TestThreadShadowRetirementCommit(t *testing.T) {
	s, h, reg, store := testShadow(t)
	id := conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: id})
	testShadowAppend(t, h, id)
	gate := &testShadowRetirement{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	s.store = gate
	s.start()
	<-gate.entered
	s.quietMu.Lock()
	s.quiet = func(conversations.ConversationID) bool { return false }
	s.quietMu.Unlock()
	d := testDelivery(t, t.TempDir(), h, nil)
	testAccept(t, d, id, "post during retirement")
	d.drain()
	version, err := h.LatestEntryID(id)
	if err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	testShadowEqual(t, h, store, id, version)
}

type testShadowUnavailable struct{ *thread.Store }

func (s testShadowUnavailable) Snapshot(conversations.ConversationID) thread.Snapshot {
	return thread.Snapshot{State: thread.StateUnavailable}
}
func TestThreadShadowFailedDrain(t *testing.T) {
	s, h, reg, store := testShadow(t)
	id := conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: id})
	testShadowAppend(t, h, id)
	s.store = testShadowUnavailable{Store: store}
	s.discover()
	// Persistence can succeed with no records; failed final folding still cannot
	// claim a clean shadow exit, and it must not wait forever for a usable view.
	if err := s.shutdown(); err == nil {
		t.Fatal("failed final fold certified clean exit")
	}
}
