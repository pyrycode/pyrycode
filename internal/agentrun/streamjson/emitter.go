// Package streamjson re-emits the parsed claude session-JSONL Event stream
// onto a writer (typically pyry's stdout) and composes a single
// `type:"result"` trailer line when the run terminates. Output shape mirrors
// `claude -p --output-format stream-json` so the dispatcher's stream-json
// parser keeps working unchanged after the agent-run migration.
//
// MUST NOT log Event content. The package logs only counts, durations, and
// error messages — never Event.Raw bytes nor per-event Usage values.
package streamjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
)

// ExitReason classifies how the run terminated. The internal value is
// translated into wire fields (`subtype` / `terminal_reason` / `is_error`)
// at trailer-composition time; see closeLocked.
type ExitReason string

const (
	// ExitReasonCompletion signals a clean, deterministic end-of-turn
	// observation from the watcher.
	ExitReasonCompletion ExitReason = "completion"

	// ExitReasonMaxTurns signals the budget Counter forcibly stopped the
	// run. Set via SetExitReason from #334's future Terminate hook.
	ExitReasonMaxTurns ExitReason = "max_turns"

	// ExitReasonError signals an unrecoverable failure — claude exited
	// non-zero, the watcher failed, or the run was torn down before
	// end-of-turn fired.
	ExitReasonError ExitReason = "error"
)

// Config configures Emitter.
type Config struct {
	// Writer is where stream-json lines are written. Required; production
	// passes os.Stdout.
	Writer io.Writer

	// SessionID is the UUIDv4 the caller minted and passed to claude via
	// --session-id. Stamped into the trailer's `session_id` field. Required.
	SessionID string

	// Now is a clock seam; defaults to time.Now. New captures Now() at
	// construction; Close calls Now() again to compute duration_ms.
	Now func() time.Time

	// Logger is optional and defaults to slog.Default().
	Logger *slog.Logger
}

// Emitter re-emits jsonl.Events to its Writer and composes a single
// `type:"result"` trailer line on Close. Safe for concurrent use across
// Emit, SetExitReason, and Close.
type Emitter struct {
	w         io.Writer
	sessionID string
	now       func() time.Time
	log       *slog.Logger
	start     time.Time

	mu             sync.Mutex
	numTurns       int
	endOfTurnSeen  bool
	lastStopReason string
	aggUsage       usageTotals
	exitReason     ExitReason
	writeErr       error
	closed         bool
	closeErr       error
}

type usageTotals struct {
	input            int
	output           int
	cacheCreationIn  int
	cacheReadIn      int
}

