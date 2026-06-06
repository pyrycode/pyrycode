package budget

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// signalRecorder counts Terminate / Kill invocations and remembers the
// timestamp of the first call to each.
type signalRecorder struct {
	mu        sync.Mutex
	terminate int
	kill      int
	termAt    time.Time
	killAt    time.Time
}

func (r *signalRecorder) Terminate() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.terminate++
	if r.termAt.IsZero() {
		r.termAt = time.Now()
	}
	return nil
}

func (r *signalRecorder) Kill() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kill++
	if r.killAt.IsZero() {
		r.killAt = time.Now()
	}
	return nil
}

func (r *signalRecorder) counts() (term, kill int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminate, r.kill
}

func assistantEntry() tuidriver.JSONLEntry {
	return tuidriver.JSONLEntry{Type: "assistant"}
}

// assistantEntryID builds an assistant entry carrying message.id == id, so a
// run of same-id entries models one logical reply split across JSONL lines.
func assistantEntryID(id string) tuidriver.JSONLEntry {
	return tuidriver.JSONLEntry{Type: "assistant", Message: &tuidriver.EntryMessage{ID: id}}
}

func mustNew(t *testing.T, cfg Config) *Counter {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = discardLogger()
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNew_Validation(t *testing.T) {
	t.Parallel()
	noop := func() error { return nil }
	cases := []struct {
		name string
		cfg  Config
	}{
		{"zero MaxTurns", Config{MaxTurns: 0, Terminate: noop, Kill: noop}},
		{"negative MaxTurns", Config{MaxTurns: -1, Terminate: noop, Kill: noop}},
		{"nil Terminate", Config{MaxTurns: 1, Terminate: nil, Kill: noop}},
		{"nil Kill", Config{MaxTurns: 1, Terminate: noop, Kill: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(tc.cfg); err == nil {
				t.Fatalf("New(%+v): expected error, got nil", tc.cfg)
			}
		})
	}
}

func TestOnEvent_NonAssistantKindsDoNotCount(t *testing.T) {
	t.Parallel()
	rec := &signalRecorder{}
	c := mustNew(t, Config{
		MaxTurns:    3,
		Terminate:   rec.Terminate,
		Kill:        rec.Kill,
		GracePeriod: 50 * time.Millisecond,
	})
	nonAssistant := []string{"user", "tool_use", "tool_result", "system", "attachment", ""}
	for _, kind := range nonAssistant {
		c.OnEvent(tuidriver.JSONLEntry{Type: kind})
	}
	if term, _ := rec.counts(); term != 0 {
		t.Fatalf("non-assistant kinds triggered Terminate: %d calls", term)
	}
	c.OnEvent(assistantEntry())
	c.OnEvent(assistantEntry())
	if term, _ := rec.counts(); term != 0 {
		t.Fatalf("Terminate called before budget: %d calls", term)
	}
	c.OnEvent(assistantEntry())
	if term, _ := rec.counts(); term != 1 {
		t.Fatalf("Terminate not called at budget: %d calls", term)
	}
}

func TestOnEvent_SIGTERMFiresExactlyAtBudget(t *testing.T) {
	t.Parallel()
	rec := &signalRecorder{}
	c := mustNew(t, Config{
		MaxTurns:    3,
		Terminate:   rec.Terminate,
		Kill:        rec.Kill,
		GracePeriod: 50 * time.Millisecond,
	})
	defer c.Stop()

	c.OnEvent(assistantEntry())
	c.OnEvent(assistantEntry())
	if term, _ := rec.counts(); term != 0 {
		t.Fatalf("Terminate fired before budget: %d", term)
	}
	c.OnEvent(assistantEntry())
	if term, _ := rec.counts(); term != 1 {
		t.Fatalf("Terminate did not fire at budget: %d", term)
	}
	// Subsequent assistant events must not re-fire Terminate.
	c.OnEvent(assistantEntry())
	c.OnEvent(assistantEntry())
	if term, _ := rec.counts(); term != 1 {
		t.Fatalf("Terminate fired more than once: %d", term)
	}
	if got := c.Reason(); got != ReasonMaxTurns {
		t.Fatalf("Reason = %q, want %q", got, ReasonMaxTurns)
	}
}

func TestOnEvent_CountsLogicalTurns(t *testing.T) {
	t.Parallel()
	user := tuidriver.JSONLEntry{Type: "user"}
	cases := []struct {
		name     string
		maxTurns int
		entries  []tuidriver.JSONLEntry
		// fireAt is the 0-based index in entries after which Terminate first
		// becomes 1. Every earlier prefix must leave Terminate at 0.
		fireAt int
	}{
		{
			// AC#1: a split reply (same message.id across entries, with a
			// non-assistant entry interleaved) counts as one turn; the budget
			// fires only when the second distinct id reaches MaxTurns.
			name:     "split reply counts as one turn",
			maxTurns: 2,
			entries: []tuidriver.JSONLEntry{
				assistantEntryID("msg_A"), // [thinking]  turn 1
				assistantEntryID("msg_A"), // [tool_use]  still turn 1
				user,                      // tool_result, must not split turn 1
				assistantEntryID("msg_B"), // [text]      turn 2 -> budget
			},
			fireAt: 3,
		},
		{
			// AC#2: K logical turns span more than K assistant entries (2 per
			// turn). The budget fires when the K-th distinct id first appears,
			// not after MaxTurns raw assistant entries (old per-entry counting
			// would have fired at index 2, mid-turn-2).
			name:     "K turns span more than K entries",
			maxTurns: 3,
			entries: []tuidriver.JSONLEntry{
				assistantEntryID("msg_1"), // turn 1
				assistantEntryID("msg_1"),
				assistantEntryID("msg_2"), // turn 2
				assistantEntryID("msg_2"),
				assistantEntryID("msg_3"), // turn 3 -> budget
				assistantEntryID("msg_3"),
			},
			fireAt: 4,
		},
		{
			// AC#5: one distinct id per entry still counts as one turn each.
			name:     "one distinct id per entry",
			maxTurns: 3,
			entries: []tuidriver.JSONLEntry{
				assistantEntryID("msg_A"),
				assistantEntryID("msg_B"),
				assistantEntryID("msg_C"), // turn 3 -> budget
			},
			fireAt: 2,
		},
		{
			// AC#5: empty-id entries are each their own turn (the empty-id
			// floor), preserving the pre-fix per-entry behaviour.
			name:     "empty id is its own turn",
			maxTurns: 3,
			entries: []tuidriver.JSONLEntry{
				assistantEntry(),
				assistantEntry(),
				assistantEntry(), // turn 3 -> budget
			},
			fireAt: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &signalRecorder{}
			c := mustNew(t, Config{
				MaxTurns:    tc.maxTurns,
				Terminate:   rec.Terminate,
				Kill:        rec.Kill,
				GracePeriod: 50 * time.Millisecond,
			})
			defer c.Stop()

			for i, e := range tc.entries {
				c.OnEvent(e)
				term, _ := rec.counts()
				want := 0
				if i >= tc.fireAt {
					want = 1
				}
				if term != want {
					t.Fatalf("after entry %d: Terminate count = %d, want %d", i, term, want)
				}
			}
			if got := c.Reason(); got != ReasonMaxTurns {
				t.Fatalf("Reason = %q, want %q", got, ReasonMaxTurns)
			}
		})
	}
}

