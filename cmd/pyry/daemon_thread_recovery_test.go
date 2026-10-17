package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// The test drives following synchronously so every fanout has a positive barrier.
func testDaemonThreadCursor(s *threadShadow) *shadowConversation {
	done := make(chan struct{})
	close(done)
	w := &shadowConversation{publisher: s.publisher, done: done, complete: true, cancel: func() {}}
	s.workers[testConvID] = w
	return w
}

func testDaemonThreadWaitBoundary(t *testing.T, s *threadShadow, id conversations.ConversationID) {
	t.Helper()
	testShadowWait(t, func() bool {
		s.mu.Lock()
		w := s.workers[id]
		s.mu.Unlock()
		if w == nil {
			return false
		}
		if !w.lockPublication(t.Context()) {
			return false
		}
		defer w.unlockPublication()
		return w.threadEpoch != ""
	})
}

func TestDaemonThreadPublicationCancellation(t *testing.T) {
	s, _, _, _ := testShadow(t)
	w := &shadowConversation{}
	if !w.lockPublication(t.Context()) {
		t.Fatal("cannot hold publication")
	}
	t.Cleanup(w.unlockPublication)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan bool, 1)
	go func() { done <- s.publish(ctx, testConvID, w) }()
	select {
	case ready := <-done:
		if ready {
			t.Fatal("canceled publication acquired a boundary")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled request waited for another publication")
	}
}

