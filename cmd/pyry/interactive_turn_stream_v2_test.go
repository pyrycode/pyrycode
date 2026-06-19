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

// TestResolveLatestSessionJSONL_ColdStartTailsFromZero is the #671 regression
// gate. A fresh relay session (mobile#421: fresh $HOME, claude under --continue
// defers JSONL creation until first input lands) has no transcript on disk when
// the producer first subscribes, so resolve#1 reports not-found and the
// subscriber retries. The phone's prompt then lands and claude writes the user
// turn + the "ping" reply, so resolve#2 finds a brand-new file whose whole
// content IS the current turn. It must tail from offset 0 — returning the file
// size (EOF) would race past the in-flight reply, the live drop this ticket
// fixes. RED before the fix (returns size), GREEN after.
func TestResolveLatestSessionJSONL_ColdStartTailsFromZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	resolve := resolveLatestSessionJSONL(dir)

	// resolve#1: no session file yet — not-found, subscriber retries.
	if _, _, err := resolve(context.Background()); err == nil {
		t.Fatal("cold-start resolve#1 over empty dir: got nil error, want 'no session jsonl found'")
	}

	// claude creates the JSONL and writes the turn + reply before resolve#2.
	want := writeJSONL(t, dir, uuidA, 42, time.Now())

	// resolve#2: the cold-start file is the current turn — tail from its start.
	path, off, err := resolve(context.Background())
	if err != nil {
		t.Fatalf("cold-start resolve#2: %v", err)
	}
	if path != want {
		t.Fatalf("cold-start path: got %q, want %q", path, want)
	}
	if off != 0 {
		t.Fatalf("cold-start startOffset: got %d, want 0 (tail from the file's start so the in-flight reply is not skipped)", off)
	}

	// After the first file is returned, a later /clear rotation is no longer a
	// cold start: a newer file returns its size (EOF), not 0, so a prior
	// transcript is never replayed. (The post-/clear first-reply race is a
	// separate, unobserved mode — out of scope for #671.)
	rotated := writeJSONL(t, dir, uuidB, 17, time.Now().Add(time.Minute))
	path, off, err = resolve(context.Background())
	if err != nil {
		t.Fatalf("post-cold-start rotation resolve: %v", err)
	}
	if path != rotated {
		t.Fatalf("rotation path: got %q, want %q (most-recently-modified)", path, rotated)
	}
	if off != 17 {
		t.Fatalf("post-cold-start rotation startOffset: got %d, want 17 (EOF, not a cold start)", off)
	}
}

// TestResolveLatestSessionJSONL_WarmStartTailsFromSize is the mandatory security
// guard (spec § Security review). A --continue resume transcript already on disk
// when the resolver first looks is a warm start, NOT a cold start: it must tail
// from the file size so the historical transcript is never replayed to the
// internet-exposed phone. Offset 0 here would leak prior-session content.
func TestResolveLatestSessionJSONL_WarmStartTailsFromSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A prior transcript is present at the first (and only) resolve.
	want := writeJSONL(t, dir, uuidA, 128, time.Now())

	path, off, err := resolveLatestSessionJSONL(dir)(context.Background())
	if err != nil {
		t.Fatalf("warm-start resolve: %v", err)
	}
	if path != want {
		t.Fatalf("warm-start path: got %q, want %q", path, want)
	}
	if off != 128 {
		t.Fatalf("warm-start startOffset: got %d, want 128 (do not replay the prior transcript to the phone)", off)
	}
}

// --- by-id resolver tests (#679) --------------------------------------------

// TestResolveBoundSessionJSONL_KeysOffIDNotMtime is the deterministic
// cross-conversation confidentiality proof (AC#2). With the bound session's
// JSONL present AND another session's JSONL present and written MORE recently,
// the resolver must still return the bound <id>.jsonl — resolution keys off the
// bound session id, never the file mtime, so another conversation's output can
// never be tailed into the active conversation's reply.
func TestResolveBoundSessionJSONL_KeysOffIDNotMtime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	bound := writeJSONL(t, dir, uuidA, 40, base)            // the bound session, older
	writeJSONL(t, dir, uuidB, 99, base.Add(10*time.Minute)) // another session, NEWER + larger

	path, off, err := resolveBoundSessionJSONL(dir, uuidA)(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if path != bound {
		t.Fatalf("path: got %q, want %q (the bound session's transcript, not the newer one)", path, bound)
	}
	if off != 40 {
		t.Fatalf("startOffset: got %d, want 40 (bound file size — warm tail, mtime-independent)", off)
	}
}

