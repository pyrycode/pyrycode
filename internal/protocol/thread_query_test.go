package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func testQueryWindow(items []ThreadItem) ThreadItemsPayload {
	lower, upper, next, older := uint64(11), uint64(97), uint64(11), true
	return ThreadItemsPayload{ConversationID: "conversation", Epoch: "epoch", Version: 89, Items: items, Final: true, LowerOrder: &lower, UpperOrder: &upper, Continuation: &next, OlderExists: &older}
}

func TestThreadQueryVocabulary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind  string
		value any
	}{
		{TypeThreadCatchUp, ThreadCatchUpPayload{"c", "e", 71, -3}},
		{TypeThreadPage, ThreadPagePayload{"c", 59, 257}},
		{TypeThreadItems, testQueryWindow([]ThreadItem{})},
		{TypeThreadReset, ThreadResetPayload{"c", "e", 83}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			raw, _ := json.Marshal(tc.value)
			request := uint64(43)
			wire, _ := json.Marshal(Envelope{Type: tc.kind, Payload: raw, InReplyTo: &request})
			var env Envelope
			if err := json.Unmarshal(wire, &env); err != nil {
				t.Fatal(err)
			}
			decoded := reflect.New(reflect.TypeOf(tc.value))
			if err := json.Unmarshal(env.Payload, decoded.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded.Elem().Interface(), tc.value) {
				t.Fatalf("DTO changed: %#v", decoded.Elem().Interface())
			}
			env.Payload, _ = json.Marshal(decoded.Interface())
			again, _ := json.Marshal(env)
			if !bytes.Equal(wire, again) {
				t.Fatal("decoded DTO did not round trip")
			}
			if !errors.Is(IsKnownAppType(env), ErrUnknownType) {
				t.Fatal("declared v2 type admitted as v1")
			}
		})
	}
	for _, tc := range []struct {
		in, want int
		bad      bool
	}{{0, 50, false}, {1, 1, false}, {256, 256, false}, {257, 256, false}, {-1, 0, true}} {
		got, err := NormalizeThreadQueryLimit(tc.in)
		if got != tc.want || (err != nil) != tc.bad {
			t.Fatalf("limit %d: %d, %v", tc.in, got, err)
		}
	}
	for _, raw := range []string{`{"conversation_id":"c","version":7}`, `{"conversation_id":"c","version":7,"limit":0}`, `{"conversation_id":"c","version":7,"limit":-1}`} {
		var p ThreadCatchUpPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		_, err := NormalizeThreadQueryLimit(p.Limit)
		if (err != nil) != (p.Limit < 0) {
			t.Fatal("nonzero catch-up limit contract")
		}
	}
	for _, raw := range []string{`{"version":-1}`, `{"before_order":-1}`} {
		var err error
		if strings.Contains(raw, "version") {
			var p ThreadCatchUpPayload
			err = json.Unmarshal([]byte(raw), &p)
		} else {
			var p ThreadPagePayload
			err = json.Unmarshal([]byte(raw), &p)
		}
		if err == nil {
			t.Fatal("negative unsigned query bound accepted")
		}
	}
}

