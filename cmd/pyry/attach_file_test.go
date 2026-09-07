package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/pyrycode/pyrycode/internal/attachments"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// attachFixture is one daemon's worth of the state fileAttacher reads: an
// instance directory to file beneath, a conversation registry, and a set of
// live session ids. Built per test rather than shared, so a test that mutates
// the registry cannot leak into a sibling.
type attachFixture struct {
	t           *testing.T
	instanceDir string
	reg         *conversations.Registry
	live        map[sessions.SessionID]bool
	attach      func(sessionID, path string) (string, error)

	// announced records every announcement the attacher made, in order. Guarded
	// because the hook fires on whatever goroutine called attach, and the
	// production seam is reached concurrently (control.Server.Serve accepts each
	// conn onto its own goroutine) — so a fixture that assumed one goroutine
	// would be modelling something the daemon does not do.
	mu        sync.Mutex
	announced []attachAnnouncement
}

// attachAnnouncement is one call of the announce hook: the three values the
// attachment_offered frame carries.
type attachAnnouncement struct {
	conversationID string
	attachmentID   string
	filename       string
}

// newAttachFixture wires a fileAttacher over a fresh registry holding no
// conversations and no live sessions, with a RECORDING announce hook. Callers
// add what they need with bindConversation.
func newAttachFixture(t *testing.T) *attachFixture {
	t.Helper()
	return newAttachFixtureWithAnnounce(t, true)
}

// newAttachFixtureWithoutAnnounce wires the same attacher with a NIL announce
// hook — the daemon that has no relay leg, where startRelay returned early
// before any manager existed. It must still store and still mint.
func newAttachFixtureWithoutAnnounce(t *testing.T) *attachFixture {
	t.Helper()
	return newAttachFixtureWithAnnounce(t, false)
}

func newAttachFixtureWithAnnounce(t *testing.T, hook bool) *attachFixture {
	t.Helper()
	root := t.TempDir()
	reg, err := conversations.Load(filepath.Join(root, "conversations.json"))
	if err != nil {
		t.Fatalf("conversations.Load: %v", err)
	}
	f := &attachFixture{
		t:           t,
		instanceDir: filepath.Join(root, "instance"),
		reg:         reg,
		live:        map[sessions.SessionID]bool{},
	}
	var announce func(conversationID, attachmentID, filename string)
	if hook {
		announce = func(conversationID, attachmentID, filename string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.announced = append(f.announced, attachAnnouncement{
				conversationID: conversationID,
				attachmentID:   attachmentID,
				filename:       filename,
			})
		}
	}
	f.attach = fileAttacher(reg, func(id sessions.SessionID) error {
		if f.live[id] {
			return nil
		}
		return sessions.ErrSessionNotFound
	}, f.instanceDir, announce, nil)
	return f
}

// offers returns a copy of what the announce hook observed.
func (f *attachFixture) offers() []attachAnnouncement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.announced)
}

// bindConversation registers a conversation whose recorded workspace is a
// fresh directory, binds sessionID to it, and marks that session live. Returns
// the conversation id and the workspace path.
func (f *attachFixture) bindConversation(sessionID, cwd string) conversations.ConversationID {
	f.t.Helper()
	id, err := conversations.NewID()
	if err != nil {
		f.t.Fatalf("NewID: %v", err)
	}
	f.reg.Create(conversations.Conversation{ID: id, Cwd: cwd, CurrentSessionID: sessionID})
	f.live[sessions.SessionID(sessionID)] = true
	return id
}

// writeFile creates a file with known contents and returns its path.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// assertContentFree is the § Attachments logging rule as an assertion: a
// refusal reason must name no host path, no filename and no workspace. It is
// applied to EVERY refusal in this file rather than to a representative one,
// because the ban is per-message and a single leaky branch is the whole
// exposure.
func assertContentFree(t *testing.T, reason string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(reason, s) {
			t.Errorf("refusal reason %q names %q; reasons must be static and content-free", reason, s)
		}
	}
}