// TestResolveBoundSessionJSONL_ColdStartTailsFromZero: a brand-new bound session
// has no transcript when the producer first subscribes (claude defers JSONL
// creation until the first input lands). The first look is absent → not-found;
// once the file appears its whole content IS the current turn, so it tails from
// offset 0 (the #671 cold-start rule, now per bound session). RED if the resolver
// returned the file size there — the in-flight reply would be skipped.
func TestResolveBoundSessionJSONL_ColdStartTailsFromZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	resolve := resolveBoundSessionJSONL(dir, uuidA)

	if _, _, err := resolve(context.Background()); err == nil {
		t.Fatal("cold-start resolve#1 over absent file: got nil error, want not-found")
	}

	want := writeJSONL(t, dir, uuidA, 42, time.Now())

	path, off, err := resolve(context.Background())
	if err != nil {
		t.Fatalf("cold-start resolve#2: %v", err)
	}
	if path != want {
		t.Fatalf("cold-start path: got %q, want %q", path, want)
	}
	if off != 0 {
		t.Fatalf("cold-start startOffset: got %d, want 0 (tail from the file's start so the in-flight reply is not skipped)", off)
	}
}

// TestResolveBoundSessionJSONL_WarmStartTailsFromSize: a bound transcript already
// on disk at the first look (a --continue resume, or a switch-back to a live
// session) is a warm start — it tails from the file size so the conversation's
// history is never replayed to the internet-exposed phone. Offset 0 here would
// leak prior turns.
func TestResolveBoundSessionJSONL_WarmStartTailsFromSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := writeJSONL(t, dir, uuidA, 128, time.Now())

	path, off, err := resolveBoundSessionJSONL(dir, uuidA)(context.Background())
	if err != nil {
		t.Fatalf("warm-start resolve: %v", err)
	}
	if path != want {
		t.Fatalf("warm-start path: got %q, want %q", path, want)
	}
	if off != 128 {
		t.Fatalf("warm-start startOffset: got %d, want 128 (do not replay the bound transcript)", off)
	}
}

// TestResolveBoundSessionJSONL_InvalidIDErrors is the path-safety guard. A
// sessionID that is not a clean UUID stem must error before any path is
// constructed, so a malformed binding can never traverse out of dir. (sessionID
// is a server-minted UUID in production; this is defense-in-depth.)
func TestResolveBoundSessionJSONL_InvalidIDErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Plant a real file so a buggy resolver that ignored the guard could "succeed".
	writeJSONL(t, dir, uuidA, 10, time.Now())

	for _, id := range []string{"", "../" + uuidA, uuidA + "/..", "not-a-uuid", uuidA + ".jsonl"} {
		path, off, err := resolveBoundSessionJSONL(dir, id)(context.Background())
		if err == nil {
			t.Fatalf("sessionID %q: got nil error, want invalid-id error", id)
		}
		if path != "" || off != 0 {
			t.Fatalf("sessionID %q: got (%q, %d), want (\"\", 0) — no path constructed", id, path, off)
		}
	}
}

// --- resolveTarget tests (#679 follow-active mapping) ------------------------

// fakeSessionHost is a turnbridge.SessionHost test double; pointer identity lets
// the resolveTarget tests assert which host a Target keys onto.
type fakeSessionHost struct{ name string }

func (*fakeSessionHost) WaitForPTY(ctx context.Context) error { return nil }
func (*fakeSessionHost) Session() *tuidriver.Session          { return nil }

