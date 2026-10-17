package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func testExpansionRequest() ThreadExpansionRequest {
	return ThreadExpansionRequest{ConversationID: "conv", Epoch: "epoch", ItemID: 17, ExpectedRev: 23, Limit: 2}
}

func testExpansionEnvelope() Envelope {
	n := uint64(37)
	return Envelope{InReplyTo: &n, ConversationID: json.RawMessage(`"conv"`), SessionID: json.RawMessage(`"session"`)}
}

func testExpansionBudget(t *testing.T, seq [][]byte) int {
	t.Helper()
	total := 0
	for _, raw := range seq {
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env.InReplyTo == nil || *env.InReplyTo != 37 || string(env.ConversationID) != `"conv"` || string(env.SessionID) != `"session"` {
			t.Fatal("lost envelope scope")
		}
		maxID := uint64(math.MaxUint64)
		env.ID, env.InReplyTo, env.EventID, env.HistoryEntryID = maxID, &maxID, &maxID, &maxID
		env.TS = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -86340))
		env.SessionStateCleared, env.PayloadEncrypted = true, true
		stamped, err := json.Marshal(env)
		if err != nil || len(stamped) > MaxThreadEnvelopeBytes {
			t.Fatalf("envelope budget %d: %v", len(stamped), err)
		}
		total += len(stamped)
	}
	return total
}

func testExpansionPage(t *testing.T, seq [][]byte) (ThreadChildrenPayload, ThreadChildrenPayload) {
	t.Helper()
	var data, marker ThreadChildrenPayload
	for i, raw := range seq {
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env.Type != TypeThreadChildren {
			t.Fatal("wrong child type")
		}
		var p ThreadChildrenPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if i == len(seq)-1 {
			marker = p
		} else {
			data = p
		}
	}
	if !marker.Final || len(marker.Children) != 0 || marker.More == nil {
		t.Fatal("missing separate completion")
	}
	if data.Final || data.More != nil || data.Cursor != nil {
		t.Fatal("data certified completion")
	}
	return data, marker
}

