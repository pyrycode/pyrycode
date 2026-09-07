package sessions

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- helpers -----------------------------------------------------------------

// stripSystemPrompt removes the adjacent "--append-system-prompt-file <path>"
// pair that #2093 injects into every interactive spawn's base and returns the
// remaining tokens. Exactly stripMCPSettings' shape and there for its reason: the
// pre-existing exact-argv assertions keep comparing only the flags they own, and
// because this fatals when the pair is absent, each of them doubles as a
// regression check that the flag reached that path.
func stripSystemPrompt(t *testing.T, argv []string) []string {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--append-system-prompt-file" {
			out := make([]string, 0, len(argv)-2)
			out = append(out, argv[:i]...)
			out = append(out, argv[i+2:]...)
			return out
		}
	}
	t.Fatalf("argv %v missing --append-system-prompt-file <path> pair (#2093)", argv)
	return nil
}

// systemPromptArgPath asserts argv carries exactly one adjacent
// "--append-system-prompt-file <path>" pair and returns the path. The
// exactly-one check is what would catch a second append site added at a
// composition site rather than through writeSystemPrompt's text parameter.
func systemPromptArgPath(t *testing.T, argv []string) string {
	t.Helper()
	idx := -1
	for i, a := range argv {
		if a == "--append-system-prompt-file" {
			if idx != -1 {
				t.Fatalf("argv %v carries more than one --append-system-prompt-file flag", argv)
			}
			idx = i
		}
	}
	if idx == -1 {
		t.Fatalf("argv %v missing --append-system-prompt-file flag", argv)
	}
	if idx+1 >= len(argv) {
		t.Fatalf("argv %v has --append-system-prompt-file with no path", argv)
	}
	return argv[idx+1]
}

// assertSystemPromptFileAt asserts the file at path holds exactly the constant.
func assertSystemPromptFileAt(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read system-prompt file %q: %v", path, err)
	}
	if string(raw) != systemPromptText {
		t.Errorf("system-prompt file %q content =\n%q\nwant\n%q", path, raw, systemPromptText)
	}
}

// promptPathOf returns the daemon-scoped prompt path for a data dir.
func promptPathOf(dataDir string) string {
	return filepath.Join(dataDir, "system-prompt.txt")
}

// --- the constant ------------------------------------------------------------

// TestSystemPromptText_Pinned (AC #2) pins the appended text byte for byte. The
// want string is transcribed here rather than referenced, so the comparison is
// against an independent copy and not against itself.
//
// The pin is the enforcement mechanism for "asserts nothing about what any client
// can render": every word below is an architecture fact, true of every client
// that will ever connect, and a future capability claim — "this client cannot
// render tables" — cannot be slipped in without reddening this test. That makes
// such a claim a visible, deliberate diff rather than drift. If you are here
// because this went red, the question to answer is not "what is the new text" but
// "is every sentence of the new text still true of EVERY client".
func TestSystemPromptText_Pinned(t *testing.T) {
	t.Parallel()
	const want = "You are running as a supervised child of the pyry daemon. " +
		"Your replies are not displayed in a terminal: they leave this process as a " +
		"structured stream and are rendered for the operator by a separate client " +
		"application, which may be running on a different machine than the one your " +
		"tools execute on. More than one client can be attached to a session, and " +
		"which one is attached can change while the session runs.\n"
	if systemPromptText != want {
		t.Errorf("systemPromptText =\n%q\nwant\n%q", systemPromptText, want)
	}
}

// --- the spawn argv ----------------------------------------------------------

// TestPool_BootstrapSpawn_IncludesSystemPromptFile (AC #1, #2): the bootstrap
// claude's argv carries "--append-system-prompt-file <path>", the path is the
// daemon-scoped file under the data dir, and the file holds the constant.
func TestPool_BootstrapSpawn_IncludesSystemPromptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	runPoolInBackground(t, pool)

	path := systemPromptArgPath(t, waitArgvRaw(t, tplWorkDir))
	if want := promptPathOf(dir); path != want {
		t.Errorf("bootstrap --append-system-prompt-file = %q, want %q", path, want)
	}
	assertSystemPromptFileAt(t, path)
}