// TestFileAttacher_Confinement is AC-2's single table. Every row runs against
// one workspace and asserts the stated outcome.
//
// The two symlink rows are a deliberate discriminating PAIR. A check that
// merely rejected paths containing a symlink, or that tested the string's
// shape, would pass the "target outside" row and fail the "final component
// resolving inside" row; only a check that runs on the RESOLVED path gets both
// right. Likewise the prefix-sibling row: a strings.HasPrefix containment test
// admits it, and a filepath.Rel one refuses it (the #118/#221 gotcha).
func TestFileAttacher_Confinement(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	ws := filepath.Join(parent, "ws")
	outside := filepath.Join(parent, "outside")
	sibling := filepath.Join(parent, "ws-evil")
	for _, d := range []string{ws, outside, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	writeFile(t, ws, "inside.md", "inside")
	writeFile(t, ws, "nested/deep.md", "deep")
	writeFile(t, ws, "real.md", "real")
	writeFile(t, outside, "secret.md", "secret")
	writeFile(t, sibling, "evil.md", "evil")
	if err := os.MkdirAll(filepath.Join(ws, "subdir"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(ws, "escape")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(ws, "real.md"), filepath.Join(ws, "alias")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	fifo := filepath.Join(ws, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	cases := []struct {
		name   string
		path   string
		accept bool
	}{
		{name: "relative path inside the tree", path: "nested/deep.md", accept: true},
		{name: "absolute path inside the tree", path: filepath.Join(ws, "inside.md"), accept: true},
		{name: "absolute path outside the tree", path: filepath.Join(outside, "secret.md")},
		{name: "relative traversal out of the tree", path: "../outside/secret.md"},
		{name: "symlink whose target is outside the tree", path: filepath.Join(ws, "escape")},
		{name: "symlink as the final component, target inside", path: filepath.Join(ws, "alias"), accept: true},
		{name: "sibling directory sharing the root's prefix", path: filepath.Join(sibling, "evil.md")},
		{name: "a directory", path: filepath.Join(ws, "subdir")},
		{name: "a non-regular file (FIFO)", path: fifo},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newAttachFixture(t)
			const sid = "11111111-2222-4333-8444-555555555555"
			convID := f.bindConversation(sid, ws)

			id, err := f.attach(sid, tc.path)
			if !tc.accept {
				if err == nil {
					t.Fatalf("attach(%q) = %q, want a refusal", tc.path, id)
				}
				assertContentFree(t, err.Error(), tc.path, ws, filepath.Base(tc.path))
				return
			}
			if err != nil {
				t.Fatalf("attach(%q): %v", tc.path, err)
			}
			if !conversations.ValidID(id) {
				t.Errorf("minted id %q is not a canonical UUIDv4", id)
			}
			// Readable back through the exact primitive #2054 serves with,
			// which is what makes the file retrievable with no special-casing.
			dir, err := attachments.ResolvePath(f.instanceDir, convID, id)
			if err != nil {
				t.Fatalf("ResolvePath: %v", err)
			}
			if _, err := os.Stat(dir); err != nil {
				t.Errorf("stored attachment not readable back: %v", err)
			}
		})
	}
}

// TestFileAttacher_StoresTheBytes pins that the accepted path's CONTENT is what
// lands, under the caller's conversation, addressed by the minted id. The
// confinement table asserts the file exists; this asserts it is the right file.
func TestFileAttacher_StoresTheBytes(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	writeFile(t, ws, "report.md", "the bytes claude wrote")

	f := newAttachFixture(t)
	const sid = "11111111-2222-4333-8444-555555555555"
	convID := f.bindConversation(sid, ws)

	id, err := f.attach(sid, "report.md")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	path, err := attachments.ResolvePath(f.instanceDir, convID, id)
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "the bytes claude wrote" {
		t.Errorf("stored bytes = %q, want the source file's contents", got)
	}
	// The claude-authored name survives as ONE sanitised path component, never
	// as a directory — which is what keeps ResolvePath's leaf resolvable.
	if filepath.Base(path) != "report.md" {
		t.Errorf("stored leaf = %q, want the sanitised source filename", filepath.Base(path))
	}
}

// TestFileAttacher_DestinationIsCallerSession is AC-4. Two live sessions are
// bound to two conversations with different workspaces; the call names the
// SECOND. The bytes must land under the second conversation and nothing under
// the first.
//
// The follow-active cursor is not consulted anywhere in the design — there is
// no cursor in this package's reach — so the force of this test is that the
// destination tracks the NAMED session rather than any ambient one. It also
// pins the cross-workspace half: session A's workspace must not be a usable
// root for session B's call.
func TestFileAttacher_DestinationIsCallerSession(t *testing.T) {
	t.Parallel()

	wsA, wsB := t.TempDir(), t.TempDir()
	writeFile(t, wsA, "a.md", "conversation A's file")
	writeFile(t, wsB, "b.md", "conversation B's file")

	f := newAttachFixture(t)
	const sidA = "11111111-2222-4333-8444-555555555555"
	const sidB = "22222222-3333-4555-9666-777777777777"
	convA := f.bindConversation(sidA, wsA)
	convB := f.bindConversation(sidB, wsB)

	id, err := f.attach(sidB, "b.md")
	if err != nil {
		t.Fatalf("attach as session B: %v", err)
	}
	if _, err := attachments.ResolvePath(f.instanceDir, convB, id); err != nil {
		t.Errorf("bytes did not land under the CALLING session's conversation: %v", err)
	}
	if _, err := attachments.ResolvePath(f.instanceDir, convA, id); err == nil {
		t.Error("bytes also resolve under conversation A; the destination must be the caller's alone")
	}

	// Session B naming a file inside A's workspace is an escape from B's root,
	// even though the path is inside SOME conversation's workspace.
	if _, err := f.attach(sidB, filepath.Join(wsA, "a.md")); err == nil {
		t.Error("session B filed a path from A's workspace; confinement is per-conversation")
	}
}

// TestFileAttacher_Refusals covers the destination-resolution branches. Each
// asserts a refusal AND that the reason is content-free.
func TestFileAttacher_Refusals(t *testing.T) {
	t.Parallel()

	const boundSID = "11111111-2222-4333-8444-555555555555"
	const unboundSID = "22222222-3333-4555-9666-777777777777"
	const unknownSID = "33333333-4444-4555-8666-777777777777"

	cases := []struct {
		name  string
		setup func(t *testing.T, f *attachFixture) (sessionID, path string)
	}{
		{
			// The empty id is the one that must never be defaulted:
			// Pool.Lookup("") resolves to the bootstrap session, and a scan
			// keyed on CurrentSessionID would match an UNBOUND conversation.
			name: "empty session id",
			setup: func(t *testing.T, f *attachFixture) (string, string) {
				ws := t.TempDir()
				f.bindConversation(boundSID, ws)
				// A conversation bound to nothing — what an empty id would match.
				f.bindConversation("", ws)
				return "", writeFile(t, ws, "x.md", "x")
			},
		},
		{
			name: "session is not live",
			setup: func(t *testing.T, f *attachFixture) (string, string) {
				ws := t.TempDir()
				f.bindConversation(boundSID, ws)
				return unknownSID, writeFile(t, ws, "x.md", "x")
			},
		},
		{
			name: "live session bound to no conversation",
			setup: func(t *testing.T, f *attachFixture) (string, string) {
				ws := t.TempDir()
				f.bindConversation(boundSID, ws)
				f.live[sessions.SessionID(unboundSID)] = true // live, but no row points at it
				return unboundSID, writeFile(t, ws, "x.md", "x")
			},
		},
		{
			name: "conversation with an empty recorded workspace",
			setup: func(t *testing.T, f *attachFixture) (string, string) {
				ws := t.TempDir()
				f.bindConversation(boundSID, "")
				return boundSID, writeFile(t, ws, "x.md", "x")
			},
		},
		{
			name: "no such file inside the workspace",
			setup: func(t *testing.T, f *attachFixture) (string, string) {
				ws := t.TempDir()
				f.bindConversation(boundSID, ws)
				return boundSID, "absent.md"
			},
		},
		{
			// Sparse, so the 16 MiB is a Stat-visible size and not 16 MiB of
			// writing: the bound is read from Stat BEFORE any byte is read,
			// which is exactly what this row pins.
			name: "file above the byte bound",
			setup: func(t *testing.T, f *attachFixture) (string, string) {
				ws := t.TempDir()
				f.bindConversation(boundSID, ws)
				path := filepath.Join(ws, "huge.bin")
				fh, err := os.Create(path)
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if err := fh.Truncate(maxAttachFileBytes + 1); err != nil {
					t.Fatalf("Truncate: %v", err)
				}
				if err := fh.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				return boundSID, path
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newAttachFixture(t)
			sid, path := tc.setup(t, f)
			id, err := f.attach(sid, path)
			if err == nil {
				t.Fatalf("attach = %q, want a refusal", id)
			}
			assertContentFree(t, err.Error(), path, filepath.Base(path))
			// A refused store announces nothing. Asserted on EVERY row rather
			// than a representative one, for assertContentFree's reason: the
			// property is per-branch, and one branch that announced a file it
			// did not store would tell clients to fetch bytes that are not
			// there.
			if got := f.offers(); len(got) != 0 {
				t.Errorf("a refused store announced %d offer(s) (%+v); nothing is announced "+
					"unless the file was stored", len(got), got)
			}
		})
	}
}

// TestFileAttacher_AnnouncesTheStoredName is AC-1's pin. A successful store
// announces once, naming the conversation it resolved, the id it minted, and —
// the part that has to be got right — the name the file was actually STORED
// under rather than the name it had on the host.
//
// The two rows are a discriminating PAIR, and the second is the whole point.
// Announcing filepath.Base of the RESOLVED source path passes the first row and
// fails the second, because attachments.Store sanitises the claude-authored name
// before writing it: a space is not in SanitizeFilename's allowlist and becomes
// an underscore. Only taking the leaf of the path Store RETURNED gets both
// right, which is what makes the announced name and the name the retrieval leg
// publishes one string rather than two derivations of one.
//
// The expectation is read off the FILESYSTEM — the single file present in the
// attachment directory — rather than recomputed from the input, so a wrong
// derivation reddens instead of being reproduced on both sides of the assertion.
func TestFileAttacher_AnnouncesTheStoredName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hostName string
	}{
		{
			name:     "a name sanitisation leaves alone",
			hostName: "report.md",
		},
		{
			name:     "a name sanitisation rewrites",
			hostName: "quarterly summary v2.md",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ws := t.TempDir()
			writeFile(t, ws, tc.hostName, "the bytes claude wrote")

			f := newAttachFixture(t)
			const sid = "11111111-2222-4333-8444-555555555555"
			convID := f.bindConversation(sid, ws)

			id, err := f.attach(sid, tc.hostName)
			if err != nil {
				t.Fatalf("attach: %v", err)
			}

			stored, err := attachments.ResolvePath(f.instanceDir, convID, id)
			if err != nil {
				t.Fatalf("ResolvePath: %v", err)
			}
			wantName := filepath.Base(stored)

			got := f.offers()
			if len(got) != 1 {
				t.Fatalf("a successful store announced %d offer(s), want exactly 1", len(got))
			}
			if got[0].conversationID != string(convID) {
				t.Errorf("announced conversation_id = %q, want the conversation the CALLING "+
					"session resolved to (%q)", got[0].conversationID, convID)
			}
			if got[0].attachmentID != id {
				t.Errorf("announced attachment_id = %q, want the id the verb minted and "+
					"returned (%q)", got[0].attachmentID, id)
			}
			if got[0].filename != wantName {
				t.Errorf("announced filename = %q, want %q — the leaf of the path Store wrote, "+
					"which is the same string the retrieval leg publishes for this id; "+
					"the host file was named %q", got[0].filename, wantName, tc.hostName)
			}
		})
	}
}

