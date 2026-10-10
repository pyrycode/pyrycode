package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func testTranscriptEntry(id uint64, typ, payload, session string) history.Entry {
	e := history.Entry{ID: id, Type: typ, Payload: json.RawMessage(payload), TS: time.Unix(int64(id), 0).UTC()}
	if session != "" {
		e.Session = &history.SessionProvenance{Kind: "claude", SessionID: session}
	}
	return e
}
func testTranscriptView(t *testing.T, entries []history.Entry) map[string]string {
	t.Helper()
	w := newMemoryTranscriptReader(nil, "chat")
	testMemoryMust(t, w.feed(entries))
	return w.files(conversations.Conversation{ID: "chat"})
}
func TestMemoryTranscriptReplay(t *testing.T) {
	text := "  complete\n\n```\nend  \n"
	raw, _ := json.Marshal(map[string]string{"role": "user", "text": text})
	entries := []history.Entry{
		testTranscriptEntry(1, "send_accepted", `{"text":"queued","accepted_at":"2026-01-01T00:00:00Z"}`, "old"),
		testTranscriptEntry(2, "message", string(raw), "receiving"),
		testTranscriptEntry(3, "send_delivered", `{"accepted_entry_id":1,"delivery_entry_id":2,"reason":"delivered","occurred_at":"2026-01-01T00:00:00Z"}`, "other"),
	}
	before := testTranscriptView(t, entries[:2])
	after := testTranscriptView(t, entries)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("reconciliation changed transcript: %v / %v", before, after)
	}
	for _, body := range after {
		if !strings.Contains(body, text) || strings.Count(body, "Message: chat/2") != 1 || !strings.Contains(body, "1970-01-01T00:00:02Z") {
			t.Fatalf("delivery identity/text/timestamp: %q", body)
		}
	}
	w := newMemoryTranscriptReader(nil, "chat")
	for _, e := range entries {
		testMemoryMust(t, w.feed([]history.Entry{e}))
	}
	if !reflect.DeepEqual(after, w.files(conversations.Conversation{ID: "chat"})) {
		t.Fatal("chunked replay differs")
	}
	for _, e := range []history.Entry{
		testTranscriptEntry(4, "message", `{"role":"assistant","text":"excluded"}`, "receiving"),
		testTranscriptEntry(5, "send_accepted", `{"text":"excluded","accepted_at":"2026-01-01T00:00:00Z"}`, "receiving"),
		testTranscriptEntry(6, "send_lost", `{"accepted_entry_id":5,"reason":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"}`, "receiving"),
		testTranscriptEntry(7, "message", `{"role":"user","text":"excluded"}`, ""),
		testTranscriptEntry(8, "send_accepted", `{"text":"excluded","accepted_at":"2026-01-01T00:00:00Z"}`, "receiving"),
		testTranscriptEntry(9, "send_dropped", `{"accepted_entry_id":8,"reason":"removed","occurred_at":"2026-01-01T00:00:00Z"}`, "receiving"),
		testTranscriptEntry(10, "memory_capture", `{"text":"excluded"}`, "receiving"),
	} {
		if e.ID == 7 {
			e.Session = &history.SessionProvenance{Kind: "none"}
		}
		testMemoryMust(t, w.feed([]history.Entry{e}))
	}
	if !reflect.DeepEqual(after, w.files(conversations.Conversation{ID: "chat"})) {
		t.Fatal("ineligible text exported")
	}
}
func TestMemoryTranscriptSessions(t *testing.T) {
	divider := func(id uint64, cause, prev, next string) history.Entry {
		return testTranscriptEntry(id, "session_divider", fmt.Sprintf(`{"cause":%q,"previous_session_id":%q,"new_session_id":%q,"previous_agent":"claude","next_agent":"codex","occurred_at":"2026-01-01T00:00:00Z"}`, cause, prev, next), "")
	}
	entries := []history.Entry{
		testTranscriptEntry(1, "message", `{"role":"user","text":"unknown"}`, ""),
		testTranscriptEntry(2, "message", `{"role":"user","text":"first"}`, "A"),
		divider(3, "operator_reset", "A", "B"),
		testTranscriptEntry(4, "session_transition", `{"reason":"clear","previous_session_id":"A","new_session_id":"B","occurred_at":"2026-01-01T00:00:00Z"}`, ""),
		testTranscriptEntry(5, "message", `{"role":"user","text":"successor"}`, ""),
		divider(6, "idle_sleep", "B", ""), divider(7, "daemon_restart", "B", ""),
		testTranscriptEntry(8, "message", `{"role":"user","text":"late"}`, "A"),
		divider(9, "recovery", "B", "B"),
	}
	files := testTranscriptView(t, entries)
	if len(files) != 3 {
		t.Fatalf("groups: %v", files)
	}
	for _, body := range files {
		switch {
		case strings.Contains(body, "first"):
			if !strings.Contains(body, "State: closed\nClosing entry: 3\n") || !strings.Contains(body, "Last delivered entry: 8\n") || !strings.Contains(body, "late") || strings.Contains(body, "successor") {
				t.Fatal(body)
			}
		case strings.Contains(body, "successor"):
			if !strings.Contains(body, "State: open\n") || strings.Contains(body, "Closing entry:") || !strings.Contains(body, "Agent: codex\n") {
				t.Fatal(body)
			}
		case strings.Contains(body, "unknown"):
			if !strings.Contains(body, "Session: unknown\n") || !strings.Contains(body, "State: open\n") {
				t.Fatal(body)
			}
		default:
			t.Fatal(body)
		}
	}
	entries = append(entries, divider(10, "agent_switch", "B", "A"), testTranscriptEntry(11, "message", `{"role":"user","text":"reused"}`, "A"), divider(12, "daemon_restart", "A", "C"))
	files = testTranscriptView(t, entries)
	if len(files) != 5 {
		t.Fatalf("recorded routing replacement lost: %v", files)
	}
	if len(testTranscriptView(t, []history.Entry{entries[0], testTranscriptEntry(2, "message", `{"role":"user","text":"named"}`, "unknown/0")})) != 2 {
		t.Fatal("opaque routing ID collided with unknown namespace")
	}
}
func TestMemoryTranscriptStorage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory", "recent-transcripts")
	name := strings.Repeat("a", 64) + ".md"
	testMemoryMust(t, publishMemoryTranscript(context.Background(), dir, name, "complete", nil))
	data, err := os.ReadFile(filepath.Join(dir, name))
	testMemoryMust(t, err)
	if string(data) != "complete" {
		t.Fatal(string(data))
	}
	for _, path := range []string{filepath.Dir(dir), dir, filepath.Join(dir, name)} {
		info, err := os.Stat(path)
		testMemoryMust(t, err)
		want := os.FileMode(0700)
		if !info.IsDir() {
			want = 0600
		}
		if info.Mode().Perm() != want {
			t.Fatal(info.Mode())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = publishMemoryTranscript(ctx, dir, name, "partial", func() { cancel() })
	if err == nil {
		t.Fatal("interrupted publication succeeded")
	}
	data, err = os.ReadFile(filepath.Join(dir, name))
	testMemoryMust(t, err)
	if string(data) != "complete" {
		t.Fatal("old file lost")
	}
	testMemoryMust(t, publishMemoryTranscript(context.Background(), dir, name, "retry", nil))
	testMemoryCheck(t, testTranscriptDisk(t, dir)[name] == "retry", "retry did not publish")
	victim := filepath.Join(t.TempDir(), "vault.md")
	testMemoryMust(t, os.WriteFile(victim, []byte("untouched"), 0600))
	testMemoryMust(t, os.Remove(filepath.Join(dir, name)))
	testMemoryMust(t, os.Symlink(victim, filepath.Join(dir, name)))
	if publishMemoryTranscript(context.Background(), dir, name, "bad", nil) == nil {
		t.Fatal("symlink accepted")
	}
	if publishMemoryTranscript(context.Background(), dir, "../vault.md", "bad", nil) == nil {
		t.Fatal("unsafe name accepted")
	}
	testMemoryMust(t, os.Symlink(filepath.Dir(victim), filepath.Join(dir, "redirect")))
	if publishMemoryTranscript(context.Background(), filepath.Join(dir, "redirect", "child"), name, "bad", nil) == nil {
		t.Fatal("symlink directory accepted")
	}
	data, err = os.ReadFile(victim)
	testMemoryMust(t, err)
	if !bytes.Equal(data, []byte("untouched")) {
		t.Fatal("vault changed")
	}
}

func testTranscriptDisk(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	names, err := filepath.Glob(filepath.Join(dir, "*.md"))
	testMemoryMust(t, err)
	for _, name := range names {
		data, err := os.ReadFile(name)
		testMemoryMust(t, err)
		files[filepath.Base(name)] = string(data)
	}
	return files
}
func TestMemoryTranscriptWorker(t *testing.T) {
	home := testManagedHome(t)
	base := filepath.Join(home, "vault")
	testMemoryMust(t, os.Mkdir(base, 0700))
	settings := testManagedMemory()
	reg, err := conversations.Load(filepath.Join(home, "registry.json"))
	testMemoryMust(t, err)
	id1, err := conversations.NewID()
	testMemoryMust(t, err)
	id2, err := conversations.NewID()
	testMemoryMust(t, err)
	title := "../../title\n```"
	reg.Create(conversations.Conversation{ID: id1, Name: &title, CurrentSessionID: "wrong-binding"})
	root := filepath.Join(home, "history-source")
	h := history.New(root)
	appendEntry := func(id conversations.ConversationID, typ, payload, kind, session string) uint64 {
		t.Helper()
		meta := history.Metadata{}
		if kind != "" {
			meta.Session = &history.SessionProvenance{Kind: kind, SessionID: session}
		}
		n, err := h.AppendWithMetadata(id, typ, json.RawMessage(payload), time.Unix(50, 0).UTC(), meta)
		testMemoryMust(t, err)
		return n
	}
	appendEntry(id1, "send_accepted", `{"text":"queued","accepted_at":"2026-01-01T00:00:00Z"}`, "claude", "A")
	appendEntry(id1, "message", `{"role":"user","text":"delivered"}`, "claude", "A")
	ticks := make(chan time.Time, 10)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	stop := startMemoryTranscripts(context.Background(), &settings, base, h, reg, log, memoryTranscriptHooks{ticks: ticks, beforeFeed: func(ctx context.Context) error {
		once.Do(func() {
			close(entered)
			select {
			case <-ctx.Done():
			case <-release:
			}
		})
		return ctx.Err()
	}})
	t.Cleanup(func() { stop() })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("replay did not start")
	}
	appendEntry(id1, "message", `{"role":"user","text":"during replay"}`, "claude", "A")
	close(release)
	dir := filepath.Join(home, ".pyry", "memory", "recent-transcripts")
	wait := func(check func(map[string]string) bool) {
		t.Helper()
		testShadowWait(t, func() bool { return check(testTranscriptDisk(t, dir)) })
	}
	contains := func(text string) func(map[string]string) bool {
		return func(files map[string]string) bool {
			for _, body := range files {
				if strings.Contains(body, text) {
					return true
				}
			}
			return false
		}
	}
	wait(contains("delivered"))
	ticks <- time.Unix(10, 0)
	wait(contains("during replay"))
	before := testTranscriptDisk(t, dir)
	appendEntry(id1, "send_delivered", `{"accepted_entry_id":1,"delivery_entry_id":2,"reason":"delivered","occurred_at":"2026-01-01T00:00:00Z"}`, "claude", "A")
	ticks <- time.Unix(20, 0)
	// Discovery and reconciliation use the same production scheduling loop.
	reg.Create(conversations.Conversation{ID: id2})
	appendEntry(id2, "message", `{"role":"user","text":"codex chat"}`, "codex", "opaque/../../id")
	ticks <- time.Unix(30, 0)
	wait(contains("codex chat"))
	for name, body := range before {
		if testTranscriptDisk(t, dir)[name] != body {
			t.Fatal("reconciliation changed delivery identity")
		}
	}
	stop()
	stop()
	// Direct writers do not send this Store's Tail notifications.
	stop = startMemoryTranscripts(context.Background(), &settings, base, h, reg, log, memoryTranscriptHooks{ticks: ticks})
	direct := history.New(root)
	_, err = direct.AppendWithMetadata(id2, "message", json.RawMessage(`{"role":"user","text":"direct writer"}`), time.Unix(60, 0), history.Metadata{Session: &history.SessionProvenance{Kind: "codex", SessionID: "opaque/../../id"}})
	testMemoryMust(t, err)
	source := make(map[string]string)
	paths, err := filepath.Glob(filepath.Join(root, "conversations", "*", "history", "*.jsonl"))
	testMemoryMust(t, err)
	for _, path := range paths {
		b, err := os.ReadFile(path)
		testMemoryMust(t, err)
		source[path] = string(b)
	}
	ticks <- time.Unix(40, 0)
	wait(contains("direct writer"))
	stop()
	complete := testTranscriptDisk(t, dir)
	for name := range complete {
		testMemoryMust(t, os.Remove(filepath.Join(dir, name)))
	}
	stop = startMemoryTranscripts(context.Background(), &settings, base, history.New(root), reg, log, memoryTranscriptHooks{ticks: ticks})
	wait(func(files map[string]string) bool { return reflect.DeepEqual(complete, files) })
	stop()
	for path, body := range source {
		b, err := os.ReadFile(path)
		testMemoryMust(t, err)
		testMemoryCheck(t, string(b) == body, "source history changed")
	}
	if memoryTranscriptInterval > 60*time.Second {
		t.Fatal("schedule exceeds bound")
	}
	// Both cancellation during replay and a held publication must join.
	for _, publication := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		entered := make(chan struct{}, 2)
		left := make(chan struct{}, 2)
		hook := memoryTranscriptHooks{ticks: ticks}
		gate := func() { entered <- struct{}{}; <-ctx.Done(); left <- struct{}{} }
		if publication {
			hook.beforeRename = gate
		} else {
			hook.beforeFeed = func(ctx context.Context) error { gate(); return ctx.Err() }
		}
		stop = startMemoryTranscripts(ctx, &settings, base, h, reg, log, hook)
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("worker gate not reached")
		}
		cancel()
		stop()
		select {
		case <-left:
		default:
			t.Fatal("shutdown did not join worker")
		}
	}
}
func TestMemoryTranscriptActivation(t *testing.T) {
	home := testManagedHome(t)
	reg, err := conversations.Load(filepath.Join(home, "registry.json"))
	testMemoryMust(t, err)
	id, err := conversations.NewID()
	testMemoryMust(t, err)
	reg.Create(conversations.Conversation{ID: id})
	h := history.New(filepath.Join(home, "history"))
	_, err = h.Append(id, "message", json.RawMessage(`{"role":"user","text":"secret text"}`), time.Now())
	testMemoryMust(t, err)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	startMemoryTranscripts(context.Background(), nil, home, h, reg, log)()
	bad := testManagedMemory()
	bad.Capture.Model = ""
	startMemoryTranscripts(context.Background(), &bad, home, h, reg, log)()
	if _, err = os.Stat(filepath.Join(home, ".pyry", "memory")); !os.IsNotExist(err) {
		t.Fatal("inactive settings created storage")
	}
	if !strings.Contains(logs.String(), "reason=settings") || strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), home) {
		t.Fatal("diagnostic leak")
	}
	base := filepath.Join(home, "vault")
	testMemoryMust(t, os.Mkdir(base, 0700))
	alias := filepath.Join(home, ".pyry", "memory", "recent-transcripts")
	testMemoryMust(t, os.MkdirAll(filepath.Dir(alias), 0700))
	testMemoryMust(t, os.Symlink(filepath.Join(home, "history"), alias))
	valid := testManagedMemory()
	logs.Reset()
	startMemoryTranscripts(context.Background(), &valid, base, h, reg, log)()
	testMemoryCheck(t, strings.Contains(logs.String(), "reason=storage"), "redirected transcript storage accepted")
}
