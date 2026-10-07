package streamsup

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// assistantErrorLine is the decoded API-error category of one `assistant` line
// (#2224). Kept separate from streamLine for resultStopLine's reason applied to a
// different line type: the segmentation struct stays at Type/Subtype/Message, and
// TestStreamLine_StaysSegmentationOnly enforces it.
//
// A SEPARATE TARGET RATHER THAN A WIDER streamLine, and the isolation runs in BOTH
// directions here, which is stronger than resultStopLine needs. streamLine decodes
// `message`, whose content array claude controls; folded together, a hostile message
// shape would blank the category, and an `error` of a hostile shape would blank the
// message — costing every content block on the line. Two targets fail independently,
// so a line whose message will not decode still reports its category, and a line
// whose category will not decode still emits all its blocks.
//
// ONE KEY DECLARED, and absence from the decode target is the stronger guarantee —
// a field that is never declared cannot leak. The key deliberately NOT declared is
// `message` itself: claude's prose for the turn lives there, a turn_end frame has
// never carried it, and this ticket does not start.
//
// A plain string, so a non-string `error` — a number, an object, claude's own richer
// error shape if it ever ships one — fails the whole decode and takes the absent
// path. That is the conservative direction: an error this parser cannot read must not
// be published as one it can.
type assistantErrorLine struct {
	Error string `json:"error"`
}

