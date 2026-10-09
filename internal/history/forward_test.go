package history

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func testForwardReader(t *testing.T, s *Store, after uint64) *ForwardReader {
	t.Helper()
	r, err := s.Forward(convA, after)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func testForwardWait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("forward operation did not finish")
	}
	var zero T
	return zero
}

func TestForwardSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	want := appendN(t, s, convA, 0, 18)
	shown := false
	metadata := Metadata{Session: &SessionProvenance{Kind: "codex", SessionID: "opaque"}, Shown: &shown}
	payload := json.RawMessage(`{"future":"<&>","shown":true}`)
	id, err := s.AppendWithMetadata(convA, "future_unknown", payload, testTS, metadata)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, Entry{ID: id, Type: "future_unknown", Payload: payload, TS: testTS, Session: metadata.Session, Shown: &shown})
	for _, reopened := range []bool{false, true} {
		t.Run(map[bool]string{false: "warm", true: "reopened"}[reopened], func(t *testing.T) {
			store := s
			if reopened {
				store = newStore(root, testSegmentBytes)
			}
			h, err := store.LatestEntryID(convA)
			if err != nil {
				t.Fatal(err)
			}
			r := testForwardReader(t, store, 0)
			var got []Entry
			consume := func(es []Entry) error { got = append(got, es...); return nil }
			if err := r.Walk(context.Background(), h, consume); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("snapshot = %#v, want %#v", got, want)
			}
			if r.LastEntryID() != h {
				t.Fatal("incorrect consumed ID")
			}
			got = nil
			if err := r.Walk(context.Background(), h, consume); err != nil || len(got) != 0 {
				t.Fatalf("repeat: %v, %v", got, err)
			}
			r = testForwardReader(t, store, 7)
			if err := r.Walk(context.Background(), h, consume); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want[7:]) {
				t.Fatal("resume did not exclude consumed entries")
			}
		})
	}
	r := testForwardReader(t, s, 0)
	h, _ := s.LatestEntryID(convA)
	later := appendN(t, s, convA, 19, 2)
	var got []Entry
	consume := func(es []Entry) error { got = append(got, es...); return nil }
	if err := r.Walk(context.Background(), h, consume); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("snapshot included later appends")
	}
	got = nil
	if err := r.Walk(context.Background(), later[1].ID, consume); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, later) {
		t.Fatal("next walk lost later appends")
	}
}

func TestForwardReplayToTail(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	want := appendN(t, s, convA, 0, 24)
	h, _ := s.LatestEntryID(convA)
	r := testForwardReader(t, s, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	paused, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	result := make(chan error, 1)
	var got []Entry
	go func() {
		result <- r.Walk(ctx, h, func(es []Entry) error {
			got = append(got, es...)
			if len(got) == len(es) {
				close(paused)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}()
	testForwardWait(t, paused)
	appended := make(chan []Entry, 1)
	go func() { appended <- appendN(t, s, convA, 24, 2) }()
	later := testForwardWait(t, appended)
	other := make(chan []Entry, 1)
	go func() { other <- appendN(t, s, convB, 0, 1) }()
	testForwardWait(t, other)
	release <- struct{}{}
	if err := testForwardWait(t, result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("replay crossed H")
	}
	// These appends precede tail registration; the log must bridge that gap.
	got = nil
	go func() {
		result <- r.Tail(ctx, func(es []Entry) error {
			got = append(got, es...)
			if len(got) == len(later) {
				cancel()
			}
			return nil
		})
	}()
	if err := testForwardWait(t, result); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, later) {
		t.Fatal("tail lost entries committed during replay")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.followers) != 0 {
		t.Fatal("tail registration leaked")
	}
}

func TestForwardTailContinuity(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 2)
	seen := make(chan uint64, 64)
	for range 2 {
		r := testForwardReader(t, s, 0)
		go func() {
			results <- r.Tail(ctx, func(es []Entry) error {
				for _, e := range es {
					seen <- e.ID
				}
				return nil
			})
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.followers[convA])
		s.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tails did not register")
		}
		runtime.Gosched()
	}
	for i := 0; i < 10; i++ {
		id, err := s.AppendWithMetadata(convA, "unknown", json.RawMessage(`{}`), testTS, Metadata{})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if got := testForwardWait(t, seen); got != id {
				t.Fatalf("got %d, want %d", got, id)
			}
		}
	}
	cancel()
	for range 2 {
		if err := testForwardWait(t, results); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.followers) != 0 {
		t.Fatal("canceled registrations retained")
	}
}

func TestForwardStops(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	appendN(t, s, convA, 0, 20)
	r := testForwardReader(t, s, 0)
	stop := errors.New("consumer stopped")
	if err := r.Walk(context.Background(), 20, func([]Entry) error { return stop }); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if r.LastEntryID() != 0 {
		t.Fatal("failed callback advanced progress")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	before := s.segmentsOpened
	s.mu.Unlock()
	var first uint64
	err := r.Walk(ctx, 20, func(es []Entry) error { first = es[0].ID; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || first != 1 {
		t.Fatalf("retry/cancellation: %d %v", first, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.segmentsOpened-before != 1 {
		t.Fatal("cancellation read subsequent segments")
	}
}

func TestForwardTailReadToWait(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	appendN(t, s, convA, 0, 10)
	r := testForwardReader(t, s, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ids []uint64
	err := r.Tail(ctx, func(es []Entry) error {
		for _, e := range es {
			ids = append(ids, e.ID)
		}
		switch es[len(es)-1].ID {
		case 10:
			// Commit after the last read but before the consumer starts waiting.
			_, err := s.AppendWithMetadata(convA, "future", json.RawMessage(`{}`), testTS, Metadata{})
			return err
		case 11:
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}) {
		t.Fatal(ids)
	}
}
