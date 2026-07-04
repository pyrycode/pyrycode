package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/acpbridge"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// errWriter always fails, so acp.Transport.Notify returns an error and the sink's
// content-free notify_err branch fires for every event — the most content-adjacent
// log site, used by the no-leak test.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// AC-1 / AC-4: a scripted turn driven through the REAL mapEvent + MapUpdate path
// (turnbridge.Producer → acpTurnStream sink) yields exactly the ordered
// session/update frames — agent_thought_chunk, agent_message_chunk, tool_call —
// each addressed to the session id, and the terminal end-of-turn (→ TurnEnd, nil
// resolver) emits no frame. This is the ACP twin of
// TestInteractiveTurnStream_EventsReachHandle; it exercises the wiring this ticket
// adds, not just the sink (which acp_turn_stream_test.go already covers directly).
func TestACPTurnStreams_ScriptedTurnEmitsOrderedFrames(t *testing.T) {
	t.Parallel()
	// Reuse the sink harness: h.stream is the acpTurnStream sink (session
	// testStreamSessionID, nil onTurnEnd) writing to h.wire.
	h := newStreamHarness(t, nil)

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: sub.subscribe,
		OnEvent:   h.stream.Handle,
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runDone := make(chan struct{})
	go func() { _ = prod.Run(ctx); close(runDone) }()

	// Blocking sends are sync points: each returns once drain has received it, so
	// the prior event's frame is already written.
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

	frames := h.frames(t)
	want := []string{
		acpbridge.SessionUpdateAgentThoughtChunk,
		acpbridge.SessionUpdateAgentMessageChunk,
		acpbridge.SessionUpdateToolCall,
	}
	if len(frames) != len(want) {
		t.Fatalf("emitted %d frames, want %d (TurnEnd emits none): %v", len(frames), len(want), frames)
	}
	for i, frame := range frames {
		update := assertNotification(t, frame)
		if got := discriminant(t, update); got != want[i] {
			t.Errorf("frame %d sessionUpdate = %q, want %q", i, got, want[i])
		}
	}
	// The assistant text survives the real mapEvent + MapUpdate path intact.
	if msg, _ := assertNotification(t, frames[1])["content"].(map[string]any); msg["text"] != "hello" {
		t.Errorf("agent_message_chunk text = %v, want hello", msg["text"])
	}
}

// AC-3: a Stall driven through the wired producer surfaces on stderr and never on
// the notification stream. This proves the mapEvent(EventKindStallDetected) →
// turnevent.Stall → sink path end-to-end (the direct-sink StallDropsToStderr test
// does not run it through the producer).
func TestACPTurnStreams_StallSurfacesOnStderrNotWire(t *testing.T) {
	t.Parallel()
	h := newStreamHarness(t, nil)

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: sub.subscribe,
		OnEvent:   h.stream.Handle,
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runDone := make(chan struct{})
	go func() { _ = prod.Run(ctx); close(runDone) }()

	ch <- tuidriver.Event{Kind: tuidriver.EventKindStallDetected}
	close(ch)
	cancel()
	waitClosed(t, runDone, "producer Run after ctx cancel")

	if got := h.frames(t); len(got) != 0 {
		t.Errorf("Stall emitted %d frames, want 0: %v", len(got), got)
	}
	if !strings.Contains(h.stderr.String(), "acp_turn.stall") {
		t.Errorf("Stall not surfaced on stderr; stderr = %q", h.stderr.String())
	}
}

// AC-2 + idempotency: a producer started for a live pool session runs, and a
// second start(id) for the same id spawns no duplicate (started stays size 1).
// Cancelling the manager ctx lets wait() return within a deadline — the producer
// goroutine exits and is joined, no leak.
func TestACPTurnStreams_LifecycleTeardownJoinsProducer(t *testing.T) {
	t.Parallel()
	pool, _, logger, _ := newFakeClaudePool(t)

	poolCtx, poolCancel := context.WithCancel(context.Background())
	defer poolCancel()
	poolErr := make(chan error, 1)
	go func() { poolErr <- pool.Run(poolCtx) }()
	select {
	case <-pool.Ready():
	case err := <-poolErr:
		t.Fatalf("pool.Run returned before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("pool never became ready")
	}

	id, err := pool.Create(poolCtx, "")
	if err != nil {
		t.Fatalf("pool.Create: %v", err)
	}

	var wire bytes.Buffer
	transport := acp.New(strings.NewReader(""), &wire, logger)
	mgrCtx, mgrCancel := context.WithCancel(context.Background())
	defer mgrCancel()
	// A fresh tmp dir the fake claude never writes JSONL into, so the producer
	// parks in WaitForPTY / resolve-retry rather than opening a live stream.
	streams := newACPTurnStreams(mgrCtx, pool, t.TempDir(), nil, logger)
	streams.attach(transport)

	streams.start(id)
	streams.start(id) // idempotent: no second producer for the same id
	streams.mu.Lock()
	n := len(streams.started)
	streams.mu.Unlock()
	if n != 1 {
		t.Fatalf("started has %d entries after two start(id) calls, want 1 (idempotent)", n)
	}

	mgrCancel()
	waited := make(chan struct{})
	go func() { streams.wait(); close(waited) }()
	waitClosed(t, waited, "acpTurnStreams.wait after ctx cancel")

	poolCancel()
	select {
	case <-poolErr:
	case <-time.After(5 * time.Second):
		t.Fatal("pool.Run did not return after cancel")
	}
}

// start with an empty dir (streaming disabled: no $HOME) is a no-op — no producer,
// nothing to join.
func TestACPTurnStreams_EmptyDirDisablesStreaming(t *testing.T) {
	t.Parallel()
	streams := newACPTurnStreams(context.Background(), nil, "", nil, discardLogger())
	// nil pool is never dereferenced: dir == "" returns before Lookup.
	streams.start("any-session-id")
	streams.mu.Lock()
	n := len(streams.started)
	streams.mu.Unlock()
	if n != 0 {
		t.Fatalf("started has %d entries, want 0 (empty dir disables streaming)", n)
	}
	streams.wait() // no producers → returns immediately
}

// AC preservation constraint: no application content — thought/assistant text,
// tool title/input — reaches the logs at any level. Drives secrets through the
// wired chain against a failing transport writer (so the sink's notify_err debug
// branch fires for every content event) and asserts none appear in the log buffer.
// Mirrors TestInteractiveTurnStream_NoAppOutputLogLeak for the ACP leg.
func TestACPTurnStreams_NoAppContentLogLeak(t *testing.T) {
	t.Parallel()
	const (
		secretThought   = "SECRETTHOUGHTZZZ"
		secretAssistant = "SECRETASSISTANTZZZ"
		secretToolTitle = "SECRETTOOLTITLEZZZ"
		secretToolInput = "SECRETINPUTZZZ"
	)
	var (
		mu     sync.Mutex
		logBuf bytes.Buffer
	)
	logger := slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &logBuf}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	transport := acp.New(strings.NewReader(""), errWriter{}, logger)
	sink := newACPTurnStream(transport, testStreamSessionID, nil, logger)

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: sub.subscribe,
		OnEvent:   sink.Handle,
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
	logs := logBuf.String()
	mu.Unlock()
	for _, secret := range []string{secretThought, secretAssistant, secretToolTitle, secretToolInput} {
		if strings.Contains(logs, secret) {
			t.Fatalf("application content %q leaked into logs:\n%s", secret, logs)
		}
	}
}