// TestPool_MintedSpawn_SharesBootstrapSystemPromptFile (AC #1): a minted session
// carries the flag too, and at the SAME path as the bootstrap.
//
// The shared-path half is the assertion that pins the design decision rather than
// merely the flag's presence: the text is identical for every session, so ONE
// daemon-scoped file serves them all and there is no per-session lifecycle to
// manage. A per-session file would satisfy "the flag is present" and fail here,
// which is exactly the distinction worth keeping red-able — it is also what makes
// Pool.Remove's non-removal of this file correct rather than an omission.
func TestPool_MintedSpawn_SharesBootstrapSystemPromptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	bootstrapPath := systemPromptArgPath(t, waitArgvRaw(t, tplWorkDir))

	spawnMintedWithSettings(t, ctx, pool, spawnDir, SessionSettings{})

	mintedPath := systemPromptArgPath(t, waitArgvRaw(t, spawnDir))
	if mintedPath != bootstrapPath {
		t.Errorf("minted --append-system-prompt-file = %q, want the bootstrap's %q: the file is "+
			"daemon-scoped, one file for every session", mintedPath, bootstrapPath)
	}
	assertSystemPromptFileAt(t, mintedPath)
}

// TestPool_LiveRestartRecompose_KeepsSystemPromptFile (AC #1): a live
// settings-restart (#842) recomposes argv from spawnBase, so the pair must
// survive the recompose pointing at the same, still-readable file. This is the
// assertion that would go red if the flag were added at claudeSettingsArgs, at
// streamsup, or at the cmd/pyry mapper instead of on the base.
func TestPool_LiveRestartRecompose_KeepsSystemPromptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperRestartPool(t, regPath, tplWorkDir, SessionSettings{Model: "sonnet"})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()

	firstPath := systemPromptArgPath(t, waitArgvRaw(t, tplWorkDir))
	assertSystemPromptFileAt(t, firstPath)
	clearRecording(t, tplWorkDir)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus")}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	restartPath := systemPromptArgPath(t, waitArgvRaw(t, tplWorkDir))
	if restartPath != firstPath {
		t.Errorf("restart --append-system-prompt-file path = %q, want stable %q", restartPath, firstPath)
	}
	assertSystemPromptFileAt(t, restartPath)
}

// --- the file's lifecycle ----------------------------------------------------

// TestPool_Run_RemovesSystemPromptFileAtShutdown (AC #3): the daemon-scoped file
// is removed when Pool.Run returns, leaving no orphan in the data dir — where,
// since #1518, nothing reaps what a missed removal leaves behind.
//
// Drives Run directly rather than through runPoolInBackground, whose cancel is a
// t.Cleanup and so would fire after the assertion.
func TestPool_Run_RemovesSystemPromptFileAtShutdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()

	path := systemPromptArgPath(t, waitArgvRaw(t, tplWorkDir))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %q while the daemon is up: %v; the removal assertion below would be vacuous", path, err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("pool.Run did not exit within 15s after cancel")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stat %q after Run returned = %v, want os.IsNotExist: the file outlived the daemon", path, err)
	}
}

// TestPool_New_SystemPromptWriteFailureIsFatal (AC #3): a write failure at daemon
// start fails New rather than silently spawning every session without the prompt,
// and takes the already-written settings file with it.
//
// The failure is forced by pre-creating a DIRECTORY at the file's path, so the
// atomic write's final rename cannot land. It is targeted: writeMCPSettings
// writes into <dataDir>/session-settings/ and still succeeds, so a green here
// cannot be explained by a shared cause that broke both writes — and the empty
// settings dir afterwards is the New-path cleanup defer being exercised.
func TestPool_New_SystemPromptWriteFailureIsFatal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")

	if err := os.MkdirAll(promptPathOf(dir), 0o700); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}

	pool, err := helperPoolFakeRunner(t, regPath, func(RunnerConfig) (Runner, error) {
		return fakeRunner{}, nil
	})
	if err == nil {
		t.Fatalf("New = %v, nil error; want a failure when the system-prompt file cannot be written", pool)
	}
	assertSettingsDirEmpty(t, settingsDirOf(dir))
}
