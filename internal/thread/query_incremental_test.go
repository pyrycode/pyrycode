package thread

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testPublicationFixture(n int) (*Fold, *publicationCapture, *Store, *conversationWorker) {
	f := New("a")
	f.captureSet = make(map[int]struct{})
	for i := range n {
		id := uint64(i + 1)
		f.items = append(f.items, Item{ID: id, Order: id, Rev: id, Kind: "assistant_message", Content: json.RawMessage(`{"text":"base"}`)})
		f.markAffected(i)
		f.publicVisible[id], f.touches[id] = true, id
		f.captureAffected = append(f.captureAffected, i)
	}
	f.version = uint64(n)
	c, s := &publicationCapture{}, NewStore(nil)
	w := &conversationWorker{changed: make(chan struct{})}
	s.workers[testStoreA] = w
	items, changes, _ := c.capture(f)
	s.publish(context.Background(), testStoreA, w, items, changes, f, c.affected)
	return f, c, s, w
}

// Mutation setup is outside measurement; capture, Changes encoding/retention,
// metadata and all index maintenance are inside. Every iteration changes state.
func BenchmarkStorePublication(b *testing.B) {
	for _, mode := range []string{"add", "append"} {
		b.Run(mode, func(b *testing.B) {
			f, c, s, w := testPublicationFixture(50000)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				testPublicationMutation(f, mode)
				b.StartTimer()
				items, changes, err := c.capture(f)
				if err != nil {
					b.Fatal(err)
				}
				s.publish(context.Background(), testStoreA, w, items, changes, f, c.affected)
			}
			b.ReportMetric(float64(len(f.publicVisible)), "items")
		})
	}
}

func testPublicationMutation(f *Fold, mode string) {
	f.version++
	i := len(f.items) - 1
	if mode == "add" {
		i++
		f.items = append(f.items, Item{ID: f.version, Order: f.version, Kind: "assistant_message", Content: json.RawMessage(`{"text":"base"}`)})
	} else {
		f.items[i].Content = json.RawMessage(fmt.Sprintf(`{"text":%q}`, messageText(f.items[i])+" x"))
	}
	f.items[i].Rev = f.version
	f.publicVisible[f.items[i].ID], f.touches[f.items[i].ID] = true, f.version
	f.captureAffected = append(f.captureAffected, i)
}

func TestPublicationPreparationGrowth(t *testing.T) {
	for _, mode := range []string{"add", "append"} {
		var counts []uint64
		for _, n := range []int{12500, 50000} {
			f, c, s, w := testPublicationFixture(n)
			var count uint64
			var copies int
			c.copied = func(n int) { copies += n }
			s.queryCopies = c.copied
			s.queryWork = func(preparing bool) {
				if preparing {
					count++
				}
			}
			testPublicationMutation(f, mode)
			items, changes, err := c.capture(f)
			if err != nil {
				t.Fatal(err)
			}
			s.publish(context.Background(), testStoreA, w, items, changes, f, c.affected)
			counts = append(counts, count)
			t.Logf("%s public=%d semantic=%d copied-pointers=%d", mode, len(f.publicVisible), count, copies)
		}
		if counts[1] >= 2*counts[0] {
			t.Fatalf("%s preparation scales with history: %v", mode, counts)
		}
	}
}

