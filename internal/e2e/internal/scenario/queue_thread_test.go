package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func queueThreadFrame(t *testing.T, conv, text string, id uint64, status string) protocol.Envelope {
	t.Helper()
	content, err := json.Marshal(map[string]any{"text": text, "queued_msg_id": id})
	if err != nil {
		t.Fatal(err)
	}
	item := protocol.ThreadItem{ID: 70, Rev: 70, Kind: "user_message", Status: status, Active: status == "queued", Shown: true, Content: content}
	if status == "delivered" {
		item.Order = 80
		item.Rev = 81
	}
	raw, err := json.Marshal(protocol.ThreadItemAddedPayload{ConversationID: conv, Epoch: "epoch", Version: item.Rev, Item: item})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Envelope{Type: protocol.TypeThreadItemAdded, Payload: raw}
}

func TestQueueCompletionCheckpoint(t *testing.T) {
	stamp := time.Now().UTC()
	frame := func(kind string, p any, second int) protocol.Envelope {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw, TS: stamp.Add(time.Duration(second) * time.Second)}
	}
	var source []history.Entry
	for i, turn := range []string{"first", "second"} {
		id := uint64(10 + i*10)
		raw, _ := json.Marshal(map[string]string{"conversation_id": "conv", "turn_id": turn, "stop_reason": "end_turn"})
		source = append(source, history.Entry{ID: id, TS: stamp.Add(time.Duration(id) * time.Second), Type: protocol.TypeTurnEnd, Payload: raw, Session: &history.SessionProvenance{Kind: "claude", SessionID: "producer"}})
	}
	var frames []protocol.Envelope
	var requests uint64
	w := Wire{
		Source: func(string) []history.Entry { return source },
		Send: func(kind string, value any) uint64 {
			if kind != protocol.TypeThreadCatchUp {
				t.Fatal("unexpected checkpoint command")
			}
			requests++
			p := value.(protocol.ThreadCatchUpPayload)
			if requests == 2 {
				env := frame(protocol.TypeError, protocol.ErrorPayload{Code: "thread.query_failed", Retryable: true}, 22)
				id := requests
				env.InReplyTo = &id
				frames = append(frames, env)
				return id
			}
			version := uint64(1)
			if requests > 1 {
				if p.Epoch != "epoch" || p.Version == 0 {
					t.Fatal("checkpoint did not retain canonical baseline")
				}
				version = 20
				// A non-final batch cannot certify the hidden completion.
				frames = append(frames, frame(protocol.TypeThreadItems, protocol.ThreadItemsPayload{ConversationID: "conv", Epoch: "epoch", Version: version}, 22))
			}
			env := frame(protocol.TypeThreadItems, protocol.ThreadItemsPayload{ConversationID: "conv", Epoch: "epoch", Version: version, Final: true}, 22)
			id := requests
			env.InReplyTo = &id
			frames = append(frames, env)
			return id
		},
		Next: func(time.Time) protocol.Envelope {
			if len(frames) == 0 {
				return protocol.Envelope{}
			}
			env := frames[0]
			frames = frames[1:]
			return env
		},
	}
	r := newQueuePlacementReader(t, w, "conv")
	for i, text := range []string{"one", "two"} {
		id := uint64(2 + i*10)
		content, _ := json.Marshal(map[string]string{"text": text})
		item := protocol.ThreadItem{ID: id, Rev: id, Order: id, Kind: "assistant_message", Content: content}
		frames = append(frames, frame(protocol.TypeThreadItemAdded, protocol.ThreadItemAddedPayload{ConversationID: "conv", Epoch: "epoch", Version: id, Item: item}, int(id)))
		frames = append(frames, frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: "responding"}, 3+i*10))
		frames = append(frames, frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: "idle"}, 9+i*10))
	}
	var got []string
	for len(got) < 4 {
		f, ok := r.next(time.Now().Add(100 * time.Millisecond))
		if !ok {
			t.Fatalf("hidden source completion did not settle: %v", got)
		}
		if f.Kind == "delta" || f.Kind == "turn_end" {
			got = append(got, f.Kind+":"+f.Text)
		}
	}
	if want := "[delta:one turn_end: delta:two turn_end:]"; fmt.Sprint(got) != want {
		t.Fatalf("two-turn boundary = %v, want %s", got, want)
	}
	if requests != 3 {
		t.Fatalf("checkpoint requests = %d, want baseline, rejected checkpoint and successful retry", requests)
	}
}

