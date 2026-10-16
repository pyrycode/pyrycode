package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestStoreToolBodyParity(t *testing.T) {
	s, h := testThreadStore(t)
	large, _ := json.Marshal(strings.Repeat("escaped <&>\n", 12000))
	entries := []history.Entry{
		testMain(0, "tool_use", `,"tool_use_id":"parent","name":"Agent","input":{},"future":[1,2]`),
		testChild(0, "tool_use", "parent", `,"tool_use_id":"r","name":"Read","input":{},"future":{"a":true}`),
		testChild(0, "tool_result", "parent", `,"tool_use_id":"r","is_error":false,"output":"","future_result":[]`),
		testMain(0, "tool_result", `,"tool_use_id":"parent","is_error":false,"output":{},"future_result":42`),
		testMain(0, "tool_use", `,"tool_use_id":"big","name":"Read","input":{"data":`+string(large)+`}`),
		testMain(0, "tool_result", `,"tool_use_id":"big","is_error":false,"output":`+string(large)),
	}
	entries[1].Shown = new(bool)
	for _, id := range []conversations.ConversationID{testStoreA, testStoreB} {
		scoped := append([]history.Entry(nil), entries...)
		scoped[0] = testMain(0, "tool_use", fmt.Sprintf(`,"tool_use_id":"parent","name":"Agent","input":{},"future":[1,2],"owner":%q`, id))
		written := testStoreAppend(t, h, id, scoped...)
		if err := s.Load(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		snap := testStoreWait(t, s, id, StateUsable, 6)
		if len(snap.Items) != 3 || snap.Items[1].ID != 2 || snap.Items[1].Shown || snap.Items[2].ID != 5 {
			t.Fatal("missing tool/agent/hidden/large fixture rows", snap)
		}
		f := New(string(id))
		testFeed(t, f, written...)
		testQueryItems(t, snap.Items, f.Items())
		for _, item := range snap.Items {
			got, err := s.ToolBody(context.Background(), id, snap.Epoch, item.ID, item.Rev)
			if err != nil || got.State != StateUsable || got.ID != item.ID || got.Rev != item.Rev || got.Epoch != snap.Epoch || got.Version != snap.Version || !reflect.DeepEqual(got.Content, item.Content) {
				t.Fatalf("body %d differs from complete renderer: %+v, %v", item.ID, got, err)
			}
			got.Content[0] = '!'
			again, err := s.ToolBody(context.Background(), id, snap.Epoch, item.ID, item.Rev)
			if err != nil || !reflect.DeepEqual(again.Content, item.Content) {
				t.Fatal("aliased output", err)
			}
		}
		// An entry that changes no row advances consumed version without staling bodies.
		testStoreAppend(t, h, id, testEntry(0, "ignored", `{}`))
		testStoreWait(t, s, id, StateUsable, 7)
		got, err := s.ToolBody(context.Background(), id, snap.Epoch, 1, snap.Items[0].Rev)
		if err != nil || got.Version != 7 || got.Epoch != snap.Epoch {
			t.Fatal(got, err)
		}
	}
	a, b := s.Status(testStoreA), s.Status(testStoreB)
	if a.Epoch == b.Epoch {
		t.Fatal("fixture did not isolate conversations")
	}
	if got, err := s.ToolBody(context.Background(), testStoreB, a.Epoch, 1, 4); !errors.Is(err, ErrStaleBody) || !reflect.DeepEqual(got, ToolBodyResult{}) {
		t.Fatal(got, err)
	}
	got, err := s.ToolBody(context.Background(), testStoreA, a.Epoch, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]json.RawMessage
	if json.Unmarshal(got.Content, &content) != nil || string(content["input"]) != `{}` || string(content["future"]) != `{"a":true}` {
		t.Fatal(string(got.Content))
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(content["result"], &result) != nil || string(result["output"]) != `""` || string(result["future_result"]) != `[]` {
		t.Fatal(string(got.Content))
	}
}

func TestStoreToolBodyRefusals(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA,
		testMain(0, "tool_use", `,"tool_use_id":"r","name":"Read"`),
		testChild(0, "tool_use", "absent", `,"tool_use_id":"private","name":"Read"`),
		testAcceptance(0, "phone"), testMessage(0),
		testSendOutcome(0, "send_delivered", `,"accepted_entry_id":3,"delivery_entry_id":4`, "delivered"),
		testAcceptance(0, "other"),
		testSendOutcome(0, "send_dropped", `,"accepted_entry_id":6`, "removed"))
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	snap := testStoreWait(t, s, testStoreA, StateUsable, 7)
	for _, tc := range []struct {
		name      string
		id        conversations.ConversationID
		epoch     string
		item, rev uint64
		want      error
	}{
		{"invalid id", "../bad", snap.Epoch, 1, 1, history.ErrInvalidID},
		{"noncanonical", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", "epoch", 1, 1, history.ErrInvalidID},
		{"empty epoch", testStoreA, "", 1, 1, ErrInvalidBodyScope},
		{"zero item", testStoreA, snap.Epoch, 0, 1, ErrInvalidBodyScope},
		{"zero revision", testStoreA, snap.Epoch, 1, 0, ErrInvalidBodyScope},
		{"stale epoch", testStoreA, "old", 1, 1, ErrStaleBody},
		{"stale revision", testStoreA, snap.Epoch, 1, 9, ErrStaleBody},
		{"missing", testStoreA, snap.Epoch, 99, 99, ErrBodyNotFound},
		{"private", testStoreA, snap.Epoch, 2, 2, ErrBodyNotFound},
		{"claimed delivery", testStoreA, snap.Epoch, 4, 4, ErrBodyNotFound},
		{"wrong kind", testStoreA, snap.Epoch, 3, 5, ErrBodyKind},
		{"public dropped", testStoreA, snap.Epoch, 6, 7, ErrBodyKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ToolBody(context.Background(), tc.id, tc.epoch, tc.item, tc.rev)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, ToolBodyResult{}) {
				t.Fatal(got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := s.ToolBody(ctx, testStoreA, snap.Epoch, 1, 1); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, ToolBodyResult{}) {
		t.Fatal(got, err)
	}
	// Reads on a cold conversation leave it cold.
	if got, err := s.ToolBody(context.Background(), testStoreB, "epoch", 1, 1); err != nil || !reflect.DeepEqual(got, ToolBodyResult{}) || s.Status(testStoreB).State != StateNotLoaded {
		t.Fatal(got, err)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ToolBody(context.Background(), testStoreA, snap.Epoch, 1, 1); err != nil || !reflect.DeepEqual(got, ToolBodyResult{}) {
		t.Fatal(got, err)
	}
}

func TestStoreToolBodyBounded(t *testing.T) {
	for _, count := range []int{10, 120} {
		for _, size := range []int{16, 4096} {
			t.Run(fmt.Sprintf("%d/%d", count, size), func(t *testing.T) {
				s, h := testThreadStore(t)
				entries := testEndingEntries()[:4]
				for i := range count {
					message := testEntry(0, "message", fmt.Sprintf(`{"role":"user","text":"%s"}`, strings.Repeat("m", size)))
					entries = append(entries, message, testChild(0, "tool_use", "nested", fmt.Sprintf(`,"tool_use_id":"desc-%d","name":"Read","input":{"data":"%s"}`, i, strings.Repeat("x", size))))
				}
				testStoreAppend(t, h, testStoreA, entries...)
				var armed atomic.Bool
				var visits atomic.Int64
				s.queryWork = func(preparing bool) {
					if !preparing && armed.Load() {
						visits.Add(1)
					}
				}
				lookup := s.lookup
				var resolved []uint64
				s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
					if ctx.Value(outputRequestKey{}) != nil {
						resolved = append(resolved, ids...)
					}
					return lookup(ctx, id, ids)
				}
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(30 * time.Second)
				for s.Status(testStoreA).Version != uint64(len(entries)) {
					if time.Now().After(deadline) {
						t.Fatal("bounded fixture did not finish preparation")
					}
					time.Sleep(time.Millisecond)
				}
				snap := s.Snapshot(testStoreA)
				for _, tc := range []struct {
					id     uint64
					refs   []uint64
					visits int64
				}{
					{1, []uint64{1, 4}, 2},
					{2, []uint64{1, 2, 4}, 3},
				} {
					var item Item
					for _, row := range snap.Items {
						if row.ID == tc.id {
							item = row
						}
					}
					resolved = nil
					visits.Store(0)
					armed.Store(true)
					got, err := s.ToolBody(context.WithValue(context.Background(), outputRequestKey{}, true), testStoreA, snap.Epoch, item.ID, item.Rev)
					armed.Store(false)
					if err != nil || !reflect.DeepEqual(got.Content, item.Content) || !reflect.DeepEqual(resolved, tc.refs) || visits.Load() != tc.visits {
						t.Fatalf("id=%d refs=%v visits=%d err=%v", tc.id, resolved, visits.Load(), err)
					}
				}
			})
		}
	}
}

func TestStoreToolBodyCapture(t *testing.T) {
	for _, mode := range []string{"publication", "unload", "replacement", "old failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, h := testThreadStore(t)
			testStoreAppend(t, h, testStoreA, testMain(0, "tool_use", `,"tool_use_id":"r","name":"Read","input":{}`))
			lookup := s.lookup
			reached, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
				if ctx.Value(outputRequestKey{}) != nil {
					close(reached)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					if mode == "old failure" {
						return nil, errors.New("private source path")
					}
				}
				return lookup(ctx, id, ids)
			}
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			snap := testStoreWait(t, s, testStoreA, StateUsable, 1)
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), outputRequestKey{}, true))
			defer cancel()
			type outcome struct {
				result ToolBodyResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() { got, err := s.ToolBody(ctx, testStoreA, snap.Epoch, 1, 1); done <- outcome{got, err} }()
			testStoreSignal(t, reached)
			independent, err := s.ToolBody(context.Background(), testStoreA, snap.Epoch, 1, 1)
			if err != nil || !reflect.DeepEqual(independent.Content, snap.Items[0].Content) {
				t.Fatal(independent, err)
			}
			switch mode {
			case "publication":
				testStoreAppend(t, h, testStoreA, testMain(0, "tool_result", `,"tool_use_id":"r","is_error":false,"output":"new"`))
				testStoreWait(t, s, testStoreA, StateUsable, 2)
				if got, err := s.ToolBody(context.Background(), testStoreA, snap.Epoch, 1, 1); !errors.Is(err, ErrStaleBody) || !reflect.DeepEqual(got, ToolBodyResult{}) {
					t.Fatal(got, err)
				}
			case "unload", "replacement", "old failure":
				if err := s.Unload(testStoreA); err != nil {
					t.Fatal(err)
				}
				if got, err := s.ToolBody(context.Background(), testStoreA, snap.Epoch, 1, 1); err != nil || !reflect.DeepEqual(got, ToolBodyResult{}) {
					t.Fatal(got, err)
				}
				if mode != "unload" {
					if err := s.Load(context.Background(), testStoreA); err != nil {
						t.Fatal(err)
					}
					testStoreWait(t, s, testStoreA, StateUsable, 1)
				}
			case "cancel":
				cancel()
			}
			once.Do(func() { close(release) })
			got := <-done
			switch mode {
			case "cancel":
				if !errors.Is(got.err, context.Canceled) || !reflect.DeepEqual(got.result, ToolBodyResult{}) {
					t.Fatal(got)
				}
			case "old failure":
				if got.err != nil || !reflect.DeepEqual(got.result, ToolBodyResult{State: StateUnavailable, Err: ErrUnavailable}) {
					t.Fatal(got)
				}
			default:
				if got.err != nil || got.result.Version != snap.Version || got.result.Epoch != snap.Epoch || got.result.Rev != 1 || !reflect.DeepEqual(got.result.Content, snap.Items[0].Content) {
					t.Fatal("capture changed", got)
				}
			}
			if mode != "unload" && s.Status(testStoreA).State != StateUsable {
				t.Fatal("reader retired healthy worker")
			}
			if mode != "unload" {
				rev := uint64(1)
				if mode == "publication" {
					rev = 2
				}
				if body, err := s.ToolBody(context.Background(), testStoreA, s.Status(testStoreA).Epoch, 1, rev); err != nil || body.State != StateUsable {
					t.Fatal("independent read failed after held reader finished", body, err)
				}
			}
		})
	}
}

