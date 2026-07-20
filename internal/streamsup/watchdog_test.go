package streamsup

import (
	"context"
	"testing"
	"time"
)

// fakeClock is a controllable time source for the tracker's `now` seam. Mirrors
// streamrunner's watchdog_test fakeClock — the streamsup parser is turn-stateless
// and needed no clock seam, so this is the first clock double in the package.
type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// recvStall waits up to timeout for one OnStall signal. It returns the emitted
// pendingPermission value and whether a signal arrived.
func recvStall(t *testing.T, ch <-chan bool, timeout time.Duration) (pending bool, fired bool) {
	t.Helper()
	select {
	case p := <-ch:
		return p, true
	case <-time.After(timeout):
		return false, false
	}
}

// --- stallTracker unit tests (white-box) ---

func TestStallTracker_AwaitingTransitions(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{t: time.Unix(1_000, 0)}
	tr := newStallTracker(clk.now, discardLogger())

	// Starts awaiting the first assistant turn (the caller has written the
	// opening user envelope; claude owes a turn).
	if a, _ := tr.snapshot(); !a {
		t.Fatal("tracker should start in the awaiting state")
	}

	writeLine := func(s string) {
		t.Helper()
		if _, err := tr.Write([]byte(s + "\n")); err != nil {
			t.Fatalf("Write(%q): %v", s, err)
		}
	}

	// system init: activity only, still awaiting.
	writeLine(`{"type":"system","subtype":"init"}`)
	if a, _ := tr.snapshot(); !a {
		t.Error("system line should leave awaiting=true")
	}

	// assistant: claude produced a turn → stop awaiting (tool-run silence expected).
	writeLine(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use"}]}}`)
	if a, _ := tr.snapshot(); a {
		t.Error("assistant line should set awaiting=false")
	}

	// user (wrapping a tool_result): claude owes the next turn → awaiting again.
	writeLine(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]}}`)
	if a, _ := tr.snapshot(); !a {
		t.Error("user/tool_result line should set awaiting=true")
	}

	// assistant again → not awaiting.
	writeLine(`{"type":"assistant"}`)
	if a, _ := tr.snapshot(); a {
		t.Error("second assistant line should set awaiting=false")
	}

	// top-level tool_result line (defensive case in the switch) → awaiting.
	writeLine(`{"type":"tool_result"}`)
	if a, _ := tr.snapshot(); !a {
		t.Error("top-level tool_result line should set awaiting=true")
	}

	// result: turn done → not awaiting.
	writeLine(`{"type":"result","subtype":"success"}`)
	if a, _ := tr.snapshot(); a {
		t.Error("result line should set awaiting=false")
	}
}

func TestStallTracker_LastEventAdvances(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{t: time.Unix(1_000, 0)}
	tr := newStallTracker(clk.now, discardLogger())

	_, last0 := tr.snapshot()

	// A parseable line advances lastEvent to the new fake now.
	clk.advance(3 * time.Second)
	if _, err := tr.Write([]byte(`{"type":"system"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_, last1 := tr.snapshot()
	if !last1.After(last0) {
		t.Errorf("lastEvent did not advance: last0=%v last1=%v", last0, last1)
	}
	if want := time.Unix(1_003, 0); !last1.Equal(want) {
		t.Errorf("lastEvent = %v, want %v", last1, want)
	}

	// An UNPARSEABLE line is still activity — lastEvent advances again.
	clk.advance(2 * time.Second)
	if _, err := tr.Write([]byte("not json at all" + "\n")); err != nil {
		t.Fatalf("Write unparseable: %v", err)
	}
	_, last2 := tr.snapshot()
	if want := time.Unix(1_005, 0); !last2.Equal(want) {
		t.Errorf("unparseable line: lastEvent = %v, want %v (every complete line is activity)", last2, want)
	}
}

