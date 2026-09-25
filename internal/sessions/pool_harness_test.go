package sessions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// harnessCapture records the harness every RunnerConfig carried into the factory,
// keyed by session id, and refuses any harness but claude the way the daemon's
// selecting factory does — so a test can see both what reached the seam and that
// a refusal leaves the pool as it was.
type harnessCapture struct {
	mu   sync.Mutex
	seen map[string]string
}

func (c *harnessCapture) factory(cfg RunnerConfig) (Runner, error) {
	c.mu.Lock()
	if c.seen == nil {
		c.seen = make(map[string]string)
	}
	c.seen[cfg.SessionID] = cfg.Harness
	c.mu.Unlock()
	if cfg.Harness != HarnessClaude {
		return nil, errors.New("no runner for harness " + cfg.Harness)
	}
	return fakeRunner{}, nil
}

func (c *harnessCapture) harnessOf(id SessionID) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.seen[string(id)]
	return h, ok
}

// forceSave writes the registry the way every mutation does, so a test can
// compare what a load-then-save produces with what was loaded.
func forceSave(t *testing.T, p *Pool) {
	t.Helper()
	p.mu.Lock()
	err := p.saveLocked()
	p.mu.Unlock()
	if err != nil {
		t.Fatalf("saveLocked: %v", err)
	}
}

// TestPool_Harness_LegacyRegistryRoundTripsByteIdentical: a sessions.json written
// before the harness key existed loads and saves back byte-identical, and its
// bootstrap is built on the claude harness (AC 1).
func TestPool_Harness_LegacyRegistryRoundTripsByteIdentical(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	dormant := helperDormantID(t)
	var cap harnessCapture
	pool, boot := helperPoolInjectedFactory(t, regPath, t.TempDir(), cap.factory,
		registryEntry{ID: dormant, Label: "conv-1", Model: "opus"},
	)
	before, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if bytes.Contains(before, []byte(`"harness"`)) {
		t.Fatalf("pre-written registry already carries a harness key, so this test proves nothing:\n%s", before)
	}

	forceSave(t, pool)

	after, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("registry changed across load+save:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if h, ok := cap.harnessOf(boot); !ok || h != HarnessClaude {
		t.Fatalf("bootstrap RunnerConfig.Harness = %q (seen %v), want %q", h, ok, HarnessClaude)
	}
}

// TestPool_Harness_DormantNonClaudeSurvivesLoadAndSave: a dormant entry naming
// another harness keeps it through a load and a save (AC 2).
func TestPool_Harness_DormantNonClaudeSurvivesLoadAndSave(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	target := helperDormantID(t)
	var cap harnessCapture
	pool, _ := helperPoolInjectedFactory(t, regPath, t.TempDir(), cap.factory,
		registryEntry{ID: target, Label: "conv-1", Harness: "codex"},
	)

	forceSave(t, pool)

	if got := harnessOnDisk(t, regPath, target); got != "codex" {
		t.Fatalf("harness on disk after save = %q, want %q", got, "codex")
	}
}

// TestPool_Harness_ReviveNonClaudeFailsAndStaysDormant: reviving a dormant entry
// whose harness has no runner hands that harness to the factory, returns its
// refusal, registers nothing, and leaves the entry dormant with its harness on
// disk and in memory (AC 3).
func TestPool_Harness_ReviveNonClaudeFailsAndStaysDormant(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	target := helperDormantID(t)
	var cap harnessCapture
	pool, _ := helperPoolInjectedFactory(t, regPath, t.TempDir(), cap.factory,
		registryEntry{ID: target, Label: "conv-1", Harness: "codex"},
	)

	if _, err := pool.Revive(target, "conv-1", ""); err == nil {
		t.Fatal("Revive of a codex entry succeeded, want the factory's refusal")
	}
	if h, ok := cap.harnessOf(target); !ok || h != "codex" {
		t.Fatalf("factory saw harness %q (seen %v) for the revived id, want %q", h, ok, "codex")
	}

	pool.mu.RLock()
	_, live := pool.sessions[target]
	entry, dormant := pool.dormant[target]
	pool.mu.RUnlock()
	if live {
		t.Fatal("the refused session was registered")
	}
	if !dormant || entry.Harness != "codex" {
		t.Fatalf("dormant entry = %+v (present %v), want it kept with harness %q", entry, dormant, "codex")
	}
	if got := harnessOnDisk(t, regPath, target); got != "codex" {
		t.Fatalf("harness on disk after the failed revive = %q, want %q", got, "codex")
	}
}

// TestPool_Harness_ReviveLegacyEntryIsClaude: a dormant entry with no harness key
// revives onto the claude harness (AC 1's "empty means claude").
func TestPool_Harness_ReviveLegacyEntryIsClaude(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	target := helperDormantID(t)
	var cap harnessCapture
	pool, _ := helperPoolInjectedFactory(t, regPath, t.TempDir(), cap.factory,
		registryEntry{ID: target, Label: "conv-1"},
	)
	// Revive needs a running pool to register; the harness reaches the factory
	// before that check, so ErrPoolNotRunning is the expected tail here.
	if _, err := pool.Revive(target, "conv-1", ""); err != nil && !errors.Is(err, ErrPoolNotRunning) {
		t.Fatalf("Revive: %v", err)
	}
	if h, ok := cap.harnessOf(target); !ok || h != HarnessClaude {
		t.Fatalf("factory saw harness %q (seen %v), want %q", h, ok, HarnessClaude)
	}
}

// TestPool_Harness_MintedSessionIsClaude: the funnel Create, CreateIn and Mint
// build through hands the factory the claude harness, and the session saves it
// back as no key at all.
func TestPool_Harness_MintedSessionIsClaude(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	var cap harnessCapture
	pool, _ := helperPoolInjectedFactory(t, regPath, t.TempDir(), cap.factory)
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	sess, err := pool.buildSession(id, "conv-1", "", SessionSettings{})
	if err != nil {
		t.Fatalf("buildSession: %v", err)
	}
	if h, ok := cap.harnessOf(id); !ok || h != HarnessClaude {
		t.Fatalf("factory saw harness %q (seen %v), want %q", h, ok, HarnessClaude)
	}
	if sess.harness != HarnessClaude {
		t.Fatalf("Session.harness = %q, want %q", sess.harness, HarnessClaude)
	}
}

// harnessOnDisk returns the harness key of id's entry in the registry at path,
// failing the test if the entry is missing.
func harnessOnDisk(t *testing.T, path string, id SessionID) string {
	t.Helper()
	reg, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if reg != nil {
		for _, e := range reg.Sessions {
			if e.ID == id {
				return e.Harness
			}
		}
	}
	t.Fatalf("entry %s missing from %s", id, path)
	return ""
}
