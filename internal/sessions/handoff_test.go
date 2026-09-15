package sessions

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// --- helpers -----------------------------------------------------------------

// handoffConvID is a canonical conversation id the tests key notes on. Written
// out rather than drawn from conversations.NewID so the on-disk filename each
// test asserts is a constant a reader can check by eye.
const handoffConvID = conversations.ConversationID("6f1c2b4a-9d3e-4a17-b8c5-0e2d7a41f9b3")

// handoffNoteDirOf returns the handoff-note directory for a data dir, the shape
// sessionPromptDirOf and settingsDirOf have.
func handoffNoteDirOf(dataDir string) string {
	return filepath.Join(dataDir, "handoff-notes")
}

// handoffNotePathOf returns the handoff-note path for a data dir and id.
func handoffNotePathOf(dataDir string, id conversations.ConversationID) string {
	return filepath.Join(handoffNoteDirOf(dataDir), string(id)+".txt")
}

// handoffPool builds a pool over registryPath with no child and no lifecycle
// goroutine — the store touches no session state, so New's on-disk effects are
// all these tests need. A nil factory would fail New's mandatory-factory check.
func handoffPool(t *testing.T, registryPath string) *Pool {
	t.Helper()
	pool, err := helperPoolFakeRunner(t, registryPath, func(RunnerConfig) (Runner, error) {
		return fakeRunner{}, nil
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return pool
}

// assertHandoffNoteHolds asserts the note file for id under dataDir holds
// exactly want, read straight off disk rather than through HandoffNote — so a
// reader bug cannot make a writer bug invisible.
func assertHandoffNoteHolds(t *testing.T, dataDir string, id conversations.ConversationID, want string) {
	t.Helper()
	raw, err := os.ReadFile(handoffNotePathOf(dataDir, id))
	if err != nil {
		t.Fatalf("read handoff note: %v", err)
	}
	if string(raw) != want {
		t.Errorf("handoff note on disk = %q, want %q", raw, want)
	}
}

// --- AC #1: the round trip, replacement, placement and modes -----------------

// TestPool_HandoffNote_RoundTripAndReplace (AC #1): a write lands, reads back
// byte-identical, and a second write REPLACES rather than appends or accumulates.
// The directory-holds-one-entry assertion is what catches an accumulating name
// and a scratch file left behind by a write that did not clean up after itself.
func TestPool_HandoffNote_RoundTripAndReplace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pool := handoffPool(t, filepath.Join(dir, "sessions.json"))

	first := "Handed off: the caller was mid-refactor of the registry loader."
	path, err := pool.WriteHandoffNote(handoffConvID, first)
	if err != nil {
		t.Fatalf("WriteHandoffNote: %v", err)
	}
	if want := handoffNotePathOf(dir, handoffConvID); path != want {
		t.Errorf("WriteHandoffNote path = %q, want %q", path, want)
	}
	got, err := pool.HandoffNote(handoffConvID)
	if err != nil {
		t.Fatalf("HandoffNote: %v", err)
	}
	if got != first {
		t.Errorf("HandoffNote = %q, want %q", got, first)
	}
	assertHandoffNoteHolds(t, dir, handoffConvID, first)

	second := "Handed off again: the refactor landed; next is the sweep."
	if _, err := pool.WriteHandoffNote(handoffConvID, second); err != nil {
		t.Fatalf("WriteHandoffNote (replace): %v", err)
	}
	got, err = pool.HandoffNote(handoffConvID)
	if err != nil {
		t.Fatalf("HandoffNote (replace): %v", err)
	}
	if got != second {
		t.Errorf("HandoffNote after replace = %q, want %q", got, second)
	}

	entries, err := os.ReadDir(handoffNoteDirOf(dir))
	if err != nil {
		t.Fatalf("readdir handoff notes: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("handoff-notes dir holds %v, want exactly one entry: a replace must not accumulate, and no scratch file may survive", names)
	}
}

// TestPool_HandoffNote_PathAndModes (AC #1): cold start — neither sessions.json
// nor the note directory exists yet — produces an absolute path under the data
// dir, a 0700 directory and a file with no group or world bits. The note is a
// fragment of the operator's conversation, so the modes are confidentiality,
// not housekeeping. TestWriteMCPSettings_DataDirPathAndModes' shape.
func TestPool_HandoffNote_PathAndModes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	noteDir := handoffNoteDirOf(dir)
	if _, err := os.Stat(noteDir); !os.IsNotExist(err) {
		t.Fatalf("test setup: %q already exists (err=%v)", noteDir, err)
	}
	pool := handoffPool(t, filepath.Join(dir, "sessions.json"))

	path, err := pool.WriteHandoffNote(handoffConvID, "note")
	if err != nil {
		t.Fatalf("WriteHandoffNote: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("WriteHandoffNote path = %q, want absolute", path)
	}
	di, err := os.Stat(noteDir)
	if err != nil {
		t.Fatalf("stat handoff-notes dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("handoff-notes dir mode = %04o, want 0700", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat handoff note: %v", err)
	}
	if got := fi.Mode().Perm(); got&0o077 != 0 {
		t.Errorf("handoff note %q mode = %04o, want no group/world bits", path, got)
	}
}

// TestPool_HandoffNote_SurvivesFreshPool (AC #1, the restart clause): a pool
// constructed afresh over the same data dir reads the note unchanged. That pool
// runs Pool.New's startup purge, which clears session-prompts wholesale — this
// is the test that pins handoff-notes as a SIBLING of that directory rather than
// a tenant of it. It reddens the moment the note moves under session-prompts.
func TestPool_HandoffNote_SurvivesFreshPool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	note := "Survives the daemon that wrote it."

	if _, err := handoffPool(t, regPath).WriteHandoffNote(handoffConvID, note); err != nil {
		t.Fatalf("WriteHandoffNote: %v", err)
	}

	got, err := handoffPool(t, regPath).HandoffNote(handoffConvID)
	if err != nil {
		t.Fatalf("HandoffNote from fresh pool: %v", err)
	}
	if got != note {
		t.Errorf("HandoffNote from fresh pool = %q, want %q: the note did not survive New's startup purge", got, note)
	}
}

// --- AC #1: the cap ----------------------------------------------------------

// TestTruncateHandoffNote (AC #1): the cap truncates rather than rejects, and
// never splits a rune. Each case checks the three invariants together — within
// the cap, a prefix of the input, and still valid UTF-8 when the input was —
// because a truncation that satisfies any two alone is still wrong.
func TestTruncateHandoffNote(t *testing.T) {
	t.Parallel()

	// "é" is two bytes, so a run of them straddles an even cap by one byte: the
	// last rune cannot fit and must be dropped whole.
	straddling := strings.Repeat("é", MaxHandoffNoteBytes) // 2×cap bytes
	tests := []struct {
		name      string
		in        string
		wantBytes int
	}{
		{"under the cap", "short note", len("short note")},
		{"exactly at the cap", strings.Repeat("a", MaxHandoffNoteBytes), MaxHandoffNoteBytes},
		{"one byte over", strings.Repeat("a", MaxHandoffNoteBytes+1), MaxHandoffNoteBytes},
		// The cap is even and each rune is two bytes, so the cut lands on a rune
		// boundary and nothing is dropped.
		{"multi-byte runes aligned with the cap", straddling, MaxHandoffNoteBytes},
		// One ASCII byte shifts every following rune by one, so the byte at the
		// cap is a continuation byte and the straddling rune goes whole.
		{"multi-byte rune straddling the cap", "a" + straddling, MaxHandoffNoteBytes - 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateHandoffNote(tt.in)
			if len(got) != tt.wantBytes {
				t.Errorf("truncateHandoffNote(%d bytes) = %d bytes, want %d", len(tt.in), len(got), tt.wantBytes)
			}
			if len(got) > MaxHandoffNoteBytes {
				t.Errorf("result is %d bytes, over the %d-byte cap", len(got), MaxHandoffNoteBytes)
			}
			if !strings.HasPrefix(tt.in, got) {
				t.Error("result is not a prefix of the input: truncation must not rewrite bytes")
			}
			if utf8.ValidString(tt.in) && !utf8.ValidString(got) {
				t.Error("truncation split a rune: valid UTF-8 in, invalid out")
			}
		})
	}
}

