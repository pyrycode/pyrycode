package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestListConversationsPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "list_conversations.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeListConversations {
		t.Errorf("Type: got %q, want %q", env.Type, TypeListConversations)
	}

	var p ListConversationsPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestConversationsPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "conversations.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeConversations {
		t.Errorf("Type: got %q, want %q", env.Type, TypeConversations)
	}

	var p ConversationsPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(p.Conversations) != 2 {
		t.Fatalf("Conversations: got len %d, want 2", len(p.Conversations))
	}

	// Row 0: name set.
	c0 := p.Conversations[0]
	if c0.ID != "c1..." || c0.Cwd != "/Users/juhana/Workspace/Projects/KitchenClaw" || !c0.IsPromoted {
		t.Errorf("row 0 scalar fields: %+v", c0)
	}
	if c0.Name == nil || *c0.Name != "kitchen-claw refactor" {
		t.Errorf("row 0 Name: got %v, want pointer to %q", c0.Name, "kitchen-claw refactor")
	}

	// Row 1: name null on wire → nil pointer.
	c1 := p.Conversations[1]
	if c1.ID != "c2..." || c1.Cwd != "/Users/juhana/pyry-workspace/scratch" || c1.IsPromoted {
		t.Errorf("row 1 scalar fields: %+v", c1)
	}
	if c1.Name != nil {
		t.Errorf("row 1 Name: got pointer to %q, want nil (wire was null)", *c1.Name)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}
