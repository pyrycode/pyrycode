package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// workspaceReadFixture is one registry holding one conversation whose recorded
// workspace is ws, plus a sibling directory outside it.
type workspaceReadFixture struct {
	reg     *conversations.Registry
	convID  conversations.ConversationID
	ws      string
	outside string
	read    func(conversationID, path string) (relay.WorkspaceFile, bool)
}

// newWorkspaceReadFixture builds the fixture with a read bound of maxBytes, so
// the size refusal is exercisable without materialising 16 MiB.
func newWorkspaceReadFixture(t *testing.T, maxBytes int64) *workspaceReadFixture {
	t.Helper()
	root := t.TempDir()
	reg, err := conversations.Load(filepath.Join(root, "conversations.json"))
	if err != nil {
		t.Fatalf("conversations.Load: %v", err)
	}
	f := &workspaceReadFixture{
		reg:     reg,
		ws:      filepath.Join(root, "ws"),
		outside: filepath.Join(root, "outside"),
	}
	for _, d := range []string{f.ws, f.outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	id, err := conversations.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	reg.Create(conversations.Conversation{ID: id, Cwd: f.ws})
	f.convID = id
	f.read = workspaceFileReader(reg, maxBytes)
	return f
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
}

// TestWorkspaceFileReader_ReadsMarkdown: a markdown file inside the workspace
// is read by relative and by absolute path, in any extension case, named by the
// RESOLVED file's base name and keyed on a fresh canonical UUIDv4 per read.
func TestWorkspaceFileReader_ReadsMarkdown(t *testing.T) {
	t.Parallel()
	f := newWorkspaceReadFixture(t, maxAttachFileBytes)
	writeFile(t, f.ws, "notes/plan.md", "# plan")
	writeFile(t, f.ws, "READ.MARKDOWN", "# upper")
	writeFile(t, f.ws, "real.md", "# real")
	mustSymlink(t, "real.md", filepath.Join(f.ws, "alias.md"))

	tests := []struct {
		name, path, wantName, wantBody string
	}{
		{"relative", "notes/plan.md", "plan.md", "# plan"},
		{"absolute", filepath.Join(f.ws, "notes", "plan.md"), "plan.md", "# plan"},
		{"upper-case extension", "READ.MARKDOWN", "READ.MARKDOWN", "# upper"},
		{"in-tree markdown symlink names the target", "alias.md", "real.md", "# real"},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		got, ok := f.read(string(f.convID), tt.path)
		if !ok {
			t.Errorf("%s: refused, want served", tt.name)
			continue
		}
		if string(got.Data) != tt.wantBody || got.Filename != tt.wantName {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tt.name, got.Filename, got.Data, tt.wantName, tt.wantBody)
		}
		if !conversations.ValidID(got.AttachmentID) {
			t.Errorf("%s: attachment id %q is not a canonical lowercase UUIDv4", tt.name, got.AttachmentID)
		}
		if seen[got.AttachmentID] {
			t.Errorf("%s: attachment id %q reused; each transfer mints its own", tt.name, got.AttachmentID)
		}
		seen[got.AttachmentID] = true
	}
}

// TestWorkspaceFileReader_ReadsLive: nothing is cached or stored — an edit
// between two reads shows in the second, and a change of the conversation's
// recorded workspace takes effect on the next read.
func TestWorkspaceFileReader_ReadsLive(t *testing.T) {
	t.Parallel()
	f := newWorkspaceReadFixture(t, maxAttachFileBytes)
	writeFile(t, f.ws, "note.md", "first")

	if got, ok := f.read(string(f.convID), "note.md"); !ok || string(got.Data) != "first" {
		t.Fatalf("first read = (%q, %v), want (\"first\", true)", got.Data, ok)
	}
	writeFile(t, f.ws, "note.md", "second, longer")
	if got, ok := f.read(string(f.convID), "note.md"); !ok || string(got.Data) != "second, longer" {
		t.Fatalf("read after an edit = (%q, %v), want the edited bytes", got.Data, ok)
	}

	writeFile(t, f.outside, "note.md", "other workspace")
	if !f.reg.Update(f.convID, func(c *conversations.Conversation) { c.Cwd = f.outside }) {
		t.Fatal("Update: conversation not found")
	}
	if got, ok := f.read(string(f.convID), "note.md"); !ok || string(got.Data) != "other workspace" {
		t.Fatalf("read after a workspace change = (%q, %v), want the new workspace's file", got.Data, ok)
	}
}

// TestWorkspaceFileReader_Refusals: every refusal is the same false. The
// confinement matrix itself is TestFileAttacher_Confinement's; the traversal
// and symlink-out rows here show this path goes through confineFile, and the
// in-tree symlink to a non-markdown file shows the RESOLVED leaf is checked too.
func TestWorkspaceFileReader_Refusals(t *testing.T) {
	t.Parallel()
	f := newWorkspaceReadFixture(t, 8)
	writeFile(t, f.ws, ".env", "SECRET=1")
	writeFile(t, f.ws, "notes.md.txt", "not markdown")
	writeFile(t, f.ws, "big.md", "123456789") // one byte over the bound of 8
	writeFile(t, f.outside, "escape.md", "outside")
	mustSymlink(t, ".env", filepath.Join(f.ws, "env.md"))
	mustSymlink(t, filepath.Join(f.outside, "escape.md"), filepath.Join(f.ws, "out.md"))
	if err := os.MkdirAll(filepath.Join(f.ws, "dir.md"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(f.ws, "pipe.md"), 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	emptyWS, err := conversations.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	f.reg.Create(conversations.Conversation{ID: emptyWS})
	unknown, err := conversations.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	conv := string(f.convID)
	tests := []struct{ name, conv, path string }{
		{"wrong extension", conv, ".env"},
		{"markdown then another extension", conv, "notes.md.txt"},
		{"empty path", conv, ""},
		{"in-tree symlink named .md resolving to .env", conv, "env.md"},
		{"traversal out of the workspace", conv, "../outside/escape.md"},
		{"absolute path outside the workspace", conv, filepath.Join(f.outside, "escape.md")},
		{"symlink leaving the workspace", conv, "out.md"},
		{"missing file", conv, "absent.md"},
		{"directory", conv, "dir.md"},
		{"FIFO", conv, "pipe.md"},
		{"over the size bound", conv, "big.md"},
		{"empty workspace", string(emptyWS), "note.md"},
		{"unknown conversation", string(unknown), "note.md"},
	}
	for _, tt := range tests {
		if got, ok := f.read(tt.conv, tt.path); ok {
			t.Errorf("%s: served %q, want refused", tt.name, got.Data)
		}
	}
}

// TestIsMarkdownName is the extension rule on its own, which is also what makes
// it checkable before any filesystem access: it reads nothing but the string.
func TestIsMarkdownName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"a.md": true, "a.MD": true, "a.Md": true, "a.markdown": true, "A.MarkDown": true,
		".md": true, "a.b.md": true,
		"a.txt": false, ".env": false, "a.md.txt": false, "a.mdx": false, "amd": false,
		"a.md ": false, "a.md\x00": false, "": false, ".": false, "a.": false,
	} {
		if got := isMarkdownName(name); got != want {
			t.Errorf("isMarkdownName(%q) = %v, want %v", name, got, want)
		}
	}
}
