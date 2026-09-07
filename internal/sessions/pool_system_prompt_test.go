package sessions

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
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

// sessionPromptDirOf returns the per-session prompt directory for a data dir.
func sessionPromptDirOf(dataDir string) string {
	return filepath.Join(dataDir, "session-prompts")
}

// sessionPromptPathOf returns the per-session prompt path for a data dir and id.
func sessionPromptPathOf(dataDir string, id SessionID) string {
	return filepath.Join(sessionPromptDirOf(dataDir), string(id)+".txt")
}

// assertPromptFileHolds asserts the file at path holds the constant followed by
// operator, per the composition rule. operator == "" wants the constant alone.
//
// The wanted bytes are assembled here from the rule rather than by calling
// composeSystemPrompt, so a composition that changed the rule cannot satisfy
// this by changing both sides at once. The constant itself is referenced (it is
// pinned independently by TestSystemPromptText_Pinned).
func assertPromptFileHolds(t *testing.T, path, operator string) {
	t.Helper()
	want := systemPromptText
	if operator != "" {
		want = systemPromptText + "\n" + operator
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read system-prompt file %q: %v", path, err)
	}
	if string(raw) != want {
		t.Errorf("system-prompt file %q content =\n%q\nwant\n%q", path, raw, want)
	}
}

// helperPoolWithConversations is helperPoolArgvRecorder plus a conversations
// registry, which is what a production pool always has and what most of this
// package's tests deliberately leave nil. reg is wired verbatim; the sweep it
// enables ticks hourly and so never fires inside a test.
func helperPoolWithConversations(t *testing.T, registryPath, tplWorkDir string, reg *conversations.Registry) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	pool, err := New(Config{
		RunnerFactory: recordingRunnerFactory,
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     argvRecorderTemplate,
			WorkDir:        tplWorkDir,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath:              registryPath,
		ConversationsRegistry:     reg,
		ConversationsRegistryPath: filepath.Join(filepath.Dir(registryPath), "conversations.json"),
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// conversationWithPrompt returns a registry holding one conversation at id whose
// SystemPrompt is prompt (nil for the absent state).
func conversationWithPrompt(id string, prompt *string) *conversations.Registry {
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:           conversations.ConversationID(id),
		Cwd:          "/tmp",
		SystemPrompt: prompt,
	})
	return reg
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

// TestPool_MintedSpawn_UsesPerSessionSystemPromptFile (#2093 AC #1, #2150 AC #2):
// a minted session carries the flag too, at its OWN path under session-prompts/,
// and — carrying no conversation prompt — holds exactly the constant.
//
// This inverts #2093's TestPool_MintedSpawn_SharesBootstrapSystemPromptFile,
// whose shared-path assertion pinned a decision #2150 reverses: the text is no
// longer identical for every session, so the file gains a per-session identity
// and, with it, a per-session lifecycle (Pool.Remove now removes it). What stays
// pinned is the content claim: a session with no prompt bytes receives today's
// appended text and nothing more.
func TestPool_MintedSpawn_UsesPerSessionSystemPromptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	ctx, _ := runPoolInBackground(t, pool)

	bootstrapPath := systemPromptArgPath(t, waitArgvRaw(t, tplWorkDir))

	id := spawnMintedWithSettings(t, ctx, pool, spawnDir, SessionSettings{})

	mintedPath := systemPromptArgPath(t, waitArgvRaw(t, spawnDir))
	if want := sessionPromptPathOf(dir, id); mintedPath != want {
		t.Errorf("minted --append-system-prompt-file = %q, want the per-session %q", mintedPath, want)
	}
	if mintedPath == bootstrapPath {
		t.Errorf("minted and bootstrap name the same prompt file %q; a per-conversation prompt "+
			"written there would reach every other session (#2150)", mintedPath)
	}
	assertPromptFileHolds(t, mintedPath, "")
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

// --- #2150: the conversation's stored prompt --------------------------------

// convPromptID is the conversation id the cases below both store the prompt
// under and pass to Mint / Revive as the session label — the two are the same
// value at both production sites (create_conversation's mint, sessionRouter's
// revive).
const convPromptID = "11111111-1111-4111-8111-111111111111"

// TestComposeSystemPrompt (#2150 AC #1, #2) pins the composition rule: no
// operator bytes yields the constant alone, and operator bytes are appended
// verbatim after a blank-line separator — never substituted for the constant.
//
// The "appended, never replacing" half is the security-review's load-bearing
// property. A composition that returned the operator text alone would satisfy
// every argv assertion in this file and silently discard what #2093 exists to
// say, so it is asserted on the composed bytes here rather than inferred.
func TestComposeSystemPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		operator string
		want     string
	}{
		{"no operator bytes", "", systemPromptText},
		{"operator bytes appended", "Speak only in haiku.", systemPromptText + "\nSpeak only in haiku."},
		{"whitespace is bytes", " ", systemPromptText + "\n "},
		{"trailing newline preserved verbatim", "x\n", systemPromptText + "\nx\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := composeSystemPrompt(tc.operator)
			if got != tc.want {
				t.Errorf("composeSystemPrompt(%q) =\n%q\nwant\n%q", tc.operator, got, tc.want)
			}
			if !strings.HasPrefix(got, systemPromptText) {
				t.Errorf("composeSystemPrompt(%q) does not start with the constant: the operator's "+
					"text must be APPENDED to claude's appended prompt, never replace it", tc.operator)
			}
		})
	}
}

