package protocol

import (
	"bytes"
	"encoding/json"
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

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
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

func TestBackfillSincePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "backfill_since.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeBackfillSince {
		t.Errorf("Type: got %q, want %q", env.Type, TypeBackfillSince)
	}

	var payload BackfillSincePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != nil {
		t.Errorf("ConversationID: got %v, want nil (spec wire shows literal null)", payload.ConversationID)
	}
	wantSince := time.Date(2026, 5, 8, 8, 14, 2, 0, time.UTC)
	if !payload.SinceTS.Equal(wantSince) {
		t.Errorf("SinceTS: got %v, want %v", payload.SinceTS, wantSince)
	}
	if payload.MaxMessages != 500 {
		t.Errorf("MaxMessages: got %d, want 500", payload.MaxMessages)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Byte-equal check is the regression detector for the *string-without-omitempty
	// design decision: if a future contributor adds omitempty back, the
	// "conversation_id":null key disappears from the output and this fails.
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
			// cwd-unset output and this fails (mirrors backfill_since's guard).
			if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
				t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
			}
		})
	}
}

func TestMessageChunkPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "message_chunk.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeMessageChunk {
		t.Errorf("Type: got %q, want %q", env.Type, TypeMessageChunk)
	}

	var payload MessageChunkPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Messages) != 2 {
		t.Fatalf("Messages: got len %d, want 2", len(payload.Messages))
	}
	if payload.Messages[0].Role != "assistant" {
		t.Errorf("Messages[0].Role: got %q, want %q", payload.Messages[0].Role, "assistant")
	}
	if payload.Messages[1].Role != "user" {
		t.Errorf("Messages[1].Role: got %q, want %q", payload.Messages[1].Role, "user")
	}
	if payload.Messages[1].MessageID != "m13" {
		t.Errorf("Messages[1].MessageID: got %q, want %q", payload.Messages[1].MessageID, "m13")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestBackfillDonePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "backfill_done.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeBackfillDone {
		t.Errorf("Type: got %q, want %q", env.Type, TypeBackfillDone)
	}
	if env.InReplyTo == nil || *env.InReplyTo != 6 {
		t.Errorf("InReplyTo: got %v, want pointer to 6", env.InReplyTo)
	}

	var payload BackfillDonePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Delivered != 47 {
		t.Errorf("Delivered: got %d, want 47", payload.Delivered)
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
