package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

// installIdentityTrustMark overrides the package-level trustMark seam with one
// that returns its argument unchanged, and restores the production value at
// test exit.
//
// Identity rather than installRecordingTrustMark's fixed return, deliberately:
// these tests assert that the recorded Cwd and the derived name come from
// confineWorkdirToHomeCreating's REAL output. Feeding the expected realpath in
// as a canned trustMark return would make both assertions tautological — they
// would pass against a creator that ignored the resolved path entirely.
// Production trustMark returns the realpath it marked, so identity is also the
// faithful stand-in.
//
// Tests using it set $HOME via t.Setenv, so they cannot run parallel.
func installIdentityTrustMark(t *testing.T) *int {
	t.Helper()
	orig := trustMark
	t.Cleanup(func() { trustMark = orig })
	calls := 0
	trustMark = func(workdir string) (string, error) {
		calls++
		return workdir, nil
	}
	return &calls
}

// newChannelTestRegistry returns an empty registry plus the path it saves to.
func newChannelTestRegistry(t *testing.T, dir string) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(dir, "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("Load empty registry: %v", err)
	}
	return reg, path
}

// stubMint records what channelCreator asks the pool to mint and answers with a
// canned session id (or error).
type stubMint struct {
	gotLabel    string
	gotSpawnDir string
	calls       int
	returnID    string
	returnErr   error
}

func (s *stubMint) mint(label, spawnDir string) (string, error) {
	s.calls++
	s.gotLabel, s.gotSpawnDir = label, spawnDir
	if s.returnErr != nil {
		return "", s.returnErr
	}
	return s.returnID, nil
}

