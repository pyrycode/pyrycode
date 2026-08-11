package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// fakeExitingClaude writes an executable shell "claude" that reproduces the
// crashed-mid-turn shape end to end, and returns its path plus the "release"
// marker the first spawn blocks on.
//
//   - FIRST spawn: print ONE stream-json assistant line (the turn opener), then
//     block until goFile appears, then exit 0. No result line is ever printed, so
//     the parser never yields a TurnEnd (parser.go:152-157) — the turn is
//     abandoned exactly as a crash abandons it.
//   - EVERY LATER spawn: print nothing and block. The two-branch shape is not
//     incidental: the runner respawns after the exit, and a second assistant line
//     would RE-OPEN the turn and race every assertion against the backoff ladder.
//
// The release marker is what makes the positive test deterministic rather than
// timed: the child is provably still alive while "is the conversation busy?" is
// asserted, so nothing can have cleared it yet. A shell wrapper rather than a
// TestHelperProcess re-exec, for the reason fakeClaudeScript (acp_test.go:148-166)
// documents — the Go test binary rejects the --session-id <uuid> the spawn carries.
// argv is ignored entirely. There is no /bin/sh availability skip: sh is POSIX-
// guaranteed on both supported platforms, and a hard failure beats a silent skip.
func fakeExitingClaude(t *testing.T, dir string) (bin, goFile string) {
	t.Helper()

	spawned := filepath.Join(dir, "spawned")
	goFile = filepath.Join(dir, "release")
	bin = filepath.Join(dir, "fake-claude.sh")

	script := fmt.Sprintf(`#!/bin/sh
if [ -e %[1]q ]; then exec sleep 3600; fi
: > %[1]q
printf '%%s\n' '%[2]s'
while [ ! -e %[3]q ]; do sleep 0.05; done
exit 0
`, spawned, assistantTextLine("m-crash", "mid-turn"), goFile)

	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return bin, goFile
}

// runFactoryRunner builds a runner through the REAL production factory over sink
// and drives it on its own goroutine, joining in cleanup. Nothing about the wiring
// is stubbed: this is newStreamRunnerFactory's own returned closure, so whatever
// it installs on the streamsup Config is what the child exit fires.
func runFactoryRunner(t *testing.T, sink *streamTurnSink, bin, sessionID string) {
	t.Helper()

	runner, err := newStreamRunnerFactory(sink, "")(supervisor.Config{
		ClaudeBin: bin,
		WorkDir:   t.TempDir(),
		SessionID: sessionID,
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("newStreamRunnerFactory(...)(%q) error = %v, want nil", sessionID, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done }) // cancel-then-join; joining first deadlocks
}

// waitRecord returns the first record carrying event=want, or fails the test.
func waitRecord(t *testing.T, recs <-chan slog.Record, want string) slog.Record {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case rec := <-recs:
			var event string
			rec.Attrs(func(a slog.Attr) bool {
				if a.Key == "event" {
					event = a.Value.String()
				}
				return true
			})
			if event == want {
				return rec
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a record with event=%q", want)
		}
	}
}

