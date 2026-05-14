// Package jsonl parses claude session JSONL output into structured
// assistant-entry events with a deterministic end-of-turn signal.
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

// Event is the parsed shape of a single assistant JSONL entry. Non-assistant
// entries are silently skipped by the Reader and never surface as Events.
type Event struct {
	// StopReason mirrors message.stop_reason verbatim ("end_turn",
	// "tool_use", "max_tokens", "stop_sequence", ""). Empty string when the
	// entry has no stop_reason field — a legitimate state for an assistant
	// entry mid-tool-call.
	StopReason string

	// TextChars is sum(len(content[i].text)) over every content block on the
	// entry. Content blocks without a "text" field (e.g. "thinking",
	// "tool_use") contribute 0 naturally.
	TextChars int

	// EndOfTurn is true iff StopReason == "end_turn" AND TextChars > 0.
	// This is the deterministic end-of-turn signal — fire once per Event
	// where this is true. Empty-content end_turn entries (transitional
	// thinking-block resolutions) have EndOfTurn == false even though their
	// StopReason is "end_turn".
	EndOfTurn bool
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
// assistant Event per call to Next.
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
// only when rawLine.Type == "assistant".
type rawAssistantMessage struct {
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// Next returns the next assistant Event from src, advancing internal state.
//
// Returns io.EOF when src has signalled io.EOF AND no complete line is
// pending in the internal buffer. Partial bytes (a line without a trailing
// '\n') are retained across calls; the next call continues from where the
// previous left off, optionally after the underlying io.Reader has produced
// more bytes (the typical fsnotify-driven case).
//
// Returns any non-EOF read error from src wrapped as
// "jsonl: read at offset %d: %w". Malformed-JSON lines are logged at Warn
// and skipped — they do NOT terminate iteration and do NOT advance the
// assistant counter.
func (r *Reader) Next() (Event, error) {
	for {
		if i := bytes.IndexByte(r.buf, '\n'); i >= 0 {
			line := r.buf[:i]
			r.buf = r.buf[i+1:]
			r.offset += int64(i + 1)

			var raw rawLine
			if err := json.Unmarshal(line, &raw); err != nil {
				r.logMalformed(err)
				continue
			}
			if raw.Type != "assistant" {
				continue
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
			return Event{
				StopReason: msg.StopReason,
				TextChars:  textChars,
				EndOfTurn:  msg.StopReason == "end_turn" && textChars > 0,
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
// silently-skipped non-assistant line), advances past the consumed line's
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
