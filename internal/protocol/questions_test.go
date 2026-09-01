package protocol

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

// TestQuestionShownPayload_RoundTrip pins the wire shape at all three nesting
// levels against a committed fixture. The bytes are on disk rather than inline
// (#1964) because the user story is a client author mirroring them field for
// field without reading Go, which a literal inside a _test.go file does not
// serve; every other payload in this package is pinned the same way.
//
// The VALUES are hand-authored, and that is the one thing the committed capture
// internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json cannot
// supply: it is one question, two options and an explicit multiSelect:false, so
// it settles none of the three arms this fixture exists to carry — more than one
// question, multi_select true, and a question with more than two options. The
// capture is also behind the e2e_realclaude build tag, which make check never
// compiles. The BYTES are the encoder's own, marshalled through this package, so
// the escaping is not a hand transcription.
//
// The bytes are what pin the wire keys, multi_select's snake-casing included:
// it is an underscore where claude sends a capital S, and nothing else in the
// tree would catch that flipping.
func TestQuestionShownPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "question_shown.json")

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
// One nesting level deeper is also where that reasoning STOPS transferring, so
// the two-level siblings' arithmetic is not copied. Their all-zero entry reaches
// their empty-array key; this shape's does not, because the entry has to carry
// one option to reach label and description. So "options":[] appears in NO
// fixture — decoding [] always yields a non-nil slice, and the only value that
// produces those bytes is a constructed nil, which is what
// TestQuestion_NilOptionsNormalises pins. The three fixtures still reach all
// nine wire keys between them.
//
// The explicit-presence loop runs BEFORE the round trip on purpose: an omitempty
// on header or multi_select would elide the key from both sides and the round
// trip alone would go on passing, which is precisely the regression AC 4 names.
// readFixture is called once, above the loop, so a failure still has raw to
// print — reading per-key would leave the message with no bytes to show.
func TestQuestionShownPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := readFixture(t, "question_shown_zero.json")

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

