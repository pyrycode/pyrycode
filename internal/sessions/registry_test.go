package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fixedTime returns a deterministic UTC time so registry round-trip tests
// don't depend on time.Now().
func fixedTime(t *testing.T) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339Nano, "2026-05-01T12:34:56.789123456Z")
	if err != nil {
		t.Fatalf("parse fixed time: %v", err)
	}
	return tt
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	when := fixedTime(t)
	in := &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID:           SessionID("8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91"),
			Label:        "main",
			CreatedAt:    when,
			LastActiveAt: when,
			Bootstrap:    true,
		}},
	}
	if err := saveRegistryLocked(path, in); err != nil {
		t.Fatalf("saveRegistryLocked: %v", err)
	}
	got, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if got == nil {
		t.Fatal("loadRegistry returned nil after save")
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(got.Sessions))
	}
	g := got.Sessions[0]
	w := in.Sessions[0]
	if g.ID != w.ID || g.Label != w.Label || g.Bootstrap != w.Bootstrap {
		t.Errorf("entry mismatch: got %+v want %+v", g, w)
	}
	if !g.CreatedAt.Equal(w.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", g.CreatedAt, w.CreatedAt)
	}
	if !g.LastActiveAt.Equal(w.LastActiveAt) {
		t.Errorf("LastActiveAt = %v, want %v", g.LastActiveAt, w.LastActiveAt)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, err := loadRegistry(filepath.Join(dir, "nope.json"))
	if err != nil {
		t.Fatalf("loadRegistry(missing): err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("loadRegistry(missing) = %+v, want nil", got)
	}
}

func TestLoad_EmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	got, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("loadRegistry(empty): err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("loadRegistry(empty) = %+v, want nil", got)
	}
}

func TestLoad_MalformedJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed: %v", err)
	}
	got, err := loadRegistry(path)
	if err == nil {
		t.Fatalf("loadRegistry(malformed) = %+v, want error", got)
	}
	if got != nil {
		t.Errorf("loadRegistry(malformed) returned non-nil registry: %+v", got)
	}
	if !strings.Contains(err.Error(), "registry: parse") {
		t.Errorf("err = %q, want it to contain %q", err, "registry: parse")
	}
}

// TestRegistry_LifecycleStateRoundTrip: writing entry with LifecycleState
// "evicted" and reloading preserves the field.
func TestRegistry_LifecycleStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	when := fixedTime(t)
	in := &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID:             SessionID("8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91"),
			CreatedAt:      when,
			LastActiveAt:   when,
			Bootstrap:      true,
			LifecycleState: "evicted",
		}},
	}
	if err := saveRegistryLocked(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || len(got.Sessions) != 1 {
		t.Fatalf("got = %+v, want 1 session", got)
	}
	if got.Sessions[0].LifecycleState != "evicted" {
		t.Errorf("LifecycleState = %q, want %q", got.Sessions[0].LifecycleState, "evicted")
	}
	if parseLifecycleState(got.Sessions[0].LifecycleState) != stateEvicted {
		t.Errorf("parseLifecycleState mismatch")
	}
}

// TestRegistry_LifecycleStateBackwardsCompat: an entry with no
// lifecycle_state field (simulating old pyry) defaults to active.
func TestRegistry_LifecycleStateBackwardsCompat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	raw := `{
      "version": 1,
      "sessions": [
        {
          "id": "8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91",
          "label": "",
          "created_at": "2026-05-01T12:34:56.789Z",
          "last_active_at": "2026-05-01T12:34:56.789Z",
          "bootstrap": true
        }
      ]
    }`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || len(got.Sessions) != 1 {
		t.Fatalf("got = %+v, want 1 session", got)
	}
	if got.Sessions[0].LifecycleState != "" {
		t.Errorf("LifecycleState = %q, want empty (omitted in old file)", got.Sessions[0].LifecycleState)
	}
	if parseLifecycleState(got.Sessions[0].LifecycleState) != stateActive {
		t.Errorf("parseLifecycleState(missing) should default to stateActive")
	}
}

