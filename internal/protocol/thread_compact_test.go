package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func testCompactRoundTrip[T any](t *testing.T, raw []byte, want T) T {
	t.Helper()
	var got T
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded: %#v, want %#v", got, want)
	}
	again, err := json.Marshal(got)
	if err != nil || !reflect.DeepEqual(testThreadValue(t, raw), testThreadValue(t, again)) {
		t.Fatalf("typed round trip: %s, %v", again, err)
	}
	return got
}

func TestThreadCompactSelection(t *testing.T) {
	t.Parallel()
	if CapabilityThreadCompact != "thread_compact" || ThreadProjectionCompact != "compact-v1" {
		t.Fatal("compact vocabulary")
	}
	for _, tc := range []struct {
		raw  string
		want ThreadCatchUpPayload
	}{
		{`{"conversation_id":"c","epoch":"","version":0}`, ThreadCatchUpPayload{ConversationID: "c"}},
		{`{"conversation_id":"c","epoch":"","version":0,"projection":"compact-v1"}`, ThreadCatchUpPayload{ConversationID: "c", Projection: ThreadProjectionCompact}},
		{`{"conversation_id":"c","epoch":"e","version":41,"projection":"compact-v1"}`, ThreadCatchUpPayload{ConversationID: "c", Epoch: "e", Version: 41, Projection: ThreadProjectionCompact}},
	} {
		testCompactRoundTrip(t, []byte(tc.raw), tc.want)
	}
	testCompactRoundTrip(t, []byte(`{"conversation_id":"c","before_order":17,"projection":"compact-v1","epoch":"e"}`), ThreadPagePayload{ConversationID: "c", BeforeOrder: 17, Projection: ThreadProjectionCompact, Epoch: "e"})
	// Completed coverage is keyed by all three scope fields. Only its version
	// can be copied into a reconnect request; incompatible scopes initialize at zero.
	for _, scope := range []struct{ conversation, epoch, projection string }{
		{"other", "e", ThreadProjectionCompact}, {"c", "other", ThreadProjectionCompact}, {"c", "e", ""},
	} {
		p := ThreadCatchUpPayload{ConversationID: scope.conversation, Epoch: scope.epoch, Projection: scope.projection}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		got := testCompactRoundTrip(t, raw, p)
		if got.Version != 0 {
			t.Fatal("incompatible view reused checkpoint")
		}
	}
	if !errors.Is(IsKnownAppType(Envelope{Type: TypeThreadCompactUpdate}), ErrUnknownType) {
		t.Fatal("declaration admitted compact frame into v1")
	}
}

func TestThreadCompactHeaders(t *testing.T) {
	t.Parallel()
	source := ThreadItem{ID: 17, Kind: "agent", Order: 19, Rev: 23, EndedOrder: 29, Parent: 31, Session: "s", Agent: "a", Turn: "t", NoChild: true, Status: "failed", Active: false, Shown: true, Subtype: "worker", Content: json.RawMessage(`{"input":"SECRET","output":"SECRET","children":[99]}`)}
	h := NewThreadSummary(source)
	if h.Headline != "Agent" || h.Rev != source.Rev || !h.DetailOmitted {
		t.Fatalf("fallback/source revision: %#v", h)
	}
	for _, known := range []bool{false, true} {
		want := h
		if known {
			zero, no, empty := uint64(0), false, ""
			want.PublicChildCount, want.HasPublicChildren, want.SubtreeVersion, want.ErrorSummary = &zero, &no, &zero, &empty
		}
		raw, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		testCompactRoundTrip(t, raw, want)
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"public_child_count", "has_public_children", "subtree_version", "error_summary"} {
			if _, ok := keys[key]; ok != known {
				t.Fatalf("unknown/zero %s: %s", key, raw)
			}
		}
		for _, forbidden := range []string{"SECRET", `"content"`, `"input"`, `"output"`, `"children"`} {
			if bytes.Contains(raw, []byte(forbidden)) {
				t.Fatalf("header retained detail: %s", raw)
			}
		}
	}
	text := " \n\t" + strings.Repeat("<>&\"\\🌲", 80) + "\r\x00"
	h.Headline, h.ErrorSummary = text, &text
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var bounded ThreadSummary
	if err := json.Unmarshal(raw, &bounded); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{bounded.Headline, *bounded.ErrorSummary} {
		if utf8.RuneCountInString(s) != 200 || !strings.HasSuffix(s, "…") || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) >= 0 {
			t.Fatalf("unbounded/non-single-line text: %q", s)
		}
	}
	if !bytes.Contains(raw, []byte(`\u003c`)) {
		t.Fatal("escaped headline fixture ineffective")
	}
	testCompactRoundTrip(t, raw, bounded)
	// The same facts survive explicitly requested child-summary encoding.
	bounded.Parent = 37
	count, has, freshness := uint64(2), true, uint64(9007199254740991)
	bounded.PublicChildCount, bounded.HasPublicChildren, bounded.SubtreeVersion = &count, &has, &freshness
	request := ThreadExpansionRequest{ConversationID: "conv", Epoch: "epoch", ItemID: 37, ExpectedRev: 43}
	page := ThreadChildrenPayload{ConversationID: "conv", Epoch: "epoch", Version: 59, ItemID: 37, Rev: 43, Children: []ThreadSummary{bounded}}
	seq, err := EncodeThreadChildren(testExpansionEnvelope(), request, page, false)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := testExpansionPage(t, seq)
	if !reflect.DeepEqual(data.Children, page.Children) {
		t.Fatalf("child facts: %#v", data.Children)
	}
}

