//go:build darwin

package rotation

import "testing"

// TestNoopProbe_AvailableFalse pins the optional-interface signal the bootstrap
// resolver reads: the no-lsof noop probe declares itself unusable so the resolver
// falls back to the newest-by-mtime path instead of trusting an always-empty
// probe. darwin-tagged because noopProbe exists only in the darwin build.
func TestNoopProbe_AvailableFalse(t *testing.T) {
	t.Parallel()
	if (noopProbe{}).Available() {
		t.Fatal("noopProbe.Available() = true, want false")
	}
}
