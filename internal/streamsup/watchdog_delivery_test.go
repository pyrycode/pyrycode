package streamsup

import (
	"errors"
	"io"
	"testing"
	"time"
)

func TestWatchdog_TrackUserTurn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lines    string
		fail     bool
		awaiting bool
		last     int64
	}{
		{"silent", "", false, true, 2},
		{"assistant", "assistant", false, false, 1},
		{"result", "result", false, false, 1},
		{"user", "user", false, true, 1},
		{"tool_result", "tool_result", false, true, 1},
		{"system", "system", false, true, 2},
		{"failed", "", true, false, 0},
		{"exit", "", false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := &fakeClock{t: time.Unix(1_000, 0)}
			w := NewWatchdog(WatchdogConfig{now: clk.now, Logger: discardLogger()})
			writeErr := errors.New("write failed")
			err := w.TrackUserTurn(func() error {
				clk.advance(time.Second)
				if tc.lines != "" {
					if _, err := io.WriteString(w.Writer(), `{"type":"`+tc.lines+`"}`+"\n"); err != nil {
						t.Fatal(err)
					}
				}
				if tc.name == "exit" {
					w.ChildExited()
				}
				clk.advance(time.Second)
				if tc.fail {
					return writeErr
				}
				return nil
			})
			if (tc.fail && !errors.Is(err, writeErr)) || (!tc.fail && err != nil) {
				t.Fatalf("delivery error: %v", err)
			}
			awaiting, last := w.tracker.snapshot()
			if awaiting != tc.awaiting || !last.Equal(time.Unix(1_000+tc.last, 0)) {
				t.Fatalf("awaiting=%t last=%v; want %t at +%ds", awaiting, last, tc.awaiting, tc.last)
			}
		})
	}
}