func TestThreadCompactReplies(t *testing.T) {
	t.Parallel()
	header := NewThreadSummary(ThreadItem{ID: 17, Kind: "tool_call", Rev: 23, Order: 31, Summary: "Read file"})
	content, err := json.Marshal(map[string]string{"text": strings.Repeat("<>&\"\\🌲", 20000)})
	if err != nil {
		t.Fatal(err)
	}
	message := ThreadItem{ID: 41, Kind: "assistant_message", Rev: 43, Order: 47, Content: content}
	for _, mode := range []string{"empty", "mixed", "oversized", "catch-up"} {
		t.Run(mode, func(t *testing.T) {
			p := testQueryWindow(nil)
			p.Projection = ThreadProjectionCompact
			if mode != "empty" {
				p.Headers = []ThreadSummary{header}
				p.Items = []ThreadItem{message}
				if mode != "oversized" {
					p.Items[0].Content = json.RawMessage(`{"text":"complete","unknown":null}`)
				}
			}
			if mode == "catch-up" {
				from := uint64(53)
				p.FromVersion, p.LowerOrder, p.UpperOrder, p.Continuation, p.OlderExists = &from, nil, nil, nil, nil
			}
			seq, err := EncodeThreadReply(testExpansionEnvelope(), p)
			if err != nil {
				t.Fatal(err)
			}
			var items []ThreadItem
			var headers []ThreadSummary
			var assembly strings.Builder
			receiver := testQueryReceiver{request: 37, checkpoint: 7}
			for n, raw := range seq {
				complete := receiver.accept(raw)
				if receiver.broken || complete != (n == len(seq)-1) || (!complete && receiver.checkpoint != 7) {
					t.Fatal("compact coverage confused with assembly/data")
				}
				var env Envelope
				if err := json.Unmarshal(raw, &env); err != nil {
					t.Fatal(err)
				}
				stamped, err := json.Marshal(threadStampBudget(env))
				if err != nil || len(stamped) > MaxThreadEnvelopeBytes || *env.InReplyTo != 37 {
					t.Fatal("stamp budget/correlation")
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(env.Payload, &fields); err != nil {
					t.Fatal(err)
				}
				if string(fields["projection"]) != `"compact-v1"` || string(fields["conversation_id"]) != `"conversation"` || string(fields["epoch"]) != `"epoch"` {
					t.Fatalf("scope: %s", env.Payload)
				}
				if bytes.HasPrefix(fields["continuation"], []byte("{")) {
					var part ThreadUpdatePart
					if err := json.Unmarshal(env.Payload, &part); err != nil {
						t.Fatal(err)
					}
					testCompactRoundTrip(t, env.Payload, part)
					assembly.WriteString(part.Data)
					if !part.Continuation.Final {
						continue
					}
					env.Payload = []byte(assembly.String())
					assembly.Reset()
				}
				var batch ThreadItemsPayload
				if err := json.Unmarshal(env.Payload, &batch); err != nil {
					t.Fatal(err)
				}
				testCompactRoundTrip(t, env.Payload, batch)
				if n == len(seq)-1 {
					want := p
					want.Items, want.Headers = []ThreadItem{}, nil
					if !reflect.DeepEqual(batch, want) {
						t.Fatalf("marker: %#v, want %#v", batch, want)
					}
				} else if batch.Final || batch.FromVersion != nil || batch.LowerOrder != nil {
					t.Fatal("data certified coverage")
				}
				items, headers = append(items, batch.Items...), append(headers, batch.Headers...)
			}
			if !reflect.DeepEqual(items, p.Items) || !reflect.DeepEqual(headers, p.Headers) {
				t.Fatal("message/header loss")
			}
			if mode == "oversized" && len(seq) < 4 {
				t.Fatal("oversized fixture did not fragment")
			}
		})
	}
	reset := ThreadResetPayload{ConversationID: "conversation", Epoch: "replacement", Version: 97, Projection: ThreadProjectionCompact}
	seq, err := EncodeThreadReply(testExpansionEnvelope(), reset)
	if err != nil || len(seq) != 1 {
		t.Fatalf("reset: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(seq[0], &env); err != nil {
		t.Fatal(err)
	}
	testCompactRoundTrip(t, env.Payload, reset)
	// Force an earlier batch to flush before invalid content; no prefix survives.
	message.Content = json.RawMessage(`{"text":"` + strings.Repeat("x", 40000) + `"}`)
	bad := message
	bad.Content = json.RawMessage(`{"broken":`)
	p := testQueryWindow([]ThreadItem{message, message, bad})
	p.Projection = ThreadProjectionCompact
	if seq, err := EncodeThreadReply(testExpansionEnvelope(), p); err == nil || seq != nil {
		t.Fatal("encoding failure returned prefix")
	}
	p.Items, p.Headers = []ThreadItem{message, message}, []ThreadSummary{{Headline: string([]byte{255})}}
	if seq, err := EncodeThreadReply(testExpansionEnvelope(), p); err == nil || seq != nil {
		t.Fatal("invalid header returned prefix")
	}
	for _, scope := range []struct{ projection, conversation, epoch string }{
		{"unknown", "c", "e"}, {ThreadProjectionCompact, "", "e"}, {ThreadProjectionCompact, "c", ""},
	} {
		invalid := testQueryWindow(nil)
		invalid.Projection, invalid.ConversationID, invalid.Epoch = scope.projection, scope.conversation, scope.epoch
		reset := ThreadResetPayload{Projection: scope.projection, ConversationID: scope.conversation, Epoch: scope.epoch}
		for _, reply := range []any{invalid, reset} {
			if seq, err := EncodeThreadReply(testExpansionEnvelope(), reply); err == nil || seq != nil {
				t.Fatal("unscoped compact reply accepted")
			}
		}
	}
	p = testQueryWindow(nil)
	p.Headers = []ThreadSummary{header}
	if seq, err := EncodeThreadReply(testExpansionEnvelope(), p); err == nil || seq != nil {
		t.Fatal("complete view accepted compact headers")
	}
}

func TestThreadCompactCoverageRejects(t *testing.T) {
	t.Parallel()
	p := testQueryWindow([]ThreadItem{{ID: 17, Rev: 23, Content: json.RawMessage(`{}`)}})
	p.Projection = ThreadProjectionCompact
	seq, err := EncodeThreadReply(testExpansionEnvelope(), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"projection", "conversation_id", "epoch"} {
		var env Envelope
		if err := json.Unmarshal(seq[1], &env); err != nil {
			t.Fatal(err)
		}
		var marker map[string]json.RawMessage
		if err := json.Unmarshal(env.Payload, &marker); err != nil {
			t.Fatal(err)
		}
		marker[field] = json.RawMessage(`"other"`)
		env.Payload, err = json.Marshal(marker)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		r := testQueryReceiver{request: 37, checkpoint: 7}
		if r.accept(seq[0]) || r.accept(raw) || !r.broken || r.checkpoint != 7 {
			t.Fatalf("foreign %s certified coverage", field)
		}
	}
}

func TestThreadCompactLive(t *testing.T) {
	t.Parallel()
	header := NewThreadSummary(ThreadItem{ID: 17, Kind: "agent", Rev: 23, Summary: "Worker"})
	for _, raw := range []string{
		`{"projection":"compact-v1","conversation_id":"c","epoch":"e","from_version":31,"version":37,"header":{"id":17,"kind":"agent","order":0,"rev":23,"parent":0,"status":"","active":false,"shown":false,"headline":"Worker","detail_omitted":true},"progress":false}`,
		`{"projection":"compact-v1","conversation_id":"c","epoch":"e","from_version":37,"version":41,"progress":true}`,
	} {
		var p ThreadCompactUpdatePayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		if p.Progress {
			if p.Header != nil || p.Item != nil || p.FromVersion != 37 || p.Version != 41 {
				t.Fatal("hidden progress")
			}
		} else if !reflect.DeepEqual(p.Header, &header) {
			t.Fatalf("header: %#v", p.Header)
		}
		testCompactRoundTrip(t, []byte(raw), p)
		seq, err := EncodeThreadUpdate(testExpansionEnvelope(), p)
		if err != nil || len(seq) != 1 {
			t.Fatalf("live: %v", err)
		}
		var env Envelope
		if err := json.Unmarshal(seq[0], &env); err != nil {
			t.Fatal(err)
		}
		if env.Type != TypeThreadCompactUpdate {
			t.Fatal("compact live type")
		}
		testCompactRoundTrip(t, env.Payload, p)
		testExpansionBudget(t, seq)
	}
	for _, p := range []ThreadCompactUpdatePayload{
		{},
		{Projection: ThreadProjectionCompact, ConversationID: "c", Epoch: "e", FromVersion: 3, Version: 2, Progress: true},
		{Projection: ThreadProjectionCompact, ConversationID: "c", Epoch: "e", Version: 2},
		{Projection: ThreadProjectionCompact, ConversationID: "c", Epoch: "e", Version: 2, Header: &header, Progress: true},
		{Projection: "unknown", ConversationID: "c", Epoch: "e", Version: 2, Progress: true},
		{Projection: ThreadProjectionCompact, Epoch: "e", Version: 2, Progress: true},
		{Projection: ThreadProjectionCompact, ConversationID: "c", Version: 2, Progress: true},
		{Projection: ThreadProjectionCompact, ConversationID: "c", Epoch: "e", Version: 2, Header: &header, Item: &ThreadItem{}},
	} {
		if seq, err := EncodeThreadUpdate(testExpansionEnvelope(), p); err == nil || seq != nil {
			t.Fatal("invalid compact live shape")
		}
	}
	content, err := json.Marshal(map[string]string{"text": strings.Repeat("<>&\"\\🌲", 20000)})
	if err != nil {
		t.Fatal(err)
	}
	p := ThreadCompactUpdatePayload{Projection: ThreadProjectionCompact, ConversationID: "c", Epoch: "e", FromVersion: 41, Version: 47, Item: &ThreadItem{ID: 17, Rev: 23, Kind: "assistant_message", Content: content}}
	seq := testThreadEncode(t, testThreadMaxEnvelope(), p)
	if len(seq) < 2 {
		t.Fatal("large compact live fixture did not fragment")
	}
	testCompactRoundTrip(t, testThreadLogical(t, seq), p)
	for _, raw := range seq {
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		var part ThreadUpdatePart
		if err := json.Unmarshal(env.Payload, &part); err != nil {
			t.Fatal(err)
		}
		if part.Projection != p.Projection || part.ConversationID != p.ConversationID || part.Epoch != p.Epoch {
			t.Fatal("live part lost scope")
		}
	}
}

func TestThreadCompactFreshness(t *testing.T) {
	t.Parallel()
	request := testExpansionRequest()
	before, after := uint64(41), uint64(47)
	root := NewThreadSummary(ThreadItem{ID: request.ItemID, Kind: "agent", Rev: request.ExpectedRev})
	root.SubtreeVersion = &before
	old, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	root.SubtreeVersion = &after
	updated, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	var previous ThreadSummary
	if err := json.Unmarshal(old, &previous); err != nil {
		t.Fatal(err)
	}
	current := testCompactRoundTrip(t, updated, root)
	if current.Rev != previous.Rev || *current.SubtreeVersion == *previous.SubtreeVersion {
		t.Fatal("source revision conflated with subtree freshness")
	}
	cursor := ThreadExpansionCursor{ConversationID: request.ConversationID, Epoch: request.Epoch, ItemID: request.ItemID, Rev: request.ExpectedRev, Version: before}
	if !cursor.Matches(request, before) || cursor.Matches(request, after) {
		t.Fatal("child cursor reused across consumed versions")
	}
	for _, content := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		p := ThreadToolBodyPayload{ConversationID: request.ConversationID, Epoch: request.Epoch, ItemID: request.ItemID, Rev: request.ExpectedRev, Content: content}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		testCompactRoundTrip(t, raw, p)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if _, present := fields["content"]; present != (content != nil) {
			t.Fatal("absent/available empty detail conflated")
		}
	}
}
