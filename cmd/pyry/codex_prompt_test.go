package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// threadOpen is one thread/start or thread/resume the fake recorded in
// FAKECODEX_THREAD_LOG. Instructions is nil when the key was omitted.
type threadOpen struct {
	Method string
	Cwd    string
	// Instructions is the developerInstructions sent, nil when absent.
	Instructions *string
}

func readThreadLog(t *testing.T, path string) []threadOpen {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var opens []threadOpen
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var e struct {
			Method string `json:"method"`
			Params struct {
				Cwd                   string  `json:"cwd"`
				DeveloperInstructions *string `json:"developerInstructions"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("thread log line %q: %v", line, err)
		}
		opens = append(opens, threadOpen{e.Method, e.Params.Cwd, e.Params.DeveloperInstructions})
	}
	return opens
}

// awaitThreadOpen waits for the nth (1-based) thread open sent from dir and
// returns it.
func awaitThreadOpen(t *testing.T, path, dir string, n int) threadOpen {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var mine []threadOpen
		for _, o := range readThreadLog(t, path) {
			if o.Cwd == dir {
				mine = append(mine, o)
			}
		}
		if len(mine) >= n {
			if len(mine) > n {
				t.Fatalf("thread opens from %s = %d, want %d", dir, len(mine), n)
			}
			return mine[n-1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for thread open %d from %s (have %d)", n, dir, len(mine))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func instructionsOf(o threadOpen) string {
	if o.Instructions == nil {
		return "<none>"
	}
	return *o.Instructions
}

func TestCodexPromptFile(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--model", "luna"}, ""},
		{[]string{"--append-system-prompt-file", "/p/a"}, "/p/a"},
		{[]string{"--append-system-prompt-file=/p/b"}, "/p/b"},
		{[]string{"--append-system-prompt-file", "/p/a", "--settings", "s", "--append-system-prompt-file=/p/c"}, "/p/c"},
		{[]string{"--append-system-prompt-file"}, ""},
	}
	for _, tc := range tests {
		if got := codexPromptFile(tc.args); got != tc.want {
			t.Errorf("codexPromptFile(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// TestCodexRunner_PromptFileReadEachSpawn (#2662): every thread open carries
// the prompt file's bytes as of that spawn. An empty, missing, oversize or
// non-regular file sends no developerInstructions key and never stops the
// client binding; no log line carries the prompt.
func TestCodexRunner_PromptFileReadEachSpawn(t *testing.T) {
	threadLog := filepath.Join(t.TempDir(), "threads.jsonl")
	t.Setenv("FAKECODEX_THREAD_LOG", threadLog)
	promptPath := filepath.Join(t.TempDir(), "system-prompt.md")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(promptPath, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("first prompt ORANGE")
	var logs lockedBuffer
	dir := t.TempDir()
	h := &codexHarnessT{}
	h.r = newCodexRunner(codexRunnerConfig{
		Binary:     fakeCodexBin(t),
		Home:       t.TempDir(),
		Dir:        dir,
		Tag:        newStreamSessionTag("s-1"),
		PromptFile: promptPath,
		Backoff:    10 * time.Millisecond,
		Log:        slog.New(slog.NewTextHandler(&logs, nil)),
	})
	h.run(t)
	client, _ := h.bound(t, nil)

	if o := awaitThreadOpen(t, threadLog, dir, 1); o.Method != "thread/start" || instructionsOf(o) != "first prompt ORANGE" {
		t.Fatalf("first open = %s %q, want thread/start with the file's bytes", o.Method, instructionsOf(o))
	}

	steps := []struct {
		name    string
		prepare func()
		want    string
	}{
		{"rewritten", func() { write("second prompt INDIGO") }, "second prompt INDIGO"},
		{"empty", func() { write("") }, "<none>"},
		{"missing", func() {
			if err := os.Remove(promptPath); err != nil {
				t.Fatal(err)
			}
		}, "<none>"},
		{"oversize", func() { write(strings.Repeat("x", codexMaxPrompt+1)) }, "<none>"},
		{"fifo", func() {
			if err := os.Remove(promptPath); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(promptPath, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "<none>"},
		{"restored", func() {
			if err := os.Remove(promptPath); err != nil {
				t.Fatal(err)
			}
			write(strings.Repeat("y", codexMaxPrompt))
		}, strings.Repeat("y", codexMaxPrompt)},
	}
	for i, st := range steps {
		st.prepare()
		h.r.Restart(nil)
		client, _ = h.bound(t, client)
		o := awaitThreadOpen(t, threadLog, dir, i+2)
		if o.Method != "thread/resume" {
			t.Errorf("%s: open = %s, want thread/resume", st.name, o.Method)
		}
		if got := instructionsOf(o); got != st.want {
			t.Errorf("%s: developerInstructions = %.40q (len %d), want %.40q (len %d)", st.name, got, len(got), st.want, len(st.want))
		}
	}

	out := string(logs.BytesCopy())
	if n := strings.Count(out, "codex: system prompt not read"); n != 3 {
		t.Errorf("prompt-read warnings = %d, want 3 (missing, oversize, fifo):\n%s", n, out)
	}
	for _, secret := range []string{"ORANGE", "INDIGO", "yyyy"} {
		if strings.Contains(out, secret) {
			t.Errorf("a log line carries prompt text %q:\n%s", secret, out)
		}
	}
}

// TestCodexRunner_ConversationPromptReachesThread (#2662): through the pool and
// the production factory, a Codex conversation's thread opens carry the
// composed prompt: the operator prompt stored before the first message on
// thread/start; a new prompt and the conversation's handoff note on the fresh
// thread a new_session starts; a prompt changed while evicted on the resume.
// Neither the prompts nor the note reach the daemon log.
func TestCodexRunner_ConversationPromptReachesThread(t *testing.T) {
	threadLog := filepath.Join(t.TempDir(), "threads.jsonl")
	t.Setenv("FAKECODEX_THREAD_LOG", threadLog)
	convID, err := conversations.NewID()
	if err != nil {
		t.Fatal(err)
	}
	first, second, third := "Answer only with ORANGE.", "Answer only with INDIGO.", "Answer only with TEAL."
	const note = "Handoff note: the MAGENTA migration is half done."
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: convID, Cwd: t.TempDir(), SystemPrompt: &first})

	var logs lockedBuffer
	regDir := t.TempDir()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0], WorkDir: t.TempDir()},
		RegistryPath: filepath.Join(regDir, "sessions.json"),
		RunnerFactory: newCodexRunnerFactory(codexHarness{
			bin: fakeCodexBin(t), home: filepath.Join(t.TempDir(), "codex-home"), sink: newStreamTurnSink(0, nil),
		}),
		Logger:                    slog.New(slog.NewTextHandler(&logs, nil)),
		ConversationsRegistry:     reg,
		ConversationsRegistryPath: filepath.Join(regDir, "conversations.json"),
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = pool.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	spawnDir := t.TempDir()
	id, err := pool.MintAs(string(convID), spawnDir, harnessCodex)
	if err != nil {
		t.Fatalf("MintAs: %v", err)
	}
	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	sess, err := pool.Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	h := &codexHarnessT{r: sess.Runner().(*codexRunner)}
	client, _ := h.bound(t, nil)
	dir, promptPath := h.r.cfg.Dir, h.r.cfg.PromptFile
	if promptPath == "" {
		t.Fatal("the factory set no prompt file from the pool's argv")
	}
	fileNow := func() string {
		t.Helper()
		raw, err := os.ReadFile(promptPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	check := func(o threadOpen, method string, want ...string) {
		t.Helper()
		got := instructionsOf(o)
		if o.Method != method {
			t.Errorf("open = %s, want %s", o.Method, method)
		}
		if got != fileNow() {
			t.Errorf("developerInstructions = %q, want the composed file %q", got, fileNow())
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("developerInstructions = %q, want it to carry %q", got, w)
			}
		}
	}
	check(awaitThreadOpen(t, threadLog, dir, 1), "thread/start", first)

	if err := reg.SetSystemPrompt(convID, &second); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.WriteHandoffNote(convID, note); err != nil {
		t.Fatalf("WriteHandoffNote: %v", err)
	}
	if err := startFreshRunner(h.r, id, "", pool.RotateForNewSession, nil); err != nil {
		t.Fatalf("new_session rotation: %v", err)
	}
	client, _ = h.bound(t, client)
	check(awaitThreadOpen(t, threadLog, dir, 2), "thread/start", second, note)

	newID := sessions.SessionID(h.r.cfg.Tag.ID())
	rotated, err := pool.Lookup(newID)
	if err != nil {
		t.Fatalf("Lookup(rotated): %v", err)
	}
	if err := reg.SetSystemPrompt(convID, &third); err != nil {
		t.Fatal(err)
	}
	if err := rotated.Evict(ctx); err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if err := pool.Activate(ctx, newID); err != nil {
		t.Fatalf("re-Activate: %v", err)
	}
	h.bound(t, client)
	check(awaitThreadOpen(t, threadLog, dir, 3), "thread/resume", third)

	out := string(logs.BytesCopy())
	for _, secret := range []string{"ORANGE", "INDIGO", "TEAL", "MAGENTA"} {
		if strings.Contains(out, secret) {
			t.Errorf("a daemon log line carries prompt or note text %q", secret)
		}
	}
}