// TestPool_WriteHandoffNote_TruncatesOnDisk (AC #1): the cap is enforced at the
// file, not only in the helper — an over-cap write succeeds and lands bounded.
func TestPool_WriteHandoffNote_TruncatesOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pool := handoffPool(t, filepath.Join(dir, "sessions.json"))

	if _, err := pool.WriteHandoffNote(handoffConvID, strings.Repeat("x", MaxHandoffNoteBytes*2)); err != nil {
		t.Fatalf("WriteHandoffNote: %v", err)
	}
	fi, err := os.Stat(handoffNotePathOf(dir, handoffConvID))
	if err != nil {
		t.Fatalf("stat handoff note: %v", err)
	}
	if fi.Size() != MaxHandoffNoteBytes {
		t.Errorf("handoff note size = %d, want %d: an over-cap note must truncate, not reject", fi.Size(), MaxHandoffNoteBytes)
	}
	got, err := pool.HandoffNote(handoffConvID)
	if err != nil {
		t.Fatalf("HandoffNote: %v", err)
	}
	if len(got) != MaxHandoffNoteBytes {
		t.Errorf("HandoffNote returned %d bytes, want %d", len(got), MaxHandoffNoteBytes)
	}
}

// --- AC #2: existence and path, without reading the text ---------------------

