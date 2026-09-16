package sessions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
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

// TestPool_UpdateDormantSettings_MergesModelAndEffort (AC 1, and the read half of
// AC 3): a write naming model and/or effort lands in the dormant entry, is
// visible to DormantSettingsFor immediately, and reaches the registry file.
//
// Asserted on one id beside UpdateSettings still refusing that id, the read
// tests' discipline: a write that quietly widened the LIVE method instead of
// adding a dormant one would pass the first assertion alone.
//
// The second write names only the model, so an implementation that overwrites
// the whole entry from the update — rather than overlaying the present fields —
// is red on the effort it dropped.
func TestPool_UpdateDormantSettings_MergesModelAndEffort(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	target := helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: target, Label: "conv-1", Model: "sonnet", Effort: "low"},
	)

	if err := pool.UpdateSettings(target, SettingsUpdate{Model: ptr("opus")}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("UpdateSettings(dormant) err = %v, want ErrSessionNotFound — the live write must keep its meaning", err)
	}

	if err := pool.UpdateDormantSettings(target, SettingsUpdate{Model: ptr("opus"), Effort: ptr("high")}); err != nil {
		t.Fatalf("UpdateDormantSettings(model+effort): %v", err)
	}
	got, err := pool.DormantSettingsFor(target)
	if err != nil {
		t.Fatalf("DormantSettingsFor after write: %v", err)
	}
	if got.Model != "opus" || got.Effort != "high" {
		t.Errorf("model/effort after write = %q/%q, want %q/%q", got.Model, got.Effort, "opus", "high")
	}

	// A partial update leaves the field it did not name alone.
	if err := pool.UpdateDormantSettings(target, SettingsUpdate{Model: ptr("sonnet")}); err != nil {
		t.Fatalf("UpdateDormantSettings(model only): %v", err)
	}
	if got, err = pool.DormantSettingsFor(target); err != nil {
		t.Fatalf("DormantSettingsFor after partial write: %v", err)
	}
	if got.Model != "sonnet" || got.Effort != "high" {
		t.Errorf("model/effort after partial write = %q/%q, want %q/%q", got.Model, got.Effort, "sonnet", "high")
	}

	// The value has to survive the process, which is the whole point of writing
	// into the entry rather than into memory: read it back off disk.
	entry := entryByID(t, regPath, target)
	if entry == nil {
		t.Fatalf("registry has no entry for %q after the write", target)
	}
	if entry.Model != "sonnet" || entry.Effort != "high" {
		t.Errorf("persisted model/effort = %q/%q, want %q/%q", entry.Model, entry.Effort, "sonnet", "high")
	}

	// An explicit empty model keeps its "run at claude's own default" reading —
	// there is no child to restart, so the live path's restart semantics do not
	// arise, but the stored value must still clear.
	if err := pool.UpdateDormantSettings(target, SettingsUpdate{Model: ptr("")}); err != nil {
		t.Fatalf("UpdateDormantSettings(explicit clear): %v", err)
	}
	if got, err = pool.DormantSettingsFor(target); err != nil {
		t.Fatalf("DormantSettingsFor after clear: %v", err)
	}
	if got.Model != "" || got.Effort != "high" {
		t.Errorf("model/effort after clear = %q/%q, want %q/%q", got.Model, got.Effort, "", "high")
	}
}

// TestPool_UpdateDormantSettings_MaterialisesNothing (AC 2): the write is a
// write into a registry entry, not a revive.
//
// The live-set comparison is what carries the claim, for the reason the read
// half's twin records: "still dormant" alone would pass under a write that
// materialised a SECOND session as a side effect. A settings frame that could
// spawn a claude child is the thing #2449 AC 3 made the read refuse, and the
// write rides the same frame family on the same restart edge.
func TestPool_UpdateDormantSettings_MaterialisesNothing(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	target := helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: target, Label: "conv-1", Model: "sonnet"},
	)

	before := liveIDs(pool)
	for i, model := range []string{"opus", "haiku", "sonnet"} {
		if err := pool.UpdateDormantSettings(target, SettingsUpdate{Model: ptr(model)}); err != nil {
			t.Fatalf("UpdateDormantSettings #%d: %v", i, err)
		}
	}

	if _, err := pool.Lookup(target); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Lookup(target) err = %v, want ErrSessionNotFound — the write materialised the session", err)
	}
	if after := liveIDs(pool); !slices.Equal(before, after) {
		t.Errorf("live sessions = %q, want %q unchanged — the write must spawn nothing", after, before)
	}
}

