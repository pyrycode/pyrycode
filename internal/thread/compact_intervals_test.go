package thread

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// testRootTracker records, independently of the index, the last version at
// which each root's public subtree differed and the last version a root left.
type testRootTracker struct {
	prints map[uint64]string
	last   map[uint64]uint64
	lost   uint64
}

func (tr *testRootTracker) observe(facts []Item, version uint64) {
	byID := make(map[uint64]Item, len(facts))
	for _, i := range facts {
		byID[i.ID] = i
	}
	groups := make(map[uint64][]string)
	for _, i := range slices.SortedFunc(slices.Values(facts), func(a, b Item) int { return int(a.ID) - int(b.ID) }) {
		root := i
		for root.Parent != 0 {
			root = byID[root.Parent]
		}
		groups[root.ID] = append(groups[root.ID], fmt.Sprintf("%+v", i))
	}
	prints := make(map[uint64]string, len(groups))
	for id, g := range groups {
		prints[id] = strings.Join(g, "\n")
		if tr.prints[id] != prints[id] {
			tr.last[id] = version
		}
	}
	for id := range tr.prints {
		if _, ok := prints[id]; !ok {
			tr.lost = version
			delete(tr.last, id)
		}
	}
	tr.prints = prints
}

func testCompactInterval(t *testing.T, s *Store, tr *testRootTracker, facts []Item, after uint64, extras bool) CompactQueryResult {
	t.Helper()
	var p CompactQueryResult
	var err error
	if extras {
		p, err = s.CompactCatchUp(context.Background(), testStoreA, "epoch", after, 0)
	} else {
		p, err = s.CompactChanges(context.Background(), testStoreA, "epoch", after)
	}
	version := s.Status(testStoreA).Version
	if err != nil || p.State != StateUsable || p.ConversationID != testStoreA || p.Projection != "compact-v1" || p.Epoch != "epoch" || p.Version != version {
		t.Fatal(p, err)
	}
	if tr.lost > after {
		if !reflect.DeepEqual(p, CompactQueryResult{State: StateUsable, ConversationID: testStoreA, Projection: "compact-v1", Epoch: "epoch", Version: version, ResetRequired: true}) {
			t.Fatal("lost root must reset", after, p)
		}
		return p
	}
	if p.ResetRequired || p.FromVersion != after || p.LowerOrder != 0 || p.UpperOrder != 0 || p.Continuation != 0 || p.OlderExists {
		t.Fatal("interval bounds", after, p)
	}
	byID := make(map[uint64]Item)
	children := make(map[uint64]uint64)
	for _, i := range facts {
		byID[i.ID] = i
		children[i.Parent]++
	}
	want := make(map[uint64]bool)
	for id, v := range tr.last {
		if v > after {
			want[id] = true
		}
	}
	if extras {
		for _, i := range facts {
			if !i.Active {
				continue
			}
			for i.Parent != 0 {
				i = byID[i.Parent]
			}
			want[i.ID] = true
		}
	}
	seen := make(map[uint64]bool)
	for _, i := range p.Items {
		if !want[i.ID] || seen[i.ID] || !reflect.DeepEqual(i, byID[i.ID]) || i.Kind == "tool_call" || i.Kind == "agent" {
			t.Fatal("message", after, i, byID[i.ID])
		}
		seen[i.ID] = true
	}
	for _, h := range p.Headers {
		i := byID[h.ID]
		if !want[h.ID] || seen[h.ID] || h.Parent != 0 || h.Rev != i.Rev || h.Active != i.Active || h.Kind != i.Kind || !h.DetailOmitted || *h.PublicChildCount != children[h.ID] || *h.SubtreeVersion != tr.last[h.ID] {
			t.Fatal("header", after, h, i, tr.last[h.ID])
		}
		seen[h.ID] = true
	}
	if len(seen) != len(want) {
		t.Fatal("membership", after, seen, want)
	}
	return p
}

func testCompactPublisher(t *testing.T, n int) (*Fold, *Store, *conversationWorker, *testRootTracker, func(...int)) {
	f, c, s, w := testPublicationFixture(n)
	tr := &testRootTracker{last: make(map[uint64]uint64)}
	tr.observe(f.Items(), f.version)
	publish := func(indices ...int) {
		t.Helper()
		f.version++
		for _, i := range indices {
			f.markAffected(i)
			f.captureAffected = append(f.captureAffected, i)
			f.touches[f.items[i].ID] = f.version
		}
		items, changes, err := c.capture(f)
		if err != nil {
			t.Fatal(err)
		}
		changes.Epoch = "epoch"
		s.publish(context.Background(), testStoreA, w, items, changes, f, c)
		tr.observe(f.Items(), f.version)
	}
	return f, s, w, tr, publish
}

