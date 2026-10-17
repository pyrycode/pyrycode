package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadCompactRetirement(t *testing.T) {
	for _, event := range []string{"retained-range-loss", "epoch-replacement"} {
		t.Run(event, func(t *testing.T) {
			s, store, _ := testDaemonExpansionFixture(t, 3, 1, 64)
			w, mgr := testDaemonThreadQueryWire(t, s, nil)
			cursor := testDaemonThreadCursor(s) // publication is explicitly barrier controlled
			caps := []string{protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact}
			w.open("phone", false, caps...)
			window := protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: "e", Limit: 256, Projection: protocol.ThreadProjectionCompact}
			base, _, _ := testCompactQuery(t, w, mgr, "phone", 1, protocol.TypeThreadCatchUp, window)
			pushes, retirements := 0, 0
			push, retire := s.publisher.push, s.publisher.retire
			s.publisher.push = func(ctx context.Context, peer string, e protocol.Envelope) error {
				pushes++
				return push(ctx, peer, e)
			}
			s.publisher.retire = func(ctx context.Context, peer string) error {
				retirements++
				return retire(ctx, peer)
			}
			if event == "retained-range-loss" {
				// Each awaited publication consumes one retained batch; a single
				// large history append would not prove eviction of the checkpoint.
				for i := range 65 {
					v := testDaemonThreadAppend(t, s.history, "tool_use", fmt.Sprintf(`{"turn_id":"t","parent_tool_use_id":"root","tool_use_id":"gap%d","name":"Read","input":{}}`, i))
					testShadowWait(t, func() bool { return store.Status(testConvID).Version == v })
				}
			} else {
				if err := store.Unload(testConvID); err != nil {
					t.Fatal(err)
				}
				dir, err := s.history.EnsureLogDir(testConvID)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"thread-certificate.json", "thread-index-state.json"} {
					if err := os.Remove(filepath.Join(dir, name)); err != nil {
						t.Fatal(err)
					}
				}
				testDaemonThreadLoad(t, store, base.Version)
				if store.Status(testConvID).Epoch == base.Epoch {
					t.Fatal("Store epoch did not change")
				}
			}
			lost, err := store.Changes(t.Context(), testConvID, base.Epoch, base.Version)
			if err != nil || !lost.BaselineRequired {
				t.Fatalf("fixture retained publication range: %+v %v", lost, err)
			}
			s.publisher.publish(t.Context(), testConvID, cursor)
			closed := w.await()
			if closed.ConnID != "phone" || closed.CloseCode != uint16(relay.StatusQueueOverflow) || retirements != 1 || pushes != 0 {
				t.Fatalf("gap did not retire without baseline: close=%+v retirements=%d pushes=%d", closed, retirements, pushes)
			}
			w.open("again", false, caps...)
			resume := window
			resume.Epoch, resume.Version = base.Epoch, base.Version
			if event == "epoch-replacement" {
				testDaemonThreadRequest(t, w, "again", 2, protocol.TypeThreadCatchUp, resume)
				typ, raw := testCompactReply(t, w, mgr, "again", 2)
				var reset protocol.ThreadResetPayload
				if typ != protocol.TypeThreadReset || json.Unmarshal(raw, &reset) != nil || reset.Projection != protocol.ThreadProjectionCompact || reset.Epoch != store.Status(testConvID).Epoch {
					t.Fatalf("explicit compact reset: %s %s", typ, raw)
				}
			} else {
				got, _, _ := testCompactQuery(t, w, mgr, "again", 2, protocol.TypeThreadCatchUp, resume)
				want, err := store.CompactCatchUp(t.Context(), testConvID, base.Epoch, base.Version, 256)
				if err != nil || got.Version != want.Version || got.FromVersion == nil || *got.FromVersion != base.Version {
					t.Fatalf("compact recovery after complete range loss: %+v %v", got, err)
				}
				testCompactHeaders(t, got.Headers, want.Headers)
			}
			got, _, _ := testCompactQuery(t, w, mgr, "again", 3, protocol.TypeThreadCatchUp, window)
			want, err := store.CompactNewestWindow(t.Context(), testConvID, 256)
			if err != nil || got.Version != want.Version || got.Epoch != want.Epoch {
				t.Fatalf("recovery checkpoint: %+v %v", got, err)
			}
			testCompactHeaders(t, got.Headers, want.Headers)
		})
	}
}

func TestDaemonThreadCompactProgress(t *testing.T) {
	s, store, _ := testDaemonExpansionFixture(t, 3, 1, 64)
	w, mgr := testDaemonThreadQueryWire(t, s, nil)
	cursor := testDaemonThreadCursor(s)
	w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact)
	base, _, _ := testCompactQuery(t, w, mgr, "phone", 1, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Projection: protocol.ThreadProjectionCompact, Limit: 256})
	v := testDaemonThreadAppend(t, s.history, "unknown", `{"secret":"unopened-private-progress"}`)
	testShadowWait(t, func() bool { return store.Status(testConvID).Version == v })
	s.publisher.publish(t.Context(), testConvID, cursor)
	frames := testCompactRead(t, w, mgr, "phone")
	if len(frames) != 1 || frames[0].env.Type != protocol.TypeThreadCompactUpdate || frames[0].frames != 1 {
		t.Fatalf("hidden-only interval did not produce one progress envelope: %d", len(frames))
	}
	var update protocol.ThreadCompactUpdatePayload
	if json.Unmarshal(frames[0].env.Payload, &update) != nil || !update.Progress || update.Item != nil || update.Header != nil || update.FromVersion != base.Version || update.Version != v {
		t.Fatalf("invalid consumed progress: %s", frames[0].env.Payload)
	}
	s.publisher.publish(t.Context(), testConvID, cursor)
	if frames := testCompactRead(t, w, mgr, "phone"); len(frames) != 0 {
		t.Fatal("idle pass repeated progress")
	}
}