func TestIncrementalQueryOracle(t *testing.T) {
	raw, err := os.ReadFile("../e2e/realclaude/testdata/thread_shadow_history.json")
	if err != nil {
		t.Fatal(err)
	}
	var shadow struct {
		Conversation string
		Entries      []history.Entry
	}
	if err := json.Unmarshal(raw, &shadow); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, conversation string
		entries            []history.Entry
	}{
		{"generated", "a", testFoldCorpus(1000)}, {"shadow", shadow.Conversation, shadow.Entries},
		{"membership", "a", []history.Entry{
			testAcceptance(1, "phone"), testMessage(2), testMessage(3),
			testSendOutcome(4, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered"),
			testMain(5, "tool_use", `,"tool_use_id":"parent","name":"Agent"`),
			testChild(6, "tool_use", "missing", `,"tool_use_id":"nested","name":"Task"`),
			testChild(7, "assistant_delta", "nested", `,"text":"deep"`),
			testMessage(8),
			testChild(9, "tool_result", "parent", `,"tool_use_id":"nested","is_error":false`),
			testChild(10, "turn_end", "nested", `,"stop_reason":"end_turn"`),
			testEntry(12, "unknown", `{}`),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := New(tc.conversation)
			f.captureSet = make(map[int]struct{})
			var c publicationCapture
			s := NewStore(nil)
			w := &conversationWorker{changed: make(chan struct{})}
			s.workers[testStoreA] = w
			for _, e := range tc.entries {
				testFeed(t, f, e)
				items, changes, err := c.capture(f)
				if err != nil {
					t.Fatal(err)
				}
				s.publish(context.Background(), testStoreA, w, items, changes, f, c.affected)
				fresh := newQueryIndex(f.Items(), nil)
				fresh.prepareCatchUp(f)
				ref := NewStore(nil)
				ref.workers[testStoreA] = &conversationWorker{snapshot: w.snapshot, query: fresh}
				for _, bound := range []uint64{0, 1, e.ID / 2, e.ID, e.ID + 1} {
					for _, newest := range []bool{false, true} {
						got, err := s.query(context.Background(), testStoreA, bound, 3, newest)
						want, wantErr := ref.query(context.Background(), testStoreA, bound, 3, newest)
						testQueryResultEqual(t, got, want, err, wantErr)
					}
					got, err := s.CatchUp(context.Background(), testStoreA, "", bound, 3)
					want, wantErr := ref.CatchUp(context.Background(), testStoreA, "", bound, 3)
					testQueryResultEqual(t, got, want, err, wantErr)
				}
			}
		})
	}
}

func TestIncrementalTreeOwnership(t *testing.T) {
	f, c, s, w := testPublicationFixture(300)
	snap, old := s.captureQuery(testStoreA)
	want, err := old.window(context.Background(), snap, 0, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			got, err := old.window(context.Background(), snap, 0, 10, true)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Error("borrowed root changed", err)
			}
		}
	}()
	defer func() { <-done }()
	for step := range 300 {
		i := (step * 137) % 300
		f.version++
		f.items[i].Order, f.items[i].Active, f.items[i].Rev = uint64(step%17), step%3 == 0, f.version
		f.touches[f.items[i].ID] = f.version
		f.captureAffected = append(f.captureAffected, i)
		if step%4 == 0 {
			delete(f.publicVisible, f.items[i].ID)
		}
		items, changes, err := c.capture(f)
		if err != nil {
			t.Fatal(err)
		}
		s.publish(context.Background(), testStoreA, w, items, changes, f, c.affected)
		for _, root := range []*queryTree{w.query.byID, w.query.ordered, w.query.active, w.query.touched} {
			var validate func(*queryTree) (int, int)
			validate = func(n *queryTree) (int, int) {
				if n == nil {
					return 0, 0
				}
				lh, ls := validate(n.left)
				rh, rs := validate(n.right)
				if lh-rh > 1 || rh-lh > 1 || n.height != max(lh, rh)+1 || n.size != ls+rs+1 {
					t.Fatal("invalid AVL metadata")
				}
				for _, child := range []*queryTree{n.left, n.right} {
					if child == nil {
						continue
					}
					less := child.key < n.key || child.key == n.key && child.row.item.ID < n.row.item.ID
					if less != (child == n.left) {
						t.Fatal("invalid AVL ordering")
					}
				}
				return n.height, n.size
			}
			validate(root)
		}
	}
	got, err := old.window(context.Background(), snap, 0, 10, true)
	testQueryResultEqual(t, got, want, err, nil)
}

func testQueryResultEqual(t *testing.T, got, want QueryResult, err, wantErr error) {
	t.Helper()
	if err != wantErr || !sameItems(got.Items, want.Items) {
		t.Fatalf("query items differ at %d: %v / %v", got.Version, err, wantErr)
	}
	got.Items, want.Items = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata differs: %#v / %#v", got, want)
	}
}
