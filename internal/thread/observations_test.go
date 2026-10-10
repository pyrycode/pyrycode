package thread

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

// Apply the observation contract independently of the production differ.
func testApplyObservation(t *testing.T, items []Item, o Observation) []Item {
	t.Helper()
	if o.BaselineRequired {
		t.Fatal("unexpected reset")
	}
	byID := make(map[uint64]Item)
	for _, item := range items {
		byID[item.ID] = item
	}
	for _, c := range o.Changes {
		if c.Version > o.Version || c.Version <= o.FromVersion {
			t.Fatal("change outside range", c)
		}
		if c.Addition != nil {
			byID[c.ID] = *c.Addition
			continue
		}
		item := byID[c.ID]
		if item.Rev != c.PreviousRev {
			t.Fatal("revision gap", item, c)
		}
		v := reflect.ValueOf(&item).Elem()
		for name, raw := range c.Fields {
			if err := json.Unmarshal(raw, v.FieldByName(name).Addr().Interface()); err != nil {
				t.Fatal(err)
			}
		}
		if c.TextAppend != "" {
			var content map[string]json.RawMessage
			if err := json.Unmarshal(item.Content, &content); err != nil {
				t.Fatal(err)
			}
			var text string
			if err := json.Unmarshal(content["text"], &text); err != nil {
				t.Fatal(err)
			}
			content["text"], _ = json.Marshal(text + c.TextAppend)
			item.Content, _ = json.Marshal(content)
		}
		item.Rev = c.Rev
		byID[c.ID] = item
	}
	var result []Item
	for _, item := range byID {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if !sameItems(result, o.Items) && o.Items != nil {
		t.Fatal("apply mismatch")
	}
	return result
}

func TestFoldObservations(t *testing.T) {
	t.Parallel()
	hidden := false
	entries := []history.Entry{
		testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
		testMain(2, "assistant_delta", `,"text":"hello"`),
		testMain(3, "assistant_delta", `,"text":" world"`),
		testMain(4, "assistant_delta", `,"text":""`),
		testMain(5, "tool_use", `,"tool_use_id":"x","name":"Read","input":{}`),
		testMain(6, "turn_end", `,"stop_reason":"end_turn"`),
		testAcceptance(7, "phone"),
		testSendOutcome(8, "send_dropped", `,"accepted_entry_id":7`, "removed"),
		testMessage(9), testEntry(10, "unknown", `{}`), testEntry(11, "banner", `{`),
		testEntry(12, "message", `{"conversation_id":"foreign","role":"user","text":"wrong"}`),
	}
	entries[8].Shown = &hidden
	wantShown := []uint64{0, 2, 3, 3, 5, 5, 7, 7, 7, 7, 7, 7}
	f := New("a")
	var applied []Item
	for i, e := range entries {
		o, err := f.FeedChanges([]history.Entry{e})
		if err != nil {
			t.Fatal(err)
		}
		applied = testApplyObservation(t, applied, o)
		if !sameItems(applied, f.Items()) || o.LastShownVersion != wantShown[i] || o.Version != e.ID {
			t.Fatalf("entry %d: %#v", e.ID, o)
		}
		if i == 2 && (len(o.Changes) != 1 || o.Changes[0].TextAppend != " world") {
			t.Fatal("missing suffix", o)
		}
	}
	chunk := New("a")
	testFeed(t, chunk, entries...)
	if chunk.Observe().LastShownVersion != 7 || !sameItems(chunk.Items(), applied) {
		t.Fatal("chunk divergence")
	}
	base := f.Observe()
	base.Items[0].Content[0] = '!'
	if !sameItems(f.Items(), applied) {
		t.Fatal("baseline alias")
	}
}

func TestObservationReplacement(t *testing.T) {
	before := Item{ID: 1, Rev: 1, Kind: "assistant_message", Content: json.RawMessage(`{"text":"old","extra":1}`), Summary: "old"}
	for _, raw := range []string{`{"text":"new"}`, `{"text":""}`, `null`, `{"text":"old suffix","extra":2}`} {
		after := before
		after.Rev = 2
		after.Content = json.RawMessage(raw)
		var content any
		_ = json.Unmarshal(after.Content, &content)
		after.Content, _ = json.Marshal(content)
		after.Summary = ""
		o := difference(Snapshot{State: StateUsable, Version: 1, Items: []Item{before}}, Snapshot{State: StateUsable, Version: 2, Items: []Item{after}})
		got := testApplyObservation(t, []Item{before}, o)
		if !sameItems(got, []Item{after}) {
			t.Fatalf("replacement %s: %#v", raw, got)
		}
	}
}

func TestStoreObservations(t *testing.T) {
	s, h := testThreadStore(t)
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 0)
	base := s.Observe(testStoreA)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	testStoreWait(t, s, testStoreA, StateUsable, 1) // commit before waiter registration
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o, err := s.Changes(ctx, testStoreA, base.Epoch, base.Version)
	if err != nil {
		t.Fatal(err)
	}
	if o.FromVersion != 0 || o.LastShownVersion != 1 || !sameItems(testApplyObservation(t, base.Items, o), s.Snapshot(testStoreA).Items) {
		t.Fatal(o)
	}
	o.Changes[0].Addition.Content[0] = '!'
	again, err := s.Changes(ctx, testStoreA, base.Epoch, 0)
	if err != nil || again.Changes[0].Addition.Content[0] == '!' {
		t.Fatal("change alias", err)
	}
	testStoreAppend(t, h, testStoreA, testEntry(0, "unknown", `{}`))
	noOp, err := s.Changes(ctx, testStoreA, base.Epoch, 1)
	if err != nil || noOp.Version != 2 || noOp.LastShownVersion != 1 || len(noOp.Changes) != 0 {
		t.Fatal(noOp, err)
	}
}

