package streamsup

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// toolResultSidecar identifies the edit and write shapes by key presence.
// Extra keys are allowed: observed sidecars often carry additional metadata.
// Read, shell and search fields are absent because those rows send no detail.
// Unbounded values are decoded only for presence through jsonKey; each composer
// decodes the values it counts into its own narrow target.
type toolResultSidecar struct {
	StructuredPatch *jsonKey `json:"structuredPatch"`
	OldString       *jsonKey `json:"oldString"`
	NewString       *jsonKey `json:"newString"`
	Content         *jsonKey `json:"content"`
	Type            *string  `json:"type"`
}

// sidecarPatch and sidecarContent are the narrow targets for counted values.
// Each composer returns only formatted integers and literals, so no decoded
// sidecar text escapes into daemon state. filePath and originalFile are absent.
type sidecarPatch struct {
	StructuredPatch []patchHunk `json:"structuredPatch"`
}

// patchHunk is one hunk of a structuredPatch, narrowed to its `lines`.
//
// oldStart/oldLines/newStart/newLines are deliberately absent: oldLines and
// newLines are the hunk's SPANS with context lines included, so differencing
// them yields the net change and not "+10 −3". The two numbers can only come
// from the +/- prefixes of `lines`, and an undeclared span cannot be reached for
// by a later edit.
type patchHunk struct {
	Lines []string `json:"lines"`
}

type sidecarContent struct {
	Content string `json:"content"`
}

// minusSign and middleDot are the row separators, and they are CONTRACT rather
// than formatting preference: the client renders this text verbatim. Written as
// literals rather than \u escapes so that a search for either glyph finds its
// declaration; the trailing comment names the codepoint, because a U+002D typed
// in place of the U+2212 would otherwise be invisible in a diff. They are the
// first non-ASCII bytes this field has ever carried — see maxResultDetailBytes,
// which is where that fact is load-bearing.
const (
	minusSign = "−" // MINUS SIGN, NOT U+002D HYPHEN-MINUS
	middleDot = "·" // MIDDLE DOT, spaced on both sides
)

// maxResultDetailBytes retains the historical conservative byte allowance.
// Each non-negative int64 formats to at most 19 digits. The remaining edit form
// is at most 1 + 19 + 1 + len(minusSign) + 19 = 43 bytes, and the write form is
// at most len("updated") + 1 + len(middleDot) + 1 + 19 + len(" lines") = 36.
// Both fit the existing 48-byte allowance used by the downstream envelope test.
const maxResultDetailBytes = 48

// toolResultDetail composes a trailing count only for edits and writes, tried
// in that order. Other shapes, absent sidecars and decode failures send nothing.
// No sidecar failure emits Unrecognized: that event would expose raw sidecar
// bytes, including file contents and operator paths, to clients.
// The composed string contains only formatted integers and fixed literals.
func toolResultDetail(sidecar json.RawMessage) string {
	if len(sidecar) == 0 {
		return ""
	}
	var sc toolResultSidecar
	// The error is deliberately swallowed rather than classified. The line has
	// already decoded once, so this cannot be news; see the fail-closed note above.
	if err := json.Unmarshal(sidecar, &sc); err != nil {
		return ""
	}
	if d := editDetail(&sc, sidecar); d != "" {
		return d
	}
	return writeDetail(&sc, sidecar)
}

