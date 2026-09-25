package sessions

import (
	"path/filepath"
	"testing"
)

// TestPool_MintAs_Codex (#2647): a session minted for another harness carries
// that harness live, on disk and — after a reload — on its dormant entry, and it
// starts with no model or effort even when the operator configured claude with
// both, so the agent runs at its own defaults.
func TestPool_MintAs_Codex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolMintBootstrap(t, regPath, t.TempDir(), SessionSettings{Model: "opus", Effort: "high"})
	runPoolInBackground(t, pool)

	id, err := pool.MintAs("conv-1", "", "codex")
	if err != nil {
		t.Fatalf("MintAs: %v", err)
	}
	if got, err := pool.HarnessFor(id); err != nil || got != "codex" {
		t.Errorf("HarnessFor = %q, %v; want codex", got, err)
	}
	settings, err := pool.SettingsFor(id)
	if err != nil {
		t.Fatalf("SettingsFor: %v", err)
	}
	if settings.Model != "" || settings.Effort != "" {
		t.Errorf("minted codex settings = model %q effort %q, want neither (Codex's own defaults)", settings.Model, settings.Effort)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	found := false
	for _, e := range reg.Sessions {
		if e.ID == id {
			found = true
			if e.Harness != "codex" {
				t.Errorf("persisted harness = %q, want codex", e.Harness)
			}
			if e.Model != "" || e.Effort != "" {
				t.Errorf("persisted model %q effort %q, want neither", e.Model, e.Effort)
			}
		}
	}
	if !found {
		t.Fatalf("minted id %q absent from %s", id, regPath)
	}

	// A second daemon lifetime over the same registry holds the entry dormant
	// and still answers codex for it.
	reloaded := helperPoolArgvRecorder(t, regPath, t.TempDir())
	if got, err := reloaded.HarnessFor(id); err != nil || got != "codex" {
		t.Errorf("after reload HarnessFor = %q, %v; want codex", got, err)
	}
}

// TestPool_MintAs_Claude (#2647): Mint's own harness through MintAs still
// inherits the operator's configured model and effort, as Mint always has.
func TestPool_MintAs_Claude(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolMintBootstrap(t, regPath, t.TempDir(), SessionSettings{Model: "opus", Effort: "high"})
	runPoolInBackground(t, pool)

	id, err := pool.MintAs("conv-1", "", HarnessClaude)
	if err != nil {
		t.Fatalf("MintAs: %v", err)
	}
	if got, err := pool.HarnessFor(id); err != nil || got != HarnessClaude {
		t.Errorf("HarnessFor = %q, %v; want claude", got, err)
	}
	settings, err := pool.SettingsFor(id)
	if err != nil {
		t.Fatalf("SettingsFor: %v", err)
	}
	if settings.Model != "opus" || settings.Effort != "high" {
		t.Errorf("minted claude settings = model %q effort %q, want opus/high", settings.Model, settings.Effort)
	}
}

// TestPool_MintWith_StoresGivenSettings (#2665): a mint's settings are the
// caller's, verbatim, from the start — a Codex session gets a model and effort
// of its own, persisted on its registry entry, with no second write.
func TestPool_MintWith_StoresGivenSettings(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolMintBootstrap(t, regPath, t.TempDir(), SessionSettings{Model: "opus", Effort: "high"})
	runPoolInBackground(t, pool)

	id, err := pool.MintWith("conv-1", "", "codex", SessionSettings{Model: "luna", Effort: "ultra"})
	if err != nil {
		t.Fatalf("MintWith: %v", err)
	}
	settings, err := pool.SettingsFor(id)
	if err != nil {
		t.Fatalf("SettingsFor: %v", err)
	}
	if settings.Model != "luna" || settings.Effort != "ultra" {
		t.Errorf("minted settings = model %q effort %q, want luna/ultra", settings.Model, settings.Effort)
	}
	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	for _, e := range reg.Sessions {
		if e.ID == id && (e.Model != "luna" || e.Effort != "ultra") {
			t.Errorf("persisted model %q effort %q, want luna/ultra", e.Model, e.Effort)
		}
	}
}

// TestPool_MintDefaults (#2665): what a mint gives each harness — the
// operator's model and effort for claude, never its posture, and nothing for
// any other harness.
func TestPool_MintDefaults(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperPoolMintBootstrap(t, regPath, t.TempDir(), SessionSettings{Model: "opus", Effort: "high", YOLO: true})

	if got := pool.MintDefaults(HarnessClaude); got != (SessionSettings{Model: "opus", Effort: "high"}) {
		t.Errorf("MintDefaults(claude) = %+v, want opus/high and no posture", got)
	}
	if got := pool.MintDefaults("codex"); got != (SessionSettings{}) {
		t.Errorf("MintDefaults(codex) = %+v, want zero", got)
	}
}
