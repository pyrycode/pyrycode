package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

type testTranscriptFeed struct {
	ctx     context.Context
	release chan struct{}
}

type testTranscriptGate struct {
	t               *testing.T
	root, dir, base string
	settings        config.MemorySettings
	reg             *conversations.Registry
	feeds           chan testTranscriptFeed
	publications    chan chan struct{}
	ticks           chan time.Time
	abort           chan struct{}
	stop            func()
}

func testNewTranscriptGate(t *testing.T) *testTranscriptGate {
	t.Helper()
	home := testManagedHome(t)
	g := &testTranscriptGate{t: t, root: filepath.Join(home, "history"),
		dir: filepath.Join(home, ".pyry", "memory", "recent-transcripts"), base: filepath.Join(home, "vault"),
		settings: testManagedMemory(), feeds: make(chan testTranscriptFeed, 10),
		publications: make(chan chan struct{}, 10), ticks: make(chan time.Time), abort: make(chan struct{})}
	testMemoryMust(t, os.Mkdir(g.base, 0700))
	var err error
	g.reg, err = conversations.Load(filepath.Join(home, "registry.json"))
	testMemoryMust(t, err)
	t.Cleanup(func() {
		close(g.abort)
		if g.stop != nil {
			g.stop()
		}
	})
	return g
}

func (g *testTranscriptGate) start() {
	g.t.Helper()
	g.stop = startMemoryTranscripts(context.Background(), &g.settings, g.base, history.New(g.root), g.reg,
		slog.New(slog.NewTextHandler(io.Discard, nil)), memoryTranscriptHooks{ticks: g.ticks,
			beforeFeed: func(ctx context.Context) error {
				f := testTranscriptFeed{ctx: ctx, release: make(chan struct{})}
				select {
				case g.feeds <- f:
				case <-ctx.Done():
					return ctx.Err()
				}
				select {
				case <-f.release:
					return ctx.Err()
				case <-g.abort:
					return ctx.Err()
				}
			}, beforeRename: func() {
				p := make(chan struct{})
				select {
				case g.publications <- p:
				case <-g.abort:
					return
				}
				select {
				case <-p:
				case <-g.abort:
				}
			}})
}

func testTranscriptReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("transcript worker did not reach barrier")
		var zero T
		return zero
	}
}

func (g *testTranscriptGate) feed(n int) context.Context {
	g.t.Helper()
	var ctx context.Context
	for range n {
		f := testTranscriptReceive(g.t, g.feeds)
		ctx = f.ctx
		close(f.release)
	}
	return ctx
}

func (g *testTranscriptGate) tick() {
	g.t.Helper()
	select {
	case g.ticks <- time.Unix(100, 0):
	case <-time.After(5 * time.Second):
		g.t.Fatal("discovery did not consume tick")
	}
}

func (g *testTranscriptGate) publish(want map[string]string) {
	g.t.Helper()
	// Every worker is held before inspecting staged files, so no candidate is
	// still being written and no released rename can race this inspection.
	seen := make(map[string]string)
	workers := len(g.reg.List())
	for batch := 0; batch < len(want); batch += workers {
		var releases []chan struct{}
		for range workers {
			releases = append(releases, testTranscriptReceive(g.t, g.publications))
		}
		paths, err := filepath.Glob(filepath.Join(g.dir, ".transcript-*"))
		testMemoryMust(g.t, err)
		if len(paths) != workers {
			g.t.Fatalf("staged %d files for %d held workers", len(paths), workers)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			testMemoryMust(g.t, err)
			_, rest, ok := strings.Cut(string(data), "\nTranscript: ")
			if !ok {
				g.t.Fatal("staged transcript lacks identity")
			}
			stem, _, _ := strings.Cut(rest, "\n")
			name := stem + ".md"
			if string(data) != want[name] {
				g.t.Fatalf("unexpected staged transcript %s:\n%s\nwant:\n%s", name, data, want[name])
			}
			if _, exists := seen[name]; exists {
				g.t.Fatal("worker staged a duplicate file", name)
			}
			seen[name] = string(data)
		}
		for _, release := range releases {
			close(release)
		}
	}
	if !reflect.DeepEqual(seen, want) {
		g.t.Fatalf("staged files differ: got %v, want %v", seen, want)
	}
	testShadowWait(g.t, func() bool { return reflect.DeepEqual(testTranscriptDisk(g.t, g.dir), want) })
}

