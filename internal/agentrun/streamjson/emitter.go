// Package streamjson re-emits the parsed claude session-JSONL entry stream
// onto a writer (typically pyry's stdout) and composes a single
// `type:"result"` trailer line when the run terminates. Output shape mirrors
// `claude -p --output-format stream-json` so the dispatcher's stream-json
// parser keeps working unchanged after the agent-run migration.
//
// MUST NOT log entry content. The package logs only counts, durations, and
// error messages — never JSONLEntry.RawLine bytes nor per-entry usage values.
package streamjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
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
	// --session-id. Stamped into the leading init envelope's `session_id`
	// field and the trailer's `session_id` field. Required.
	SessionID string

	// Cwd is claude's working directory. Stamped into the leading init
	// envelope's `cwd` field. Required (non-empty).
	Cwd string

	// Tools is the human-readable tool allowlist stamped into the leading
	// init envelope's `tools` field. Required (non-nil); an empty slice is
	// accepted and marshals as `[]`. Runtime enforcement is performed by
	// the deny-default settings file ptyrunner writes; this list is the
	// wire-shape mirror of those names.
	Tools []string

	// Model is the model identifier stamped into the leading init
	// envelope's `model` field. Required (non-empty).
	Model string

	// Now is a clock seam; defaults to time.Now. New captures Now() at
	// construction; Close calls Now() again to compute duration_ms.
	Now func() time.Time

	// Logger is optional and defaults to slog.Default().
	Logger *slog.Logger
}

// Emitter re-emits tuidriver.JSONLEntry values to its Writer and composes a
// single `type:"result"` trailer line on Close. Safe for concurrent use
// across Emit, SetExitReason, and Close.
type Emitter struct {
	w         io.Writer
	sessionID string
	now       func() time.Time
	log       *slog.Logger
	start     time.Time

	mu                 sync.Mutex
	numTurns           int
	lastAssistantMsgID string // message.id of the previous assistant entry; "" = none seen yet
	endOfTurnSeen      bool
	lastStopReason     string
	lastAssistantText  string
	aggUsage           usageTotals
	exitReason         ExitReason
	writeErr           error
	closed             bool
	closeErr           error
}

type usageTotals struct {
	input           int
	output          int
	cacheCreationIn int
	cacheReadIn     int
}

// New constructs an Emitter and writes the leading
// `{"type":"system","subtype":"init",...}` envelope to cfg.Writer before
// returning. Returns an error if Writer is nil, SessionID is empty, Cwd is
// empty, Tools is nil, or Model is empty. On init-write failure the returned
// *Emitter is nil and the error wraps the underlying writer error.
func New(cfg Config) (*Emitter, error) {
	if cfg.Writer == nil {
		return nil, errors.New("streamjson: nil Writer")
	}
	if cfg.SessionID == "" {
		return nil, errors.New("streamjson: empty SessionID")
	}
	if cfg.Cwd == "" {
		return nil, errors.New("streamjson: empty Cwd")
	}
	if cfg.Tools == nil {
		return nil, errors.New("streamjson: nil Tools")
	}
	if cfg.Model == "" {
		return nil, errors.New("streamjson: empty Model")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	init := initLine{
		Type:      "system",
		Subtype:   "init",
		Cwd:       cfg.Cwd,
		Tools:     cfg.Tools,
		Model:     cfg.Model,
		SessionID: cfg.SessionID,
	}
	buf, err := json.Marshal(&init)
	if err != nil {
		return nil, fmt.Errorf("streamjson: marshal init: %w", err)
	}
	buf = append(buf, '\n')
	if _, err := cfg.Writer.Write(buf); err != nil {
		return nil, fmt.Errorf("streamjson: emit init: %w", err)
	}

	return &Emitter{
		w:         cfg.Writer,
		sessionID: cfg.SessionID,
		now:       now,
		log:       log,
		start:     now(),
	}, nil
}

// Emit re-emits entry.RawLine verbatim followed by '\n', aggregates the
// per-entry usage counters, and counts logical assistant turns (consecutive
// assistant entries grouped by message.id). Safe for concurrent use.
//
// Byte passthrough uses entry.RawLine (the verbatim source-line bytes
// captured by the tail goroutine). Re-marshalling from entry.Raw would
// normalise key order and whitespace, breaking the byte-equivalence contract
// the dispatcher relies on.
//
// Once an underlying write fails, the error is sticky: subsequent Emit calls
// no-op (returning nil) so the watcher can keep parsing until end-of-turn or
// ctx cancel without thrashing on a broken pipe. Close still attempts to
// write the trailer (best-effort).
//
// Emit after Close is a no-op returning nil — late entries from a slow
// watcher goroutine must not panic.
func (e *Emitter) Emit(entry tuidriver.JSONLEntry) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed || e.writeErr != nil {
		return nil
	}

	// State updates first; even if the write fails, the totals stay
	// consistent so Close emits a coherent trailer.
	if entry.Type == "assistant" {
		// Count logical turns, not raw assistant entries. claude serialises one
		// logical reply as multiple consecutive assistant entries sharing a
		// message.id (2.1.158 emits a thinking line then a text line for a
		// single reply); a new turn begins only when the message.id changes, so
		// the count matches claude's native num_turns. Empty id is ungroupable
		// (synthetic/malformed entries) → counted as its own turn, preserving
		// the pre-fix per-entry behaviour for id-less entries. Transition-
		// counting equals distinct-id-counting because claude completes one
		// assistant message before starting the next — no A,B,A interleaving
		// has been observed.
		id := ""
		if entry.Message != nil {
			id = entry.Message.ID
		}
		if id == "" || id != e.lastAssistantMsgID {
			e.numTurns++
		}
		e.lastAssistantMsgID = id
		if entry.Message != nil {
			e.lastStopReason = entry.Message.StopReason
		}
		// Capture the final assistant turn's text for the trailer's `result`
		// field. claude -p (and thus the streamrunner passthrough) populates
		// `result` with the last assistant message's text; the ptyrunner path
		// synthesises its own trailer in Close, so without this it shipped an
		// empty `result` — a parity gap that blanked the dispatcher's
		// completion-comment embed and the salvage-PR context tail (surfaced
		// 2026-05-29). Reuses the same AssistantText primitive that drives
		// end-of-turn detection above, so it's non-empty exactly when a turn
		// carried text. Last non-empty wins: intermediate "let me check…"
		// turns are overwritten by the end-of-turn summary, and textless
		// tool_use turns return "" and don't clobber the captured text.
		if txt := tuidriver.AssistantText(entry); txt != "" {
			e.lastAssistantText = txt
		}
	}
	if tuidriver.IsEndTurn(entry) {
		e.endOfTurnSeen = true
	}
	if u, ok := readUsage(entry); ok {
		e.aggUsage.input += u.InputTokens
		e.aggUsage.output += u.OutputTokens
		e.aggUsage.cacheCreationIn += u.CacheCreationInputTokens
		e.aggUsage.cacheReadIn += u.CacheReadInputTokens
	}

	line := append([]byte(nil), entry.RawLine...)
	line = append(line, '\n')
	if _, err := e.w.Write(line); err != nil {
		e.writeErr = err
		return fmt.Errorf("streamjson: emit: %w", err)
	}
	return nil
}