func TestOnEvent_SIGKILLFiresAfterGrace(t *testing.T) {
	t.Parallel()
	rec := &signalRecorder{}
	const grace = 80 * time.Millisecond
	c := mustNew(t, Config{
		MaxTurns:    1,
		Terminate:   rec.Terminate,
		Kill:        rec.Kill,
		GracePeriod: grace,
	})
	defer c.Stop()

	c.OnEvent(assistantEntry())
	term, kill := rec.counts()
	if term != 1 {
		t.Fatalf("Terminate not fired at budget: %d", term)
	}
	if kill != 0 {
		t.Fatalf("Kill fired before grace elapsed: %d", kill)
	}

	// Halfway through the grace period — Kill must not have fired.
	time.Sleep(grace / 2)
	if _, kill := rec.counts(); kill != 0 {
		t.Fatalf("Kill fired before grace elapsed: %d", kill)
	}

	// Past the grace period — Kill must have fired exactly once.
	time.Sleep(grace)
	if _, kill := rec.counts(); kill != 1 {
		t.Fatalf("Kill not fired after grace: %d", kill)
	}

	rec.mu.Lock()
	elapsed := rec.killAt.Sub(rec.termAt)
	rec.mu.Unlock()
	// Allow small measurement slop: the kill timer is scheduled slightly
	// before Terminate's callback records termAt (see OnEvent in budget.go —
	// AfterFunc is called inside the locked section, then Terminate is
	// invoked after the unlock). The implementation correctly waits the
	// full grace duration from when the timer was scheduled; the measured
	// elapsed is systematically a few ms less than grace. CI's slower
	// scheduler surfaces this; locally on faster hardware it passes.
	const slop = 5 * time.Millisecond
	if elapsed < grace-slop {
		t.Fatalf("Kill fired %v after Terminate, want >= %v (with %v slop)", elapsed, grace, slop)
	}
}

