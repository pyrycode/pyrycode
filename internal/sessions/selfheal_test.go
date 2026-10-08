package sessions

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestRotateBootstrapForSelfHeal verifies identity persistence and the internal recovery fact.
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

	t.Run("fires recovery without a legacy reason", func(t *testing.T) {
		pool := helperPool(t, false)
		rec := &transitionRecorder{}
		pool.SetTransitionObserver(rec.observe)

		if _, err := pool.RotateBootstrapForSelfHeal(); err != nil {
			t.Fatalf("RotateBootstrapForSelfHeal: %v", err)
		}
		if signals := rec.snapshot(); len(signals) != 1 || signals[0].Cause != CauseRecovery || signals[0].Reason != "" {
			t.Errorf("recovery facts = %+v", signals)
		}
	})
}
