package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

// strptr is a local helper for building the tri-state SystemPrompt pointer. The
// package has no shared one and a second caller would be over-DRY.
func strptr(s string) *string { return &s }

// TestRequestSystemPromptPayload_RoundTrip pins the conversation system-prompt
// READ request (#2152) end to end through a full Envelope: the type string, the
// correlation-free shape, and the one field the frame carries.
func TestRequestSystemPromptPayload_RoundTrip(t *testing.T) {
	body, err := json.Marshal(RequestSystemPromptPayload{ConversationID: "conv-abc123"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	raw, err := json.Marshal(Envelope{
		ID:      812,
		Type:    TypeRequestSystemPrompt,
		TS:      time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
		Payload: body,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRequestSystemPrompt {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRequestSystemPrompt)
	}
	var got RequestSystemPromptPayload
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.ConversationID != "conv-abc123" {
		t.Errorf("ConversationID = %q, want %q", got.ConversationID, "conv-abc123")
	}
}

// TestRequestSystemPromptPayload_WireKeys pins the request's COMPLETE key set, so
// a later field cannot be added without this failing. It is the machine-checked
// form of the frame's central omission — NO REQUEST-ID KEY, because correlation
// rides the envelope's InReplyTo.
func TestRequestSystemPromptPayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(RequestSystemPromptPayload{ConversationID: "c1"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	want := map[string]bool{"conversation_id": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected wire key %q: the request's key set is fixed at %v — correlation rides the envelope's in_reply_to, so this frame carries no request-id key", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s — the key is always present (no omitempty), so absent and empty stay the same case", k, b)
		}
	}
}

// TestRequestSystemPromptPayload_ZeroValue_KeyPresent pins the no-omitempty
// decision directly: a zero-valued request still carries the key. Separate from
// the key-set test above because the two fail on different mutants — that one
// marshals a POPULATED struct, so an omitempty added to ConversationID leaves it
// green; only a zero value forces the tag to speak.
func TestRequestSystemPromptPayload_ZeroValue_KeyPresent(t *testing.T) {
	b, err := json.Marshal(RequestSystemPromptPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	if want := `{"conversation_id":""}`; string(b) != want {
		t.Errorf("zero payload: got %s, want %s — the key is unconditional, so a client that names no conversation still sends a well-formed frame", b, want)
	}
}

// TestSystemPromptPayload_TriStateSurvivesARoundTrip is AC #1's round-trip clause,
// and the one test that would catch the most likely way this payload ships broken:
// a plain string in place of the pointer, or an omitempty dropped from it.
//
// The registry stores three states — no prompt, an explicitly empty prompt, and
// text — and set_system_prompt accepts all three inbound. If a read collapsed two
// of them, a client that read a value and wrote it straight back would silently
// convert "explicitly empty" into "no prompt". Each row asserts the exact bytes on
// the wire AND that a decode of those bytes reproduces the same pointer state, so
// neither direction can drift on its own.
func TestSystemPromptPayload_TriStateSurvivesARoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		stored   *string
		wantJSON string
	}{
		{
			name:     "no prompt stored: the key is ABSENT, never null-with-a-key",
			stored:   nil,
			wantJSON: `{"session_prompt_status":"no_session"}`,
		},
		{
			name:     "an explicitly empty prompt: the key is PRESENT and empty",
			stored:   strptr(""),
			wantJSON: `{"system_prompt":"","session_prompt_status":"no_session"}`,
		},
		{
			name:     "stored text: carried verbatim",
			stored:   strptr("Answer only in haiku."),
			wantJSON: `{"system_prompt":"Answer only in haiku.","session_prompt_status":"no_session"}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(SystemPromptPayload{
				SystemPrompt:        tc.stored,
				SessionPromptStatus: SystemPromptStatusNoSession,
			})
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if string(b) != tc.wantJSON {
				t.Fatalf("marshalled = %s, want %s — omitempty on a POINTER tests the pointer, not the pointee, which is what keeps the two no-bytes states distinguishable", b, tc.wantJSON)
			}

			var got SystemPromptPayload
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			switch {
			case tc.stored == nil && got.SystemPrompt != nil:
				t.Errorf("decoded SystemPrompt = %q, want nil — a read-then-write round trip must not turn 'no prompt' into an explicitly empty one", *got.SystemPrompt)
			case tc.stored != nil && got.SystemPrompt == nil:
				t.Errorf("decoded SystemPrompt = nil, want a pointer to %q — a read-then-write round trip must not turn a stored value into 'no prompt'", *tc.stored)
			case tc.stored != nil && *got.SystemPrompt != *tc.stored:
				t.Errorf("decoded SystemPrompt = %q, want %q", *got.SystemPrompt, *tc.stored)
			}
		})
	}
}

// TestSystemPromptPayload_WireKeys pins the reply's COMPLETE key set against a
// populated payload — the two fields it carries, and by omission the two it
// deliberately does not: the spawned-with text (a verdict is sent instead of a
// second copy of up to 8192 operator bytes) and a conversation_id (correlation
// rides in_reply_to, and its absence is what lets an unhosted conversation be
// answered byte-identically to a hosted one holding nothing).
func TestSystemPromptPayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(SystemPromptPayload{
		SystemPrompt:        strptr("Answer only in haiku."),
		SessionPromptStatus: SystemPromptStatusDiffers,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	want := map[string]bool{"system_prompt": true, "session_prompt_status": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected wire key %q: the reply's key set is fixed at %v — it reports a DIFFERENCE, not a second copy of the spawned-with text, and carries no conversation_id", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s", k, b)
		}
	}
}

// TestSystemPromptPayload_StatusIsAlwaysOnTheWire pins the no-omitempty decision on
// the verdict field, which the tri-state test above cannot: every row there sets a
// non-empty status, so an omitempty added to SessionPromptStatus for tidiness would
// leave all three green.
//
// The daemon never emits the zero payload — the handler sets one of the three
// constants on every branch, the unresolvable one included — so this asserts the
// SHAPE holds even for a value no path produces, which is what makes "a client
// switches on three cases and has no fourth" a property of the type.
func TestSystemPromptPayload_StatusIsAlwaysOnTheWire(t *testing.T) {
	b, err := json.Marshal(SystemPromptPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	if want := `{"session_prompt_status":""}`; string(b) != want {
		t.Errorf("zero payload: got %s, want %s — the verdict key is unconditional", b, want)
	}
}

// TestSystemPromptStatus_VocabularyIsThreeDistinctValues pins the closed set the
// wire publishes. A client branches on these strings, so a rename is a wire break
// rather than a refactor, and two constants collapsing onto one value would make a
// state unreportable while every handler test stayed green.
func TestSystemPromptStatus_VocabularyIsThreeDistinctValues(t *testing.T) {
	got := map[string]string{
		"no-session": SystemPromptStatusNoSession,
		"matches":    SystemPromptStatusMatches,
		"differs":    SystemPromptStatusDiffers,
	}
	want := map[string]string{
		"no-session": "no_session",
		"matches":    "matches",
		"differs":    "differs",
	}
	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("%s status = %q, want %q — the wire string IS the contract", name, got[name], wantValue)
		}
	}
	seen := map[string]bool{}
	for name, value := range got {
		if value == "" {
			t.Errorf("%s status is the empty string, which this payload reserves for no state at all", name)
		}
		if seen[value] {
			t.Errorf("%s status duplicates another constant's value %q — one of the three states would be unreportable", name, value)
		}
		seen[value] = true
	}
}

// TestSystemPromptPayload_RoundTrip pins the reply end to end through a full
// Envelope, including the InReplyTo correlation the payload deliberately carries no
// key for.
func TestSystemPromptPayload_RoundTrip(t *testing.T) {
	body, err := json.Marshal(SystemPromptPayload{
		SystemPrompt:        strptr("Answer only in haiku."),
		SessionPromptStatus: SystemPromptStatusDiffers,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	inReplyTo := uint64(812)
	raw, err := json.Marshal(Envelope{
		ID:        1,
		Type:      TypeSystemPrompt,
		TS:        time.Date(2026, 9, 7, 12, 0, 1, 0, time.UTC),
		Payload:   body,
		InReplyTo: &inReplyTo,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSystemPrompt {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSystemPrompt)
	}
	if env.InReplyTo == nil || *env.InReplyTo != inReplyTo {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, inReplyTo)
	}
	if env.EventID != nil {
		t.Errorf("EventID = %d, want absent — a correlated reply never enters the replay ring", *env.EventID)
	}
	var got SystemPromptPayload
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.SystemPrompt == nil || *got.SystemPrompt != "Answer only in haiku." {
		t.Errorf("SystemPrompt = %v, want a pointer to the stored text", got.SystemPrompt)
	}
	if got.SessionPromptStatus != SystemPromptStatusDiffers {
		t.Errorf("SessionPromptStatus = %q, want %q", got.SessionPromptStatus, SystemPromptStatusDiffers)
	}
}