func TestStoreObservationDelivery(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testAcceptance(1, "phone"))
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	base := testStoreWait(t, s, testStoreA, StateUsable, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	testStoreAppend(t, h, testStoreA, testMessage(2))
	delivery, err := s.Changes(ctx, testStoreA, base.Epoch, 1)
	if err != nil || delivery.BaselineRequired || delivery.LastShownVersion != 2 {
		t.Fatal(delivery, err)
	}
	testStoreAppend(t, h, testStoreA, testSendOutcome(3, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered"))
	linked, err := s.Changes(ctx, testStoreA, base.Epoch, 2)
	if err != nil || !linked.BaselineRequired || len(linked.Changes) != 0 || linked.LastShownVersion != 2 {
		t.Fatal(linked, err)
	}
	fresh := s.Observe(testStoreA)
	if len(fresh.Items) != 1 || fresh.Items[0].Order != 2 || fresh.Items[0].Rev != 3 {
		t.Fatal(fresh)
	}
}

func TestFoldObservationChildren(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"other","name":"Agent"`),
		testChild(3, "tool_use", "missing", `,"tool_use_id":"b","name":"Task"`),
		testChild(4, "assistant_delta", "b", `,"text":"private"`),
		testChild(5, "assistant_delta", "b", `,"text":" suffix"`),
		testChild(6, "tool_result", "a", `,"tool_use_id":"b","is_error":false`),
		testChild(7, "assistant_delta", "b", `,"text":" public"`),
		testMain(8, "tool_denied", `,"tool_use_id":"a"`),
	}
	f := New("a")
	var applied []Item
	for i, e := range entries {
		o, err := f.FeedChanges([]history.Entry{e})
		if err != nil {
			t.Fatal(err)
		}
		applied = testApplyObservation(t, applied, o)
		if !sameItems(applied, f.Items()) {
			t.Fatal("child range", o)
		}
		want := uint64(1)
		if i > 0 {
			want = 2
		}
		if i >= 5 {
			want = 6
		}
		if i >= 6 {
			want = 7
		}
		if o.LastShownVersion != want {
			t.Fatalf("entry %d: %#v", e.ID, o)
		}
		if i == 5 && len(o.Changes) < 2 {
			t.Fatal("missing newly public rows", o)
		}
	}
	full := New("a")
	testFeed(t, full, entries...)
	if full.Observe().LastShownVersion != 7 || !sameItems(full.Items(), applied) {
		t.Fatal("child replay")
	}
}