func TestQueueCompletionNeedsFinalAndIdle(t *testing.T) {
	stamp := time.Now().UTC()
	raw, _ := json.Marshal(map[string]string{"conversation_id": "conv", "turn_id": "turn", "stop_reason": "end_turn"})
	end := history.Entry{ID: 10, TS: stamp, Type: protocol.TypeTurnEnd, Payload: raw, Session: &history.SessionProvenance{Kind: "claude", SessionID: "producer"}}
	var frames []protocol.Envelope
	checkpoint := func(reply uint64, final bool) protocol.Envelope {
		raw, _ := json.Marshal(protocol.ThreadItemsPayload{ConversationID: "conv", Epoch: "epoch", Version: 10, Final: final})
		return protocol.Envelope{Type: protocol.TypeThreadItems, InReplyTo: &reply, Payload: raw}
	}
	idle := func(ts time.Time) protocol.Envelope {
		raw, _ := json.Marshal(protocol.TurnStatePayload{ConversationID: "conv", State: "idle"})
		return protocol.Envelope{Type: protocol.TypeTurnState, TS: ts, Payload: raw}
	}
	r := &queuePlacementReader{t: t, conv: "conv", epoch: "epoch", version: 1, checkpoint: 1, items: map[uint64]protocol.ThreadItem{}, w: Wire{
		Source: func(string) []history.Entry { return []history.Entry{end} },
		Send:   func(string, any) uint64 { return 99 },
		Next: func(time.Time) protocol.Envelope {
			if len(frames) == 0 {
				return protocol.Envelope{}
			}
			e := frames[0]
			frames = frames[1:]
			return e
		},
	}}
	check := func(want bool) {
		t.Helper()
		for {
			f, ok := r.next(time.Now().Add(5 * time.Millisecond))
			if f.Kind == "turn_end" {
				if !want {
					t.Fatal("incomplete checkpoint or predecessor idle settled a turn")
				}
				return
			}
			if !ok {
				if want {
					t.Fatal("final checkpoint and actual idle did not settle")
				}
				return
			}
		}
	}
	frames = append(frames, idle(stamp.Add(-time.Second)), checkpoint(98, true), checkpoint(99, false))
	check(false)
	frames = append(frames, checkpoint(99, true))
	check(false)
	running := idle(stamp.Add(-500 * time.Millisecond))
	running.Payload, _ = json.Marshal(protocol.TurnStatePayload{ConversationID: "conv", State: "responding"})
	foreign := running
	foreign.SessionID = json.RawMessage(`"another-producer"`)
	foreignIdle := idle(stamp.Add(-100 * time.Millisecond))
	foreignIdle.SessionID = foreign.SessionID
	frames = append(frames, foreign, foreignIdle)
	check(false)
	running.SessionID = json.RawMessage(`"producer"`)
	currentIdle := idle(stamp.Add(-100 * time.Millisecond))
	currentIdle.SessionID = running.SessionID
	frames = append(frames, running, currentIdle)
	check(true)
	check(false) // repeated source snapshots cannot complete the same turn twice
}

