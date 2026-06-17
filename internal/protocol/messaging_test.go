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
		wantPrevID   string
		wantNewID    string
		wantReason   string
		wantOccurred time.Time
		wantCwd      *string // nil ⇒ expect WorkspaceCwd == nil (literal JSON null)
	}{
		{
			name:         "cwd-unset",
			fixture:      "session_transition.json",
			wantPrevID:   "sess-a",
			wantNewID:    "sess-b",
			wantReason:   "idle_evict",
			wantOccurred: time.Date(2026, 6, 9, 10, 33, 14, 500000000, time.UTC),
			wantCwd:      nil,
		},
		{
			name:         "cwd-set",
			fixture:      "session_transition_workspace.json",
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
