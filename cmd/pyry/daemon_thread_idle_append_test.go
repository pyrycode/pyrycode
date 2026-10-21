package main

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadIdleAppendKeepsConnections(t *testing.T) {
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	testShadowAppend(t, h, testConvID)
	var idle atomic.Bool
	s.quiet = func(conversations.ConversationID) bool { return idle.Load() }
	w, mgr := testDaemonThreadQueryWire(t, s, nil)
	s.start()
	testShadowWait(t, func() bool {
		s.mu.Lock()
		cursor := s.workers[testConvID]
		s.mu.Unlock()
		if cursor == nil || !cursor.lockPublication(t.Context()) {
			return false
		}
		defer cursor.unlockPublication()
		return cursor.threadVersion == 1
	})
	w.open("phone", false, protocol.CapabilityThread)
	w.open("legacy", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact)
	for n, peer := range []string{"phone", "legacy"} {
		projection := ""
		if peer == "legacy" {
			projection = protocol.ThreadProjectionCompact
		}
		base, _, _ := testCompactQuery(t, w, mgr, peer, uint64(500+n), protocol.TypeThreadCatchUp,
			protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 256, Projection: projection})
		if base.Version != 1 || !base.Final || len(base.Items) != 1 {
			t.Fatalf("initial catch-up: %+v", base)
		}
	}
	before := map[string]bool{}
	for _, conn := range mgr.ActiveConns(t.Context()) {
		before[conn.ConnID] = true
	}
	if len(before) != 2 {
		t.Fatal("both peers were not admitted")
	}
	for cycle := range 2 {
		idle.Store(true)
		testShadowWait(t, func() bool { return store.Status(testConvID).State == thread.StateNotLoaded })
		idle.Store(false)
		version := testDaemonThreadAppend(t, h, "message", `{"role":"user","text":"durable idle append"}`)
		testShadowWait(t, func() bool {
			s.mu.Lock()
			cursor := s.workers[testConvID]
			s.mu.Unlock()
			if !cursor.lockPublication(t.Context()) {
				return false
			}
			defer cursor.unlockPublication()
			return cursor.threadVersion == version
		})
		after := mgr.ActiveConns(t.Context())
		if len(after) != len(before) {
			t.Fatalf("idle append retired original connections: before=%v after=%+v", before, after)
		}
		for _, conn := range after {
			if !before[conn.ConnID] {
				t.Fatal("connection ID replaced")
			}
		}
		frames := testCompactRead(t, w, mgr, "phone", "legacy")
		seen := map[string]int{}
		for _, frame := range frames {
			if frame.env.InReplyTo != nil {
				t.Fatal("live update became a catch-up reply")
			}
			if frame.env.Type == protocol.TypeConversationUpdated {
				continue // Retirement also refreshes conversation-list metadata.
			}
			seen[frame.peer]++
			if frame.peer == "phone" {
				var p protocol.ThreadItemAddedPayload
				if frame.env.Type != protocol.TypeThreadItemAdded || json.Unmarshal(frame.env.Payload, &p) != nil || p.Version != version || p.Item.ID != version || string(p.Item.Content) != `{"role":"user","text":"durable idle append"}` {
					t.Fatalf("complete live update: %s %s", frame.env.Type, frame.env.Payload)
				}
			} else {
				var p protocol.ThreadCompactUpdatePayload
				if frame.env.Type != protocol.TypeThreadCompactUpdate || json.Unmarshal(frame.env.Payload, &p) != nil || p.FromVersion != version-1 || p.Version != version || p.Item == nil || p.Item.ID != version || p.Header != nil || p.Progress {
					t.Fatalf("compact live update: %s %s", frame.env.Type, frame.env.Payload)
				}
			}
		}
		if seen["phone"] != 1 || seen["legacy"] != 1 {
			t.Fatalf("lost/duplicated live updates: %+v", seen)
		}
		// Both original authenticated peers remain able to query after delivery.
		for n, peer := range []string{"phone", "legacy"} {
			projection := ""
			if peer == "legacy" {
				projection = protocol.ThreadProjectionCompact
			}
			p, _, _ := testCompactQuery(t, w, mgr, peer, uint64(510+cycle*2+n), protocol.TypeThreadCatchUp,
				protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 256, Projection: projection})
			if p.Version != version || len(p.Items) != int(version) {
				t.Fatal("original peer became unusable")
			}
		}
	}
}
