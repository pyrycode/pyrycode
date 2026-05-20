package ptyrunner

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"

	"github.com/pyrycode/pyrycode/internal/agentrun/streamjson"
)

const defaultWatchdogTick = 1 * time.Second

// spinnerSecondsRe matches the class-A spinner rendering ("✻ Verb for Ns"
// or "✻ Verb for Nm Ks") and captures the minutes/seconds components.
// Class B ("✻ Channeling…") and class C ("✻ Actualizing… (2s · ↓N tokens)")
// do not match; for those renderings the watchdog falls back to the
// PTY-quiet arm only.
//
// Mirrors the spike-binary regex at
// github.com/pyrycode/tui-driver/cmd/spike-one-turn/main.go:58. Kept here
// rather than in tuidriver because the regex is consumer-policy (which
// spinner classes to parse) not driver-policy.
var spinnerSecondsRe = regexp.MustCompile(`✻\s+\S+(?:\s+\S+)?\s+for\s+(?:(\d+)m\s+)?(\d+)s`)

// parseSpinnerSeconds returns (totalSeconds, true) when stripped contains a
// class-A spinner; (0, false) otherwise.
func parseSpinnerSeconds(stripped []byte) (int, bool) {
	m := spinnerSecondsRe.FindSubmatch(stripped)
	if m == nil {
		return 0, false
	}
	var minutes int
	if len(m[1]) > 0 {
		minutes, _ = strconv.Atoi(string(m[1]))
	}
	seconds, _ := strconv.Atoi(string(m[2]))
	return minutes*60 + seconds, true
}

// runWatchdog drives the tui-driver two-arm watchdog at the cadence
// specified by tick (zero → defaultWatchdogTick). On each tick it
// snapshots the rolling buffer, parses any class-A spinner-seconds
// rendering, observes the tracker, and calls CheckWatchdog. On a non-nil
// CheckWatchdog it sets ExitReasonError on the emitter, cancels the run
// ctx, and returns. On ctx.Done it returns immediately.
//
// Discipline: the only thing logged is the tuidriver-generated watchdog
// error string (which carries last-state + duration but no Event content).
func runWatchdog(
	ctx context.Context,
	buf *tuidriver.Buffer,
	tr *tuidriver.Tracker,
	emitter *streamjson.Emitter,
	cancel context.CancelFunc,
	tick time.Duration,
	logger *slog.Logger,
) {
	if tick <= 0 {
		tick = defaultWatchdogTick
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := buf.Snapshot()
			stripped := tuidriver.StripANSI(snap)
			total, ok := parseSpinnerSeconds(stripped)
			// ObserveSpinner's visible argument requires BOTH the
			// thinking glyph AND a class-A seconds rendering — that
			// keeps the freeze arm from engaging on class B / C
			// spinners (no seconds counter to track progress against).
			tr.ObserveSpinner(ok && tuidriver.IsThinking(stripped), total)
			if werr := tr.CheckWatchdog(buf); werr != nil {
				logger.Warn("ptyrunner: watchdog fired", "err", werr)
				emitter.SetExitReason(streamjson.ExitReasonError)
				cancel()
				return
			}
		}
	}
}