// readSavedConversations decodes the on-disk registry file. A missing file
// decodes as no conversations, which is what "nothing was persisted" looks
// like on the refusal paths.
func readSavedConversations(t *testing.T, path string) []conversations.Conversation {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var file struct {
		Conversations []conversations.Conversation `json:"conversations"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return file.Conversations
}

// TestChannelCreator_CreatesPromotedRow covers the ticket's first acceptance
// criterion end to end at the cmd layer: a directory under $HOME yields a
// promoted conversation whose Cwd is the RESOLVED real path, whose name is that
// path's base name, which carries a bound session id, and which is on disk by
// the time the call returns (the eager save).
//
// The realpath comparison is not incidental. On macOS t.TempDir() sits under
// /var/folders/…, and /var is a symlink, so the resolved path differs from the
// path handed in. An assertion written against the un-resolved dir passes on
// Linux and fails here — which is also exactly why the name has to be derived
// after confinement rather than before.
func TestChannelCreator_CreatesPromotedRow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installIdentityTrustMark(t)

	proj := filepath.Join(home, "my-project")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	wantCwd, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", proj, err)
	}

	reg, path := newChannelTestRegistry(t, home)
	minter := &stubMint{returnID: "11111111-2222-4333-8444-555555555555"}
	create := channelCreator(reg, minter.mint, path, nil, discardLogger())

	id, err := create(proj, "")
	if err != nil {
		t.Fatalf("create(%q, \"\") error = %v", proj, err)
	}
	if id == "" {
		t.Fatal("create returned an empty conversation id")
	}

	rows := reg.List()
	if len(rows) != 1 {
		t.Fatalf("registry holds %d rows, want 1", len(rows))
	}
	got := rows[0]
	if string(got.ID) != id {
		t.Errorf("row ID = %q, want the returned id %q", got.ID, id)
	}
	if !got.IsPromoted {
		t.Error("row IsPromoted = false, want true — this verb creates a channel, not a discussion")
	}
	if got.Cwd != wantCwd {
		t.Errorf("row Cwd = %q, want the resolved real path %q", got.Cwd, wantCwd)
	}
	if got.Name == nil || *got.Name != filepath.Base(wantCwd) {
		t.Errorf("row Name = %v, want the resolved path's base name %q", got.Name, filepath.Base(wantCwd))
	}
	if got.CurrentSessionID != minter.returnID {
		t.Errorf("row CurrentSessionID = %q, want the minted session %q", got.CurrentSessionID, minter.returnID)
	}
	if got.LastUsedAt.IsZero() {
		t.Error("row LastUsedAt is zero, want the creation timestamp")
	}

	// The minter must receive the ALREADY-RESOLVED path: it does not re-confine,
	// and the label is the conversation id (the session↔conversation breadcrumb).
	if minter.gotSpawnDir != wantCwd {
		t.Errorf("mint got spawnDir %q, want the resolved path %q", minter.gotSpawnDir, wantCwd)
	}
	if minter.gotLabel != id {
		t.Errorf("mint got label %q, want the conversation id %q", minter.gotLabel, id)
	}

	// Eager save: the row must survive a daemon restart, so it is on disk now
	// rather than at the next sweep tick.
	saved := readSavedConversations(t, path)
	if len(saved) != 1 || string(saved[0].ID) != id {
		t.Fatalf("on-disk registry = %+v, want the one row eagerly persisted", saved)
	}
	if saved[0].Cwd != wantCwd || !saved[0].IsPromoted {
		t.Errorf("persisted row = {Cwd:%q IsPromoted:%v}, want {%q true}", saved[0].Cwd, saved[0].IsPromoted, wantCwd)
	}
}

// TestChannelCreator_NameOverride pins that --name wins over the base-name
// default, and that an explicitly empty --name takes the default rather than
// producing a deliberately blank channel name.
func TestChannelCreator_NameOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installIdentityTrustMark(t)

	proj := filepath.Join(home, "my-project")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	tests := []struct {
		name     string
		supplied string
		want     string
	}{
		{name: "explicit label wins", supplied: "Backend Work", want: "Backend Work"},
		{name: "empty label takes the base name", supplied: "", want: filepath.Base(resolved)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, path := newChannelTestRegistry(t, t.TempDir())
			create := channelCreator(reg, (&stubMint{returnID: "sid"}).mint, path, nil, discardLogger())

			if _, err := create(proj, tt.supplied); err != nil {
				t.Fatalf("create error = %v", err)
			}
			rows := reg.List()
			if len(rows) != 1 {
				t.Fatalf("registry holds %d rows, want 1", len(rows))
			}
			if rows[0].Name == nil || *rows[0].Name != tt.want {
				t.Errorf("row Name = %v, want %q", rows[0].Name, tt.want)
			}
		})
	}
}

// TestChannelCreator_RefusesEscapingDir is the security core of this file. A
// directory that escapes $HOME after symlink resolution must be refused, the
// refusal must name neither the requested path, the resolved path, nor $HOME,
// and nothing may be left behind — no row in memory, no file on disk, no
// half-bound session.
func TestChannelCreator_RefusesEscapingDir(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir() // a sibling temp dir, deliberately not under home
	t.Setenv("HOME", home)
	installIdentityTrustMark(t)

	reg, path := newChannelTestRegistry(t, home)
	minter := &stubMint{returnID: "must-not-be-minted"}
	create := channelCreator(reg, minter.mint, path, nil, discardLogger())

	id, err := create(outside, "")
	if err == nil {
		t.Fatalf("create(%q) = (%q, nil), want a refusal", outside, id)
	}
	if id != "" {
		t.Errorf("create returned id %q alongside an error, want \"\"", id)
	}
	if err.Error() != msgChannelCwdRejected {
		t.Errorf("refusal = %q, want the static %q", err.Error(), msgChannelCwdRejected)
	}

	// The message must not leak a path. resolveSpawnDir's wrapped error embeds
	// both the resolved path and $HOME, so this is the assertion that catches a
	// future edit forwarding it instead of the constant.
	homeReal, err2 := filepath.EvalSymlinks(home)
	if err2 != nil {
		t.Fatalf("EvalSymlinks(home): %v", err2)
	}
	outsideReal, err2 := filepath.EvalSymlinks(outside)
	if err2 != nil {
		t.Fatalf("EvalSymlinks(outside): %v", err2)
	}
	for _, leak := range []string{outside, outsideReal, home, homeReal, filepath.Base(outside)} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("refusal %q leaks %q", err.Error(), leak)
		}
	}

	// Nothing persisted, nothing minted, no half-bound row.
	if rows := reg.List(); len(rows) != 0 {
		t.Errorf("registry holds %d rows after a refusal, want 0: %+v", len(rows), rows)
	}
	if saved := readSavedConversations(t, path); len(saved) != 0 {
		t.Errorf("on-disk registry holds %d rows after a refusal, want 0", len(saved))
	}
	if minter.calls != 0 {
		t.Errorf("mint called %d time(s) on the refusal path, want 0 — validation runs first", minter.calls)
	}
}

// TestChannelCreator_RefusesEmptyCwd pins the seam-side half of the two-sided
// empty-cwd guard. It is not redundant with handleChannelNew's: resolveSpawnDir
// reads "" as "spawn in the shared trusted workdir" and returns ("", nil) —
// success, having confined and trust-marked nothing — so without this guard an
// empty cwd reaching the creator would mint a channel rooted nowhere the caller
// named, with a Cwd of "" and a name of ".".
//
// Mutation check: deleting the guard makes this test red, and it fails on the
// row's existence rather than on the message, so it cannot pass vacuously.
func TestChannelCreator_RefusesEmptyCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	trustCalls := installIdentityTrustMark(t)

	reg, path := newChannelTestRegistry(t, home)
	minter := &stubMint{returnID: "must-not-be-minted"}
	create := channelCreator(reg, minter.mint, path, nil, discardLogger())

	if _, err := create("", ""); err == nil || err.Error() != msgChannelCwdRejected {
		t.Fatalf("create(\"\") error = %v, want the static %q", err, msgChannelCwdRejected)
	}
	if rows := reg.List(); len(rows) != 0 {
		t.Errorf("registry holds %d rows after an empty cwd, want 0: %+v", len(rows), rows)
	}
	if minter.calls != 0 {
		t.Errorf("mint called %d time(s) for an empty cwd, want 0", minter.calls)
	}
	if *trustCalls != 0 {
		t.Errorf("trustMark called %d time(s) for an empty cwd, want 0", *trustCalls)
	}
	if saved := readSavedConversations(t, path); len(saved) != 0 {
		t.Errorf("on-disk registry holds %d rows, want 0", len(saved))
	}
}

// TestChannelCreator_MintFailureLeavesNoRow pins that a mint failure returns
// the static session message and leaves no row — the ordering (mint before
// record) is what makes "no half-bound row" true.
func TestChannelCreator_MintFailureLeavesNoRow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installIdentityTrustMark(t)

	proj := filepath.Join(home, "my-project")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	reg, path := newChannelTestRegistry(t, home)
	minter := &stubMint{returnErr: errors.New("sessions: create supervisor: " + proj)}
	create := channelCreator(reg, minter.mint, path, nil, discardLogger())

	_, err := create(proj, "")
	if err == nil {
		t.Fatal("create succeeded, want the mint failure surfaced")
	}
	if err.Error() != msgChannelMintFailed {
		t.Errorf("refusal = %q, want the static %q", err.Error(), msgChannelMintFailed)
	}
	// The pool's own error text can name a path; the static message is what
	// keeps it off the wire.
	if strings.Contains(err.Error(), proj) {
		t.Errorf("refusal %q leaks the workdir from the pool's error", err.Error())
	}
	if rows := reg.List(); len(rows) != 0 {
		t.Errorf("registry holds %d rows after a mint failure, want 0", len(rows))
	}
}

// TestChannelCreator_DuplicatesPermitted pins that running the verb twice in
// one directory yields two rows sharing a name and a cwd. Registry.Create is a
// bare append and the wire create_conversation has the same property, so a
// uniqueness guard here would make this verb's rows distinguishable from a
// client's — which the ticket forbids.
func TestChannelCreator_DuplicatesPermitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installIdentityTrustMark(t)

	proj := filepath.Join(home, "my-project")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	reg, path := newChannelTestRegistry(t, home)
	create := channelCreator(reg, (&stubMint{returnID: "sid"}).mint, path, nil, discardLogger())

	first, err := create(proj, "")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := create(proj, "")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first == second {
		t.Errorf("both creates returned id %q, want distinct server-minted ids", first)
	}
	rows := reg.List()
	if len(rows) != 2 {
		t.Fatalf("registry holds %d rows, want 2 (duplicates are permitted by construction)", len(rows))
	}
	if rows[0].Cwd != rows[1].Cwd {
		t.Errorf("rows have different Cwds (%q, %q), want the same directory", rows[0].Cwd, rows[1].Cwd)
	}
}

// TestChannelCreator_WrapsSpawnDirSentinel pins that the classification between
// "not allowed" and "could not prepare" is made with errors.Is against
// handlers.ErrSpawnDirRejected rather than by matching message text, so it
// survives future rewording upstream. A trustMark write failure is not a
// verdict on the directory and must not be reported as one.
func TestChannelCreator_WrapsSpawnDirSentinel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	proj := filepath.Join(home, "my-project")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	// A trustMark failure: confinement PASSED, so resolveSpawnDir returns a
	// plain error that does not wrap the sentinel.
	orig := trustMark
	t.Cleanup(func() { trustMark = orig })
	trustMark = func(string) (string, error) { return "", errors.New("write ~/.claude.json: disk full") }

	reg, path := newChannelTestRegistry(t, home)
	create := channelCreator(reg, (&stubMint{returnID: "sid"}).mint, path, nil, discardLogger())

	_, err := create(proj, "")
	if err == nil {
		t.Fatal("create succeeded, want the trust-mark failure surfaced")
	}
	if err.Error() != msgChannelWorkspaceFailed {
		t.Errorf("refusal = %q, want %q — a write failure is not a verdict on the directory",
			err.Error(), msgChannelWorkspaceFailed)
	}
	// Control: the sentinel path really does classify differently.
	if errors.Is(errors.New(msgChannelWorkspaceFailed), handlers.ErrSpawnDirRejected) {
		t.Fatal("the static messages are indistinguishable by errors.Is — this test proves nothing")
	}
	if rows := reg.List(); len(rows) != 0 {
		t.Errorf("registry holds %d rows after a trust-mark failure, want 0", len(rows))
	}
}

// recordingAnnouncer records every record channelCreator announces (#2156).
// A slice rather than a single value so "announced exactly once" is checkable —
// a second push on the same create would be a client drawing the channel twice.
type recordingAnnouncer struct {
	calls []protocol.ConversationUpdatedPayload
}

func (r *recordingAnnouncer) announce(p protocol.ConversationUpdatedPayload) {
	r.calls = append(r.calls, p)
}

// #2210 AC-1 + AC-2 for the seventh producer: the unsolicited create-announce
// push carries workspace_label, holding the label stored for the announced row's
// own cwd. This is the one producer outside internal/relay/handlers, and the
// only one that is not a reply, so nothing in that package's coverage reaches it.
//
// The label is stored under the RESOLVED path and a decoy under the raw one,
// which is the same distinction TestChannelCreator_AnnouncesStoredRow draws for
// Cwd: a lookup keyed off the caller's unvalidated input would find the decoy.
// On macOS the two genuinely differ (t.TempDir() sits under the /var symlink);
// where they coincide the decoy is simply the same key and the assertion falls
// back to the plain positive case.
//
// The absent case is asserted in the same test rather than in a second one: it
// is the same announce path with nothing stored, and pairing them here keeps the
// "explicit value, never a stale one" contract visible in one place.
func TestChannelCreator_AnnouncesWorkspaceLabel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installIdentityTrustMark(t)

	proj := filepath.Join(home, "labelled-project")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	wantCwd, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", proj, err)
	}

	// An unlabelled workspace announces an explicit absence, not a fallback.
	reg, path := newChannelTestRegistry(t, home)
	rec := &recordingAnnouncer{}
	create := channelCreator(reg, (&stubMint{returnID: "sid"}).mint, path, rec.announce, discardLogger())
	if _, err := create(proj, ""); err != nil {
		t.Fatalf("create error = %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("announced %d times, want exactly 1", len(rec.calls))
	}
	if got := rec.calls[0].WorkspaceLabel; got != nil {
		t.Errorf("announced workspace_label = pointer to %q, want nil for an unlabelled workspace", *got)
	}

	// Now name the workspace and create a second channel in it.
	const wantLabel = "Announced workspace"
	const decoy = "keyed off the caller's raw path — never correct"
	label := wantLabel
	decoyLabel := decoy
	reg.SetWorkspaceLabel(wantCwd, &label)
	if proj != wantCwd {
		reg.SetWorkspaceLabel(proj, &decoyLabel)
	}

	if _, err := create(proj, ""); err != nil {
		t.Fatalf("second create error = %v", err)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("announced %d times after the second create, want 2", len(rec.calls))
	}
	got := rec.calls[1]
	if got.Cwd != wantCwd {
		t.Fatalf("announced cwd = %q, want the row's resolved %q", got.Cwd, wantCwd)
	}
	switch {
	case got.WorkspaceLabel == nil:
		t.Errorf("announced workspace_label = nil, want pointer to %q", wantLabel)
	case *got.WorkspaceLabel == decoy:
		t.Errorf("announced workspace_label = %q — the lookup keyed off the caller's raw "+
			"path instead of the stored, resolved cwd %q", decoy, wantCwd)
	case *got.WorkspaceLabel != wantLabel:
		t.Errorf("announced workspace_label = %q, want %q", *got.WorkspaceLabel, wantLabel)
	}
}

// AC-1: a successful create announces the STORED row, once.
//
// The Cwd assertion is what separates a read-back from a payload assembled out
// of the request, and it is not decoration. On macOS t.TempDir() sits under
// /var/folders/…, and /var is a symlink, so the resolved path the row records
// differs from the path handed in — a payload built from the caller's raw cwd
// fails here. (On a filesystem where the two coincide the assertion is merely
// weaker, the same limitation TestChannelCreator_CreatesPromotedRow carries.)
// It matters beyond accuracy: the caller's cwd is unvalidated, and what
// resolveSpawnDir confined is what the row holds.
func TestChannelCreator_AnnouncesStoredRow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installIdentityTrustMark(t)

	proj := filepath.Join(home, "announced-project")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	wantCwd, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", proj, err)
	}

	reg, path := newChannelTestRegistry(t, home)
	rec := &recordingAnnouncer{}
	create := channelCreator(reg, (&stubMint{returnID: "sid"}).mint, path, rec.announce, discardLogger())

	id, err := create(proj, "")
	if err != nil {
		t.Fatalf("create error = %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("announced %d times, want exactly 1", len(rec.calls))
	}
	got := rec.calls[0]
	if got.ID != id {
		t.Errorf("announced id = %q, want the created %q", got.ID, id)
	}
	if got.Cwd != wantCwd {
		t.Errorf("announced cwd = %q, want the row's resolved %q — the record is read back from "+
			"the registry, never assembled from the request's raw %q", got.Cwd, wantCwd, proj)
	}
	if got.Name == nil || *got.Name != filepath.Base(wantCwd) {
		t.Errorf("announced name = %v, want %q", got.Name, filepath.Base(wantCwd))
	}
	if !got.IsPromoted {
		t.Errorf("announced is_promoted = false; a channel is a promoted conversation")
	}
	if got.IsArchived {
		t.Errorf("announced is_archived = true, want false on a freshly created row")
	}

	// The announced record must agree with what is actually stored, field for
	// field — the property "carries the stored row" rather than "carries a
	// plausible row".
	rows := reg.List()
	if len(rows) != 1 {
		t.Fatalf("registry holds %d rows, want 1", len(rows))
	}
	if !got.LastUsedAt.Equal(rows[0].LastUsedAt) {
		t.Errorf("announced last_used_at = %v, want the stored %v", got.LastUsedAt, rows[0].LastUsedAt)
	}
	if got.Cwd != rows[0].Cwd || got.IsPromoted != rows[0].IsPromoted || got.IsArchived != rows[0].IsArchived {
		t.Errorf("announced record %+v disagrees with the stored row %+v", got, rows[0])
	}
}

// AC-3: with no relay leg the hook is nil and the create succeeds silently.
// This is the daemon startRelay's no-URL early return produces, so it is the
// production shape rather than a defensive case.
func TestChannelCreator_NilAnnounceStillCreates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installIdentityTrustMark(t)

	proj := filepath.Join(home, "relayless")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	reg, path := newChannelTestRegistry(t, home)
	create := channelCreator(reg, (&stubMint{returnID: "sid"}).mint, path, nil, discardLogger())

	id, err := create(proj, "")
	if err != nil {
		t.Fatalf("create with a nil announce hook error = %v, want success", err)
	}
	if id == "" {
		t.Fatalf("create returned an empty id alongside a nil error")
	}
	if rows := reg.List(); len(rows) != 1 {
		t.Fatalf("registry holds %d rows, want 1 — a nil hook must not change what is stored", len(rows))
	}
}

// Every refusal path announces nothing: the announcement sits on the success
// path, after every return above it. A client must not be told about a channel
// that was never created.
func TestChannelCreator_RefusalsAnnounceNothing(t *testing.T) {
	outside := t.TempDir() // a sibling temp dir, deliberately not under home

	tests := []struct {
		name     string
		cwd      string
		mintFail bool
	}{
		{name: "empty cwd", cwd: ""},
		{name: "directory escaping $HOME", cwd: outside},
		{name: "session mint failure", mintFail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			installIdentityTrustMark(t)

			cwd := tt.cwd
			minter := &stubMint{returnID: "sid"}
			if tt.mintFail {
				// A directory that PASSES confinement, so the run reaches the mint
				// and fails there rather than being turned away earlier.
				cwd = filepath.Join(home, "mint-fails")
				if err := os.MkdirAll(cwd, 0o700); err != nil {
					t.Fatalf("mkdir project: %v", err)
				}
				minter = &stubMint{returnErr: errors.New("pool down")}
			}

			reg, path := newChannelTestRegistry(t, home)
			rec := &recordingAnnouncer{}
			create := channelCreator(reg, minter.mint, path, rec.announce, discardLogger())

			if _, err := create(cwd, ""); err == nil {
				t.Fatalf("create(%q) succeeded, want a refusal", cwd)
			}
			if len(rec.calls) != 0 {
				t.Errorf("a refused create announced %d record(s): %+v", len(rec.calls), rec.calls)
			}
		})
	}
}

// TestParseChannelNewArgs covers the flag-parse + arity rules without dialling
// the control socket.
func TestParseChannelNewArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantName string
		wantErr  bool
	}{
		{name: "bare", args: nil, wantName: ""},
		{name: "empty slice", args: []string{}, wantName: ""},
		{name: "name flag", args: []string{"--name", "Backend"}, wantName: "Backend"},
		{name: "name flag single dash", args: []string{"-name", "Backend"}, wantName: "Backend"},
		{name: "name flag glued", args: []string{"--name=Backend Work"}, wantName: "Backend Work"},
		{name: "explicitly empty name", args: []string{"--name="}, wantName: ""},
		{name: "stray positional", args: []string{"extra"}, wantErr: true},
		{name: "positional after flag", args: []string{"--name", "x", "extra"}, wantErr: true},
		{name: "unknown flag", args: []string{"--nope"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseChannelNewArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseChannelNewArgs(%q) = (%q, nil), want an error", tt.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseChannelNewArgs(%q) error = %v", tt.args, err)
			}
			if got != tt.wantName {
				t.Errorf("parseChannelNewArgs(%q) = %q, want %q", tt.args, got, tt.wantName)
			}
		})
	}
}

// TestChannelNewVerdict pins the pure formatter: success is silent, every
// failure is one line at exit 1 under the verb's own prefix.
func TestChannelNewVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantCode int
		wantLine string
	}{
		{name: "success", err: nil, wantCode: 0, wantLine: ""},
		{
			name:     "daemon refusal",
			err:      errors.New("channel.new: " + msgChannelCwdRejected),
			wantCode: 1,
			wantLine: "pyry channel new: channel.new: working directory not allowed",
		},
		{
			name:     "seam not configured",
			err:      errors.New("channel.new: no channel creator configured"),
			wantCode: 1,
			wantLine: "pyry channel new: channel.new: no channel creator configured",
		},
		{
			name:     "transport failure",
			err:      errors.New("control: dial /tmp/p.sock: connect: no such file or directory"),
			wantCode: 1,
			wantLine: "pyry channel new: control: dial /tmp/p.sock: connect: no such file or directory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, line := channelVerdict("new", tt.err)
			if code != tt.wantCode || line != tt.wantLine {
				t.Errorf("channelVerdict(\"new\", %v) = (%d, %q), want (%d, %q)",
					tt.err, code, line, tt.wantCode, tt.wantLine)
			}
		})
	}
}
