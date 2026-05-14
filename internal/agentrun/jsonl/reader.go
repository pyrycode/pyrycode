// Package jsonl parses claude session JSONL output into structured events,
// surfacing every line (assistant, user, tool_use, tool_result, system,
// attachment, or unrecognised) with its verbatim bytes, alongside a
// deterministic end-of-turn signal on assistant entries.
//
// MUST NOT log file contents at any layer. Claude session JSONL may contain
// user prompts, file contents, or other operator-supplied material; this
// package logs only offsets and error messages, never the line bytes.
//
// Discovered by the Phase A spike (#329) and verified across 1151 real
// pyrycode sessions: the deterministic end-of-turn rule is an assistant
// entry with message.stop_reason == "end_turn" AND the sum of
// len(content[i].text) > 0. Empty-content end_turn entries are transitional
// thinking-block resolutions; they count as assistant entries but do NOT
// fire the end-of-turn signal.
package jsonl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// maxLineBytes caps any single buffered partial line. Real claude lines
// max out at ~80 KiB in observed fixtures; 16 MiB is well above that and
// bounds memory if a writer is pathologically broken.
const maxLineBytes = 16 << 20

// initialBufCap is the starting capacity of the partial-line buffer.
const initialBufCap = 8192

// ErrLineTooLarge is returned when a single line without a trailing '\n'
// exceeds maxLineBytes buffered bytes. Mirrors stdlib bufio.ErrTooLong.
// The stream is structurally broken at this point; the consumer should
// abort.
var ErrLineTooLarge = errors.New("jsonl: line exceeds maximum size")

// Event is the parsed shape of a single JSONL line. Every well-formed line
// the Reader consumes surfaces as an Event; malformed-JSON lines are still
// logged-and-skipped (see Reader.Next). Most fields apply only to assistant
// entries — see each field's contract.
type Event struct {
	// StopReason mirrors message.stop_reason verbatim ("end_turn",
	// "tool_use", "max_tokens", "stop_sequence", ""). Empty string when the
	// entry has no stop_reason field — a legitimate state for an assistant
	// entry mid-tool-call. Always "" on non-assistant entries.
	StopReason string

	// TextChars is sum(len(content[i].text)) over every content block on an
	// assistant entry. Content blocks without a "text" field (e.g.
	// "thinking", "tool_use") contribute 0 naturally. Always 0 on
	// non-assistant entries.
	TextChars int

	// EndOfTurn is true iff Kind == "assistant" AND StopReason == "end_turn"
	// AND TextChars > 0. This is the deterministic end-of-turn signal —
	// fire once per Event where this is true. Empty-content end_turn entries
	// (transitional thinking-block resolutions) have EndOfTurn == false even
	// though their StopReason is "end_turn". Always false on non-assistant
	// entries.
	EndOfTurn bool

	// Raw holds the verbatim line bytes the Reader consumed, with the
	// trailing '\n' stripped. A trailing '\r' (CRLF) is preserved. Backed
	// by a freshly-allocated slice, safe to retain past subsequent Next
	// calls. Typed as json.RawMessage so consumers can re-emit without
	// re-encoding.
	Raw json.RawMessage

	// Kind is the line's "type" field, whitelisted to one of "assistant",
	// "user", "tool_use", "tool_result", "system", "attachment", or "" for
	// any other value (including a missing field). Downstream re-emitters
	// can still pass unrecognised kinds through unchanged via Raw.
	Kind string

	// Usage is the per-entry token-usage block, populated only on assistant
	// entries that carry a "usage" object. nil on every other kind and on
	// assistant entries without a usage field.
	Usage *UsageBlock
}

// UsageBlock mirrors the assistant message.usage JSON object. Pointer-valued
// on Event to distinguish "field absent" from "field present with all zeros".
type UsageBlock struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// Config configures Reader. Logger is optional (defaults to slog.Default).
// StartOffset is informational: callers must Seek src to that position
// before constructing; the Reader uses StartOffset only to make Offset()
// report absolute file positions for resume.
type Config struct {
	Logger      *slog.Logger
	StartOffset int64
}

// Reader parses claude session JSONL from an io.Reader, surfacing one
// Event per call to Next for every well-formed line.
//
// Not safe for concurrent use. Construct one Reader per source.
type Reader struct {
	src     io.Reader
	log     *slog.Logger
	buf     []byte
	scratch [4096]byte

	offset         int64
	assistantCount int

	malformedSeen int
}

// NewReader returns a Reader that consumes src. Does not read from src
// until the first Next call.
func NewReader(src io.Reader, cfg Config) *Reader {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Reader{
		src:    src,
		log:    log,
		buf:    make([]byte, 0, initialBufCap),
		offset: cfg.StartOffset,
	}
}