// TestPool_HandoffNotePath_ExistenceWithoutReading (AC #2): the store answers
// where the note is and whether there is one, and the path is derivable before
// any note exists — which is what lets #2468 omit its pointer line rather than
// having to handle an error. The note text here is a sentinel this test never
// passes through HandoffNote, so the existence answer cannot be coming from a read.
func TestPool_HandoffNotePath_ExistenceWithoutReading(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pool := handoffPool(t, filepath.Join(dir, "sessions.json"))
	want := handoffNotePathOf(dir, handoffConvID)

	path, exists, err := pool.HandoffNotePath(handoffConvID)
	if err != nil {
		t.Fatalf("HandoffNotePath before write: %v", err)
	}
	if path != want {
		t.Errorf("HandoffNotePath before write = %q, want %q", path, want)
	}
	if exists {
		t.Error("HandoffNotePath reports a note before anything was written")
	}

	if _, err := pool.WriteHandoffNote(handoffConvID, "SENTINEL-NEVER-READ-BACK"); err != nil {
		t.Fatalf("WriteHandoffNote: %v", err)
	}
	path, exists, err = pool.HandoffNotePath(handoffConvID)
	if err != nil {
		t.Fatalf("HandoffNotePath after write: %v", err)
	}
	if path != want {
		t.Errorf("HandoffNotePath after write = %q, want %q", path, want)
	}
	if !exists {
		t.Error("HandoffNotePath reports no note after a successful write")
	}
}

// TestPool_HandoffNote_AbsentIsNotAnError (AC #2): reading a conversation with
// no note yields "" rather than an error — the first reset of any conversation
// has no predecessor, and #2455 must not have to special-case it.
func TestPool_HandoffNote_AbsentIsNotAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pool := handoffPool(t, filepath.Join(dir, "sessions.json"))

	got, err := pool.HandoffNote(handoffConvID)
	if err != nil {
		t.Fatalf("HandoffNote with no note on disk = %v, want no error", err)
	}
	if got != "" {
		t.Errorf("HandoffNote with no note on disk = %q, want empty", got)
	}
}

// TestPool_HandoffNotePath_SymlinkIsNotANote: HandoffNotePath answers Lstat, so
// a symlink planted at the note path reports ABSENT rather than handing #2468 a
// path it would name to claude — which has file-read tools. The 0700 directory
// is the real boundary (planting the link needs the daemon's own uid); this is
// the correct predicate for the question "is there a note here", not a second
// line of defence.
func TestPool_HandoffNotePath_SymlinkIsNotANote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pool := handoffPool(t, filepath.Join(dir, "sessions.json"))

	target := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(target, []byte("not a handoff note"), 0o600); err != nil {
		t.Fatalf("write link target: %v", err)
	}
	if err := os.MkdirAll(handoffNoteDirOf(dir), 0o700); err != nil {
		t.Fatalf("mkdir handoff notes: %v", err)
	}
	if err := os.Symlink(target, handoffNotePathOf(dir, handoffConvID)); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	if _, exists, err := pool.HandoffNotePath(handoffConvID); err != nil || exists {
		t.Errorf("HandoffNotePath over a symlink = (exists=%v, err=%v), want (false, nil)", exists, err)
	}
}

// --- AC #3: a non-canonical conversation id is a hard error ------------------

