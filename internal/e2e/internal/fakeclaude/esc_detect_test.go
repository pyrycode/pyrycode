package main

import (
	"bytes"
	"testing"
)

// pasteOpen / pasteClose are the exact byte sequences tui-driver writes to wrap
// a delivered prompt (ESC[200~ … ESC[201~). Their leading 0x1b is always
// followed by '[' (0x5b), so neither is a bare ESC.
var (
	pasteOpen  = []byte{0x1b, '[', '2', '0', '0', '~'}
	pasteClose = []byte{0x1b, '[', '2', '0', '1', '~'}
)

// TestContainsBareESC pins the bare-ESC discrimination the Esc-ends-turn mode
// (envEscEndsTurn) relies on: the interrupt's lone 0x1b must be detected, and the
// bracketed-paste markers that wrap a delivered prompt (0x1b '[' …) must NOT be —
// otherwise the interrupt-live e2e (#794) could fire on a paste marker and the
// turn_end would no longer prove the Esc was received. Intentionally UNTAGGED (no
// //go:build e2e), mirroring modal_detect_test.go, so the standard `go test`
// gate exercises the detector without the e2e build tag.
func TestContainsBareESC(t *testing.T) {
	t.Parallel()

	// fullPaste is a complete bracketed paste of a short plain-ASCII prompt, as a
	// single read would carry it — no interrupt, so no bare ESC.
	fullPaste := bytes.Join([][]byte{pasteOpen, []byte("a short prompt"), pasteClose, {'\r'}}, nil)
	// pasteThenInterrupt is a paste immediately followed by the interrupt's lone
	// 0x1b in the same read — the trailing ESC is bare.
	pasteThenInterrupt := append(bytes.Clone(fullPaste), 0x1b)

	tests := []struct {
		name string
		buf  []byte
		want bool
	}{
		{"empty", nil, false},
		{"plain ascii, no esc", []byte("hello world"), false},
		{"lone esc (interrupt)", []byte{0x1b}, true},
		{"paste open marker only", pasteOpen, false},
		{"paste close marker only", pasteClose, false},
		{"full bracketed paste, no interrupt", fullPaste, false},
		{"esc as last byte after ascii", []byte{'h', 'i', 0x1b}, true},
		{"esc mid-buffer not followed by '['", []byte{'a', 0x1b, 'b'}, true},
		{"esc followed by '[' mid-buffer (CSI)", []byte{'a', 0x1b, '[', 'A'}, false},
		{"paste then interrupt in one read", pasteThenInterrupt, true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := containsBareESC(tc.buf); got != tc.want {
				t.Errorf("containsBareESC(%v) = %v, want %v", tc.buf, got, tc.want)
			}
		})
	}
}
