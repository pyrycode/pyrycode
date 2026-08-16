//go:build e2e_realclaude

package realclaude

// Shared harness for the real-claude interactive suite: driving one stream-json
// line through the parser.
//
// Transcribed from ptyrunner_byte_equivalence_test.go, which #1348 deleted on
// 2026-08-16 because it compared the deleted terminal runner against the
// surviving one. That comparison is meaningless now and the file is gone for
// good. This helper stays because the dropped-line capture tests call it, and it
// never touched the deleted runner: it drives the stream-json parser directly.
// See harness_daemon_test.go for the full account of why these files exist.

import (
	"testing"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// parseOne writes one stream-json line (plus the trailing newline Parser.Write
// needs to consider it complete) to a fresh streamsup.Parser and returns the
// events it emitted. nil logger → slog.Default; the parser Debug-logs only.
func parseOne(t *testing.T, line string) []turnevent.Event {
	t.Helper()
	var got []turnevent.Event
	p := streamsup.NewParser(func(e turnevent.Event) { got = append(got, e) }, nil)
	if _, err := p.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("parser.Write(%s): %v", line, err)
	}
	return got
}