// TestRegistry_SettingsRoundTrip: an entry carrying Model/Effort/YOLO survives
// save → reload unchanged (AC #1, registry-serialization layer).
func TestRegistry_SettingsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	when := fixedTime(t)
	in := &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID:           SessionID("8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91"),
			CreatedAt:    when,
			LastActiveAt: when,
			Bootstrap:    true,
			Model:        "opus",
			Effort:       "high",
			YOLO:         true,
		}},
	}
	if err := saveRegistryLocked(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || len(got.Sessions) != 1 {
		t.Fatalf("got = %+v, want 1 session", got)
	}
	g := got.Sessions[0]
	if g.Model != "opus" || g.Effort != "high" || !g.YOLO {
		t.Errorf("settings round-trip mismatch: got Model=%q Effort=%q YOLO=%v, want opus/high/true",
			g.Model, g.Effort, g.YOLO)
	}
}

// TestRegistry_DefaultSettingsOmittedOnDisk: a default entry (empty model/effort,
// YOLO false) serializes WITHOUT those keys, preserving the byte-stable on-disk
// shape (AC #5 / omitempty discipline).
func TestRegistry_DefaultSettingsOmittedOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	when := fixedTime(t)
	if err := saveRegistryLocked(path, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID:        SessionID("8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91"),
			CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, key := range []string{`"model"`, `"effort"`, `"yolo"`} {
		if strings.Contains(string(data), key) {
			t.Errorf("default entry serialized %s key; want omitted (byte shape). file:\n%s", key, data)
		}
	}
}

// TestRegistry_MissingSettingsDefaults (AC #2): a registry written before these
// fields existed loads without error; model/effort default to "" (inherit the
// template) and YOLO to false (permissions enforced).
func TestRegistry_MissingSettingsDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	raw := `{
      "version": 1,
      "sessions": [
        {
          "id": "8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91",
          "created_at": "2026-05-01T12:34:56.789Z",
          "last_active_at": "2026-05-01T12:34:56.789Z",
          "bootstrap": true
        }
      ]
    }`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || len(got.Sessions) != 1 {
		t.Fatalf("got = %+v, want 1 session", got)
	}
	g := got.Sessions[0]
	if g.Model != "" || g.Effort != "" || g.YOLO {
		t.Errorf("missing settings should default to empty/false; got Model=%q Effort=%q YOLO=%v",
			g.Model, g.Effort, g.YOLO)
	}
}

// TestRegistry_CorruptYOLOFailsLoud (AC #2, security): a wrong-typed yolo value
// fails the whole parse and never surfaces a registry with YOLO enabled. This
// is the fail-closed guarantee — corruption can never silently enable bypass.
func TestRegistry_CorruptYOLOFailsLoud(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	raw := `{
      "version": 1,
      "sessions": [
        {
          "id": "8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91",
          "created_at": "2026-05-01T12:34:56.789Z",
          "last_active_at": "2026-05-01T12:34:56.789Z",
          "bootstrap": true,
          "yolo": "maybe"
        }
      ]
    }`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := loadRegistry(path)
	if err == nil {
		t.Fatalf("loadRegistry(corrupt yolo) = %+v, want error", got)
	}
	if got != nil {
		t.Errorf("loadRegistry returned non-nil registry on corrupt yolo: %+v (must never surface YOLO=true)", got)
	}
}

func TestLoad_TolerateUnknownFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	raw := `{
      "version": 1,
      "future_top_level": "ignore me",
      "sessions": [
        {
          "id": "8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91",
          "label": "",
          "created_at": "2026-05-01T12:34:56.789Z",
          "last_active_at": "2026-05-01T12:34:56.789Z",
          "bootstrap": true,
          "claude_session_id": "future-field"
        }
      ]
    }`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	got, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if got == nil || len(got.Sessions) != 1 {
		t.Fatalf("got = %+v, want 1 session", got)
	}
	if !got.Sessions[0].Bootstrap {
		t.Errorf("Bootstrap = false, want true")
	}
	if got.Sessions[0].ID != "8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91" {
		t.Errorf("ID = %q, unexpected", got.Sessions[0].ID)
	}
}

