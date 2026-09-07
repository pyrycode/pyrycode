package sessions

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestRotateBootstrapForSelfHeal covers the supervisor-driven crash-loop
// self-heal rotation primitive (#1165): it mints a fresh daemon id, re-keys the
// CURRENT bootstrap entry to it (preserving the *Session pointer), flips
// BootstrapID, and re-persists the registry — but, unlike RotateForNewSession,
// it does NOT fire a client transition, which would mislead clients into
// thinking the user ran /clear. It also used to differ by not priming the
// freshly-allocated skip-set, but #2137 retired the rotation watcher that set
// existed for, so that half of the asymmetry is gone from both methods.
func TestRotateBootstrapForSelfHeal(t *testing.T) {
	t.Parallel()

	t.Run("mints fresh id, rekeys bootstrap, persists", func(t *testing.T) {
		regDir := t.TempDir()
		regPath := filepath.Join(regDir, "sessions.json")
		pool := helperPoolPersistent(t, regPath)

		orig := pool.Default()
		oldID := pool.BootstrapID()

		newID, err := pool.RotateBootstrapForSelfHeal()
		if err != nil {
			t.Fatalf("RotateBootstrapForSelfHeal: %v", err)
		}
		if newID == "" || !ValidID(string(newID)) {
			t.Fatalf("newID = %q, want a fresh valid UUID", newID)
		}
		if newID == oldID {
			t.Fatalf("newID == oldID (%q); want a distinct minted id", newID)
		}

		// BootstrapID now resolves the new id; the old id is gone from the pool.
		if got := pool.BootstrapID(); got != newID {
			t.Errorf("BootstrapID() = %q, want %q (rotated)", got, newID)
		}
		if _, err := pool.Lookup(oldID); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("Lookup(oldID) err = %v, want ErrSessionNotFound (entry should have moved)", err)
		}
		// The SAME *Session pointer is preserved through the rekey.
		got, err := pool.Lookup(newID)
		if err != nil {
			t.Fatalf("Lookup(newID): %v", err)
		}
		if got != orig {
			t.Errorf("Lookup(newID) returned a different *Session; want the rotated entry preserved")
		}

		// Registry re-persisted with the new id as the sole bootstrap entry.
		reg, err := loadRegistry(regPath)
		if err != nil {
			t.Fatalf("loadRegistry: %v", err)
		}
		if reg == nil || len(reg.Sessions) != 1 {
			t.Fatalf("registry = %+v, want exactly one session", reg)
		}
		if reg.Sessions[0].ID != newID {
			t.Errorf("persisted id = %q, want %q (rotated id must survive reload)", reg.Sessions[0].ID, newID)
		}
		if !reg.Sessions[0].Bootstrap {
			t.Errorf("persisted entry Bootstrap = false, want true")
		}
	})

	t.Run("does not fire a client transition", func(t *testing.T) {
		pool := helperPool(t, false)
		rec := &transitionRecorder{}
		pool.SetTransitionObserver(rec.observe)

		if _, err := pool.RotateBootstrapForSelfHeal(); err != nil {
			t.Fatalf("RotateBootstrapForSelfHeal: %v", err)
		}
		if signals := rec.snapshot(); len(signals) != 0 {
			t.Errorf("observer fired %d times, want 0 — self-heal is a supervisor-internal crash-recovery rotation and must NOT emit ReasonClear (that would mislead clients into thinking the user ran /clear): %+v", len(signals), signals)
		}
	})
}