// TestPool_HandoffNote_RejectsNonCanonicalID (AC #3): every method refuses a
// non-canonical id rather than writing, matching what systemPromptPathFor does
// with a malformed session id. The id names a file, so this gate is what keeps a
// separator or a ".." segment from placing the write outside the data dir — the
// no-entries-created assertion is the half that proves it.
func TestPool_HandoffNote_RejectsNonCanonicalID(t *testing.T) {
	t.Parallel()
	ids := []struct {
		name string
		id   conversations.ConversationID
	}{
		{"empty", ""},
		{"uppercase hex", "6F1C2B4A-9D3E-4A17-B8C5-0E2D7A41F9B3"},
		{"parent traversal", "../../etc/passwd"},
		{"path separator", "6f1c2b4a-9d3e-4a17-b8c5/0e2d7a41f9b3"},
		{"too short", "6f1c2b4a-9d3e-4a17-b8c5-0e2d7a41f9b"},
		{"not version 4", "6f1c2b4a-9d3e-1a17-b8c5-0e2d7a41f9b3"},
	}
	for _, tt := range ids {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			pool := handoffPool(t, filepath.Join(dir, "sessions.json"))

			if _, err := pool.WriteHandoffNote(tt.id, "note"); err == nil {
				t.Error("WriteHandoffNote accepted a non-canonical conversation id")
			}
			if _, err := pool.HandoffNote(tt.id); err == nil {
				t.Error("HandoffNote accepted a non-canonical conversation id")
			}
			if _, _, err := pool.HandoffNotePath(tt.id); err == nil {
				t.Error("HandoffNotePath accepted a non-canonical conversation id")
			}
			if entries, err := os.ReadDir(handoffNoteDirOf(dir)); err == nil && len(entries) != 0 {
				t.Errorf("a rejected id left %d entries on disk, want none", len(entries))
			}
		})
	}
}

// --- AC #4: teardown, rotation and shutdown all leave the note ---------------

// TestPool_Remove_LeavesHandoffNote (AC #4): tearing a session down removes its
// settings file and leaves the note. Asserted against the SAME Pool.Remove call,
// so the settings-file check is what proves the teardown path actually ran — a
// note surviving a teardown that never happened proves nothing. The note is keyed
// by conversation precisely so it outlives the session that produced it.
func TestPool_Remove_LeavesHandoffNote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)
	id := spawnMintedWithSettings(t, ctx, pool, spawnDir, SessionSettings{})

	note := "The successor should pick up the registry loader refactor."
	if _, err := pool.WriteHandoffNote(handoffConvID, note); err != nil {
		t.Fatalf("WriteHandoffNote: %v", err)
	}

	pool.mu.RLock()
	settingsPath := pool.sessions[id].settingsPath
	pool.mu.RUnlock()
	if settingsPath == "" {
		t.Fatal("minted session has empty settingsPath")
	}
	if err := pool.Remove(ctx, id, RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("settings file still present after Remove (err=%v); the teardown path did not run, so the assertion below would be vacuous", err)
	}
	assertHandoffNoteHolds(t, dir, handoffConvID, note)
}

// TestPool_RotateID_LeavesHandoffNote (AC #4): a `/clear` rotation re-keys the
// session in place and leaves the note at an UNCHANGED path. That the path does
// not move is the whole reason the key is the conversation and not the session:
// a reset is exactly when the successor needs what its predecessor left.
func TestPool_RotateID_LeavesHandoffNote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)
	id := spawnMintedWithSettings(t, ctx, pool, spawnDir, SessionSettings{})

	note := "Carried across the rotation."
	before, err := pool.WriteHandoffNote(handoffConvID, note)
	if err != nil {
		t.Fatalf("WriteHandoffNote: %v", err)
	}

	newID, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if err := pool.RotateID(id, newID); err != nil {
		t.Fatalf("RotateID: %v", err)
	}

	after, exists, err := pool.HandoffNotePath(handoffConvID)
	if err != nil {
		t.Fatalf("HandoffNotePath after RotateID: %v", err)
	}
	if !exists || after != before {
		t.Errorf("after RotateID the note is at (%q, exists=%v), want (%q, true)", after, exists, before)
	}
	assertHandoffNoteHolds(t, dir, handoffConvID, note)
}

