package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadQueryColdDemand(t *testing.T) {
	s, h, reg, store := testShadow(t)
	id := conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: id})
	cfg := relay.V2SessionConfig{}
	installDaemonThreadRelay(&cfg, s)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	empty, err := cfg.ThreadCatchUp(ctx, protocol.ThreadCatchUpPayload{ConversationID: string(id), Limit: 2})
	if err != nil || empty.State != thread.StateUsable || empty.Epoch == "" || empty.Version != 0 || len(empty.Items) != 0 {
		t.Fatalf("cold empty query: state=%v version=%d items=%d err=%v", empty.State, empty.Version, len(empty.Items), err)
	}
	s.start()
	testShadowWait(t, func() bool { return store.Status(id).State == thread.StateNotLoaded })
	v := testShadowAppend(t, h, id)
	testShadowWait(t, func() bool { return s.consumed(id) == v && store.Status(id).State == thread.StateNotLoaded })
	fresh, err := cfg.ThreadCatchUp(ctx, protocol.ThreadCatchUpPayload{ConversationID: string(id), Limit: 2})
	if err != nil || fresh.State != thread.StateUsable || fresh.Version != v || len(fresh.Items) != 1 {
		t.Fatalf("idle baseline query: state=%v version=%d items=%d err=%v", fresh.State, fresh.Version, len(fresh.Items), err)
	}
	testShadowWait(t, func() bool { return store.Status(id).State == thread.StateNotLoaded })
	delta, err := cfg.ThreadCatchUp(ctx, protocol.ThreadCatchUpPayload{ConversationID: string(id), Epoch: fresh.Epoch, Version: fresh.Version, Limit: 2})
	if err != nil || delta.State != thread.StateUsable || delta.ResetRequired || delta.FromVersion != fresh.Version {
		t.Fatalf("saved checkpoint query: reset=%v state=%v err=%v", delta.ResetRequired, delta.State, err)
	}
	page, err := cfg.ThreadPage(ctx, protocol.ThreadPagePayload{ConversationID: string(id), BeforeOrder: v + 1, Limit: 2})
	if err != nil || page.State != thread.StateUsable || len(page.Items) != 1 {
		t.Fatalf("idle page query: state=%v items=%d err=%v", page.State, len(page.Items), err)
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if _, err := cfg.ThreadCatchUp(canceled, protocol.ThreadCatchUpPayload{ConversationID: string(id), Limit: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	unknown := testPostID(t)
	if result, err := cfg.ThreadPage(ctx, protocol.ThreadPagePayload{ConversationID: unknown, BeforeOrder: 1, Limit: 2}); err == nil || result.State == thread.StateUsable {
		t.Fatal("unknown conversation acquired demand")
	}
	if store.Status(conversations.ConversationID(unknown)).State != thread.StateNotLoaded {
		t.Fatal("unknown query loaded a worker")
	}
}

func TestDaemonThreadQueryDemandCancellationIsPerOwner(t *testing.T) {
	s, h, reg, _ := testShadow(t)
	id := conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: id})
	testShadowAppend(t, h, id)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	store := thread.NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
		once.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	})
	s.store = store
	cfg := relay.V2SessionConfig{}
	installDaemonThreadRelay(&cfg, s)
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() {
		_, err := cfg.ThreadCatchUp(ctx, protocol.ThreadCatchUpPayload{ConversationID: string(id), Limit: 2})
		first <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("query never started recovery")
	}
	go func() {
		r, err := cfg.ThreadPage(t.Context(), protocol.ThreadPagePayload{ConversationID: string(id), BeforeOrder: 2, Limit: 2})
		if err == nil && (r.State != thread.StateUsable || len(r.Items) != 1) {
			err = errors.New("sibling lost its certified page")
		}
		second <- err
	}()
	testShadowWait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.demands[id].refs == 2 })
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled owner: %v", err)
	}
	s.mu.Lock()
	owners := s.demands[id].refs
	s.mu.Unlock()
	if owners != 1 || store.Status(id).State != thread.StateRebuilding {
		t.Fatal("cancellation released sibling or stopped its worker")
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(id).State == thread.StateNotLoaded })
}

func TestDaemonThreadQueryDemandDoesNotInventUnavailableData(t *testing.T) {
	s, h, reg, _ := testShadow(t)
	id := conversations.ConversationID(testPostID(t))
	reg.Create(conversations.Conversation{ID: id})
	testShadowAppend(t, h, id)
	s.store = thread.NewStore(h, func(context.Context, conversations.ConversationID) error { return errors.New("fold refused") })
	cfg := relay.V2SessionConfig{}
	installDaemonThreadRelay(&cfg, s)
	result, err := cfg.ThreadCatchUp(t.Context(), protocol.ThreadCatchUpPayload{ConversationID: string(id), Limit: 2})
	if err == nil || result.State == thread.StateUsable || result.Items != nil || result.Epoch != "" {
		t.Fatal("unavailable recovery supplied thread data")
	}
}
