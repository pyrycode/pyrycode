package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSendMessagePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "send_message.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSendMessage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSendMessage)
	}

	var payload SendMessagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.MessageID != "m9" {
		t.Errorf("MessageID: got %q, want %q", payload.MessageID, "m9")
	}
	if !strings.HasPrefix(payload.Text, "what's the weather") {
		t.Errorf("Text: got %q, want prefix %q", payload.Text, "what's the weather")
	}
	// The absent-key half of the attachment_ids contract (#2036). This fixture
	// keeps its original three-key form deliberately: send_message is a
	// v1-compatible inbound type, so a client predating the field sends exactly
	// these three keys and always will, and that is the regression case. The
	// paired populated fixture is send_message_attachments.json — the
	// question_shown.json / question_shown_empty.json convention.
	//
	// The nil assertion is this test's whole contribution to the field, and the
	// round trip below adds NOTHING to it: that comparison re-marshals env while
	// env.Payload is still the fixture's own raw bytes, so the payload struct's
	// tags never run and an omitempty mutant leaves it green (measured, not
	// assumed). TestSendMessagePayload_ZeroValue_KeyAbsent is the only omitempty
	// pin in this file — do not weaken it on the belief that this test overlaps it.
	if payload.AttachmentIDs != nil {
		t.Errorf("AttachmentIDs: got %v, want nil — an absent key is the no-attachments case", payload.AttachmentIDs)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

// TestSendMessagePayload_Attachments_RoundTrip pins the POPULATED form: the wire
// key attachment_ids, its element order, and the canonical lowercase-UUIDv4
// element shape § Attachments publishes.
//
// A committed fixture is the only thing that can catch a wire-STRING typo here.
// The package's three compat_test.go registries all key on the Go symbol, so
// renaming the json tag to "attachment_id_list" moves consistently through every
// one of them and reddens none — the property #1895's mutant run established for
// TypeAttachmentStored, applied to a field tag instead of a type constant.
func TestSendMessagePayload_Attachments_RoundTrip(t *testing.T) {
	raw := readFixture(t, "send_message_attachments.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSendMessage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSendMessage)
	}

	var payload SendMessagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	want := []string{
		"3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d4a67",
		"8c1d5e92-4a03-4b7f-9e21-6d0fa3b85c14",
	}
	if len(payload.AttachmentIDs) != len(want) {
		t.Fatalf("AttachmentIDs: got %d ids %v, want %d", len(payload.AttachmentIDs), payload.AttachmentIDs, len(want))
	}
	// Index-by-index rather than a set comparison: the array order is the
	// client's own presentation order and a decoder must preserve it.
	for i, id := range want {
		if payload.AttachmentIDs[i] != id {
			t.Errorf("AttachmentIDs[%d]: got %q, want %q", i, payload.AttachmentIDs[i], id)
		}
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestSendMessagePayload_WireKeys pins the payload's COMPLETE key set on a
// populated value, so a fourth key cannot be added and a fifth cannot appear
// unnoticed. TestAttachmentStoredPayload_WireKeys records why this outlives the
// round trips above: an added field reddens those too, but only until somebody
// regenerates the fixture, and regenerating under the same mutant turns them
// green again while this stays red.
//
// Two-sided on purpose — every expected key present AND no unexpected key —
// since a one-sided containment check is exactly what lets an added field
// through.
//
// It deliberately does NOT cover the empty case: attachment_ids carries
// omitempty, so a populated value reaches the key whatever the empty-case
// posture is. That is TestSendMessagePayload_ZeroValue_KeyAbsent's job, and the
// division is the same one TestAttachmentStoredPayload_ZeroValue_KeysPresent
// exists for, read in the opposite direction.
func TestSendMessagePayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(SendMessagePayload{
		ConversationID: "c1",
		MessageID:      "m10",
		Text:           "here are the two files from this morning",
		AttachmentIDs:  []string{"3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d4a67"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	want := map[string]bool{"conversation_id": true, "message_id": true, "text": true, "attachment_ids": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected wire key %q: the payload's key set is fixed at %v", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s", k, b)
		}
	}
}

// TestSendMessagePayload_ZeroValue_KeyAbsent is the empty-case WIRE FORM pin:
// a message naming no attachments carries no attachment_ids key at all.
//
// This is the inverse of TestAttachmentStoredPayload_ZeroValue_KeysPresent, and
// it exists for that test's reason read the other way. omitempty changes a key's
// presence only at the zero value, so the two round trips above and the key-set
// check all marshal a non-empty list and stay entirely green if the omitempty is
// dropped. Only a zero value reaches the question.
//
// The other three keys are asserted PRESENT in the same breath, so the test
// cannot go green by marshalling nothing at all — they carry no omitempty and a
// zero-valued send_message is still three keys on the wire.
func TestSendMessagePayload_ZeroValue_KeyAbsent(t *testing.T) {
	b, err := json.Marshal(SendMessagePayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	if _, ok := got["attachment_ids"]; ok {
		t.Errorf("zero payload must omit attachment_ids entirely (a client naming no attachments sends no such key), got: %s", b)
	}
	for _, k := range []string{"conversation_id", "message_id", "text"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q: the other three carry no omitempty, got: %s", k, b)
		}
	}
}

// TestSendMessagePayload_EmptyForms_Indistinguishable is the AC that the three
// empty wire forms — key absent, null, and [] — are one case a consumer cannot
// branch on.
//
// The nil assertion is the whole test. len(x) == 0 already agrees across all
// three with a plain []string, so a len-only check would pass against no
// implementation at all; x == nil is where they disagree, because encoding/json
// decodes [] to an EMPTY NON-NIL slice while an absent key and null both leave
// nil. SendMessagePayload.UnmarshalJSON is what collapses the third, and
// dropping it reddens exactly the "[]" row here while the other two stay green —
// so the failure names which form regressed.
func TestSendMessagePayload_EmptyForms_Indistinguishable(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"key absent", `{"conversation_id":"c1","message_id":"m9","text":"hi"}`},
		{"null", `{"conversation_id":"c1","message_id":"m9","text":"hi","attachment_ids":null}`},
		{"empty array", `{"conversation_id":"c1","message_id":"m9","text":"hi","attachment_ids":[]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p SendMessagePayload
			if err := json.Unmarshal([]byte(tc.payload), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if p.AttachmentIDs != nil {
				t.Errorf("AttachmentIDs: got %#v, want nil — all three empty forms must decode to one value a consumer cannot tell apart", p.AttachmentIDs)
			}
			if len(p.AttachmentIDs) != 0 {
				t.Errorf("AttachmentIDs: got %d ids, want 0", len(p.AttachmentIDs))
			}
			// The sibling fields must survive the custom decode path — an
			// UnmarshalJSON that decodes only its own field would drop them.
			if p.ConversationID != "c1" || p.MessageID != "m9" || p.Text != "hi" {
				t.Errorf("sibling fields lost in the custom decode: %+v", p)
			}
		})
	}
}

// TestSendMessagePayload_AttachmentIDsIllTyped_Rejected pins the decode-FAILURE
// branch: an attachment_ids that is not an array of strings is a rejected frame,
// never a silently-empty list.
//
// Without it, an UnmarshalJSON that swallowed its error would read "this message
// names no attachments" off garbage input, and every other test in this file
// would stay green. The one production decode site,
// internal/relay/handlers/send_message.go's SendMessage, turns the returned
// error into a protocol.malformed reply — the reading QuestionAnswerPayload's
// doc block publishes for its own decoder.
func TestSendMessagePayload_AttachmentIDsIllTyped_Rejected(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"string", `{"conversation_id":"c1","attachment_ids":"3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d4a67"}`},
		{"object", `{"conversation_id":"c1","attachment_ids":{"id":"3f2a1c40"}}`},
		{"number", `{"conversation_id":"c1","attachment_ids":7}`},
		{"array of numbers", `{"conversation_id":"c1","attachment_ids":[7]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p SendMessagePayload
			err := json.Unmarshal([]byte(tc.payload), &p)
			if err == nil {
				t.Fatalf("decode must fail on an ill-typed attachment_ids, got payload %+v", p)
			}
			// The error is logged by the handler, so it must not carry the
			// remote-authored bytes back into a line-oriented log.
			if strings.Contains(err.Error(), tc.payload) {
				t.Errorf("decode error must not embed the raw payload, got: %v", err)
			}
		})
	}
}

// TestSendMessagePayload_MaxAttachmentIDs_FitV2EnvelopeCap measures rather than
// argues MaxAttachmentIDsPerMessage's headroom claim: a send_message naming the
// bound's worth of canonical ids must leave the envelope overwhelmingly free for
// text, so a client never trades attachments against message length.
//
// The shape and the statement are TestAttachmentChunkPayload_FitV2EnvelopeCap's:
// a per-element bound does not compose into an envelope guarantee on its own.
// The assertion is two-sided — under the cap, and under a tenth of it — because
// "fits" alone would still pass at a bound of 1000 ids, which is the mutant this
// test is for. If it ever fails, LOWER the constant.
func TestSendMessagePayload_MaxAttachmentIDs_FitV2EnvelopeCap(t *testing.T) {
	ids := make([]string, MaxAttachmentIDsPerMessage)
	for i := range ids {
		// Canonical shape, distinct per element; the alphabet is lowercase hex,
		// so no element escapes and every one costs exactly 36 bytes.
		ids[i] = fmt.Sprintf("3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d%04x", i)
	}
	ts := time.Date(2026, 9, 2, 11, 4, 22, 317000000, time.UTC)
	payloadBytes, err := json.Marshal(SendMessagePayload{
		ConversationID: "3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d4a67",
		MessageID:      "8c1d5e92-4a03-4b7f-9e21-6d0fa3b85c14",
		AttachmentIDs:  ids,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	out, err := json.Marshal(Envelope{ID: 8, Type: TypeSendMessage, TS: ts, Payload: payloadBytes})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	t.Logf("send_message naming %d ids: %d B (%.1f%% of the %d B cap)",
		MaxAttachmentIDsPerMessage, len(out), float64(len(out))/float64(maxV2AppEnvelope)*100, maxV2AppEnvelope)
	if len(out) >= maxV2AppEnvelope {
		t.Errorf("serialised envelope: got %d B, want < %d B", len(out), maxV2AppEnvelope)
	}
	if len(out) >= maxV2AppEnvelope/10 {
		t.Errorf("serialised envelope: got %d B, want < %d B — the bound must leave the envelope free for text, not merely fit inside it",
			len(out), maxV2AppEnvelope/10)
	}
}

func TestMessagePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "message.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeMessage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeMessage)
	}

	var payload MessagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.MessageID != "m12" {
		t.Errorf("MessageID: got %q, want %q", payload.MessageID, "m12")
	}
	if payload.Role != "assistant" {
		t.Errorf("Role: got %q, want %q", payload.Role, "assistant")
	}
	if payload.Text == "" {
		t.Errorf("Text: got empty, want non-empty")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestSessionTransitionPayload_RoundTrip(t *testing.T) {
	cwd := "/home/user/project"
	cases := []struct {
		name         string
		fixture      string
		wantConvID   string
		wantPrevID   string
		wantNewID    string
		wantReason   string
		wantOccurred time.Time
		wantCwd      *string // nil ⇒ expect WorkspaceCwd == nil (literal JSON null)
	}{
		{
			name:         "cwd-unset",
			fixture:      "session_transition.json",
			wantConvID:   "", // producer emits "" until #741 binds it
			wantPrevID:   "sess-a",
			wantNewID:    "sess-b",
			wantReason:   "idle_evict",
			wantOccurred: time.Date(2026, 6, 9, 10, 33, 14, 500000000, time.UTC),
			wantCwd:      nil,
		},
		{
			name:         "cwd-set",
			fixture:      "session_transition_workspace.json",
			wantConvID:   "", // producer emits "" until #741 binds it
			wantPrevID:   "sess-b",
			wantNewID:    "sess-c",
			wantReason:   "workspace_change",
			wantOccurred: time.Date(2026, 6, 9, 11, 0, 0, 0, time.UTC),
			wantCwd:      &cwd,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := readFixture(t, tc.fixture)

			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if env.Type != TypeSessionTransition {
				t.Errorf("Type: got %q, want %q", env.Type, TypeSessionTransition)
			}

			var payload SessionTransitionPayload
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if payload.ConversationID != tc.wantConvID {
				t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, tc.wantConvID)
			}
			if payload.PreviousSessionID != tc.wantPrevID {
				t.Errorf("PreviousSessionID: got %q, want %q", payload.PreviousSessionID, tc.wantPrevID)
			}
			if payload.NewSessionID != tc.wantNewID {
				t.Errorf("NewSessionID: got %q, want %q", payload.NewSessionID, tc.wantNewID)
			}
			if payload.Reason != tc.wantReason {
				t.Errorf("Reason: got %q, want %q", payload.Reason, tc.wantReason)
			}
			// .Equal (never == / reflect.DeepEqual): RFC3339Nano strips the
			// monotonic clock on marshal, so the wall clocks compare equal but
			// the structs do not.
			if !payload.OccurredAt.Equal(tc.wantOccurred) {
				t.Errorf("OccurredAt: got %v, want %v", payload.OccurredAt, tc.wantOccurred)
			}
			switch {
			case tc.wantCwd == nil && payload.WorkspaceCwd != nil:
				t.Errorf("WorkspaceCwd: got %q, want nil (literal null for non-workspace_change)", *payload.WorkspaceCwd)
			case tc.wantCwd != nil && payload.WorkspaceCwd == nil:
				t.Errorf("WorkspaceCwd: got nil, want %q", *tc.wantCwd)
			case tc.wantCwd != nil && *payload.WorkspaceCwd != *tc.wantCwd:
				t.Errorf("WorkspaceCwd: got %q, want %q", *payload.WorkspaceCwd, *tc.wantCwd)
			}

			out, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Byte-equal check is the regression detector for the
			// *string-without-omitempty design: if a future contributor adds
			// omitempty, the "workspace_cwd":null key disappears from the
			// cwd-unset output and this fails (the same *string-without-omitempty
			// regression check).
			if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
				t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
			}
		})
	}
}

func TestDebugBundleChunkPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "debug_bundle_chunk.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeDebugBundleChunk {
		t.Errorf("Type: got %q, want %q", env.Type, TypeDebugBundleChunk)
	}

	var payload DebugBundleChunkPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Seq != 0 {
		t.Errorf("Seq: got %d, want 0", payload.Seq)
	}
	if got, want := string(payload.Data), "hello, bundle"; got != want {
		t.Errorf("Data: got %q, want %q", got, want)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestDebugBundleDonePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "debug_bundle_done.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeDebugBundleDone {
		t.Errorf("Type: got %q, want %q", env.Type, TypeDebugBundleDone)
	}

	var payload DebugBundleDonePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Total != 4 {
		t.Errorf("Total: got %d, want 4", payload.Total)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestModalShownPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "modal_shown.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModalShown {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModalShown)
	}

	var payload ModalShownPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// ConversationID is the outbound scoping key (#1065): the golden pins it as
	// the first field so json.Compact byte-equality also guards its placement.
	if payload.ConversationID != "conv-7f3a" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conv-7f3a")
	}
	if payload.ModalID != "mdl-7f3a" {
		t.Errorf("ModalID: got %q, want %q", payload.ModalID, "mdl-7f3a")
	}
	if payload.Class != "permission" {
		t.Errorf("Class: got %q, want %q", payload.Class, "permission")
	}
	if payload.Title != "Allow Bash?" {
		t.Errorf("Title: got %q, want %q", payload.Title, "Allow Bash?")
	}
	if payload.Prompt != "claude wants to run: rm -rf build/" {
		t.Errorf("Prompt: got %q, want %q", payload.Prompt, "claude wants to run: rm -rf build/")
	}
	// Array order IS option order: assert both length and the positional ids.
	if len(payload.Options) != 2 {
		t.Fatalf("Options: got len %d, want 2", len(payload.Options))
	}
	if payload.Options[0].ID != "allow" || payload.Options[0].Label != "Allow" {
		t.Errorf("Options[0]: got %+v, want {allow Allow}", payload.Options[0])
	}
	if payload.Options[1].ID != "deny" || payload.Options[1].Label != "Deny" {
		t.Errorf("Options[1]: got %+v, want {deny Deny}", payload.Options[1])
	}
	if payload.DefaultOptionID != "deny" {
		t.Errorf("DefaultOptionID: got %q, want %q", payload.DefaultOptionID, "deny")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestModalAnswerPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "modal_answer.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModalAnswer {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModalAnswer)
	}

	var payload ModalAnswerPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ModalID != "mdl-7f3a" {
		t.Errorf("ModalID: got %q, want %q", payload.ModalID, "mdl-7f3a")
	}
	if payload.OptionID != "allow" {
		t.Errorf("OptionID: got %q, want %q", payload.OptionID, "allow")
	}
	// AnswerToken round-trip is AC-pinned: it is the idempotency key #703 dedups on.
	if payload.AnswerToken != "atk-91c2" {
		t.Errorf("AnswerToken: got %q, want %q", payload.AnswerToken, "atk-91c2")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestModalCancelPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "modal_cancel.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModalCancel {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModalCancel)
	}

	var payload ModalCancelPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ModalID != "mdl-7f3a" {
		t.Errorf("ModalID: got %q, want %q", payload.ModalID, "mdl-7f3a")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestModalDismissedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "modal_dismissed.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModalDismissed {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModalDismissed)
	}

	var payload ModalDismissedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ModalID != "mdl-7f3a" {
		t.Errorf("ModalID: got %q, want %q", payload.ModalID, "mdl-7f3a")
	}
	if payload.Outcome != "allow" {
		t.Errorf("Outcome: got %q, want %q", payload.Outcome, "allow")
	}
	if payload.Source != "remote" {
		t.Errorf("Source: got %q, want %q", payload.Source, "remote")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestQueueStatePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "queue_state.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeQueueState {
		t.Errorf("Type: got %q, want %q", env.Type, TypeQueueState)
	}

	var payload QueueStatePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "conv-1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conv-1")
	}
	// Array order IS enqueue order: assert both length and the positional fields.
	if len(payload.Queued) != 2 {
		t.Fatalf("Queued: got len %d, want 2", len(payload.Queued))
	}
	wantItems := []struct {
		id   uint64
		text string
		ts   time.Time
	}{
		{1, "first queued", time.Date(2026, 6, 23, 9, 59, 58, 0, time.UTC)},
		{2, "second queued", time.Date(2026, 6, 23, 9, 59, 59, 0, time.UTC)},
	}
	for i, want := range wantItems {
		got := payload.Queued[i]
		if got.QueuedMsgID != want.id {
			t.Errorf("Queued[%d].QueuedMsgID: got %d, want %d", i, got.QueuedMsgID, want.id)
		}
		if got.Text != want.text {
			t.Errorf("Queued[%d].Text: got %q, want %q", i, got.Text, want.text)
		}
		// .Equal (never == / reflect.DeepEqual): RFC3339Nano strips the monotonic
		// clock on marshal, so the wall clocks compare equal but the structs do not.
		if !got.TS.Equal(want.ts) {
			t.Errorf("Queued[%d].TS: got %v, want %v", i, got.TS, want.ts)
		}
	}

	// Byte-equal round-trip catches an accidental omitempty re-introduction.
	roundTripEnvelope(t, env, payload, raw)
}

func TestSessionErrorPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "session_error.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSessionError {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSessionError)
	}

	var payload SessionErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "conv-1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conv-1")
	}
	if payload.Code != CodeSessionBlocked {
		t.Errorf("Code: got %q, want %q", payload.Code, CodeSessionBlocked)
	}
	if payload.Message == "" {
		t.Error("Message: got empty, want non-empty daemon-generated reason")
	}

	// Byte-equal round-trip pins the stable json field names (conversation_id
	// routing key) and catches an accidental omitempty re-introduction.
	roundTripEnvelope(t, env, payload, raw)
}

func TestDequeueMessagePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "dequeue_message.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeDequeueMessage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeDequeueMessage)
	}

	var payload DequeueMessagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "conv-1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conv-1")
	}
	if payload.QueuedMsgID != 2 {
		t.Errorf("QueuedMsgID: got %d, want 2", payload.QueuedMsgID)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestDequeueMessagePayload_Malformed pins the AC's "malformed dequeue_message
// rejected cleanly (error, no panic)". A queued_msg_id that is not a JSON
// number fails to unmarshal into the uint64 field via stdlib json — no new code
// path, no panic.
func TestDequeueMessagePayload_Malformed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"queued_msg_id-as-string", `{"conversation_id":"conv-1","queued_msg_id":"2"}`},
		{"queued_msg_id-negative", `{"conversation_id":"conv-1","queued_msg_id":-1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var payload DequeueMessagePayload
			if err := json.Unmarshal([]byte(tc.raw), &payload); err == nil {
				t.Errorf("Unmarshal(%s): got nil error, want non-nil", tc.raw)
			}
		})
	}
}
