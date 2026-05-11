package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestCreateConversationPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "create_conversation.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeCreateConversation {
		t.Errorf("Type: got %q, want %q", env.Type, TypeCreateConversation)
	}

	var p CreateConversationPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.IsPromoted == nil || *p.IsPromoted {
		t.Errorf("IsPromoted: got %v, want pointer to false", p.IsPromoted)
	}
	if p.Name != nil {
		t.Errorf("Name: got pointer to %q, want nil (wire was null)", *p.Name)
	}
	if p.Cwd != nil {
		t.Errorf("Cwd: got pointer to %q, want nil (wire was null)", *p.Cwd)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestConversationCreatedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "conversation_created.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeConversationCreated {
		t.Errorf("Type: got %q, want %q", env.Type, TypeConversationCreated)
	}
	if env.InReplyTo == nil || *env.InReplyTo != 4 {
		t.Errorf("InReplyTo: got %v, want pointer to 4", env.InReplyTo)
	}

	var p ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.ID != "c3..." {
		t.Errorf("ID: got %q, want %q", p.ID, "c3...")
	}
	if p.IsPromoted {
		t.Errorf("IsPromoted: got true, want false")
	}
	if p.Cwd != "/Users/juhana/pyry-workspace/scratch" {
		t.Errorf("Cwd: got %q", p.Cwd)
	}
	if p.Name != nil {
		t.Errorf("Name: got pointer to %q, want nil (wire was null)", *p.Name)
	}
	wantTS, err := time.Parse(time.RFC3339Nano, "2026-05-08T10:34:01Z")
	if err != nil {
		t.Fatalf("parse expected last_used_at: %v", err)
	}
	if !p.LastUsedAt.Equal(wantTS) {
		t.Errorf("LastUsedAt: got %v, want %v", p.LastUsedAt, wantTS)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestPromoteConversationPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "promote_conversation.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypePromoteConversation {
		t.Errorf("Type: got %q, want %q", env.Type, TypePromoteConversation)
	}

	var p PromoteConversationPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.ConversationID != "c2..." {
		t.Errorf("ConversationID: got %q, want %q", p.ConversationID, "c2...")
	}
	if p.Name != "weekly-planning" {
		t.Errorf("Name: got %q, want %q", p.Name, "weekly-planning")
	}
	if p.Cwd != "/Users/juhana/pyry-workspace/weekly-planning" {
		t.Errorf("Cwd: got %q", p.Cwd)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestConversationUpdatedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "conversation_updated.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeConversationUpdated {
		t.Errorf("Type: got %q, want %q", env.Type, TypeConversationUpdated)
	}

	var p ConversationUpdatedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.ID != "c2..." {
		t.Errorf("ID: got %q, want %q", p.ID, "c2...")
	}
	if !p.IsPromoted {
		t.Errorf("IsPromoted: got false, want true")
	}
	if p.Name == nil || *p.Name != "weekly-planning" {
		t.Errorf("Name: got %v, want pointer to %q", p.Name, "weekly-planning")
	}
	if p.Cwd != "/Users/juhana/pyry-workspace/weekly-planning" {
		t.Errorf("Cwd: got %q", p.Cwd)
	}
	wantTS, err := time.Parse(time.RFC3339Nano, "2026-05-08T10:34:30Z")
	if err != nil {
		t.Fatalf("parse expected last_used_at: %v", err)
	}
	if !p.LastUsedAt.Equal(wantTS) {
		t.Errorf("LastUsedAt: got %v, want %v", p.LastUsedAt, wantTS)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}