// TestPool_ConversationPrompt_TriState (#2150 AC #2) pins the resolver: all
// three of #2149's states plus every lookup miss collapse to "no bytes", so the
// spawn site carries one predicate and the tri-state stays in the registry.
//
// The nil-registry row is the one that keeps most of this package's tests
// working — Config.ConversationsRegistry's doc calls nil the test default — and
// rebindConversation is the precedent for treating it as a silent no-op.
func TestPool_ConversationPrompt_TriState(t *testing.T) {
	t.Parallel()
	text := "Speak only in haiku."
	empty := ""
	tests := []struct {
		name  string
		reg   *conversations.Registry
		label string
		want  string
	}{
		{"nil registry", nil, convPromptID, ""},
		{"empty label", conversationWithPrompt(convPromptID, &text), "", ""},
		{"label names no conversation", conversationWithPrompt(convPromptID, &text), "no-such-conversation", ""},
		{"absent prompt (nil)", conversationWithPrompt(convPromptID, nil), convPromptID, ""},
		{"explicitly empty prompt", conversationWithPrompt(convPromptID, &empty), convPromptID, ""},
		{"operator-set prompt", conversationWithPrompt(convPromptID, &text), convPromptID, text},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &Pool{convReg: tc.reg}
			if got := p.conversationPrompt(tc.label); got != tc.want {
				t.Errorf("conversationPrompt(%q) = %q, want %q", tc.label, got, tc.want)
			}
		})
	}
}

// TestPool_MintedSpawn_AppendsConversationPrompt (#2150 AC #1) is the headline:
// a session minted for a conversation that already carries a prompt spawns with
// those bytes in the file its argv names.
func TestPool_MintedSpawn_AppendsConversationPrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()
	prompt := "Answer only with the word ORANGE."

	pool := helperPoolWithConversations(t, regPath, tplWorkDir, conversationWithPrompt(convPromptID, &prompt))
	ctx, _ := runPoolInBackground(t, pool)
	waitArgvRaw(t, tplWorkDir) // the bootstrap's own spawn, so the wait below is the minted one

	id, err := pool.Mint(convPromptID, spawnDir)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	path := systemPromptArgPath(t, waitArgvRaw(t, spawnDir))
	if want := sessionPromptPathOf(dir, id); path != want {
		t.Errorf("minted --append-system-prompt-file = %q, want %q", path, want)
	}
	assertPromptFileHolds(t, path, prompt)
}

