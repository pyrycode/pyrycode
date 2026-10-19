package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadStreamingRevisions(t *testing.T) {
	t.Parallel()
	f := thread.New("conv")
	rows := make(map[uint64]protocol.ThreadItem)
	var text string
	changed, appended := 0, 0
	deltas := []string{"hello", " 世界\n\"\\", strings.Repeat("x", 300), " tail", " again"}
	entries := []history.Entry{{ID: 1, Type: "main_turn_opened", Payload: json.RawMessage(`{"turn_id":"t","occurred_at":"2026-01-01T00:00:00Z"}`)}}
	for i, delta := range deltas {
		raw, err := json.Marshal(map[string]any{"turn_id": "t", "text": delta, "future": map[string]any{"nested": true}})
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, history.Entry{ID: uint64(i + 2), Type: "assistant_delta", Payload: raw})
	}
	entries = append(entries, history.Entry{ID: uint64(len(entries) + 1), Type: "turn_end", Payload: json.RawMessage(`{"turn_id":"t","stop_reason":"end_turn"}`)})
	for _, entry := range entries {
		o, err := f.FeedChanges([]history.Entry{entry})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range o.Changes {
			for _, update := range daemonThreadUpdates("conv", "epoch", c) {
				held := rows[c.ID]
				env, _ := testDaemonThreadEncodedUpdate(t, update)
				switch env.Type {
				case protocol.TypeThreadItemAdded:
					var u protocol.ThreadItemAddedPayload
					if err := json.Unmarshal(env.Payload, &u); err != nil {
						t.Fatal(err)
					}
					held = u.Item
				case protocol.TypeThreadItemChanged:
					var u protocol.ThreadItemChangedPayload
					if err := json.Unmarshal(env.Payload, &u); err != nil {
						t.Fatal(err)
					}
					testDaemonThreadRevision(t, held, u.ItemID, u.BaseRev, u.Rev)
					raw, err := json.Marshal(held)
					if err != nil {
						t.Fatal(err)
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(raw, &fields); err != nil {
						t.Fatal(err)
					}
					for key, value := range u.Changes {
						fields[key] = value
					}
					raw, err = json.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(raw, &held); err != nil {
						t.Fatal(err)
					}
					held.Rev = u.Rev
					changed++
				case protocol.TypeThreadTextAppend:
					var u protocol.ThreadTextAppendPayload
					if err := json.Unmarshal(env.Payload, &u); err != nil {
						t.Fatal(err)
					}
					testDaemonThreadRevision(t, held, u.ItemID, u.BaseRev, u.Rev)
					var content map[string]any
					if err := json.Unmarshal(held.Content, &content); err != nil {
						t.Fatal(err)
					}
					content["text"] = content["text"].(string) + u.Text
					held.Content, err = json.Marshal(content)
					if err != nil {
						t.Fatal(err)
					}
					held.Rev = u.Rev
					appended++
				default:
					t.Fatalf("unexpected update type %s", env.Type)
				}
				rows[held.ID] = held
			}
		}
		if entry.Type == "assistant_delta" {
			text += deltas[int(entry.ID)-2]
		}
		if entry.ID == 1 {
			continue
		}
		items := f.Items()
		if len(items) != len(rows) {
			t.Fatalf("entry %d: held %d items, fold has %d", entry.ID, len(rows), len(items))
		}
		for _, item := range items {
			testDaemonThreadJSONEqual(t, rows[item.ID], daemonThreadItem(item))
		}
		var content struct{ Text string }
		if err := json.Unmarshal(rows[2].Content, &content); err != nil || content.Text != text {
			t.Fatalf("entry %d: accumulated text %q, want %q (error %v)", entry.ID, content.Text, text, err)
		}
	}
	if changed < 2 || appended < 2 {
		t.Fatalf("missing combined or suffix-only updates: changed=%d appended=%d", changed, appended)
	}
}

func TestDaemonThreadCombinedAssembly(t *testing.T) {
	t.Parallel()
	content, err := json.Marshal(map[string]any{"text": "beforeafter", "future": strings.Repeat("<&>雪\"", 20000), "clear": nil})
	if err != nil {
		t.Fatal(err)
	}
	c := thread.Change{
		ID: 1, PreviousRev: 2, Rev: 5, Version: 7,
		Fields:     map[string]json.RawMessage{"Summary": json.RawMessage(`""`), "Active": json.RawMessage(`false`)},
		TextAppend: "after", ResultContent: content,
	}
	updates := daemonThreadUpdates("conv", "epoch", c)
	if len(updates) != 1 {
		t.Fatalf("combined update count %d", len(updates))
	}
	env, count := testDaemonThreadEncodedUpdate(t, updates[0])
	if count < 2 || env.Type != protocol.TypeThreadItemChanged {
		t.Fatal("combined update did not use bounded changed assembly")
	}
	var got protocol.ThreadItemChangedPayload
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatal(err)
	}
	testDaemonThreadJSONEqual(t, got, protocol.ThreadItemChangedPayload{ConversationID: "conv", Epoch: "epoch", Version: 7, ItemID: 1, BaseRev: 2, Rev: 5, Changes: map[string]json.RawMessage{"summary": json.RawMessage(`""`), "active": json.RawMessage(`false`), "content": content}})
}

func testDaemonThreadRevision(t *testing.T, held protocol.ThreadItem, id, base, rev uint64) {
	t.Helper()
	if id != held.ID || base != held.Rev || rev <= base {
		t.Fatalf("invalid revision transition: held=%d/%d update=%d/%d->%d", held.ID, held.Rev, id, base, rev)
	}
}

func testDaemonThreadJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	normalize := func(value any) any {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	if !reflect.DeepEqual(normalize(got), normalize(want)) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func testDaemonThreadEncodedUpdate(t *testing.T, update any) (protocol.Envelope, int) {
	t.Helper()
	frames, err := protocol.EncodeThreadUpdate(protocol.Envelope{}, update)
	if err != nil {
		t.Fatal(err)
	}
	var env protocol.Envelope
	var logical strings.Builder
	for i, frame := range frames {
		if len(frame) > protocol.MaxThreadEnvelopeBytes {
			t.Fatalf("oversized envelope: %d", len(frame))
		}
		if err := json.Unmarshal(frame, &env); err != nil {
			t.Fatal(err)
		}
		var part protocol.ThreadUpdatePart
		if err := json.Unmarshal(env.Payload, &part); err != nil {
			t.Fatal(err)
		}
		if part.Continuation == nil {
			if len(frames) != 1 {
				t.Fatal("ordinary update mixed with parts")
			}
			return env, 1
		}
		if part.Continuation.Index != uint64(i) || part.Continuation.Offset != uint64(logical.Len()) || part.Continuation.Final != (i == len(frames)-1) {
			t.Fatal("invalid assembly sequence")
		}
		logical.WriteString(part.Data)
		if part.Continuation.Final && part.Continuation.TotalBytes != uint64(logical.Len()) {
			t.Fatal("incomplete logical update")
		}
	}
	env.Payload = json.RawMessage(logical.String())
	return env, len(frames)
}