func TestStop_CancelsPendingSIGKILL(t *testing.T) {
	t.Parallel()
	rec := &signalRecorder{}
	const grace = 50 * time.Millisecond
	c := mustNew(t, Config{
		MaxTurns:    1,
		Terminate:   rec.Terminate,
		Kill:        rec.Kill,
		GracePeriod: grace,
	})

	c.OnEvent(assistantEntry())
	if term, _ := rec.counts(); term != 1 {
		t.Fatalf("Terminate not fired: %d", term)
	}
	c.Stop()

	time.Sleep(grace * 3)
	if _, kill := rec.counts(); kill != 0 {
		t.Fatalf("Kill fired after Stop: %d", kill)
	}
	// Stop is idempotent.
	c.Stop()
}

func TestStop_WithoutBudgetHit(t *testing.T) {
	t.Parallel()
	rec := &signalRecorder{}
	c := mustNew(t, Config{
		MaxTurns:    5,
		Terminate:   rec.Terminate,
		Kill:        rec.Kill,
		GracePeriod: 10 * time.Millisecond,
	})
	// Calling Stop with no pending timer is a no-op.
	c.Stop()
	if term, kill := rec.counts(); term != 0 || kill != 0 {
		t.Fatalf("Stop without budget hit triggered signals: term=%d kill=%d", term, kill)
	}
}

func TestOnEndOfTurn_ReasonCompletion(t *testing.T) {
	t.Parallel()
	rec := &signalRecorder{}
	c := mustNew(t, Config{
		MaxTurns:    5,
		Terminate:   rec.Terminate,
		Kill:        rec.Kill,
		GracePeriod: 10 * time.Millisecond,
	})
	c.OnEvent(assistantEntry())
	c.OnEvent(assistantEntry())
	c.OnEndOfTurn()
	if got := c.Reason(); got != ReasonCompletion {
		t.Fatalf("Reason = %q, want %q", got, ReasonCompletion)
	}
	if term, kill := rec.counts(); term != 0 || kill != 0 {
		t.Fatalf("completion path triggered signals: term=%d kill=%d", term, kill)
	}
}

func TestOnEndOfTurn_DoesNotOverwriteMaxTurns(t *testing.T) {
	t.Parallel()
	rec := &signalRecorder{}
	c := mustNew(t, Config{
		MaxTurns:    1,
		Terminate:   rec.Terminate,
		Kill:        rec.Kill,
		GracePeriod: 50 * time.Millisecond,
	})
	defer c.Stop()
	c.OnEvent(assistantEntry()) // hits budget, reason=max_turns
	c.OnEndOfTurn()             // must NOT overwrite to completion
	if got := c.Reason(); got != ReasonMaxTurns {
		t.Fatalf("Reason = %q, want %q (first-terminal-wins)", got, ReasonMaxTurns)
	}
}

func TestReason_ZeroValueBeforeTerminalEvent(t *testing.T) {
	t.Parallel()
	rec := &signalRecorder{}
	c := mustNew(t, Config{
		MaxTurns:    5,
		Terminate:   rec.Terminate,
		Kill:        rec.Kill,
		GracePeriod: 10 * time.Millisecond,
	})
	c.OnEvent(assistantEntry())
	c.OnEvent(tuidriver.JSONLEntry{Type: "user"})
	if got := c.Reason(); got != "" {
		t.Fatalf("Reason = %q, want zero value before terminal event", got)
	}
}

func TestTerminateError_DoesNotBlockKill(t *testing.T) {
	// If Terminate returns an error (e.g. ESRCH because the child already
	// died), the grace timer must still arm and Kill must still fire.
	t.Parallel()
	const grace = 50 * time.Millisecond
	var killCalls atomic.Int32
	c := mustNew(t, Config{
		MaxTurns:    1,
		Terminate:   func() error { return errors.New("simulated ESRCH") },
		Kill:        func() error { killCalls.Add(1); return nil },
		GracePeriod: grace,
	})
	defer c.Stop()
	c.OnEvent(assistantEntry())
	time.Sleep(grace * 3)
	if got := killCalls.Load(); got != 1 {
		t.Fatalf("Kill calls = %d, want 1 after Terminate error", got)
	}
}

func TestKillError_IsLogged(t *testing.T) {
	// Kill errors are non-fatal — surface them at Warn but don't panic.
	t.Parallel()
	buf := &syncWriter{w: &strings.Builder{}}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	c := mustNew(t, Config{
		MaxTurns:    1,
		Terminate:   func() error { return nil },
		Kill:        func() error { return errors.New("simulated kill failure") },
		GracePeriod: 20 * time.Millisecond,
		Logger:      logger,
	})
	defer c.Stop()
	c.OnEvent(assistantEntry())
	time.Sleep(80 * time.Millisecond)
	out := buf.String()
	if !strings.Contains(out, "kill failed") {
		t.Fatalf("expected kill-failure log line, got: %q", out)
	}
}

// syncWriter serialises Write calls for the slog test handler; slog handlers
// may write concurrently from time.AfterFunc and the test goroutine.
type syncWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.String()
}