func TestThreadExpansionVocabulary(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"conversation_id":"conv","epoch":"epoch","item_id":17,"expected_rev":23,"limit":2,"cursor":{"conversation_id":"conv","epoch":"epoch","item_id":17,"rev":23,"version":41,"order":0,"id":43}}`)
	var request ThreadExpansionRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	want := testExpansionRequest()
	want.Cursor = &ThreadExpansionCursor{ConversationID: "conv", Epoch: "epoch", ItemID: 17, Rev: 23, Version: 41, Order: 0, ID: 43}
	if !reflect.DeepEqual(request, want) {
		t.Fatalf("request: %#v", request)
	}
	again, err := json.Marshal(request)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("request round trip: %s, %v", again, err)
	}
	for _, typ := range []string{TypeThreadReadToolBody, TypeThreadToolBody, TypeThreadReadChildren, TypeThreadChildren} {
		if !errors.Is(IsKnownAppType(Envelope{Type: typ}), ErrUnknownType) {
			t.Fatal("admitted undeployed type")
		}
	}
	if CapabilityThreadExpansion != "thread_expansion" {
		t.Fatal("capability spelling")
	}
	item := ThreadItem{ID: 43, Kind: "agent", Order: 0, Rev: 47, EndedOrder: 53, Session: "s", Agent: "a", NoChild: true, Turn: "t", Parent: 17, Status: "running", Active: true, Shown: true, Summary: "  line\n\t" + strings.Repeat("🌲", 201), Subtype: "sub", Content: json.RawMessage(`{"secret":"omitted","children":[99]}`)}
	summary := NewThreadSummary(item)
	wantSummary := ThreadSummary{ID: 43, Kind: "agent", Order: 0, Rev: 47, EndedOrder: 53, Session: "s", Agent: "a", NoChild: true, Turn: "t", Parent: 17, Status: "running", Active: true, Shown: true, Headline: "line " + strings.Repeat("🌲", 194) + "…", Subtype: "sub", DetailOmitted: true}
	if !reflect.DeepEqual(summary, wantSummary) || utf8.RuneCountInString(summary.Headline) != 200 {
		t.Fatalf("summary: %#v", summary)
	}
	b, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ThreadSummary
	if err := json.Unmarshal(b, &decoded); err != nil || !reflect.DeepEqual(decoded, wantSummary) {
		t.Fatalf("summary round trip: %#v, %v", decoded, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"content", "children", "descendants"} {
		if _, ok := fields[key]; ok {
			t.Fatal("summary leaked detail")
		}
	}
	if string(fields["detail_omitted"]) != "true" || string(fields["order"]) != "0" {
		t.Fatal("omission/order-zero contract")
	}
}

func TestThreadExpansionPages(t *testing.T) {
	t.Parallel()
	request := testExpansionRequest()
	page := ThreadChildrenPayload{ConversationID: "conv", Epoch: "epoch", Version: 41, ItemID: 17, Rev: 23}
	for i, order := range []uint64{0, 0, 3, 3, 3, 9} {
		page.Children = append(page.Children, NewThreadSummary(ThreadItem{ID: uint64(i + 50), Parent: 17, Order: order, Rev: 7}))
	}
	original, _ := json.Marshal(page)
	var ids []uint64
	for n := 0; n < 3; n++ {
		seq, err := EncodeThreadChildren(testExpansionEnvelope(), request, page, false)
		if err != nil || testExpansionBudget(t, seq) > MaxThreadEnvelopeBytes {
			t.Fatalf("page %d: %v", n, err)
		}
		again, err := EncodeThreadChildren(testExpansionEnvelope(), request, page, false)
		if err != nil || !reflect.DeepEqual(seq, again) {
			t.Fatal("nondeterministic page")
		}
		data, marker := testExpansionPage(t, seq)
		if len(data.Children) != 2 || *marker.More != (n != 2) || data.Version != 41 || marker.Rev != 23 {
			t.Fatalf("page contract: %#v", marker)
		}
		for _, row := range data.Children {
			ids = append(ids, row.ID)
		}
		if n != 2 {
			last := data.Children[1]
			if marker.Cursor == nil || !marker.Cursor.Matches(request, 41) || marker.Cursor.Order != last.Order || marker.Cursor.ID != last.ID {
				t.Fatal("bad continuation")
			}
		} else if marker.Cursor != nil {
			t.Fatal("cursor after enumeration complete")
		}
		if n == 0 {
			after, _ := json.Marshal(page)
			if !bytes.Equal(original, after) {
				t.Fatal("mutated capture")
			}
		}
		request.Cursor = marker.Cursor
		page.Children = page.Children[2:]
	}
	if !reflect.DeepEqual(ids, []uint64{50, 51, 52, 53, 54, 55}) {
		t.Fatalf("gaps or duplicates: %v", ids)
	}
	for _, tc := range []struct {
		limit, count int
		bad          bool
	}{{0, 50, false}, {1, 1, false}, {256, 256, false}, {999, 256, false}, {-1, 0, true}} {
		request = testExpansionRequest()
		request.Limit = tc.limit
		page.Children = nil
		for i := 0; i < 260; i++ {
			page.Children = append(page.Children, NewThreadSummary(ThreadItem{ID: uint64(i + 1), Parent: 17}))
		}
		seq, err := EncodeThreadChildren(testExpansionEnvelope(), request, page, false)
		if tc.bad {
			if err == nil || seq != nil {
				t.Fatal("negative limit accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		data, marker := testExpansionPage(t, seq)
		if len(data.Children) != tc.count || !*marker.More {
			t.Fatalf("limit %d: %d", tc.limit, len(data.Children))
		}
	}
}

func TestThreadExpansionMismatch(t *testing.T) {
	t.Parallel()
	base := testExpansionRequest()
	body := ThreadToolBodyPayload{ConversationID: "conv", Epoch: "epoch", Version: 41, ItemID: 17, Rev: 23, Kind: "tool_call", Content: json.RawMessage(`{}`)}
	for _, change := range []func(*ThreadExpansionRequest){
		func(r *ThreadExpansionRequest) { r.ConversationID = "other" }, func(r *ThreadExpansionRequest) { r.Epoch = "other" }, func(r *ThreadExpansionRequest) { r.ItemID++ }, func(r *ThreadExpansionRequest) { r.ExpectedRev++ },
	} {
		r := base
		change(&r)
		if seq, err := EncodeThreadToolBody(testExpansionEnvelope(), r, body); err == nil || seq != nil {
			t.Fatal("stale first body")
		}
		p := ThreadChildrenPayload{ConversationID: "conv", Epoch: "epoch", Version: 41, ItemID: 17, Rev: 23}
		if seq, err := EncodeThreadChildren(testExpansionEnvelope(), r, p, false); err == nil || seq != nil {
			t.Fatal("stale first children")
		}
	}
	for _, change := range []func(*ThreadExpansionCursor){
		func(c *ThreadExpansionCursor) { c.ConversationID = "other" }, func(c *ThreadExpansionCursor) { c.Epoch = "other" }, func(c *ThreadExpansionCursor) { c.ItemID++ }, func(c *ThreadExpansionCursor) { c.Rev++ }, func(c *ThreadExpansionCursor) { c.Version++ },
	} {
		c := ThreadExpansionCursor{ConversationID: "conv", Epoch: "epoch", ItemID: 17, Rev: 23, Version: 41, Order: 3, ID: 50}
		change(&c)
		r := base
		r.Cursor = &c
		p := ThreadChildrenPayload{ConversationID: "conv", Epoch: "epoch", Version: 41, ItemID: 17, Rev: 23}
		if c.Matches(r, 41) {
			t.Fatal("cursor accepted changed capture")
		}
		if seq, err := EncodeThreadChildren(testExpansionEnvelope(), r, p, false); err == nil || seq != nil {
			t.Fatal("cursor mismatch accepted")
		}
	}
	r := base
	r.Cursor = &ThreadExpansionCursor{ConversationID: "conv", Epoch: "epoch", ItemID: 17, Rev: 23, Version: 41, ID: 50}
	p := ThreadChildrenPayload{ConversationID: "conv", Epoch: "epoch", Version: 41, ItemID: 17, Rev: 23, Children: []ThreadSummary{NewThreadSummary(ThreadItem{ID: 50, Parent: 17})}}
	if seq, err := EncodeThreadChildren(testExpansionEnvelope(), r, p, false); err == nil || seq != nil {
		t.Fatal("inclusive cursor repeated row")
	}
}

func TestThreadExpansionBody(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"tool_call", "agent"} {
		for _, text := range []string{"", strings.Repeat("\x00\n\"\\<>&\u2028🌲", 9000)} {
			content, _ := json.Marshal(map[string]any{"input": text, "output": "", "unknown": []any{false, nil, 7}})
			p := ThreadToolBodyPayload{ConversationID: "conv", Epoch: "epoch", Version: 41, ItemID: 17, Rev: 23, Kind: kind, Content: content}
			seq, err := EncodeThreadToolBody(testExpansionEnvelope(), testExpansionRequest(), p)
			if err != nil {
				t.Fatal(err)
			}
			testExpansionBudget(t, seq)
			var logical []byte
			for _, raw := range seq[:len(seq)-1] {
				var env Envelope
				if err := json.Unmarshal(raw, &env); err != nil {
					t.Fatal(err)
				}
				if env.Type != TypeThreadToolBody {
					t.Fatal("wrong body type")
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(env.Payload, &fields); err != nil {
					t.Fatal(err)
				}
				if fields["continuation"] != nil {
					var part ThreadUpdatePart
					if err := json.Unmarshal(env.Payload, &part); err != nil {
						t.Fatal(err)
					}
					if part.ItemID != 17 || part.Rev != 23 || part.Version != 41 || part.Continuation.Offset != uint64(len(logical)) {
						t.Fatal("fragment binding")
					}
					logical = append(logical, part.Data...)
					if part.Continuation.Final && part.Continuation.TotalBytes != uint64(len(logical)) {
						t.Fatal("assembly length")
					}
				} else {
					logical = env.Payload
				}
			}
			want, _ := json.Marshal(p)
			if !bytes.Equal(logical, want) {
				t.Fatal("body content lost during assembly")
			}
			var decoded ThreadToolBodyPayload
			if err := json.Unmarshal(logical, &decoded); err != nil || !reflect.DeepEqual(decoded, p) {
				t.Fatalf("body round trip: %#v, %v", decoded, err)
			}
			var env Envelope
			if err := json.Unmarshal(seq[len(seq)-1], &env); err != nil {
				t.Fatal(err)
			}
			var marker ThreadToolBodyPayload
			if err := json.Unmarshal(env.Payload, &marker); err != nil {
				t.Fatal(err)
			}
			if !marker.Final || marker.Content != nil || marker.ItemID != 17 || marker.Rev != 23 {
				t.Fatal("body completion contract")
			}
			if text != "" && len(seq) < 3 {
				t.Fatal("oversized body not fragmented")
			}
		}
	}
	for _, tc := range []struct{ kind, content string }{{"tool_call", ""}, {"tool_call", "null"}, {"notice", "{}"}, {"tool_call", "["}} {
		p := ThreadToolBodyPayload{ConversationID: "conv", Epoch: "epoch", Version: 41, ItemID: 17, Rev: 23, Kind: tc.kind, Content: json.RawMessage(tc.content)}
		if seq, err := EncodeThreadToolBody(testExpansionEnvelope(), testExpansionRequest(), p); err == nil || seq != nil {
			t.Fatal("unavailable/wrong-kind body succeeded")
		}
	}
}

func TestThreadExpansionBudgets(t *testing.T) {
	t.Parallel()
	r := testExpansionRequest()
	p := ThreadChildrenPayload{ConversationID: "conv", Epoch: "epoch", Version: 41, ItemID: 17, Rev: 23, Children: []ThreadSummary{NewThreadSummary(ThreadItem{ID: 50, Parent: 17})}}
	seq, err := EncodeThreadChildren(testExpansionEnvelope(), r, p, true)
	if err != nil {
		t.Fatal(err)
	}
	p.Children[0].Session = strings.Repeat("s", MaxThreadEnvelopeBytes-testExpansionBudget(t, seq)-len(`,"session":""`))
	seq, err = EncodeThreadChildren(testExpansionEnvelope(), r, p, true)
	if err != nil || testExpansionBudget(t, seq) != MaxThreadEnvelopeBytes {
		t.Fatalf("exact aggregate budget: %v", err)
	}
	p.Children = append(p.Children, NewThreadSummary(ThreadItem{ID: 51, Parent: 17}))
	seq, err = EncodeThreadChildren(testExpansionEnvelope(), r, p, false)
	if err != nil {
		t.Fatal(err)
	}
	data, marker := testExpansionPage(t, seq)
	if len(data.Children) != 1 || !*marker.More || marker.Cursor.ID != 50 {
		t.Fatal("byte-excluded suffix hidden")
	}
	p.Children[0].Session += "s"
	if seq, err := EncodeThreadChildren(testExpansionEnvelope(), r, p, false); err == nil || seq != nil {
		t.Fatal("no row fits succeeded")
	}
	p.Children = nil
	seq, err = EncodeThreadChildren(testExpansionEnvelope(), r, p, false)
	if err != nil {
		t.Fatal(err)
	}
	_, marker = testExpansionPage(t, seq)
	if *marker.More || len(seq) != 1 {
		t.Fatal("empty enumeration failed")
	}
	if seq, err := EncodeThreadChildren(testExpansionEnvelope(), r, p, true); err == nil || seq != nil {
		t.Fatal("empty prefix with remaining rows completed")
	}
	env := testExpansionEnvelope()
	env.SessionID, _ = json.Marshal(strings.Repeat("x", MaxThreadEnvelopeBytes))
	if seq, err := EncodeThreadChildren(env, r, p, false); err == nil || seq != nil {
		t.Fatal("oversized marker completed")
	}
	p.Children = []ThreadSummary{NewThreadSummary(ThreadItem{ID: 50, Parent: 17}), NewThreadSummary(ThreadItem{ID: 51, Parent: 17})}
	p.Children[0].Session = strings.Repeat("s", 40000)
	p.Children[1].Agent = string([]byte{0xff})
	if seq, err := EncodeThreadChildren(testExpansionEnvelope(), r, p, false); err == nil || seq != nil {
		t.Fatal("invalid later row exposed usable prefix")
	}
	body := ThreadToolBodyPayload{ConversationID: "conv", Epoch: "epoch", Version: 41, ItemID: 17, Rev: 23, Kind: "tool_call", Content: json.RawMessage(`{"input":"","output":""}`)}
	seq, err = EncodeThreadToolBody(testExpansionEnvelope(), r, body)
	if err != nil {
		t.Fatal(err)
	}
	padding := strings.Repeat("x", MaxThreadEnvelopeBytes-testExpansionBudget(t, seq[:1]))
	body.Content = json.RawMessage(`{"input":"` + padding + `","output":""}`)
	seq, err = EncodeThreadToolBody(testExpansionEnvelope(), r, body)
	if err != nil || len(seq) != 2 || testExpansionBudget(t, seq[:1]) != MaxThreadEnvelopeBytes {
		t.Fatalf("exact body budget: %v", err)
	}
	body.Content = json.RawMessage(`{"input":"` + padding + `x","output":""}`)
	seq, err = EncodeThreadToolBody(testExpansionEnvelope(), r, body)
	if err != nil || len(seq) < 3 {
		t.Fatalf("over-budget body did not fragment: %v", err)
	}
	testExpansionBudget(t, seq)
}

func TestThreadExpansionCompletion(t *testing.T) {
	t.Parallel()
	p := ThreadToolBodyPayload{ConversationID: "conv", Epoch: "epoch", Version: 41, ItemID: 17, Rev: 23, Kind: "agent"}
	p.Content, _ = json.Marshal(map[string]string{"input": strings.Repeat("x", 90000), "output": ""})
	seq, err := EncodeThreadToolBody(testExpansionEnvelope(), testExpansionRequest(), p)
	if err != nil {
		t.Fatal(err)
	}
	refusal, err := EncodeThreadReply(testExpansionEnvelope(), ErrorPayload{Code: "thread.query_failed", Message: "thread query could not complete", Retryable: true})
	if err != nil {
		t.Fatal(err)
	}
	for index, delivery := range [][][]byte{seq, seq[:len(seq)-1], seq[:1], seq[1:], append(append([][]byte{}, seq[:len(seq)-1]...), refusal...)} {
		complete, assembled := false, false
		var logical []byte
		for _, raw := range delivery {
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatal(err)
			}
			var part ThreadUpdatePart
			if err := json.Unmarshal(env.Payload, &part); err != nil {
				t.Fatal(err)
			}
			if part.Continuation != nil {
				if part.Continuation.Offset != uint64(len(logical)) {
					break
				}
				logical = append(logical, part.Data...)
				assembled = part.Continuation.Final && part.Continuation.TotalBytes == uint64(len(logical)) && json.Valid(logical)
				continue
			}
			var marker ThreadToolBodyPayload
			if err := json.Unmarshal(env.Payload, &marker); err != nil {
				t.Fatal(err)
			}
			if env.Type == TypeThreadToolBody && marker.Final && assembled {
				complete = true
			}
			var full ThreadItemsPayload
			if err := json.Unmarshal(env.Payload, &full); err != nil {
				t.Fatal(err)
			}
			if threadQueryCoverage(full) || env.EventID != nil || env.HistoryEntryID != nil {
				t.Fatal("expansion certified thread coverage or live progress")
			}
		}
		if complete != (index == 0) {
			t.Fatal("partial/error/cancelled expansion completed")
		}
	}
}
