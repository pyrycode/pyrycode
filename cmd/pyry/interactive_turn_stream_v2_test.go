package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// --- tuidriver.Event fixtures (package-main copies of turnbridge's private
// test helpers; the producer maps these, so they must populate both the typed
// Message and RawLine, exactly as the turnbridge fixtures do) ----------------

func streamEntry(t *testing.T, envType, msgID string, block map[string]any) tuidriver.JSONLEntry {
	t.Helper()
	bt, _ := block["type"].(string)
	line, err := json.Marshal(map[string]any{
		"type":    envType,
		"message": map[string]any{"id": msgID, "content": []any{block}},
	})
	if err != nil {
		t.Fatalf("marshal stream entry: %v", err)
	}
	return tuidriver.JSONLEntry{
		Type: envType,
		Message: &tuidriver.EntryMessage{
			ID:      msgID,
			Content: []tuidriver.ContentBlock{{Type: bt, Raw: block}},
		},
		RawLine: line,
	}
}

func jsonlStreamEvent(e tuidriver.JSONLEntry) tuidriver.Event {
	return tuidriver.Event{Kind: tuidriver.EventKindJsonlEntry, Source: tuidriver.EventSourceJsonl, Entry: e}
}

func endOfTurnEvent() tuidriver.Event {
	return tuidriver.Event{Kind: tuidriver.EventKindJsonlEndOfTurn}
}

// --- resolver tests ---------------------------------------------------------

// writeJSONL writes a <uuid>.jsonl file of n bytes and stamps its mtime.
func writeJSONL(t *testing.T, dir, uuid string, n int, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, uuid+".jsonl")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), n), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

const (
	uuidA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	uuidB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	uuidC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func TestResolveLatestSessionJSONL_NewestWinsWithSizeOffset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	writeJSONL(t, dir, uuidA, 10, base)
	writeJSONL(t, dir, uuidB, 20, base.Add(2*time.Minute)) // newest
	writeJSONL(t, dir, uuidC, 30, base.Add(time.Minute))

	path, off, err := resolveLatestSessionJSONL(dir)(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(dir, uuidB+".jsonl"); path != want {
		t.Fatalf("path: got %q, want %q (most-recently-modified)", path, want)
	}
	if off != 20 {
		t.Fatalf("startOffset: got %d, want 20 (newest file's current size)", off)
	}
}

func TestResolveLatestSessionJSONL_ReEvaluatesPerCall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	writeJSONL(t, dir, uuidA, 5, base)

	resolve := resolveLatestSessionJSONL(dir)
	path1, _, err := resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve#1: %v", err)
	}
	if want := filepath.Join(dir, uuidA+".jsonl"); path1 != want {
		t.Fatalf("resolve#1 path: got %q, want %q", path1, want)
	}

	// A /clear rotation in miniature: a newer JSONL appears; the same closure
	// must now resolve to it (per-call freshness is the AC#2 mechanism).
	writeJSONL(t, dir, uuidB, 7, base.Add(time.Minute))
	path2, off2, err := resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve#2: %v", err)
	}
	if want := filepath.Join(dir, uuidB+".jsonl"); path2 != want {
		t.Fatalf("resolve#2 path: got %q, want %q (rotated file)", path2, want)
	}
	if off2 != 7 {
		t.Fatalf("resolve#2 offset: got %d, want 7", off2)
	}
}

func TestResolveLatestSessionJSONL_TieBreakLexicographic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mt := time.Now().Add(-time.Hour)
	writeJSONL(t, dir, uuidA, 1, mt)
	writeJSONL(t, dir, uuidB, 2, mt) // same mtime — lexicographically larger wins

	path, _, err := resolveLatestSessionJSONL(dir)(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(dir, uuidB+".jsonl"); path != want {
		t.Fatalf("tie-break path: got %q, want %q (lexicographically larger)", path, want)
	}
}

func TestResolveLatestSessionJSONL_IgnoresNonSessionEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mt := time.Now()
	// Non-.jsonl file, a subdirectory, and a non-UUID-stem .jsonl — all skipped.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scratch.jsonl"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	// One real session file, written last so it is also the newest.
	writeJSONL(t, dir, uuidA, 9, mt)

	path, off, err := resolveLatestSessionJSONL(dir)(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(dir, uuidA+".jsonl"); path != want {
		t.Fatalf("path: got %q, want %q (only the <uuid>.jsonl)", path, want)
	}
	if off != 9 {
		t.Fatalf("offset: got %d, want 9", off)
	}
}

