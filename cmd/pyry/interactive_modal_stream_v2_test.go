package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// fakeScreenHost is a turnbridge.SessionHost that ALSO satisfies
// screenSnapshotter, modelling a bound *supervisor.Supervisor. Pointer identity
// lets the boundScreenText table assert the host's screen text is returned.
type fakeScreenHost struct{ text string }

func (*fakeScreenHost) WaitForPTY(ctx context.Context) error { return nil }
func (*fakeScreenHost) Session() *tuidriver.Session          { return nil }
func (h *fakeScreenHost) ScreenSnapshot() (string, bool)     { return h.text, true }

// TestInteractiveModalStream_ShownReachesEmitterWithBoundScreen (AC5 surface): a
// scripted EventKindPtyModalShown{Permission} drained by runModalStream reaches
// the emitter's Handle paired with the current screen text, so a modal_shown is
// broadcast whose Title is the trimmed screen body and the deny-on-timeout is
// armed exactly once for the surfaced modal_id.
func TestInteractiveModalStream_ShownReachesEmitterWithBoundScreen(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "a", Interactive: true}}
	_, bcast, armer, emitter := newModalEmitterTestDeps(conns)

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}
	screenText := func() string { return "  a plain modal body  " }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); runModalStream(ctx, sub.subscribe, screenText, emitter) }()

	// Blocking send is a sync point; close+cancel then wait so every Handle call
	// (serial on the run goroutine) has completed before we assert.
	ch <- tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassPermission}
	close(ch)
	cancel()
	waitClosed(t, done, "modal stream run after ctx cancel")

	if len(bcast.pushes) != 1 {
		t.Fatalf("modal_shown push count: got %d, want 1 (one interactive conn)", len(bcast.pushes))
	}
	payload := decodeModalShown(t, bcast.pushes[0].env)
	if payload.Prompt != "a plain modal body" {
		t.Errorf("Prompt: got %q, want the trimmed screen body", payload.Prompt)
	}
	if len(armer.armed) != 1 || armer.armed[0] != payload.ModalID {
		t.Errorf("ArmModalTimeout calls = %v, want exactly [%s]", armer.armed, payload.ModalID)
	}
}

// TestInteractiveModalStream_LocalDismissal (AC5): a Shown then a Hidden for the
// same class drives a local first-answer-wins resolution — exactly one
// modal_dismissed{source: local, outcome: dismissed_local}.
func TestInteractiveModalStream_LocalDismissal(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "a", Interactive: true}}
	_, bcast, _, emitter := newModalEmitterTestDeps(conns)

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}
	screenText := func() string { return "permission body" }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); runModalStream(ctx, sub.subscribe, screenText, emitter) }()

	ch <- tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassPermission}
	ch <- tuidriver.Event{Kind: tuidriver.EventKindPtyModalHidden, Modal: tuidriver.ModalClassPermission}
	close(ch)
	cancel()
	waitClosed(t, done, "modal stream run after ctx cancel")

	dis := dismissedPushes(bcast.pushes)
	if len(dis) != 1 {
		t.Fatalf("modal_dismissed push count: got %d, want 1", len(dis))
	}
	got := decodeModalDismissed(t, dis[0].env)
	if got.Source != string(audit.SourceLocal) {
		t.Errorf("source: got %q, want %q", got.Source, audit.SourceLocal)
	}
	if got.Outcome != string(audit.OutcomeDismissedLocal) {
		t.Errorf("outcome: got %q, want %q", got.Outcome, audit.OutcomeDismissedLocal)
	}
}

// TestInteractiveModalStream_NonModalEventsNoRenderNoPush (AC2 efficiency +
// no-op): idle/thinking/jsonl/stall/banner events never render the screen and
// never surface anything. The screen is evaluated ONLY on a modal Shown, so no
// ScreenSnapshot render happens on the 50 ms idle/thinking/JSONL ticks.
func TestInteractiveModalStream_NonModalEventsNoRenderNoPush(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "a", Interactive: true}}
	_, bcast, armer, emitter := newModalEmitterTestDeps(conns)

	var screenCalls int // written only on the run goroutine; read after <-done
	screenText := func() string { screenCalls++; return "should never be rendered" }

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); runModalStream(ctx, sub.subscribe, screenText, emitter) }()

	for _, ev := range []tuidriver.Event{
		{Kind: tuidriver.EventKindPtyIdle},
		{Kind: tuidriver.EventKindPtyThinking},
		{Kind: tuidriver.EventKindJsonlEntry},
		{Kind: tuidriver.EventKindStallDetected},
		{Kind: tuidriver.EventKindPtyMcpFailureShown},
	} {
		ch <- ev
	}
	close(ch)
	cancel()
	waitClosed(t, done, "modal stream run after ctx cancel")

	if screenCalls != 0 {
		t.Errorf("screenText called %d times on non-modal events, want 0", screenCalls)
	}
	if len(bcast.pushes) != 0 {
		t.Errorf("non-modal events produced %d pushes, want 0", len(bcast.pushes))
	}
	if len(armer.armed) != 0 {
		t.Errorf("non-modal events armed %d timeouts, want 0", len(armer.armed))
	}
}

