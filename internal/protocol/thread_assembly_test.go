package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// testThreadReceiver models an atomic receiver; any assembly error requires catch-up.
type testThreadReceiver struct {
	rev, version uint64
	item         map[string]json.RawMessage
	first        *ThreadUpdatePart
	identity     string
	total        uint64
	kind         string
	data         string
	next         uint64
	broken       bool
}

func (r *testThreadReceiver) accept(b []byte) bool {
	if r.broken {
		return false
	}
	fail := func() bool { r.broken = true; r.data = ""; return false }
	var e Envelope
	if json.Unmarshal(b, &e) != nil {
		return fail()
	}
	var p ThreadUpdatePart
	if json.Unmarshal(e.Payload, &p) != nil {
		return fail()
	}
	logical := e.Payload
	if c := p.Continuation; c != nil {
		if c.Index != r.next || c.Offset != uint64(len(r.data)) || p.Data == "" {
			return fail()
		}
		if r.first == nil {
			copy := p
			copy.Data = ""
			copy.Continuation = nil
			r.first = &copy
			r.identity, r.total, r.kind = c.UpdateID, c.TotalBytes, e.Type
		}
		if c.UpdateID != r.identity || c.TotalBytes != r.total || e.Type != r.kind {
			return fail()
		}
		metadata := p
		metadata.Data = ""
		metadata.Continuation = nil
		if !reflect.DeepEqual(*r.first, metadata) {
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
		if uint64(len(r.data)) != c.TotalBytes || c.UpdateID != hex.EncodeToString(sum[:]) {
			return fail()
		}
		logical = []byte(r.data)
	}
	var route struct {
		ConversationID string `json:"conversation_id"`
		Epoch          string
		Version        uint64
		ItemID         uint64  `json:"item_id"`
		BaseRev        *uint64 `json:"base_rev"`
		Rev            uint64
		Item           ThreadItem
	}
	if json.Unmarshal(logical, &route) != nil {
		return fail()
	}
	if e.Type == TypeThreadItemAdded {
		route.ItemID = route.Item.ID
		route.Rev = route.Item.Rev
	}
	if r.first != nil && !reflect.DeepEqual(*r.first, ThreadUpdatePart{ConversationID: route.ConversationID, Epoch: route.Epoch, Version: route.Version, ItemID: route.ItemID, BaseRev: route.BaseRev, Rev: route.Rev}) {
		return fail()
	}
	if route.BaseRev != nil && r.rev != *route.BaseRev {
		return fail()
	}
	// Assemble before touching any field or completed revision/version.
	switch e.Type {
	case TypeThreadItemAdded:
		var a ThreadItemAddedPayload
		_ = json.Unmarshal(logical, &a)
		item, _ := json.Marshal(a.Item)
		_ = json.Unmarshal(item, &r.item)
	case TypeThreadItemChanged:
		var change ThreadItemChangedPayload
		_ = json.Unmarshal(logical, &change)
		for k, v := range change.Changes {
			r.item[k] = v
		}
	case TypeThreadTextAppend:
		var appendText ThreadTextAppendPayload
		_ = json.Unmarshal(logical, &appendText)
		var content map[string]json.RawMessage
		_ = json.Unmarshal(r.item["content"], &content)
		var text string
		_ = json.Unmarshal(content["text"], &text)
		content["text"], _ = json.Marshal(text + appendText.Text)
		r.item["content"], _ = json.Marshal(content)
	default:
		return fail()
	}
	r.rev = route.Rev
	r.version = route.Version
	r.item["rev"], _ = json.Marshal(route.Rev)
	r.first = nil
	r.data = ""
	r.next = 0
	return true
}

func TestThreadUpdateAssembly(t *testing.T) {
	t.Parallel()
	large := strings.Repeat("🌲\x00", MaxThreadEnvelopeBytes)
	item := ThreadItem{ID: 1, Kind: "assistant_message", Rev: 2, Order: 1, Parent: 7, Summary: large, Content: json.RawMessage(`{"text":"start","unknown":[true,null,9007199254740991]}`)}
	raw, _ := json.Marshal(map[string]any{"text": "replacement", "nested": large})
	updates := []any{ThreadItemAddedPayload{"c", "e", 2, item}, ThreadItemChangedPayload{"c", "e", 3, 1, 2, 3, map[string]json.RawMessage{"summary": json.RawMessage(`""`), "content": raw, "active": json.RawMessage(`false`), "session": json.RawMessage(`null`)}}, ThreadTextAppendPayload{"c", "e", 4, 1, 3, 4, large}}
	r := testThreadReceiver{}
	for i, u := range updates {
		parts := testThreadEncode(t, Envelope{}, u)
		oldRev, oldVersion := r.rev, r.version
		for n, b := range parts {
			complete := r.accept(b)
			if complete != (n == len(parts)-1) || r.broken {
				t.Fatal("ordered assembly rejected")
			}
			if !complete && (r.rev != oldRev || r.version != oldVersion) {
				t.Fatal("incomplete update advanced history")
			}
		}
		if r.rev != uint64(i+2) || r.version != uint64(i+2) {
			t.Fatal("wrong completed history")
		}
	}
	var content map[string]json.RawMessage
	_ = json.Unmarshal(r.item["content"], &content)
	var text string
	_ = json.Unmarshal(content["text"], &text)
	if text != "replacement"+large || string(r.item["summary"]) != `""` || string(r.item["session"]) != `null` || string(r.item["kind"]) != `"assistant_message"` || string(r.item["parent"]) != `7` {
		t.Fatal("application lost original fields or patch meanings")
	}
	if !reflect.DeepEqual(testThreadValue(t, content["nested"]), large) {
		t.Fatal("whole content replacement lost unknown value")
	}
}

func TestThreadUpdateAssemblyRejects(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"change", "append"} {
		t.Run(kind, func(t *testing.T) {
			large := strings.Repeat("x", MaxThreadEnvelopeBytes*3)
			var update any = ThreadTextAppendPayload{"c", "e", 3, 1, 2, 3, large}
			if kind == "change" {
				raw, _ := json.Marshal(large)
				update = ThreadItemChangedPayload{"c", "e", 3, 1, 2, 3, map[string]json.RawMessage{"summary": raw}}
			}
			parts := testThreadEncode(t, Envelope{}, update)
			for _, scenario := range []string{"missing middle", "out of order", "wrong base", "epoch", "digest", "replay"} {
				t.Run(scenario, func(t *testing.T) {
					r := testThreadReceiver{rev: 2, version: 2, item: map[string]json.RawMessage{"content": json.RawMessage(`{"text":"before"}`)}}
					seq := append([][]byte(nil), parts...)
					switch scenario {
					case "missing middle":
						seq = append(seq[:1:1], seq[2:]...)
					case "out of order":
						seq[0], seq[1] = seq[1], seq[0]
					case "wrong base":
						r.rev = 1
					case "epoch", "digest":
						var e Envelope
						_ = json.Unmarshal(seq[1], &e)
						var p ThreadUpdatePart
						_ = json.Unmarshal(e.Payload, &p)
						if scenario == "epoch" {
							p.Epoch = "other"
						} else {
							p.Continuation.UpdateID = strings.Repeat("0", 64)
						}
						e.Payload, _ = json.Marshal(p)
						seq[1], _ = json.Marshal(e)
					case "replay":
						for _, b := range parts {
							r.accept(b)
						}
						seq = parts
					}
					before, _ := json.Marshal(r.item)
					rev, version := r.rev, r.version
					for _, b := range seq {
						if r.accept(b) {
							t.Fatal("bad sequence committed")
						}
					}
					after, _ := json.Marshal(r.item)
					if !r.broken || r.rev != rev || r.version != version || string(before) != string(after) {
						t.Fatal("rejected sequence mutated completed state")
					}
					// A fresh catch-up snapshot discards failed assembly before retrying.
					repaired := testThreadReceiver{rev: 2, version: 2, item: map[string]json.RawMessage{"content": json.RawMessage(`{"text":"before"}`)}}
					for _, b := range parts {
						repaired.accept(b)
					}
					if repaired.broken || repaired.rev != 3 || repaired.version != 3 {
						t.Fatal("catch-up did not repair")
					}
				})
			}
		})
	}
}