// usageBlock mirrors message.usage's four counter fields. Used only as
// readUsage's return type; internal aggregation state, not part of the API
// surface.
type usageBlock struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// readUsage extracts the four token-counter fields from entry.Message.Raw's
// "usage" object. Returns (zero, false) when the entry has no Message, no
// Message.Raw, no "usage" key, or a non-map value at "usage" — same
// observable behaviour as the old `ev.Usage == nil` gate.
//
// encoding/json decodes JSON numbers as float64 into map[string]any; this
// helper truncates to int, matching the wire shape's integer-valued
// counters.
func readUsage(entry tuidriver.JSONLEntry) (usageBlock, bool) {
	if entry.Message == nil || entry.Message.Raw == nil {
		return usageBlock{}, false
	}
	m, ok := entry.Message.Raw["usage"].(map[string]any)
	if !ok {
		return usageBlock{}, false
	}
	read := func(k string) int {
		v, _ := m[k].(float64)
		return int(v)
	}
	return usageBlock{
		InputTokens:              read("input_tokens"),
		OutputTokens:             read("output_tokens"),
		CacheCreationInputTokens: read("cache_creation_input_tokens"),
		CacheReadInputTokens:     read("cache_read_input_tokens"),
	}, true
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

// ExitReason reports the run's terminal classification. Before Close it
// returns whatever SetExitReason recorded (possibly ""); Close resolves and
// persists the final reason (defaulting to ExitReasonError when no end-of-turn
// was seen), so after Close this returns that resolved value. The ptyrunner
// flight-recorder reads it to tag a recording by the run's REAL outcome — Run
// returns a nil error even on a watchdog-fired wedge, so the Go return alone
// cannot distinguish a wedge from a clean finish.
func (e *Emitter) ExitReason() ExitReason {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.exitReason
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
	// Persist the resolved reason so ExitReason() reports the final
	// classification post-Close, not the possibly-empty pre-resolution value.
	e.exitReason = exit

	subtype, terminal, isErr := wireFields(exit)

	tr := trailer{
		Type:         "result",
		Subtype:      subtype,
		IsError:      isErr,
		DurationMS:   e.now().Sub(e.start).Milliseconds(),
		NumTurns:     e.numTurns,
		Result:       e.lastAssistantText,
		StopReason:   e.lastStopReason,
		SessionID:    e.sessionID,
		TotalCostUSD: 0,
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

// initLine is the on-the-wire shape of the leading `type:"system"
// subtype:"init"` envelope. Field order here pins JSON key order so the
// captured-fixture byte-equivalence test diffs cleanly. Reordering breaks
// the wire contract even though the resulting JSON is still semantically
// valid.
type initLine struct {
	Type      string   `json:"type"`
	Subtype   string   `json:"subtype"`
	Cwd       string   `json:"cwd"`
	Tools     []string `json:"tools"`
	Model     string   `json:"model"`
	SessionID string   `json:"session_id"`
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
