package sessions

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// ptr returns a pointer to v — the presence marker for SettingsUpdate fields.
func ptr[T any](v T) *T { return &v }

// helperPoolWithSettings warm-starts a persistent Pool whose bootstrap session
// begins with the given stored settings, giving UpdateSettings a known starting
// point to merge over. The supervisor target is /bin/sleep, never spawned
// (these tests don't call Run). New does not re-persist on warm start, so the
// on-disk bytes after this returns are exactly what saveRegistryLocked wrote.
func helperPoolWithSettings(t *testing.T, regPath string, settings SessionSettings) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	when := time.Now().UTC()
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID:           SessionID("550e8400-e29b-41d4-a716-446655440000"),
			CreatedAt:    when,
			LastActiveAt: when,
			Bootstrap:    true,
			Model:        settings.Model,
			Effort:       settings.Effort,
			YOLO:         settings.YOLO,
		}},
	}); err != nil {
		t.Fatalf("pre-write registry: %v", err)
	}
	pool, err := New(Config{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath: regPath,
		Bootstrap:    SessionConfig{ClaudeBin: "/bin/sleep"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return pool
}

// diskSettings reads the bootstrap entry's persisted settings back off disk.
func diskSettings(t *testing.T, regPath string) SessionSettings {
	t.Helper()
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	e := pickBootstrap(reg)
	if e == nil {
		t.Fatalf("registry has no bootstrap entry: %+v", reg)
	}
	return SessionSettings{Model: e.Model, Effort: e.Effort, YOLO: e.YOLO}
}

// TestPool_UpdateSettings_PartialMerge (AC1): only the fields the caller marks
// present change; unmarked fields keep their stored value on disk. Covers
// per-field updates and the empty-string-is-a-real-value case.
func TestPool_UpdateSettings_PartialMerge(t *testing.T) {
	t.Parallel()
	start := SessionSettings{Model: "sonnet", Effort: "low", YOLO: false}
	cases := []struct {
		name   string
		update SettingsUpdate
		want   SessionSettings
	}{
		{"model only", SettingsUpdate{Model: ptr("opus")}, SessionSettings{Model: "opus", Effort: "low", YOLO: false}},
		{"effort only", SettingsUpdate{Effort: ptr("high")}, SessionSettings{Model: "sonnet", Effort: "high", YOLO: false}},
		{"yolo only", SettingsUpdate{YOLO: ptr(true)}, SessionSettings{Model: "sonnet", Effort: "low", YOLO: true}},
		{"empty-string model clears (present, not omitted)", SettingsUpdate{Model: ptr("")}, SessionSettings{Model: "", Effort: "low", YOLO: false}},
		{"all three at once", SettingsUpdate{Model: ptr("opus"), Effort: ptr("high"), YOLO: ptr(true)}, SessionSettings{Model: "opus", Effort: "high", YOLO: true}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			regPath := filepath.Join(dir, "sessions.json")
			pool := helperPoolWithSettings(t, regPath, start)
			id := pool.Default().ID()

			if err := pool.UpdateSettings(id, tc.update); err != nil {
				t.Fatalf("UpdateSettings: %v", err)
			}
			if got := diskSettings(t, regPath); got != tc.want {
				t.Errorf("on-disk settings = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestPool_UpdateSettings_YOLOAbsentLeavesStored (AC3): an update with YOLO
// omitted (nil) — even while it changes Model/Effort — must leave the stored
// YOLO untouched in either direction; an absent field can never flip bypass on.
func TestPool_UpdateSettings_YOLOAbsentLeavesStored(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stored bool
	}{
		{"stored off stays off", false},
		{"stored on stays on", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			regPath := filepath.Join(dir, "sessions.json")
			pool := helperPoolWithSettings(t, regPath, SessionSettings{Model: "sonnet", YOLO: tc.stored})
			id := pool.Default().ID()

			if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("opus"), Effort: ptr("high")}); err != nil {
				t.Fatalf("UpdateSettings: %v", err)
			}
			if got := diskSettings(t, regPath); got.YOLO != tc.stored {
				t.Errorf("YOLO = %v after nil-YOLO update, want %v (absence must never change bypass)", got.YOLO, tc.stored)
			}
		})
	}
}

// TestPool_UpdateSettings_YOLOPresentFlipsBothWays (AC3): an explicit non-nil
// YOLO changes the stored value in both directions.
func TestPool_UpdateSettings_YOLOPresentFlipsBothWays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stored bool
		set    bool
	}{
		{"off to on", false, true},
		{"on to off", true, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			regPath := filepath.Join(dir, "sessions.json")
			pool := helperPoolWithSettings(t, regPath, SessionSettings{YOLO: tc.stored})
			id := pool.Default().ID()

			if err := pool.UpdateSettings(id, SettingsUpdate{YOLO: ptr(tc.set)}); err != nil {
				t.Fatalf("UpdateSettings: %v", err)
			}
			if got := diskSettings(t, regPath); got.YOLO != tc.set {
				t.Errorf("YOLO = %v, want %v", got.YOLO, tc.set)
			}
		})
	}
}

