package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRequestSnapshotPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "request_snapshot.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRequestSnapshot {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRequestSnapshot)
	}

	var payload RequestSnapshotPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestScreenSnapshotPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "screen_snapshot.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeScreenSnapshot {
		t.Errorf("Type: got %q, want %q", env.Type, TypeScreenSnapshot)
	}

	var payload ScreenSnapshotPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	// A rendered screen is multi-line: the fixture text carries newlines and
	// the canonical round-trip below pins the escaped multi-line shape.
	wantText := "line one\nline two\nline three\n"
	if payload.Text != wantText {
		t.Errorf("Text: got %q, want %q", payload.Text, wantText)
	}
	if !strings.Contains(payload.Text, "\n") {
		t.Errorf("Text should be multi-line (contain a newline); got %q", payload.Text)
	}
	// ts is a time.Time; the monotonic-clock reading strips on JSON marshal,
	// so it must be compared with time.Time.Equal — never == or
	// reflect.DeepEqual (PROJECT-MEMORY time.Time round-trip discipline).
	wantTS, err := time.Parse(time.RFC3339Nano, "2026-05-08T10:33:14Z")
	if err != nil {
		t.Fatalf("parse expected ts: %v", err)
	}
	if !payload.TS.Equal(wantTS) {
		t.Errorf("TS: got %v, want %v", payload.TS, wantTS)
	}
	// The settings-snapshot fields carry representative non-default values in
	// the fixture; assert they survive unmarshal before the canonical
	// round-trip below pins the full wire shape.
	if payload.Model != "opus" {
		t.Errorf("Model: got %q, want %q", payload.Model, "opus")
	}
	if payload.Effort != "high" {
		t.Errorf("Effort: got %q, want %q", payload.Effort, "high")
	}
	if !payload.YOLO {
		t.Errorf("YOLO: got %v, want true", payload.YOLO)
	}
	// The context-window usage fields (#857) carry representative non-zero
	// values in the fixture; assert they survive unmarshal before the canonical
	// round-trip below pins the full wire shape.
	if payload.UsedTokens != 45000 {
		t.Errorf("UsedTokens: got %d, want %d", payload.UsedTokens, 45000)
	}
	if payload.WindowTokens != 200000 {
		t.Errorf("WindowTokens: got %d, want %d", payload.WindowTokens, 200000)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestScreenSnapshotPayload_ZeroSettingsFieldsPresent pins the
// "distinguishable from unset" contract a consumer relies on: with no
// omitempty, the zero values (model "", effort "", yolo false, used_tokens 0,
// window_tokens 0) stay explicitly on the wire and are not dropped. An empty
// model/effort means "inherited default, no per-session override"; yolo:false
// means permissions enforced; window_tokens 0 means the usage seam was not
// wired (#857).
func TestScreenSnapshotPayload_ZeroSettingsFieldsPresent(t *testing.T) {
	out, err := json.Marshal(ScreenSnapshotPayload{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"model":""`, `"effort":""`, `"yolo":false`, `"used_tokens":0`, `"window_tokens":0`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("zero-value field %s should stay on the wire; got %s", want, out)
		}
	}
}

// TestSnapshotPayloads_EmptyConversationID pins the empty-conversation_id
// boundary for both payloads: with no omitempty, an empty conversation_id
// stays explicitly on the wire (it does not silently drop) and round-trips
// back to the empty string. Mirrors the Seq==0 / IsError==false
// boundary-pinning style of the interactive payloads.
func TestSnapshotPayloads_EmptyConversationID(t *testing.T) {
	t.Run("request_snapshot", func(t *testing.T) {
		out, err := json.Marshal(RequestSnapshotPayload{ConversationID: ""})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !bytes.Contains(out, []byte(`"conversation_id":""`)) {
			t.Errorf("empty conversation_id should stay on the wire; got %s", out)
		}
		var back RequestSnapshotPayload
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if back.ConversationID != "" {
			t.Errorf("ConversationID round-trip: got %q, want empty", back.ConversationID)
		}
	})

	t.Run("screen_snapshot", func(t *testing.T) {
		out, err := json.Marshal(ScreenSnapshotPayload{ConversationID: ""})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !bytes.Contains(out, []byte(`"conversation_id":""`)) {
			t.Errorf("empty conversation_id should stay on the wire; got %s", out)
		}
		var back ScreenSnapshotPayload
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if back.ConversationID != "" {
			t.Errorf("ConversationID round-trip: got %q, want empty", back.ConversationID)
		}
	})
}
