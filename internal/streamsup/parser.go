package streamsup

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// defaultMaxParseBuf caps the partial-line accumulator (streamrunner's value).
// claude emits newline-delimited JSON, so the remainder between newlines is one
// in-flight line; if it grows past this without a newline (pathological or
// hostile child), the partial is dropped rather than buffered unbounded. 4 MiB
// clears any realistic single stream-json line. Carried as a per-parser field
// (maxBuf) so a test can shrink it without racing a shared global.
const defaultMaxParseBuf = 4 << 20

// maxUnrecognizedRaw caps the raw JSON carried on a turnevent.Unrecognized.
// Applied at CONSTRUCTION, so an oversized payload never enters the event
// stream, the push queue, or any log — the cap is the only thing standing
// between a pathological line and the wire's size limit.
//
// The binding limit is the v2 application-envelope cap of 65519 bytes, NOT v1's
// 1 MiB, which v2 superseded (docs/protocol-mobile.md § Application-envelope
// size cap). 16 KiB is roughly a quarter of it, which leaves room for the
// envelope's other fields plus the JSON escaping this blob picks up on the way
// out. Escaping is mild in practice because the payload is already JSON text:
// its control characters arrive pre-escaped as printable pairs, so the growth is
// quotes and backslashes rather than a \u00XX expansion of every byte.
//
// It is also far more than a human reads off a timeline row, which is the other
// reason not to raise it.
const maxUnrecognizedRaw = 16 << 10

// ignoredLineTypes is the MEASURED set of top-level stream-json types the
// parser deliberately drops in silence. Membership is what separates "known and
// deliberately ignored" from "genuinely unrecognized"; getting it wrong in
// either direction is the whole risk of the unrecognized-message feature. Too
// narrow and every turn grows a noise row; too wide and a real new message type
// stays invisible.
//
// Measured 2026-07-27 by driving claude directly on this exact bare stream-json
// surface (the fixed --input-format/--output-format/--verbose prefix
// buildArgs emits), three turns each on haiku and on the default model, one turn
// per run calling tools. Observed top-level types across both runs:
//
//	assistant, user, result   — mapped below
//	system                    — subtypes init, thinking_tokens (and status, per
//	                            the #1088 spike); init fires ONCE PER TURN, and
//	                            thinking_tokens fired ~10 times per turn
//	rate_limit_event          — once per run
//
// system is ignored WHOLESALE rather than per-subtype on purpose. It is
// claude's catch-all namespace and its highest-rate emitter, so subtype-grained
// matching would turn every new subtype into a per-turn noise row — the exact
// failure the two-tier design exists to prevent. A genuinely new MESSAGE TYPE is
// the alarm worth raising, and that is what the top-level key catches.
//
// The measurement also settled the open question of whether claude echoes the
// delivered prompt back as a `user` message holding a `text` block, as it does
// on the agent-run surface: it does NOT here. Every `user` line in both runs
// carried tool_result blocks only. So user/text needs no ignore entry, and a
// user/text block appearing in future is a real change worth surfacing.
//
// A real-claude test asserts a normal turn produces zero unrecognized events, so
// this list going stale fails the pre-ship gate rather than reaching a client.
var ignoredLineTypes = map[string]bool{
	"system":           true,
	"rate_limit_event": true,
}

// Parser turns the child's stdout stream-json line stream into neutral
// turnevent.Event values. It is an io.Writer wired as streamsup Config.Stdout;
// each Write consumes every complete '\n'-delimited line and emits zero-or-more
// events per line to the sink, in stream order. A `result` line ends the turn
// (→ TurnEnd); no transcript file is opened, watched, or resolved (AC4) — the
// only input is the Write bytes.
//
// Turn-stateless. The parser holds no turn counter, no awaiting flag, no
// per-session accumulator — only the partial-line byte buffer. Each line maps
// independently, which makes zero cross-turn bleed structural: the only turn
// boundary is a `result` line, and no other line type can create, reset, or leak
// state across a boundary.
//
// Single-writer invariant: os/exec drives a non-*os.File Config.Stdout through
// exactly one internal goroutine (io.Copy of the child's stdout pipe into this
// writer), so Write — hence buf and the sink calls — is only ever invoked
// serially from that one goroutine. There is no second reader of parser state,
// so no mutex is needed. (Contrast streamrunner's streamParser, which locks
// because its watchdog goroutine reads its state.) If a future slice adds a
// concurrent reader, it adds the guard then.
type Parser struct {
	sink   func(turnevent.Event)
	log    *slog.Logger
	maxBuf int
	buf    []byte
}