func TestThreadQueryReplies(t *testing.T) {
	t.Parallel()
	hostile := "\x00\n\"\\<>&\u2028🌲"
	content, _ := json.Marshal(map[string]any{"unknown": strings.Repeat(hostile, 9000), "clear": nil})
	full := ThreadItem{ID: 1, Kind: "unknown-kind", Order: 23, Rev: 37, EndedOrder: 41, Session: "session", Agent: "agent", NoChild: true, Turn: "turn", Parent: 5, Status: "unknown-status", Active: true, Shown: true, Summary: hostile, Subtype: "subtype", Content: content}
	ordinary := full
	ordinary.ID = 2
	ordinary.Content = json.RawMessage(`{"unknown":[false,0,null]}`)
	mid := ordinary
	mid.ID = 3
	mid.Summary = strings.Repeat("x", 34000)
	many := make([]ThreadItem, 300)
	for i := range many {
		many[i] = ordinary
		many[i].ID = uint64(i + 10)
	}
	from := uint64(53)
	catchup := testQueryWindow(many)
	catchup.FromVersion = &from
	catchup.LowerOrder = nil
	catchup.UpperOrder = nil
	catchup.Continuation = nil
	catchup.OlderExists = nil
	zero := uint64(0)
	no := false
	empty := testQueryWindow(nil)
	empty.LowerOrder = &zero
	empty.UpperOrder = &zero
	empty.Continuation = &zero
	empty.OlderExists = &no
	for _, input := range []ThreadItemsPayload{empty, testQueryWindow([]ThreadItem{ordinary}), testQueryWindow([]ThreadItem{mid, mid, full, ordinary}), catchup} {
		env := testThreadMaxEnvelope()
		request := uint64(43)
		env.InReplyTo = &request
		env.SessionID, _ = json.Marshal(strings.Repeat(hostile, 120))
		env.ConversationID, _ = json.Marshal(strings.Repeat(hostile, 150))
		seq, err := EncodeThreadReply(env, input)
		if err != nil {
			t.Fatal(err)
		}
		again, err := EncodeThreadReply(env, input)
		if err != nil || !reflect.DeepEqual(seq, again) {
			t.Fatal("non-deterministic encoding")
		}
		receiver := testQueryReceiver{request: 43, checkpoint: 7}
		var fragments [][]byte
		for i, b := range seq {
			var e Envelope
			if err := json.Unmarshal(b, &e); err != nil {
				t.Fatal(err)
			}
			stamped, err := json.Marshal(threadStampBudget(e))
			if err != nil || len(stamped) > MaxThreadEnvelopeBytes {
				t.Fatalf("budget: %d, %v", len(stamped), err)
			}
			if e.Type != TypeThreadItems || e.InReplyTo == nil || *e.InReplyTo != 43 || !bytes.Equal(e.SessionID, env.SessionID) || !bytes.Equal(e.ConversationID, env.ConversationID) {
				t.Fatal("lost envelope metadata")
			}
			var keys map[string]json.RawMessage
			_ = json.Unmarshal(e.Payload, &keys)
			if bytes.HasPrefix(keys["continuation"], []byte("{")) {
				fragments = append(fragments, b)
				var p ThreadUpdatePart
				_ = json.Unmarshal(e.Payload, &p)
				if p.Continuation.Final {
					logical := testThreadLogical(t, fragments)
					expected, _ := json.Marshal(ThreadItemsPayload{ConversationID: input.ConversationID, Epoch: input.Epoch, Version: input.Version, Items: []ThreadItem{full}})
					if !bytes.Equal(logical, expected) {
						t.Fatal("fragment bytes differ from logical DTO")
					}
					fragments = nil
				}
			} else {
				var decoded ThreadItemsPayload
				if err := json.Unmarshal(e.Payload, &decoded); err != nil {
					t.Fatal(err)
				}
				e.Payload, _ = json.Marshal(decoded)
				roundtrip, _ := json.Marshal(e)
				if !bytes.Equal(b, roundtrip) {
					t.Fatal("decoded DTO envelope changed")
				}
				final := i == len(seq)-1
				if string(keys["final"]) != map[bool]string{true: "true", false: "false"}[final] {
					t.Fatal("missing explicit final")
				}
				if final {
					if string(keys["items"]) != "[]" {
						t.Fatal("marker must be separate empty array")
					}
					expected := input
					expected.Items = []ThreadItem{}
					if !reflect.DeepEqual(decoded, expected) {
						t.Fatalf("coverage changed: %#v", decoded)
					}
				} else if decoded.FromVersion != nil || decoded.LowerOrder != nil || decoded.Continuation != nil || decoded.OlderExists != nil {
					t.Fatal("batch certified coverage")
				}
			}
			complete := receiver.accept(b)
			if receiver.broken || complete != (i == len(seq)-1) || (i < len(seq)-1 && receiver.checkpoint != 7) {
				t.Fatal("request completion differs from assembly")
			}
		}
		want := input.Items
		if want == nil {
			want = []ThreadItem{}
		}
		if !reflect.DeepEqual(receiver.items, want) || receiver.checkpoint != input.Version {
			t.Fatalf("lost full rows: got %d want %d", len(receiver.items), len(want))
		}
		if len(input.Items) > 2 && len(seq) < 3 {
			t.Fatal("fixture did not batch")
		}
	}
}

// testQueryReceiver stages rows and commits coverage only after validated assembly and a marker.
type testQueryReceiver struct {
	request, checkpoint uint64
	descriptor          *ThreadContinuation
	routing             string
	first               *ThreadUpdatePart
	data                string
	next                uint64
	items               []ThreadItem
	identity            *ThreadItemsPayload
	broken, terminal    bool
}