// TestQuestionShownPayload_Empty_RoundTrip pins the DECODE side of the batch's
// nil-to-[] normalisation, which is the arm #1963 could not reach: its
// TestQuestionShownPayload_NilQuestionsNormalises proves nil marshals to [] on a
// CONSTRUCTED value, and nothing pinned that a frame carrying no questions
// arrives with the key present as [] rather than null or elided.
//
// An empty batch is OUT OF CONTRACT — the documented bounds are 1-4 questions —
// and it is pinned anyway for QuestionShownPayload.MarshalJSON's reason: [] is
// what keeps such a frame decodable by a client whose array type is
// non-optional, where null fails that decode outright. A wire that only ever
// stated the well-formed case would leave that client's behaviour on a producer
// bug undefined.
//
// TestSlashCommandListPayload_Empty_RoundTrip's shape, one nesting level up: an
// empty batch reaches the payload's three keys and none of the nested types'.
func TestQuestionShownPayload_Empty_RoundTrip(t *testing.T) {
	raw := readFixture(t, "question_shown_empty.json")

	if !bytes.Contains(canonical(t, raw), []byte(`"questions":[]`)) {
		t.Errorf("fixture must carry the questions key as an empty array, got: %s", raw)
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
	if payload.ConversationID != "conv-1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conv-1")
	}
	if payload.QuestionBatchID != "qb-0e21" {
		t.Errorf("QuestionBatchID: got %q, want %q", payload.QuestionBatchID, "qb-0e21")
	}
	if payload.Questions == nil {
		t.Errorf("Questions: decoded to nil; an empty array must decode to a non-nil empty slice")
	}
	if len(payload.Questions) != 0 {
		t.Errorf("Questions: got %d entries, want 0", len(payload.Questions))
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

// TestQuestionDismissedPayload_RoundTrip pins the dismissal frame's encoding
// against the committed fixture: the three wire keys, their order, and the
// envelope type they ride under.
//
// The three VALUES are pairwise distinct on purpose, and that is what makes the
// field-reordering mutant detectable at all. roundTripEnvelope compares canonical
// bytes, so a struct whose fields are swapped re-encodes to a different byte
// string only when the values it swaps differ — a fixture reusing one value
// across two keys would let the swap through green. The #1964 lesson generalised:
// pin coverage per key by running the mutant, not by reading the fixture.
//
// The values are otherwise PLACEHOLDERS, the way #1964 records the question_shown
// ids as placeholders. "unanswered" is not a sentinel this slice declares — the
// outcome vocabulary is the producer's (#1973), and nothing may be sized or
// switched on from these bytes.
func TestQuestionDismissedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "question_dismissed.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeQuestionDismissed {
		t.Errorf("Type: got %q, want %q", env.Type, TypeQuestionDismissed)
	}

	var payload QuestionDismissedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.QuestionBatchID != "qb-4c19" {
		t.Errorf("QuestionBatchID: got %q, want %q", payload.QuestionBatchID, "qb-4c19")
	}
	if payload.Outcome != "unanswered" {
		t.Errorf("Outcome: got %q, want %q", payload.Outcome, "unanswered")
	}
	if payload.Source != "timeout" {
		t.Errorf("Source: got %q, want %q", payload.Source, "timeout")
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestQuestionDismissedPayload_ZeroValue_KeysPresent is the omitempty pin, and it
// is a marshalled zero value rather than a second fixture because this payload is
// flat: every key is reachable from one all-zero struct, so the second file
// question_shown needed to reach its nested keys buys nothing here.
//
// It has to exist separately from the round trip above: omitempty elides a key
// only at its zero value, and the fixture's three values are all non-empty, so an
// omitempty added to any of them leaves that test entirely green.
func TestQuestionDismissedPayload_ZeroValue_KeysPresent(t *testing.T) {
	b, err := json.Marshal(QuestionDismissedPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	for _, want := range []string{
		`"question_batch_id":""`,
		`"outcome":""`,
		`"source":""`,
	} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("zero payload must carry %s explicitly, got: %s", want, b)
		}
	}
}