func TestDaemonThreadCompactConversationIsolation(t *testing.T) {
	s, store, _ := testDaemonExpansionFixture(t, 3, 1, 64)
	b := conversations.ConversationID(testConvIDB)
	s.registry.Create(conversations.Conversation{ID: b})
	if _, err := s.history.Append(b, "message", json.RawMessage(`{"role":"user","text":"second-only"}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(b).State == thread.StateUsable })
	w, mgr := testDaemonThreadQueryWire(t, s, nil)
	aCursor := testDaemonThreadCursor(s)
	bCursor := &shadowConversation{done: aCursor.done, cancel: func() {}, complete: true}
	s.workers[b] = bCursor
	caps := []string{protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact}
	w.open("phone", true, caps...)
	w.open("legacy", true, caps...)
	bases := map[string]protocol.ThreadItemsPayload{}
	id := uint64(10)
	for _, conv := range []string{testConvID, testConvIDB} {
		for _, peer := range []string{"phone", "legacy"} {
			projection := ""
			if (conv == testConvID) == (peer == "phone") {
				projection = protocol.ThreadProjectionCompact
			}
			id++
			q, _, _ := testCompactQuery(t, w, mgr, peer, id, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: conv, Limit: 256, Projection: projection})
			bases[peer+conv] = q
		}
	}
	av := testDaemonThreadAppend(t, s.history, "tool_use", `{"turn_id":"t","parent_tool_use_id":"root","tool_use_id":"isolated","name":"Read","input":{"secret":"first-only"}}`)
	bv, err := s.history.Append(b, "message", json.RawMessage(`{"role":"user","text":"second-live"}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(testConvID).Version == av && store.Status(b).Version == bv })
	s.publisher.publish(t.Context(), testConvID, aCursor)
	s.publisher.publish(t.Context(), b, bCursor)
	seen := map[string]uint64{}
	live := map[string][]json.RawMessage{}
	for _, f := range testCompactRead(t, w, mgr, "phone", "legacy") {
		var scope struct {
			Conversation string `json:"conversation_id"`
			Version      uint64 `json:"version"`
			From         uint64 `json:"from_version"`
		}
		json.Unmarshal(f.env.Payload, &scope)
		base, ok := bases[f.peer+scope.Conversation]
		if !ok || scope.Version == 0 {
			continue
		}
		compact := base.Projection == protocol.ThreadProjectionCompact
		if (f.env.Type == protocol.TypeThreadCompactUpdate) != compact {
			t.Fatalf("projection crossed conversation: %s %s %s", f.peer, scope.Conversation, f.env.Type)
		}
		want := av
		if scope.Conversation == testConvIDB {
			want = bv
		}
		if scope.Version != want || compact && scope.From != base.Version {
			t.Fatalf("checkpoint crossed conversation: %+v base=%d want=%d", scope, base.Version, want)
		}
		seen[f.peer+scope.Conversation] = scope.Version
		live[f.peer+scope.Conversation] = append(live[f.peer+scope.Conversation], f.env.Payload)
	}
	if len(seen) != 4 || av == bv {
		t.Fatalf("independent delivery missing: %v", seen)
	}
	for key, base := range bases {
		conv := conversations.ConversationID(base.ConversationID)
		var expected []any
		if base.Projection == protocol.ThreadProjectionCompact {
			interval, err := store.CompactChanges(t.Context(), conv, base.Epoch, base.Version)
			if err != nil {
				t.Fatal(err)
			}
			expected = daemonThreadCompactUpdates(base.ConversationID, base.Version, interval)
		} else {
			interval, err := store.Changes(t.Context(), conv, base.Epoch, base.Version)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range interval.Changes {
				expected = append(expected, daemonThreadUpdates(base.ConversationID, interval.Epoch, c)...)
			}
		}
		if len(live[key]) != len(expected) {
			t.Fatalf("live rows crossed conversation: %s", key)
		}
		for i, update := range expected {
			if !bytes.Equal(live[key][i], mustQueryJSON(t, update)) {
				t.Fatalf("live content/order crossed conversation: %s", key)
			}
		}
		peer := "phone"
		if key[:6] == "legacy" {
			peer = "legacy"
		}
		id++
		got, _, _ := testCompactQuery(t, w, mgr, peer, id, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: base.ConversationID, Epoch: base.Epoch, Limit: 256, Projection: base.Projection})
		if base.Projection == protocol.ThreadProjectionCompact {
			want, _ := store.CompactNewestWindow(t.Context(), conversations.ConversationID(base.ConversationID), 256)
			testCompactHeaders(t, got.Headers, want.Headers)
			testDaemonThreadRows(t, got.Items, want.Items)
		} else {
			want, _ := store.NewestWindow(t.Context(), conversations.ConversationID(base.ConversationID), 256)
			testDaemonThreadRows(t, got.Items, want.Items)
		}
	}
}