func TestQueueBufferedSuccessorRevisions(t *testing.T) {
	stamp := time.Now().UTC()
	frame := func(kind string, payload any) protocol.Envelope {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw, TS: stamp}
	}
	var source []history.Entry
	for i, turn := range []string{"first", "second"} {
		raw, _ := json.Marshal(map[string]string{"conversation_id": "conv", "turn_id": turn, "stop_reason": "end_turn"})
		source = append(source, history.Entry{ID: uint64(10 + i*10), Type: protocol.TypeTurnEnd, Payload: raw, Session: &history.SessionProvenance{Kind: "claude", SessionID: "producer"}})
	}
	user := protocol.ThreadItem{ID: 3, Rev: 11, Order: 11, Kind: "user_message", Status: "delivered", Shown: true, Content: json.RawMessage(`{"text":"queued","queued_msg_id":3}`)}
	assistant := protocol.ThreadItem{ID: 13, Rev: 13, Order: 13, Kind: "assistant_message", Active: true, Shown: true, Content: json.RawMessage(`{"text":"two"}`)}
	frames := []protocol.Envelope{
		frame(protocol.TypeThreadItemAdded, protocol.ThreadItemAddedPayload{ConversationID: "conv", Epoch: "epoch", Version: 11, Item: user}),
		frame(protocol.TypeThreadItemChanged, protocol.ThreadItemChangedPayload{ConversationID: "conv", Epoch: "epoch", Version: 12, ItemID: user.ID, BaseRev: 11, Rev: 12, Changes: map[string]json.RawMessage{"status": json.RawMessage(`"delivered"`)}}),
		frame(protocol.TypeThreadItemAdded, protocol.ThreadItemAddedPayload{ConversationID: "conv", Epoch: "epoch", Version: 13, Item: assistant}),
		frame(protocol.TypeThreadTextAppend, protocol.ThreadTextAppendPayload{ConversationID: "conv", Epoch: "epoch", Version: 14, ItemID: assistant.ID, BaseRev: 13, Rev: 14, Text: "!"}),
		frame(protocol.TypeThreadItemChanged, protocol.ThreadItemChangedPayload{ConversationID: "conv", Epoch: "epoch", Version: 15, ItemID: assistant.ID, BaseRev: 14, Rev: 15, Changes: map[string]json.RawMessage{"active": json.RawMessage(`false`), "status": json.RawMessage(`"done"`)}}),
	}
	for range 2 {
		for _, state := range []string{"responding", "idle"} {
			env := frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: state})
			env.SessionID = json.RawMessage(`"producer"`)
			frames = append(frames, env)
		}
	}
	user.Rev = 12
	assistant.Rev, assistant.Active, assistant.Status = 15, false, "done"
	assistant.Content = json.RawMessage(`{"text":"two!"}`)
	var requests int
	r := &queuePlacementReader{t: t, conv: "conv", epoch: "epoch", version: 1, checkpoint: 1, items: map[uint64]protocol.ThreadItem{}, w: Wire{
		Source: func(string) []history.Entry { return source },
		Send: func(kind string, payload any) uint64 {
			requests++
			if kind != protocol.TypeThreadCatchUp || requests != 1 {
				t.Fatal("unexpected completion query")
			}
			p := payload.(protocol.ThreadCatchUpPayload)
			if p.Epoch != "epoch" || p.Version != 1 {
				t.Fatal("lost canonical baseline")
			}
			// Full rows can repeat already received revisions while placement
			// remains buffered behind the first source completion.
			frames = append(frames, frame(protocol.TypeThreadItems, protocol.ThreadItemsPayload{ConversationID: "conv", Epoch: "epoch", Version: 20, Items: []protocol.ThreadItem{user, assistant}}))
			final := frame(protocol.TypeThreadItems, protocol.ThreadItemsPayload{ConversationID: "conv", Epoch: "epoch", Version: 20, FromVersion: &p.Version, Final: true})
			id := uint64(99)
			final.InReplyTo = &id
			frames = append(frames, final)
			return id
		},
		Next: func(time.Time) protocol.Envelope {
			if len(frames) == 0 {
				return protocol.Envelope{}
			}
			env := frames[0]
			frames = frames[1:]
			return env
		},
	}}
	var got []string
	for ends := 0; ends < 2; {
		f, ok := r.next(time.Now().Add(time.Second))
		if !ok {
			t.Fatal("buffered successor did not complete")
		}
		switch f.Kind {
		case "turn_end":
			ends++
			got = append(got, f.Kind)
		case "delta", "user_message":
			got = append(got, f.Kind+":"+f.Text)
		}
	}
	if want := "[turn_end user_message:queued delta:two delta:! turn_end]"; fmt.Sprint(got) != want {
		t.Fatalf("placement = %v, want %s", got, want)
	}
	for _, want := range []protocol.ThreadItem{user, assistant} {
		got, _ := json.Marshal(r.items[want.ID])
		raw, _ := json.Marshal(want)
		if string(got) != string(raw) {
			t.Fatalf("reported canonical item %d differs from final catch-up", want.ID)
		}
	}
}

func TestQueueRevisionGap(t *testing.T) {
	if kind := os.Getenv("PYRY_QUEUE_GAP_CONTROL"); kind != "" {
		var payload any = protocol.ThreadItemChangedPayload{ConversationID: "conv", ItemID: 1, BaseRev: 2, Rev: 3}
		if kind == protocol.TypeThreadTextAppend {
			payload = protocol.ThreadTextAppendPayload{ConversationID: "conv", ItemID: 1, BaseRev: 2, Rev: 3, Text: "missing base"}
		}
		raw, _ := json.Marshal(payload)
		r := &queuePlacementReader{t: t, conv: "conv", items: map[uint64]protocol.ThreadItem{1: {ID: 1, Rev: 1}}, w: Wire{Next: func(time.Time) protocol.Envelope {
			return protocol.Envelope{Type: kind, Payload: raw}
		}}}
		r.next(time.Now().Add(time.Second))
		t.Fatal("missing base accepted")
	}
	for _, kind := range []string{protocol.TypeThreadItemChanged, protocol.TypeThreadTextAppend} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQueueRevisionGap$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_QUEUE_GAP_CONTROL="+kind)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || err == nil || !strings.Contains(string(out), "revision gap: item=1 have=1 base=2 rev=3") {
				t.Fatalf("gap rejection err=%v output=%s", err, out)
			}
		})
	}
}