// editDetail composes an edit's row text — "+10 −3", either half omitted when it
// is zero, nothing when both are.
//
// THE TWO NUMBERS CAN ONLY COME FROM THE +/- PREFIXES OF THE HUNKS' LINES; see
// patchHunk for why the hunk spans cannot give them. Those prefixes are the
// diff's own ASCII bytes and are read as data: the U+2212 in the output is
// display text this function composes, never a byte that came from claude.
//
// Both halves zero is unobserved — of 632 measured edits, 425 changed both
// halves, 190 added only and 17 removed only — so dropping it is a fail-closed
// decision about a shape with no observations, not a measured rule.
func editDetail(sc *toolResultSidecar, sidecar json.RawMessage) string {
	if sc.StructuredPatch == nil || sc.OldString == nil || sc.NewString == nil {
		return ""
	}
	var p sidecarPatch
	if err := json.Unmarshal(sidecar, &p); err != nil {
		return ""
	}
	var added, removed int64
	for _, h := range p.StructuredPatch {
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				added++
			case strings.HasPrefix(l, "-"):
				removed++
			}
		}
	}
	switch {
	case added > 0 && removed > 0:
		return "+" + strconv.FormatInt(added, 10) + " " + minusSign + strconv.FormatInt(removed, 10)
	case added > 0:
		return "+" + strconv.FormatInt(added, 10)
	case removed > 0:
		return minusSign + strconv.FormatInt(removed, 10)
	}
	return ""
}

// writeDetail composes a write's row text — "created · 54 lines".
//
// The `type` is checked BEFORE the content is decoded, so a verb this row cannot
// name never materialises the file being written. structuredPatch is required to
// be PRESENT but not non-empty: it is [] on every one of the 240 observed
// creates, so an arm demanding hunks would be dead code on 97% of writes.
//
// Empty content still sends "created · 0 lines": an empty file that was
// created is something that happened.
func writeDetail(sc *toolResultSidecar, sidecar json.RawMessage) string {
	if sc.StructuredPatch == nil || sc.Content == nil || sc.Type == nil {
		return ""
	}
	var verb string
	switch *sc.Type {
	case "create":
		verb = "created"
	case "update":
		verb = "updated"
	default:
		return ""
	}
	var c sidecarContent
	if err := json.Unmarshal(sidecar, &c); err != nil {
		return ""
	}
	return verb + " " + middleDot + " " + strconv.FormatInt(countLines(c.Content), 10) + " lines"
}

