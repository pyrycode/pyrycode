package thread

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestAffectedPublicationOracle(t *testing.T) {
	t.Parallel()
	for scenario, entries := range [][]history.Entry{testFoldCorpus(1000), {
		testMain(1, "tool_use", `,"tool_use_id":"root","name":"Agent"`),
		testChild(2, "tool_use", "missing", `,"tool_use_id":"nested","name":"Task"`),
		testChild(3, "tool_use", "nested", `,"tool_use_id":"deep","name":"Agent"`),
		testChild(4, "assistant_delta", "deep", `,"text":"retained"`),
		testChild(5, "tool_result", "root", `,"tool_use_id":"nested","is_error":false`),
		testChild(6, "tool_result", "missing", `,"tool_use_id":"nested","is_error":false`),
		testChild(7, "tool_result", "root", `,"tool_use_id":"nested","is_error":false`),
		testChild(8, "assistant_delta", "deep", `,"text":""`),
		testEntry(9, "ignored", `{}`), testEntry(10, "banner", `{`),
	}} {
		t.Run(fmt.Sprint(scenario), func(t *testing.T) {
			type run struct {
				chunk, start int
				fold         *Fold
				source       *recentSource
				capture      publicationCapture
				before       Snapshot
			}
			var runs []run
			for _, chunk := range []int{1, 2, 3, 4, 7, 31, 113, 1000} {
				f, source := testReferenceFold(entries)
				f.captureSet = make(map[int]struct{})
				runs = append(runs, run{chunk: chunk, fold: f, source: source, before: Snapshot{State: StateUsable}})
			}
			// Render the independent complete oracle once per entry boundary.
			// Each batch size still owns a separate fold, source and capture,
			// and compares at every boundary it visited in its standalone run.
			oracle, source := testReferenceFold(entries)
			comparisons := 0
			for end := 1; end <= len(entries); end++ {
				source.chunk = entries[end-1 : end]
				testFeed(t, oracle, source.chunk...)
				after := oracle.Observe().Snapshot
				metadata := slices.Clone(after.Items)
				for _, item := range oracle.items {
					after.Active = after.Active || item.Active
				}
				for i := range metadata {
					r := oracle.recipes[metadata[i].ID]
					if r.creating == 0 && len(r.fields) != 0 {
						index, found := slices.BinarySearchFunc(oracle.items, metadata[i].ID, func(item Item, id uint64) int { return cmp.Compare(item.ID, id) })
						if !found {
							t.Fatal("rendered item absent from inventory")
						}
						metadata[i].Content = oracle.items[index].Content
					}
					if r.creating != 0 {
						metadata[i].Content = nil
						if metadata[i].Kind == "user_message" || metadata[i].Kind == "assistant_message" {
							metadata[i].Summary = ""
						}
					}
				}
				for i := range runs {
					r := &runs[i]
					if end%r.chunk != 0 && end != len(entries) {
						continue
					}
					r.source.chunk = entries[r.start:end]
					testFeed(t, r.fold, r.source.chunk...)
					items, got, err := r.capture.capture(r.fold)
					if err != nil {
						t.Fatal(err)
					}
					want := difference(r.before, after)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("chunk %d version %d: changes differ\ngot %#v\nwant %#v", r.chunk, end, got, want)
					}
					if !testPublicationItemsEqual(items, metadata) || got.Active != after.Active {
						t.Fatalf("chunk %d version %d: capture differs", r.chunk, end)
					}
					if len(r.fold.captureSet) != 0 || len(r.fold.captureAffected) != 0 {
						t.Fatal("candidates retained after capture")
					}
					r.before, r.start = after, end
					comparisons++
				}
			}
			expected := 0
			for _, r := range runs {
				expected += (len(entries) + r.chunk - 1) / r.chunk
			}
			if comparisons != expected {
				t.Fatalf("compared %d of %d batch boundaries", comparisons, expected)
			}
			t.Logf("checked %d batch boundaries across %d independent folds", comparisons, len(runs))
		})
	}
}

// Compare every row and exact content without serializing or copying payloads.
// The expected inventory still comes from the independent full-render oracle.
func testPublicationItemsEqual(slots []*Item, want []Item) bool {
	index := 0
	for _, item := range slots {
		if item == nil {
			continue
		}
		if index == len(want) || !reflect.DeepEqual(*item, want[index]) {
			return false
		}
		index++
	}
	return index == len(want)
}