func TestCompactInterval(t *testing.T) {
	f, s, _, tr, publish := testCompactPublisher(t, 16)
	check := func() {
		t.Helper()
		facts := f.Items()
		for after := uint64(16); after <= f.version; after++ {
			testCompactInterval(t, s, tr, facts, after, true)
			if after < f.version {
				testCompactInterval(t, s, tr, facts, after, false)
			}
		}
	}
	f.items[0].Kind, f.items[1].Kind = "agent", "tool_call"
	f.items[2].Parent, f.items[3].Parent = 1, 3
	publish(0, 1, 2, 3)
	check()
	for _, active := range []bool{true, false, true} {
		f.items[3].Active = active
		publish(3)
		check()
	}
	if f.items[0].Rev != 1 {
		t.Fatal("nested burst changed root revision")
	}
	burst := testCompactInterval(t, s, tr, f.Items(), f.version-3, false)
	if len(burst.Headers) != 1 || burst.Headers[0].ID != 1 || len(burst.Items) != 0 || *burst.Headers[0].SubtreeVersion != f.version {
		t.Fatal("coalesced nested burst", burst)
	}
	publish()
	if p := testCompactInterval(t, s, tr, f.Items(), f.version-1, false); len(p.Items)+len(p.Headers) != 0 || p.FromVersion != f.version-1 || p.Version != f.version {
		t.Fatal("hidden-only progress", p)
	}
	f.items[2].Parent = 2
	publish(2)
	check()
	f.items[3].Parent = 0
	publish(3)
	check()
	f.items[5].Summary = "changed"
	publish(5)
	check()
	p := testCompactInterval(t, s, tr, f.Items(), 17, true)
	p.Items[0].Content[0] = 'x'
	*p.Headers[0].SubtreeVersion = 999
	check()
	f.publicVisible[7] = false
	f.messages[7] = -1
	publish(6)
	if tr.lost != f.version {
		t.Fatal("suppression fixture", tr.lost)
	}
	check()
	f.items[7].Parent = 1
	publish(7)
	if tr.lost != f.version {
		t.Fatal("reparent fixture", tr.lost)
	}
	check()
	f.items[5].Summary = "again"
	publish(5)
	check()
}

