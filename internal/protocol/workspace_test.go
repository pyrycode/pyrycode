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