// TestResolveTarget_BootstrapWhenNoRoute: before any route (empty cursor) the
// target is the bootstrap host + the recency resolver — the unchanged pre-#679
// path (AC#4) — with the switch channel set so the first route re-subscribes.
func TestResolveTarget_BootstrapWhenNoRoute(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeJSONL(t, dir, uuidB, 12, time.Now()) // newest in dir — what recency picks

	active := &activeConversation{}
	bootstrap := &fakeSessionHost{name: "bootstrap"}
	boundHost := func(string) (turnbridge.SessionHost, string, bool) {
		t.Error("boundHost must not be consulted before any route")
		return nil, "", false
	}

	target, err := resolveTarget(active, boundHost, bootstrap, dir)(context.Background())
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if target.Host != turnbridge.SessionHost(bootstrap) {
		t.Fatalf("bootstrap path host: got %v, want the bootstrap host", target.Host)
	}
	if target.Switch == nil {
		t.Fatal("bootstrap path: Switch is nil; the first route would not re-subscribe")
	}
	path, _, err := target.Resolve(context.Background())
	if err != nil {
		t.Fatalf("bootstrap resolve: %v", err)
	}
	if want := filepath.Join(dir, uuidB+".jsonl"); path != want {
		t.Fatalf("bootstrap resolve path: got %q, want %q (recency)", path, want)
	}
}

// TestResolveTarget_BoundSessionWhenRouted (AC#1/AC#2): a routed conversation maps
// to its bound session's host + a by-id resolver that tails <bound-id>.jsonl even
// when another session's JSONL is newer — the confidentiality property, asserted
// at the resolveTarget seam.
func TestResolveTarget_BoundSessionWhenRouted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	boundFile := writeJSONL(t, dir, uuidA, 30, base)       // the bound session
	writeJSONL(t, dir, uuidB, 50, base.Add(5*time.Minute)) // another session, newer

	active := &activeConversation{}
	active.set("conv-1")
	host := &fakeSessionHost{name: "bound"}
	boundHost := func(convID string) (turnbridge.SessionHost, string, bool) {
		if convID != "conv-1" {
			t.Errorf("boundHost convID = %q, want conv-1", convID)
		}
		return host, uuidA, true
	}

	target, err := resolveTarget(active, boundHost, &fakeSessionHost{name: "bootstrap"}, dir)(context.Background())
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if target.Host != turnbridge.SessionHost(host) {
		t.Fatalf("bound path host: got %v, want the bound session host", target.Host)
	}
	if target.Switch == nil {
		t.Fatal("bound path: Switch is nil; a conversation switch would not re-subscribe")
	}
	path, off, err := target.Resolve(context.Background())
	if err != nil {
		t.Fatalf("bound resolve: %v", err)
	}
	if path != boundFile {
		t.Fatalf("bound resolve path: got %q, want %q (by bound id, not the newer file)", path, boundFile)
	}
	if off != 30 {
		t.Fatalf("bound resolve offset: got %d, want 30 (bound file size)", off)
	}
}

// TestResolveTarget_UnresolvableConversationErrors is the load-bearing
// confidentiality guard: when the active conversation cannot be resolved to a
// bound session, resolveTarget returns an ERROR (subscriber backs off + retries)
// — it must NEVER fall back to the bootstrap or any other transcript while the
// emitter stamps a non-empty conversation_id, which would cross-stream.
func TestResolveTarget_UnresolvableConversationErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A bootstrap transcript exists — a buggy fallback would happily tail it.
	writeJSONL(t, dir, uuidB, 64, time.Now())

	active := &activeConversation{}
	active.set("conv-gone")
	boundHost := func(string) (turnbridge.SessionHost, string, bool) { return nil, "", false }

	_, err := resolveTarget(active, boundHost, &fakeSessionHost{name: "bootstrap"}, dir)(context.Background())
	if err == nil {
		t.Fatal("unresolvable bound conversation: got nil error, want a retry error (no bootstrap fallback under a non-empty cursor)")
	}
}

// --- wiring tests (producer ⇄ emitter via a fake Subscriber) -----------------

// scriptedSubscriber returns each channel in streams in turn (incrementing a
// re-subscription counter and calling resolveSpy on each call, mirroring
// NewTargetSubscriber's resolve-fresh-per-subscription contract), then blocks
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