var _ io.Writer = (*Parser)(nil)

// NewParser returns a Parser that emits events to sink. sink is called serially,
// in stream order, on the os/exec forwarder goroutine; the consumer owns any
// synchronization it needs beyond that (the round-trip test's sink pushes to a
// channel; a real consumer forwards to the relay). logger is used for
// content-free Debug diagnostics only; nil falls back to slog.Default.
func NewParser(sink func(turnevent.Event), logger *slog.Logger) *Parser {
	if logger == nil {
		logger = slog.Default()
	}
	return &Parser{
		sink:   sink,
		log:    logger,
		maxBuf: defaultMaxParseBuf,
	}
}

// Write appends b to the line buffer, consumes every complete '\n'-delimited
// line (emitting events to the sink), and keeps the partial remainder for the
// next Write. It always reports (len(b), nil): the parser is the terminal stdout
// sink, not a tee, so it fully consumes what it is handed and has no downstream
// short-write to propagate.
func (p *Parser) Write(b []byte) (int, error) {
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
		p.log.Debug("streamsup: dropping oversized partial line", "bytes", len(rest))
		rest = nil
	}
	// Copy the remainder into a fresh slice so the (possibly large) backing
	// array of p.buf is released.
	p.buf = append([]byte(nil), rest...)
	return len(b), nil
}

// streamLine is the minimal decoded shape of one stdout stream-json line: only
// the fields the mapping reads are declared; everything else claude emits is
// ignored. Segmentation keys on the top-level Type only — nested content is
// never re-scanned for control types, so a tool result whose text is literally
// `{"type":"result"}` cannot forge a turn boundary.
type streamLine struct {
	Type    string         `json:"type"`
	Subtype string         `json:"subtype"`
	Message *streamMessage `json:"message"`
}

