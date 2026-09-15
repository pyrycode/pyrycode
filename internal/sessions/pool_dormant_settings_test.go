package sessions

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

// liveIDs is the pool's live session set, sorted — the "materialises nothing"
// assertion's subject. Pool.List is deliberately the source rather than a direct
// p.sessions read: it takes the same RLock every other reader does, so the
// assertion stays race-clean against the pool's own goroutines.
func liveIDs(p *Pool) []SessionID {
	infos := p.List()
	out := make([]SessionID, 0, len(infos))
	for _, i := range infos {
		out = append(out, i.ID)
	}
	slices.Sort(out)
	return out
}

// TestPool_DormantSettingsFor_AnswersPersistedModelAndEffort (AC 1): an entry
// this pool parsed but did not materialise answers with its own model and
// effort, where SettingsFor answers ErrSessionNotFound.
//
// Both halves are asserted on one id, because "the dormant read answers" is only
// interesting beside "the live read still refuses": a read that quietly widened
// SettingsFor itself would pass the first assertion alone.
func TestPool_DormantSettingsFor_AnswersPersistedModelAndEffort(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	configured, bare := helperDormantID(t), helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: configured, Label: "conv-1", Model: "opus", Effort: "high"},
		registryEntry{ID: bare, Label: "conv-2"},
	)

	if _, err := pool.SettingsFor(configured); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("SettingsFor(dormant) err = %v, want ErrSessionNotFound — the live read must keep its meaning", err)
	}

	got, err := pool.DormantSettingsFor(configured)
	if err != nil {
		t.Fatalf("DormantSettingsFor(%q): %v", configured, err)
	}
	if got.Model != "opus" || got.Effort != "high" {
		t.Errorf("model/effort = %q/%q, want %q/%q", got.Model, got.Effort, "opus", "high")
	}

	// An entry that persisted neither field is a real answer, not a refusal: the
	// conversation is on the daemon's defaults and the reply says so.
	empty, err := pool.DormantSettingsFor(bare)
	if err != nil {
		t.Fatalf("DormantSettingsFor(%q): %v", bare, err)
	}
	if empty.Model != "" || empty.Effort != "" {
		t.Errorf("model/effort = %q/%q, want both empty", empty.Model, empty.Effort)
	}
}

// TestPool_DormantSettingsFor_RevokesPersistedPosture (AC 2): neither a
// persisted yolo:true nor a persisted non-default permission_mode is reported,
// and what comes back is the posture Revive will actually materialise.
//
// The fixture persists BOTH spellings with non-zero values, which is what makes
// the assertion unforgeable: an implementation that copies the entry and clears
// the two fields afterwards passes a zero-valued fixture, and so does one that
// forgets the clearing statement entirely.
func TestPool_DormantSettingsFor_RevokesPersistedPosture(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	bypassed, planned := helperDormantID(t), helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: bypassed, Label: "conv-1", Model: "opus", YOLO: true},
		registryEntry{ID: planned, Label: "conv-2", Effort: "high", PermissionMode: "plan"},
	)

	for _, tc := range []struct {
		name string
		id   SessionID
	}{
		{"a persisted bypass", bypassed},
		{"a persisted non-default mode", planned},
	} {
		got, err := pool.DormantSettingsFor(tc.id)
		if err != nil {
			t.Fatalf("%s: DormantSettingsFor: %v", tc.name, err)
		}
		if got.YOLO {
			t.Errorf("%s: YOLO = true, want false — a restart is a revocation point for a phone-granted bypass (#1487)", tc.name)
		}
		if got.PermissionMode != permissionModeDefault {
			t.Errorf("%s: PermissionMode = %q, want %q — the reported posture must be the one Revive materialises", tc.name, got.PermissionMode, permissionModeDefault)
		}
	}

	// The model and effort beside the revoked posture are still the entry's own,
	// so the revocation is not a blanket zeroing of everything persisted.
	if got, _ := pool.DormantSettingsFor(bypassed); got.Model != "opus" {
		t.Errorf("model = %q, want %q — revoking the posture must not drop the model", got.Model, "opus")
	}
}

// TestPool_DormantSettingsFor_MaterialisesNothing (AC 3): the read is a read.
// After it the session is still dormant and the pool holds exactly the live
// sessions it held before.
//
// The live-set comparison is what carries the claim. "Still dormant" alone would
// pass under a read that materialised a SECOND session as a side effect, and this
// verb fires on every channel activation — a read that revived would turn N
// channel opens into N claude children.
func TestPool_DormantSettingsFor_MaterialisesNothing(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	target := helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: target, Label: "conv-1", Model: "opus", Effort: "high"},
	)

	before := liveIDs(pool)
	for range 3 {
		if _, err := pool.DormantSettingsFor(target); err != nil {
			t.Fatalf("DormantSettingsFor: %v", err)
		}
	}

	if _, err := pool.Lookup(target); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup(target) err = %v, want ErrSessionNotFound — the read materialised the session", err)
	}
	if after := liveIDs(pool); !slices.Equal(before, after) {
		t.Errorf("live sessions = %q, want %q unchanged — the read must spawn nothing", after, before)
	}
}

// TestPool_DormantSettingsFor_UnknownIDIsNotFound (AC 5): an id in neither half
// is a miss of this read's own, not the zero SessionSettings.
//
// This is the whole reason Pool.revivedSettings could not be reused: it collapses
// "no entry" into the zero value, which is correct for a revive and would make a
// caller here report an unknown bound id beside empty settings.
func TestPool_DormantSettingsFor_UnknownIDIsNotFound(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	never := helperDormantID(t)

	pool, boot := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: helperDormantID(t), Label: "conv-1", Model: "opus"},
	)

	for _, tc := range []struct {
		name string
		id   SessionID
	}{
		{"an id this pool never held", never},
		{"the empty id", ""},
		{"the live bootstrap, which is not dormant", boot},
	} {
		got, err := pool.DormantSettingsFor(tc.id)
		if !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("%s: DormantSettingsFor err = %v, want ErrSessionNotFound", tc.name, err)
		}
		if got != (SessionSettings{}) {
			t.Errorf("%s: DormantSettingsFor = %+v, want the zero value beside the error", tc.name, got)
		}
	}
}

// TestPool_DormantSettingsFor_MissesAfterRevive: live and dormant PARTITION.
// Reviving an id retires its dormant entry (materialise's contract, #2448), and
// this read has to see that — otherwise one id could be answered from both
// halves, and a caller's read order would decide which settings a conversation
// reports.
func TestPool_DormantSettingsFor_MissesAfterRevive(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	target := helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: target, Label: "conv-1", Model: "opus", Effort: "high"},
	)
	runPoolInBackground(t, pool)

	if _, err := pool.DormantSettingsFor(target); err != nil {
		t.Fatalf("DormantSettingsFor(before revive): %v", err)
	}
	if _, err := pool.Revive(target, "conv-1", ""); err != nil {
		t.Fatalf("Revive: %v", err)
	}

	if _, err := pool.DormantSettingsFor(target); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("DormantSettingsFor(after revive) err = %v, want ErrSessionNotFound — materialise retires the entry it takes over", err)
	}
	if _, err := pool.SettingsFor(target); err != nil {
		t.Errorf("SettingsFor(after revive): %v — the id must have moved to the live half, not vanished", err)
	}
}
