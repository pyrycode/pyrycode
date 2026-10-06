package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

const pinnedDaemonInstructions = `Keep the main thread free. The operator cannot send you a message while a turn runs. Their messages wait until the turn ends.
- Hand any work longer than a few tool calls to a background subagent. Reply at once with what started, and report the result when it arrives.
- Delegate builds, test runs, device testing, waiting on CI, investigations across several files, ticket filing, design work, log digging and knowledge capture.
- Keep inline only quick work: an answer from context, one lookup, one file read, one small edit.
- In this conversation, responsiveness matters more than token usage.
- Use a cheaper model for routine delegated work.
- Give each subagent a short brief, not the conversation history.
- Check a subagent's claim cheaply before acting on it.
- Never run two subagents on one shared resource, such as a connected device, an emulator or one working tree.`

func instructionsConfig(t *testing.T, path string) Config {
	t.Helper()
	return Config{
		RunnerFactory: recordingRunnerFactory,
		RegistryPath:  path,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Bootstrap: SessionConfig{ClaudeBin: "/bin/sh", ClaudeArgs: argvRecorderTemplate,
			WorkDir: t.TempDir(), BackoffInitial: 10 * time.Millisecond, BackoffMax: 10 * time.Millisecond},
	}
}

func instructionsPool(t *testing.T, cfg Config) *Pool {
	t.Helper()
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(p.systemPromptPath)
		for _, s := range p.sessions {
			_ = os.Remove(s.settingsPath)
		}
	})
	return p
}

func settingBytes(t *testing.T, text string) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		Instructions []byte `json:"instructions"`
	}{[]byte(text)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertSetting(t *testing.T, dir, text string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "daemon-instructions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, settingBytes(t, text)) {
		t.Fatalf("setting bytes differ: %q", b)
	}
	info, err := os.Stat(filepath.Join(dir, "daemon-instructions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}
}

func TestDaemonInstructionsStartup(t *testing.T) {
	t.Parallel()
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "upgrade"}[upgrade], func(t *testing.T) {
			dir := t.TempDir()
			cfg := instructionsConfig(t, filepath.Join(dir, "sessions.json"))
			if upgrade {
				p := instructionsPool(t, cfg)
				if err := os.Remove(filepath.Join(dir, "daemon-instructions.json")); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(p.registryPath); err != nil {
					t.Fatal(err)
				}
			}
			p := instructionsPool(t, cfg)
			if p.DefaultDaemonInstructions() != pinnedDaemonInstructions || p.DaemonInstructions() != pinnedDaemonInstructions {
				t.Fatal("default differs from independent transcription")
			}
			assertSetting(t, dir, pinnedDaemonInstructions)
			for _, text := range []string{" \t规则\n\n", "", p.DefaultDaemonInstructions()} {
				if err := p.SetDaemonInstructions(text); err != nil {
					t.Fatal(err)
				}
				assertSetting(t, dir, text)
				// New purges transient prompts but must retain the independent setting.
				p = instructionsPool(t, cfg)
				if p.DaemonInstructions() != text {
					t.Fatal("restart replaced stored value")
				}
			}
		})
	}
	t.Run("disabled", func(t *testing.T) {
		cfg := instructionsConfig(t, "")
		p := instructionsPool(t, cfg)
		if p.DaemonInstructions() != pinnedDaemonInstructions {
			t.Fatal("missing in-memory default")
		}
		if err := p.SetDaemonInstructions(""); err != nil {
			t.Fatal(err)
		}
		if p.DaemonInstructions() != "" || p.dataDir() != "" {
			t.Fatal("disabled write failed")
		}
		entries, err := os.ReadDir(cfg.Bootstrap.WorkDir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("disabled setting created files: %v, %v", entries, err)
		}
	})
}

func TestDaemonInstructionsStoreFailures(t *testing.T) {
	t.Parallel()
	const secret = "PRIVATE-INSTRUCTION-SENTINEL"
	cases := []struct {
		name       string
		data       []byte
		directory  bool
		unreadable bool
		want       error
	}{
		{name: "malformed", data: []byte(`{"instructions":"` + secret)},
		{name: "missing", data: []byte(`{}`)},
		{name: "null", data: []byte(`{"instructions":null}`)},
		{name: "wrong type", data: []byte(`{"instructions":42}`)},
		{name: "invalid base64", data: []byte(`{"instructions":"` + secret + `!"}`)},
		{name: "oversized", data: settingBytes(t, strings.Repeat("x", conversations.MaxSystemPromptBytes+1)), want: ErrDaemonInstructionsTooLong},
		{name: "invalid UTF8", data: settingBytes(t, secret+"\xff"), want: ErrDaemonInstructionsInvalidUTF8},
		{name: "invalid raw UTF8", data: []byte("{\"instructions\":\"\xff\"}")},
		{name: "nonregular", directory: true},
		{name: "unreadable", data: settingBytes(t, secret), unreadable: true, want: os.ErrPermission},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "daemon-instructions.json")
			if tc.directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, tc.data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unreadable {
				if err := os.Chmod(path, 0o000); err != nil {
					t.Fatal(err)
				}
			}
			cfg := instructionsConfig(t, filepath.Join(dir, "sessions.json"))
			logs := &syncBuffer{}
			cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
			p, err := New(cfg)
			if err == nil || p != nil {
				t.Fatal("invalid existing store allowed startup")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error()+logs.String(), secret) {
				t.Fatal("instruction leaked")
			}
			if tc.unreadable {
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.directory {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, tc.data) {
					t.Fatal("startup replaced malformed store")
				}
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		p := &Pool{registryPath: filepath.Join(dir, "sessions.json")}
		// A dangling link looks absent to open but cannot be accepted as an absent setting.
		if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "daemon-instructions.json")); err != nil {
			t.Fatal(err)
		}
		if err := p.loadDaemonInstructions(); err == nil {
			t.Fatal("symlink accepted")
		}
	})
}

func TestDaemonInstructionsInitialPersistenceFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := instructionsConfig(t, filepath.Join(dir, "sessions.json"))
	cfg.RunnerFactory = func(cfg RunnerConfig) (Runner, error) {
		// Bootstrap files have been written; make only the initial setting write fail.
		if err := os.Chmod(dir, 0o500); err != nil {
			return nil, err
		}
		return recordingRunnerFactory(cfg)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	p, err := New(cfg)
	if p != nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("startup = %v, %v", p, err)
	}
	if !strings.Contains(err.Error(), "persist daemon instructions") {
		t.Fatalf("wrong failing write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "daemon-instructions.json")); !os.IsNotExist(err) {
		t.Fatalf("failed seed left store: %v", err)
	}
}

func TestDaemonInstructionsWrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := instructionsPool(t, instructionsConfig(t, filepath.Join(dir, "sessions.json")))
	cases := []struct {
		name, text string
		want       error
	}{
		{"verbatim", " \tprivate\r\n规则\n", nil},
		{"ASCII inclusive", strings.Repeat("a", 8192), nil},
		{"multibyte inclusive", strings.Repeat("ä", 4096), nil},
		{"empty", "", nil},
		{"too long", strings.Repeat("a", 8193), ErrDaemonInstructionsTooLong},
		{"invalid", "PRIVATE-INSTRUCTION-SENTINEL\xff", ErrDaemonInstructionsInvalidUTF8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := p.DaemonInstructions()
			err := p.SetDaemonInstructions(tc.text)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			want := tc.text
			if tc.want != nil {
				want = before
				if strings.Contains(err.Error(), "PRIVATE-INSTRUCTION-SENTINEL") {
					t.Fatal("leaked text")
				}
			}
			if p.DaemonInstructions() != want {
				t.Fatal("memory differs")
			}
			assertSetting(t, dir, want)
		})
	}
	before := p.DaemonInstructions()
	backup := dir + "-backup"
	if err := os.Rename(dir, backup); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(dir); _ = os.Rename(backup, dir) }()
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.SetDaemonInstructions("PRIVATE-INSTRUCTION-SENTINEL"); err == nil || strings.Contains(err.Error(), "PRIVATE-INSTRUCTION-SENTINEL") {
		t.Fatalf("persistence error = %v", err)
	}
	if p.DaemonInstructions() != before {
		t.Fatal("failed persistence changed memory")
	}
	assertSetting(t, backup, before)
}

func TestDaemonInstructionsConcurrent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := instructionsPool(t, instructionsConfig(t, filepath.Join(dir, "sessions.json")))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				if err := p.SetDaemonInstructions("parallel"); err != nil {
					t.Error(err)
					return
				}
				_ = p.DaemonInstructions()
			}
		}()
	}
	wg.Wait()
	assertSetting(t, dir, p.DaemonInstructions())
}

func TestDaemonInstructionsComposition(t *testing.T) {
	t.Parallel()
	client := ClientIdentity{Name: "Phone", Version: "1"}
	head := daemonPromptText([]string{"/notes"})
	tail := "\n" + clientSectionLead + `"Phone" (version "1").` + "\n\n" + noteSectionOf(noteText) + "\noperator"
	for _, instructions := range []string{"", " \tshared\n规则\n"} {
		want := head + tail
		if instructions != "" {
			want = head + "\n" + instructions + tail
		}
		got := composeSystemPromptForOn(head, instructions, "operator", []ClientIdentity{client}, noteText)
		if got != want {
			t.Fatalf("composition = %q, want %q", got, want)
		}
	}
}