func (r *testQueryReceiver) accept(b []byte) bool {
	fail := func() bool { r.broken = true; return false }
	if r.broken || r.terminal {
		return fail()
	}
	var e Envelope
	if json.Unmarshal(b, &e) != nil || e.InReplyTo == nil || *e.InReplyTo != r.request {
		return fail()
	}
	if e.Type == TypeThreadReset || e.Type == TypeError {
		r.terminal = true
		r.items = nil
		return false
	}
	if e.Type != TypeThreadItems {
		return fail()
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(e.Payload, &keys) != nil {
		return fail()
	}
	logical := e.Payload
	if bytes.HasPrefix(keys["continuation"], []byte("{")) {
		var p ThreadUpdatePart
		if json.Unmarshal(logical, &p) != nil || p.Continuation == nil {
			return fail()
		}
		c := p.Continuation
		if c.Index != r.next || c.Offset != uint64(len(r.data)) || p.Data == "" {
			return fail()
		}
		meta := p
		meta.Data = ""
		meta.Continuation = nil
		route := string(e.ConversationID) + "\x00" + string(e.SessionID)
		if r.first == nil {
			r.first = &meta
			copy := *c
			r.descriptor = &copy
			r.routing = route
		} else if !reflect.DeepEqual(*r.first, meta) || route != r.routing {
			return fail()
		}
		if c.UpdateID != r.descriptor.UpdateID || c.TotalBytes != r.descriptor.TotalBytes {
			return fail()
		}
		r.data += p.Data
		r.next++
		if uint64(len(r.data)) > c.TotalBytes {
			return fail()
		}
		if !c.Final {
			return false
		}
		sum := sha256.Sum256([]byte(e.Type + "\x00" + r.data))
		if c.TotalBytes != uint64(len(r.data)) || c.UpdateID != hex.EncodeToString(sum[:]) {
			return fail()
		}
		logical = []byte(r.data)
	}
	var p ThreadItemsPayload
	if json.Unmarshal(logical, &p) != nil {
		return fail()
	}
	if r.identity == nil {
		copy := p
		r.identity = &copy
		r.items = []ThreadItem{}
	}
	if p.ConversationID != r.identity.ConversationID || p.Epoch != r.identity.Epoch || p.Version != r.identity.Version {
		return fail()
	}
	if p.Final {
		if r.first != nil || len(p.Items) != 0 {
			return fail()
		}
		r.checkpoint = p.Version
		r.terminal = true
		return true
	}
	if r.first != nil {
		if len(p.Items) != 1 || p.ConversationID != r.first.ConversationID || p.Epoch != r.first.Epoch || p.Version != r.first.Version || p.Items[0].ID != r.first.ItemID || p.Items[0].Rev != r.first.Rev {
			return fail()
		}
	}
	r.items = append(r.items, p.Items...)
	r.first = nil
	r.data = ""
	r.next = 0
	return false
}

func TestThreadQueryAssemblyRejects(t *testing.T) {
	t.Parallel()
	item := ThreadItem{ID: 1, Rev: 2, Content: json.RawMessage(`{}`), Summary: strings.Repeat("x", MaxThreadEnvelopeBytes*3)}
	request := uint64(43)
	seq, err := EncodeThreadReply(Envelope{InReplyTo: &request}, testQueryWindow([]ThreadItem{item}))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"missing", "out of order", "epoch", "version", "item", "digest", "length", "correlation", "routing", "session", "interrupted", "reset", "error"} {
		t.Run(scenario, func(t *testing.T) {
			parts := append([][]byte(nil), seq...)
			switch scenario {
			case "missing":
				parts = append(parts[:1], parts[2:]...)
			case "out of order":
				parts[0], parts[1] = parts[1], parts[0]
			case "interrupted":
				parts = [][]byte{parts[0], parts[len(parts)-1]}
			case "reset", "error":
				var payload any = ThreadResetPayload{"conversation", "epoch", 89}
				if scenario == "error" {
					payload = ErrorPayload{Code: CodeProtocolMalformed, Message: "failed"}
				}
				terminal, err := EncodeThreadReply(Envelope{InReplyTo: &request}, payload)
				if err != nil {
					t.Fatal(err)
				}
				parts = [][]byte{parts[0], terminal[0]}
			default:
				var e Envelope
				_ = json.Unmarshal(parts[1], &e)
				var p ThreadUpdatePart
				_ = json.Unmarshal(e.Payload, &p)
				switch scenario {
				case "epoch":
					p.Epoch = "other"
				case "version":
					p.Version++
				case "item":
					p.ItemID++
				case "digest":
					p.Continuation.UpdateID = strings.Repeat("0", 64)
				case "length":
					p.Continuation.TotalBytes++
				case "correlation":
					other := uint64(44)
					e.InReplyTo = &other
				case "routing":
					e.ConversationID = json.RawMessage(`"other"`)
				case "session":
					e.SessionID = json.RawMessage(`"other"`)
				}
				e.Payload, _ = json.Marshal(p)
				parts[1], _ = json.Marshal(e)
			}
			r := testQueryReceiver{request: 43, checkpoint: 7}
			for _, b := range parts {
				if r.accept(b) {
					t.Fatal("bad sequence certified completion")
				}
			}
			if r.checkpoint != 7 || (!r.broken && !r.terminal) {
				t.Fatal("bad assembly advanced checkpoint")
			}
		})
	}
}