// TestQuestionAnswerPayload_RoundTrip pins the answer frame's encoding at both
// nesting levels against the committed fixture: the payload's three wire keys,
// the entry's two, their order, and the envelope type they ride under.
//
// The fixture is POPULATED, and it has to be: TestQuestionAnswerEntry_ZeroValue_KeysPresent
// below explains why a populated fixture is nonetheless not enough. Two entries
// rather than one, one single-valued and one multi-valued, so the multi-select
// arm claude's contract permits is carried by the bytes a client mirrors rather
// than only by prose.
//
// Every value is PAIRWISE DISTINCT on purpose, QuestionDismissedPayload's
// finding one level up: roundTripEnvelope compares canonical bytes, so a
// field-reordering mutant re-encodes identically whenever the two swapped keys
// share a value, and a fixture reusing one string across two keys lets the swap
// through green.
//
// The ids and the answer text are PLACEHOLDERS. Nothing about a real nonce or a
// real answer may be sized from them, and "Rewrite the parser" is not a value
// this slice declares — values are opaque client-authored strings, checked
// against nothing.
func TestQuestionAnswerPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "question_answer.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeQuestionAnswer {
		t.Errorf("Type: got %q, want %q", env.Type, TypeQuestionAnswer)
	}

	var payload QuestionAnswerPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.QuestionBatchID != "qb-4c19" {
		t.Errorf("QuestionBatchID: got %q, want %q", payload.QuestionBatchID, "qb-4c19")
	}
	if payload.AnswerToken != "at-7f3d" {
		t.Errorf("AnswerToken: got %q, want %q", payload.AnswerToken, "at-7f3d")
	}
	if len(payload.Answers) != 2 {
		t.Fatalf("Answers: got %d entries, want 2", len(payload.Answers))
	}

	if payload.Answers[0].QuestionIndex != 0 {
		t.Errorf("Answers[0].QuestionIndex: got %d, want 0", payload.Answers[0].QuestionIndex)
	}
	if got, want := payload.Answers[0].Values, []string{"Rewrite the parser"}; !slices.Equal(got, want) {
		t.Errorf("Answers[0].Values: got %v, want %v", got, want)
	}
	if payload.Answers[1].QuestionIndex != 1 {
		t.Errorf("Answers[1].QuestionIndex: got %d, want 1", payload.Answers[1].QuestionIndex)
	}
	if got, want := payload.Answers[1].Values, []string{"Add a benchmark", "Add a fuzz target"}; !slices.Equal(got, want) {
		t.Errorf("Answers[1].Values: got %v, want %v", got, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestQuestionAnswerPayload_ZeroValue_KeysPresent is the omitempty pin for the
// payload's three keys, and it must exist separately from the round trip above:
// omitempty elides a key only at its zero value, and every value in the fixture
// is non-empty, so an omitempty added to any of the three leaves that test
// entirely green. QuestionDismissedPayload's pair of tests is the shape being
// copied, and the two stay sole-red for disjoint mutant classes.
//
// It also pins the nil→[] normalisation on the payload's own array, which is the
// half of the []-never-null invariant a decoded fixture cannot reach: "answers":[]
// bytes are produced by marshalling a constructed nil, never by decoding a
// populated frame.
func TestQuestionAnswerPayload_ZeroValue_KeysPresent(t *testing.T) {
	b, err := json.Marshal(QuestionAnswerPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	for _, want := range []string{
		`"question_batch_id":""`,
		`"answer_token":""`,
		`"answers":[]`,
	} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("zero payload must carry %s explicitly, got: %s", want, b)
		}
	}
	if bytes.Contains(b, []byte(`"answers":null`)) {
		t.Errorf("a nil Answers must serialise as [], never null, got: %s", b)
	}
}

// TestQuestionAnswerEntry_ZeroValue_KeysPresent is the same pin one level down,
// and it is the one a populated fixture CANNOT buy no matter how it is authored.
// #1964 recorded the arithmetic one level up and it holds again here: an empty
// answers array reaches none of the entry's keys, so the only route to them is a
// constructed zero value — and "values":[] bytes are likewise produced only by
// marshalling a nil, never by decoding.
//
// question_index is pinned at its zero value on purpose: 0 is a LEGAL index (the
// batch's first question), so an omitempty on it would silently drop the key for
// exactly the answer a client is most likely to send.
func TestQuestionAnswerEntry_ZeroValue_KeysPresent(t *testing.T) {
	b, err := json.Marshal(QuestionAnswerEntry{})
	if err != nil {
		t.Fatalf("marshal zero entry: %v", err)
	}
	for _, want := range []string{
		`"question_index":0`,
		`"values":[]`,
	} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("zero entry must carry %s explicitly, got: %s", want, b)
		}
	}
	if bytes.Contains(b, []byte(`"values":null`)) {
		t.Errorf("a nil Values must serialise as [], never null, got: %s", b)
	}
}

// TestQuestionAnswerPayload_EmptyArraysDecodeNonNil pins the []-never-null
// invariant in the INBOUND direction, which is the direction this frame actually
// travels: a client that encodes [] must find that the daemon decodes it to an
// empty-but-present slice and re-encodes it as [] rather than promoting it to
// null on the way back out.
//
// The bytes are inline rather than a third committed fixture: a zero-answer
// answer frame is OUT OF CONTRACT (a client with nothing to say sends
// question_refused), so publishing it under testdata/ would offer a client author
// a shape to mirror that it must never send.
func TestQuestionAnswerPayload_EmptyArraysDecodeNonNil(t *testing.T) {
	const raw = `{"question_batch_id":"qb-0","answer_token":"at-0","answers":[{"question_index":0,"values":[]}]}`

	var payload QuestionAnswerPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Answers == nil {
		t.Error("Answers: decoding [] must yield an empty non-nil slice, got nil")
	}
	if len(payload.Answers) != 1 {
		t.Fatalf("Answers: got %d entries, want 1", len(payload.Answers))
	}
	if payload.Answers[0].Values == nil {
		t.Error("Answers[0].Values: decoding [] must yield an empty non-nil slice, got nil")
	}

	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if string(out) != raw {
		t.Errorf("re-encoding an empty array must be stable:\n got: %s\nwant: %s", out, raw)
	}
}

