package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestDaemonLivePublicationRetirement(t *testing.T) {
	o := testLiveOwner()
	s := newStreamTurnSink(1, discardLogger())
	s.live = o
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	delivered := make(chan relay.LiveState, 512)
	retired := make(chan string, 1)
	start := func() func() {
		return startDaemonLivePublisher(t.Context(), o, func(ctx context.Context) []relay.ActiveConn {
			once.Do(func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
			return []relay.ActiveConn{{ConnID: "thread", Interactive: true, Thread: true}, {ConnID: "legacy", Interactive: true}}
		}, func(_ context.Context, id string, r relay.LiveState) error {
			if id != "thread" {
				t.Error("duplicate legacy publication")
			}
			delivered <- r
			return nil
		}, func(_ context.Context, id string) { retired <- id }, discardLogger())
	}
	stop := start()
	defer stop()
	src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
	o.acceptEvent(src, turnevent.ModelAnnounced{Model: "old"})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("publisher did not start")
	}
	// Publication holds neither the stream boundary nor owner state, including a refused fan-in.
	send := s.sinkForSessionTag(newStreamSessionTag("a"), "claude")
	for i := 0; i < 300; i++ {
		send(turnevent.Stall{})
	}
	s.remove(conversations.ConversationID("conv"))
	close(release)
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("bounded handoff failed to retire consumers")
	}
	stop()
	if len(delivered) != 0 {
		t.Fatal("removed state published")
	}
	// A second installation has one worker; shutdown discards pending data.
	stop = start()
	stop()
	o.acceptEvent(src, turnevent.Stall{})
	if len(delivered) != 0 {
		t.Fatal("shutdown publisher remained attached")
	}
}

func TestDaemonLiveRelaySnapshot(t *testing.T) {
	o := testLiveOwner()
	s := newStreamTurnSink(1, discardLogger())
	s.live = o
	b := &daemonLiveBindings{sink: s}
	src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
	b.offer(src, protocol.TypeModalShown, json.RawMessage(`{"modal_id":"ineligible"}`), "ineligible", false)
	b.offer(src, protocol.TypeModalShown, json.RawMessage(`{"modal_id":"eligible"}`), "eligible", true)
	cursor := daemonRelaySnapshot(o)()
	seen := map[string]bool{}
	for {
		r, ok := cursor()
		if !ok {
			break
		}
		seen[r.ReadingID] = true
		if string(r.Envelope.SessionID) != `"a"` || r.Revision == 0 || r.SessionGeneration == 0 {
			t.Fatal("source lost")
		}
	}
	if seen["ineligible"] || !seen["eligible"] {
		t.Fatal("captured prompt eligibility ignored")
	}
}

func TestDaemonLivePublicationShutdownInstallation(t *testing.T) {
	o := testLiveOwner()
	entered, exiting, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	stop := startDaemonLivePublisher(t.Context(), o, func(ctx context.Context) []relay.ActiveConn {
		close(entered)
		<-ctx.Done()
		close(exiting)
		<-release
		return nil
	}, func(context.Context, string, relay.LiveState) error { return nil }, func(context.Context, string) {}, discardLogger())
	o.acceptEvent(o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true), turnevent.Stall{})
	<-entered
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	<-exiting
	o.mu.Lock()
	original := o.publisher
	o.mu.Unlock()
	duplicate := startDaemonLivePublisher(t.Context(), o, func(context.Context) []relay.ActiveConn { t.Error("duplicate worker enumerated"); return nil }, func(context.Context, string, relay.LiveState) error { return nil }, func(context.Context, string) {}, discardLogger())
	duplicate()
	o.mu.Lock()
	same := o.publisher == original
	o.mu.Unlock()
	close(release)
	<-stopped
	if !same {
		t.Fatal("installation overlapped an exiting publisher")
	}
	if len(original.pending) != 0 {
		t.Fatal("shutdown retained pending payloads")
	}
}