// TestPool_Activate_ComposesPromptSetAfterMint (#2150 AC #1) is the clause that
// decides whether this ticket ships alive: since #2085 a conversation's session
// is MINTED at create and started on the first message, so the operator's normal
// flow sets the prompt after buildSession has already frozen spawnBase. A design
// that composed only at build time passes every other case in this file and
// fails this one — which is the shape Conversation.Cwd is stuck in today.
func TestPool_Activate_ComposesPromptSetAfterMint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	reg := conversationWithPrompt(convPromptID, nil)
	pool := helperPoolWithConversations(t, regPath, tplWorkDir, reg)
	ctx, _ := runPoolInBackground(t, pool)
	waitArgvRaw(t, tplWorkDir)

	// Mint with no prompt stored: this is create_conversation's moment.
	id, err := pool.Mint(convPromptID, spawnDir)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	path := sessionPromptPathOf(dir, id)
	assertPromptFileHolds(t, path, "")

	// The operator sets the prompt, then sends the first message.
	prompt := "Answer only with the word ORANGE."
	if err := reg.SetSystemPrompt(conversations.ConversationID(convPromptID), &prompt); err != nil {
		t.Fatalf("SetSystemPrompt: %v", err)
	}
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	if got := systemPromptArgPath(t, waitArgvRaw(t, spawnDir)); got != path {
		t.Errorf("spawn argv names %q, want the stable %q: the refresh must rewrite the file the "+
			"argv already points at, never a new path", got, path)
	}
	assertPromptFileHolds(t, path, prompt)
}

// TestPool_Reactivate_AfterEviction_RecomposesPrompt (#2150 AC #1) covers the
// re-activate clause: a session revived from idle eviction re-execs the argv it
// was built with, so the bytes behind that stable path must be current.
func TestPool_Reactivate_AfterEviction_RecomposesPrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	first := "Answer only with the word ORANGE."
	reg := conversationWithPrompt(convPromptID, &first)
	pool := helperPoolWithConversations(t, regPath, tplWorkDir, reg)
	ctx, _ := runPoolInBackground(t, pool)
	waitArgvRaw(t, tplWorkDir)

	id, err := pool.Mint(convPromptID, spawnDir)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	path := systemPromptArgPath(t, waitArgvRaw(t, spawnDir))
	assertPromptFileHolds(t, path, first)

	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if err := sess.Evict(ctx); err != nil {
		t.Fatalf("Evict: %v", err)
	}

	second := "Answer only with the word INDIGO."
	if err := reg.SetSystemPrompt(conversations.ConversationID(convPromptID), &second); err != nil {
		t.Fatalf("SetSystemPrompt: %v", err)
	}
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("re-Activate: %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		raw, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(raw), second)
	}) {
		t.Errorf("after a re-activate the file at %q does not carry the current prompt", path)
	}
	assertPromptFileHolds(t, path, second)
}

// TestPool_ExplicitlyEmptyPrompt_SpawnsConstantOnly (#2150 AC #2): #2149's
// non-nil pointer to "" is a stored state a spawn must handle, and it reaches
// claude as exactly today's appended text — not as a blank line, not as a
// trailing separator.
func TestPool_ExplicitlyEmptyPrompt_SpawnsConstantOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()
	empty := ""

	pool := helperPoolWithConversations(t, regPath, tplWorkDir, conversationWithPrompt(convPromptID, &empty))
	ctx, _ := runPoolInBackground(t, pool)
	waitArgvRaw(t, tplWorkDir)

	id, err := pool.Mint(convPromptID, spawnDir)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	assertSystemPromptFileAt(t, systemPromptArgPath(t, waitArgvRaw(t, spawnDir)))
}

// TestPool_Revive_AppendsConversationPrompt (#2150 AC #1, #2): the second path a
// conversation's session is built by — a daemon restart, where sessionRouter
// revives the bound id with the conversation id as the label — composes the
// same way. Revive reaches buildSession through materialise rather than Mint.
func TestPool_Revive_AppendsConversationPrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()
	prompt := "Answer only with the word ORANGE."

	pool := helperPoolWithConversations(t, regPath, tplWorkDir, conversationWithPrompt(convPromptID, &prompt))
	ctx, _ := runPoolInBackground(t, pool)
	waitArgvRaw(t, tplWorkDir)

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := pool.Revive(id, convPromptID, spawnDir); err != nil {
		t.Fatalf("Revive: %v", err)
	}
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	path := systemPromptArgPath(t, waitArgvRaw(t, spawnDir))
	if want := sessionPromptPathOf(dir, id); path != want {
		t.Errorf("revived --append-system-prompt-file = %q, want %q", path, want)
	}
	assertPromptFileHolds(t, path, prompt)
}

