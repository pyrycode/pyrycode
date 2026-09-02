package sessions

import (
	"path/filepath"
	"testing"
)

// TestPool_DefaultSettings_KnownSettings (AC-2): a pool whose bootstrap carries
// known settings returns exactly those values, with the existence bool true.
func TestPool_DefaultSettings_KnownSettings(t *testing.T) {
	t.Parallel()
	// The posture is spelled out: a warm start derives it from the entry's yolo
	// (settingsFromEntry), so a bypass session reads back as the escalation named
	// rather than as an empty mode beside a true bit.
	want := SessionSettings{Model: "opus", Effort: "high", YOLO: true, PermissionMode: permissionModeBypass}
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolWithSettings(t, regPath, want)

	got, ok := pool.DefaultSettings()
	if !ok {
		t.Fatalf("DefaultSettings: existence bool = false, want true (a bootstrap exists)")
	}
	if got != want {
		t.Errorf("DefaultSettings: got %+v, want %+v", got, want)
	}
}

// TestPool_DefaultSettings_NoBootstrap pins the fallback contract the consumer
// relies on: with no bootstrap session to read from, the accessor returns the
// zero SessionSettings and false so the consumer can fall back to defaults. A
// zero-value Pool has a nil sessions map and an empty bootstrap id, so the
// lookup is a nil-map miss.
func TestPool_DefaultSettings_NoBootstrap(t *testing.T) {
	t.Parallel()
	p := &Pool{}
	got, ok := p.DefaultSettings()
	if ok {
		t.Errorf("DefaultSettings: existence bool = true, want false (no bootstrap)")
	}
	if got != (SessionSettings{}) {
		t.Errorf("DefaultSettings: got %+v, want zero value", got)
	}
}