func TestDaemonInstructionsNextStarts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	operator := "conversation-one-last"
	cfg := instructionsConfig(t, filepath.Join(dir, "sessions.json"))
	cfg.ConversationsRegistry = conversationWithPrompt(convPromptID, &operator)
	p := instructionsPool(t, cfg)
	ctx, cancel := runPoolInBackground(t, p)
	bootstrap := systemPromptArgPath(t, waitArgvRaw(t, cfg.Bootstrap.WorkDir))
	assertSystemPromptFileAt(t, bootstrap)
	spawnDir := t.TempDir()
	id, err := p.Mint(convPromptID, spawnDir)
	if err != nil {
		t.Fatal(err)
	}
	path := sessionPromptPathOf(dir, id)
	suffix := "\n" + operator
	assert := func(shared string) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := systemPromptText + "\n" + shared + suffix
		if string(raw) != want {
			t.Fatalf("prompt = %q, want %q", raw, want)
		}
	}
	assert(pinnedDaemonInstructions)
	client := ClientIdentity{Name: "Phone", Version: "1"}
	p.SetClientIdentityResolver(func(context.Context) []ClientIdentity { return []ClientIdentity{client} })
	plantNote(t, dir, convPromptID, noteText)
	tail := "\n" + clientSectionLead + `"Phone" (version "1").` + "\n\n" + staleHandoffWarning + noteSectionOf(noteText) + "\n" + operator
	suffix = tail
	if err := p.SetDaemonInstructions("after-mint"); err != nil {
		t.Fatal(err)
	}
	if err := p.Activate(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := systemPromptArgPath(t, waitArgvRaw(t, spawnDir)); got != path {
		t.Fatal("argv names wrong file")
	}
	assert("after-mint")
	sess, err := p.Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	if !pollUntil(t, 2*time.Second, func() bool { return sess.State().ChildPID > 0 }) {
		t.Fatal("child did not start")
	}
	stamp := time.Unix(123456789, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	pid := sess.State().ChildPID
	if err := p.SetDaemonInstructions("next-start"); err != nil {
		t.Fatal(err)
	}
	if err := p.Activate(ctx, id); err != nil {
		t.Fatal(err)
	}
	assert("after-mint")
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(stamp) {
		t.Fatal("active prompt file was rewritten")
	}
	if sess.State().ChildPID != pid {
		t.Fatal("edit restarted active child")
	}
	if err := sess.Evict(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Activate(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitArgvRaw(t, spawnDir)
	assert("next-start")
	if err := p.SetDaemonInstructions("rotation"); err != nil {
		t.Fatal(err)
	}
	p.SetClientIdentityResolver(func(_ context.Context) []ClientIdentity { t.Error("rotation resolved clients"); return nil })
	id, err = p.RotateForNewSession(id)
	if err != nil {
		t.Fatal(err)
	}
	assert("rotation")
	p.SetClientIdentityResolver(nil)
	if err := p.Remove(ctx, id, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	assertSetting(t, dir, "rotation")
	// A separately revived conversation sees the same setting, with its own prompt last.
	other := "22222222-2222-4222-8222-222222222222"
	last := "conversation-two-last"
	cfg.ConversationsRegistry.Create(conversations.Conversation{ID: conversations.ConversationID(other), SystemPrompt: &last})
	otherID, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	otherDir := t.TempDir()
	if _, err := p.Revive(otherID, other, otherDir); err != nil {
		t.Fatal(err)
	}
	if err := p.SetDaemonInstructions("revived"); err != nil {
		t.Fatal(err)
	}
	if err := p.Activate(ctx, otherID); err != nil {
		t.Fatal(err)
	}
	otherPath := systemPromptArgPath(t, waitArgvRaw(t, otherDir))
	raw, err := os.ReadFile(otherPath)
	if err != nil || string(raw) != systemPromptText+"\nrevived\n"+last {
		t.Fatalf("revived prompt = %q, %v", raw, err)
	}
	assertSystemPromptFileAt(t, bootstrap)
	cancel()
	if !pollUntil(t, 3*time.Second, func() bool { _, err := os.Stat(otherPath); return os.IsNotExist(err) }) {
		t.Fatal("shutdown did not remove transient prompts")
	}
	assertSetting(t, dir, "revived")
	again := instructionsPool(t, cfg)
	if again.DaemonInstructions() != "revived" {
		t.Fatal("shutdown/purge removed setting")
	}
}

func TestDaemonInstructionsRefreshFailureKeepsCompletePrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logs := &syncBuffer{}
	cfg := instructionsConfig(t, filepath.Join(dir, "sessions.json"))
	cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
	p := instructionsPool(t, cfg)
	runPoolInBackground(t, p)
	id, err := p.Mint("conversation", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := p.Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(sess.systemPromptPath)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "PRIVATE-INSTRUCTION-SENTINEL"
	if err := p.SetDaemonInstructions(secret); err != nil {
		t.Fatal(err)
	}
	promptDir := filepath.Dir(sess.systemPromptPath)
	backup := promptDir + "-backup"
	if err := os.Rename(promptDir, backup); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(promptDir); _ = os.Rename(backup, promptDir) }()
	if err := os.WriteFile(promptDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p.refreshSystemPromptForRotation(sess)
	after, err := os.ReadFile(filepath.Join(backup, filepath.Base(sess.systemPromptPath)))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed refresh changed previous complete prompt")
	}
	if !strings.Contains(logs.String(), "compose appended system prompt") || strings.Contains(logs.String(), secret) {
		t.Fatalf("refresh logs = %q", logs.String())
	}
}