// TestBoundScreenText_HostSelection (AC3): the screen text follows the active
// conversation the way the turn stream's host does — a routed conversation's
// bound host, the bootstrap before any route, and a safe "" degrade on a miss or
// a host that carries no screen.
func TestBoundScreenText_HostSelection(t *testing.T) {
	t.Parallel()

	boundScreen := &fakeScreenHost{text: "bound host screen"}
	plainHost := &fakeSessionHost{name: "no-screen"} // SessionHost but NOT a screenSnapshotter

	tests := []struct {
		name      string
		convID    string
		boundHost boundHostFunc
		want      string
	}{
		{
			name:   "routed conv, host carries a screen -> that host's text",
			convID: "conv-1",
			boundHost: func(id string) (turnbridge.SessionHost, string, string, bool) {
				if id != "conv-1" {
					t.Errorf("boundHost convID = %q, want conv-1", id)
				}
				return boundScreen, "sid", "dir", true
			},
			want: "bound host screen",
		},
		{
			name:   "no route yet -> bootstrap text",
			convID: "",
			boundHost: func(string) (turnbridge.SessionHost, string, string, bool) {
				t.Error("boundHost must not be consulted before any route")
				return nil, "", "", false
			},
			want: "bootstrap screen",
		},
		{
			name:   "boundHost miss -> empty (never a foreign screen)",
			convID: "conv-gone",
			boundHost: func(string) (turnbridge.SessionHost, string, string, bool) {
				return nil, "", "", false
			},
			want: "",
		},
		{
			name:   "host does not satisfy screenSnapshotter -> empty (defensive)",
			convID: "conv-2",
			boundHost: func(string) (turnbridge.SessionHost, string, string, bool) {
				return plainHost, "sid", "dir", true
			},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			active := &activeConversation{}
			if tc.convID != "" {
				active.set(tc.convID)
			}
			bootstrap := &fakeScreenHost{text: "bootstrap screen"}
			got := boundScreenText(active, tc.boundHost, bootstrap)()
			if got != tc.want {
				t.Fatalf("boundScreenText() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInteractiveModalStream_TeardownUnblocks (AC4): a never-yielding subscriber
// keeps runModalStream blocked in subscribe on ctx; on cancel the goroutine
// exits and the <-done cleanup returns within the deadline.
func TestInteractiveModalStream_TeardownUnblocks(t *testing.T) {
	t.Parallel()
	conns := []relay.ActiveConn{{ConnID: "a", Interactive: true}}
	_, _, _, emitter := newModalEmitterTestDeps(conns)

	sub := &scriptedSubscriber{} // never yields a stream: blocks in subscribe on ctx
	screenText := func() string { return "" }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runModalStream(ctx, sub.subscribe, screenText, emitter) }()
	cleanup := func() { <-done }

	cancel()
	cleaned := make(chan struct{})
	go func() { cleanup(); close(cleaned) }()
	waitClosed(t, cleaned, "modal stream cleanup after ctx cancel")
}

// TestInteractiveModalStream_NoScreenTextLogLeak (AC5 content-free): the wiring
// is the first production code to handle rendered ScreenSnapshot bytes; feeding a
// Shown carrying a secret screen body with the push-drop DEBUG branch firing for
// every conn (pushErr set) must never echo the screen body into logs. Mirrors
// TestInteractiveTurnStream_NoAppOutputLogLeak.
func TestInteractiveModalStream_NoScreenTextLogLeak(t *testing.T) {
	t.Parallel()
	const secret = "SECRETSCREENBODYZZZ"

	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	logger := slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	reg := modalbridge.New()
	// Push fails so the emit push-drop DEBUG branch fires for the conn — the most
	// log-heavy path.
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}},
		pushErr:   map[string]error{"a": relay.ErrConnNotFound},
	}
	emitter := newInteractiveModalEmitterV2(reg, bcast, &fakeArmer{}, logger)

	ch := make(chan tuidriver.Event)
	sub := &scriptedSubscriber{streams: []<-chan tuidriver.Event{ch}}
	screenText := func() string { return secret }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); runModalStream(ctx, sub.subscribe, screenText, emitter) }()

	ch <- tuidriver.Event{Kind: tuidriver.EventKindPtyModalShown, Modal: tuidriver.ModalClassPermission}
	close(ch)
	cancel()
	waitClosed(t, done, "modal stream run after ctx cancel")

	mu.Lock()
	logs := buf.String()
	mu.Unlock()
	if strings.Contains(logs, secret) {
		t.Fatalf("screen body %q leaked into logs:\n%s", secret, logs)
	}
}
