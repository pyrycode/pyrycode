package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

func labelPtr(s string) *string { return &s }

// spawnDirCreator is sessionMinter.Create minus the pool: it resolves through
// the same resolveSpawnDir and answers with that result, which is all
// sessionMinter adds to Pool.Mint's id.
type spawnDirCreator struct{}

func (spawnDirCreator) Create(_ context.Context, _, spawnDir string) (string, string, error) {
	dir, err := resolveSpawnDir(spawnDir)
	if err != nil {
		return "", "", err
	}
	return "sess", dir, nil
}

// newNormaliseHome points $HOME at a temp dir, creates $HOME/pyry-workspace and
// makes it the process cwd, and returns it with the realpath of its "default"
// child as every spelling of that folder should record it.
func newNormaliseHome(t *testing.T) (home, ws, wantReal string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	ws = filepath.Join(home, "pyry-workspace")
	if err := os.MkdirAll(filepath.Join(ws, "default"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(ws)
	real, err := filepath.EvalSymlinks(filepath.Join(ws, "default"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return home, ws, real
}

// TestCreateConversation_SpellingsOfOneFolder_RecordOneCwd covers #2568 AC2:
// with the daemon's cwd at $HOME/pyry-workspace, creating conversations with
// "default", "~/pyry-workspace/default" and the absolute form stores three rows
// whose Cwd is byte-equal — the folder the session spawns in.
func TestCreateConversation_SpellingsOfOneFolder_RecordOneCwd(t *testing.T) {
	_, ws, wantReal := newNormaliseHome(t)
	installIdentityTrustMark(t)
	regPath := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h := handlers.CreateConversation(reg, spawnDirCreator{}, regPath, "/unused/default", discardLogger())

	for _, spelling := range []string{"default", "~/pyry-workspace/default", filepath.Join(ws, "default")} {
		cwd := spelling
		payload, err := json.Marshal(protocol.CreateConversationPayload{Cwd: &cwd})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		out := make(chan protocol.RoutingEnvelope, 1)
		c := dispatch.NewTestConn("c-2568", out, nil)
		env := protocol.Envelope{ID: 1, Type: protocol.TypeCreateConversation, TS: time.Now().UTC(), Payload: payload}
		if err := h(context.Background(), c, env); err != nil {
			t.Fatalf("handler(%q): %v", spelling, err)
		}
		var reply protocol.Envelope
		if err := json.Unmarshal((<-out).Frame, &reply); err != nil {
			t.Fatalf("unmarshal reply: %v", err)
		}
		if reply.Type != protocol.TypeConversationCreated {
			t.Fatalf("reply to %q = %s, want %s", spelling, reply.Type, protocol.TypeConversationCreated)
		}
	}

	rows := reg.List()
	if len(rows) != 3 {
		t.Fatalf("registry has %d rows, want 3", len(rows))
	}
	for _, r := range rows {
		if r.Cwd != wantReal {
			t.Errorf("row %s Cwd = %q, want %q", r.ID, r.Cwd, wantReal)
		}
	}
}

// TestNormaliseLegacyCwds_RewritesRowsAndLabels covers #2568 AC3: relative and
// "~" rows are rewritten to the realpath, a label moves with its key and loses
// to one already stored under the absolute key, absolute rows (even ones that no
// longer resolve) and empty ones are untouched, and the result is saved.
func TestNormaliseLegacyCwds_RewritesRowsAndLabels(t *testing.T) {
	home, _, wantReal := newNormaliseHome(t)
	if err := os.MkdirAll(filepath.Join(home, "notes"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	notesReal, err := filepath.EvalSymlinks(filepath.Join(home, "notes"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	regPath := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	reg.Create(conversations.Conversation{ID: "rel", Cwd: "default"})
	reg.Create(conversations.Conversation{ID: "tilde", Cwd: "~/pyry-workspace/default"})
	reg.Create(conversations.Conversation{ID: "abs", Cwd: wantReal})
	reg.Create(conversations.Conversation{ID: "notes", Cwd: "~/notes"})
	reg.Create(conversations.Conversation{ID: "gone", Cwd: "/no/such/absolute/dir"})
	reg.Create(conversations.Conversation{ID: "empty", Cwd: ""})
	reg.SetWorkspaceLabel("default", labelPtr("Legacy"))
	reg.SetWorkspaceLabel(wantReal, labelPtr("Absolute"))
	reg.SetWorkspaceLabel("~/notes", labelPtr("Notes"))

	var buf bytes.Buffer
	normaliseLegacyCwds(reg, regPath, resolveWorkspaceDir, slog.New(slog.NewTextHandler(&buf, nil)))

	back, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	want := map[conversations.ConversationID]string{
		"rel":   wantReal,
		"tilde": wantReal,
		"abs":   wantReal,
		"notes": notesReal,
		"gone":  "/no/such/absolute/dir",
		"empty": "",
	}
	rows := back.List()
	if len(rows) != len(want) {
		t.Fatalf("reloaded %d rows, want %d", len(rows), len(want))
	}
	for _, r := range rows {
		if r.Cwd != want[r.ID] {
			t.Errorf("row %s Cwd = %q, want %q", r.ID, r.Cwd, want[r.ID])
		}
	}
	if got, _ := back.WorkspaceLabel(wantReal); got != "Absolute" {
		t.Errorf("label at the absolute key = %q, want the one already stored there", got)
	}
	if got, _ := back.WorkspaceLabel(notesReal); got != "Notes" {
		t.Errorf("label at %q = %q, want the moved \"Notes\"", notesReal, got)
	}
	for _, old := range []string{"default", "~/notes"} {
		if _, ok := back.WorkspaceLabel(old); ok {
			t.Errorf("legacy label key %q survived", old)
		}
	}
	if !strings.Contains(buf.String(), "conversations.legacy_cwd_normalised") {
		t.Errorf("no normalised event logged:\n%s", buf.String())
	}
}

// TestNormaliseLegacyCwds_UnresolvableLeftAndNotEchoed covers #2568 AC4: a
// legacy cwd naming a folder that no longer exists, or one escaping $HOME, is
// left unchanged and logged by a static event that carries neither path; with
// nothing rewritten, nothing is saved.
func TestNormaliseLegacyCwds_UnresolvableLeftAndNotEchoed(t *testing.T) {
	home, _, _ := newNormaliseHome(t)
	outside := t.TempDir()
	escape, err := filepath.Rel(filepath.Join(home, "pyry-workspace"), outside)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	regPath := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	reg.Create(conversations.Conversation{ID: "missing", Cwd: "vanished-folder"})
	reg.Create(conversations.Conversation{ID: "escape", Cwd: escape})
	reg.SetWorkspaceLabel("vanished-folder", labelPtr("Kept"))

	var buf bytes.Buffer
	normaliseLegacyCwds(reg, regPath, resolveWorkspaceDir, slog.New(slog.NewTextHandler(&buf, nil)))

	for _, r := range reg.List() {
		wantCwd := map[conversations.ConversationID]string{"missing": "vanished-folder", "escape": escape}[r.ID]
		if r.Cwd != wantCwd {
			t.Errorf("row %s Cwd = %q, want it left as %q", r.ID, r.Cwd, wantCwd)
		}
	}
	if got, ok := reg.WorkspaceLabel("vanished-folder"); !ok || got != "Kept" {
		t.Errorf("label on an unresolved key = (%q, %v), want it kept", got, ok)
	}
	if _, err := os.Stat(regPath); !os.IsNotExist(err) {
		t.Errorf("registry file written with nothing rewritten (stat err = %v)", err)
	}
	logged := buf.String()
	if n := strings.Count(logged, "conversations.legacy_cwd_unresolved"); n != 2 {
		t.Errorf("unresolved event logged %d times, want 2:\n%s", n, logged)
	}
	outsideReal, _ := filepath.EvalSymlinks(outside)
	for _, leak := range []string{"vanished-folder", escape, outside, outsideReal, home} {
		if strings.Contains(logged, leak) {
			t.Errorf("log echoes %q:\n%s", leak, logged)
		}
	}
}