// TestFileAttacher_NoAnnounceHookStillStores is AC-2. The hook is absent exactly
// when the relay leg is — startRelay returns before building a manager when no
// URL is configured — and that daemon must file the bytes and answer with a
// minted id rather than refuse. The nil-tolerance SetApprovalSurfacer(nil)
// already has, not the always-installed approval registry's shape.
func TestFileAttacher_NoAnnounceHookStillStores(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	writeFile(t, ws, "note.md", "no relay leg here")

	f := newAttachFixtureWithoutAnnounce(t)
	const sid = "11111111-2222-4333-8444-555555555555"
	convID := f.bindConversation(sid, ws)

	id, err := f.attach(sid, "note.md")
	if err != nil {
		t.Fatalf("attach with no announce hook: %v", err)
	}
	if !conversations.ValidID(id) {
		t.Errorf("id %q is not a canonical lowercase UUIDv4", id)
	}
	path, err := attachments.ResolvePath(f.instanceDir, convID, id)
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "no relay leg here" {
		t.Errorf("stored bytes = %q, want the source file's contents", got)
	}
}

// TestFileAttacher_MintedID pins that the id is daemon-minted rather than
// derived from anything the caller sent: it obeys the published attachment_id
// shape, and two calls naming the SAME file answer with different ids.
func TestFileAttacher_MintedID(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	writeFile(t, ws, "note.md", "note")

	f := newAttachFixture(t)
	const sid = "11111111-2222-4333-8444-555555555555"
	f.bindConversation(sid, ws)

	first, err := f.attach(sid, "note.md")
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	second, err := f.attach(sid, "note.md")
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	for _, id := range []string{first, second} {
		if !conversations.ValidID(id) {
			t.Errorf("id %q is not a canonical lowercase UUIDv4", id)
		}
	}
	if first == second {
		t.Errorf("both calls minted %q; each filing is its own attachment", first)
	}
}