// Content is held as raw bytes, not []streamBlock, and each element is decoded
// on demand in emitAssistant / emitUser. streamBlock declares only the fields
// the mapping reads, so decoding straight into it would DISCARD every unknown
// field — and re-marshalling the struct afterwards would lose exactly the
// content an unrecognized block exists to show. Keeping the bytes costs one
// deferred Unmarshal per block and makes the block's original JSON available
// verbatim; it also turns a block that fails to decode into a surfaced event
// rather than a silent skip.
type streamMessage struct {
	ID      string            `json:"id"`
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

// streamBlock is one content block of an assistant/user message, decoded from
// the raw bytes streamMessage.Content holds. The fields are
// a union across the block types we map: text (assistant text), thinking
// (assistant thinking), id/name/input (tool_use), tool_use_id/content/is_error
// (tool_result). Content is decoded as `any` so a tool_result's content — a JSON
// string or an array of text blocks — lands as the string / []any that
// toolResultText switches on (mirroring mapper.go).
type streamBlock struct {
	Type string `json:"type"`

	// assistant text / thinking
	Text     string `json:"text"`
	Thinking string `json:"thinking"`

	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`

	// tool_result
	ToolUseID string `json:"tool_use_id"`
	Content   any    `json:"content"`
	IsError   bool   `json:"is_error"`
}

// consumeLine decodes one complete line and emits the events it maps to. A blank
// line emits nothing. A line on the measured known-ignored list emits nothing and
// is Debug-logged by type only — never content. A line that fails to decode, and
// a line of a type outside both the mapped set and the ignored list, emits a
// turnevent.Unrecognized so the drop is VISIBLE to a client instead of dying in a
// debug log the production daemon does not print. Either way one bad line never
// poisons later lines.
func (p *Parser) consumeLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var sl streamLine
	if err := json.Unmarshal(line, &sl); err != nil {
		// Not even the type is known here, so Kind stays empty. Previously this
		// dropped without recording anything at all.
		p.emitUnrecognized(turnevent.UnrecognizedUndecodable, "", line)
		return
	}
	switch sl.Type {
	case "assistant":
		p.emitAssistant(sl.Message)
	case "user":
		p.emitUser(sl.Message)
	case "result":
		// The turn boundary. The subtype selects the reason: an interrupted turn
		// (subtype error_during_execution, spike T1 #1075) → cancelled; a clean
		// turn and every other subtype → end_turn. Richer max_tokens/refusal
		// classification remains future work (resultTurnEndReason's default).
		p.emit(turnevent.TurnEnd{Reason: resultTurnEndReason(sl.Subtype)})
	default:
		if ignoredLineTypes[sl.Type] {
			// system (init / thinking_tokens / status) and rate_limit_event:
			// tolerated and dropped, silently, exactly as before. system/init is a
			// per-turn marker (spike § 1), not a session-open event — dropping it is
			// correct because the parser is turn-stateless (there is no session
			// state to reset). See ignoredLineTypes for the measurement behind the
			// list.
			p.log.Debug("streamsup: dropping stdout line", "type", sl.Type)
			return
		}
		// A type we have never seen. Surface it: this is the one arm that tells
		// anyone claude started emitting something new.
		p.emitUnrecognized(turnevent.UnrecognizedLineType, sl.Type, line)
	}
}

// emitUnrecognized builds and emits one turnevent.Unrecognized, truncating raw to
// maxUnrecognizedRaw first so an oversized payload never enters the event stream.
// The log records site, type, and byte count only — never the content itself,
// which is the package's standing rule; the content crosses the wire, not the log.
func (p *Parser) emitUnrecognized(site turnevent.UnrecognizedSite, kind string, raw []byte) {
	text, truncated := truncateRaw(raw)
	p.log.Debug("streamsup: unrecognized payload",
		"site", string(site),
		"type", kind,
		"bytes", len(raw),
		"truncated", truncated)
	p.emit(turnevent.Unrecognized{
		Site:      site,
		Kind:      kind,
		Raw:       text,
		Truncated: truncated,
	})
}

// truncateRaw cuts raw to maxUnrecognizedRaw bytes, reporting whether it cut.
// Byte-sliced, then scrubbed of any invalid UTF-8 the cut may have produced:
// slicing can land mid-rune, and the result rides a JSON string field, where an
// invalid sequence would be silently replaced downstream anyway. Doing it here
// keeps the payload well-formed at the point of construction. The value is a
// diagnostic blob, not text to read to the end of, so cutting mid-structure is
// fine; Truncated is what tells the reader the JSON is incomplete.
func truncateRaw(raw []byte) (string, bool) {
	if len(raw) <= maxUnrecognizedRaw {
		return strings.ToValidUTF8(string(raw), ""), false
	}
	return strings.ToValidUTF8(string(raw[:maxUnrecognizedRaw]), ""), true
}

// resultTurnEndReason maps a result line's subtype to its TurnEnd reason.
// error_during_execution is claude's interrupt-terminated turn (spike T1,
// #1075) → cancelled; every other subtype (success, and any unknown) keeps
// end_turn — correct for a clean turn and a safe default otherwise. The change
// is scoped to error_during_execution only: this is not a general subtype→reason
// table (max_tokens/refusal classification remains future work).
func resultTurnEndReason(subtype string) turnevent.TurnEndReason {
	switch subtype {
	case "error_during_execution":
		return turnevent.TurnEndReasonCancelled
	default:
		return turnevent.TurnEndReasonEndTurn
	}
}

// emitAssistant maps one assistant message's content blocks. Unlike mapper.go
// (one block per JSONL line), a stream-json assistant event carries a whole
// message that may hold several blocks; we iterate them and emit one event per
// block, preserving order. A nil message or zero mappable blocks emits nothing.
func (p *Parser) emitAssistant(msg *streamMessage) {
	if msg == nil {
		return
	}
	for _, raw := range msg.Content {
		block, ok := p.decodeBlock(raw)
		if !ok {
			continue
		}
		switch block.Type {
		case "text":
			p.emit(turnevent.TextChunk{MessageID: msg.ID, Text: block.Text})
		case "thinking":
			p.emit(turnevent.ThoughtChunk{MessageID: msg.ID, Text: block.Thinking})
		case "tool_use":
			p.emit(turnevent.ToolStart{
				ToolCallID: block.ID,
				Title:      block.Name,
				Kind:       toolKind(block.Name),
				RawInput:   rawInput(block.Input),
			})
		default:
			// No known-ignored list at block level: the measurement found exactly
			// these three assistant block types and nothing else, so there is no
			// per-turn noise to suppress. Any fourth is news.
			p.emitUnrecognized(turnevent.UnrecognizedAssistantBlock, block.Type, raw)
		}
	}
}

// decodeBlock unmarshals one content block's raw bytes into streamBlock. A block
// that fails to decode is surfaced as an undecodable Unrecognized rather than
// skipped in silence, and reports ok == false so the caller moves on to the next
// block — one malformed block never costs the rest of the message.
func (p *Parser) decodeBlock(raw json.RawMessage) (streamBlock, bool) {
	var block streamBlock
	if err := json.Unmarshal(raw, &block); err != nil {
		p.emitUnrecognized(turnevent.UnrecognizedUndecodable, "", raw)
		return streamBlock{}, false
	}
	return block, true
}

// emitUser maps one user message's tool_result blocks to ToolUpdate. A nil
// message emits nothing; any non-tool_result block emits an Unrecognized.
func (p *Parser) emitUser(msg *streamMessage) {
	if msg == nil {
		return
	}
	for _, raw := range msg.Content {
		block, ok := p.decodeBlock(raw)
		if !ok {
			continue
		}
		if block.Type != "tool_result" {
			// The measurement found user messages carry tool_result blocks and
			// nothing else — notably NOT a text echo of the delivered prompt, which
			// the agent-run surface does emit. So a user/text block here would be a
			// genuine change, and gets surfaced rather than dropped.
			p.emitUnrecognized(turnevent.UnrecognizedUserBlock, block.Type, raw)
			continue
		}
		p.emit(turnevent.ToolUpdate{
			ToolCallID: block.ToolUseID,
			Status:     toolStatus(block.IsError),
			Content:    toolResultContent(block.Content),
		})
	}
}

// emit forwards one event to the sink. A nil sink is a no-op guard — the parser
// runs on the os/exec forwarder goroutine, where a nil-sink panic would be
// disproportionate to a misconfiguration.
func (p *Parser) emit(ev turnevent.Event) {
	if p.sink != nil {
		p.sink(ev)
	}
}

// The helpers below mirror internal/turnbridge/mapper.go: unexported and keyed
// on tui-driver types there, so they cannot be imported; re-implemented here on
// our already-decoded fields (no ParseToolUse/ParseToolResult re-parse).

// toolKind maps a claude tool name to its ACP kind, best-effort; unknown names
// fall to ToolKindOther.
func toolKind(name string) turnevent.ToolKind {
	switch name {
	case "Read":
		return turnevent.ToolKindRead
	case "Edit", "Write":
		return turnevent.ToolKindEdit
	case "Bash":
		return turnevent.ToolKindExecute
	case "Grep", "Glob":
		return turnevent.ToolKindSearch
	case "WebFetch":
		return turnevent.ToolKindFetch
	case "Task":
		return turnevent.ToolKindThink
	default:
		return turnevent.ToolKindOther
	}
}

// toolStatus maps a tool_result's is_error flag to a terminal status: a
// tool_result marks the call finished, so completed/failed (never pending).
func toolStatus(isError bool) turnevent.ToolStatus {
	if isError {
		return turnevent.ToolStatusFailed
	}
	return turnevent.ToolStatusCompleted
}

// toolResultContent maps a tool_result's content union to ToolContent. Empty or
// absent content yields nil — the legal status-only ToolUpdate.
func toolResultContent(content any) turnevent.ToolContent {
	text := toolResultText(content)
	if text == "" {
		return nil
	}
	return turnevent.TextContent{Text: text}
}

// toolResultText extracts plain text from a tool_result content union: a string
// returns itself; a []any joins the "text" field of each {"type":"text",…}
// block; anything else returns "".
func toolResultText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := block["type"].(string); t != "text" {
				continue
			}
			s, _ := block["text"].(string)
			b.WriteString(s)
		}
		return b.String()
	default:
		return ""
	}
}

// rawInput carries a tool_use's already-decoded input bytes through opaquely for
// ToolStart.RawInput. Empty/absent input → nil. Unlike mapper.go's rawInput (a
// map re-marshal, which sorts keys), passing claude's raw bytes preserves the
// original key order and avoids a second marshal — RawInput is opaque
// pass-through the consumer never key-orders against.
func rawInput(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	return in
}