func TestStallTracker_LineBuffering(t *testing.T) {
	t.Parallel()

	t.Run("partial line does not transition until completed", func(t *testing.T) {
		t.Parallel()
		tr := newStallTracker(nil, discardLogger())
		// First half of an assistant line, no newline → no state change.
		if _, err := tr.Write([]byte(`{"type":"assi`)); err != nil {
			t.Fatalf("Write part 1: %v", err)
		}
		if a, _ := tr.snapshot(); !a {
			t.Error("partial line must not change awaiting (still true)")
		}
		// Completion arrives → the line parses, awaiting flips.
		if _, err := tr.Write([]byte(`stant"}` + "\n")); err != nil {
			t.Fatalf("Write part 2: %v", err)
		}
		if a, _ := tr.snapshot(); a {
			t.Error("completed assistant line should set awaiting=false")
		}
	})

	t.Run("multiple complete lines in one write transition in order", func(t *testing.T) {
		t.Parallel()
		tr := newStallTracker(nil, discardLogger())
		// assistant (→false) then user (→true), both in one Write: the last line
		// wins, proving both were consumed in order.
		if _, err := tr.Write([]byte(`{"type":"assistant"}` + "\n" + `{"type":"user"}` + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if a, _ := tr.snapshot(); !a {
			t.Error("awaiting should reflect the last line (user→true)")
		}
	})

	t.Run("oversized partial dropped, scanning recovers at next newline", func(t *testing.T) {
		t.Parallel()
		tr := newStallTracker(nil, discardLogger())
		tr.maxBuf = 64

		// A long newline-less blob must not be buffered unbounded; the partial
		// remainder is dropped once it exceeds maxBuf.
		if _, err := tr.Write(make([]byte, 200)); err != nil {
			t.Fatalf("Write blob: %v", err)
		}
		tr.mu.Lock()
		bufLen := len(tr.buf)
		tr.mu.Unlock()
		if bufLen != 0 {
			t.Errorf("oversized newline-less remainder not dropped: buf len = %d", bufLen)
		}

		// Parsing still works after a drop.
		if _, err := tr.Write([]byte(`{"type":"assistant"}` + "\n")); err != nil {
			t.Fatalf("Write after drop: %v", err)
		}
		if a, _ := tr.snapshot(); a {
			t.Error("tracker should recover after dropping an oversized partial")
		}
	})
}

// TestStallTracker_ContentFree (AC3): the tracker reads only the top-level
// `type` field. A line with rich nested content transitions `awaiting`
// identically to the same line with no content at all.
func TestStallTracker_ContentFree(t *testing.T) {
	t.Parallel()
	// A `type` decode reads nothing beyond the top level, so these two lines are
	// indistinguishable to the tracker.
	awaitingAfter := func(line string) bool {
		tr := newStallTracker(nil, discardLogger())
		if _, err := tr.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write(%q): %v", line, err)
		}
		a, _ := tr.snapshot()
		return a
	}

	rich := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"secret plan"},{"type":"text","text":"visible answer"},{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"rm -rf /"}}]}}`
	bare := `{"type":"assistant"}`
	if awaitingAfter(rich) != awaitingAfter(bare) {
		t.Errorf("rich-content assistant transitioned differently from a bare one — content leaked into the decision")
	}

	richUser := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"a long tool body","is_error":true}]}}`
	bareUser := `{"type":"user"}`
	if awaitingAfter(richUser) != awaitingAfter(bareUser) {
		t.Errorf("rich-content user transitioned differently from a bare one — content leaked into the decision")
	}
}

// --- shouldFire pure predicate (AC1) ---

func TestShouldFire(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000, 0)
	idle := 5 * time.Second
	cases := []struct {
		name     string
		awaiting bool
		last     time.Time
		want     bool
	}{
		{"in-flight tool silence never fires", false, now.Add(-10 * time.Second), false},
		{"awaiting but within threshold", true, now.Add(-3 * time.Second), false},
		{"awaiting and idle past threshold", true, now.Add(-10 * time.Second), true},
		{"awaiting, no elapsed", true, now, false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldFire(c.awaiting, c.last, now, idle); got != c.want {
				t.Errorf("shouldFire(awaiting=%v, elapsed=%v, idle=%v) = %v, want %v",
					c.awaiting, now.Sub(c.last), idle, got, c.want)
			}
		})
	}
}

// --- poll goroutine integration tests (real short Idle, real clock) ---

