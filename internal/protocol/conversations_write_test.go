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
	// The unlabelled-workspace case, the state a client meets before any folder
	// is ever named. As with Name above, the decoded assertion is the whole
	// check: the envelope round-trip re-marshals the fixture's raw payload bytes
	// and cannot see this field. That the fixture spells it null rather than
	// omitting the key is the wire contract, and it is what the handler tests
	// assert on raw bytes.
	if p.WorkspaceLabel != nil {
		t.Errorf("WorkspaceLabel: got pointer to %q, want nil (wire was null)", *p.WorkspaceLabel)
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

func TestRenameConversationPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "rename_conversation.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRenameConversation {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRenameConversation)
	}

	var p RenameConversationPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.ConversationID != "c2..." {
		t.Errorf("ConversationID: got %q, want %q", p.ConversationID, "c2...")
	}
	if p.Name != "weekly-sync" {
		t.Errorf("Name: got %q, want %q", p.Name, "weekly-sync")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestDeleteConversationPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "delete_conversation.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeDeleteConversation {
		t.Errorf("Type: got %q, want %q", env.Type, TypeDeleteConversation)
	}

	var p DeleteConversationPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.ConversationID != "c2..." {
		t.Errorf("ConversationID: got %q, want %q", p.ConversationID, "c2...")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

// TestArchiveConversationPayload_RoundTrip exercises the shared
// ArchiveConversationPayload against both wire types: archive_conversation and
// unarchive_conversation differ only in the envelope type, decode into the same
// id-only payload, and round-trip byte-equivalently.
func TestArchiveConversationPayload_RoundTrip(t *testing.T) {
	cases := []struct {
		fixture  string
		wantType string
	}{
		{"archive_conversation.json", TypeArchiveConversation},
		{"unarchive_conversation.json", TypeUnarchiveConversation},
	}
	for _, tc := range cases {
		t.Run(tc.wantType, func(t *testing.T) {
			raw := readFixture(t, tc.fixture)

			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if env.Type != tc.wantType {
				t.Errorf("Type: got %q, want %q", env.Type, tc.wantType)
			}

			var p ArchiveConversationPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if p.ConversationID != "c2..." {
				t.Errorf("ConversationID: got %q, want %q", p.ConversationID, "c2...")
			}

			out, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
				t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
			}
		})
	}
}

func TestConversationDeletedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "conversation_deleted.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeConversationDeleted {
		t.Errorf("Type: got %q, want %q", env.Type, TypeConversationDeleted)
	}
	if env.InReplyTo == nil || *env.InReplyTo != 6 {
		t.Errorf("InReplyTo: got %v, want pointer to 6", env.InReplyTo)
	}

	var p ConversationDeletedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.ID != "c2..." {
		t.Errorf("ID: got %q, want %q", p.ID, "c2...")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

// #2571: is_muted is always serialized on both the list row and the update
// record, false included. A client folds an update record into its list in
// place, so an omitted key would read as "not muted" on a muted channel's next
// rename. The fixture round-trips cannot pin this: Envelope.Payload is raw
// bytes, so marshalling the envelope never touches these structs.
func TestIsMuted_AlwaysSerialized(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"ConversationSummary", ConversationSummary{}},
		{"ConversationUpdatedPayload", ConversationUpdatedPayload{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !bytes.Contains(out, []byte(`"is_muted":false`)) {
				t.Errorf("zero value omitted is_muted:\n%s", out)
			}
		})
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
	if p.IsArchived {
		t.Errorf("IsArchived: got true, want false (fixture is an active conversation)")
	}
	if p.Name == nil || *p.Name != "weekly-planning" {
		t.Errorf("Name: got %v, want pointer to %q", p.Name, "weekly-planning")
	}
	if p.Cwd != "/Users/juhana/pyry-workspace/weekly-planning" {
		t.Errorf("Cwd: got %q", p.Cwd)
	}
	// The re-marshal below cannot see WorkspaceLabel at all: Envelope.Payload is
	// a json.RawMessage, so marshalling env writes the fixture's own payload
	// bytes back and the byte comparison passes whatever the struct gained or
	// lost. This decoded assertion is the only thing holding the field to the
	// fixture. The sibling conversation_created fixture carries the null case.
	if p.WorkspaceLabel == nil || *p.WorkspaceLabel != "Weekly planning" {
		t.Errorf("WorkspaceLabel: got %v, want pointer to %q", p.WorkspaceLabel, "Weekly planning")
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
