package main

import (
	"bytes"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/pyrycode/pyrycode/internal/agentrun"
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

// folderReadFixture is workspaceReadFixture's registry plus two operator-named
// folders resolved the way the daemon resolves them at startup.
type folderReadFixture struct {
	*workspaceReadFixture
	vault, notes string
	read         func(conversationID, path string) (relay.WorkspaceFile, bool)
}

func newFolderReadFixture(t *testing.T, maxBytes int64) *folderReadFixture {
	t.Helper()
	base := newWorkspaceReadFixture(t, maxBytes)
	root := filepath.Dir(base.ws)
	f := &folderReadFixture{
		workspaceReadFixture: base,
		vault:                filepath.Join(root, "vault"),
		notes:                filepath.Join(root, "notes"),
	}
	for _, d := range []string{f.vault, f.notes} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	folders := resolveReadFolders([]string{f.vault, f.notes}, nil)
	if len(folders) != 2 {
		t.Fatalf("resolveReadFolders kept %d of 2 folders", len(folders))
	}
	f.read = workspaceFileReader(base.reg, maxBytes, folders...)
	return f
}

// TestWorkspaceFileReader_ConfiguredFolders: an absolute path resolving inside
// any configured folder is served; a relative path still resolves against the
// workspace only; and the folders apply to a conversation with no workspace.
func TestWorkspaceFileReader_ConfiguredFolders(t *testing.T) {
	t.Parallel()
	f := newFolderReadFixture(t, maxAttachFileBytes)
	writeFile(t, f.ws, "ws.md", "# workspace")
	writeFile(t, f.vault, "daily/today.md", "# today")
	writeFile(t, f.notes, "idea.md", "# idea")
	mustSymlink(t, f.vault, filepath.Join(filepath.Dir(f.vault), "vault-link"))

	noWS, err := conversations.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	f.reg.Create(conversations.Conversation{ID: noWS})

	conv := string(f.convID)
	tests := []struct {
		name, conv, path, wantName, wantBody string
	}{
		{"workspace still served", conv, "ws.md", "ws.md", "# workspace"},
		{"absolute path in the first folder", conv, filepath.Join(f.vault, "daily", "today.md"), "today.md", "# today"},
		{"absolute path in the second folder", conv, filepath.Join(f.notes, "idea.md"), "idea.md", "# idea"},
		{"folder reached through a symlinked spelling", conv, filepath.Join(filepath.Dir(f.vault), "vault-link", "daily", "today.md"), "today.md", "# today"},
		{"empty workspace still reads a folder", string(noWS), filepath.Join(f.notes, "idea.md"), "idea.md", "# idea"},
	}
	for _, tt := range tests {
		got, ok := f.read(tt.conv, tt.path)
		if !ok {
			t.Errorf("%s: refused, want served", tt.name)
			continue
		}
		if string(got.Data) != tt.wantBody || got.Filename != tt.wantName {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tt.name, got.Filename, got.Data, tt.wantName, tt.wantBody)
		}
	}
}

