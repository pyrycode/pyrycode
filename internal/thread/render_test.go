package thread

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func testReferenceFold(entries []history.Entry) (*Fold, *recentSource) {
	s := newRecentSource(func(ids []uint64) ([]history.Entry, error) {
		var out []history.Entry
		for _, id := range ids {
			if i, ok := slices.BinarySearchFunc(entries, id, func(e history.Entry, id uint64) int { return cmp.Compare(e.ID, id) }); ok {
				out = append(out, entries[i])
			}
		}
		return out, nil
	})
	return newReferenceFold("a", s), s
}

func TestReferenceRetainedBytes(t *testing.T) {
	entries := testFoldCorpus(6738)
	legacy := New("a")
	f, source := testReferenceFold(entries)
	testFeed(t, legacy, entries...)
	source.chunk = entries
	testFeed(t, f, entries...)
	source.remember(entries)
	source.chunk = nil
	var before, after, deferred int
	for i, item := range f.items {
		if f.recipes[item.ID] != 0 {
			before += len(legacy.items[i].Content)
			after += len(item.Content)
		} else {
			deferred += len(item.Content)
		}
	}
	if before == 0 || after != 0 || source.bytes > recentEntryBudget {
		t.Fatal("reference retention did not drop", before, after, source.bytes)
	}
	t.Logf("generated entries=%d converted Content bytes before=%d after=%d recent cache=%d deferred inline item Content=%d", len(entries), before, after, source.bytes, deferred)
}

func TestReferenceRendering(t *testing.T) {
	entries := testFoldCorpus(101)
	entries[0] = testEntry(1, "message", "{ \"role\": \"user\", \"text\": \"  hi\\nthere\\u0001  \", \"future\": [1, 2] }")
	for _, size := range []int{1, 7, len(entries)} {
		legacy := New("a")
		f, source := testReferenceFold(entries)
		for start := 0; start < len(entries); start += size {
			chunk := entries[start:min(start+size, len(entries))]
			source.chunk = chunk
			testFeed(t, f, chunk...)
			testFeed(t, legacy, chunk...)
			got, err := f.Render()
			if err != nil || !reflect.DeepEqual(got, legacy.Items()) {
				t.Fatalf("chunk %d/%d differs: %v", start, size, err)
			}
			source.remember(chunk)
			source.chunk = nil
		}
		if f.items[0].Content != nil || f.items[0].Summary != "" || f.recipes[1] != 1 {
			t.Fatal("plain message retained content or summary")
		}
		got, err := f.Render()
		if err != nil || !bytes.Equal(got[0].Content, entries[0].Payload) || got[0].Summary != "hi there" {
			t.Fatal("raw bytes or summary changed", err)
		}
		got[0].Content[0] = 'X'
		again, _ := f.Render()
		if !bytes.Equal(again[0].Content, entries[0].Payload) {
			t.Fatal("renderer leaked content ownership")
		}
	}
	f, source := testReferenceFold([]history.Entry{testEntry(1, "message", `{"role":"user","text":"\n\u0001"}`)})
	source.chunk = []history.Entry{testEntry(1, "message", `{"role":"user","text":"\n\u0001"}`)}
	testFeed(t, f, source.chunk...)
	items, err := f.Render()
	if err != nil || items[0].Summary != "user message" {
		t.Fatal("empty summary fallback changed", err)
	}
}

