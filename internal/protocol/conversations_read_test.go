package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
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
	if len(p.Conversations) != 3 {
		t.Fatalf("Conversations: got len %d, want 3", len(p.Conversations))
	}

	// Row 0: name set, archived on the wire.
	c0 := p.Conversations[0]
	if c0.ID != "c1..." || c0.Cwd != "/Users/juhana/Workspace/Projects/KitchenClaw" || !c0.IsPromoted {
		t.Errorf("row 0 scalar fields: %+v", c0)
	}
	if c0.Name == nil || *c0.Name != "kitchen-claw refactor" {
		t.Errorf("row 0 Name: got %v, want pointer to %q", c0.Name, "kitchen-claw refactor")
	}
	if !c0.IsArchived {
		t.Errorf("row 0 IsArchived: got false, want true")
	}
	// The re-marshal below cannot see WorkspaceLabel at all: Envelope.Payload is
	// a json.RawMessage, so marshalling the envelope writes the fixture's own
	// payload bytes back and the byte comparison passes whatever the struct
	// gained or lost. These decoded assertions are the only proof the key is
	// carried by the type.
	if c0.WorkspaceLabel == nil || *c0.WorkspaceLabel != "Kitchen Claw" {
		t.Errorf("row 0 WorkspaceLabel: got %v, want pointer to %q", c0.WorkspaceLabel, "Kitchen Claw")
	}
	wantArchivedAt := time.Date(2026, 5, 8, 10, 32, 40, 0, time.UTC)
	if c0.ArchivedAt == nil || !c0.ArchivedAt.Equal(wantArchivedAt) {
		t.Errorf("row 0 ArchivedAt: got %v, want %v", c0.ArchivedAt, wantArchivedAt)
	}

	// Row 1: name null on wire → nil pointer; active (is_archived false).
	c1 := p.Conversations[1]
	if c1.ID != "c2..." || c1.Cwd != "/Users/juhana/pyry-workspace/scratch" || c1.IsPromoted {
		t.Errorf("row 1 scalar fields: %+v", c1)
	}
	if c1.Name != nil {
		t.Errorf("row 1 Name: got pointer to %q, want nil (wire was null)", *c1.Name)
	}
	if c1.IsArchived {
		t.Errorf("row 1 IsArchived: got true, want false")
	}
	if c1.WorkspaceLabel != nil {
		t.Errorf("row 1 WorkspaceLabel: got pointer to %q, want nil (wire was null)", *c1.WorkspaceLabel)
	}
	if c1.ArchivedAt != nil {
		t.Errorf("row 1 ArchivedAt: got %v, want nil (active row, wire was null)", *c1.ArchivedAt)
	}

	// Row 2: archived before the stamp existed (#2698) — archived, stamp null.
	c2 := p.Conversations[2]
	if c2.ID != "c3..." || !c2.IsArchived {
		t.Errorf("row 2 scalar fields: %+v", c2)
	}
	if c2.ArchivedAt != nil {
		t.Errorf("row 2 ArchivedAt: got %v, want nil (legacy archived row, wire was null)", *c2.ArchivedAt)
	}

	// archived_at is always serialized: a nil stamp writes an explicit null
	// rather than dropping the key (#2698).
	rowJSON, err := json.Marshal(c1)
	if err != nil {
		t.Fatalf("marshal row 1: %v", err)
	}
	if !bytes.Contains(rowJSON, []byte(`"archived_at":null`)) {
		t.Errorf("row with nil ArchivedAt serialized without an explicit null: %s", rowJSON)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}
