package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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
	s, h, reg, _ := testShadow(t)
	s.quiet = func(conversations.ConversationID) bool { return false }
	a, b, c := conversations.ConversationID(testPostID(t)), conversations.ConversationID(testPostID(t)), conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: a})
	reg.Create(conversations.Conversation{ID: b})
	reg.Create(conversations.Conversation{ID: c, IsPromoted: true})
	testShadowAppend(t, h, c)
	testShadowAppend(t, h, a)
	testShadowAppend(t, h, b)
	entered, tailEntered, tailCancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var tail atomic.Bool
	var once sync.Once
	store := thread.NewStore(h, func(ctx context.Context, id conversations.ConversationID) error {
		if id == a {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			return ctx.Err()
		}
		if id == b && tail.Load() {
			close(tailEntered)
			<-ctx.Done()
			close(tailCancelled)
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
	tail.Store(true)
	testShadowAppend(t, h, b)
	awaitSwitchPublication(t, tailEntered) // the actual tail chunk is in flight
	testShadowAppend(t, h, c)
	testShadowEqual(t, h, store, c, 2)
	deleted = make(chan bool, 1)
	go func() { deleted <- conversations.Sweep(reg, time.Now()) == 1 }()
	if !<-deleted {
		t.Fatal("tail sweep missed")
	}
	select {
	case <-tailCancelled:
	default:
		t.Fatal("removal returned before tail cancellation/join")
	}
	testShadowAppend(t, h, b)
	s.discover()
	if store.Snapshot(b).State != thread.StateNotLoaded {
		t.Fatal("removed tail reloaded")
	}
	testShadowAppend(t, h, c)
	testShadowEqual(t, h, store, c, 3)
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
	s, h, reg, _ := testShadow(t)
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
	store := thread.NewStore(h, func(ctx context.Context, id conversations.ConversationID) error {
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
	for _, failure := range []string{"worker", "discovery"} {
		t.Run(failure, func(t *testing.T) {
			s, h, reg, store := testShadow(t)
			var logs bytes.Buffer
			s.log = slog.New(slog.NewTextHandler(&logs, nil))
			id := conversations.ConversationID(testPostID(t))
			reg.Create(conversations.Conversation{ID: id})
			testShadowAppend(t, h, id)
			if failure == "worker" {
				s.store = testShadowUnavailable{Store: store}
				s.discover()
			} else {
				dir, err := h.LogDir(id)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(dir, dir+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+"-saved", dir); err != nil {
					t.Fatal(err)
				}
			}
			// Empty Store persistence can succeed even though a registered
			// conversation's final boundary was never established or folded.
			if err := s.shutdown(); err == nil {
				t.Fatal("failed final drain certified clean exit")
			}
			if strings.Contains(logs.String(), "thread_shadow.clean_exit") {
				t.Fatal("failed final drain logged clean exit")
			}
		})
	}
}

func TestThreadShadowStartup(t *testing.T) {
	// Pin construction ordering as well as exercising old-history quiescence.
	source := formattedGoFunc(t, "main.go", "runSupervisor")
	if strings.Index(source, "shadow.start()") < strings.Index(source, "approvalSurfaces.set(approvalSurface)") {
		t.Fatal("shadow admission precedes completed relay/runtime construction")
	}
	s, h, reg, store := testShadow(t)
	id := conversations.ConversationID(testConvID)
	reg.Create(conversations.Conversation{ID: id})
	testShadowAppend(t, h, id)
	reconcileStartupHistory(h, reg, discardLogger(), time.Now())
	sink := newStreamTurnSink(32, discardLogger())
	busy := newTurnBusyTracker(constResolver(testConvID, true), discardLogger())
	d := testDelivery(t, t.TempDir(), h, nil)
	d.bind(busy)
	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop, _, _, _, _, _, err := startRelay(ctx, discardLogger(), relayWiring{convReg: reg, active: &activeConversation{}, hist: h, streamSink: sink, busy: busy, shadow: s, transitions: testShadowTransitions{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); stop() }()
	read := make(chan struct{})
	var once sync.Once
	s.quiet = func(id conversations.ConversationID) bool {
		once.Do(func() { close(read) })
		return shadowQuiescent(d, q, sink, id)
	}
	s.start()
	awaitSwitchPublication(t, read)
	testShadowWait(t, func() bool { return s.consumed(id) > 0 && store.Snapshot(id).State == thread.StateNotLoaded })
}

func TestThreadShadowDiscoveryRetries(t *testing.T) {
	s, h, reg, store := testShadow(t)
	s.quiet = func(conversations.ConversationID) bool { return false }
	id := conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: id})
	testShadowAppend(t, h, id)
	dir, err := h.LogDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir+"-saved", dir); err != nil {
		t.Fatal(err)
	}
	if s.discover() {
		t.Fatal("failed discovery established a boundary")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+"-saved", dir); err != nil {
		t.Fatal(err)
	}
	s.start()
	testShadowEqual(t, h, store, id, 1)
	if err := s.shutdown(); err != nil {
		t.Fatal("ordinary discovery failure was not retryable")
	}
}

type testShadowTransitions struct{}

func (testShadowTransitions) SetTransitionObserver(sessions.TransitionObserver) {}
