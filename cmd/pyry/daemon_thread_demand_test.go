package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadDemandIdleReadKeepsConnection(t *testing.T) {
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	testShadowAppend(t, h, testConvID)
	var idle atomic.Bool
	s.quiet = func(conversations.ConversationID) bool { return idle.Load() }
	w, mgr := testDaemonThreadQueryWire(t, s, nil)
	w.open("phone", false, protocol.CapabilityThread)
	s.start()
	testDaemonThreadRequest(t, w, "phone", 449, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID})
	initial, typ := testDaemonThreadReply(t, w, 449)
	if typ != protocol.TypeThreadItems || !initial.Final || initial.Version != 1 {
		t.Fatalf("initial recovery: %s", typ)
	}
	idle.Store(true)
	for attempt := range 3 {
		testShadowWait(t, func() bool { return store.Status(testConvID).State == thread.StateNotLoaded })
		// Let the follower observe the deliberate absence after a clean unload.
		select {
		case <-time.After(120 * time.Millisecond):
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
		if len(mgr.ActiveConns(t.Context())) != 1 {
			t.Fatal("clean idle retirement disconnected the reader")
		}
		id := uint64(450 + attempt)
		testDaemonThreadRequest(t, w, "phone", id, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID})
		got, typ := testDaemonThreadReply(t, w, id)
		if typ != protocol.TypeThreadItems || got.Version != 1 || !got.Final {
			t.Fatal("idle read lost its checkpoint")
		}
	}
}

func TestDaemonThreadDemandRecovery(t *testing.T) {
	for _, mode := range []string{"empty", "newest", "checkpoint", "older"} {
		t.Run(mode, func(t *testing.T) {
			s, h, reg, store := testShadow(t)
			reg.Create(conversations.Conversation{ID: testConvID})
			var saved thread.QueryResult
			if mode != "empty" {
				for range 3 {
					testShadowAppend(t, h, testConvID)
				}
				saved = testDaemonThreadLoad(t, store, 3)
				s.start()
				testShadowWait(t, func() bool {
					return s.consumed(testConvID) == 3 && store.Status(testConvID).State == thread.StateNotLoaded
				})
			}
			cfg := relay.V2SessionConfig{}
			installDaemonThreadRelay(&cfg, s)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			var got thread.QueryResult
			var err error
			if mode == "older" {
				got, err = cfg.ThreadPage(ctx, protocol.ThreadPagePayload{ConversationID: testConvID, BeforeOrder: 3, Limit: 50})
			} else {
				request := protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 50}
				if mode == "checkpoint" {
					request.Epoch, request.Version = saved.Epoch, 1
				}
				got, err = cfg.ThreadCatchUp(ctx, request)
			}
			if err != nil || got.Err != nil || got.State != thread.StateUsable || got.Epoch == "" || got.ResetRequired {
				t.Fatalf("cold %s query: %#v %v", mode, got, err)
			}
			if mode == "empty" {
				if got.Version != 0 || len(got.Items) != 0 {
					t.Fatal("empty thread invented history")
				}
			} else if got.Version != 3 || len(got.Items) == 0 || got.Epoch != saved.Epoch {
				t.Fatal("idle history/checkpoint lost")
			}
			if mode == "older" && len(got.Items) != 2 {
				t.Fatalf("older page: %d rows", len(got.Items))
			}
			if mode == "checkpoint" && got.FromVersion != 1 {
				t.Fatal("saved checkpoint not used")
			}
			testShadowWait(t, func() bool { return store.Status(testConvID).State == thread.StateNotLoaded })
		})
	}
}

func TestDaemonThreadDemandWireAfterIdle(t *testing.T) {
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	testShadowAppend(t, h, testConvID)
	s.start()
	testShadowWait(t, func() bool {
		return s.consumed(testConvID) == 1 && store.Status(testConvID).State == thread.StateNotLoaded
	})
	w, _ := testDaemonThreadQueryWire(t, s, nil)
	w.open("phone", false, protocol.CapabilityThread)
	testDaemonThreadRequest(t, w, "phone", 400, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID})
	got, typ := testDaemonThreadReply(t, w, 400)
	if typ != protocol.TypeThreadItems || got.Version != 1 || len(got.Items) != 1 || !got.Final {
		t.Fatal("idle baseline was not certified")
	}
}