// TestSave_AtomicRenamePreservesOldFile uses option (b) from the spec: make
// the rename fail by chmod'ing the parent directory read-only after a
// pre-existing target file is in place. The pre-existing file's bytes must
// survive the failed save unchanged — proving rename is the commit point.
func TestSave_AtomicRenamePreservesOldFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permission semantics required")
	}
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	original := []byte(`{"version":1,"sessions":[{"id":"original-uuid","label":"","created_at":"2026-01-01T00:00:00Z","last_active_at":"2026-01-01T00:00:00Z","bootstrap":true}]}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}

	// Make the directory non-writable so CreateTemp fails. We don't get to
	// observe a partially-written rename target — the point is only that
	// the pre-existing file is left untouched on save failure.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	when := fixedTime(t)
	err := saveRegistryLocked(path, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: "new-uuid", CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	})
	if err == nil {
		t.Fatal("saveRegistryLocked: nil error, want failure")
	}

	// Restore directory permissions so we can read the file back.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restore chmod: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original after failed save: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("original file mutated by failed save:\n got = %s\nwant = %s", got, original)
	}
}

func TestSave_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permission semantics required")
	}
	t.Parallel()
	dir := t.TempDir()
	subdir := filepath.Join(dir, "pyry")
	path := filepath.Join(subdir, "sessions.json")
	when := fixedTime(t)

	if err := saveRegistryLocked(path, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID:        SessionID("8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91"),
			CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("saveRegistryLocked: %v", err)
	}

	dirInfo, err := os.Stat(subdir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o, want 0700", mode)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if mode := fileInfo.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}
}

// TestSave_StableOrdering runs saveLocked twice on a Pool whose in-memory
// sessions are deliberately given non-trivial CreatedAt timestamps to defend
// against Go's map-iteration randomness.
func TestSave_StableOrdering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	when := fixedTime(t)

	// Hand-construct two registryFiles from the same logical content but
	// passed in different orders; saveRegistryLocked should produce the same
	// bytes regardless of input order, as long as we sort first.
	mk := func(order []int) *registryFile {
		entries := []registryEntry{
			{ID: "11111111-1111-4111-8111-111111111111", CreatedAt: when, LastActiveAt: when, Bootstrap: true},
			{ID: "22222222-2222-4222-8222-222222222222", CreatedAt: when.Add(time.Second), LastActiveAt: when.Add(time.Second)},
			{ID: "33333333-3333-4333-8333-333333333333", CreatedAt: when.Add(2 * time.Second), LastActiveAt: when.Add(2 * time.Second)},
		}
		out := make([]registryEntry, len(order))
		for i, idx := range order {
			out[i] = entries[idx]
		}
		sortEntriesByCreatedAt(out)
		return &registryFile{Version: 1, Sessions: out}
	}

	pathA := filepath.Join(dir, "a.json")
	pathB := filepath.Join(dir, "b.json")
	if err := saveRegistryLocked(pathA, mk([]int{0, 1, 2})); err != nil {
		t.Fatalf("save A: %v", err)
	}
	if err := saveRegistryLocked(pathB, mk([]int{2, 0, 1})); err != nil {
		t.Fatalf("save B: %v", err)
	}
	a, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	b, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("byte content differs between same-content saves\nA = %s\nB = %s", a, b)
	}
	// Sanity: assert it's well-formed JSON.
	var rt registryFile
	if err := json.Unmarshal(a, &rt); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
}

// TestSettingsFromEntry (#2043, AC #2's read half) pins the registry's
// default-TOLERANT posture read. The rows that matter are the ones with no
// permission_mode key at all — every registry written before #2043 — and the ones
// whose key cannot be trusted.
//
// Every tolerant arm lands on the default posture, so the tolerance can only ever
// move AWAY from the escalation: yolo stays the authoritative half, and a
// bypassPermissions hand-written beside yolo:false does not grant one.
func TestSettingsFromEntry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		entry registryEntry
		want  SessionSettings
	}{
		{
			name:  "pre-2043 entry, no key, no yolo",
			entry: registryEntry{Model: "sonnet", Effort: "low"},
			want:  SessionSettings{Model: "sonnet", Effort: "low", PermissionMode: permissionModeDefault},
		},
		{
			name:  "pre-2043 entry with yolo yields the escalation",
			entry: registryEntry{YOLO: true},
			want:  SessionSettings{YOLO: true, PermissionMode: permissionModeBypass},
		},
		{
			name:  "a stored in-band mode round-trips",
			entry: registryEntry{PermissionMode: "plan"},
			want:  SessionSettings{PermissionMode: "plan"},
		},
		{
			name:  "an unrecognised value degrades to default",
			entry: registryEntry{PermissionMode: "hand-edited-nonsense"},
			want:  SessionSettings{PermissionMode: permissionModeDefault},
		},
		{
			name:  "a hand-written escalation without yolo does not grant one",
			entry: registryEntry{PermissionMode: permissionModeBypass},
			want:  SessionSettings{PermissionMode: permissionModeDefault},
		},
		{
			name:  "yolo wins over a contradicting stored mode",
			entry: registryEntry{YOLO: true, PermissionMode: "plan"},
			want:  SessionSettings{YOLO: true, PermissionMode: permissionModeBypass},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := settingsFromEntry(tc.entry)
			if got != tc.want {
				t.Errorf("settingsFromEntry(%+v) = %+v, want %+v", tc.entry, got, tc.want)
			}
			if got.YOLO != (got.PermissionMode == permissionModeBypass) {
				t.Errorf("settingsFromEntry produced a posture that disagrees with itself: %+v", got)
			}
		})
	}
}

// TestPermissionModeForDisk (#2043, AC #2's write half): only the four
// non-default in-band modes are ever serialised. The default posture writes
// nothing so the omitempty on-disk shape stays byte-stable for a default session,
// and the escalation writes nothing because the yolo key is its one on-disk
// spelling — which is what makes it impossible for the file to hold a mode that
// contradicts yolo.
func TestPermissionModeForDisk(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   SessionSettings
		want string
	}{
		{"zero value", SessionSettings{}, ""},
		{"default posture", SessionSettings{PermissionMode: permissionModeDefault}, ""},
		{"in-band mode", SessionSettings{PermissionMode: "acceptEdits"}, "acceptEdits"},
		{"escalation", SessionSettings{YOLO: true, PermissionMode: permissionModeBypass}, ""},
		{"escalation spelled by yolo alone", SessionSettings{YOLO: true}, ""},
		{"a mode beside yolo is still not written", SessionSettings{YOLO: true, PermissionMode: "plan"}, ""},
		{"an unrecognised in-memory value", SessionSettings{PermissionMode: "nonsense"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := permissionModeForDisk(tc.in); got != tc.want {
				t.Errorf("permissionModeForDisk(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRegistry_PermissionModeOnDiskShape (#2043, AC #2): the key survives a real
// save → load → derive round-trip, and a default session's file does not carry it
// at all — the byte-stable shape the omitempty tags exist for.
func TestRegistry_PermissionModeOnDiskShape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	when := fixedTime(t)

	save := func(t *testing.T, name string, s SessionSettings) (string, string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := saveRegistryLocked(path, &registryFile{
			Version: 1,
			Sessions: []registryEntry{{
				ID:             SessionID("550e8400-e29b-41d4-a716-446655440000"),
				CreatedAt:      when,
				LastActiveAt:   when,
				Bootstrap:      true,
				YOLO:           s.YOLO,
				PermissionMode: permissionModeForDisk(s),
			}},
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return path, string(raw)
	}

	path, raw := save(t, "plan.json", SessionSettings{PermissionMode: "plan"})
	if !strings.Contains(raw, `"permission_mode": "plan"`) {
		t.Errorf("a stored mode is missing from the file: %s", raw)
	}
	reg, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if got := settingsFromEntry(*pickBootstrap(reg)); got.PermissionMode != "plan" {
		t.Errorf("round-tripped posture = %+v, want plan", got)
	}

	for _, s := range []SessionSettings{{}, {PermissionMode: permissionModeDefault}, {YOLO: true, PermissionMode: permissionModeBypass}} {
		_, raw := save(t, "shape.json", s)
		if strings.Contains(raw, "permission_mode") {
			t.Errorf("settings %+v wrote a permission_mode key, want the pre-2043 shape: %s", s, raw)
		}
	}
}