// TestQuestionAnswerPayload_MarshalJSON_DoesNotMutateCaller pins the property
// that makes both normalisers safe for a payload shared between goroutines, and
// it covers BOTH receivers because they fail differently.
//
// QuestionShownPayload's test is the shape being copied and its reasoning
// transfers whole: a payload marshaller normalising entries in place would reach
// through p.Answers[i] into the caller's backing array, which is a data race as
// well as a correctness bug, and would not fire at all when an entry is
// marshalled on its own.
func TestQuestionAnswerPayload_MarshalJSON_DoesNotMutateCaller(t *testing.T) {
	e := QuestionAnswerEntry{QuestionIndex: 3}
	p := QuestionAnswerPayload{Answers: []QuestionAnswerEntry{e}}

	t.Run("payload", func(t *testing.T) {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if !bytes.Contains(b, []byte(`"values":[]`)) {
			t.Errorf("a nested nil Values must serialise as [], got: %s", b)
		}
		if p.Answers[0].Values != nil {
			t.Errorf("payload marshalling mutated the caller's backing array: Values is now %v", p.Answers[0].Values)
		}
	})

	t.Run("payload-nil-answers", func(t *testing.T) {
		q := QuestionAnswerPayload{}
		if _, err := json.Marshal(q); err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if q.Answers != nil {
			t.Errorf("MarshalJSON mutated the receiver: Answers is now %v", q.Answers)
		}
	})

	// The entry's own normalisation must not write back through the receiver.
	if _, err := json.Marshal(e); err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if e.Values != nil {
		t.Errorf("MarshalJSON mutated the receiver: Values is now %v", e.Values)
	}
}

// TestQuestionRefusedPayload_RoundTrip pins the refusal frame's encoding against
// the committed fixture: its two wire keys, their order, and the envelope type.
//
// The refusal is a SEPARATE TYPE rather than an answer carrying an empty array or
// a nullable flag, which is what makes this a two-key payload at all — the
// family's own precedent (question_dismissed being its own type rather than a
// reused modal_dismissed) applied to the inbound half. The two values are
// distinct for the reordering-mutant reason the answer frame's test states.
func TestQuestionRefusedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "question_refused.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeQuestionRefused {
		t.Errorf("Type: got %q, want %q", env.Type, TypeQuestionRefused)
	}

	var payload QuestionRefusedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.QuestionBatchID != "qb-91ae" {
		t.Errorf("QuestionBatchID: got %q, want %q", payload.QuestionBatchID, "qb-91ae")
	}
	if payload.AnswerToken != "at-2c60" {
		t.Errorf("AnswerToken: got %q, want %q", payload.AnswerToken, "at-2c60")
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestQuestionRefusedPayload_ZeroValue_KeysPresent is the omitempty pin, a
// marshalled zero value rather than a second fixture because this payload is
// flat — QuestionDismissedPayload's reasoning exactly. The round trip above is
// structurally blind to every omitempty, since both fixture values are non-empty.
func TestQuestionRefusedPayload_ZeroValue_KeysPresent(t *testing.T) {
	b, err := json.Marshal(QuestionRefusedPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	for _, want := range []string{
		`"question_batch_id":""`,
		`"answer_token":""`,
	} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("zero payload must carry %s explicitly, got: %s", want, b)
		}
	}
}