// TestWorkspaceFileReader_ConfiguredFolderRefusals: every reader rule holds
// inside a configured folder, and failing every root is the same one false.
func TestWorkspaceFileReader_ConfiguredFolderRefusals(t *testing.T) {
	t.Parallel()
	f := newFolderReadFixture(t, 8)
	writeFile(t, f.vault, ".env", "SECRET=1")
	writeFile(t, f.vault, "only-here.md", "vault")
	writeFile(t, f.vault, "big.md", "123456789") // one byte over the bound of 8
	writeFile(t, f.outside, "escape.md", "outside")
	mustSymlink(t, ".env", filepath.Join(f.vault, "notes.md"))
	mustSymlink(t, filepath.Join(f.outside, "escape.md"), filepath.Join(f.vault, "out.md"))
	if err := os.MkdirAll(filepath.Join(f.vault, "dir.md"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(f.vault, "pipe.md"), 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	// A sibling sharing the folder's name as a prefix is not inside it.
	writeFile(t, f.vault+"-other", "x.md", "prefix sibling")

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
		{"relative path naming a file only in a folder", conv, "only-here.md"},
		{"relative path for a conversation with no workspace", string(emptyWS), "only-here.md"},
		{"absolute path outside the workspace and every folder", conv, filepath.Join(f.outside, "escape.md")},
		{"symlink in a folder pointing outside every root", conv, filepath.Join(f.vault, "out.md")},
		{"traversal out of a folder", conv, filepath.Join(f.vault, "..", "outside", "escape.md")},
		{"prefix sibling of a folder", conv, filepath.Join(f.vault+"-other", "x.md")},
		{"markdown symlink to .env in the same folder", conv, filepath.Join(f.vault, "notes.md")},
		{"wrong extension in a folder", conv, filepath.Join(f.vault, ".env")},
		{"directory in a folder", conv, filepath.Join(f.vault, "dir.md")},
		{"FIFO in a folder", conv, filepath.Join(f.vault, "pipe.md")},
		{"over the size bound in a folder", conv, filepath.Join(f.vault, "big.md")},
		{"missing file in a folder", conv, filepath.Join(f.vault, "absent.md")},
		{"unknown conversation", string(unknown), filepath.Join(f.vault, "only-here.md")},
	}
	for _, tt := range tests {
		if got, ok := f.read(tt.conv, tt.path); ok {
			t.Errorf("%s: served %q, want refused", tt.name, got.Data)
		}
	}
}

// TestResolveReadFolders: each entry is resolved once with the workspace's own
// recipe; an entry that is not absolute, does not resolve or is not a
// directory is skipped with exactly one warning, and the rest are kept in
// order.
func TestResolveReadFolders(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	file := writeFile(t, root, "file.md", "not a folder")
	link := filepath.Join(root, "link")
	mustSymlink(t, vault, link)
	canonicalVault, err := agentrun.ResolveWorkdir(vault)
	if err != nil {
		t.Fatalf("ResolveWorkdir: %v", err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	got := resolveReadFolders([]string{
		"relative/vault",
		filepath.Join(root, "missing"),
		file,
		link,
		vault,
	}, log)

	want := []string{canonicalVault, canonicalVault}
	if !slices.Equal(got, want) {
		t.Errorf("resolveReadFolders = %q, want %q", got, want)
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 3 {
		t.Errorf("logged %d warnings, want 3 (one per skipped entry):\n%s", n, buf.String())
	}
	if got := resolveReadFolders(nil, log); len(got) != 0 {
		t.Errorf("resolveReadFolders(nil) = %q, want empty", got)
	}
}

// TestWithWorkdirReadFolder: the daemon's working folder joins the read folders
// in its canonical spelling, once, unless it is the home folder or the root,
// in which case it is left out with exactly one startup line naming it.
func TestWithWorkdirReadFolder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	wd := filepath.Join(home, "pyry-workspace")
	vault := filepath.Join(root, "vault")
	for _, d := range []string{wd, vault} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	wdLink := filepath.Join(root, "wd-link")
	mustSymlink(t, wd, wdLink)
	homeLink := filepath.Join(root, "home-link")
	mustSymlink(t, home, homeLink)
	canonical := func(p string) string {
		t.Helper()
		r, err := agentrun.ResolveWorkdir(p)
		if err != nil {
			t.Fatalf("ResolveWorkdir: %v", err)
		}
		return r
	}
	canonicalWD, canonicalVault := canonical(wd), canonical(vault)

	tests := []struct {
		name          string
		folders       []string
		workdir, home string
		want          []string
		wantLog       bool
	}{
		{"added when nothing is configured", nil, wd, home, []string{canonicalWD}, false},
		{"added first beside a configured folder", []string{canonicalVault}, wd, home, []string{canonicalWD, canonicalVault}, false},
		{"canonicalised from a symlinked spelling", nil, wdLink, home, []string{canonicalWD}, false},
		{"not repeated when also configured", resolveReadFolders([]string{vault, wdLink}, nil), wd, home, []string{canonicalVault, canonicalWD}, false},
		{"home folder is not added", []string{canonicalVault}, home, home, []string{canonicalVault}, true},
		{"symlink to the home folder is not added", nil, homeLink, home, nil, true},
		{"home given by a symlinked spelling is still caught", nil, home, homeLink, nil, true},
		{"root is not added", nil, "/", home, nil, true},
		{"unresolvable home adds nothing", nil, wd, "", nil, true},
	}
	for _, tt := range tests {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		got := withWorkdirReadFolder(tt.folders, tt.workdir, tt.home, log)
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
		n := strings.Count(buf.String(), "working folder not made readable")
		if want := map[bool]int{true: 1, false: 0}[tt.wantLog]; n != want || strings.Count(buf.String(), "\n") != want {
			t.Errorf("%s: logged %q, want %d line(s) saying the folder was not made readable", tt.name, buf.String(), want)
		}
		if tt.wantLog && !strings.Contains(buf.String(), tt.workdir) {
			t.Errorf("%s: log %q does not name the folder %q", tt.name, buf.String(), tt.workdir)
		}
	}
}

// workdirReadFixture is the pyrybox layout: the daemon runs from wd, and the
// conversation's workspace is a subfolder of it, with no -pyry-read-folder.
type workdirReadFixture struct {
	reg             *conversations.Registry
	convID          conversations.ConversationID
	wd, ws, outside string
}

func newWorkdirReadFixture(t *testing.T) *workdirReadFixture {
	t.Helper()
	root := t.TempDir()
	reg, err := conversations.Load(filepath.Join(root, "conversations.json"))
	if err != nil {
		t.Fatalf("conversations.Load: %v", err)
	}
	f := &workdirReadFixture{
		reg:     reg,
		wd:      filepath.Join(root, "home", "pyry-workspace"),
		outside: filepath.Join(root, "outside"),
	}
	f.ws = filepath.Join(f.wd, "default")
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
	return f
}

// TestWorkspaceFileReader_WorkdirFolder: with no configured folders, a
// conversation in a subfolder of the working folder reads a markdown file in
// the working folder by absolute path, and every other reader rule still holds
// there: a markdown symlink leaving every root and a non-markdown file are the
// same one false.
func TestWorkspaceFileReader_WorkdirFolder(t *testing.T) {
	t.Parallel()
	f := newWorkdirReadFixture(t)
	writeFile(t, f.wd, "BEHAVIOR.md", "# behavior")
	writeFile(t, f.wd, "notes.txt", "not markdown")
	writeFile(t, f.outside, "secret.md", "outside")
	mustSymlink(t, filepath.Join(f.outside, "secret.md"), filepath.Join(f.wd, "escape.md"))

	folders := withWorkdirReadFolder(resolveReadFolders(nil, nil), f.wd, filepath.Dir(f.wd), nil)
	read := workspaceFileReader(f.reg, maxAttachFileBytes, folders...)
	conv := string(f.convID)

	got, ok := read(conv, filepath.Join(f.wd, "BEHAVIOR.md"))
	if !ok || string(got.Data) != "# behavior" || got.Filename != "BEHAVIOR.md" {
		t.Errorf("working-folder markdown: got (%q, %q, %v), want served", got.Filename, got.Data, ok)
	}
	if _, ok := workspaceFileReader(f.reg, maxAttachFileBytes)(conv, filepath.Join(f.wd, "BEHAVIOR.md")); ok {
		t.Error("without the working folder the reader served it; the fixture does not discriminate")
	}
	for name, path := range map[string]string{
		"markdown symlink to a file outside every root": filepath.Join(f.wd, "escape.md"),
		"non-markdown file in the working folder":       filepath.Join(f.wd, "notes.txt"),
	} {
		if got, ok := read(conv, path); ok {
			t.Errorf("%s: served %q, want refused", name, got.Data)
		}
	}
}

// TestFolderList: the flag value appends one entry per occurrence.
func TestFolderList(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	var folders folderList
	fs.Var(&folders, "pyry-read-folder", "")
	if err := fs.Parse([]string{"-pyry-read-folder", "/a", "-pyry-read-folder=/b"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !slices.Equal([]string(folders), []string{"/a", "/b"}) {
		t.Errorf("folders = %q, want [/a /b]", folders)
	}
	if !strings.Contains(helpText, "-pyry-read-folder") {
		t.Error("helpText does not list -pyry-read-folder")
	}
}