// TestPool_Run_LeavesHandoffNoteAtShutdown (AC #4): the daemon's shutdown defer
// clears session-prompts wholesale and leaves the note. The session-prompts
// assertion is what proves the defer ran; without it a surviving note would only
// mean the shutdown path was never reached. A note that did not survive here
// could never reach a successor across a restart.
func TestPool_Run_LeavesHandoffNoteAtShutdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	waitArgvRaw(t, tplWorkDir)

	note := "Still here after the daemon stopped."
	if _, err := pool.WriteHandoffNote(handoffConvID, note); err != nil {
		t.Fatalf("WriteHandoffNote: %v", err)
	}

	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(sessionPromptDirOf(dir)); !os.IsNotExist(err) {
		t.Fatalf("session-prompts still present after shutdown (err=%v); the shutdown defer did not run, so the assertion below would be vacuous", err)
	}
	assertHandoffNoteHolds(t, dir, handoffConvID, note)
}

// --- AC #5: no log line carries a fragment of the note -----------------------

// TestPool_HandoffNote_LogsNoNoteText (AC #5): the store logs nothing, so no
// fragment of claude-authored note text can reach a log. Every method is driven
// including its failure paths — a non-canonical id and an absent note — because
// an error path is the likeliest place a future edit would log the value it is
// complaining about. The store holds no logger at all today; this is what keeps
// that true.
func TestPool_HandoffNote_LogsNoNoteText(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logs := &bytes.Buffer{}
	pool, err := New(Config{
		Bootstrap:    SessionConfig{ClaudeBin: "/nonexistent/claude-should-never-be-execd"},
		Logger:       slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		RegistryPath: filepath.Join(dir, "sessions.json"),
		RunnerFactory: func(RunnerConfig) (Runner, error) {
			return fakeRunner{}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const sentinel = "QUINCUNX-ZEPHYR-MARMALADE"
	if _, err := pool.WriteHandoffNote(handoffConvID, sentinel); err != nil {
		t.Fatalf("WriteHandoffNote: %v", err)
	}
	if _, err := pool.HandoffNote(handoffConvID); err != nil {
		t.Fatalf("HandoffNote: %v", err)
	}
	if _, _, err := pool.HandoffNotePath(handoffConvID); err != nil {
		t.Fatalf("HandoffNotePath: %v", err)
	}
	// Failure paths, over the same sentinel.
	_, _ = pool.WriteHandoffNote("not-a-conversation-id", sentinel)
	_, _ = pool.HandoffNote(conversations.ConversationID("11111111-1111-4111-8111-111111111111"))

	for _, frag := range []string{sentinel, "QUINCUNX", "MARMALADE"} {
		if strings.Contains(logs.String(), frag) {
			t.Errorf("a log line carries %q; no fragment of a note may be logged:\n%s", frag, logs.String())
		}
	}
}

// --- the persistence-disabled case -------------------------------------------

// TestPool_HandoffNote_PersistenceDisabled: with no RegistryPath there is no
// data dir, and the os.TempDir answer writeMCPSettings and writeSystemPromptFile
// give does NOT transfer — a note an OS reaper may delete is not a note. The
// write says so with a sentinel error; the two readers stay total, so every test
// pool in this package (almost none set RegistryPath) is on the ordinary path.
func TestPool_HandoffNote_PersistenceDisabled(t *testing.T) {
	t.Parallel()
	pool := handoffPool(t, "")

	if _, err := pool.WriteHandoffNote(handoffConvID, "note"); !errors.Is(err, ErrHandoffNotesDisabled) {
		t.Errorf("WriteHandoffNote with persistence disabled = %v, want ErrHandoffNotesDisabled", err)
	}
	got, err := pool.HandoffNote(handoffConvID)
	if err != nil || got != "" {
		t.Errorf("HandoffNote with persistence disabled = (%q, %v), want (\"\", nil)", got, err)
	}
	path, exists, err := pool.HandoffNotePath(handoffConvID)
	if err != nil || exists || path != "" {
		t.Errorf("HandoffNotePath with persistence disabled = (%q, %v, %v), want (\"\", false, nil)", path, exists, err)
	}

	// And nothing was minted in os.TempDir as a consolation file.
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "pyry-handoff-*"))
	if err != nil {
		t.Fatalf("glob temp dir: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("persistence-disabled write left %v in os.TempDir, want nothing", matches)
	}
}