func TestDaemonThreadBoundedRecovery(t *testing.T) {
	for _, count := range []int{128, 8333} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, h, reg, store := testShadow(t)
			reg.Create(conversations.Conversation{ID: testConvID})
			for range count {
				testShadowAppend(t, h, testConvID)
			}
			testDaemonThreadLoad(t, store, uint64(count))
			wire, mgr := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) {
				c.Devices.Add(devices.Device{TokenHash: devices.HashToken("recovered-token"), Name: "test"})
			})
			recipient := "phone"
			cursor := testDaemonThreadCursor(s)
			p := s.publisher
			frames, bytes := 0, 0
			push := p.push
			p.push = func(ctx context.Context, id string, e protocol.Envelope) error {
				raw, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				frames++
				bytes += len(raw)
				return push(ctx, id, e)
			}
			p.publish(t.Context(), testConvID, cursor)
			wire.open(recipient, false, protocol.CapabilityThread)
			// An already connected client must not receive initial historical rows.
			initial := &shadowConversation{}
			p.publish(t.Context(), testConvID, initial)
			if frames != 0 || bytes != 0 {
				t.Fatalf("initial historical output: frames=%d bytes=%d", frames, bytes)
			}
			testDaemonThreadRequest(t, wire, "phone", 400, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 8})
			got, typ := testDaemonThreadReply(t, wire, 400)
			if typ != protocol.TypeThreadItems || !got.Final || len(got.Items) != 8 || got.Version != uint64(count) || got.OlderExists == nil || !*got.OlderExists {
				t.Fatalf("bounded recovery: %#v %s", got, typ)
			}
			all := append([]protocol.ThreadItem(nil), got.Items...)
			page := got
			for requestID := uint64(500); page.OlderExists != nil && *page.OlderExists; requestID++ {
				if page.Continuation == nil {
					t.Fatal("missing page boundary")
				}
				before := *page.Continuation
				testDaemonThreadRequest(t, wire, recipient, requestID, protocol.TypeThreadPage, protocol.ThreadPagePayload{ConversationID: testConvID, BeforeOrder: before, Limit: 256})
				page, typ = testDaemonThreadReply(t, wire, requestID, recipient)
				if typ != protocol.TypeThreadItems || !page.Final || page.Version != got.Version || page.Continuation == nil || *page.Continuation >= before {
					t.Fatal("paging failed to make certified progress")
				}
				all = append(all, page.Items...)
			}
			if len(all) != count {
				t.Fatalf("paged %d rows, want %d", len(all), count)
			}
			testDaemonThreadRows(t, all, store.Snapshot(testConvID).Items)
			for index, event := range []string{"idle", "range", "epoch"} {
				t.Run(event, func(t *testing.T) {
					before := store.Status(testConvID)
					var v uint64
					if event == "range" {
						for range 65 {
							v = testShadowAppend(t, h, testConvID)
							testShadowWait(t, func() bool { return store.Status(testConvID).Version == v })
						}
					} else {
						if err := store.Unload(testConvID); err != nil {
							t.Fatal(err)
						}
						if event == "epoch" {
							dir, err := h.EnsureLogDir(testConvID)
							if err != nil {
								t.Fatal(err)
							}
							if err := os.Remove(filepath.Join(dir, "thread-certificate.json")); err != nil {
								t.Fatal(err)
							}
						}
						v = testShadowAppend(t, h, testConvID)
						testDaemonThreadLoad(t, store, v)
					}
					p.publish(t.Context(), testConvID, cursor)
					closed := wire.await()
					if closed.ConnID != recipient || closed.CloseCode != uint16(relay.StatusQueueOverflow) {
						t.Fatalf("missing reconciliation: %#v", closed)
					}
					if frames != 0 || bytes != 0 {
						t.Fatalf("replacement historical output: %d frames / %d bytes", frames, bytes)
					}
					recipient = []string{"again", "legacy", "recovered"}[index]
					wire.open(recipient, false, protocol.CapabilityThread)
					testDaemonThreadRequest(t, wire, recipient, 401, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: before.Epoch, Version: before.Version, Limit: 8})
					recovered, kind := testDaemonThreadReply(t, wire, 401, recipient)
					if kind == protocol.TypeThreadReset {
						testDaemonThreadRequest(t, wire, recipient, 402, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 8})
						recovered, kind = testDaemonThreadReply(t, wire, 402, recipient)
						if event != "epoch" {
							t.Fatal("unexpected reset")
						}
					}
					if kind != protocol.TypeThreadItems || !recovered.Final || recovered.Version != v {
						t.Fatal("recovery not completed")
					}
					want, err := store.CatchUp(t.Context(), testConvID, before.Epoch, before.Version, 8)
					if err != nil {
						t.Fatal(err)
					}
					if want.ResetRequired {
						want, err = store.CatchUp(t.Context(), testConvID, "", 0, 8)
					}
					if err != nil {
						t.Fatal(err)
					}
					testDaemonThreadRows(t, recovered.Items, want.Items)
					testDaemonThreadRequest(t, wire, recipient, 403, protocol.TypeThreadPage, protocol.ThreadPagePayload{ConversationID: testConvID, BeforeOrder: uint64(count), Limit: 8})
					older, kind := testDaemonThreadReply(t, wire, 403, recipient)
					page, err := store.HistoryPage(t.Context(), testConvID, uint64(count), 8)
					if err != nil || kind != protocol.TypeThreadItems || !older.Final || older.Continuation == nil || *older.Continuation != page.Continuation {
						t.Fatal("older page unavailable")
					}
					testDaemonThreadRows(t, older.Items, page.Items)
					// Synchronous publish completion precedes the FIFO drain barrier.
					p.publish(t.Context(), testConvID, cursor)
					if err := mgr.FlushPushes(t.Context()); err != nil {
						t.Fatal(err)
					}
					barrier := protocol.Envelope{Type: protocol.TypeError, Payload: json.RawMessage(`{}`)}
					if err := mgr.Push(t.Context(), recipient, barrier); err != nil {
						t.Fatal(err)
					}
					peer, e := wire.read()
					if peer != recipient || e.Type != protocol.TypeError {
						t.Fatal("historical inventory escaped after completion")
					}
				})
			}
			v := testShadowAppend(t, h, testConvID)
			testDaemonThreadLoad(t, store, v)
			p.publish(t.Context(), testConvID, cursor)
			peer, e := wire.read()
			var added protocol.ThreadItemAddedPayload
			if err := json.Unmarshal(e.Payload, &added); err != nil {
				t.Fatal(err)
			}
			if peer != recipient || e.Type != protocol.TypeThreadItemAdded || added.Version != v || frames != 1 || bytes > 1024 {
				t.Fatalf("new output: peer=%s type=%s version=%d frames=%d bytes=%d", peer, e.Type, added.Version, frames, bytes)
			}
			t.Logf("old_rows=%d initial/replacement_item_frames=0 new_item_frames=%d new_bytes=%d", count, frames, bytes)
		})
	}
}