// TestPool_UpdateDormantSettings_RevivesUnderTheWrittenValue (AC 3, argv half):
// the written value is what the session comes back as. Write, then revive, then
// read the claude argv the first spawn actually carried.
//
// This is the assertion the whole design rests on — that merging into the entry
// is enough, because the entry is already what Pool.revivedSettings hands
// materialise — so it goes through Revive and Activate rather than through
// another read of the map.
//
// The written model is an EXACT id rather than a bare family, so the assertion
// pins #2447's rewrite: --model names the family alias of the stored value, not
// the stored bytes. Writing "opus" would pass either way.
func TestPool_UpdateDormantSettings_RevivesUnderTheWrittenValue(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	spawnDir := t.TempDir()
	target := helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: target, Label: "conv-1", Model: "sonnet", Effort: "low"},
	)
	ctx, _ := runPoolInBackground(t, pool)

	if err := pool.UpdateDormantSettings(target, SettingsUpdate{
		Model:  ptr("claude-opus-4-5-20251101"),
		Effort: ptr("high"),
	}); err != nil {
		t.Fatalf("UpdateDormantSettings: %v", err)
	}

	sess, err := pool.Revive(target, "conv-1", spawnDir)
	if err != nil {
		t.Fatalf("Revive: %v", err)
	}
	// Revive registers without spawning; Activate is what brings the child up.
	if err := pool.Activate(ctx, target); err != nil {
		t.Fatalf("Activate(revived): %v", err)
	}
	if !pollUntil(t, 5*time.Second, func() bool { return sess.State().ChildPID > 0 }) {
		t.Fatalf("revived session never spawned a child; state=%+v lc=%v", sess.State(), sess.LifecycleState())
	}

	got := waitArgv(t, spawnDir)
	want := append([]string{"--session-id", string(target), "--model", "opus", "--effort", "high"},
		alwaysOnPosture(permissionModeDefault)...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("revived argv = %v, want %v — the revive must carry the WRITTEN value, not the one the entry was loaded with", got, want)
	}
}

// TestPool_UpdateDormantSettings_RefusesPosture (AC 5): a frame naming yolo or
// permission_mode is refused and persists no field of itself.
//
// Every case carries a model and an effort BESIDE the posture, which is what
// makes "persists no field of itself" testable rather than assumed: an
// implementation that merged the model first and refused afterwards is red here
// and green against a posture-only fixture.
//
// The fixture entry persists BOTH on-disk posture spellings with non-zero
// values, and they are asserted byte-identical afterwards. A revive does not
// restore a persisted posture (#1487), so those bytes are inert — but a write
// that rewrote or cleared them would be changing state nobody asked it to touch.
func TestPool_UpdateDormantSettings_RefusesPosture(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	target := helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: target, Label: "conv-1", Model: "sonnet", Effort: "low", YOLO: true, PermissionMode: "plan"},
	)

	for _, tc := range []struct {
		name   string
		update SettingsUpdate
	}{
		{"an escalation", SettingsUpdate{Model: ptr("opus"), Effort: ptr("high"), YOLO: ptr(true)}},
		{"a revoke", SettingsUpdate{Model: ptr("opus"), Effort: ptr("high"), YOLO: ptr(false)}},
		{"a non-default mode", SettingsUpdate{Model: ptr("opus"), Effort: ptr("high"), PermissionMode: ptr("plan")}},
		{"the bypass mode", SettingsUpdate{Model: ptr("opus"), Effort: ptr("high"), PermissionMode: ptr(permissionModeBypass)}},
		{"a posture alone", SettingsUpdate{PermissionMode: ptr("acceptEdits")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := pool.UpdateDormantSettings(target, tc.update); !errors.Is(err, ErrDormantPostureUnsupported) {
				t.Fatalf("UpdateDormantSettings err = %v, want ErrDormantPostureUnsupported", err)
			}

			got, err := pool.DormantSettingsFor(target)
			if err != nil {
				t.Fatalf("DormantSettingsFor: %v", err)
			}
			if got.Model != "sonnet" || got.Effort != "low" {
				t.Errorf("model/effort = %q/%q, want %q/%q — the refusal must persist no field of the frame", got.Model, got.Effort, "sonnet", "low")
			}

			entry := entryByID(t, regPath, target)
			if entry == nil {
				t.Fatalf("registry has no entry for %q", target)
			}
			if entry.Model != "sonnet" || entry.Effort != "low" {
				t.Errorf("persisted model/effort = %q/%q, want %q/%q", entry.Model, entry.Effort, "sonnet", "low")
			}
			if !entry.YOLO || entry.PermissionMode != "plan" {
				t.Errorf("persisted posture = yolo:%v mode:%q, want the fixture's yolo:true mode:%q untouched",
					entry.YOLO, entry.PermissionMode, "plan")
			}
		})
	}
}