// rawLine captures the type-only shape of a claude JSONL entry. The Message
// field stays raw so we don't pay the parse cost (or fail on the type
// mismatch) for non-assistant lines, which use entirely different content
// shapes (e.g. user lines have content as a string, not an array).
type rawLine struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`
}

// rawAssistantMessage is the assistant-line message shape we need. Decoded
// only when rawLine.Type == "assistant". Usage is pointer-typed so the JSON
// decoder leaves it nil when the field is absent.
type rawAssistantMessage struct {
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage *struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// knownKinds is the whitelist for Event.Kind classification. Any other value
// of the "type" field (including a missing field) maps to "".
var knownKinds = map[string]struct{}{
	"assistant":   {},
	"user":        {},
	"tool_use":    {},
	"tool_result": {},
	"system":      {},
	"attachment":  {},
}

// Next returns the next Event from src, advancing internal state.
//
// Every well-formed line surfaces as an Event — including non-assistant and
// unrecognised kinds. Only assistant entries populate StopReason, TextChars,
// EndOfTurn, and (optionally) Usage; non-assistant Events carry only Raw and
// Kind.
//
// Returns io.EOF when src has signalled io.EOF AND no complete line is
// pending in the internal buffer. Partial bytes (a line without a trailing
// '\n') are retained across calls; the next call continues from where the
// previous left off, optionally after the underlying io.Reader has produced
// more bytes (the typical fsnotify-driven case).
//
// Returns any non-EOF read error from src wrapped as
// "jsonl: read at offset %d: %w". Malformed-JSON lines are logged at Warn
// and skipped — they do NOT terminate iteration, do NOT surface as Events,
// and do NOT advance the assistant counter.
func (r *Reader) Next() (Event, error) {
	for {
		if i := bytes.IndexByte(r.buf, '\n'); i >= 0 {
			// Copy into a fresh slice: subsequent append(r.buf, ...) may
			// reuse the backing array and mutate bytes the caller still
			// holds via Event.Raw.
			lineCopy := make([]byte, i)
			copy(lineCopy, r.buf[:i])
			r.buf = r.buf[i+1:]
			r.offset += int64(i + 1)

			var raw rawLine
			if err := json.Unmarshal(lineCopy, &raw); err != nil {
				r.logMalformed(err)
				continue
			}
			kind := ""
			if _, ok := knownKinds[raw.Type]; ok {
				kind = raw.Type
			}
			if kind != "assistant" {
				return Event{
					Raw:  json.RawMessage(lineCopy),
					Kind: kind,
				}, nil
			}
			var msg rawAssistantMessage
			if len(raw.Message) > 0 {
				if err := json.Unmarshal(raw.Message, &msg); err != nil {
					r.logMalformed(err)
					continue
				}
			}
			r.assistantCount++
			textChars := 0
			for _, c := range msg.Content {
				textChars += len(c.Text)
			}
			var usage *UsageBlock
			if msg.Usage != nil {
				usage = &UsageBlock{
					InputTokens:              msg.Usage.InputTokens,
					OutputTokens:             msg.Usage.OutputTokens,
					CacheCreationInputTokens: msg.Usage.CacheCreationInputTokens,
					CacheReadInputTokens:     msg.Usage.CacheReadInputTokens,
				}
			}
			return Event{
				StopReason: msg.StopReason,
				TextChars:  textChars,
				EndOfTurn:  msg.StopReason == "end_turn" && textChars > 0,
				Raw:        json.RawMessage(lineCopy),
				Kind:       kind,
				Usage:      usage,
			}, nil
		}

		n, err := r.src.Read(r.scratch[:])
		if n > 0 {
			if len(r.buf)+n > maxLineBytes {
				return Event{}, ErrLineTooLarge
			}
			r.buf = append(r.buf, r.scratch[:n]...)
		}
		// EOF is NOT sticky: if the source is a growing *os.File, a later
		// Read may return more bytes. We surface io.EOF only when the
		// current Read produced nothing AND the source signalled EOF.
		if n == 0 && err == io.EOF {
			return Event{}, io.EOF
		}
		if err != nil && err != io.EOF {
			return Event{}, fmt.Errorf("jsonl: read at offset %d: %w", r.offset+int64(len(r.buf)), err)
		}
	}
}

// Offset returns the byte position of the next not-yet-consumed line —
// safe to persist as the resume point. Equals Config.StartOffset before
// the first Next call. After every successful Next (and after every
// silently-skipped malformed line), advances past the consumed line's
// trailing '\n'. Does NOT advance into a partial-line buffer.
func (r *Reader) Offset() int64 {
	return r.offset
}

// AssistantCount returns the number of assistant entries consumed so far,
// including transitional empty-content end_turn entries.
func (r *Reader) AssistantCount() int {
	return r.assistantCount
}

func (r *Reader) logMalformed(err error) {
	r.malformedSeen++
	// Rate-limit: log the first and every 100th occurrence thereafter.
	if r.malformedSeen == 1 || r.malformedSeen%100 == 0 {
		r.log.Warn("jsonl: skipping malformed line",
			slog.Int64("offset", r.offset),
			slog.Int("seen", r.malformedSeen),
			slog.String("err", err.Error()),
		)
	}
}
