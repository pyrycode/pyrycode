package thread

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

func testStoreStatusEqual(t *testing.T, s *Store, id conversations.ConversationID) {
	t.Helper()
	want := s.Snapshot(id)
	want.Items = nil
	if got := s.Status(id); !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %#v, want snapshot metadata %#v", got, want)
	}
}

func TestStoreStatusParity(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		snapshot Snapshot
		absent   bool
		retiring bool
		closed   bool
	}{
		{name: "not-loaded", absent: true},
		{name: "rebuilding", snapshot: Snapshot{State: StateRebuilding}},
		{name: "usable-empty", snapshot: Snapshot{State: StateUsable, Epoch: "empty"}},
		{name: "private-active", snapshot: Snapshot{State: StateUsable, Epoch: "private", Version: 7, Active: true}},
		{name: "usable", snapshot: Snapshot{State: StateUsable, Epoch: "shown", Version: 9, LastShownVersion: 8, Items: []Item{{Content: []byte(`{"text":"content"}`)}}}},
		{name: "unavailable", snapshot: Snapshot{State: StateUnavailable, Err: ErrUnavailable}},
		{name: "retiring", snapshot: Snapshot{State: StateUsable, Epoch: "withdrawn", Version: 9}, retiring: true},
		{name: "closed", absent: true, closed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// No history backend or worker is needed by either read.
			s := &Store{closed: tt.closed, workers: make(map[conversations.ConversationID]*conversationWorker)}
			if !tt.absent {
				s.workers[testStoreA] = &conversationWorker{snapshot: tt.snapshot, retiring: tt.retiring}
			}
			testStoreStatusEqual(t, s, testStoreA)
			want := tt.snapshot
			want.Items = nil
			if tt.retiring {
				want = Snapshot{}
			}
			if got := s.Status(testStoreA); !reflect.DeepEqual(got, want) {
				t.Fatalf("status = %#v, want %#v", got, want)
			}
			testStoreStatusEqual(t, s, testStoreB)
		})
	}
}

func TestStoreStatusAllocations(t *testing.T) {
	for _, count := range []int{0, 1, 30000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			items := make([]Item, count)
			for i := range items {
				items[i].Content = []byte(`{"text":"retained content"}`)
			}
			s := &Store{workers: map[conversations.ConversationID]*conversationWorker{
				testStoreA: {snapshot: Snapshot{State: StateUsable, Items: items, Version: 42, Epoch: "epoch", Active: true, LastShownVersion: 40}},
			}}
			var got Snapshot
			if allocs := testing.AllocsPerRun(1000, func() { got = s.Status(testStoreA) }); allocs != 0 {
				t.Fatalf("status allocated %g times for %d items", allocs, count)
			}
			if got.Items != nil || got.Version != 42 || !got.Active || got.LastShownVersion != 40 || got.Epoch != "epoch" {
				t.Fatalf("status lost metadata or retained content: %#v", got)
			}
		})
	}
}

func TestStoreStatusConcurrent(t *testing.T) {
	t.Parallel()
	publications := []Snapshot{
		{},
		{State: StateRebuilding},
		{State: StateUsable, Epoch: "first", Version: 2, LastShownVersion: 1, Active: true},
		{State: StateUsable, Epoch: "replacement", Version: 4, LastShownVersion: 3},
		{State: StateUnavailable, Err: ErrUnavailable},
	}
	s := &Store{workers: make(map[conversations.ConversationID]*conversationWorker)}
	var readers sync.WaitGroup
	start := make(chan struct{})
	for range 4 {
		readers.Go(func() {
			<-start
			for range 2000 {
				got := s.Status(testStoreA)
				valid := false
				for _, publication := range publications {
					valid = valid || reflect.DeepEqual(got, publication)
				}
				if !valid {
					t.Errorf("mixed publication metadata: %#v", got)
					return
				}
			}
		})
	}
	close(start)
	for i := range 2000 {
		s.mu.Lock()
		w := s.workers[testStoreA]
		if w == nil || i%5 == 0 {
			w = &conversationWorker{}
			s.workers[testStoreA] = w
		}
		w.snapshot = publications[i%len(publications)]
		w.snapshot.Items = []Item{{Content: []byte(`{"text":"never exposed"}`)}}
		w.retiring = i%3 == 0
		s.mu.Unlock()
	}
	readers.Wait()
}