func TestResolveLatestSessionJSONL_EmptyDirErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, off, err := resolveLatestSessionJSONL(dir)(context.Background())
	if err == nil {
		t.Fatal("empty dir: got nil error, want non-nil")
	}
	if path != "" || off != 0 {
		t.Fatalf("empty dir: got (%q, %d), want (\"\", 0)", path, off)
	}
}

func TestResolveLatestSessionJSONL_UnreadableDirErrors(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, _, err := resolveLatestSessionJSONL(missing)(context.Background())
	if err == nil {
		t.Fatal("nonexistent dir: got nil error, want wrapped read error")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error should carry the dir path; got %v", err)
	}
}

// --- wiring tests (producer ⇄ emitter via a fake Subscriber) -----------------

// scriptedSubscriber returns each channel in streams in turn (incrementing a
// re-subscription counter and calling resolveSpy on each call, mirroring
// NewSessionSubscriber's resolve-fresh-per-subscription contract), then blocks
// on ctx — so the producer's Run re-subscribe loop is driven deterministically.
type scriptedSubscriber struct {
	mu       sync.Mutex
	streams  []<-chan tuidriver.Event
	calls    int
	resolves int
}

func (s *scriptedSubscriber) resolveSpy() {
	s.mu.Lock()
	s.resolves++
	s.mu.Unlock()
}

func (s *scriptedSubscriber) subscribe(ctx context.Context) (<-chan tuidriver.Event, error) {
	s.mu.Lock()
	n := s.calls
	s.calls++
	s.mu.Unlock()
	s.resolveSpy() // models resolve being called fresh on every (re)subscription
	if n < len(s.streams) {
		return s.streams[n], nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *scriptedSubscriber) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *scriptedSubscriber) resolveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolves
}

// AC#1: events drained by the producer reach the emitter's Handle and fan out
// as the expected ordered envelopes — the OnEvent -> Handle bridge this slice
// constructs.
func TestInteractiveTurnStream_EventsReachHandle(t *testing.T) {
	t.Parallel()

	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: sub.subscribe,
		OnEvent:   func(ev turnevent.Event) { emitter.Handle(ctx, ev) },
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runDone := make(chan struct{})
	go func() { _ = prod.Run(ctx); close(runDone) }()

	// Blocking sends are sync points: each returns once drain has received it.
	for _, ev := range []tuidriver.Event{
		jsonlStreamEvent(streamEntry(t, "assistant", "m1", map[string]any{"type": "thinking", "thinking": "reasoning"})),
		jsonlStreamEvent(streamEntry(t, "assistant", "m2", map[string]any{"type": "text", "text": "hello"})),
		jsonlStreamEvent(streamEntry(t, "assistant", "m3", map[string]any{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]any{"file_path": "/tmp/x"}})),
		endOfTurnEvent(),
	} {
		ch <- ev
	}
	close(ch)
	cancel()
	waitClosed(t, runDone, "producer Run after ctx cancel")

	wantTypes := []string{
		protocol.TypeTurnState,      // thinking
		protocol.TypeTurnState,      // responding
		protocol.TypeAssistantDelta, // hello
		protocol.TypeToolUse,        // Read
		protocol.TypeTurnEnd,        // end_turn
		protocol.TypeTurnState,      // idle
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("envelope order through the bridge:\n got %v\nwant %v", got, wantTypes)
	}
}

// AC#2: when a session ends (channel close), the producer re-subscribes, the
// resolver is re-evaluated, and the structured stream continues on the new
// session — with no leaked goroutine (Run returns cleanly).
func TestInteractiveTurnStream_RotationReSubscribes(t *testing.T) {
	t.Parallel()

	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	ch1 := make(chan tuidriver.Event)
	ch2 := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch1, ch2}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: sub.subscribe,
		OnEvent:   func(ev turnevent.Event) { emitter.Handle(ctx, ev) },
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runDone := make(chan struct{})
	go func() { _ = prod.Run(ctx); close(runDone) }()

	// Subscription 1: a text event then end-of-turn (which flushes the coalesced
	// delta #609), then the session ends.
	ch1 <- jsonlStreamEvent(streamEntry(t, "assistant", "m1", map[string]any{"type": "text", "text": "before"}))
	ch1 <- endOfTurnEvent()
	close(ch1)

	// Subscription 2: the blocking send only completes once Run has re-subscribed
	// (got ch2) and is draining it — proving the rotation re-subscription. The
	// end-of-turn flushes the second coalesced delta.
	ch2 <- jsonlStreamEvent(streamEntry(t, "assistant", "m2", map[string]any{"type": "text", "text": "after"}))
	ch2 <- endOfTurnEvent()
	close(ch2)

	cancel()
	waitClosed(t, runDone, "producer Run after rotation + ctx cancel")

	if got := sub.callCount(); got < 2 {
		t.Fatalf("subscribe calls: got %d, want >= 2 (re-subscription after session end)", got)
	}
	if got := sub.resolveCount(); got < 2 {
		t.Fatalf("resolve calls: got %d, want >= 2 (resolver re-evaluated per subscription)", got)
	}
	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("assistant_delta count across rotation: got %d, want 2", len(deltas))
	}
}

