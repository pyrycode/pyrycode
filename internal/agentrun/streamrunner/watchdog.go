package streamrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// idleStall is the default watchdog threshold: how long the stream may sit
// silent *while claude owes an assistant turn* before the run is judged
// wedged and claude is killed. Hardcoded per the package's no-timing-knobs
// stance (mirrors killGrace); Config.IdleTimeout overrides it for tests.
//
// Set conservatively (240s). In plain stream-json mode (--verbose, no
// --include-partial-messages) claude emits no stdout mid-turn, so a
// legitimately long assistant turn (extended thinking at xhigh/Opus) is
// indistinguishable from a wedged connection until the turn completes — the
// threshold must clear a realistic max turn. Type-awareness (awaitingAssistant)
// already excludes the big false-positive source: in-flight tool runs
// (Gradle / `go test -race`) happen *after* an assistant turn, when claude
// owes nothing, so their silence never trips the watchdog. This threshold
// covers only the residual awaiting-turn case. Future tightening: enable
// claude's --include-partial-messages to get a mid-turn "generating"
// heartbeat and lower this toward ~120s.
const idleStall = 240 * time.Second

// watchdogTick is the ceiling on the watchdog's poll interval. The actual
// tick is derived from the idle threshold (watchdogTickFor) so that a shrunk
// test threshold still gets a proportionally fine tick.
const watchdogTick = 5 * time.Second

// minWatchdogTick floors the derived tick so a tiny test threshold can't spin
// the poll loop into a busy-wait.
const minWatchdogTick = 5 * time.Millisecond

// defaultMaxParseBuf caps the partial-line accumulator. claude emits
// newline-delimited JSON, so the remainder between newlines is one in-flight
// line; if it grows past this without a newline (pathological), the partial is
// dropped rather than buffered unbounded. Passthrough is unaffected — bytes are
// forwarded in Write before the parser ever sees them. 4MiB clears any
// realistic single stream-json line. Carried as a per-parser field (maxBuf) so
// tests can shrink it without racing a shared global.
const defaultMaxParseBuf = 4 << 20

// watchdogTickFor derives the poll interval from the idle threshold: idle/8,
// clamped to [minWatchdogTick, watchdogTick]. For the 240s production default
// this is 5s; for a 200ms test threshold it is 25ms.
func watchdogTickFor(idle time.Duration) time.Duration {
	tick := idle / 8
	if tick > watchdogTick {
		tick = watchdogTick
	}
	if tick < minWatchdogTick {
		tick = minWatchdogTick
	}
	return tick
}

// streamParser wraps the caller's stdout writer. Every Write forwards the
// bytes verbatim to the wrapped writer FIRST (byte-for-byte passthrough is
// never delayed or altered by parsing), then feeds them to a minimal scanner
// that reads only the top-level `type` of each complete newline-delimited
// event line. It never inspects, retains, or logs event content — only the
// structural `type` field and the relative ordering of lines.
//
// Tracked state:
//   - lastEvent:  reset on every complete line (any activity from claude).
//   - awaiting:   true when claude owes the next assistant turn — initially
//     (the stdin user-turn has been written), and after each `user`/
//     `tool_result` line. Set false after an `assistant` line (claude then
//     runs a tool or completes; that silence is expected) and after a
//     `result` line (the run is done).
//   - sawResult:  true once claude emitted its own `result` trailer, so Run
//     won't synthesise a duplicate.
type streamParser struct {
	dst    io.Writer
	now    func() time.Time
	maxBuf int

	mu        sync.Mutex
	buf       []byte
	lastEvent time.Time
	awaiting  bool
	sawResult bool
}

// newStreamParser returns a parser wrapping dst. now is a clock seam for
// tests; nil falls back to time.Now. The parser starts in the awaiting state
// because Run writes the user-turn envelope to claude's stdin before the
// child produces any output — claude owes the first assistant turn.
func newStreamParser(dst io.Writer, now func() time.Time) *streamParser {
	if now == nil {
		now = time.Now
	}
	return &streamParser{
		dst:       dst,
		now:       now,
		maxBuf:    defaultMaxParseBuf,
		awaiting:  true,
		lastEvent: now(),
	}
}

// Write forwards p to the wrapped writer unchanged, then feeds the forwarded
// bytes to the scanner. The return value is exactly the wrapped writer's, so
// the io.Copy that os/exec drives stays correct.
func (p *streamParser) Write(b []byte) (int, error) {
	n, err := p.dst.Write(b)
	if n > 0 {
		p.feed(b[:n])
	}
	return n, err
}

// feed appends the bytes to the line accumulator and consumes every complete
// newline-delimited line. The partial remainder is kept for the next Write;
// if it grows past maxParseBuf without a newline it is dropped.
func (p *streamParser) feed(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.buf = append(p.buf, b...)
	rest := p.buf
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			break
		}
		p.consumeLine(rest[:i])
		rest = rest[i+1:]
	}
	if len(rest) > p.maxBuf {
		rest = nil
	}
	// Copy the remainder into a fresh slice so the (possibly large) backing
	// array of p.buf is released.
	p.buf = append([]byte(nil), rest...)
}