// TestPool_RotatedSession_KeepsItsPromptFile (#2150 AC #1) covers the `/clear`
// clause. A rotation re-keys the session IN PLACE (Pool.rekeyLocked) rather than
// building a new one, so spawnBase — and the prompt path inside it — still names
// the file derived from the OLD id. A refresh that re-derived the path from the
// session's current id would write a file nothing reads, and every later spawn
// would silently carry the pre-rotation prompt.
func TestPool_RotatedSession_KeepsItsPromptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	first := "Answer only with the word ORANGE."
	reg := conversationWithPrompt(convPromptID, &first)
	pool := helperPoolWithConversations(t, regPath, tplWorkDir, reg)
	ctx, _ := runPoolInBackground(t, pool)
	waitArgvRaw(t, tplWorkDir)

	oldID, err := pool.Mint(convPromptID, spawnDir)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if err := pool.Activate(ctx, oldID); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	path := systemPromptArgPath(t, waitArgvRaw(t, spawnDir))

	newID, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if err := pool.RotateID(oldID, newID); err != nil {
		t.Fatalf("RotateID: %v", err)
	}

	second := "Answer only with the word INDIGO."
	if err := reg.SetSystemPrompt(conversations.ConversationID(convPromptID), &second); err != nil {
		t.Fatalf("SetSystemPrompt: %v", err)
	}
	sess, err := pool.Lookup(newID)
	if err != nil {
		t.Fatalf("Lookup after rotation: %v", err)
	}
	if err := sess.Evict(ctx); err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if err := pool.Activate(ctx, newID); err != nil {
		t.Fatalf("Activate after rotation: %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool {
		raw, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(raw), second)
	}) {
		t.Errorf("after a rotation the refresh did not reach %q — the path the argv still names", path)
	}
	if _, err := os.Stat(sessionPromptPathOf(dir, newID)); err == nil {
		t.Errorf("the refresh wrote a file at the post-rotation id's path (%q), which no argv names",
			sessionPromptPathOf(dir, newID))
	}
}

// TestPool_SystemPromptFor (#2150 AC #4): the daemon can report, for a live
// session, the OPERATOR bytes it was spawned with — not the composed whole,
// which would make #2152 strip a constant it does not own to compare against the
// stored value.
func TestPool_SystemPromptFor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()

	first := "Answer only with the word ORANGE."
	reg := conversationWithPrompt(convPromptID, &first)
	pool := helperPoolWithConversations(t, regPath, tplWorkDir, reg)
	ctx, _ := runPoolInBackground(t, pool)
	waitArgvRaw(t, tplWorkDir)

	if _, err := pool.SystemPromptFor("no-such-session"); err != ErrSessionNotFound {
		t.Errorf("SystemPromptFor(unknown) error = %v, want ErrSessionNotFound", err)
	}

	id, err := pool.Mint(convPromptID, spawnDir)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	waitArgvRaw(t, spawnDir)

	got, err := pool.SystemPromptFor(id)
	if err != nil {
		t.Fatalf("SystemPromptFor: %v", err)
	}
	if got != first {
		t.Errorf("SystemPromptFor = %q, want the operator bytes %q", got, first)
	}

	// The stored value moving on does not move the running one: that gap is the
	// difference #2152 exists to report.
	second := "Answer only with the word INDIGO."
	if err := reg.SetSystemPrompt(conversations.ConversationID(convPromptID), &second); err != nil {
		t.Fatalf("SetSystemPrompt: %v", err)
	}
	if got, err := pool.SystemPromptFor(id); err != nil || got != first {
		t.Errorf("SystemPromptFor after a stored-value change = (%q, %v), want the spawned-with %q",
			got, err, first)
	}
}

// --- #2150: the file's lifecycle and its confidentiality --------------------

