package sessions

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestPool_HarnessFor answers the agent a session runs for both halves of the
// pool (#2629): the live bootstrap, a dormant entry naming codex, and a dormant
// entry with no harness key, which is claude. An id in neither half, the empty id
// included, is a miss — HarnessFor has none of Lookup's bootstrap fallback.
func TestPool_HarnessFor(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	codex, bare := helperDormantID(t), helperDormantID(t)

	pool, boot := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: codex, Label: "conv-1", Harness: "codex"},
		registryEntry{ID: bare, Label: "conv-2"},
	)

	for _, tc := range []struct {
		name string
		id   SessionID
		want string
	}{
		{"live bootstrap", boot, HarnessClaude},
		{"dormant codex entry", codex, "codex"},
		{"dormant entry with no harness key", bare, HarnessClaude},
	} {
		got, err := pool.HarnessFor(tc.id)
		if err != nil {
			t.Errorf("%s: HarnessFor err = %v, want nil", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: HarnessFor = %q, want %q", tc.name, got, tc.want)
		}
	}

	for _, tc := range []struct {
		name string
		id   SessionID
	}{
		{"an id this pool never held", helperDormantID(t)},
		{"the empty id", ""},
	} {
		got, err := pool.HarnessFor(tc.id)
		if !errors.Is(err, ErrSessionNotFound) || got != "" {
			t.Errorf("%s: HarnessFor = (%q, %v), want (\"\", ErrSessionNotFound)", tc.name, got, err)
		}
	}
}
