package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// The quiet barrier lets the real follower consume the next accepted turn after
// publication but before idle retirement. No source or Changes result is stubbed.
func TestDaemonThreadRepeatedTurnRetirement(t *testing.T) {
	for _, separated := range []bool{false, true} {
		t.Run(map[bool]string{false: "coalesced", true: "separated"}[separated], func(t *testing.T) {
			dir := t.TempDir()
			h := history.New(dir)
			sends := testSendHistory(h)
			var hold atomic.Bool
			foldEntered, foldRelease := make(chan struct{}, 1), make(chan struct{})
			store := thread.NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
				if hold.Load() {
					select {
					case foldEntered <- struct{}{}:
					default:
					}
					select {
					case <-foldRelease:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			})
			reg, err := conversations.Load("")
			if err != nil {
				t.Fatal(err)
			}
			reg.Create(conversations.Conversation{ID: testConvID})
			s := newThreadShadow(h, reg, store, discardLogger())
			t.Cleanup(func() {
				if err := s.shutdown(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var armed, idle atomic.Bool
			quietEntered, quietRelease := make(chan struct{}), make(chan struct{})
			s.quiet = func(conversations.ConversationID) bool {
				if armed.CompareAndSwap(true, false) {
					close(quietEntered)
					select {
					case <-quietRelease:
					case <-ctx.Done():
						return false
					}
					return true
				}
				return idle.Load()
			}
			var mu sync.Mutex
			var frames []protocol.Envelope
			var retired atomic.Int32
			p := &daemonThreadPublisher{store: store, ctx: ctx, log: discardLogger(),
				active: func(context.Context, string) []relay.ActiveConn {
					return []relay.ActiveConn{{ConnID: "original", Interactive: true, Thread: true, View: relay.ThreadViewComplete}}
				},
				push: func(_ context.Context, peer string, e protocol.Envelope) error {
					if peer != "original" {
						t.Error("wrong recipient")
					}
					mu.Lock()
					frames = append(frames, e)
					mu.Unlock()
					return nil
				},
				retire: func(context.Context, string) error { retired.Add(1); return nil },
			}
			s.publisher = p
			source := history.SessionProvenance{Kind: "claude", SessionID: "owned"}
			appendFact := func(kind, raw string) uint64 {
				t.Helper()
				id := appendConversationHistory(h, discardLogger(), "test.append", testConvID, kind, json.RawMessage(raw), historyTS, source)
				if id == nil {
					t.Fatal("append failed")
				}
				return *id
			}
			turn := func(n uint64) uint64 {
				t.Helper()
				m := msgqueue.QueuedMessage{ID: n, MessageID: fmt.Sprint(n), Text: fmt.Sprintf("user-%d", n), TS: historyTS}
				sends.accepted(testConvID, m)
				if separated && n == 2 {
					v, _ := h.LatestEntryID(testConvID)
					testShadowWait(t, func() bool { return store.Status(testConvID).Version == v })
				}
				operatorMessageHistory(h, nil, discardLogger(), sends, source)(testConvID, m)
				appendFact("main_turn_opened", fmt.Sprintf(`{"turn_id":"turn-%d","occurred_at":"2026-01-01T00:00:00Z"}`, n))
				appendFact(protocol.TypeAssistantDelta, fmt.Sprintf(`{"turn_id":"turn-%d","text":"answer-%d"}`, n, n))
				return appendFact(protocol.TypeTurnEnd, fmt.Sprintf(`{"turn_id":"turn-%d","stop_reason":"end_turn"}`, n))
			}
			first := turn(1)
			s.start()
			cursor := func() uint64 {
				s.mu.Lock()
				w := s.workers[testConvID]
				s.mu.Unlock()
				if w == nil || !w.lockPublication(ctx) {
					return 0
				}
				defer w.unlockPublication()
				return w.threadVersion
			}
			testShadowWait(t, func() bool { return cursor() == first })
			base := store.Snapshot(testConvID)
			armed.Store(true)
			select {
			case <-quietEntered:
			case <-time.After(10 * time.Second):
				t.Fatal("quiet barrier")
			}
			if !separated {
				hold.Store(true)
				appendFact("padding", `{}`)
				select {
				case <-foldEntered:
				case <-time.After(10 * time.Second):
					t.Fatal("fold barrier")
				}
			}
			second := turn(2)
			if !separated {
				hold.Store(false)
				close(foldRelease)
			}
			testShadowWait(t, func() bool { return store.Status(testConvID).Version == second })
			o, err := store.Changes(ctx, testConvID, base.Epoch, first)
			if err != nil || o.BaselineRequired {
				t.Fatal("source already lost continuity before retirement")
			}
			idle.Store(true)
			close(quietRelease)
			testShadowWait(t, func() bool { return store.Status(testConvID).State == thread.StateNotLoaded })
			retirementCursor := cursor()
			idle.Store(false)
			last := appendFact("padding", `{}`)
			testShadowWait(t, func() bool { return store.Status(testConvID).Version == last })
			proof, err := store.Changes(ctx, testConvID, base.Epoch, retirementCursor)
			if err != nil {
				t.Fatal(err)
			}
			testShadowWait(t, func() bool { return cursor() == last || retired.Load() != 0 })
			t.Logf("published=%d source_before_retirement=%d retirement_cursor=%d reload=%d same_epoch=%v baseline_required=%v retired=%d", first, second, retirementCursor, last, proof.Epoch == base.Epoch, proof.BaselineRequired, retired.Load())
			if proof.BaselineRequired || retired.Load() != 0 || retirementCursor != second {
				t.Fatal("idle retirement discarded the retained range ahead of the publisher cursor")
			}
			current := store.Snapshot(testConvID)
			users, answers := 0, 0
			for _, item := range current.Items {
				if item.Kind == "user_message" {
					users++
					if item.ID != 1 && item.ID != first+1 && item.ID != first+2 {
						t.Fatal("invented acceptance identity")
					}
				}
				if item.Kind == "assistant_message" {
					answers++
					if item.Session != "owned" || item.Active {
						t.Fatal("lost terminal ownership")
					}
				}
			}
			if users != 2 || answers != 2 {
				t.Fatalf("users=%d answers=%d", users, answers)
			}
			testAcceptedDeliveryReplay(t, dir, current)
			mu.Lock()
			defer mu.Unlock()
			rows := map[uint64]protocol.ThreadItem{}
			for _, item := range base.Items {
				rows[item.ID] = daemonThreadItem(item)
			}
			additions := 0
			for _, frame := range frames {
				switch frame.Type {
				case protocol.TypeConversationUpdated:
					continue // Clean retirement refreshes conversation-list metadata.
				case protocol.TypeThreadItemAdded:
					var a protocol.ThreadItemAddedPayload
					if err := json.Unmarshal(frame.Payload, &a); err != nil {
						t.Fatal(err)
					}
					if a.Item.Kind == "user_message" {
						additions++
					}
					if a.Epoch != base.Epoch || rows[a.Item.ID].ID != 0 {
						t.Fatal("replacement epoch or duplicate item")
					}
					rows[a.Item.ID] = a.Item
				case protocol.TypeThreadItemChanged:
					var c protocol.ThreadItemChangedPayload
					if err := json.Unmarshal(frame.Payload, &c); err != nil {
						t.Fatal(err)
					}
					prior := rows[c.ItemID]
					if c.Epoch != base.Epoch || prior.Rev != c.BaseRev || c.Rev <= c.BaseRev {
						t.Fatal("invented revision or lost patch")
					}
					raw, _ := json.Marshal(prior)
					var fields map[string]json.RawMessage
					_ = json.Unmarshal(raw, &fields)
					for key, value := range c.Changes {
						fields[key] = value
					}
					fields["rev"], _ = json.Marshal(c.Rev)
					raw, _ = json.Marshal(fields)
					if err := json.Unmarshal(raw, &prior); err != nil {
						t.Fatal(err)
					}
					rows[c.ItemID] = prior
				default:
					t.Fatal("unexpected live frame", frame.Type)
				}
			}
			if additions != 1 {
				t.Fatalf("second user deliveries=%d", additions)
			}
			if len(rows) != len(current.Items) {
				t.Fatal("live inventory differs from source")
			}
			for _, item := range current.Items {
				testDaemonThreadJSONEqual(t, rows[item.ID], daemonThreadItem(item))
			}
		})
	}
}