// AC1: on genuine idle (claude owes an assistant turn and the stream sits silent
// past the threshold) the watchdog fires once, with pendingPermission=false.
func TestWatchdog_GenuineIdleFires(t *testing.T) {
	t.Parallel()
	stalls := make(chan bool, 8)
	wd := NewWatchdog(WatchdogConfig{
		Idle:    60 * time.Millisecond,
		OnStall: func(pending bool) { stalls <- pending },
		Logger:  discardLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// system line: activity, still awaiting the first assistant turn.
	if _, err := wd.Writer().Write([]byte(`{"type":"system","subtype":"init"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	wd.Start(ctx)

	pending, fired := recvStall(t, stalls, 2*time.Second)
	if !fired {
		t.Fatal("watchdog did not fire on genuine idle")
	}
	if pending {
		t.Errorf("OnStall pendingPermission = true, want false (no permission pending)")
	}
	cancel()
	wd.Wait()
}

// AC1 (no-fire half): in-flight tool silence — claude owes nothing after an
// assistant turn — must NOT trip the watchdog, however long the silence.
func TestWatchdog_InFlightToolSilence_NoFire(t *testing.T) {
	t.Parallel()
	stalls := make(chan bool, 8)
	wd := NewWatchdog(WatchdogConfig{
		Idle:    60 * time.Millisecond,
		OnStall: func(pending bool) { stalls <- pending },
		Logger:  discardLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// assistant line: awaiting→false; the silence that follows is an in-flight
	// tool run, which claude owes nothing for.
	if _, err := wd.Writer().Write([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use"}]}}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	wd.Start(ctx)

	// Wait several idle windows: a type-blind watchdog WOULD fire; the type-aware
	// one must not.
	if pending, fired := recvStall(t, stalls, 400*time.Millisecond); fired {
		t.Errorf("watchdog fired during in-flight tool silence (pending=%v)", pending)
	}
	cancel()
	wd.Wait()
}

// AC2 (the load-bearing hook test): with the pending-permission hook engaged the
// watchdog emits a stall — exactly once, latched — and NEVER kills, however long
// the approval stays outstanding. Emit-not-kill is structural: the Watchdog holds
// no cancel/process handle, so it cannot cancel the ctx it polls under.
func TestWatchdog_PendingPermission_EmitNotKill(t *testing.T) {
	t.Parallel()
	stalls := make(chan bool, 8)
	wd := NewWatchdog(WatchdogConfig{
		Idle:              60 * time.Millisecond,
		PendingPermission: func() bool { return true },
		OnStall:           func(pending bool) { stalls <- pending },
		Logger:            discardLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// system line: awaiting stays true (claude owes the turn it is blocked on
	// pending approval).
	if _, err := wd.Writer().Write([]byte(`{"type":"system"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	wd.Start(ctx)

	pending, fired := recvStall(t, stalls, 2*time.Second)
	if !fired {
		t.Fatal("watchdog did not emit a stall while a permission was pending")
	}
	if !pending {
		t.Errorf("OnStall pendingPermission = false, want true (the hook value must flow through)")
	}

	// "however long the approval stays outstanding": keep silent for many more
	// idle windows and assert NO second emit (edge-triggered / latched) and NO
	// kill.
	if p, refired := recvStall(t, stalls, 300*time.Millisecond); refired {
		t.Errorf("watchdog emitted a second stall (pending=%v); want exactly once (latched)", p)
	}
	if ctx.Err() != nil {
		t.Errorf("ctx was cancelled while the watchdog ran (%v); emit-not-kill violated — the watchdog must never kill", ctx.Err())
	}
	cancel()
	wd.Wait()
}

// The latch re-arms per stall episode: after a fire, activity clears it, and a
// subsequent idle span fires again.
func TestWatchdog_LatchReArms(t *testing.T) {
	t.Parallel()
	stalls := make(chan bool, 8)
	wd := NewWatchdog(WatchdogConfig{
		Idle:    60 * time.Millisecond,
		OnStall: func(pending bool) { stalls <- pending },
		Logger:  discardLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := wd.Writer().Write([]byte(`{"type":"system"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	wd.Start(ctx)

	if _, fired := recvStall(t, stalls, 2*time.Second); !fired {
		t.Fatal("first stall did not fire")
	}
	// Activity advances lastEvent → the next non-firing tick clears the latch.
	if _, err := wd.Writer().Write([]byte(`{"type":"system"}` + "\n")); err != nil {
		t.Fatalf("Write (reset activity): %v", err)
	}
	if _, refired := recvStall(t, stalls, 2*time.Second); !refired {
		t.Fatal("watchdog did not re-fire after a new idle episode (latch stuck)")
	}
	cancel()
	wd.Wait()
}

// Clean shutdown: cancelling ctx exits the poll goroutine and Wait returns. No
// leak (go test -race).
func TestWatchdog_CleanShutdown_NoLeak(t *testing.T) {
	t.Parallel()
	wd := NewWatchdog(WatchdogConfig{
		Idle:    60 * time.Millisecond,
		OnStall: func(bool) {},
		Logger:  discardLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	wd.Start(ctx)
	cancel()
	wd.Wait() // returns once the poll goroutine has exited
}

func TestWatchdogTickFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		idle time.Duration
		want time.Duration
	}{
		{idle: idleStall, want: watchdogTick},                       // 240s/8=30s, capped to 5s
		{idle: 200 * time.Millisecond, want: 25 * time.Millisecond}, // /8
		{idle: 1 * time.Microsecond, want: minWatchdogTick},         // floored
	}
	for _, c := range cases {
		if got := watchdogTickFor(c.idle); got != c.want {
			t.Errorf("watchdogTickFor(%v) = %v, want %v", c.idle, got, c.want)
		}
	}
}