// AC#3: on ctx-cancel the producer goroutine exits and the helper-style cleanup
// (<-done) unblocks within a deadline — the teardown ordering contract.
func TestInteractiveTurnStream_TeardownUnblocks(t *testing.T) {
	t.Parallel()

	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	// A subscriber that never yields a stream — Run blocks in subscribe on ctx,
	// exactly the steady-state shape between sessions.
	sub := &scriptedSubscriber{}

	ctx, cancel := context.WithCancel(context.Background())
	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: sub.subscribe,
		OnEvent:   func(ev turnevent.Event) { emitter.Handle(ctx, ev) },
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Mirror startInteractiveTurnStreamV2's goroutine + cleanup shape.
	done := make(chan struct{})
	go func() { defer close(done); _ = prod.Run(ctx) }()
	cleanup := func() { <-done }

	cancel()
	cleaned := make(chan struct{})
	go func() { cleanup(); close(cleaned) }()
	waitClosed(t, cleaned, "stream cleanup after ctx cancel")
}

// AC#5: no application output (thought/assistant/tool strings) is logged at any
// level by the wiring, producer, or resolver path.
func TestInteractiveTurnStream_NoAppOutputLogLeak(t *testing.T) {
	t.Parallel()
	const (
		secretThought   = "SECRETTHOUGHTZZZ"
		secretAssistant = "SECRETASSISTANTZZZ"
		secretToolTitle = "SECRETTOOLTITLEZZZ"
		secretToolInput = "SECRETINPUTZZZ"
	)

	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	// The producer's Run goroutine and the test goroutine both touch buf; guard it.
	logger := slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cur := &stubCursor{}
	cur.set(testConvID)
	// Push fails so the emit push_err DEBUG branch fires for every envelope —
	// the most log-heavy path.
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}},
		pushErr:   map[string]error{"a": relay.ErrConnNotFound},
	}
	emitter := newInteractiveTurnEmitterV2(cur, bcast, logger)

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: sub.subscribe,
		OnEvent:   func(ev turnevent.Event) { emitter.Handle(ctx, ev) },
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runDone := make(chan struct{})
	go func() { _ = prod.Run(ctx); close(runDone) }()

	for _, ev := range []tuidriver.Event{
		jsonlStreamEvent(streamEntry(t, "assistant", "m1", map[string]any{"type": "thinking", "thinking": secretThought})),
		jsonlStreamEvent(streamEntry(t, "assistant", "m2", map[string]any{"type": "text", "text": secretAssistant})),
		jsonlStreamEvent(streamEntry(t, "assistant", "m3", map[string]any{"type": "tool_use", "id": "t1", "name": secretToolTitle, "input": map[string]any{"query": secretToolInput}})),
		endOfTurnEvent(),
	} {
		ch <- ev
	}
	close(ch)
	cancel()
	waitClosed(t, runDone, "producer Run after ctx cancel")

	mu.Lock()
	logs := buf.String()
	mu.Unlock()
	for _, secret := range []string{secretThought, secretAssistant, secretToolTitle, secretToolInput} {
		if strings.Contains(logs, secret) {
			t.Fatalf("application output %q leaked into logs:\n%s", secret, logs)
		}
	}
	// Thought text must never reach the wire either.
	for _, p := range bcast.pushes {
		if bytes.Contains(p.env.Payload, []byte(secretThought)) {
			t.Fatalf("thought text leaked into a %q envelope payload", p.env.Type)
		}
	}
}

// --- test plumbing ----------------------------------------------------------

// lockedWriter serialises writes to an underlying buffer shared across goroutines.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// waitClosed fails the test if ch is not closed within a deadline.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: did not complete within deadline", what)
	}
}
