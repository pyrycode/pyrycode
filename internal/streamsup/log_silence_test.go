package streamsup

import (
	"context"
	"log/slog"
	"testing"
)

// TestDefaultLogger_DiscardsEveryRecord pins the two halves of the property that
// keeps this package's test binary silent on its own stderr (#2470).
//
// `go test` discards a passing package's output but dumps the whole test-binary
// output when the package fails, so every record a passing test leaks competes
// with the failing sibling's "--- FAIL:" line for the last 4000 characters of
// the gate log — the window the dispatcher injects. At dbf97327 that leak was
// 25,671 bytes over 101 lines, 6.4x the window, and the test name behind a red
// internal/streamsup gate was unrecoverable.
//
// The leak's whole route is the `cfg.Logger == nil` fallback in New (and its two
// siblings in watchdog.go): a test that names no Logger gets slog.Default(), and
// the stdlib default writes to os.Stderr. TestMain re-points that default at a
// discarding handler, so this test asserts BOTH ends of that route — the default
// refuses every record, and the omitted-logger path is what reaches it.
func TestDefaultLogger_DiscardsEveryRecord(t *testing.T) {
	t.Parallel()

	// End 1: the installed default refuses every level. Deleting or weakening
	// the slog.SetDefault call in TestMain reddens this — the stdlib default is
	// enabled from Info up, which is where 96 of the 101 baseline records came
	// from. LevelError+4 covers a custom level above the named four.
	levels := []struct {
		name  string
		level slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"above error", slog.LevelError + 4},
	}
	for _, tc := range levels {
		t.Run(tc.name, func(t *testing.T) {
			if slog.Default().Enabled(context.Background(), tc.level) {
				t.Errorf("default logger is enabled at %s: a test that omits Config.Logger "+
					"will write records to the test binary's own stderr and bury the gate log's "+
					"--- FAIL: line; install a discarding handler in TestMain", tc.level)
			}
		})
	}

	// End 2: omitting Config.Logger really does resolve to that default. Without
	// this half the level table is a statement about slog rather than about this
	// package — re-pointing New's fallback at a fresh stderr handler would
	// restore all 101 records with the table above still green. helperRunCfg is
	// the shared builder every integration test here uses, and it sets no
	// Logger, so it is exactly the omitting caller the regression guards.
	t.Run("omitted config logger falls back to it", func(t *testing.T) {
		t.Parallel()
		r, err := New(helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{}))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if r.log != slog.Default() {
			t.Error("a Config with no Logger did not fall back to slog.Default(); " +
				"the discarding default installed in TestMain no longer covers this package's " +
				"lifecycle records")
		}
	})
}