func TestThreadQueryTerminalFailure(t *testing.T) {
	t.Parallel()
	request := uint64(43)
	for _, input := range []any{ThreadResetPayload{"conversation", "epoch", 89}, ErrorPayload{Code: CodeProtocolMalformed, Message: "failed", Retryable: false}} {
		env := testThreadMaxEnvelope()
		env.InReplyTo = &request
		seq, err := EncodeThreadReply(env, input)
		if err != nil || len(seq) != 1 {
			t.Fatalf("terminal: %v", err)
		}
		var e Envelope
		_ = json.Unmarshal(seq[0], &e)
		measured, err := json.Marshal(threadStampBudget(e))
		if err != nil || len(measured) > MaxThreadEnvelopeBytes {
			t.Fatal("terminal envelope exceeds budget")
		}
		if e.InReplyTo == nil || *e.InReplyTo != request {
			t.Fatal("terminal lost correlation")
		}
		b, _ := json.Marshal(e)
		var keys map[string]json.RawMessage
		_ = json.Unmarshal(e.Payload, &keys)
		for _, k := range []string{"items", "final", "from_version", "lower_order", "upper_order", "continuation", "older_exists"} {
			if _, ok := keys[k]; ok {
				t.Fatal("failure carries coverage")
			}
		}
		r := testQueryReceiver{request: 43, checkpoint: 7, items: []ThreadItem{{ID: 1}}}
		if r.accept(b) || !r.terminal || r.checkpoint != 7 {
			t.Fatal("failure certified coverage")
		}
	}
}

func TestThreadQueryErrors(t *testing.T) {
	t.Parallel()
	request := uint64(43)
	base := testQueryWindow([]ThreadItem{{ID: 1, Content: json.RawMessage(`{}`)}})
	for _, scenario := range []string{"json", "utf8", "content utf8", "routing utf8", "routing json", "session json", "correlation", "marker", "coverage", "range", "metadata", "reset metadata", "error metadata", "error utf8"} {
		t.Run(scenario, func(t *testing.T) {
			input := base
			input.Items = append([]ThreadItem(nil), base.Items...)
			env := Envelope{InReplyTo: &request}
			var reply any
			if scenario == "json" || scenario == "utf8" || scenario == "content utf8" {
				input.Items[0].Summary = strings.Repeat("x", 34000)
				input.Items = append(input.Items, input.Items[0])
			}
			switch scenario {
			case "json":
				input.Items = append(input.Items, ThreadItem{Content: json.RawMessage(`{"SECRET"`)})
			case "utf8":
				input.Items = append(input.Items, ThreadItem{Summary: string([]byte{255}), Content: json.RawMessage(`{}`)})
			case "content utf8":
				input.Items = append(input.Items, ThreadItem{Content: json.RawMessage{'"', 255, '"'}})
			case "routing utf8":
				env.ConversationID = json.RawMessage{'"', 255, '"'}
			case "routing json":
				env.ConversationID = json.RawMessage(`{"SECRET"`)
			case "session json":
				env.SessionID = json.RawMessage(`{"SECRET"`)
			case "correlation":
				env.InReplyTo = nil
			case "marker":
				input.Final = false
			case "coverage":
				input.OlderExists = nil
			case "range":
				n := uint64(0)
				input.Continuation = &n
			case "metadata", "reset metadata", "error metadata":
				env.SessionID, _ = json.Marshal(strings.Repeat("SECRET", MaxThreadEnvelopeBytes))
			case "error utf8":
				reply = ErrorPayload{Message: string([]byte{255})}
			}
			if scenario == "reset metadata" {
				reply = ThreadResetPayload{"c", "e", 1}
			}
			if scenario == "error metadata" {
				reply = ErrorPayload{Message: "failed"}
			}
			if reply == nil {
				reply = input
			}
			seq, err := EncodeThreadReply(env, reply)
			if err == nil || seq != nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error exposed prefix/content: %d %v", len(seq), err)
			}
		})
	}
}