// TestConfineFile_SwapBetweenCheckAndRead is AC-3. The check and the read are
// separate functions, so the swap happens deterministically BETWEEN them
// rather than being raced for.
//
// The swap renames a PRE-EXISTING second file over the checked path. That
// matters: a create-after-delete could be handed the freed inode back, which
// would make os.SameFile agree and the assertion vacuous. A file that was
// already live cannot share the victim's inode.
func TestConfineFile_SwapBetweenCheckAndRead(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	victim := writeFile(t, ws, "victim.md", "the file that passed the check")
	decoy := writeFile(t, ws, "decoy.md", "the file swapped in behind it")

	resolved, checked, err := confineFile(ws, victim)
	if err != nil {
		t.Fatalf("confineFile: %v", err)
	}

	// Control arm first: with no swap, the same pair reads normally. Without
	// this the refusal below would not distinguish "the guard fired" from
	// "the helper never works".
	body, err := readChecked(resolved, checked, maxAttachFileBytes)
	if err != nil {
		t.Fatalf("readChecked with no swap: %v", err)
	}
	if string(body) != "the file that passed the check" {
		t.Fatalf("readChecked returned %q, want the checked file's contents", body)
	}

	if err := os.Rename(decoy, victim); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	swapped, err := readChecked(resolved, checked, maxAttachFileBytes)
	if err == nil {
		t.Fatalf("readChecked after the swap returned %q, want a refusal", swapped)
	}
	assertContentFree(t, err.Error(), victim, decoy, ws, "victim.md", "decoy.md")
}

// TestReadChecked_ByteBound pins the bound as a parameter rather than only as
// the wired constant, so the two rungs — the Stat check and the read cap — can
// both be exercised without writing 16 MiB.
func TestReadChecked_ByteBound(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	path := writeFile(t, ws, "ten.md", "0123456789")
	resolved, checked, err := confineFile(ws, path)
	if err != nil {
		t.Fatalf("confineFile: %v", err)
	}

	if _, err := readChecked(resolved, checked, 9); err == nil {
		t.Error("readChecked with a bound below the file size = nil error, want a refusal")
	}
	body, err := readChecked(resolved, checked, 10)
	if err != nil {
		t.Fatalf("readChecked at exactly the bound: %v", err)
	}
	if string(body) != "0123456789" {
		t.Errorf("body = %q, want the whole file at exactly the bound", body)
	}
}