func testTranscriptAppend(t *testing.T, h *history.Store, id conversations.ConversationID, kind, session, fact string) uint64 {
	t.Helper()
	typ, payload, _ := strings.Cut(fact, " ")
	meta := history.Metadata{}
	if kind != "" {
		meta.Session = &history.SessionProvenance{Kind: kind, SessionID: session}
	}
	last, err := h.LatestEntryID(id)
	testMemoryMust(t, err)
	e := testTranscriptEntry(last+1, typ, payload, session)
	n, err := h.AppendWithMetadata(id, typ, e.Payload, e.TS, meta)
	testMemoryMust(t, err)
	if n != e.ID {
		t.Fatalf("fixture identity: got %d, want %d", n, e.ID)
	}
	return n
}

type testTranscriptRow struct {
	id                   uint64
	speaker, agent, text string
}

// Expectations describe recorded rows, not Fold items or exporter snapshots.
func testTranscriptExpected(id conversations.ConversationID, session string, start, last, text, closed uint64, rows ...testTranscriptRow) (string, string) {
	unknown := session == ""
	identitySession := session
	if unknown {
		identitySession = fmt.Sprintf("unknown/%d", start)
	}
	identity, _ := json.Marshal([]any{string(id), identitySession, start, unknown})
	stem := fmt.Sprintf("%x", sha256.Sum256(identity))
	label := fmt.Sprintf("%q", session)
	if unknown {
		label = "unknown"
	}
	body := fmt.Sprintf("# Recent conversation transcript\n\nConversation: %q\nTitle: null\nTranscript: %s\nSession: %s\n", id, stem, label)
	if closed == 0 {
		body += "State: open\n"
	} else {
		body += fmt.Sprintf("State: closed\nClosing entry: %d\n", closed)
	}
	body += fmt.Sprintf("Last delivered entry: %d\nLast text entry: %d\n", last, text)
	for _, row := range rows {
		title := "User"
		if row.speaker == "assistant" {
			title = "Assistant"
		}
		body += fmt.Sprintf("\n## %s\nMessage: %s/%d\nTimestamp: %s\nSpeaker: %s\nAgent: %s\n\n", title, id, row.id,
			time.Unix(int64(row.id), 0).UTC().Format(time.RFC3339Nano), row.speaker, row.agent)
		if row.speaker == "assistant" {
			body += "Session: " + label + "\n\n"
		}
		body += "```text\n" + row.text + "\n```\n"
	}
	return stem + ".md", body
}