// decodeFrames splits buf into JSON-RPC frames (one per line) and decodes each
// into a generic map. Shared by streamHarness.frames and the AC-5 end-to-end
// test, which reads a standalone transport buffer rather than a harness.
func decodeFrames(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("frame not valid JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// AC-5: an end-to-end scripted turn over the wired stream — a held session/prompt,
// some session/update frames, then a terminal TurnEnd — resolves the held call
// with the mapped stopReason, and the result frame lands strictly AFTER every
// notification. One transport over one buffer: the producer's single Run goroutine
// emits the notifications and then (via onTurnEnd → holds.end) the result, so the
// streaming-precedes-return ordering is structural, not timing-dependent.
func TestACPTurnStreams_ScriptedTurnResolvesHeldPromptAfterNotifications(t *testing.T) {
	t.Parallel()
	logger := discardLogger()

	// One transport over an in-memory buffer: both the session/update
	// notifications and the held call's resolution write here.
	var wire bytes.Buffer
	holds := newPromptHolds(logger)
	dlv := newRecordingDeliverer(false) // non-gated: delivery commits, call stays held

	// Register session/prompt and run Serve once over a single frame so a real hold
	// (with a Responder bound to this transport) is registered. Serve dispatches the
	// frame (ErrDeferred, no frame written), hits EOF, returns nil. A *Responder is
	// only obtainable through dispatch, hence the single Serve pass.
	tr := acp.New(strings.NewReader(promptFrame(t, 42, testStreamSessionID, "hi")+"\n"), &wire, logger)
	tr.Register("session/prompt", promptHandler(holds, func(sessions.SessionID) (promptDeliverer, error) {
		return dlv, nil
	}, logger))
	if err := tr.Serve(context.Background()); err != nil {
		t.Fatalf("Serve over the prompt frame: %v", err)
	}

	// Build the sink on the SAME transport, wiring onTurnEnd to resolve the held
	// call — exactly the join this ticket adds.
	sink := newACPTurnStream(tr, testStreamSessionID, func(r string) { holds.end(testStreamSessionID, r) }, logger)

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: sub.subscribe,
		OnEvent:   sink.Handle,
		Logger:    logger,
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
		endOfTurnEvent(),
	} {
		ch <- ev
	}
	close(ch)
	cancel()
	waitClosed(t, runDone, "producer Run after ctx cancel")

	// Decode every frame. The notifications (no id) must all precede the one
	// session/prompt result frame (id 42, stopReason end_turn).
	frames := decodeFrames(t, &wire)
	if len(frames) < 2 {
		t.Fatalf("emitted %d frames, want >=2 (notifications + result): %v", len(frames), frames)
	}
	resultIdx := -1
	for i, f := range frames {
		_, hasResult := f["result"]
		_, hasID := f["id"]
		if hasResult && hasID {
			if resultIdx != -1 {
				t.Fatalf("more than one result frame written: %v", frames)
			}
			resultIdx = i
			continue
		}
		// Every non-result frame must be a session/update notification (no id).
		if f["method"] != acpbridge.MethodSessionUpdate {
			t.Errorf("frame %d is neither a session/update notification nor the result: %v", i, f)
		}
	}
	if resultIdx == -1 {
		t.Fatalf("no session/prompt result frame written: %v", frames)
	}
	if resultIdx != len(frames)-1 {
		t.Fatalf("result frame at index %d, want last (index %d): all notifications must precede the return", resultIdx, len(frames)-1)
	}
	result, _ := frames[resultIdx]["result"].(map[string]any)
	if result["stopReason"] != "end_turn" {
		t.Errorf("stopReason = %v, want end_turn", result["stopReason"])
	}
	if id, _ := frames[resultIdx]["id"].(float64); id != 42 {
		t.Errorf("result id = %v, want 42 (the held request id)", frames[resultIdx]["id"])
	}
}
