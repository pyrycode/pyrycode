package main

import (
	"bytes"
	"testing"
)

// TestContainsClearCommand pins the "/clear" discrimination the clear-rotate mode
// (envClearRotates) relies on: the "/clear" slash command must be detected once it
// has fully arrived, whether the default canonical discipline delivers it as a
// single "/clear\n" line or a raw discipline delivers it byte-by-byte across reads
// (the caller accumulates before matching). Plain input with no command must NOT
// match, so a stray keystroke can never fabricate a rotation. Intentionally
// UNTAGGED (no //go:build e2e), mirroring esc_detect_test.go / modal_detect_test.go,
// so the standard `go test` gate exercises the detector without the e2e build tag.
func TestContainsClearCommand(t *testing.T) {
	t.Parallel()

	// splitBytes reassembles the byte-by-byte accumulation the reader performs:
	// each element is one read, joined in order into the buffer the detector sees.
	splitClear := bytes.Join([][]byte{{'/'}, {'c'}, {'l'}, {'e'}, {'a'}, {'r'}, {'\r'}}, nil)

	tests := []struct {
		name string
		buf  []byte
		want bool
	}{
		{"empty", nil, false},
		{"plain ascii, no command", []byte("hello world"), false},
		{"full /clear line (canonical)", []byte("/clear\n"), true},
		{"/clear with carriage return", []byte("/clear\r"), true},
		{"/clear accumulated byte-by-byte (raw)", splitClear, true},
		{"leading slash only", []byte("/"), false},
		{"partial command", []byte("/cle"), false},
		{"/clear mid-buffer", []byte("draft/clear\r"), true},
		{"different slash command", []byte("/help\n"), false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := containsClearCommand(tc.buf); got != tc.want {
				t.Errorf("containsClearCommand(%q) = %v, want %v", tc.buf, got, tc.want)
			}
		})
	}
}
