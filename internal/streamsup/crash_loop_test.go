package streamsup

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestCrashEpisode_Observe(t *testing.T) {
	t.Parallel()
	const (
		fast = time.Millisecond
		slow = crashLoopFastUptime
	)
	repeat := func(d time.Duration, n int) []time.Duration {
		out := make([]time.Duration, n)
		for i := range out {
			out[i] = d
		}
		return out
	}
	cat := func(parts ...[]time.Duration) []time.Duration {
		var out []time.Duration
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}

	tests := []struct {
		name    string
		uptimes []time.Duration
		// fireAt lists the zero-based indexes whose observe must return true.
		fireAt []int
	}{
		{"one short of N", repeat(fast, crashLoopFastExits-1), nil},
		{"Nth fast exit fires", repeat(fast, crashLoopFastExits), []int{crashLoopFastExits - 1}},
		{"once per episode", repeat(fast, 3*crashLoopFastExits), []int{crashLoopFastExits - 1}},
		{"uptime at the threshold is not fast", repeat(slow, 2*crashLoopFastExits), nil},
		{"spawn that never launched counts", repeat(0, crashLoopFastExits), []int{crashLoopFastExits - 1}},
		{
			"slow exit mid-run restarts the count",
			cat(repeat(fast, crashLoopFastExits-1), []time.Duration{slow}, repeat(fast, crashLoopFastExits)),
			[]int{2*crashLoopFastExits - 1},
		},
		{
			"slow exit ends the episode and a later run fires again",
			cat(repeat(fast, crashLoopFastExits+1), []time.Duration{time.Hour}, repeat(fast, crashLoopFastExits)),
			[]int{crashLoopFastExits - 1, 2*crashLoopFastExits + 1},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var e crashEpisode
			var got []int
			for i, u := range tc.uptimes {
				if e.observe(u) {
					got = append(got, i)
				}
			}
			if len(got) != len(tc.fireAt) {
				t.Fatalf("fired at %v, want %v", got, tc.fireAt)
			}
			for i := range got {
				if got[i] != tc.fireAt[i] {
					t.Fatalf("fired at %v, want %v", got, tc.fireAt)
				}
			}
		})
	}
}

// TestCrashLoopFastExits_WithinTenSecondsOnDefaultLadder pins the AC's bound: the
// notice goes out as the Nth fast exit enters backoff, so the backoff already
// waited is the sum of the N-1 delays before it.
func TestCrashLoopFastExits_WithinTenSecondsOnDefaultLadder(t *testing.T) {
	t.Parallel()
	bo := newBackoffTimer(defaultBackoffInitial, defaultBackoffMax, defaultBackoffReset)
	var waited time.Duration
	for i := 0; i < crashLoopFastExits-1; i++ {
		waited += bo.next(0)
	}
	if waited > 10*time.Second {
		t.Fatalf("backoff waited before the notice = %v, want <= 10s", waited)
	}
}

// TestRunner_OnCrashLoopFiresOncePerEpisode drives Run against a child that exits
// within 20ms every time, so every iteration is a fast exit in one long episode.
func TestRunner_OnCrashLoopFiresOncePerEpisode(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "crash", &safeBuffer{}, &safeBuffer{})
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 2 * time.Millisecond
	var spawns, fired atomic.Int32
	cfg.onSpawn = func(int) { spawns.Add(1) }
	cfg.OnCrashLoop = func() { fired.Add(1) }

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer join()
	defer cancel()

	deadline := time.Now().Add(10 * time.Second)
	for spawns.Load() < 3*crashLoopFastExits {
		if time.Now().After(deadline) {
			t.Fatalf("saw %d spawns in 10s, want %d", spawns.Load(), 3*crashLoopFastExits)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("OnCrashLoop fired %d times across %d fast exits, want 1", got, spawns.Load()-1)
	}
}

// TestRunner_OnCrashLoopIgnoresDeliberateRestarts restarts a healthy child more
// than N times in quick succession. Each relaunch is immediate and fast, but none
// enters the backoff ladder, so none is a crash.
func TestRunner_OnCrashLoopIgnoresDeliberateRestarts(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) { spawned <- struct{}{} }
	var fired atomic.Int32
	cfg.OnCrashLoop = func() { fired.Add(1) }

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer join()
	defer cancel()

	waitSpawn := func(i int) {
		t.Helper()
		select {
		case <-spawned:
		case <-time.After(5 * time.Second):
			t.Fatalf("spawn %d did not happen within 5s", i)
		}
	}
	waitSpawn(0)
	for i := 1; i <= crashLoopFastExits+1; i++ {
		r.Restart(nil)
		waitSpawn(i)
	}
	if got := fired.Load(); got != 0 {
		t.Fatalf("OnCrashLoop fired %d times across deliberate restarts, want 0", got)
	}
	if got := r.State().RestartCount; got != 0 {
		t.Fatalf("RestartCount = %d, want 0 — a restart entered the backoff ladder", got)
	}
}