func TestCompactIntervalReset(t *testing.T) {
	_, s, w, _, publish := testCompactPublisher(t, 16)
	publish(1)
	snap, q := s.captureQuery(testStoreA)
	reset := CompactQueryResult{State: StateUsable, ConversationID: testStoreA, Projection: "compact-v1", Epoch: "epoch", Version: 17, ResetRequired: true}
	for _, c := range []struct {
		epoch string
		after uint64
	}{{"other", 16}, {"", 16}, {"epoch", 18}, {"epoch", math.MaxUint64}, {"epoch", 15}, {"epoch", 1}} {
		if p, e := s.CompactCatchUp(context.Background(), testStoreA, c.epoch, c.after, 0); e != nil || !reflect.DeepEqual(p, reset) {
			t.Fatal(c, p, e)
		}
		if p, e := s.CompactChanges(context.Background(), testStoreA, c.epoch, c.after); e != nil || !reflect.DeepEqual(p, reset) {
			t.Fatal(c, p, e)
		}
	}
	if p, e := s.CompactCatchUp(context.Background(), testStoreA, "epoch", 16, 0); e != nil || p.ResetRequired || len(p.Items) != 1 {
		t.Fatal("compact evidence starts at the first publication", p, e)
	}
	for _, c := range []struct {
		name  string
		snap  Snapshot
		query *queryIndex
	}{
		{"source continuity", Snapshot{State: StateUsable, Epoch: "epoch", Version: 18}, q},
		{"absent compact evidence", snap, nil},
	} {
		old := NewStore(nil)
		old.workers[testStoreA] = &conversationWorker{snapshot: c.snap, query: c.query, changed: make(chan struct{})}
		want := reset
		want.Version = c.snap.Version
		if p, e := old.CompactCatchUp(context.Background(), testStoreA, "epoch", 16, 0); e != nil || !reflect.DeepEqual(p, want) {
			t.Fatal(c.name, p, e)
		}
	}
	for range MaxCatchUpGap {
		publish()
	}
	if p, _ := s.CompactCatchUp(context.Background(), testStoreA, "epoch", 17, 0); p.ResetRequired {
		t.Fatal("gap at the inclusive limit", p)
	}
	publish()
	if p, _ := s.CompactCatchUp(context.Background(), testStoreA, "epoch", 17, 0); !p.ResetRequired || len(p.Items)+len(p.Headers) != 0 {
		t.Fatal("gap above the limit", p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, e := s.CompactCatchUp(ctx, "bad/id", "epoch", 17, 0); !errors.Is(e, context.Canceled) || !reflect.DeepEqual(p, CompactQueryResult{}) {
		t.Fatal(p, e)
	}
	if _, e := s.CompactChanges(context.Background(), "bad/id", "epoch", 17); !errors.Is(e, history.ErrInvalidID) {
		t.Fatal(e)
	}
	for _, limit := range []int{-1, 0} {
		if _, e := s.CompactCatchUp(context.Background(), testStoreA, "", 0, limit); !errors.Is(e, ErrInvalidLimit) {
			t.Fatal(e)
		}
	}
	want, _ := s.CompactNewestWindow(context.Background(), testStoreA, 3)
	for _, epoch := range []string{"", "epoch", "stale"} {
		if p, e := s.CompactCatchUp(context.Background(), testStoreA, epoch, 0, 3); e != nil || !reflect.DeepEqual(p, want) {
			t.Fatal("version-zero recovery", p, e)
		}
	}
	w.snapshot = Snapshot{State: StateRebuilding}
	if p, e := s.CompactCatchUp(context.Background(), testStoreA, "epoch", 17, 0); e != nil || !reflect.DeepEqual(p, CompactQueryResult{State: StateRebuilding}) {
		t.Fatal(p, e)
	}
}

func TestCompactCatchUpReload(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(0), testMain(0, "tool_use", `,"tool_use_id":"root","name":"Agent"`))
	if e := s.Load(context.Background(), testStoreA); e != nil {
		t.Fatal(e)
	}
	first := testStoreWait(t, s, testStoreA, StateUsable, 2)
	if p, e := s.CompactCatchUp(context.Background(), testStoreA, first.Epoch, 0, 10); e != nil || p.ResetRequired || p.Version != 2 {
		t.Fatal(p, e)
	}
	testStoreAppend(t, h, testStoreA, testMessage(0))
	testStoreWait(t, s, testStoreA, StateUsable, 3)
	if p, e := s.CompactCatchUp(context.Background(), testStoreA, first.Epoch, 2, 0); e != nil || p.ResetRequired || len(p.Items) != 1 || p.Items[0].Order != 3 {
		t.Fatal("same-run interval", p, e)
	}
	if e := s.Unload(testStoreA); e != nil {
		t.Fatal(e)
	}
	if e := s.Load(context.Background(), testStoreA); e != nil {
		t.Fatal(e)
	}
	reloaded := testStoreWait(t, s, testStoreA, StateUsable, 3)
	_, q := s.captureQuery(testStoreA)
	if reloaded.Epoch != first.Epoch || q.compactFrom != 3 {
		t.Fatal("same-epoch reload fixture", reloaded.Epoch, first.Epoch, q.compactFrom)
	}
	if p, e := s.CompactCatchUp(context.Background(), testStoreA, first.Epoch, 2, 0); e != nil || !p.ResetRequired || len(p.Items)+len(p.Headers) != 0 || p.FromVersion != 0 {
		t.Fatal("equal epoch cannot replace compact evidence", p, e)
	}
	if p, e := s.CompactChanges(context.Background(), testStoreA, first.Epoch, 2); e != nil || !p.ResetRequired {
		t.Fatal("lost live range", p, e)
	}
	if p, e := s.CatchUp(context.Background(), testStoreA, first.Epoch, 2, 0); e != nil || p.ResetRequired {
		t.Fatal("complete recovery is independent", p, e)
	}
	if p, e := s.CompactCatchUp(context.Background(), testStoreA, first.Epoch, 3, 0); e != nil || p.ResetRequired || len(p.Headers) != 1 {
		t.Fatal("interval from the reload version", p, e)
	}
}

func TestCompactChangesWait(t *testing.T) {
	f, s, w, tr, publish := testCompactPublisher(t, 16)
	f.items[0].Kind = "agent"
	f.items[2].Parent = 1
	publish(0, 2)
	wait := func(ctx context.Context, after uint64) <-chan CompactQueryResult {
		out := make(chan CompactQueryResult, 1)
		go func() {
			p, e := s.CompactChanges(ctx, testStoreA, "epoch", after)
			if (e != nil) != (ctx.Err() != nil) {
				t.Error(e)
			}
			out <- p
		}()
		select {
		case p := <-out:
			t.Fatal("returned without progress", p)
		case <-time.After(20 * time.Millisecond):
		}
		return out
	}
	recv := func(out <-chan CompactQueryResult) CompactQueryResult {
		t.Helper()
		select {
		case p := <-out:
			return p
		case <-time.After(5 * time.Second):
			t.Fatal("waiter not released")
		}
		return CompactQueryResult{}
	}
	out := wait(context.Background(), f.version)
	f.items[2].Active = true
	publish(2)
	if p := recv(out); len(p.Headers) != 1 || p.Headers[0].ID != 1 || p.FromVersion != f.version-1 || len(p.Items) != 0 {
		t.Fatal("nested progress", p)
	}
	out = wait(context.Background(), f.version)
	publish()
	if p := recv(out); p.State != StateUsable || p.ResetRequired || p.FromVersion != f.version-1 || p.Version != f.version || len(p.Items)+len(p.Headers) != 0 {
		t.Fatal("explicit nonvisual progress", p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	out = wait(ctx, f.version)
	cancel()
	if p := recv(out); !reflect.DeepEqual(p, CompactQueryResult{}) {
		t.Fatal("cancellation", p)
	}

	var readers sync.WaitGroup
	var mu sync.Mutex
	results := make(map[uint64][]CompactQueryResult)
	base := f.version
	stop := make(chan struct{})
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				p, e := s.CompactCatchUp(context.Background(), testStoreA, "epoch", base, 0)
				if c, ce := s.CatchUp(context.Background(), testStoreA, "epoch", base, 0); e != nil || ce != nil || c.ResetRequired {
					t.Error(e, ce, c)
				}
				mu.Lock()
				results[p.Version] = append(results[p.Version], p)
				mu.Unlock()
			}
		}()
	}
	expected := make(map[uint64]CompactQueryResult)
	for i := range 20 {
		f.items[2].Active = i%2 == 1
		f.items[4+i%3].Summary = fmt.Sprint("burst ", i)
		publish(2, 4+i%3)
		expected[f.version] = testCompactInterval(t, s, tr, f.Items(), base, true)
	}
	close(stop)
	readers.Wait()
	for version, got := range results {
		for _, p := range got {
			if want, ok := expected[version]; ok && !reflect.DeepEqual(p, want) {
				t.Fatal("concurrent capture", version, p, want)
			}
		}
	}

	out = wait(context.Background(), f.version)
	s.mu.Lock()
	w.snapshot, w.query = Snapshot{State: StateUnavailable, Err: ErrUnavailable}, nil
	w.notify()
	s.mu.Unlock()
	if p := recv(out); !reflect.DeepEqual(p, CompactQueryResult{State: StateUnavailable, Err: ErrUnavailable}) {
		t.Fatal("lifecycle change", p)
	}
	s.mu.Lock()
	w.snapshot = Snapshot{State: StateUsable, Epoch: "epoch", Version: f.version}
	s.mu.Unlock()
	out = wait(context.Background(), f.version)
	s.mu.Lock()
	w.retiring = true
	w.notify()
	s.mu.Unlock()
	if p := recv(out); !reflect.DeepEqual(p, CompactQueryResult{}) {
		t.Fatal("retirement", p)
	}
}