func TestAffectedPublicationOwnership(t *testing.T) {
	t.Parallel()
	f := New("a")
	f.captureSet = make(map[int]struct{})
	var capture publicationCapture
	testFeed(t, f, testMessage(1), testMain(2, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`), testMain(3, "assistant_delta", `,"text":"old"`), testMain(4, "assistant_delta", `,"text":" initialized"`))
	old, changes, err := capture.capture(f)
	if err != nil {
		t.Fatal(err)
	}
	baseline := detachPublication(old)
	retained := copyObservation(changes)
	q := newPublicationIndex(old, nil)
	captured, err := q.window(context.Background(), changes.Snapshot, 0, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			result, err := q.window(context.Background(), changes.Snapshot, 0, 10, true)
			if err != nil || !sameItems(result.Items, baseline) {
				t.Error("old query changed", err)
			}
		}
	}()
	defer func() { <-done }()
	for id := uint64(5); id < 24; id++ {
		testFeed(t, f, testMain(id, "assistant_delta", `,"text":" suffix"`))
		items, _, err := capture.capture(f)
		if err != nil {
			t.Fatal(err)
		}
		if items[0] != old[0] || items[1] == old[1] {
			t.Fatal("immutable pointer reuse")
		}
		if &items[1].Content[0] != &f.items[1].Content[0] {
			t.Fatal("capture copied content")
		}
		copy := detachPublication(items)
		copy[0].Content[0] = '!'
	}
	<-done
	if !sameItems(detachPublication(old), baseline) || !reflect.DeepEqual(changes, retained) {
		t.Fatal("retained publication changed")
	}
	if !sameItems(captured.Items, baseline) {
		t.Fatal("retained query result changed")
	}
	captured.Items[0].Content[0] = '!'
	retained.Changes[0].Addition.Content[0] = '!'
	if !sameItems(detachPublication(old), baseline) {
		t.Fatal("addition aliases publication")
	}
}

func TestAffectedPublicationTextLifecycle(t *testing.T) {
	t.Parallel()
	large, _ := json.Marshal(strings.Repeat("<&>雪 ", 100))
	for _, lane := range []string{"main", "child"} {
		t.Run(lane, func(t *testing.T) {
			f := New("a")
			f.captureSet = make(map[int]struct{})
			var capture publicationCapture
			type retained struct {
				items    []*Item
				want     []Item
				snapshot Snapshot
				query    *queryIndex
				result   QueryResult
			}
			var roots []retained
			feed := func(e history.Entry) {
				e.ID = f.version + 1
				testFeed(t, f, e)
			}
			delta := func(raw string) history.Entry {
				if lane == "child" {
					return testChild(1, "assistant_delta", "p", `,"text":`+raw+`,"z":"tail"`)
				}
				return testMain(1, "assistant_delta", `,"text":`+raw+`,"z":"tail"`)
			}
			check := func() {
				for _, root := range roots {
					if !sameItems(detachPublication(root.items), root.want) || !sameItems(root.result.Items, root.want) {
						t.Error("retained publication/result changed at version", root.snapshot.Version)
						return
					}
					result, err := root.query.window(context.Background(), root.snapshot, 0, 10, true)
					if err != nil || !sameItems(result.Items, root.want) {
						t.Error("retained query root changed", root.snapshot.Version, err)
						return
					}
					for _, item := range root.items {
						if !json.Valid(item.Content) {
							t.Error("invalid retained JSON", root.snapshot.Version)
						}
					}
				}
			}
			retain := func() Observation {
				items, changes, err := capture.capture(f)
				if err != nil {
					t.Fatal(err)
				}
				for i, item := range items {
					if &item.Content[0] != &f.items[i].Content[0] {
						t.Fatal("capture copied content")
					}
				}
				q := newPublicationIndex(items, nil)
				result, err := q.window(context.Background(), changes.Snapshot, 0, 10, true)
				if err != nil {
					t.Fatal(err)
				}
				roots = append(roots, retained{items, f.Items(), changes.Snapshot, q, result})
				check()
				return changes
			}
			if lane == "child" {
				feed(testMain(1, "tool_use", `,"tool_use_id":"p","name":"Agent"`))
			}
			feed(delta(`"initial"`))
			feed(delta(`" initialized"`))
			retain()
			for phase := 0; phase < 3; phase++ {
				if lane == "child" && phase == 1 {
					feed(testMain(1, "tool_result", `,"tool_use_id":"p","is_error":false`))
					retain()
					if f.items[1].Active {
						t.Fatal("child not provisionally closed")
					}
				}
				if lane == "child" && phase == 2 {
					feed(testMain(1, "background_task_started", `,"task_id":"task","tool_call_id":"p"`))
					retain()
					if !f.items[1].Active {
						t.Fatal("child did not reopen")
					}
				}
				for _, suffix := range []string{`" x"`, string(large), `" y"`} {
					// Only the producer mutates the fold; retained roots are read concurrently.
					func() {
						done := make(chan struct{})
						defer func() { <-done }()
						go func() {
							defer close(done)
							for range 10 {
								check()
							}
						}()
						feed(delta(suffix))
					}()
					changes := retain()
					var want string
					_ = json.Unmarshal([]byte(suffix), &want)
					if len(changes.Changes) != 1 || changes.Changes[0].TextAppend != want || changes.Changes[0].Fields["Content"] != nil {
						t.Fatal("pure append did not publish exactly its suffix", changes.Changes)
					}
				}
			}
			for _, root := range roots {
				root.result.Items[len(root.result.Items)-1].Content[0] = '!'
				copy := detachPublication(root.items)
				copy[len(copy)-1].Content[0] = '!'
				if !sameItems(detachPublication(root.items), root.want) {
					t.Fatal("public result aliases publication")
				}
			}
		})
	}
}

func TestAffectedPublicationReplacement(t *testing.T) {
	for _, raw := range []string{`{"text":"new"}`, `{"text":""}`, `null`, `{"text":"old suffix","extra":2}`} {
		t.Run(raw, func(t *testing.T) {
			f := New("a")
			f.captureSet = make(map[int]struct{})
			f.items = []Item{{ID: 1, Rev: 1, Kind: "assistant_message", Shown: true, Summary: "old", Content: json.RawMessage(`{"text":"old","extra":1}`)}}
			f.version = 1
			f.markAffected(0)
			f.recordShown()
			var capture publicationCapture
			_, _, _ = capture.capture(f)
			before := f.Observe().Snapshot
			f.version = 2
			f.items[0].Rev, f.items[0].Summary, f.items[0].Content = 2, "", json.RawMessage(raw)
			f.markAffected(0)
			f.recordShown()
			_, got, err := capture.capture(f)
			want := difference(before, f.Observe().Snapshot)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("replacement differs", err, got, want)
			}
		})
	}
}

// Prepared inputs isolate item capture and comparison from feed, checkpoint,
// query preparation and output encoding. Both paths publish the same one-row delta.
func BenchmarkPublicationPhases(b *testing.B) {
	entries := testFoldCorpus(1000)
	for _, mode := range []string{"whole", "affected"} {
		b.Run(mode, func(b *testing.B) {
			f := New("a")
			f.captureSet = make(map[int]struct{})
			if err := f.Feed(entries); err != nil {
				b.Fatal(err)
			}
			var base publicationCapture
			_, _, err := base.capture(f)
			if err != nil {
				b.Fatal(err)
			}
			before := f.Observe().Snapshot
			index := len(f.items) - 2
			f.items[index].Rev = f.version + 1
			f.items[index].Summary += " changed"
			f.setContent(index, "text", messageText(f.items[index])+" tail")
			f.version++
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if mode == "whole" {
					after := f.Observe().Snapshot
					_ = difference(before, after)
				} else {
					c := publicationCapture{slots: base.slots, version: base.version, active: base.active}
					f.captureSet[index] = struct{}{}
					f.captureAffected = append(f.captureAffected, index)
					if _, _, err := c.capture(f); err != nil {
						b.Fatal(err)
					}
				}
			}
			b.ReportMetric(float64(len(entries)), "entries")
			b.ReportMetric(float64(len(before.Items)), "items")
		})
	}
}