// TestPool_UpdateDormantSettings_UnknownIDIsNotFound: an id this pool holds no
// dormant entry for is a miss, and nothing is created for it.
//
// The live bootstrap is the partition case and the interesting one: a live id
// must not be reachable through the dormant seam, or one id could be written
// through both halves and the caller's order would decide which write survived.
//
// The empty id is the ""-is-bootstrap case. Pool.Lookup("") deliberately
// resolves the bootstrap for legacy internal callers; neither p.sessions nor
// p.dormant carries a "" key, so fall-through-to-bootstrap has no expression on
// this path at all.
func TestPool_UpdateDormantSettings_UnknownIDIsNotFound(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	never, held := helperDormantID(t), helperDormantID(t)

	pool, boot := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: held, Label: "conv-1", Model: "sonnet"},
	)

	for _, tc := range []struct {
		name string
		id   SessionID
	}{
		{"an id this pool never held", never},
		{"the empty id", ""},
		{"the live bootstrap, which is not dormant", boot},
	} {
		if err := pool.UpdateDormantSettings(tc.id, SettingsUpdate{Model: ptr("opus")}); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("%s: UpdateDormantSettings err = %v, want ErrSessionNotFound", tc.name, err)
		}
		if _, err := pool.DormantSettingsFor(tc.id); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("%s: the refused write created a dormant entry", tc.name)
		}
	}

	// The bootstrap's own live settings are untouched by the refusal above — the
	// write must not have reached the live half by another route.
	if got, err := pool.SettingsFor(boot); err != nil || got.Model != "" {
		t.Errorf("bootstrap settings = %+v (err %v), want the model still unset", got, err)
	}
}

// TestPool_UpdateDormantSettings_NoOpWritesNothing (byte-stability): an update
// that changes nothing returns nil and leaves the registry bytes and mtime
// untouched — Pool.UpdateSettings' own no-op contract, carried over to the half
// of the shape that still applies.
func TestPool_UpdateDormantSettings_NoOpWritesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		update SettingsUpdate
	}{
		{"all fields nil", SettingsUpdate{}},
		{"present fields equal stored", SettingsUpdate{Model: ptr("sonnet"), Effort: ptr("low")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regPath := filepath.Join(t.TempDir(), "sessions.json")
			target := helperDormantID(t)

			pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
				registryEntry{ID: target, Label: "conv-1", Model: "sonnet", Effort: "low"},
			)

			beforeBytes, err := os.ReadFile(regPath)
			if err != nil {
				t.Fatalf("read before: %v", err)
			}
			beforeStat, err := os.Stat(regPath)
			if err != nil {
				t.Fatalf("stat before: %v", err)
			}

			if err := pool.UpdateDormantSettings(target, tc.update); err != nil {
				t.Fatalf("UpdateDormantSettings: %v", err)
			}

			afterBytes, err := os.ReadFile(regPath)
			if err != nil {
				t.Fatalf("read after: %v", err)
			}
			if !bytes.Equal(beforeBytes, afterBytes) {
				t.Errorf("no-op update rewrote registry:\nbefore=%s\nafter =%s", beforeBytes, afterBytes)
			}
			afterStat, err := os.Stat(regPath)
			if err != nil {
				t.Fatalf("stat after: %v", err)
			}
			if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
				t.Errorf("no-op update changed mtime: before=%v after=%v", beforeStat.ModTime(), afterStat.ModTime())
			}
		})
	}
}