func TestMemoryTranscriptPersistedReplay(t *testing.T) {
	g := testNewTranscriptGate(t)
	h := history.New(g.root)
	ids := []conversations.ConversationID{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}
	kinds := []string{"claude", "codex"}
	successorKinds := []string{"claude", "claude"}
	for _, id := range ids {
		g.reg.Create(conversations.Conversation{ID: id, CurrentSessionID: "wrong-binding"})
	}
	appendBoth := func(fact string) {
		for i, id := range ids {
			testTranscriptAppend(t, h, id, kinds[i], "A", fact)
		}
	}
	for _, id := range ids {
		testTranscriptAppend(t, h, id, "", "", `message {"role":"user","text":"unresolved"}`)
	}
	for _, fact := range []string{
		`send_accepted {"text":"queued","accepted_at":"2026-01-01T00:00:00Z"}`,
		`message {"role":"user","text":"  question\n"}`, replyOpen,
		`assistant_delta {"turn_id":"t","text":"  before\n"}`,
		`assistant_delta {"turn_id":"t","text":"\n"}`,
		`tool_use {"turn_id":"t","tool_use_id":"call","name":"Read","input_summary":"excluded tool"}`,
	} {
		appendBoth(fact)
	}
	// Put the tool result and second text run in a different persisted callback.
	for range history.MaxPageEntries - 7 {
		appendBoth(`status {"text":"excluded status"}`)
	}
	for _, fact := range []string{
		`tool_result {"turn_id":"t","tool_use_id":"call","is_error":false,"text":"excluded result"}`,
		`assistant_delta {"turn_id":"t","text":"after  \n"}`,
		`thinking_delta {"turn_id":"t","text":"excluded reasoning"}`,
		`assistant_delta {"turn_id":"t","parent_tool_use_id":"child","text":"excluded child"}`,
		`memory_capture {"text":"excluded capture"}`,
		`message {"role":"system","text":"excluded injected"}`,
		`turn_end {"turn_id":"other","stop_reason":"end_turn"}`,
	} {
		appendBoth(fact)
	}
	for i, id := range ids {
		testTranscriptAppend(t, h, id, kinds[i], "foreign", replyEnd)
		testTranscriptAppend(t, h, id, "", "", replyEnd)
		testTranscriptAppend(t, h, id, kinds[1-i], "A", replyEnd)
		for _, fact := range []string{
			`assistant_delta {"turn_id":"legacy","text":"excluded legacy"}`,
			`session_divider {"cause":"idle_sleep","previous_session_id":"A","occurred_at":"2026-01-01T00:00:00Z"}`,
			`turn_end {"turn_id":"legacy","stop_reason":"end_turn"}`,
		} {
			testTranscriptAppend(t, h, id, "", "", fact)
		}
	}
	want := make(map[string]string)
	for i, id := range ids {
		name, body := testTranscriptExpected(id, "", 0, 1, 1, 0, testTranscriptRow{1, "user", "unknown", "unresolved"})
		want[name] = body
		name, body = testTranscriptExpected(id, "A", 0, 3, 3, 0, testTranscriptRow{3, "user", kinds[i], "  question\n"})
		want[name] = body
	}
	g.start()
	// Both first chunks have been read but not folded. Commit while replay is held.
	first := []testTranscriptFeed{testTranscriptReceive(t, g.feeds), testTranscriptReceive(t, g.feeds)}
	for i, id := range ids {
		testTranscriptAppend(t, h, id, kinds[i], "A", `send_delivered {"accepted_entry_id":2,"delivery_entry_id":3,"reason":"delivered","occurred_at":"2026-01-01T00:00:00Z"}`)
		terminal := `turn_end {"turn_id":"t","stop_reason":"error","is_error":true}`
		if i == 1 {
			terminal = `turn_end {"turn_id":"t","stop_reason":"cancelled","terminal_reason":"cancelled"}`
		}
		testTranscriptAppend(t, h, id, kinds[i], "A", terminal)
	}
	for _, f := range first {
		close(f.release)
	}
	g.feed(2)       // second bounded persisted callbacks, not manual feed slicing
	g.publish(want) // unrelated endings and tools must leave both replies withheld
	g.tick()
	g.feed(2)
	for i, id := range ids {
		name, body := testTranscriptExpected(id, "A", 0, 3, uint64(history.MaxPageEntries+2), 0,
			testTranscriptRow{3, "user", kinds[i], "  question\n"}, testTranscriptRow{5, "assistant", kinds[i], "  before\n\n"},
			testTranscriptRow{uint64(history.MaxPageEntries + 2), "assistant", kinds[i], "after  \n"})
		want[name] = body
	}
	// Publication is held while another Store commits replacements and new work.
	p := testTranscriptReceive(t, g.publications)
	direct := history.New(g.root)
	boundaries, users := make([]uint64, 2), make([]uint64, 2)
	for i, id := range ids {
		cause := "operator_reset"
		if i == 1 {
			cause = "agent_switch"
		}
		boundaries[i] = testTranscriptAppend(t, direct, id, "", "", fmt.Sprintf(`session_divider {"cause":%q,"previous_session_id":"A","new_session_id":"B","previous_agent":%q,"next_agent":%q,"occurred_at":"2026-01-01T00:00:00Z"}`, cause, kinds[i], successorKinds[i]))
		users[i] = testTranscriptAppend(t, direct, id, successorKinds[i], "B", `message {"role":"user","text":"successor question"}`)
		opening := testTranscriptAppend(t, direct, id, successorKinds[i], "B", replyOpen)
		testTranscriptAppend(t, direct, id, successorKinds[i], "B", `assistant_delta {"turn_id":"t","text":"excluded interrupted"}`)
		testTranscriptAppend(t, direct, id, successorKinds[i], "B", fmt.Sprintf(`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":%d,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, opening))
		testTranscriptAppend(t, direct, id, successorKinds[i], "B", replyEnd)
		// The predecessor terminal cannot qualify an explicitly reopened turn.
		users[i] = testTranscriptAppend(t, direct, id, successorKinds[i], "B", `message {"role":"user","text":"fresh question"}`)
		testTranscriptAppend(t, direct, id, successorKinds[i], "B", replyOpen)
		testTranscriptAppend(t, direct, id, successorKinds[i], "B", `assistant_delta {"turn_id":"t","text":"excluded fresh generation"}`)
	}
	// Return this held callback to the queue so publish checks the same cycle.
	g.publications <- p
	g.publish(want)
	g.tick()
	g.feed(2)
	for i, id := range ids {
		name, body := testTranscriptExpected(id, "A", 0, 3, uint64(history.MaxPageEntries+2), boundaries[i],
			testTranscriptRow{3, "user", kinds[i], "  question\n"}, testTranscriptRow{5, "assistant", kinds[i], "  before\n\n"},
			testTranscriptRow{uint64(history.MaxPageEntries + 2), "assistant", kinds[i], "after  \n"})
		want[name] = body
		name, body = testTranscriptExpected(id, "B", boundaries[i], users[i], users[i], 0,
			testTranscriptRow{boundaries[i] + 1, "user", successorKinds[i], "successor question"},
			testTranscriptRow{users[i], "user", successorKinds[i], "fresh question"})
		want[name] = body
	}
	g.publish(want)
	late := make([]uint64, 2)
	for pass := range 2 {
		for i, id := range ids {
			// Reopen a Store for each writing phase, without stale append cursors.
			n := testTranscriptAppend(t, history.New(g.root), id, kinds[i], "A", `assistant_delta {"turn_id":"t","text":" late\n"}`)
			if pass == 0 {
				late[i] = n
			}
			name, body := testTranscriptExpected(id, "A", 0, 3, n, boundaries[i],
				testTranscriptRow{3, "user", kinds[i], "  question\n"}, testTranscriptRow{5, "assistant", kinds[i], "  before\n\n"},
				testTranscriptRow{uint64(history.MaxPageEntries + 2), "assistant", kinds[i], "after  \n"},
				testTranscriptRow{late[i], "assistant", kinds[i], strings.Repeat(" late\n", pass+1)})
			want[name] = body
		}
		g.tick()
		g.feed(2)
		g.publish(want) // successor files and predecessor closing entry are unchanged
	}
	for i, id := range ids {
		writer := history.New(g.root)
		for _, fact := range []string{
			`tool_use {"turn_id":"t","tool_use_id":"trailing","name":"Read","input_summary":"excluded trailing tool"}`,
			`tool_result {"turn_id":"t","tool_use_id":"trailing","is_error":false}`,
			`status {"text":"excluded trailing status"}`, replyEnd,
		} {
			testTranscriptAppend(t, writer, id, kinds[i], "A", fact)
		}
	}
	g.tick()
	g.feed(2)
	g.publish(want) // terminal/tool/status revisions alone cannot advance coverage
	source := testTranscriptSource(t, g.root)
	g.stop()
	// Independently specified complete files must also match uninterrupted replay.
	reconstructed := make(map[string]string)
	for _, id := range ids {
		w := newMemoryTranscriptReader(history.New(g.root), id)
		testMemoryMust(t, w.reader.Walk(context.Background(), ^uint64(0), w.feed))
		for name, body := range w.files(conversations.Conversation{ID: id}) {
			reconstructed[name] = body
		}
	}
	if !reflect.DeepEqual(reconstructed, want) {
		t.Fatal("uninterrupted reconstruction differs from complete expected files")
	}
	for name := range want {
		testMemoryMust(t, os.Remove(filepath.Join(g.dir, name)))
	}
	g.start()
	g.feed(4)
	g.publish(want)
	g.stop()
	if !reflect.DeepEqual(testTranscriptSource(t, g.root), source) {
		t.Fatal("chunked reconstruction modified source-history files")
	}
	if memoryTranscriptInterval > 60*time.Second {
		t.Fatal("polling interval exceeds export deadline")
	}
}

func testTranscriptSource(t *testing.T, root string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "conversations", "*", "history", "*"))
	testMemoryMust(t, err)
	if len(paths) == 0 {
		t.Fatal("source-history snapshot is empty")
	}
	source := make(map[string]string)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		testMemoryMust(t, err)
		source[path] = string(data)
	}
	return source
}

func (g *testTranscriptGate) stopHeld(ctx context.Context, release chan struct{}) {
	g.t.Helper()
	stopped := make(chan struct{})
	go func() { g.stop(); close(stopped) }()
	testTranscriptReceive(g.t, ctx.Done()) // cancellation reached the held worker
	select {
	case <-stopped:
		g.t.Fatal("stop returned before held worker joined")
	default:
	}
	close(release)
	testTranscriptReceive(g.t, stopped)
}

func TestMemoryTranscriptPublicationRecovery(t *testing.T) {
	g := testNewTranscriptGate(t)
	id := conversations.ConversationID("11111111-1111-4111-8111-111111111111")
	g.reg.Create(conversations.Conversation{ID: id, CurrentSessionID: "wrong-binding"})
	h := history.New(g.root)
	for _, fact := range []string{replyUser, replyOpen, replyText, replyEnd,
		replyOpen, `assistant_delta {"turn_id":"t","text":"excluded assistant-only wrap-up"}`, replyEnd} {
		testTranscriptAppend(t, h, id, "claude", "A", fact)
	}
	name, body := testTranscriptExpected(id, "A", 0, 1, 3, 0,
		testTranscriptRow{1, "user", "claude", "question"}, testTranscriptRow{3, "assistant", "claude", "answer"})
	previous := map[string]string{name: body}
	g.start()
	g.feed(1)
	g.publish(previous)
	// A new user permits an independently completed reply after the wrap-up.
	for _, fact := range []string{`message {"role":"user","text":"retry question"}`, replyOpen,
		`assistant_delta {"turn_id":"t","text":"  retry\n"}`, replyEnd} {
		testTranscriptAppend(t, h, id, "claude", "A", fact)
	}
	name, body = testTranscriptExpected(id, "A", 0, 8, 10, 0,
		testTranscriptRow{1, "user", "claude", "question"}, testTranscriptRow{3, "assistant", "claude", "answer"},
		testTranscriptRow{8, "user", "claude", "retry question"}, testTranscriptRow{10, "assistant", "claude", "  retry\n"})
	updated := map[string]string{name: body}
	g.tick()
	g.feed(1)
	p := testTranscriptReceive(t, g.publications)
	if !reflect.DeepEqual(testTranscriptDisk(t, g.dir), previous) {
		t.Fatal("staging changed previous complete file")
	}
	// Refuse replacement after staging while retaining the old regular file.
	path := filepath.Join(g.dir, name)
	testMemoryMust(t, os.Chmod(path, 0644))
	close(p)
	testShadowWait(t, func() bool {
		temps, err := filepath.Glob(filepath.Join(g.dir, ".transcript-*"))
		testMemoryMust(t, err)
		return len(temps) == 0
	})
	if !reflect.DeepEqual(testTranscriptDisk(t, g.dir), previous) {
		t.Fatal("failed publication lost previous complete file")
	}
	testMemoryMust(t, os.Chmod(path, 0600))
	g.tick()
	g.publish(updated) // no restart or additional commit is needed for retry

	testTranscriptAppend(t, h, id, "claude", "A", `assistant_delta {"turn_id":"t","text":"tail  "}`)
	source := testTranscriptSource(t, g.root) // fixture commits are now finished
	g.tick()
	ctx := g.feed(1)
	p = testTranscriptReceive(t, g.publications)
	g.stopHeld(ctx, p)
	if !reflect.DeepEqual(testTranscriptDisk(t, g.dir), updated) {
		t.Fatal("cancelled publication replaced previous complete file")
	}
	// Replacement replay must also be joined before a subsequent worker starts.
	g.start()
	f := testTranscriptReceive(t, g.feeds)
	g.stopHeld(f.ctx, f.release)
	if !reflect.DeepEqual(testTranscriptDisk(t, g.dir), updated) {
		t.Fatal("cancelled replay published partial reconstruction")
	}
	name, body = testTranscriptExpected(id, "A", 0, 8, 12, 0,
		testTranscriptRow{1, "user", "claude", "question"}, testTranscriptRow{3, "assistant", "claude", "answer"},
		testTranscriptRow{8, "user", "claude", "retry question"}, testTranscriptRow{10, "assistant", "claude", "  retry\n"}, testTranscriptRow{12, "assistant", "claude", "tail  "})
	complete := map[string]string{name: body}
	g.start()
	g.feed(1)
	g.publish(complete)
	g.stop()
	testMemoryMust(t, os.Remove(path))
	g.start()
	g.feed(1)
	g.publish(complete)
	g.stop()
	if !reflect.DeepEqual(testTranscriptSource(t, g.root), source) {
		t.Fatal("recovery modified source-history files")
	}
}