func TestCompactIntervalWork(t *testing.T) {
	var reads []int
	for _, n := range []int{128, 8192} {
		f, s, _, tr, publish := testCompactPublisher(t, n)
		f.items[0].Kind, f.items[0].Summary = "agent", strings.Repeat("x", n*64)
		f.items[0].Content = []byte(`{"secret":"` + strings.Repeat("s", n*64) + `"}`)
		indices := []int{0}
		for i := 3; i < n; i++ {
			f.items[i].Parent, f.items[i].Active = 1, i%2 == 0
			f.items[i].Kind = "tool_call"
			indices = append(indices, i)
		}
		publish(indices...)
		semantic, copies := 0, 0
		s.queryWork = func(preparing bool) {
			if preparing {
				semantic++
			}
		}
		s.queryCopies = func(c int) { copies += c }
		f.items[n-1].Active = !f.items[n-1].Active
		f.items[1].Summary = "changed"
		publish(n-1, 1)
		if semantic > 1000 || copies == 0 {
			t.Fatal("preparation", n, semantic, copies)
		}
		visits, prep := 0, 0
		_, q := s.captureQuery(testStoreA)
		q.work = func(p bool) {
			if p {
				prep++
			} else {
				visits++
			}
		}
		lookups := 0
		s.lookup = func(context.Context, conversations.ConversationID, []uint64) ([]history.Entry, error) {
			lookups++
			return nil, errors.New("unexpected body lookup")
		}
		for _, extras := range []bool{true, false} {
			p := testCompactInterval(t, s, tr, f.Items(), f.version-1, extras)
			if len(p.Headers) != 1 || len(p.Items) != 1 || strings.Contains(fmt.Sprint(p), "secret") {
				t.Fatal("affected roots only", n, p)
			}
		}
		if prep != 0 || lookups != 0 || visits > 60 {
			t.Fatal("request work", n, visits, prep, lookups)
		}
		reads = append(reads, visits)
		t.Logf("descendants=%d request-visits=%d preparation=%d copied=%d", n-3, visits, semantic, copies)
	}
	if reads[1] > reads[0] {
		t.Fatal("request work grows with hidden inventory", reads)
	}
}