// AC1 + AC2 (positive direction) + AC3, through the production wiring: a
// conversation reported busy when its claude child dies mid-turn is reported idle
// afterwards.
//
// AC1's two structurally-silent channels are silent BY CONSTRUCTION here, and that
// is the whole force of the test: the fake claude never prints a result line, so no
// TurnEnd is parseable for this turn, and no TransitionObserver and no
// sessions.Pool is constructed anywhere in this file, so no SessionTransition can
// fire. The observed clear can only have come from the child-exit lane.
//
// The fixture puts the cursor and the active-session gate on conversation B while
// the runner under test is A's — so A is a BACKGROUND conversation. That is both
// the common crash case (the crashed runner need not be the one the user is looking
// at) and the fixture that makes the drain's exit-arm placement discriminating: a
// clear delivered after the active-session gate would be dropped there and could
// not produce the result asserted below. Same reasoning as exitLaneDrain
// (stream_turn_busy_test.go:492-498).
//
// AC3's discriminant is the resolve map: it maps ONLY "sess-a", the runner's
// construction-time session id. A clear keyed on anything else — the runner's
// rotating internal spawn id, say — resolves to no conversation, clearForSession
// returns early, and the WaitIdle below deadlines.
func TestStreamRunnerFactory_ChildExitClearsTurnBusy(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin, goFile := fakeExitingClaude(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvIDB) // cursor on conversation B...
	active := &stubActiveSession{}
	active.set("sess-b") // ...and B's bound session is what the gate admits
	bcast := newChanBcast("conn-b")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	busy := newTurnBusyTracker(stubBusyResolve(map[string]string{
		"sess-a": testConvID,
	}), discardLogger())

	drops := make(chan string, 8)
	sink := newStreamTurnSink(0, discardLogger())
	drainCleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, busy,
		slog.New(dropWatcher{kinds: drops}))
	defer func() { cancel(); drainCleanup() }() // cancel-then-join; joining first deadlocks

	runFactoryRunner(t, sink, bin, "sess-a")

	// Barrier: the drain logs the not-active drop AFTER observe, on the same
	// goroutine, so seeing it proves the tracker was already fed. No sleep, no poll.
	waitDropKind(t, drops, "text_chunk")

	// NOT decoration. WaitIdle returns nil IMMEDIATELY on an already-idle
	// conversation, so without establishing busy first the wait below would pass
	// even with the exit lane doing nothing at all. This assertion is deterministic
	// rather than racing the clear because the child is still ALIVE at this point —
	// it blocks until the release marker below appears — so no exit has fired yet.
	if !busy.Busy(testConvID) {
		t.Fatalf("Busy(A) = false after the child's assistant line; the WaitIdle below would then pass vacuously")
	}

	// Release the child: it exits 0, having printed no result line.
	if err := os.WriteFile(goFile, nil, 0o600); err != nil {
		t.Fatalf("release fake claude: %v", err)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	if err := busy.WaitIdle(waitCtx, testConvID); err != nil {
		t.Fatalf("WaitIdle(A) = %v after A's child died mid-turn, want nil", err)
	}
	if busy.Busy(testConvID) {
		t.Errorf("Busy(A) = true after the child exit closed A's abandoned turn, want false")
	}
}

// AC4: the callback the factory installs IS (*streamTurnSink).exitFor, not a
// hand-rolled equivalent.
//
// The factory returns sessions.Runner — an interface over an unexported
// *streamsup.Runner — so the installed callback cannot be read back, and the
// discriminant has to be behavioural. exitFor has one no hand-rolled send would
// reproduce: on a full fan-in its drop branch logs at WARN with exactly
// event=stream_turn.exit_sink_full plus session_id, and NOTHING else — no "kind"
// (there is no event to name) and no content. sinkFor's sibling drop is Debug and
// carries "kind", so the record below distinguishes the two lanes as well.
//
// No drain is started, so nothing consumes the channel and the buffer-of-1 fill is
// deterministic and timing-free: the child's single assistant line takes the slot
// (Parser.emit is synchronous, and cmd.Wait joins the stdout copier before the exit
// fires), so the exit that follows always finds the channel full.
//
// This also covers AC4's "blocks on nothing": a blocking send would wedge the
// runner's Run goroutine on the full channel and this test would hit its deadline
// instead. AC4's remaining two clauses — "panics on nothing" and "reads no
// Runner.State()" — are code-shape criteria that installing exitFor satisfies by
// construction; they are verified by review, not by a test.
func TestStreamRunnerFactory_ChildExitInstallsExitFor(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin, goFile := fakeExitingClaude(t, dir)
	// Released up front: the script prints its line BEFORE it looks for the marker,
	// so the opener still lands on the fan-in ahead of the exit.
	if err := os.WriteFile(goFile, nil, 0o600); err != nil {
		t.Fatalf("release fake claude: %v", err)
	}

	recs := make(chan slog.Record, 16)
	// newStreamTurnSink only replaces buf when it is <= 0, so 1 survives. The
	// watcher goes on the SINK's logger: the exit drop is emitted from the sink
	// closure, never from the drain or the runner.
	sink := newStreamTurnSink(1, slog.New(dropWatcher{recs: recs}))

	runFactoryRunner(t, sink, bin, "sess-a")

	rec := waitRecord(t, recs, "stream_turn.exit_sink_full")

	if rec.Level != slog.LevelWarn {
		t.Errorf("exit drop level = %v, want %v (a wedged conversation is degraded operation, not a lost delta)",
			rec.Level, slog.LevelWarn)
	}
	attrs := map[string]string{}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	if got := attrs["session_id"]; got != "sess-a" {
		t.Errorf("exit drop session_id = %q, want %q (the runner's construction-time id)", got, "sess-a")
	}
	if _, ok := attrs["kind"]; ok {
		t.Errorf("exit drop carries a %q attr; there is no event to name", "kind")
	}
	if len(attrs) != 2 {
		t.Errorf("exit drop attrs = %v, want exactly event + session_id", attrs)
	}
}