func TestReferenceSourceOrder(t *testing.T) {
	var requested []uint64
	s := newRecentSource(func(ids []uint64) ([]history.Entry, error) {
		requested = append([]uint64(nil), ids...)
		return []history.Entry{testMessage(3)}, nil
	})
	s.remember([]history.Entry{testMessage(2)})
	s.chunk = []history.Entry{testMessage(1)}
	entries, err := s.Lookup([]uint64{3, 2, 1, 3, 2})
	if err != nil || len(entries) != 3 || !reflect.DeepEqual(requested, []uint64{3}) {
		t.Fatal("lookup did not exclude supplied IDs or normalize", requested, err)
	}
	s.lookup = func([]uint64) ([]history.Entry, error) { t.Fatal("supplied ID caused segment lookup"); return nil, nil }
	if _, err := s.Lookup([]uint64{2, 1, 2}); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceSourceBound(t *testing.T) {
	s := newRecentSource(func([]uint64) ([]history.Entry, error) { return nil, nil })
	base := int(unsafe.Sizeof(*s))
	e := testMessage(1)
	e.Payload = make([]byte, 250000, 2<<20)
	e.Type = strings.Repeat("t", 3000)
	e.Session = testSource(strings.Repeat("k", 3000), strings.Repeat("s", 3000))
	e.Shown = new(bool)
	s.remember([]history.Entry{e})
	if s.bytes <= base+len(e.Payload)+len(e.Type) || s.bytes > recentEntryBudget {
		t.Fatal("metadata/backing storage not accounted", s.bytes)
	}
	e.Payload[0] = 'X'
	e.Session.Kind = "changed"
	got, _ := s.Lookup([]uint64{1})
	if got[0].Payload[0] == 'X' || got[0].Session.Kind == "changed" || cap(got[0].Payload) > len(got[0].Payload) {
		t.Fatal("cache retained input backing or metadata")
	}
	for i := 2; i <= 8; i++ {
		e.ID = uint64(i)
		s.remember([]history.Entry{e})
		if s.bytes > recentEntryBudget {
			t.Fatal("cache exceeded bound", s.bytes)
		}
	}
	got, _ = s.Lookup([]uint64{1})
	if len(got) != 0 {
		t.Fatal("old entry was not evicted")
	}
	before := s.bytes
	e.Payload = make([]byte, recentEntryBudget)
	s.remember([]history.Entry{e})
	if s.bytes != before {
		t.Fatal("oversized entry was retained")
	}
}

func TestReferenceMissingAtomic(t *testing.T) {
	entries := []history.Entry{testMessage(1), testMessage(2)}
	f, source := testReferenceFold(entries)
	testFeed(t, f, entries...)
	for _, lookup := range []func([]uint64) ([]history.Entry, error){
		func([]uint64) ([]history.Entry, error) { return entries[:1], nil },
		func([]uint64) ([]history.Entry, error) { return nil, errors.New("private source path") },
	} {
		source.lookup = lookup
		if items, err := f.Render(); err == nil || items != nil {
			t.Fatal("partial rendering escaped", items, err)
		}
	}
}

func TestReferenceDeliveryFailure(t *testing.T) {
	entries := testFoldCorpus(23)
	f, source := testReferenceFold(entries)
	testFeed(t, f, entries[:22]...)
	before := f.items[f.accepted[21]]
	source.lookup = func([]uint64) ([]history.Entry, error) { return nil, errors.New("unreadable delivery") }
	if err := f.Feed(entries[22:]); err == nil {
		t.Fatal("claimed delivery ignored source failure")
	}
	if !reflect.DeepEqual(f.items[f.accepted[21]], before) || f.messages[22] < 0 {
		t.Fatal("failed delivery altered permanent acceptance or suppressed message")
	}
}

func TestStoreReferenceRetry(t *testing.T) {
	for _, phase := range []string{"replay", "tail"} {
		for _, failure := range []string{"missing", "unreadable"} {
			t.Run(phase+"/"+failure, func(t *testing.T) {
				s, h := testThreadStore(t)
				entries := testStoreAppend(t, h, testStoreA, testMessage(1))
				var armed atomic.Bool
				armed.Store(phase == "replay")
				base := s.lookup
				s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
					if !armed.Load() {
						return base(ctx, id, ids)
					}
					if failure == "missing" {
						return nil, nil
					}
					return nil, errors.New("private unreadable source")
				}
				if phase == "tail" {
					if err := s.Load(context.Background(), testStoreA); err != nil {
						t.Fatal(err)
					}
					testStoreWait(t, s, testStoreA, StateUsable, 1)
					armed.Store(true)
				}
				// Later facts evict the creating message; neither the current feed
				// chunk nor the recent cache can mask the failed authoritative read.
				for range history.MaxPageEntries {
					entries = append(entries, testStoreAppend(t, h, testStoreA, testEntry(0, "ignored", `{}`))...)
				}
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				snap := testStoreWait(t, s, testStoreA, StateUnavailable, 0)
				if snap.Err != ErrUnavailable || snap.Items != nil {
					t.Fatal("unsafe partial publication", snap)
				}
				s.mu.Lock()
				done := s.workers[testStoreA].done
				s.mu.Unlock()
				<-done
				armed.Store(false)
				if err := s.Retry(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				testStoreEqual(t, testStoreWait(t, s, testStoreA, StateUsable, uint64(len(entries))), testStoreA, entries)
			})
		}
	}
}
