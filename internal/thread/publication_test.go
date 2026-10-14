package thread

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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
		for _, chunk := range []int{1, 2, 3, 4, 7, 31, 113, 1000} {
			t.Run(fmt.Sprintf("%d/%d", scenario, chunk), func(t *testing.T) {
				f, source := testReferenceFold(entries)
				f.captureSet = make(map[int]struct{})
				var capture publicationCapture
				before := Snapshot{State: StateUsable}
				for start := 0; start < len(entries); start += chunk {
					end := min(start+chunk, len(entries))
					source.chunk = entries[start:end]
					testFeed(t, f, source.chunk...)
					items, got, err := capture.capture(f)
					if err != nil {
						t.Fatal(err)
					}
					after := f.Observe().Snapshot
					for _, item := range f.items {
						after.Active = after.Active || item.Active
					}
					want := difference(before, after)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("version %d: changes differ\ngot %#v\nwant %#v", after.Version, got, want)
					}
					if !sameItems(detachPublication(items), after.Items) || got.Active != after.Active {
						t.Fatalf("version %d: capture differs", after.Version)
					}
					before = after
					if len(f.captureSet) != 0 || len(f.captureAffected) != 0 {
						t.Fatal("candidates retained after capture")
					}
				}
			})
		}
	}
}

func TestAffectedPublicationOwnership(t *testing.T) {
	t.Parallel()
	f := New("a")
	f.captureSet = make(map[int]struct{})
	var capture publicationCapture
	testFeed(t, f, testMessage(1), testMain(2, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`), testMain(3, "assistant_delta", `,"text":"old"`))
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
	for id := uint64(4); id < 24; id++ {
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
			b.ReportMetric(float64(len(f.items)), "items")
		})
	}
}
