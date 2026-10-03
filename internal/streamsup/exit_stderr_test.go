package streamsup

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// exitRecordRunner builds a runner wired the way the daemon wires it (#2723):
// Config.Stderr nil, and a logger whose primary handler writes text to primary
// while control.SlogTee copies every record into ring. The primary handler logs
// at Debug, so a stray Debug record carrying stderr would also be caught.
func exitRecordRunner(t *testing.T, mode string, stdout, primary *safeBuffer, ring *control.RingBuffer) *Runner {
	t.Helper()
	cfg := helperRunCfg(t, mode, stdout, nil)
	cfg.Stderr = nil
	cfg.Logger = slog.New(control.SlogTee(
		slog.NewTextHandler(primary, &slog.HandlerOptions{Level: slog.LevelDebug}), ring))
	// One exit per test: the backoff outlasts it, and cancel ends the wait.
	cfg.BackoffInitial = time.Hour
	cfg.BackoffMax = time.Hour
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// exitLine returns the first line of out holding the claude exited record.
func exitLine(t *testing.T, out string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, `msg="claude exited"`) {
			return line
		}
	}
	t.Fatalf("no claude exited record in:\n%s", out)
	return ""
}

var quotedStderrAttr = regexp.MustCompile(`stderr=("(?:[^"\\]|\\.)*")`)

// TestRunner_NonZeroExitLogsSessionAndStderrTail is #2723's AC4: a child that
// writes more than the cap to stderr and exits 1 yields a claude exited record
// whose daemon log output names the session and carries the capped END of the
// stderr as a quoted attribute, while the tee'd ring records the exit and the
// session but holds no byte of the child's stderr.
func TestRunner_NonZeroExitLogsSessionAndStderrTail(t *testing.T) {
	t.Parallel()
	primary := &safeBuffer{}
	ring := control.NewRingBuffer(200)
	r := exitRecordRunner(t, "stderr_crash", &safeBuffer{}, primary, ring)
	cancel, join := runInBackground(t, r)
	waitForContains(t, primary, `msg="claude exited"`, 10*time.Second)
	cancel()
	_ = join()

	line := exitLine(t, primary.String())
	if !strings.Contains(line, "session="+testSessionID) {
		t.Errorf("exit record lacks the session id: %q", line)
	}
	m := quotedStderrAttr.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("exit record lacks a quoted stderr attribute: %q", line)
	}
	tail, err := strconv.Unquote(m[1])
	if err != nil {
		t.Fatalf("stderr attribute %s does not unquote: %v", m[1], err)
	}
	lines := helperStderrLines()
	written := strings.Join(lines, "\n")
	if !strings.HasSuffix(written, tail) {
		t.Errorf("tail is not the end of what the child wrote: %q", tail)
	}
	if len(tail) > stderrTailBytes {
		t.Errorf("tail is %d bytes, want <= %d", len(tail), stderrTailBytes)
	}
	if n := strings.Count(tail, "\n") + 1; n > stderrTailLines {
		t.Errorf("tail has %d lines, want <= %d", n, stderrTailLines)
	}
	if !strings.HasSuffix(tail, lines[len(lines)-1]) {
		t.Errorf("tail lost the last line the child wrote: %q", tail)
	}
	if strings.Contains(tail, lines[0]) {
		t.Errorf("tail kept the start instead of the end: %q", tail)
	}

	snap := ring.Snapshot()
	ringExit := exitLine(t, strings.Join(snap, "\n"))
	if !strings.Contains(ringExit, "session="+testSessionID) {
		t.Errorf("ring's exit record lacks the session id: %q", ringExit)
	}
	for _, l := range snap {
		if strings.Contains(l, helperStderrMarker) || strings.Contains(l, "eeeeeeee") {
			t.Errorf("ring holds child stderr: %q", l)
		}
	}
}

// TestRunner_NoStderrTailOnCleanOrDeliberateExit is #2723's AC2: a clean exit, a
// Restart, a RestartFresh and a daemon shutdown log no stderr tail, although the
// child wrote stderr each time — and the restart children exit NON-ZERO on their
// SIGTERM, so the iteration-ctx check is what withholds the tail, not the status.
func TestRunner_NoStderrTailOnCleanOrDeliberateExit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode string
		// act runs once the child has written its stderr; it returns whether a
		// claude exited record is expected (shutdown skips it).
		act func(r *Runner) bool
	}{
		{"clean exit", "stderr_exit0", func(*Runner) bool { return true }},
		{"restart", "stderr_block", func(r *Runner) bool { r.Restart(nil); return true }},
		{"restart fresh", "stderr_block", func(r *Runner) bool {
			r.RestartFresh("22222222-3333-4444-5555-666666666666")
			return true
		}},
		{"shutdown", "stderr_block", func(*Runner) bool { return false }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout, primary := &safeBuffer{}, &safeBuffer{}
			ring := control.NewRingBuffer(200)
			r := exitRecordRunner(t, tc.mode, stdout, primary, ring)
			cancel, join := runInBackground(t, r)
			if tc.mode == "stderr_block" {
				waitForContains(t, stdout, "READY", 10*time.Second)
			}
			wantExit := tc.act(r)
			if wantExit {
				waitForContains(t, primary, `msg="claude exited"`, 10*time.Second)
			}
			cancel()
			_ = join()

			out := primary.String()
			if wantExit {
				if line := exitLine(t, out); !strings.Contains(line, "session="+testSessionID) {
					t.Errorf("exit record lacks the spawn's session id: %q", line)
				}
			} else if strings.Contains(out, `msg="claude exited"`) {
				t.Errorf("shutdown logged a claude exited record:\n%s", out)
			}
			if strings.Contains(out, "stderr=") || strings.Contains(out, helperStderrMarker) {
				t.Errorf("daemon log carries a stderr tail:\n%s", out)
			}
		})
	}
}
