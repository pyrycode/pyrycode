package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestQuestionShownPayload_RoundTrip pins the wire shape at all three nesting
// levels against an inline golden. The golden is a literal rather than a fixture
// for two reasons: the testdata fixtures are #1964's, and the committed capture
// this shape mirrors sits behind the e2e_realclaude build tag, which make check
// never compiles. canonical compacts both sides, so the literal may be indented.
//
// The bytes are what pin the wire keys, multi_select's snake-casing included:
// it is an underscore where claude sends a capital S, and nothing else in the
// tree would catch that flipping.
func TestQuestionShownPayload_RoundTrip(t *testing.T) {
	raw := []byte(`{"id":901,"type":"question_shown","ts":"2026-09-01T10:00:00Z","payload":{
		"conversation_id":"conv-1","question_batch_id":"qb-7f3a","questions":[
		{"question":"Which write strategy should the cache use?","header":"Write strategy",
		 "options":[{"label":"Write-through","description":"Writes reach the cache and the store together."},
		            {"label":"Write-behind","description":"Writes reach the cache first, the store later."}],
		 "multi_select":false},
		{"question":"Which eviction policies should it support?","header":"Eviction",
		 "options":[{"label":"LRU","description":"Evict the least recently used entry."},
		            {"label":"LFU","description":"Evict the least frequently used entry."},
		            {"label":"TTL","description":"Evict entries after a fixed time to live."}],
		 "multi_select":true}]}}`)

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeQuestionShown {
		t.Errorf("Type: got %q, want %q", env.Type, TypeQuestionShown)
	}

	var payload QuestionShownPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "conv-1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conv-1")
	}
	if payload.QuestionBatchID != "qb-7f3a" {
		t.Errorf("QuestionBatchID: got %q, want %q", payload.QuestionBatchID, "qb-7f3a")
	}
	if len(payload.Questions) != 2 {
		t.Fatalf("Questions: got %d entries, want 2", len(payload.Questions))
	}

	first := payload.Questions[0]
	if first.Text != "Which write strategy should the cache use?" {
		t.Errorf("Questions[0].Text: got %q", first.Text)
	}
	if first.Header != "Write strategy" {
		t.Errorf("Questions[0].Header: got %q, want %q", first.Header, "Write strategy")
	}
	if first.MultiSelect {
		t.Errorf("Questions[0].MultiSelect: got true, want false")
	}
	if len(first.Options) != 2 {
		t.Fatalf("Questions[0].Options: got %d entries, want 2", len(first.Options))
	}
	if first.Options[0].Label != "Write-through" {
		t.Errorf("Questions[0].Options[0].Label: got %q, want %q", first.Options[0].Label, "Write-through")
	}
	if first.Options[0].Description != "Writes reach the cache and the store together." {
		t.Errorf("Questions[0].Options[0].Description: got %q", first.Options[0].Description)
	}

	second := payload.Questions[1]
	if second.Header != "Eviction" {
		t.Errorf("Questions[1].Header: got %q, want %q", second.Header, "Eviction")
	}
	if !second.MultiSelect {
		t.Errorf("Questions[1].MultiSelect: got false, want true")
	}
	if len(second.Options) != 3 {
		t.Fatalf("Questions[1].Options: got %d entries, want 3", len(second.Options))
	}
	if second.Options[2].Label != "TTL" {
		t.Errorf("Questions[1].Options[2].Label: got %q, want %q", second.Options[2].Label, "TTL")
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestQuestionShownPayload_ZeroValue_RoundTrip pins the seven wire keys an
// omitempty would elide. The batch carries exactly one all-zero Question holding
// one all-zero QuestionOption because that single entry is the only route to the
// nested types' keys at all — a batch with no questions reaches none of them.
// TestModelListPayload_ZeroValue_RoundTrip's reasoning, one nesting level deeper.
//
// The explicit-presence loop runs BEFORE the round trip on purpose: an omitempty
// on header or multi_select would elide the key from both sides and the round
// trip alone would go on passing, which is precisely the regression AC 4 names.
func TestQuestionShownPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := []byte(`{"id":902,"type":"question_shown","ts":"2026-09-01T10:00:01Z","payload":{
		"conversation_id":"","question_batch_id":"","questions":[
		{"question":"","header":"","options":[{"label":"","description":""}],"multi_select":false}]}}`)

	for _, want := range []string{
		`"conversation_id":""`,
		`"question_batch_id":""`,
		`"question":""`,
		`"header":""`,
		`"label":""`,
		`"description":""`,
		`"multi_select":false`,
	} {
		if !bytes.Contains(canonical(t, raw), []byte(want)) {
			t.Errorf("golden must carry %s explicitly at its zero value, got: %s", want, raw)
		}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeQuestionShown {
		t.Errorf("Type: got %q, want %q", env.Type, TypeQuestionShown)
	}

	var payload QuestionShownPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "" || payload.QuestionBatchID != "" {
		t.Errorf("ids: got %q / %q, want both empty", payload.ConversationID, payload.QuestionBatchID)
	}
	if len(payload.Questions) != 1 {
		t.Fatalf("Questions: got %d entries, want 1", len(payload.Questions))
	}

	q := payload.Questions[0]
	if q.Text != "" || q.Header != "" {
		t.Errorf("entry strings: got %q / %q, want both empty", q.Text, q.Header)
	}
	if q.MultiSelect {
		t.Errorf("MultiSelect: got true, want false")
	}
	if len(q.Options) != 1 {
		t.Fatalf("Options: got %d entries, want 1", len(q.Options))
	}
	if q.Options[0].Label != "" || q.Options[0].Description != "" {
		t.Errorf("option strings: got %q / %q, want both empty", q.Options[0].Label, q.Options[0].Description)
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestQuestionShownPayload_NilQuestionsNormalises(t *testing.T) {
	p := QuestionShownPayload{ConversationID: "c1", QuestionBatchID: "qb-1"}
	if p.Questions != nil {
		t.Fatalf("precondition: Questions must be nil, got %v", p.Questions)
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", p},
		{"pointer", &p},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if bytes.Contains(out, []byte(`"questions":null`)) {
				t.Errorf("nil Questions marshalled to null: %s", out)
			}
			if !bytes.Contains(out, []byte(`"questions":[]`)) {
				t.Errorf("nil Questions did not normalise to []: %s", out)
			}
		})
	}

	// The normalisation must not mutate the receiver's copy back into the caller.
	if p.Questions != nil {
		t.Errorf("MarshalJSON mutated the receiver: Questions is now %v", p.Questions)
	}
}

// TestQuestion_NilOptionsNormalises pins the entry's normalisation at the one
// value no golden can reach: decoding [] always yields a non-nil slice, so only a
// constructed value is nil here.
//
// The nested-in-payload subtest proves the entry marshaller fires THROUGH the
// payload's, and its trailing assertion is the only thing in this file that would
// catch a payload marshaller normalising entries in place — that mutant reaches
// through p.Questions[i] into the caller's backing array, and its JSON is
// byte-identical to the correct implementation's, so no byte check could tell the
// two apart. Verified by running that mutant, not predicted: with the in-place
// loop added to QuestionShownPayload.MarshalJSON, this subtest's trailing
// assertion is the sole failure in the package.
//
// This is TestSlashCommand_NilSliceEncodings's shape, shared assertEncodings
// helper included, so the two assertions are stated once.
func TestQuestion_NilOptionsNormalises(t *testing.T) {
	q := Question{Text: "Which write strategy?", Header: "Write strategy"}
	if q.Options != nil {
		t.Fatalf("precondition: Options must be nil, got %v", q.Options)
	}

	assertEncodings := func(t *testing.T, out []byte) {
		t.Helper()
		if bytes.Contains(out, []byte(`"options":null`)) {
			t.Errorf("nil Options marshalled to null: %s", out)
		}
		if !bytes.Contains(out, []byte(`"options":[]`)) {
			t.Errorf("nil Options did not normalise to []: %s", out)
		}
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", q},
		{"pointer", &q},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			assertEncodings(t, out)
		})
	}

	t.Run("nested in payload", func(t *testing.T) {
		p := QuestionShownPayload{ConversationID: "c1", Questions: []Question{q}}
		out, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		assertEncodings(t, out)
		if p.Questions[0].Options != nil {
			t.Errorf("payload marshalling mutated the caller's backing array: Options is now %v", p.Questions[0].Options)
		}
	})

	// The entry's own normalisation must not write back through the receiver.
	if q.Options != nil {
		t.Errorf("MarshalJSON mutated the receiver: Options is now %v", q.Options)
	}
}
