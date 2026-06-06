package streamjson

import (
	"bytes"
	"strings"
	"testing"
)

// TestEmit_LargeRawLinePassthrough proves the stdout-forwarding path copies a
// single line far larger than the 64 KiB stdlib bufio.Scanner default
// byte-for-byte. The emitter re-emits entry.RawLine verbatim (see Emit), and
// pyry's jsonl reader caps a line at maxLineBytes = 16 MiB — an explicit guard
// that returns ErrLineTooLarge, never a silent truncation — so a large
// tool_result line must survive intact on stdout.
//
// This is the deterministic replacement for the deleted realclaude
// TestRealClaude_LargeToolOutput_ExceedsDefaultScannerBuffer. That test tried
// to drive a >70 KiB tool_result block through a live claude session, which
// can never happen: claude's Bash tool offloads any large command output to a
// file on disk and places only a short notice plus a ~2 KiB preview into the
// transcript (measured at ~2 KiB for an 80 KiB command output on claude
// 2.1.158, 2026-06-06). A >64 KiB line therefore never reaches pyry's stdin in
// the first place, so the live test's 70 KiB threshold was structurally
// unreachable on every claude version, not a tunable version-drift number. The
// forwarding path it meant to guard is unit-testable here with no model in the
// loop.
func TestEmit_LargeRawLinePassthrough(t *testing.T) {
	em, buf := newTestEmitter(t)
	initLen := buf.Len() // skip past the leading system/init envelope

	const size = 100 * 1024 // 100 KiB: comfortably past the 64 KiB scanner default
	payload := strings.Repeat("A", size)
	raw := `{"type":"user","message":{"content":[{"type":"tool_result","content":"` + payload + `"}]}}`

	if err := em.Emit(entry("user", raw)); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	got := buf.Bytes()[initLen:]
	want := append([]byte(raw), '\n')
	if !bytes.Equal(got, want) {
		t.Fatalf("large line not passed through verbatim: got %d bytes, want %d bytes",
			len(got), len(want))
	}
}