// consumeLine records the line as activity and, if it parses as a JSON object
// with a top-level `type`, updates the awaiting/sawResult state. Caller holds
// p.mu. Content beyond `type` is never decoded into retained state.
func (p *streamParser) consumeLine(line []byte) {
	// Every complete line is activity, even one that fails to parse.
	p.lastEvent = p.now()

	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var lt struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &lt); err != nil {
		return
	}
	switch lt.Type {
	case "assistant":
		// claude produced a turn; it now runs a tool or completes — silence
		// is expected, so stop awaiting.
		p.awaiting = false
	case "user", "tool_result":
		// a tool result came back; claude owes the next assistant turn.
		p.awaiting = true
	case "result":
		p.sawResult = true
		p.awaiting = false
	}
	// "system" (init, notices) and any other type: activity only.
}

// snapshot returns the current awaiting flag and last-activity time.
func (p *streamParser) snapshot() (awaiting bool, last time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.awaiting, p.lastEvent
}

// hasSeenResult reports whether claude emitted its own `result` trailer.
func (p *streamParser) hasSeenResult() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sawResult
}

// watchdog owns the idle-stall poll goroutine.
type watchdog struct {
	fired atomic.Bool
	done  chan struct{}
}

// startWatchdog launches the poll goroutine. It ticks at watchdogTickFor(idle)
// and fires when the parser reports awaiting && idle elapsed since the last
// line. On fire it records the firing, logs a Warn (no content), and cancels
// the supplied (child) context — reusing Run's cmd.Cancel/WaitDelay
// SIGTERM→SIGKILL path. The goroutine also exits cleanly when ctx is cancelled
// for any other reason (normal child exit, operator shutdown).
func startWatchdog(ctx context.Context, p *streamParser, idle time.Duration, cancel context.CancelFunc, logger *slog.Logger) *watchdog {
	wd := &watchdog{done: make(chan struct{})}
	tick := watchdogTickFor(idle)
	go func() {
		defer close(wd.done)
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				awaiting, last := p.snapshot()
				if awaiting && time.Since(last) > idle {
					wd.fired.Store(true)
					logger.Warn("streamrunner: idle stream stall — killing claude",
						"idle_seconds", int(idle.Seconds()))
					cancel()
					return
				}
			}
		}
	}()
	return wd
}

// wait blocks until the watchdog goroutine has exited.
func (w *watchdog) wait() { <-w.done }

// hasFired reports whether the watchdog killed the run.
func (w *watchdog) hasFired() bool { return w.fired.Load() }

// idleStallResult is the synthetic stream-json `result` trailer Run writes
// when the watchdog killed claude before it could emit its own. Field names,
// JSON tags, and order replicate the shape streamjson.Emitter produces (see
// internal/agentrun/streamjson/emitter.go's trailer) so the dispatcher's
// stream-json parser reads it identically — WITHOUT importing streamjson (the
// package's dependency rule forbids cross-importing agentrun subpackages).
type idleStallResult struct {
	Type           string         `json:"type"`
	Subtype        string         `json:"subtype"`
	IsError        bool           `json:"is_error"`
	DurationMS     int64          `json:"duration_ms"`
	NumTurns       int            `json:"num_turns"`
	Result         string         `json:"result"`
	StopReason     string         `json:"stop_reason"`
	SessionID      string         `json:"session_id"`
	TotalCostUSD   float64        `json:"total_cost_usd"`
	Usage          idleStallUsage `json:"usage"`
	TerminalReason string         `json:"terminal_reason"`
}

type idleStallUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// writeIdleStallResult composes and writes the synthetic `result` line. The
// `subtype`/`terminal_reason`/`is_error` triple is distinct and retryable: the
// dispatcher classifies `idle_stall` as a transient runner-side error and
// auto-retries (vs `max_turns`/`timeout`, which it deliberately never retries).
func writeIdleStallResult(w io.Writer, idle time.Duration, runStart time.Time) error {
	secs := int(idle.Seconds())
	tr := idleStallResult{
		Type:           "result",
		Subtype:        "error_idle_stall",
		IsError:        true,
		DurationMS:     time.Since(runStart).Milliseconds(),
		NumTurns:       0,
		Result:         fmt.Sprintf("idle_stall: no stream activity for %ds while awaiting assistant turn", secs),
		StopReason:     "",
		SessionID:      "",
		TotalCostUSD:   0,
		Usage:          idleStallUsage{},
		TerminalReason: "idle_stall",
	}
	buf, err := json.Marshal(&tr)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	_, err = w.Write(buf)
	return err
}