// assistantParentLine carries the `parent_tool_use_id` sibling of an `assistant`
// line's `message` — the tool_use_id of the Agent/Task call that spawned the
// subagent producing the line, or null on the main conversation (#2191).
//
// A THIRD TARGET RATHER THAN A WIDER assistantErrorLine, and that struct's own
// docblock is the argument. It exists so a hostile `message` cannot blank the
// category and a hostile `error` cannot blank the message; folding this key in
// would put a THIRD value behind the same single point of failure, so an `error`
// of a shape the target cannot hold would silently un-nest every subagent row on
// the line. Two targets fail independently — a line whose error will not decode
// still reports its parent, and one whose parent will not decode still reports
// its error. TestParser_ParentToolUseID_LeavesTheAssistantErrorCategoryIntact is
// that property in both directions.
//
// The USER line answers the same key the opposite way, by widening userLine, and
// the two are not in tension: that struct's rule is "ONE decode, not two", because
// a user line routinely carries a whole file and a second full pass over it is not
// free. An assistant line carries the model's own blocks, so the second pass here
// is affordable where it is not there. Read both docs before moving either field.
//
// json.RawMessage, NOT string, so the decode cannot fail on this key's value. The
// type is not load-bearing on this side — an isolated target has nothing to lose
// by failing — and it is chosen anyway so that ONE converter decides what a valid
// value is for both line types, rather than two arms drifting into disagreeing
// about the same key. It IS load-bearing on the user side; see userLine.
type assistantParentLine struct {
	ParentToolUseID json.RawMessage `json:"parent_tool_use_id"`
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

// decodeAssistantError reads the wrapper-level API error category off one `assistant`
// line and returns it bounded (#2224). A pure function of the bytes: no receiver, no
// parser state read or written, nothing logged on any path.
//
// decodeStopShape's three properties hold here verbatim and are not restated: it
// cannot disturb what the line emits, every failure returns a value rather than an
// error, and the decode error is DISCARDED rather than logged because encoding/json
// quotes the offending input into its error text — logging nothing at all is what
// makes "no claude-authored byte from this decode reaches a log line" structural.
//
// THE BOUND IS APPLIED HERE rather than at the emit, and here it does more work than
// it does for its siblings: the value is not published on the line it is read from
// but REMEMBERED until the turn boundary, so bounding at the emit would leave an
// unbounded string sitting in parser state in between. Bounding at the decode means
// the parser never holds one.
//
// WHAT IT DELIBERATELY DOES NOT DO is treat an unreadable `error` differently from an
// absent one. Both give "", and a consumer cannot tell them apart, because there is
// nothing it would do differently — turnevent.TurnEnd.Outcome's absent-or-over-cap
// collapse, applied to a third shape.
func decodeAssistantError(line []byte) string {
	var al assistantErrorLine
	if err := json.Unmarshal(line, &al); err != nil {
		return ""
	}
	return boundStopField(al.Error)
}

// decodeAssistantParent reads the `parent_tool_use_id` sibling off one assistant
// line. A pure function of the bytes: no receiver, no parser state read or written,
// nothing logged on any path — decodeAssistantError's shape, and its reason for
// discarding the decode error rather than logging it. Every failure returns a value,
// and "" is a complete answer to "claude named no spawning call".
func decodeAssistantParent(line []byte) string {
	var pl assistantParentLine
	if err := json.Unmarshal(line, &pl); err != nil {
		return ""
	}
	return parentToolUseID(pl.ParentToolUseID)
}

// parentToolUseID is the one site that validates the parent_tool_use_id wire key
// wherever streamsup publishes it. A JSON string yields its decoded value;
// every other JSON value — a number, an object, an array, null — and an absent key
// yield "". On assistant/user lines that means "main thread"; on tool_progress
// it makes the heartbeat unjoinable, so emitToolProgressHeartbeat drops the event.
//
// It is also the one site that applies the bound, so the cap cannot be applied twice
// or forgotten on any arm. The key's RELATIONSHIP meaning remains line-specific:
// assistant/user lines name the Agent/Task call that spawned a subagent, while a
// tool_progress heartbeat names the foreground tool call whose elapsed time it
// reports.
//
// THE CAP IS maxTaskFieldID, NOT A NEW CONSTANT. That constant caps a
// machine-generated identifier, which is exactly this value's class; observed values
// are `toolu_`-prefixed and around 30 bytes, so 256 is roughly 8x the observation —
// the multiple-of-observation form that constant already uses. #2233's
// consumePermissionDeniedLine applies the same cap to a tool_use_id on a frame that
// ships, and is the precedent followed. ToolUseID's verbatim pass-through on these
// same two frames is the older one, and is deliberately NOT followed: leaving a
// claude-authored string bounded only by defaultMaxParseBuf puts that whole 4 MiB on
// a NEVER-DROPPABLE frame (tool_result is control class 4413), where a frame over the
// application-envelope cap is lost rather than truncated and the operator sees no row
// at all. The check is O(1) against a line that cap has already bounded.
//
// IT DROPS RATHER THAN CUTS, and here that judgement is sharper than
// maxTurnEndStopField's. This value is a JOIN KEY, not prose: a cut id matches no
// tool_use_id while still LOOKING like one, so a client joining on it could file a
// row under the wrong parent. Dropping degrades to "", which renders the row at top
// level — the behaviour before this field existed, and an honest answer where a wrong
// parent is not. The boundary is <=, matching boundStopField's, so a value of exactly
// the cap is carried.
//
// No truncation report is owed, on maxTurnEndStopField's rule: a dropped scalar reads
// as an absent one, and that is the intended reading. #2233 needed report arrays
// because three of its fields are EMPTIED and empty had two meanings there; here empty
// has exactly one, "main thread", which an over-cap id degrades into truthfully.
//
// No strings.ToValidUTF8 scrub, for boundStopField's reason: that function scrubs
// because it CUTS and a cut can land mid-rune. Nothing here cuts, and the input is a
// Go string encoding/json has already U+FFFD-replaced.
func parentToolUseID(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	if len(s) > maxTaskFieldID {
		return ""
	}
	return s
}

// emitAssistant maps one assistant message's content blocks. Unlike mapper.go
// (one block per JSONL line), a stream-json assistant event carries a whole
// message that may hold several blocks; we iterate them and emit one event per
// block, preserving order. A nil message or zero mappable blocks emits nothing.
//
// It takes the RAW LINE BYTES as well as the message, since #2191, because the
// spawning Agent call it reads is a sibling of `message` on the LINE rather than
// a field inside it — so this call site is the only place it can meet the blocks.
// emitUser's signature is the in-file precedent and states the same reason.
//
// DECODED HERE RATHER THAN LATCHED IN consumeLine, which is where #2224's
// error category is read, and the difference is the point. That value belongs to
// the TURN and has to survive until the turn_end fires, so it is parser state with
// a reset boundary. This one belongs to the LINE and is consumed inside this call,
// so there is nothing to latch — and latching it would be a bug, not merely
// redundant: a residual would file the next main-thread line's tool calls under the
// last subagent that ran. TestParser_ParentToolUseID_ReadsInnerDepthVerbatim drives
// two lines through one parser and would redden on exactly that.
//
// The nil-message early return costs nothing here: a line with no message has no
// blocks to attribute, so a parent id read from it would have no consumer.
func (p *Parser) emitAssistant(msg *streamMessage, line []byte) {
	if msg == nil {
		return
	}
	parent := decodeAssistantParent(line)
	for _, raw := range msg.Content {
		block, ok := p.decodeBlock(raw)
		if !ok {
			continue
		}
		switch block.Type {
		case "text":
			// Claude's partial-message surface settles one open text block as a
			// single-block assistant line after all of its deltas. Suppress only that
			// captured correlation; multi-block and unattributed assistant lines keep
			// the established completed-text behavior.
			if len(msg.Content) == 1 && msg.ID == p.streamMessageID &&
				p.streamBlockOpen && p.streamBlockType == "text" && p.streamBlockTextDelta {
				continue
			}
			p.emit(turnevent.TextChunk{
				MessageID:        msg.ID,
				ParentToolCallID: parent,
				Text:             block.Text,
			})
		case "thinking":
			p.emit(turnevent.ThoughtChunk{MessageID: msg.ID, Text: block.Thinking, ParentToolCallID: parent})
		case "tool_use":
			p.emit(turnevent.ToolStart{
				ToolCallID:       block.ID,
				ParentToolCallID: parent,
				Title:            block.Name,
				Kind:             toolKind(block.Name),
				RawInput:         rawInput(block.Input),
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