func TestDaemonThreadDemandCancellation(t *testing.T) {
	s, h, reg, _ := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	testShadowAppend(t, h, testConvID)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	store := thread.NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	s.store = store
	cfg := relay.V2SessionConfig{}
	installDaemonThreadRelay(&cfg, s)
	first, cancel := context.WithCancel(t.Context())
	second, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := cfg.ThreadCatchUp(first, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 50})
		firstDone <- err
	}()
	<-entered
	go func() {
		_, err := cfg.ThreadCatchUp(second, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 50})
		secondDone <- err
	}()
	testShadowWait(t, func() bool {
		s.quietMu.Lock()
		defer s.quietMu.Unlock()
		return s.queryDemands[testConvID] != nil && s.queryDemands[testConvID].count == 2
	})
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	if store.Status(testConvID).State != thread.StateRebuilding {
		t.Fatal("one cancellation retired the shared worker")
	}
	close(release)
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(testConvID).State == thread.StateNotLoaded })
}

func TestDaemonThreadDemandProtectsRendering(t *testing.T) {
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	testShadowAppend(t, h, testConvID)
	idleCheck, rendering, release := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	s.quiet = func(conversations.ConversationID) bool {
		select {
		case idleCheck <- struct{}{}:
		default:
		}
		return true
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.queryThread(t.Context(), testConvID, store, func(ctx context.Context) (thread.QueryResult, error) {
			close(rendering)
			select {
			case <-release:
				return store.CatchUp(ctx, testConvID, "", 0, 50)
			case <-ctx.Done():
				return thread.QueryResult{}, ctx.Err()
			}
		})
		done <- err
	}()
	<-rendering
	<-idleCheck // the production follower actually considered idle retirement
	if store.Status(testConvID).State != thread.StateUsable {
		t.Fatal("thread unloaded during query rendering")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(testConvID).State == thread.StateNotLoaded })
}

func TestDaemonThreadDemandTeardown(t *testing.T) {
	for _, mode := range []string{"remove", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			s, h, reg, _ := testShadow(t)
			reg.Create(conversations.Conversation{ID: testConvID})
			testShadowAppend(t, h, testConvID)
			entered, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			store := thread.NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
				once.Do(func() { close(entered) })
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			s.store = store
			cfg := relay.V2SessionConfig{}
			installDaemonThreadRelay(&cfg, s)
			done := make(chan error, 1)
			go func() {
				_, err := cfg.ThreadCatchUp(t.Context(), protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 50})
				done <- err
			}()
			<-entered
			stopped := make(chan struct{})
			go func() {
				if mode == "remove" {
					reg.Delete(testConvID)
					s.remove(testConvID)
				} else {
					_ = s.shutdown()
				}
				close(stopped)
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("teardown result: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("teardown did not cancel demand")
			}
			releaseOnce.Do(func() { close(release) })
			<-stopped
			if _, err := cfg.ThreadCatchUp(t.Context(), protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 50}); err == nil {
				t.Fatal("request admitted after teardown")
			}
		})
	}
}

type demandRetirementStore struct {
	shadowStore
	entered, release chan struct{}
	once             sync.Once
}

func (s *demandRetirementStore) Unload(id conversations.ConversationID) error {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.shadowStore.Unload(id)
}

func TestDaemonThreadDemandCancelledDuringRetirement(t *testing.T) {
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	testShadowAppend(t, h, testConvID)
	cfg := relay.V2SessionConfig{}
	installDaemonThreadRelay(&cfg, s)
	blocked := &demandRetirementStore{shadowStore: store, entered: make(chan struct{}), release: make(chan struct{})}
	s.store = blocked
	t.Cleanup(func() { close(blocked.release) })
	s.start()
	<-blocked.entered
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := cfg.ThreadCatchUp(ctx, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 50})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request waited on unrelated retirement I/O after cancellation")
	}
}

func TestDaemonThreadDemandRefusals(t *testing.T) {
	s, h, reg, _ := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	cfg := relay.V2SessionConfig{}
	installDaemonThreadRelay(&cfg, s)
	if _, err := cfg.ThreadCatchUp(t.Context(), protocol.ThreadCatchUpPayload{ConversationID: testPostID(t), Limit: 50}); err == nil {
		t.Fatal("unknown conversation admitted")
	}
	testShadowAppend(t, h, testConvID)
	store := thread.NewStore(h, func(context.Context, conversations.ConversationID) error { return errors.New("fixture read failure") })
	s.store = store
	installDaemonThreadRelay(&cfg, s)
	if _, err := cfg.ThreadCatchUp(t.Context(), protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 50}); !errors.Is(err, thread.ErrUnavailable) {
		t.Fatalf("unavailable view: %v", err)
	}
}