// New constructs an Emitter. Returns an error if Writer is nil or SessionID
// is empty.
func New(cfg Config) (*Emitter, error) {
	if cfg.Writer == nil {
		return nil, errors.New("streamjson: nil Writer")
	}
	if cfg.SessionID == "" {
		return nil, errors.New("streamjson: empty SessionID")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Emitter{
		w:         cfg.Writer,
		sessionID: cfg.SessionID,
		now:       now,
		log:       log,
		start:     now(),
	}, nil
}

// Emit re-emits ev.Raw verbatim followed by '\n', aggregates ev.Usage, and
// counts assistant entries. Safe for concurrent use.
//
// Once an underlying write fails, the error is sticky: subsequent Emit calls
// no-op (returning nil) so the watcher can keep parsing until end-of-turn or
// ctx cancel without thrashing on a broken pipe. Close still attempts to
// write the trailer (best-effort).
//
// Emit after Close is a no-op returning nil — late events from a slow
// watcher goroutine must not panic.
func (e *Emitter) Emit(ev jsonl.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed || e.writeErr != nil {
		return nil
	}

	// State updates first; even if the write fails, the totals stay
	// consistent so Close emits a coherent trailer.
	if ev.Kind == "assistant" {
		e.numTurns++
		e.lastStopReason = ev.StopReason
	}
	if ev.EndOfTurn {
		e.endOfTurnSeen = true
	}
	if ev.Usage != nil {
		e.aggUsage.input += ev.Usage.InputTokens
		e.aggUsage.output += ev.Usage.OutputTokens
		e.aggUsage.cacheCreationIn += ev.Usage.CacheCreationInputTokens
		e.aggUsage.cacheReadIn += ev.Usage.CacheReadInputTokens
	}

	line := append([]byte(nil), ev.Raw...)
	line = append(line, '\n')
	if _, err := e.w.Write(line); err != nil {
		e.writeErr = err
		return fmt.Errorf("streamjson: emit: %w", err)
	}
	return nil
}

// SetExitReason overrides the default exit-reason classification used by
// Close. Pluggable seam for the future budget integration (#334), which
// calls SetExitReason(ExitReasonMaxTurns) from its Terminate hook before
// SIGTERMing claude.
//
// Idempotent: only the first non-empty value sticks; later calls are no-ops.
// Safe for concurrent use.
func (e *Emitter) SetExitReason(r ExitReason) {
	if r == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.exitReason == "" {
		e.exitReason = r
	}
}

// Close writes the final `type:"result"` trailer line. MUST be called
// exactly once after Emit calls have stopped; calling Close while Emit is
// still firing is a race.
//
// Default exit-reason classification when SetExitReason was not called:
//   - end-of-turn was observed during Emit  → ExitReasonCompletion
//   - end-of-turn was NOT observed          → ExitReasonError
//
// Idempotent: second and subsequent calls return the first call's error
// verbatim without re-writing the trailer.
func (e *Emitter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return e.closeErr
	}
	e.closed = true

	exit := e.exitReason
	if exit == "" {
		if e.endOfTurnSeen {
			exit = ExitReasonCompletion
		} else {
			exit = ExitReasonError
		}
	}

	subtype, terminal, isErr := wireFields(exit)

	tr := trailer{
		Type:          "result",
		Subtype:       subtype,
		IsError:       isErr,
		DurationMS:    e.now().Sub(e.start).Milliseconds(),
		NumTurns:      e.numTurns,
		Result:        "",
		StopReason:    e.lastStopReason,
		SessionID:     e.sessionID,
		TotalCostUSD:  0,
		Usage: trailerUsage{
			InputTokens:              e.aggUsage.input,
			OutputTokens:             e.aggUsage.output,
			CacheCreationInputTokens: e.aggUsage.cacheCreationIn,
			CacheReadInputTokens:     e.aggUsage.cacheReadIn,
		},
		TerminalReason: terminal,
	}

	buf, err := json.Marshal(&tr)
	if err != nil {
		e.closeErr = fmt.Errorf("streamjson: marshal trailer: %w", err)
		return e.closeErr
	}
	buf = append(buf, '\n')
	if _, err := e.w.Write(buf); err != nil {
		e.closeErr = fmt.Errorf("streamjson: write trailer: %w", err)
		return e.closeErr
	}
	return nil
}

// wireFields maps the internal ExitReason to (subtype, terminal_reason,
// is_error) per the spec's termination table.
func wireFields(r ExitReason) (subtype, terminal string, isErr bool) {
	switch r {
	case ExitReasonCompletion:
		return "success", "completed", false
	case ExitReasonMaxTurns:
		return "error_max_turns", "max_turns", true
	default:
		return "error_during_execution", "", true
	}
}

// trailer is the on-the-wire shape of the `type:"result"` line. Field order
// here pins JSON key order so the captured-fixture byte-equivalence test
// diffs cleanly.
type trailer struct {
	Type           string       `json:"type"`
	Subtype        string       `json:"subtype"`
	IsError        bool         `json:"is_error"`
	DurationMS     int64        `json:"duration_ms"`
	NumTurns       int          `json:"num_turns"`
	Result         string       `json:"result"`
	StopReason     string       `json:"stop_reason"`
	SessionID      string       `json:"session_id"`
	TotalCostUSD   float64      `json:"total_cost_usd"`
	Usage          trailerUsage `json:"usage"`
	TerminalReason string       `json:"terminal_reason"`
}

type trailerUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}