// TestPool_Remove_RemovesSystemPromptFile (#2150 AC #3): the per-session file is
// removed when its session is torn down. Unlike #2093's daemon-scoped file —
// whose non-removal in Pool.Remove was correct because every other session read
// it — this one belongs to exactly one session and holds operator text.
func TestPool_Remove_RemovesSystemPromptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()
	prompt := "Answer only with the word ORANGE."

	pool := helperPoolWithConversations(t, regPath, tplWorkDir, conversationWithPrompt(convPromptID, &prompt))
	ctx, _ := runPoolInBackground(t, pool)
	waitArgvRaw(t, tplWorkDir)

	id, err := pool.Mint(convPromptID, spawnDir)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	path := sessionPromptPathOf(dir, id)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %q after Mint: %v; the removal assertion below would be vacuous", path, err)
	}
	if err := pool.Remove(ctx, id, RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stat %q after Remove = %v, want os.IsNotExist: the operator's prompt outlived its session",
			path, err)
	}
}

// TestPool_Run_RemovesSessionPromptsAtShutdown (#2150 AC #3): no per-session
// prompt file outlives the daemon. The whole directory goes, so a session that
// was never Remove-d does not leave operator text in the data dir, where nothing
// reaps it.
func TestPool_Run_RemovesSessionPromptsAtShutdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()
	prompt := "Answer only with the word ORANGE."

	pool := helperPoolWithConversations(t, regPath, tplWorkDir, conversationWithPrompt(convPromptID, &prompt))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	waitArgvRaw(t, tplWorkDir)

	id, err := pool.Mint(convPromptID, spawnDir)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	path := sessionPromptPathOf(dir, id)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %q while the daemon is up: %v; the assertion below would be vacuous", path, err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("pool.Run did not exit within 15s after cancel")
	}

	if _, err := os.Stat(sessionPromptDirOf(dir)); !os.IsNotExist(err) {
		t.Errorf("stat %q after Run returned = %v, want os.IsNotExist", sessionPromptDirOf(dir), err)
	}
}

// TestPool_New_PurgesStaleSessionPrompts (#2150 AC #3): a SIGKILL survives no
// defer, so the shutdown removal alone cannot make "never outlives the daemon"
// true. New purges the directory before it writes anything, which is sound
// because no session has been materialised at that point.
func TestPool_New_PurgesStaleSessionPrompts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")

	stale := filepath.Join(sessionPromptDirOf(dir), "stale.txt")
	if err := os.MkdirAll(sessionPromptDirOf(dir), 0o700); err != nil {
		t.Fatalf("mkdir stale dir: %v", err)
	}
	if err := os.WriteFile(stale, []byte("a previous daemon's operator prompt"), 0o600); err != nil {
		t.Fatalf("write stale prompt: %v", err)
	}

	if _, err := helperPoolFakeRunner(t, regPath, func(RunnerConfig) (Runner, error) {
		return fakeRunner{}, nil
	}); err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stat %q after New = %v, want os.IsNotExist: a killed daemon's prompts survived", stale, err)
	}
}

// TestPool_SpawnPath_NeverLogsPromptBytes (#2150 AC #3) is the confidentiality
// assertion the ticket makes explicitly, and it is why the flag takes a file
// rather than a string: the interactive runner logs the whole spawn argv at Info
// on every spawn, so an --append-system-prompt <text> spelling would print the
// operator's prompt on every restart. The PATH is expected in that record — the
// --settings path already reaches it the same way — so this asserts the split,
// not silence.
func TestPool_SpawnPath_NeverLogsPromptBytes(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()
	prompt := "Answer only with the word ORANGE."

	logs := &syncBuffer{}
	pool, err := New(Config{
		RunnerFactory: recordingRunnerFactory,
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     argvRecorderTemplate,
			WorkDir:        tplWorkDir,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger:                    slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		RegistryPath:              regPath,
		ConversationsRegistry:     conversationWithPrompt(convPromptID, &prompt),
		ConversationsRegistryPath: filepath.Join(dir, "conversations.json"),
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	ctx, _ := runPoolInBackground(t, pool)
	waitArgvRaw(t, tplWorkDir)

	id, err := pool.Mint(convPromptID, spawnDir)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	waitArgvRaw(t, spawnDir)

	if got := logs.String(); strings.Contains(got, prompt) {
		t.Errorf("the daemon log carries the operator's prompt bytes %q:\n%s", prompt, got)
	}
}

// syncBuffer is a mutex-guarded byte buffer, so the test goroutine can read the
// captured log while the pool's goroutines are still writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