// countLines counts a write's content: a trailing newline does not add a line,
// so "a\nb\n" and "a\nb" are both 2, and "" is 0.
//
// It returns an int64 rather than a sub-slice or a []string, which is what keeps
// the composed string from pinning the decoded value's allocation.
func countLines(s string) int64 {
	if s == "" {
		return 0
	}
	n := int64(strings.Count(s, "\n"))
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// toolProgressHeartbeatTrue is the ONE byte sequence the heartbeat marker
// matches. Compared against a json.RawMessage, so this is a test on the value
// claude actually put on the wire rather than on whatever a Go type would coerce
// it to.
var toolProgressHeartbeatTrue = []byte("true")

// The marker names, as logged. A closed set of three, chosen by this package —
// never a byte derived from claude's line, which is what keeps the drop site's
// record content-free while still saying which variety fired.
const (
	toolProgressMarkerHeartbeat    = "heartbeat"
	toolProgressMarkerSubagentType = "subagent_type"
	toolProgressMarkerReplCall     = "repl_call"
)

// toolProgressMarkers carries the three WIRE FIELDS that identify a tool_progress
// variety. Four internal engine events feed the one outbound type; three of them
// set one of these, and the fourth — bash/powershell progress — is the residual
// shape, identified only by the absence of all three.
//
// Kept separate from streamLine for userToolResultLine's reason: streamLine is
// the line-level SEGMENTATION struct and stays at Type/Subtype/Message, so fields
// belonging to one line type would blur that boundary. The line is decoded a
// second time instead, which consumeLine already hands the raw bytes down for —
// emitUser, emitRateLimit and decodeModelWindows all take them.
//
// THE MARKERS ARE FIELD NAMES, NOT CONTENT, AND NEITHER READS A tool_name. The
// report's frame showed a `-heartbeat-N` suffix on tool_use_id and that suffix is
// deliberately not matched: it is content, and a counter in an identifier is the
// most fragile thing in the frame. The retry variety's tool_name is a minified
// module constant this surface cannot pin, so subagent_type stands in for it —
// every agent_api_retry frame sets it, resolved and unresolved alike, and no
// other variety does. subagent_retry (set only on the unresolved one) is
// therefore not read: it would widen the matched set without widening what is
// caught.
//
// HEARTBEAT IS json.RawMessage RATHER THAN *bool, AND THAT IS LOAD-BEARING. A
// *bool target turns `"heartbeat": "true"` into a whole-line UnmarshalTypeError;
// encoding/json saves that error and keeps decoding, so the OTHER markers still
// populate while the call reports failure, and the matcher is left choosing
// between honouring the error (dropping a validly-marked frame to the lane) and
// ignoring it (losing the type-strictness). A RawMessage cannot fail, and a byte
// comparison against `true` is exactly the strict test wanted: "true", 1 and
// false each produce different bytes and none of them matches.
//
// The other two are *jsonKey — presence decided without a byte of claude's value
// reaching daemon state, and present-but-null distinguished from present, so a
// null marker is not a marker.
type toolProgressMarkers struct {
	Heartbeat    json.RawMessage `json:"heartbeat"`
	SubagentType *jsonKey        `json:"subagent_type"`
	ReplCall     *jsonKey        `json:"repl_call"`
}

// toolProgressHeartbeat is the heartbeat variety's second decode target. It is
// separate from toolProgressMarkers so marker tolerance cannot be widened by a
// field needed only after a strict heartbeat match, and streamLine stays a
// segmentation struct.
//
// ParentToolUseID is raw so one malformed join key drops only the event rather
// than making encoding/json's saved type error compete with the already-set
// marker. ElapsedSeconds is signed and absent reads as zero, following
// systemTaskProgressUsage's decode contract: unusual upstream readings remain
// observable rather than wrapping or becoming a validation rule.
type toolProgressHeartbeat struct {
	ParentToolUseID json.RawMessage `json:"parent_tool_use_id"`
	ElapsedSeconds  int             `json:"elapsed_time_seconds"`
}

// consumeToolProgress reports whether this tool_progress line carried a known
// marker. A heartbeat may emit one ToolProgress; the other known varieties remain
// content-free drops. Unlike emitRateLimit's void return, this arm has a real "did
// you handle it?" to report back, because a marker-less frame must still reach the
// unrecognized lane.
//
// The case arms are the ONE enumeration of the matched set, as they are in
// emitSystemSubtype — adding a variety IS adding an arm.
//
// THIS IS A MATCHING PRIMITIVE, so the narrowness above is the whole safety
// argument and not fussiness. A matched heartbeat now publishes an event, while
// either other match withholds a row; both consequences stay bounded exactly while
// the marker set stays narrow and type-strict. Loosening a marker — any truthy
// heartbeat, a tool_name prefix, the id suffix — would either inject an event from
// an unmeasured variety or swallow a frame nobody measured. harnessNoOutputNudge
// argues its own tolerance the same way, and for the same reason: a suppression or
// mapping match is the thing to be strict about.
//
// # MEASURED 2026-09-06 — claude 2.1.259, model haiku, one live turn
//
// The capture is internal/e2e/realclaude/testdata/tool_progress_v2.1.259.json,
// recorded upstream of the parser by TestRealClaude_ToolProgressCapture: a
// foreground Bash call (`cat` on a rig-held FIFO) held open across a 379 s turn,
// 21 lines on the wire, 12 of them tool_progress. Stated in the CORRECTED/AMENDED
// style ignoredLineTypes uses, and a variety measured ABSENT is recorded as
// absent rather than quietly added to the matched set:
//
//   - heartbeat — OBSERVED, 12/12 frames. Every one carries `"heartbeat":true`
//     and an `elapsed_time_seconds` stepping 30, 60 … 360, which is the wire-level
//     confirmation of the `setInterval` at 30 000 ms behind it: the first tick
//     lands at t+30 s, so a call that leaves the foreground sooner produces
//     nothing whatever the marker set says. tool_use_id carries the reported
//     `-heartbeat-N` suffix and is still deliberately not matched.
//   - subagent retry — ABSENT, and the absence is a property of the staging, not
//     of the surface: agent_api_retry is not reachable from a foreground Bash
//     call at all, so no single-turn capture of this shape could observe it.
//     subagent_type is on the declared schema and set by both the resolved and
//     unresolved emits; the marker is UNEXERCISED, not confirmed.
//   - repl call — ABSENT, same reason, same status.
//   - bash/powershell progress, the residual marker-less shape — NOT OBSERVED,
//     and open question 1 is NARROWED rather than closed. Zero of the 12 frames
//     carried none of the three markers, but two sufficient explanations fit that
//     equally and this capture cannot separate them. (a) The two emit sites differ
//     for this variety alone: one is behind
//     `if(!CLAUDE_CODE_REMOTE && !CLAUDE_CODE_CONTAINER_ID) break;` plus a
//     throttle, the other — logging `[engine] yield-twin tool_progress` — has
//     neither, and neither variable was set on this surface. The frames cannot
//     say which emitter fed them: the yield-twin wrapper appends session_id and a
//     uuid to every frame it yields, so the two heartbeat literals reach the wire
//     byte-identical, key order included. (b) The engine event is raised per chunk
//     the shell generator yields, carrying output/totalLines/totalBytes — and the
//     staged `cat <fifo>` produces no output at all until EOF, so the event may
//     simply never have been raised. Settling it needs a different staging: a
//     command with incremental output, or a run with CLAUDE_CODE_CONTAINER_ID set.
//
// The failure direction is the safe one, which is why the unexercised markers and
// the unresolved question are tolerable here: an unmatched frame FALLS THROUGH to
// the unrecognized lane, so a variety nobody measured stays visible as the row it
// is today rather than being swallowed. The marker-less shape is unhandled ON
// PURPOSE.
//
// TestParser_ToolProgressCapturedFramesEmitHeartbeatEvents walks that capture
// through this arm inside `make check`, which is what stops a decoder keyed on a
// spelling claude does not use from never firing in silence.
func (p *Parser) consumeToolProgress(line []byte) bool {
	var m toolProgressMarkers
	// The error is not a branch. consumeLine has already decoded this line into
	// streamLine, so it is well-formed JSON with an object at the top level;
	// RawMessage accepts any value and jsonKey.UnmarshalJSON discards every one,
	// so this cannot fail. An `if err != nil` arm here would be unreachable code —
	// userToolResultLine states the same property for the same reason.
	_ = json.Unmarshal(line, &m)

	var marker string
	switch {
	case bytes.Equal(m.Heartbeat, toolProgressHeartbeatTrue):
		marker = toolProgressMarkerHeartbeat
		p.emitToolProgressHeartbeat(line)
	case m.SubagentType != nil:
		marker = toolProgressMarkerSubagentType
	case m.ReplCall != nil:
		marker = toolProgressMarkerReplCall
	default:
		return false
	}
	// Type and marker only. Both are keywords from closed sets this package owns —
	// the same class as the sl.Type the drop branch below logs — so no byte of the
	// frame is recorded anywhere.
	p.log.Debug("streamsup: dropping tool_progress", "type", "tool_progress", "marker", marker)
	return true
}

// emitToolProgressHeartbeat emits at most one ToolProgress from a matched
// heartbeat. Every failure is silent: consumeToolProgress still reports the line
// handled, so an unusable join key or elapsed shape cannot move it into the
// unrecognized lane.
func (p *Parser) emitToolProgressHeartbeat(line []byte) {
	var heartbeat toolProgressHeartbeat
	if err := json.Unmarshal(line, &heartbeat); err != nil {
		return
	}
	toolCallID := parentToolUseID(heartbeat.ParentToolUseID)
	if toolCallID == "" {
		return
	}
	p.emit(turnevent.ToolProgress{
		ToolCallID:     toolCallID,
		ElapsedSeconds: heartbeat.ElapsedSeconds,
	})
}

// The helpers below map this parser's already-decoded fields (no
// ParseToolUse/ParseToolResult re-parse). They began as a copy of the PTY path's
// tui-driver-keyed mapping helpers; those were deleted with that path (#1543),
// so these are now the only copy and mirror nothing.

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
