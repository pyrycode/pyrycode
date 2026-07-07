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
