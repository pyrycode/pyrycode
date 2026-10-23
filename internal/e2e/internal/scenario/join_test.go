package scenario

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestJoinPreservesInterleavedSnapshots(t *testing.T) {
	var id uint64
	var input, restored []protocol.Envelope
	reading := func(kind string, clear bool) protocol.Envelope {
		return protocol.Envelope{Type: kind, SessionStateCleared: clear, Payload: json.RawMessage(`{}`)}
	}
	want := []protocol.Envelope{reading(protocol.TypeModelList, true), reading(protocol.TypeModelList, false), reading(protocol.TypeModalShown, false), reading(protocol.TypeReplySuggestion, false)}
	w := Wire{Send: func(kind string, payload any) uint64 {
		id++
		reply := id
		if kind == protocol.TypeListConversations {
			input = append(input, want[:2]...)
			input = append(input, protocol.Envelope{Type: protocol.TypeConversations, InReplyTo: &reply, Payload: json.RawMessage(`{"conversations":[{"id":"conv"}]}`)})
		} else {
			input = append(input, want[2], protocol.Envelope{Type: protocol.TypeThreadItems, InReplyTo: &reply, Payload: json.RawMessage(`{"conversation_id":"conv","epoch":"epoch","final":false}`)}, want[3], protocol.Envelope{Type: protocol.TypeThreadItems, InReplyTo: &reply, Payload: json.RawMessage(`{"conversation_id":"conv","epoch":"epoch","final":true}`)})
		}
		return id
	}, Next: func(time.Time) protocol.Envelope {
		if len(input) == 0 {
			t.Fatal("read beyond query completion")
		}
		e := input[0]
		input = input[1:]
		return e
	}}
	JoinConversations(t, w, func(envs []protocol.Envelope) { restored = envs })
	if id != 2 || len(input) != 0 || !reflect.DeepEqual(restored, want) {
		t.Fatalf("requests=%d unread=%d saved=%+v", id, len(input), restored)
	}
}

func TestJoinFragmentedCatchUp(t *testing.T) {
	var frames []protocol.Envelope
	var saved []protocol.Envelope
	w := Wire{Send: func(kind string, payload any) uint64 {
		request := uint64(9)
		lower, upper, older := uint64(0), uint64(2), false
		content, err := json.Marshal(map[string]string{"text": strings.Repeat("<🌲>", protocol.MaxThreadEnvelopeBytes)})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := protocol.EncodeThreadReply(protocol.Envelope{InReplyTo: &request}, protocol.ThreadItemsPayload{
			ConversationID: "conv", Epoch: "epoch", Version: 1, Final: true, LowerOrder: &lower, UpperOrder: &upper, Continuation: &lower, OlderExists: &older,
			Items: []protocol.ThreadItem{{ID: 1, Rev: 1, Kind: "assistant_message", Content: content}},
		})
		if err != nil || len(raw) < 3 {
			t.Fatalf("fragment fixture: frames=%d error=%v", len(raw), err)
		}
		for i, bytes := range raw {
			var env protocol.Envelope
			if err := json.Unmarshal(bytes, &env); err != nil {
				t.Fatal(err)
			}
			frames = append(frames, env)
			if i == 0 {
				frames = append(frames, protocol.Envelope{Type: protocol.TypeModalShown, Payload: json.RawMessage(`{"modal_id":"retained"}`)})
			}
		}
		return request
	}, Next: func(time.Time) protocol.Envelope {
		if len(frames) == 0 {
			t.Fatal("read past separate coverage marker")
		}
		e := frames[0]
		frames = frames[1:]
		return e
	}}
	JoinConversations(t, w, func(envs []protocol.Envelope) { saved = envs }, "conv")
	if len(frames) != 0 || len(saved) != 1 || saved[0].Type != protocol.TypeModalShown {
		t.Fatal("fragment assembly lost interleaved prompt or coverage")
	}
}

func TestJoinRetriesUnavailableQuery(t *testing.T) {
	var request uint64
	var reply protocol.Envelope
	JoinConversations(t, Wire{Send: func(string, any) uint64 {
		request++
		id := request
		reply = protocol.Envelope{InReplyTo: &id, Type: protocol.TypeError, Payload: json.RawMessage(`{"code":"thread.query_failed","retryable":true}`)}
		if request == 2 {
			reply.Type = protocol.TypeThreadItems
			reply.Payload = json.RawMessage(`{"conversation_id":"conv","epoch":"epoch","final":true}`)
		}
		return id
	}, Next: func(time.Time) protocol.Envelope { return reply }}, func(saved []protocol.Envelope) {
		if len(saved) != 0 {
			t.Fatal("query replies leaked to live reader")
		}
	}, "conv")
	if request != 2 {
		t.Fatalf("queries=%d want 2", request)
	}
}
