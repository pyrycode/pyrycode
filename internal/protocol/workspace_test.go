package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCreateWorkspaceFolderPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "create_workspace_folder.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeCreateWorkspaceFolder {
		t.Errorf("Type: got %q, want %q", env.Type, TypeCreateWorkspaceFolder)
	}

	var p CreateWorkspaceFolderPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Parent != "~/pyry-workspace" {
		t.Errorf("Parent: got %q, want %q", p.Parent, "~/pyry-workspace")
	}
	if p.Name != "new-project" {
		t.Errorf("Name: got %q, want %q", p.Name, "new-project")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestRecentWorkspacesPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "recent_workspaces.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRecentWorkspaces {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRecentWorkspaces)
	}

	var p RecentWorkspacesPayload
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

func TestRecentWorkspacesListPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "recent_workspaces_list.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRecentWorkspacesList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRecentWorkspacesList)
	}
	if env.InReplyTo == nil || *env.InReplyTo != 8 {
		t.Errorf("InReplyTo: got %v, want pointer to 8", env.InReplyTo)
	}

	var p RecentWorkspacesListPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(p.Workspaces) != 2 {
		t.Fatalf("len(Workspaces): got %d, want 2", len(p.Workspaces))
	}
	if p.Workspaces[0].Path != "/Users/juhana/pyry-workspace/alpha" {
		t.Errorf("Workspaces[0].Path: got %q", p.Workspaces[0].Path)
	}
	// Entries are most-recent-first: alpha (09:12) precedes beta (18:04 prior day).
	if !p.Workspaces[0].LastUsedAt.After(p.Workspaces[1].LastUsedAt) {
		t.Errorf("ordering: got %v then %v, want most-recent-first",
			p.Workspaces[0].LastUsedAt, p.Workspaces[1].LastUsedAt)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

// TestRenameWorkspacePayload_RoundTrip pins the request wire shape (#2207). The
// fixture carries a SET (a non-null label), the half the reply fixture below does
// not cover.
func TestRenameWorkspacePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "rename_workspace.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRenameWorkspace {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRenameWorkspace)
	}

	var p RenameWorkspacePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Path != "/Users/juhana/pyry-workspace/alpha" {
		t.Errorf("Path: got %q", p.Path)
	}
	if p.Label == nil || *p.Label != "Tax filing" {
		t.Errorf("Label: got %v, want pointer to %q", p.Label, "Tax filing")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

// TestWorkspaceUpdatedPayload_RoundTrip pins the reply wire shape (#2207). The
// fixture deliberately carries the CLEAR — an explicit "label":null — because
// that is the arm an accidental `omitempty` on the field would break: a nil
// pointer would marshal to an absent key, and the byte comparison below would
// then differ from a fixture that has one. A set-label fixture could not tell the
// two encodings apart.
func TestWorkspaceUpdatedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "workspace_updated.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeWorkspaceUpdated {
		t.Errorf("Type: got %q, want %q", env.Type, TypeWorkspaceUpdated)
	}
	if env.InReplyTo == nil || *env.InReplyTo != 9 {
		t.Errorf("InReplyTo: got %v, want pointer to 9", env.InReplyTo)
	}

	var p WorkspaceUpdatedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Path != "/Users/juhana/pyry-workspace/beta" {
		t.Errorf("Path: got %q", p.Path)
	}
	if p.Label != nil {
		t.Errorf("Label: got pointer to %q, want nil (a cleared label is null, not \"\")", *p.Label)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestWorkspaceFolderCreatedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "workspace_folder_created.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeWorkspaceFolderCreated {
		t.Errorf("Type: got %q, want %q", env.Type, TypeWorkspaceFolderCreated)
	}
	if env.InReplyTo == nil || *env.InReplyTo != 7 {
		t.Errorf("InReplyTo: got %v, want pointer to 7", env.InReplyTo)
	}

	var p WorkspaceFolderCreatedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Path != "/Users/juhana/pyry-workspace/new-project" {
		t.Errorf("Path: got %q", p.Path)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}
