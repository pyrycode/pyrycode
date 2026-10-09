//go:build thread_shadow_evidence && !e2e_realclaude

package realclaude

import "testing"

// TestThreadShadowRetainedEvidence requires the observed pair and its counted
// dispatcher report. The thread_shadow_evidence tag selects this credential-free
// gate independently of the authenticated capture's e2e_realclaude tag.
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