func TestStoreToolBodyUnavailable(t *testing.T) {
	for _, mode := range []string{"missing", "unreadable", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			s, h := testThreadStore(t)
			testStoreAppend(t, h, testStoreA, testMain(0, "tool_use", `,"tool_use_id":"r","name":"Read"`))
			lookup := s.lookup
			var armed atomic.Bool
			s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
				if armed.Load() {
					switch mode {
					case "missing":
						return nil, nil
					case "unreadable":
						return nil, errors.New("private source path")
					case "malformed":
						return []history.Entry{{ID: 1, Payload: []byte(`null`)}}, nil
					}
				}
				return lookup(ctx, id, ids)
			}
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			snap := testStoreWait(t, s, testStoreA, StateUsable, 1)
			armed.Store(true)
			for range 2 {
				got, err := s.ToolBody(context.Background(), testStoreA, snap.Epoch, 1, 1)
				if err != nil || !reflect.DeepEqual(got, ToolBodyResult{State: StateUnavailable, Err: ErrUnavailable}) {
					t.Fatal(got, err)
				}
			}
			if s.Status(testStoreA).State != StateUnavailable {
				t.Fatal("failed worker retained publication")
			}
		})
	}
}

func TestStoreToolBodyRebuilding(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	forward := s.forward
	reached, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	s.forward = func(id conversations.ConversationID) (storeReader, error) {
		r, err := forward(id)
		return testStoreReader{storeReader: r, walk: func(ctx context.Context, upper uint64, feed func([]history.Entry) error) error {
			close(reached)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return r.Walk(ctx, upper, feed)
		}}, err
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, reached)
	got, err := s.ToolBody(context.Background(), testStoreA, "epoch", 1, 1)
	if err != nil || !reflect.DeepEqual(got, ToolBodyResult{State: StateRebuilding}) {
		t.Fatal(got, err)
	}
}
