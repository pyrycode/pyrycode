//go:build e2e

package e2e

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// TestE2E_ConvSweep_RemovesUnpromotedKeepsPromoted seeds two conversations
// with LastUsedAt 60 days in the past — one promoted, one unpromoted — and
// asserts that within a bounded budget the running daemon's sweep loop
// removes the unpromoted entry from the on-disk conversations.json while
// the promoted entry persists. After the asserts, the daemon is sent
// SIGTERM and is expected to exit cleanly within the harness's escalation
// window without panicking.
//
// Closes the gap left by internal/sessions.TestPool_Run_RegistersSweepLoop_HappyPath:
// that test exercises Pool.Run's wiring directly, but cannot catch a
// regression in cmd/pyry/main.go's sessions.Config construction or in the
// full process lifecycle (signal-driven shutdown of the sweep goroutine).
func TestE2E_ConvSweep_RemovesUnpromotedKeepsPromoted(t *testing.T) {
	home, _ := newRegistryHome(t)
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")

	sixtyDaysAgo := time.Now().UTC().Add(-60 * 24 * time.Hour)

	promotedName := "kept-channel"
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:         "11111111-1111-4111-8111-111111111111",
		Name:       &promotedName,
		Cwd:        "/seed-promoted",
		IsPromoted: true,
		LastUsedAt: sixtyDaysAgo,
	})
	reg.Create(conversations.Conversation{
		ID:         "22222222-2222-4222-8222-222222222222",
		Cwd:        "/seed-unpromoted",
		IsPromoted: false,
		LastUsedAt: sixtyDaysAgo,
	})
	if err := reg.Save(convPath); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	h := StartIn(t, home, "-pyry-conv-sweep-interval=100ms")
	pid := h.PID

	// Poll the on-disk file (atomic-rename Save makes cross-process reads
	// race-clean) until the unpromoted entry has been swept. 50ms gap
	// against a 100ms tick gives ~10 chances inside the 5s budget.
	var swept *conversations.Registry
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		loaded, err := conversations.Load(convPath)
		if err != nil {
			t.Fatalf("Load while polling: %v", err)
		}
		if len(loaded.List()) == 1 {
			swept = loaded
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if swept == nil {
		t.Fatalf("sweep did not remove unpromoted entry within 5s; current file:\n%s",
			mustReadFile(t, convPath))
	}

	survivors := swept.List()
	if got := string(survivors[0].ID); got != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("survivor ID = %q, want promoted entry", got)
	}
	if !survivors[0].IsPromoted {
		t.Errorf("survivor IsPromoted = false, want true: %+v", survivors[0])
	}

	// Stop sends SIGTERM with a 3s grace, then escalates to SIGKILL with
	// a further 1s — a 4s upper bound, comfortably under AC#4's 5s.
	// processAlive afterwards catches the killGrace-exceeded case
	// (Stop only t.Logf's that path, doesn't fail). The stderr scan
	// catches a panic on the way down (Stop doesn't inspect stderr).
	h.Stop(t)
	if processAlive(pid) {
		t.Errorf("daemon pid=%d still alive after Stop", pid)
	}
	for _, bad := range [][]byte{[]byte("panic"), []byte("runtime/"), []byte("goroutine ")} {
		if bytes.Contains(h.Stderr.Bytes(), bad) {
			t.Errorf("stderr contains %q — expected clean exit, not crash:\n%s",
				bad, h.Stderr.Bytes())
		}
	}
}