func TestDaemonThreadQueryBoundary(t *testing.T) {
	s, h, reg, store := testShadow(t)
	s.quiet = func(conversations.ConversationID) bool { return false }
	reg.Create(conversations.Conversation{ID: testConvID})
	testDaemonThreadAppend(t, h, "assistant_delta", `{"turn_id":"t","text":"first"}`)
	testDaemonThreadLoad(t, store, 1)
	captured, release, published := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	wire, mgr := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) {
		query := c.ThreadCatchUp
		first := true
		c.ThreadCatchUp = func(ctx context.Context, r protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
			q, err := query(ctx, r)
			if first {
				first = false
				close(captured)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return q, err
		}
	})
	push := s.publisher.push
	var delivered sync.Once
	s.publisher.push = func(ctx context.Context, peer string, e protocol.Envelope) error {
		err := push(ctx, peer, e)
		if e.Type == protocol.TypeThreadTextAppend {
			delivered.Do(func() { close(published) })
		}
		return err
	}
	wire.open("phone", false, protocol.CapabilityThread)
	request := protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 8}
	testDaemonThreadRequest(t, wire, "phone", 410, protocol.TypeThreadCatchUp, request)
	awaitSwitchPublication(t, captured)
	testDaemonThreadAppend(t, h, "assistant_delta", `{"turn_id":"t","text":" second"}`)
	awaitSwitchPublication(t, published)
	unblock()
	failed, live := false, 0
	for !failed || live < 2 {
		peer, e := wire.read()
		if peer != "phone" {
			t.Fatal("wrong recipient")
		}
		if e.Type == protocol.TypeError && e.InReplyTo != nil && *e.InReplyTo == 410 {
			failed = true
			continue
		}
		if e.InReplyTo != nil {
			t.Fatal("racing initial query certified partial recovery")
		}
		if e.Type != protocol.TypeThreadItemChanged && e.Type != protocol.TypeThreadTextAppend {
			t.Fatalf("historical output %s", e.Type)
		}
		live++
	}
	testDaemonThreadRequest(t, wire, "phone", 411, protocol.TypeThreadCatchUp, request)
	got, typ := testDaemonThreadReply(t, wire, 411)
	if typ != protocol.TypeThreadItems || !got.Final || got.Version != 2 {
		t.Fatal("retry failed to complete")
	}
	testDaemonThreadRows(t, got.Items, store.Snapshot(testConvID).Items)
	// A completed query precedes the delivery-lane drain; a timeout proves nothing.
	if err := mgr.FlushPushes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Push(t.Context(), "phone", protocol.Envelope{Type: protocol.TypeError, Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	_, e := wire.read()
	if e.Type != protocol.TypeError {
		t.Fatal("late historical output")
	}
}

func TestDaemonThreadRecoveryCommit(t *testing.T) {
	s, h, reg, old := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	for range 128 {
		testShadowAppend(t, h, testConvID)
	}
	testDaemonThreadLoad(t, old, 128)
	if err := old.Unload(testConvID); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
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
	wire, _ := testDaemonThreadQueryWire(t, s, nil)
	cursor := testDaemonThreadCursor(s)
	wire.open("phone", false, protocol.CapabilityThread)
	testDaemonThreadRequest(t, wire, "phone", 420, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 8})
	awaitSwitchPublication(t, entered)
	testShadowAppend(t, h, testConvID)
	unblock()
	got, typ := testDaemonThreadReply(t, wire, 420)
	if typ != protocol.TypeThreadItems || !got.Final {
		t.Fatal("store recovery did not complete")
	}
	// Replay may first publish its captured bound; explicit catch-up covers the tail.
	testDaemonThreadLoad(t, store, 129)
	s.publisher.publish(t.Context(), testConvID, cursor)
	if got.Version < 129 {
		_, e := wire.read()
		if e.Type != protocol.TypeThreadItemAdded {
			t.Fatal("recovery tail was not delivered")
		}
	}
	testDaemonThreadRequest(t, wire, "phone", 421, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: got.Epoch, Version: got.Version, Limit: 8})
	tail, typ := testDaemonThreadReply(t, wire, 421)
	if typ != protocol.TypeThreadItems || !tail.Final || tail.Version != 129 {
		t.Fatal("recovery commit lost")
	}
	rows := map[uint64]protocol.ThreadItem{}
	for _, i := range got.Items {
		rows[i.ID] = i
	}
	for _, i := range tail.Items {
		rows[i.ID] = i
	}
	want, err := store.CatchUp(t.Context(), testConvID, "", 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range want.Items {
		if actual, ok := rows[item.ID]; !ok || actual.Rev != item.Rev {
			t.Fatalf("recovery row %d missing/stale", item.ID)
		}
	}
}
