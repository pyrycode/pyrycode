package main

import (
	"testing"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// TestFakeClaude_TrustScreenClassifiesAsTrustFolder is the mandated fast de-risk
// assertion for the #993 startup trust-folder fixture. It renders the trustScreen
// const through the same tui-driver grid detectors the live daemon uses and
// asserts BOTH the classification (DetectModalClass == ModalClassTrustFolder, the
// producer's Unknown->TrustFolder transition that surfaces modal_shown{trust}) AND
// the startup safety net (HasTrustModal == true, the Readiness.TrustModal that
// makes the supervisor hold the queued turn, #1013). One screen shape must drive
// both — that co-dependence is the whole point of the fixture. This runs in
// milliseconds with no harness, so a mis-detecting fixture (wrong header, missing
// pointer-marked option row per tui-driver #219, or a higher-priority anchor) is
// caught here rather than inside a slow live-daemon e2e run. Intentionally UNTAGGED (no
// //go:build e2e) so `go test ./internal/e2e/internal/fakeclaude/...` exercises it
// without the e2e build tag, mirroring modal_detect_test.go.
func TestFakeClaude_TrustScreenClassifiesAsTrustFolder(t *testing.T) {
	if got := tuidriver.DetectModalClass([]byte(trustScreen)); got != tuidriver.ModalClassTrustFolder {
		t.Fatalf("DetectModalClass(trustScreen) = %q, want %q; the fixture must classify as a trust-folder modal or the #993 e2e cannot surface modal_shown{trust}.\ntrustScreen rendered:\n%s",
			got, tuidriver.ModalClassTrustFolder, tuidriver.Render([]byte(trustScreen), 0, 0))
	}
	if !tuidriver.HasTrustModal([]byte(trustScreen)) {
		t.Fatalf("HasTrustModal(trustScreen) = false, want true; without it Readiness.TrustModal is never set and the supervisor never holds the queued turn (#1013).\ntrustScreen rendered:\n%s",
			tuidriver.Render([]byte(trustScreen), 0, 0))
	}
}

// TestFakeClaude_TrustClearScreenReturnsToNonTrust is the mandated fast de-risk
// for the #993 accept-clears fixture. Feeding trustScreen+trustClearScreen to a
// fresh detector reproduces the daemon's sequential-write vt10x state
// deterministically, so it predicts the live TrustFolder->Unknown transition the
// accept keystroke must cause — in milliseconds, with no harness. Unlike the
// permission clear (a bottom-12-window scroll), gridHasTrustDialog scans the WHOLE
// grid, so the trust header must scroll off the entire DefaultPtyRows screen; too
// few blank lines leaves it visible and the held turn never delivers. Both
// detectors must return NOT-trust (DetectModalClass and
// HasTrustModal), and the trailing idle glyph must keep IsIdle true so WaitReady
// returns clean rather than blocking. Untagged (no //go:build e2e) so plain
// `go test ./.../fakeclaude/...` exercises it.
func TestFakeClaude_TrustClearScreenReturnsToNonTrust(t *testing.T) {
	combined := []byte(trustScreen + trustClearScreen)
	if got := tuidriver.DetectModalClass(combined); got == tuidriver.ModalClassTrustFolder {
		t.Fatalf("DetectModalClass(trustScreen+trustClearScreen) = %q, want NOT %q; the clear must scroll the trust header off the whole grid or the accept never fires the TrustFolder->Unknown transition and the held turn never delivers.\ncombined rendered:\n%s",
			got, tuidriver.ModalClassTrustFolder, tuidriver.Render(combined, 0, 0))
	}
	if tuidriver.HasTrustModal(combined) {
		t.Fatalf("HasTrustModal(trustScreen+trustClearScreen) = true, want false; Readiness.TrustModal would stay set and WaitReady would keep holding the turn after the accept.\ncombined rendered:\n%s",
			tuidriver.Render(combined, 0, 0))
	}
	if !tuidriver.IsIdle(combined) {
		t.Fatalf("IsIdle(trustScreen+trustClearScreen) = false, want true; the trailing idle glyph must keep claude idle after the clear or WaitReady blocks instead of delivering the held turn.\ncombined rendered:\n%s",
			tuidriver.Render(combined, 0, 0))
	}
}
