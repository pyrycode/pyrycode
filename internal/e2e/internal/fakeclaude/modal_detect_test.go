package main

import (
	"testing"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// TestFakeClaude_ModalScreenClassifiesAsPermission is the mandated fast de-risk
// assertion for the #791 permission-modal fixture. It renders the modalScreen
// const through the same tui-driver grid detector the live daemon uses and
// asserts it classifies as ModalClassPermission. This runs in milliseconds and
// with no harness, so a mis-detecting fixture (wrong anchor, anchor scrolled out
// of the bottom permission region, or a higher-priority anchor accidentally
// present) is caught here rather than inside a slow live-daemon e2e run. It is
// intentionally UNTAGGED (no //go:build e2e) so `go test ./internal/e2e/internal/
// fakeclaude/...` exercises it without the e2e build tag.
func TestFakeClaude_ModalScreenClassifiesAsPermission(t *testing.T) {
	got := tuidriver.DetectModalClass([]byte(modalScreen))
	if got != tuidriver.ModalClassPermission {
		t.Fatalf("DetectModalClass(modalScreen) = %q, want %q; the fixture must classify as a permission modal or the #791 e2e cannot surface modal_shown.\nmodalScreen rendered:\n%s",
			got, tuidriver.ModalClassPermission, tuidriver.Render([]byte(modalScreen), 0, 0))
	}
}

// TestFakeClaude_ModalClearScreenReturnsToNonPermission is the mandated fast
// de-risk for the #793 clear-on-answer fixture. Feeding modalScreen+modalClearScreen
// to a fresh DetectModalClass reproduces the daemon's sequential-write vt10x state
// deterministically, so it predicts the live Permission->Unknown transition the
// local answer keystroke must cause — in milliseconds, with no harness. A
// clear-screen that still classifies as Permission (e.g. too few newlines, so the
// "Do you want to proceed?" anchor stays inside the bottom permissionRegionRows
// window) is caught here rather than inside a slow live-daemon e2e run. Untagged
// (no //go:build e2e) so plain `go test ./.../fakeclaude/...` exercises it.
func TestFakeClaude_ModalClearScreenReturnsToNonPermission(t *testing.T) {
	combined := []byte(modalScreen + modalClearScreen)
	got := tuidriver.DetectModalClass(combined)
	if got == tuidriver.ModalClassPermission {
		t.Fatalf("DetectModalClass(modalScreen+modalClearScreen) = %q, want NOT %q; the clear screen must scroll the permission anchor out of the bottom-%d detection window or the #793 local answer never fires EventKindPtyModalHidden.\ncombined rendered:\n%s",
			got, tuidriver.ModalClassPermission, 12, tuidriver.Render(combined, 0, 0))
	}
}