// TestPool_UpdateSettings_UnknownID (AC4): an unknown id returns
// ErrSessionNotFound with no entry created and the registry bytes/mtime
// byte-identical to the prior state.
func TestPool_UpdateSettings_UnknownID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolWithSettings(t, regPath, SessionSettings{Model: "sonnet", Effort: "low"})

	beforeBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	beforeStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	unknown := SessionID("00000000-0000-0000-0000-000000000000")
	err = pool.UpdateSettings(unknown, SettingsUpdate{Model: ptr("opus"), YOLO: ptr(true)})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("UpdateSettings(unknown) err = %v, want ErrSessionNotFound", err)
	}

	afterBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Errorf("registry bytes changed on unknown-id update:\nbefore=%s\nafter =%s", beforeBytes, afterBytes)
	}
	afterStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("registry mtime changed on unknown-id update: before=%v after=%v",
			beforeStat.ModTime(), afterStat.ModTime())
	}
}

// TestPool_UpdateSettings_NoOpWritesNothing (byte-stability): an update that
// changes nothing — all fields nil, or every present field already equal to the
// stored value — returns nil and leaves the registry bytes/mtime untouched.
func TestPool_UpdateSettings_NoOpWritesNothing(t *testing.T) {
	t.Parallel()
	start := SessionSettings{Model: "sonnet", Effort: "low", YOLO: true}
	cases := []struct {
		name   string
		update SettingsUpdate
	}{
		{"all fields nil", SettingsUpdate{}},
		{"present fields equal stored", SettingsUpdate{Model: ptr("sonnet"), Effort: ptr("low"), YOLO: ptr(true)}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			regPath := filepath.Join(dir, "sessions.json")
			pool := helperPoolWithSettings(t, regPath, start)
			id := pool.Default().ID()

			beforeBytes, err := os.ReadFile(regPath)
			if err != nil {
				t.Fatalf("read before: %v", err)
			}
			beforeStat, err := os.Stat(regPath)
			if err != nil {
				t.Fatalf("stat before: %v", err)
			}

			if err := pool.UpdateSettings(id, tc.update); err != nil {
				t.Fatalf("UpdateSettings: %v", err)
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
				t.Errorf("no-op update changed mtime: before=%v after=%v",
					beforeStat.ModTime(), afterStat.ModTime())
			}
		})
	}
}

// TestPool_UpdateSettings_ConcurrentSerialize (AC2, -race): concurrent updaters
// serialize on Pool.mu. Each goroutine writes a self-consistent full triple, so
// the final on-disk state must exactly match one goroutine's triple — no torn
// or interleaved fields — and the run must be race-detector clean.
func TestPool_UpdateSettings_ConcurrentSerialize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolWithSettings(t, regPath, SessionSettings{})
	id := pool.Default().ID()

	const goroutines = 8
	const iters = 50

	valid := make(map[SessionSettings]bool, goroutines)
	for i := 0; i < goroutines; i++ {
		valid[SessionSettings{Model: fmt.Sprintf("m%d", i), Effort: fmt.Sprintf("e%d", i), YOLO: i%2 == 0}] = true
	}

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			upd := SettingsUpdate{Model: ptr(fmt.Sprintf("m%d", i)), Effort: ptr(fmt.Sprintf("e%d", i)), YOLO: ptr(i%2 == 0)}
			for j := 0; j < iters; j++ {
				if err := pool.UpdateSettings(id, upd); err != nil {
					t.Errorf("UpdateSettings: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := diskSettings(t, regPath); !valid[got] {
		t.Errorf("final on-disk settings %+v is not any issued triple (torn write)", got)
	}
}

// TestPool_UpdateSettings_RoundTripToSpawnArgv (AC5): an update persists through
// saveLocked to disk, and #833's spawn path launches that session's claude with
// the new settings after a simulated daemon restart (a second Pool warm-started
// from the same registry). Exercises setter → saveLocked → disk → New warm-start
// → claudeSettingsArgs → argv.
func TestPool_UpdateSettings_RoundTripToSpawnArgv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()

	// Cold start: bootstrap begins with no settings. This pool is never run;
	// it only applies the update and persists it.
	first := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	id := first.Default().ID()

	if err := first.UpdateSettings(id, SettingsUpdate{Model: ptr("opus"), Effort: ptr("high"), YOLO: ptr(true)}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if got := diskSettings(t, regPath); got != (SessionSettings{Model: "opus", Effort: "high", YOLO: true}) {
		t.Fatalf("on-disk settings after update = %+v, want opus/high/true", got)
	}

	// Simulated daemon restart: a second Pool warm-starts from the same
	// registry and spawns the bootstrap child, which records its own argv.
	second := helperPoolArgvRecorder(t, regPath, tplWorkDir)
	runPoolInBackground(t, second)

	got := waitArgv(t, tplWorkDir)
	want := []string{"--model", "opus", "--effort", "high", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("post-update bootstrap argv = %v, want %v", got, want)
	}
}
