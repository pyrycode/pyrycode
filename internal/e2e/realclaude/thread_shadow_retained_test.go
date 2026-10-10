//go:build !e2e_realclaude

package realclaude

import "testing"

// TestThreadShadowRetainedEvidence requires the committed observed pair, pinned
// to its counted live-gate report, and replays every checkpoint through a fresh
// store and fold. It needs no credential.
func TestThreadShadowRetainedEvidence(t *testing.T) {
	h, e, err := shadowReadPair("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if err := shadowValidatePair(h, e, true); err != nil {
		t.Fatal(err)
	}
	shadowReplay(t, h, e, false)
}
