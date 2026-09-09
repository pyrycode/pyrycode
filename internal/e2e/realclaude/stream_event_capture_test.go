//go:build e2e_realclaude

package realclaude

// Evidence capture for #2269 — every stdout line of ONE live turn spawned with
// --include-partial-messages, verbatim and in arrival order.
//
// The interactive spawn does not request partial messages today, so no
// stream_event line has ever reached the parser and the string appears nowhere
// else in the tree. #2270 writes the parser arm from the bytes this probe commits
// rather than from the Agent SDK's type definitions, which name keys the wire does
// not always send and say nothing at all about arrival order.
//
// # What is reused, and the one deliberate difference
//
// dropcapRecorder (line splitting and the `result` turn boundary), dropcapRedactor,
// dropcapScanner, dropcapMakeEntry (payload encoding, base64 arm included),
// newDropcapArgvHandler, dropcapWaitForChild and parseOne all live in
// dropped_line_capture_test.go. The fixture-absence gate is #2089's; the
// version-named fixture behind a globbing gate, the t.Deadline()-sized budget and
// the double write are #2262's.
//
// THE DIFFERENCE: every sibling capture in this family filters to a quarry — the
// lines the parser dropped, the tool_progress frames, the api_retry lines. This one
// keeps EVERY line of the turn, because the questions #2270 turns on are about
// ORDER and ADJACENCY: where an `assistant` line sits relative to the deltas of the
// block it settles, and which line carries the message id the delta coalescer keys
// on. A filtered capture answers none of them, and two consequences follow that the
// sibling probes do not carry — see secapFixtureWorthy's cap arm and
// secapRedactionRationale.
//
// # Production is untouched
//
// The flag rides in this probe's own streamsup.Config.Args, which buildArgs appends
// after its fixed --input-format/--output-format/--verbose prefix. Nothing in
// internal/streamsup changes, and the production interactive spawn still emits no
// --include-partial-messages.
//
// # Running it
//
// `make e2e-realclaude` on an authenticated machine, and nothing else: the probe
// arms itself while the fixture is absent and disarms once it exists. To force a
// re-capture at a new claude version:
//
//	PYRY_PROBE_STREAM_EVENT_CAPTURE=1 go test -tags e2e_realclaude -timeout 15m -v \
//	  -run '^TestRealClaude_StreamEventCapture$' ./internal/e2e/realclaude/
//
// Read WHICH skip: "fixture already exists" is the steady state, while a skip from
// WithWorktreeAuthenticated means the machine has no claude login and no evidence
// was produced. The TestSecap* tests below run offline, with no claude.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// secapEnableEnv FORCES a re-capture when a fixture already exists. It is not the
// gate — see the gate comment in TestRealClaude_StreamEventCapture.
const secapEnableEnv = "PYRY_PROBE_STREAM_EVENT_CAPTURE"

// The fixture is matched by GLOB and NAMED from the observed version, which is
// #2262's shape rather than #2229's compile-time pin: the record IS this ticket's
// deliverable, so a claude release landing between authoring and the live gate run
// must not turn the whole output into nothing promoted. #2270's reader pins from
// the other end, splicing its own version constant.
const (
	secapFixtureGlob   = "testdata/stream_event_v*.json"
	secapFixturePrefix = "testdata/stream_event_v"
	// Long enough for any real version, short enough that a pathological one
	// cannot compose an absurd name.
	secapMaxVersionToken = 40
)

// Every file-local identifier takes the secap prefix, for the reason #1260's header
// gives: siblings add files to this package concurrently and a branch-overlap check
// does not catch a same-package identifier collision.
const (
	secapTicket         = "2269"
	secapWorkdirName    = "secap-work"
	secapRecordName     = "secap-record.json"
	secapArtifactPrefix = "pyry-2269-capture-*"
	secapModel          = "haiku"
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from every
	// sibling probe's so a record can never be mistaken for one of theirs, and it is
	// the id claude echoes back, which is what makes dropcapRedactor's session_id
	// class able to catch it.
	secapSessionID = "3f8a1d64-2b95-4e07-a1c3-6d94f0e28b71"
)

// secapPartialFlag is the whole point of the spawn. Checked against the OBSERVED
// argv, never assumed: without that check a zero-stream_event capture cannot
// distinguish "claude emits none here" from "the flag never reached the child", and
// only the first is a finding.
const secapPartialFlag = "--include-partial-messages"

var secapArgs = []string{"--model", secapModel, "--dangerously-skip-permissions", secapPartialFlag}

const (
	secapTurnBudget  = 4 * time.Minute
	secapRunExitWait = 30 * time.Second
	// What the turn must leave behind for its own teardown: cancel the runner and
	// wait it out, then collect, marshal, scan and write the record twice. A binary
	// killed by -timeout runs NO cleanups and records nothing, so the tokens buy no
	// evidence — #2262's measured failure, applied here before a token is spent.
	secapDeadlineReserve = 90 * time.Second
	// Below this there is not enough turn left to be worth the tokens: the staged
	// turn is prose plus one echo, which is short, but a cut-off capture publishes a
	// hole rather than a shorter capture.
	secapMinTurnBudget = 60 * time.Second
)

const (
	secapFired            = "fired"
	secapDidNotFire       = "did-not-fire"
	secapInstrumentBroken = "instrument-broken"
)

const (
	secapTerminatedResult = "result"
	secapTerminatedBudget = "budget"
)

// The wire vocabulary this probe keys on, spelled as literals rather than imported:
// nothing in internal/streamsup maps these yet, and a census keyed on production
// constants would agree with a matcher by construction instead of measuring it.
const (
	secapTypeStreamEvent        = "stream_event"
	secapTypeAssistant          = "assistant"
	secapEventMessageStart      = "message_start"
	secapEventContentBlockStart = "content_block_start"
	secapEventContentBlockDelta = "content_block_delta"
	secapDeltaText              = "text_delta"
	secapBlockToolUse           = "tool_use"
	// A content_block_delta whose index no content_block_start opened. Its own
	// bucket, so an orphan can never be counted as a delta of a real block.
	secapBlockUnmapped = "<unmapped>"
	// A line carrying no id under any declared probe path. A value rather than an
	// empty string: "looked and found none" is a different observation from "never
	// looked", and it is the answer #2270's coalescer most needs if it lands on the
	// delta lines.
	secapIDSourceNone = "none"
)

// secapMessageIDPaths is the DECLARED, ordered probe set for the message id. The
// entry records both the id and the path that produced it, so an absence is a
// measured absence over a named set rather than silence — and secapEntry.EventKeys
// is what lets a reader see a fifth spelling this set does not know.
var secapMessageIDPaths = []struct {
	name string
	path []string
}{
	{"event.message.id", []string{"event", "message", "id"}},
	{"event.message_id", []string{"event", "message_id"}},
	{"message.id", []string{"message", "id"}},
	{"message_id", []string{"message_id"}},
}

const secapSpawnShapeDelta = "The YOLO interactive shape plus " + secapPartialFlag + ", which is the " +
	"one flag this capture exists to exercise. See dropcapSpawnShapeDelta for what production's " +
	"non-yolo spawn adds and what that implies for system/init. Production does NOT pass the partial " +
	"flag at all: it rides in this probe's own streamsup.Config.Args, which buildArgs appends after " +
	"its fixed prefix, and internal/streamsup is untouched by this ticket."

const secapLimitations = "One turn, one spawn shape, one claude version, one model (" + secapModel +
	"), one tool (Bash). Cross-version, cross-model and cross-tool stability are UNMEASURED. The " +
	"message-id probe set is DECLARED and ordered (secapMessageIDPaths): an id spelled at a fifth " +
	"path reads as message_id_source=none here, which is why every stream_event entry also carries " +
	"the sorted key names of its `event` object — read those before concluding a line carries no id. " +
	"Block types are resolved from the content_block_start that opened the index, and the index map " +
	"is reset on message_start; orphan_deltas and block_index_reused count the two ways that " +
	"resolution can fail rather than letting either pass silently."

const secapRedactionRationale = "The mechanism is #1260's whole (see dropcapRedactionRationale): a " +
	"fresh empty non-git workdir, a rig-authored prompt, os.Environ() never read into the record, " +
	"dropcapRedactor's declared substitution table over every string that enters the record, and " +
	"dropcapScanner as a fail-closed deny-scan over the marshalled record and every decoded base64 " +
	"payload. This probe sets no environment variable, so env_delta is empty rather than a literal. " +
	"WHAT THIS CAPTURE SPECIFICALLY CARRIES, stated here rather than inherited because the inherited " +
	"sentence was authored for narrower probes: keeping EVERY line of the turn commits two classes " +
	"the sibling captures' filters excluded. First, the assistant's own prose — rig-prompted, on a " +
	"rig-chosen topic. Second, the Bash call's input and its result, which in this staging is an " +
	"echo of a rig-minted token in an empty directory. Both are bounded by construction, with the " +
	"deny-scan behind them as different fabric. " +
	"ALSO KEPT, AND OPERATOR-DERIVED: system/init, whose payload is the operator's local claude " +
	"configuration inventory — MCP server names, the tool list (which includes each MCP server's " +
	"tool names), slash-command names, skill names, the subagent names in `agents`, `plugins`, " +
	"`capabilities`, `output_style`, `apiKeySource` and `permissionMode`. None of it is a credential " +
	"and all of it is kept, because it is part of what a mapping ticket has to read, but it " +
	"describes one machine's setup rather than claude — so whether to publish this record is the " +
	"operator's call, which is the point of saying so here instead of leaving a reader to notice. " +
	"Message ids (msg_…) are claude-minted and KEPT: the delta coalescer keys on them, so whether " +
	"they repeat across lines is the measurement. They stay in the record and out of every log line."

// --- the record --------------------------------------------------------------

// secapEntry is one captured line. The payload half is built by dropcapMakeEntry so
// the base64 arm for invalid UTF-8 is shared rather than re-derived; the rest is
// this ticket's.
//
// events_emitted is the SHIPPED parser's verdict (parseOne), carried as DATA rather
// than as a filter: a stream_event line is not in ignoredLineTypes today, so it
// falls to consumeLine's default and surfaces to the operator as an
// unrecognized_message. Recording the count per line is what lets #2270 see the
// before state it is changing.
type secapEntry struct {
	Index   int    `json:"index"`
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Decoded bool   `json:"decoded"`

	EventType       string   `json:"event_type,omitempty"`
	DeltaType       string   `json:"delta_type,omitempty"`
	BlockIndex      *int     `json:"block_index,omitempty"`
	BlockType       string   `json:"block_type,omitempty"`
	EventKeys       []string `json:"event_keys,omitempty"`
	MessageID       string   `json:"message_id,omitempty"`
	MessageIDSource string   `json:"message_id_source,omitempty"`

	EventsEmitted int `json:"events_emitted"`

	PayloadLenBytesCaptured int    `json:"payload_len_bytes_captured"`
	PayloadLenBytes         int    `json:"payload_len_bytes"`
	PayloadEncoding         string `json:"payload_encoding"`
	Payload                 string `json:"payload,omitempty"`
	PayloadB64              string `json:"payload_b64,omitempty"`
}

// secapWalkResult is everything the ordered walk produces. Split from the record so
// the offline test can assert the walk alone, with no live run and no provenance.
type secapWalkResult struct {
	Entries           []secapEntry
	LineTypes         map[string]int
	EventTypes        map[string]int
	DeltaTypes        map[string]int
	BlockTypes        map[string]int
	DeltasByBlockType map[string]map[string]int
	IDSources         map[string]int
	MessageIDs        []string
	StreamEventLines  int
	AssistantLines    int
	OrphanDeltas      int
	BlockIndexReused  int
	UndecodedLines    int
}

type secapRecord struct {
	Ticket          string   `json:"ticket"`
	ClaudeVersion   string   `json:"claude_version"`
	CapturedAt      string   `json:"captured_at"`
	IsCapture       bool     `json:"is_capture"`
	Model           string   `json:"model"`
	SpawnShape      []string `json:"spawn_shape"`
	SpawnShapeDelta string   `json:"spawn_shape_delta"`
	EnvDelta        []string `json:"env_delta"`
	Workdir         string   `json:"workdir"`
	Prompt          string   `json:"prompt"`

	Outcome       string  `json:"outcome"`
	OutcomeDetail string  `json:"outcome_detail"`
	TerminatedOn  string  `json:"terminated_on"`
	TurnSeconds   float64 `json:"turn_seconds"`

	// Observed from the runner's own "spawning claude" record, never transcribed.
	// The one field that separates a finding about claude from an instrument fault.
	PartialFlagInArgv bool   `json:"partial_messages_flag_in_argv"`
	StagingVerdict    string `json:"staging_verdict"`

	LinesCaptured    int          `json:"lines_captured"`
	StreamEventLines int          `json:"stream_event_lines"`
	AssistantLines   int          `json:"assistant_lines"`
	Lines            []secapEntry `json:"lines"`

	LineTypeCensus    map[string]int            `json:"line_type_census"`
	EventTypeCensus   map[string]int            `json:"event_type_census"`
	DeltaTypeCensus   map[string]int            `json:"delta_type_census"`
	BlockTypeCensus   map[string]int            `json:"block_type_census"`
	DeltasByBlockType map[string]map[string]int `json:"deltas_by_block_type"`
	IDSourceCensus    map[string]int            `json:"message_id_source_census"`
	MessageIDs        []string                  `json:"message_ids_observed"`
	OrphanDeltas      int                       `json:"orphan_deltas"`
	BlockIndexReused  int                       `json:"block_index_reused"`
	UndecodedLines    int                       `json:"undecoded_lines"`

	Redaction             []dropcapSubstitution `json:"redaction"`
	RedactionRationale    string                `json:"redaction_rationale"`
	CredentialScanApplied map[string]bool       `json:"credential_scan_applied"`
	CredentialScanSkipped []string              `json:"credential_scan_skipped"`

	Limitations string `json:"limitations"`

	LinesDroppedOverCap    int `json:"lines_dropped_over_cap"`
	BytesDroppedOverCap    int `json:"bytes_dropped_over_cap"`
	PartialsDropped        int `json:"partials_dropped"`
	BlankLines             int `json:"blank_lines"`
	UnterminatedPartialLen int `json:"unterminated_partial_len"`
}

func (rec *secapRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
}

// --- the walk -----------------------------------------------------------------

// secapWalk classifies every captured line in arrival order and fills every census.
//
// Order is the whole product here, so the walk is stateful in exactly one way: a
// map of content-block index to the block type that content_block_start opened,
// reset on each message_start. That is what resolves a content_block_delta to the
// block it belongs to — the measurement AC 3's tool_use arm turns on — without
// presuming what a tool_use block's delta type is called.
func secapWalk(t *testing.T, lines []dropcapCaptured, red *dropcapRedactor) secapWalkResult {
	t.Helper()
	out := secapWalkResult{
		Entries:           []secapEntry{},
		LineTypes:         map[string]int{},
		EventTypes:        map[string]int{},
		DeltaTypes:        map[string]int{},
		BlockTypes:        map[string]int{},
		DeltasByBlockType: map[string]map[string]int{},
		IDSources:         map[string]int{},
		MessageIDs:        []string{},
	}
	// Content-block index -> the block type the content_block_start at that index
	// opened. RESET on message_start, because indices are per-message and restart at
	// 0: a walk that never reset would resolve the second message's block 0 to the
	// first message's type and mislabel every delta of a multi-message turn.
	blocks := map[int]string{}
	seenID := map[string]bool{}

	for _, c := range lines {
		// The payload half, shared with #1260 so the base64 arm for invalid UTF-8 is
		// not re-derived. Its reason field is spent on this ticket's own fields.
		payload := dropcapMakeEntry(c, "", red)
		e := secapEntry{
			Index:                   payload.Index,
			Type:                    payload.Type,
			Subtype:                 payload.Subtype,
			Decoded:                 c.Decoded,
			EventsEmitted:           len(parseOne(t, string(c.Raw))),
			PayloadLenBytesCaptured: payload.PayloadLenBytesCaptured,
			PayloadLenBytes:         payload.PayloadLenBytes,
			PayloadEncoding:         payload.PayloadEncoding,
			Payload:                 payload.Payload,
			PayloadB64:              payload.PayloadB64,
		}

		switch {
		case !c.Decoded:
			out.UndecodedLines++
		default:
			out.LineTypes[secapLineKey(c)]++
			e.MessageID, e.MessageIDSource = secapMessageID(c.Raw)
			out.IDSources[e.MessageIDSource]++
			if e.MessageID != "" && !seenID[e.MessageID] {
				seenID[e.MessageID] = true
				out.MessageIDs = append(out.MessageIDs, e.MessageID)
			}
			switch c.Type {
			case secapTypeStreamEvent:
				out.StreamEventLines++
				secapWalkEvent(c.Raw, &e, &out, blocks)
			case secapTypeAssistant:
				out.AssistantLines++
			}
		}
		out.Entries = append(out.Entries, e)
	}
	return out
}

// secapWalkEvent fills one stream_event entry's inner fields and folds it into the
// censuses. blocks is mutated in place — message_start clears it rather than
// replacing it, so the caller's map stays the live one.
func secapWalkEvent(raw []byte, e *secapEntry, out *secapWalkResult, blocks map[int]string) {
	var line struct {
		Event json.RawMessage `json:"event"`
	}
	if json.Unmarshal(raw, &line) != nil || len(line.Event) == 0 {
		return
	}
	var ev struct {
		Type         string          `json:"type"`
		Index        *int            `json:"index"`
		Delta        json.RawMessage `json:"delta"`
		ContentBlock json.RawMessage `json:"content_block"`
	}
	if json.Unmarshal(line.Event, &ev) != nil {
		return
	}

	e.EventKeys = secapEventKeys(line.Event)
	e.EventType = ev.Type
	e.BlockIndex = ev.Index
	out.EventTypes[ev.Type]++

	switch ev.Type {
	case secapEventMessageStart:
		for k := range blocks {
			delete(blocks, k)
		}
	case secapEventContentBlockStart:
		blockType := secapTypeField(ev.ContentBlock)
		e.BlockType = blockType
		out.BlockTypes[blockType]++
		if ev.Index != nil {
			if _, live := blocks[*ev.Index]; live {
				out.BlockIndexReused++
			}
			blocks[*ev.Index] = blockType
		}
	case secapEventContentBlockDelta:
		deltaType := secapTypeField(ev.Delta)
		e.DeltaType = deltaType
		out.DeltaTypes[deltaType]++

		blockType := secapBlockUnmapped
		if ev.Index != nil {
			if mapped, ok := blocks[*ev.Index]; ok {
				blockType = mapped
			}
		}
		if blockType == secapBlockUnmapped {
			out.OrphanDeltas++
		}
		e.BlockType = blockType
		if out.DeltasByBlockType[blockType] == nil {
			out.DeltasByBlockType[blockType] = map[string]int{}
		}
		out.DeltasByBlockType[blockType][deltaType]++
	}
}

// secapLineKey is the line-type census key: the top-level type, with the subtype
// appended where there is one, since that is where a system line says what it is.
func secapLineKey(c dropcapCaptured) string {
	if c.Subtype == "" {
		return c.Type
	}
	return c.Type + "/" + c.Subtype
}

// secapTypeField reads the `type` of a nested object — a delta's or a content
// block's. An object without one is recorded under the empty key rather than
// silently folded into a neighbour.
func secapTypeField(raw json.RawMessage) string {
	var probe struct {
		Type string `json:"type"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &probe) != nil {
		return ""
	}
	return probe.Type
}

// secapMessageID runs the declared probe set over one line's raw bytes and returns
// the first hit with the path that produced it.
func secapMessageID(raw []byte) (id, source string) {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return "", secapIDSourceNone
	}
	for _, p := range secapMessageIDPaths {
		if v, ok := secapJSONString(root, p.path); ok && v != "" {
			return v, p.name
		}
	}
	return "", secapIDSourceNone
}

// secapJSONString walks a decoded object down a path and returns the string at the
// end, if the whole path resolves to one.
func secapJSONString(root map[string]any, path []string) (string, bool) {
	var cur any = root
	for _, seg := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = obj[seg]; !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}

// secapEventKeys returns the sorted top-level key names of a stream_event's `event`
// object. KEY NAMES ONLY — a structural vocabulary, never a value.
func secapEventKeys(event json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if len(event) == 0 || json.Unmarshal(event, &obj) != nil {
		return nil
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- the verdict and the gate --------------------------------------------------

// stagingVerdict names WHICH of five readings a thin capture is, because only some
// are findings about pyry's surface. The sibling probes' lesson exactly: a rig that
// failed to stage the thing it meant to measure must never read as a measurement,
// or the next reader loosens a classifier to fix a staging bug.
func (rec *secapRecord) stagingVerdict() string {
	switch {
	case !rec.PartialFlagInArgv:
		return fmt.Sprintf("the observed argv carried no %s, so claude was never asked for partial "+
			"messages and nothing here measures that surface. This is an INSTRUMENT fault: read "+
			"spawn_shape, not the censuses — the staging failed before claude was exercised",
			secapPartialFlag)
	case rec.StreamEventLines == 0:
		return fmt.Sprintf("claude was spawned WITH %s — the flag is in the observed argv — and the "+
			"turn produced zero %s lines out of %d captured. The staging worked, so this is a "+
			"finding about the surface and it is to be routed back, not fixed by loosening a census",
			secapPartialFlag, secapTypeStreamEvent, rec.LinesCaptured)
	case rec.DeltaTypeCensus[secapDeltaText] == 0:
		return fmt.Sprintf("%d %s line(s) arrived but not one carried a %s, so no assistant text "+
			"streamed at all (event types %v). The staging failed: the prompt or the model, not the "+
			"surface", rec.StreamEventLines, secapTypeStreamEvent, secapDeltaText, rec.EventTypeCensus)
	case rec.BlockTypeCensus[secapBlockToolUse] == 0:
		return fmt.Sprintf("assistant text streamed but no %s block was ever opened (block types "+
			"%v), so the turn made no tool call. The staging failed: claude declined or reworded the "+
			"command, and this capture cannot say what a partial tool input looks like",
			secapBlockToolUse, rec.BlockTypeCensus)
	case len(rec.DeltasByBlockType[secapBlockToolUse]) == 0:
		return fmt.Sprintf("a %s block was opened and carried NO deltas (deltas by block %v). The "+
			"staging worked, so this is a finding about the surface — the tool input does not stream "+
			"here, and #2270 has to consume a block whose input arrives whole rather than in pieces",
			secapBlockToolUse, rec.DeltasByBlockType)
	default:
		return fmt.Sprintf("a complete capture: %d %s line(s) and %d assistant line(s), deltas by "+
			"block %v. Every question #2270 turns on is answerable from the committed lines, and the "+
			"only finding about the surface left to make is the mapping itself",
			rec.StreamEventLines, secapTypeStreamEvent, rec.AssistantLines, rec.DeltasByBlockType)
	}
}

// fixtureWorthy answers whether this record may be promoted in-repo, and names the
// reason when it may not. Every rejection is a case where the record is still
// valuable evidence — it is written to the artifact directory either way — but
// would be a lie as the committed proof.
func (rec *secapRecord) fixtureWorthy() (string, bool) {
	if rec.Outcome != secapFired {
		return fmt.Sprintf("outcome=%s", rec.Outcome), false
	}
	if !rec.PartialFlagInArgv {
		return fmt.Sprintf("the observed argv carried no %s, so this record describes the ordinary "+
			"surface and vouches for nothing about partial messages", secapPartialFlag), false
	}
	if rec.StreamEventLines == 0 {
		return fmt.Sprintf("zero %s lines — a vacuous fixture every assertion would pass against "+
			"without reading a byte claude sent", secapTypeStreamEvent), false
	}
	if rec.DeltaTypeCensus[secapDeltaText] == 0 {
		return fmt.Sprintf("no %s in any delta (delta types %v), so the fixture holds no streamed "+
			"assistant text for the mapping to read", secapDeltaText, rec.DeltaTypeCensus), false
	}
	if len(rec.DeltasByBlockType[secapBlockToolUse]) == 0 {
		return fmt.Sprintf("no delta belonged to a %s block (deltas by block %v), so the fixture "+
			"cannot show what crosses the wire while a tool input is still partial",
			secapBlockToolUse, rec.DeltasByBlockType), false
	}
	// THIS ARM IS THIS PROBE'S OWN. Every sibling capture filters to a handful of
	// lines and can never reach dropcapMaxCaptureBytes; this one keeps every line of
	// a turn under a flag that multiplies the count. Past the cap whole lines are
	// dropped and only counted, so a promoted record would carry a hole in the middle
	// of a delta run — unmappable by #2270, and indistinguishable from a complete
	// capture unless the promotion refuses it here.
	if rec.LinesDroppedOverCap > 0 || rec.PartialsDropped > 0 || rec.UnterminatedPartialLen > 0 {
		return fmt.Sprintf("the recorder's caps dropped part of the turn: %d line(s) over the byte "+
			"cap (%d bytes), %d discarded partial(s), %d byte(s) of unterminated tail. A capture "+
			"with a hole in it reads as complete and cannot be mapped — re-run, and raise "+
			"dropcapMaxCaptureBytes if the turn is genuinely this large",
			rec.LinesDroppedOverCap, rec.BytesDroppedOverCap, rec.PartialsDropped,
			rec.UnterminatedPartialLen), false
	}
	if _, err := secapFixturePath(rec.ClaudeVersion); err != nil {
		return err.Error(), false
	}
	// dropcapMakeEntry emits base64 with an EMPTY payload for a line that is not
	// valid UTF-8. Promoting one as a stream_event would hand #2270 a fixture with no
	// readable line to replay. A non-quarry line in that state costs the reader
	// nothing and must not block an otherwise good record.
	for _, l := range rec.Lines {
		if l.Type == secapTypeStreamEvent && l.PayloadEncoding != dropcapEncodingJSONString {
			return fmt.Sprintf("the %s line at index %d is encoded %q, so it carries no readable "+
				"payload for the mapping to be written against", secapTypeStreamEvent, l.Index,
				l.PayloadEncoding), false
		}
	}
	return "", true
}

// secapFixturePath composes the fixture name from the version claude actually
// printed, and refuses any token that cannot safely be one.
//
// THIS IS A TRUST BOUNDARY, not a tidiness check: probeClaudeVersion reads the
// version from a SUBPROCESS, so the string is subprocess-controlled data flowing
// into a write path. The shape check admits no separator and requires a leading
// digit, so every traversal and absolute form is rejected and an "<unavailable: …>"
// fails at the first character rather than naming a fixture that vouches for
// nothing. The fixture write uses ONLY this function's returned path; the record's
// raw version field composes a path nowhere.
func secapFixturePath(version string) (string, error) {
	token, _, _ := strings.Cut(strings.TrimSpace(version), " ")
	if token == "" {
		return "", fmt.Errorf("claude_version is empty, so the fixture could not be named for the " +
			"release it vouches for")
	}
	if len(token) > secapMaxVersionToken {
		return "", fmt.Errorf("claude_version's leading token is %d bytes, over the %d cap",
			len(token), secapMaxVersionToken)
	}
	if token[0] < '0' || token[0] > '9' {
		return "", fmt.Errorf("claude_version %q does not start with a digit, so it is not a version "+
			"(an unreadable one is recorded as \"<unavailable: …>\", and a traversal starts with a "+
			"dot or a separator)", token)
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			c == '.' || c == '-'
		if !ok {
			return "", fmt.Errorf("claude_version %q carries a byte that may not appear in a filename",
				token)
		}
	}
	return secapFixturePrefix + token + ".json", nil
}

// complete reports whether the capture answered every question it was staged for.
// It is stagingVerdict's default arm as a predicate: the arms above that default
// are exactly the negations of these conjuncts, in this order, so the two must move
// together — TestSecapStagingVerdictSeparatesRigFailureFromFinding asserts they
// agree on every row rather than leaving the pairing to a reader's care.
func (rec *secapRecord) complete() bool {
	return rec.PartialFlagInArgv &&
		rec.StreamEventLines > 0 &&
		rec.DeltaTypeCensus[secapDeltaText] > 0 &&
		rec.BlockTypeCensus[secapBlockToolUse] > 0 &&
		len(rec.DeltasByBlockType[secapBlockToolUse]) > 0
}

// secapTurnBudgetWithin sizes the turn against the TEST BINARY'S OWN DEADLINE, not
// against the turn alone — #2262's measured failure, where a capture sized against
// itself starved a sibling under the dispatcher's shared -timeout and the binary's
// kill ran no cleanups, so the tokens were spent and the record was never written.
func secapTurnBudgetWithin(t *testing.T) (time.Duration, string) {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		// No -timeout at all, so nothing to be starved by and nothing to reserve for.
		return secapTurnBudget, ""
	}
	return secapBudgetFor(time.Until(deadline))
}

// secapBudgetFor is the arithmetic half, split out because a *testing.T's deadline
// comes from the go command and cannot be set from inside a test.
func secapBudgetFor(remaining time.Duration) (time.Duration, string) {
	spendable := remaining - secapDeadlineReserve
	if spendable < secapMinTurnBudget {
		return 0, fmt.Sprintf("the test binary's deadline leaves %s, and this capture needs a %s "+
			"turn plus %s to write its record; a binary killed by -timeout runs no cleanups and "+
			"records nothing, so the tokens would buy no evidence. Re-run with a longer -timeout",
			remaining.Round(time.Second), secapMinTurnBudget, secapDeadlineReserve)
	}
	return min(secapTurnBudget, spendable), ""
}

// secapPrompt stages the one turn AC 1 asks for: several blocks of assistant text
// and one tool call, in one turn.
//
// Text before AND after the call is what produces both — it is also what reproduces
// the two-`assistant`-lines-for-one-reply shape the ticket names as an open
// question, and it puts an `assistant` line between two runs of deltas so a reader
// can see where each one falls. The command is an echo of a rig-minted token in a
// fresh empty non-git directory, so the YOLO spawn shape's blast radius is nil and
// the only thing the tool result can carry is the nonce, which dropcapRedactor's
// prompt_nonce class substitutes.
func secapPrompt(nonce int64) string {
	return fmt.Sprintf("Do these three things in order and nothing else. First, write two short "+
		"sentences about the colour blue. Second, use the Bash tool exactly once to run this "+
		"command verbatim: echo pyry-%d. Third, write one short closing sentence. Do not read or "+
		"write any file, do not use any other tool, do not chain commands with && or ;, do not add "+
		"flags or redirections, and do not comment on the task. run=%d", nonce, nonce)
}

// TestRealClaude_StreamEventCapture drives the turn and writes the record.
//
// Ordering below is load-bearing in one place: newDropcapScanner reads
// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY via os.Getenv AS DENY NEEDLES, and
// WithWorktreeAuthenticated is what re-pins them into this process's environment.
// Building the scanner first yields EMPTY needles, which dropcapScanner.scan reports
// as notApplied — silently skipped, not fatal. The credential net would be off while
// every message still read green, so the scanner is built after the auth helper and
// the skipped classes ship in the record.
func TestRealClaude_StreamEventCapture(t *testing.T) {
	// THE GATE IS THE FIXTURE'S ABSENCE, which is #2089's break from the env-gated
	// siblings. Under the sibling shape `make e2e-realclaude` never sets the
	// variable, so the probe skips on the ENV check before it ever reaches the
	// credential check, the live gate passes vacuously and the fixture never lands —
	// CLAUDE.md § Testing's #1763 failure exactly, where a green gate and a spent
	// budget look identical whether the bytes landed or not. The glob admits ANY
	// version, so a claude release between authoring and the gate run cannot leave
	// this ticket with nothing promoted; the variable survives only as a FORCE.
	force := os.Getenv(secapEnableEnv) == "1"
	if existing, _ := filepath.Glob(secapFixtureGlob); len(existing) > 0 && !force {
		t.Skipf("#2269 stream_event capture: %v already exists, so there is nothing to capture and "+
			"this costs no claude turn.\n"+
			"Force a re-capture (a new claude version, or a suspected shape change) with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 15m -v \\\n"+
			"    -run '^TestRealClaude_StreamEventCapture$' ./internal/e2e/realclaude/",
			existing, secapEnableEnv)
	}

	// Sized against the binary's deadline before a single token is spent.
	budget, tooLate := secapTurnBudgetWithin(t)
	if tooLate != "" {
		t.Skipf("#2269 stream_event capture: %s", tooLate)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner

	// Deliberately NOT t.TempDir(): #2229's capture fired inside the dispatcher's
	// gate-only worktree and lost its in-repo fixture when that worktree was
	// discarded. This directory is outside every worktree and is the copy that
	// survives such a run.
	artifactDir, err := os.MkdirTemp("", secapArtifactPrefix)
	if err != nil {
		t.Fatalf("#2269: create artifact dir: %v", err)
	}

	// A fresh EMPTY directory, deliberately not a git repo: no branch names and no
	// file contents can reach a payload. This capture keeps every line, so the
	// by-construction bound matters more here than in the filtered siblings.
	workdir := filepath.Join(home, secapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2269: create workdir: %v", err)
	}
	nonce := time.Now().UnixNano()

	// The empty slot is fifoPath: this probe holds no FIFO. dropcapRedactor.add
	// guards "" — strings.ReplaceAll(s, "", x) would otherwise insert x between
	// every character.
	red := newDropcapRedactor(home, artifactDir, workdir, "", secapSessionID, nonce)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#2269 capture artifacts: %s", red.str(artifactDir))

	rec := &secapRecord{
		Ticket:        secapTicket,
		ClaudeVersion: probeClaudeVersion(claudeBin),
		CapturedAt:    time.Now().Format(time.RFC3339),
		IsCapture:     true,
		Model:         secapModel,
		// EMPTY, not a literal: this probe sets no environment variable, and
		// os.Environ() is never harvested into the record (#1223's rule).
		EnvDelta:              []string{},
		SpawnShapeDelta:       secapSpawnShapeDelta,
		Workdir:               red.str(workdir),
		Lines:                 []secapEntry{},
		RedactionRationale:    secapRedactionRationale,
		CredentialScanApplied: scanner.applied(),
		CredentialScanSkipped: []string{},
		Limitations:           secapLimitations,
	}
	rec.set(secapInstrumentBroken, "did not reach a classification point")

	// Registered before anything below can fail, so a structural t.Fatalf still
	// leaves the evidence on disk — #1260's ordering.
	t.Cleanup(func() { secapWriteRecord(t, artifactDir, red, scanner, rec) })

	recorder := newDropcapRecorder()
	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: secapSessionID,
		Args:      secapArgs,
		Stdout:    recorder,
		Logger:    slog.New(argvHandler),
	})
	if err != nil {
		rec.set(secapInstrumentBroken, "streamsup.New failed, so no claude was ever spawned: %v",
			red.str(err.Error()))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = runner.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(secapRunExitWait):
			t.Errorf("#2269: streamsup.Run did not return within %s of cancel", secapRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(secapInstrumentBroken, "no live child within %s: claude never spawned, so nothing "+
			"was on the wire to capture", dropcapSpawnWait)
		return
	}

	prompt := secapPrompt(nonce)
	rec.Prompt = red.str(prompt)
	turnStart := time.Now()
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		rec.set(secapInstrumentBroken, "writing the turn envelope failed, so no turn was ever "+
			"driven: %v", red.str(err.Error()))
		return
	}

	select {
	case <-recorder.resultSeen:
		rec.TerminatedOn = secapTerminatedResult
	case <-time.After(budget):
		rec.TerminatedOn = secapTerminatedBudget
	}
	rec.TurnSeconds = time.Since(turnStart).Seconds()

	// OBSERVED from the runner's own "spawning claude" record, never transcribed:
	// this is the field that separates "claude emits none here" from "the flag never
	// reached the child", and only the first is a finding.
	observed := argv()
	rec.PartialFlagInArgv = secapContains(observed, secapPartialFlag)
	rec.SpawnShape = red.strs(observed)

	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	walk := secapWalk(t, lines, red)
	rec.Lines = walk.Entries
	rec.StreamEventLines = walk.StreamEventLines
	rec.AssistantLines = walk.AssistantLines
	rec.LineTypeCensus = walk.LineTypes
	rec.EventTypeCensus = walk.EventTypes
	rec.DeltaTypeCensus = walk.DeltaTypes
	rec.BlockTypeCensus = walk.BlockTypes
	rec.DeltasByBlockType = walk.DeltasByBlockType
	rec.IDSourceCensus = walk.IDSources
	rec.MessageIDs = walk.MessageIDs
	rec.OrphanDeltas = walk.OrphanDeltas
	rec.BlockIndexReused = walk.BlockIndexReused
	rec.UndecodedLines = walk.UndecodedLines

	rec.StagingVerdict = rec.stagingVerdict()
	switch {
	case !rec.PartialFlagInArgv:
		rec.set(secapInstrumentBroken, "%s", rec.StagingVerdict)
	case rec.complete():
		rec.set(secapFired, "%s", rec.StagingVerdict)
	default:
		rec.set(secapDidNotFire, "%s", rec.StagingVerdict)
	}

	// --- AC 3 -----------------------------------------------------------------
	// Every message below reports counts and census KEYS only — type names, a closed
	// structural vocabulary, never a payload. The lines themselves are in the record
	// the cleanup has already written; putting claude's bytes in CI output is
	// precisely the exposure the deny-scan exists to prevent.
	if !rec.PartialFlagInArgv {
		t.Fatalf("#2269: the observed argv carried no %s (%d line(s) captured). This is an "+
			"INSTRUMENT fault and NOT a measurement of the surface — read spawn_shape in the record "+
			"before reading any census below it.\n  staging: %s",
			secapPartialFlag, rec.LinesCaptured, rec.StagingVerdict)
	}
	if rec.StreamEventLines == 0 {
		t.Fatalf("#2269: the turn recorded ZERO %s lines out of %d captured (terminated_on=%s, "+
			"turn=%.1fs). A capture recording none is vacuous.\n"+
			"  line types: %v; event types: %v\n  staging: %s",
			secapTypeStreamEvent, rec.LinesCaptured, rec.TerminatedOn, rec.TurnSeconds,
			rec.LineTypeCensus, rec.EventTypeCensus, rec.StagingVerdict)
	}
	if rec.DeltaTypeCensus[secapDeltaText] == 0 {
		t.Fatalf("#2269: %d %s line(s) arrived and NOT ONE carried a %s, so the fixture holds no "+
			"streamed assistant text for #2270 to map.\n"+
			"  line types: %v; event types: %v; delta types: %v; block types: %v\n  staging: %s",
			rec.StreamEventLines, secapTypeStreamEvent, secapDeltaText, rec.LineTypeCensus,
			rec.EventTypeCensus, rec.DeltaTypeCensus, rec.BlockTypeCensus, rec.StagingVerdict)
	}
	if len(rec.DeltasByBlockType[secapBlockToolUse]) == 0 {
		t.Fatalf("#2269: no delta belonged to a %s block, so the capture cannot show what crosses "+
			"the wire while a tool input is still partial JSON — the question #2270 has to answer "+
			"without letting a half-built tool call reach the client.\n"+
			"  block types: %v; deltas by block: %v; delta types: %v; orphan deltas: %d\n"+
			"  staging: %s",
			secapBlockToolUse, rec.BlockTypeCensus, rec.DeltasByBlockType, rec.DeltaTypeCensus,
			rec.OrphanDeltas, rec.StagingVerdict)
	}
}

// secapWriteRecord marshals, deny-scans and writes — twice. #1260's fail-closed rule
// verbatim: on a hit NOTHING is written and the message names the CLASS only, never
// the matched value.
//
// The double write is #2262's lesson, applied by default rather than as optional
// hardening: the artifact directory outlives a discarded worktree, so a live-gate
// run whose in-repo write dies with the dispatcher's checkout still leaves the bytes
// somewhere a repair commit can find them.
func secapWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *secapRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2269: marshal record: %v", err)
		return
	}
	hits, notApplied := scanner.scan(blob)
	// A base64 payload hides its bytes from a scan of the marshalled record, so the
	// decoded bytes are scanned too.
	for i, l := range rec.Lines {
		if l.PayloadB64 == "" {
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(l.PayloadB64)
		if derr != nil {
			t.Errorf("#2269: line %d: decode base64 payload for the scan: %v", i, derr)
			return
		}
		if h, _ := scanner.scan(decoded); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("#2269: deny-scan found %d denied class(es) still present in the record: %v\n"+
			"NOTHING was written — not the record, not the fixture. Extend dropcapRedactor's table "+
			"with the named class and re-run the capture. The offending value is deliberately not "+
			"printed: putting it in CI output is exactly the exposure this scan exists to prevent",
			len(hits), hits)
	}
	rec.CredentialScanSkipped = notApplied

	// Re-marshal so credential_scan_skipped ships in the written bytes. Its value is
	// a list of CLASS NAMES the scan could not apply, which is the one thing that
	// makes a silently-off credential net visible after the fact.
	blob, err = json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2269: re-marshal record: %v", err)
		return
	}

	path := filepath.Join(dir, secapRecordName)
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2269: write record %s: %v", red.str(path), err)
		return
	}
	// message_ids_observed is deliberately ABSENT from this line: it is the one
	// census-adjacent field carrying identifiers rather than type names, and it stays
	// in the record rather than in CI output.
	t.Logf("#2269 outcome=%s terminated_on=%s turn=%.1fs flag_in_argv=%v captured=%d "+
		"stream_event=%d assistant=%d line_types=%v event_types=%v delta_types=%v block_types=%v "+
		"deltas_by_block=%v id_sources=%v orphan_deltas=%d reused=%d undecoded=%d caps=%d/%d/%d/%d "+
		"scan_not_applied=%v\n  record: %s\n  staging: %s",
		rec.Outcome, rec.TerminatedOn, rec.TurnSeconds, rec.PartialFlagInArgv, rec.LinesCaptured,
		rec.StreamEventLines, rec.AssistantLines, rec.LineTypeCensus, rec.EventTypeCensus,
		rec.DeltaTypeCensus, rec.BlockTypeCensus, rec.DeltasByBlockType, rec.IDSourceCensus,
		rec.OrphanDeltas, rec.BlockIndexReused, rec.UndecodedLines, rec.LinesDroppedOverCap,
		rec.BytesDroppedOverCap, rec.PartialsDropped, rec.UnterminatedPartialLen, notApplied,
		red.str(path), rec.StagingVerdict)

	// The same deny-scanned bytes, promoted in-repo so the run that produced them is
	// the run that lands them. A capture that still needs a human to copy a file out
	// of a tempdir is a capture #1763 says will not land.
	if reason, ok := rec.fixtureWorthy(); !ok {
		t.Logf("#2269: NOT promoted — %s. The record above is the evidence; read it, then re-run "+
			"or route the finding back", reason)
		return
	}
	// The write uses ONLY this path. rec.ClaudeVersion composes a path nowhere else:
	// it is read from a SUBPROCESS, and secapFixturePath is the boundary that keeps
	// a version string from naming a file outside testdata/.
	fixturePath, err := secapFixturePath(rec.ClaudeVersion)
	if err != nil {
		t.Logf("#2269: NOT promoted — %v", err)
		return
	}
	if err := os.WriteFile(fixturePath, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2269: write fixture %s: %v", fixturePath, red.str(err.Error()))
		return
	}
	t.Logf("#2269: FIXTURE WRITTEN to %s (%d lines, %d %s, deltas by block %v).\n"+
		"  Commit it — `git add %s` — in THIS ticket's PR. If this run was the dispatcher's "+
		"real-claude gate rather than a builder's branch checkout, the in-repo write above dies "+
		"with that worktree (#2229) and the surviving copy is the artifact record named earlier.",
		fixturePath, rec.LinesCaptured, rec.StreamEventLines, secapTypeStreamEvent,
		rec.DeltasByBlockType, fixturePath)
}

func secapContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// --- offline self-checks -------------------------------------------------------

// secapLine builds one dropcapCaptured from a JSON literal, the way the recorder
// would have. Offline only.
func secapLine(t *testing.T, index int, raw string) dropcapCaptured {
	t.Helper()
	var sl struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	decoded := json.Unmarshal([]byte(raw), &sl) == nil
	return dropcapCaptured{
		Index:   index,
		Raw:     []byte(raw),
		Type:    sl.Type,
		Subtype: sl.Subtype,
		Decoded: decoded,
	}
}

// secapSyntheticTurn is a hand-authored miniature of the shape the live turn is
// staged to produce: a message_start carrying an id, a text block with one delta,
// a tool_use block with one delta, and the `assistant` line that settles them.
//
// Hand-authored is the point. The walk must be provable without a live claude, and
// a fixture-derived input would make this test agree with whatever the capture
// happened to record instead of with the shape the walk claims to resolve.
func secapSyntheticTurn(t *testing.T) []dropcapCaptured {
	t.Helper()
	raws := []string{
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_a","role":"assistant"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"Bash"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"c\":"}}}`,
		`{"type":"assistant","message":{"id":"msg_a","content":[{"type":"text","text":"hi"}]}}`,
		// An orphan: a delta at an index no start opened.
		`{"type":"stream_event","event":{"type":"content_block_delta","index":7,"delta":{"type":"text_delta","text":"x"}}}`,
		`{"type":"result","subtype":"success"}`,
	}
	out := make([]dropcapCaptured, 0, len(raws))
	for i, raw := range raws {
		out = append(out, secapLine(t, i, raw))
	}
	return out
}

// secapTestRedactor is a redactor with a table that cannot match anything in the
// synthetic lines, so the offline walk tests assert the walk and not redaction.
func secapTestRedactor() *dropcapRedactor {
	return newDropcapRedactor("", "", "", "", "", 1)
}

// TestSecapWalkResolvesDeltasToTheirBlock runs offline. It is the load-bearing
// guard on the whole record: every census the gate reads and every field #2270
// navigates by comes out of this one function.
func TestSecapWalkResolvesDeltasToTheirBlock(t *testing.T) {
	t.Parallel()
	got := secapWalk(t, secapSyntheticTurn(t), secapTestRedactor())

	if len(got.Entries) != 9 {
		t.Fatalf("walk kept %d entries, want 9: every line of the turn is the deliverable, and a "+
			"filtered walk would drop exactly the adjacency #2270 reads", len(got.Entries))
	}
	if got.StreamEventLines != 6 || got.AssistantLines != 1 {
		t.Errorf("stream_event=%d assistant=%d, want 6 and 1", got.StreamEventLines, got.AssistantLines)
	}

	// The measurement AC 3's tool_use arm turns on: a delta resolved to the block
	// its content_block_start opened, WITHOUT presuming the delta's name.
	wantByBlock := map[string]map[string]int{
		"text":             {secapDeltaText: 1},
		secapBlockToolUse:  {"input_json_delta": 1},
		secapBlockUnmapped: {secapDeltaText: 1},
	}
	if diff := fmt.Sprint(got.DeltasByBlockType); diff != fmt.Sprint(wantByBlock) {
		t.Errorf("deltas_by_block_type = %v, want %v", got.DeltasByBlockType, wantByBlock)
	}
	if got.OrphanDeltas != 1 {
		t.Errorf("orphan_deltas = %d, want 1: a delta at an index no start opened must never be "+
			"counted as a delta of a real block", got.OrphanDeltas)
	}

	if got.EventTypes[secapEventContentBlockDelta] != 3 {
		t.Errorf("event_type_census[%s] = %d, want 3", secapEventContentBlockDelta,
			got.EventTypes[secapEventContentBlockDelta])
	}
	if got.DeltaTypes[secapDeltaText] != 2 {
		t.Errorf("delta_type_census[%s] = %d, want 2", secapDeltaText, got.DeltaTypes[secapDeltaText])
	}
	if got.BlockTypes[secapBlockToolUse] != 1 {
		t.Errorf("block_type_census[%s] = %d, want 1", secapBlockToolUse,
			got.BlockTypes[secapBlockToolUse])
	}
	if got.LineTypes["system/init"] != 1 || got.LineTypes["result/success"] != 1 {
		t.Errorf("line_type_census = %v, want system/init and result/success present", got.LineTypes)
	}

	// The id question the coalescer turns on: message_start carries it, the
	// `assistant` line carries it, and a content_block_delta does NOT.
	if len(got.MessageIDs) != 1 || got.MessageIDs[0] != "msg_a" {
		t.Errorf("message_ids_observed = %v, want exactly [msg_a] deduped in order", got.MessageIDs)
	}
	if got.IDSources["event.message.id"] != 1 || got.IDSources["message.id"] != 1 {
		t.Errorf("message_id_source_census = %v, want one event.message.id and one message.id",
			got.IDSources)
	}
	for _, e := range got.Entries {
		if e.EventType != secapEventContentBlockDelta {
			continue
		}
		if e.MessageIDSource != secapIDSourceNone {
			t.Errorf("entry %d is a %s carrying an id from %q; the record must show plainly that a "+
				"delta line carries none, since that is what the coalescer's keying turns on",
				e.Index, e.EventType, e.MessageIDSource)
		}
	}

	// Key names are what let a reader see a spelling the declared probe set does
	// not know, so an absent key list would make every `none` unreadable.
	for _, e := range got.Entries {
		if e.Type == secapTypeStreamEvent && len(e.EventKeys) == 0 {
			t.Errorf("entry %d is a stream_event with no event_keys recorded", e.Index)
		}
	}
}

// TestSecapWalkResetsTheBlockMapPerMessage runs offline. Content-block indices are
// per-message and restart at 0, so a walk that never reset would resolve the second
// message's block 0 to the FIRST message's type — silently mislabelling every delta
// of a multi-message turn, which is exactly what this turn is staged to produce.
func TestSecapWalkResetsTheBlockMapPerMessage(t *testing.T) {
	t.Parallel()
	raws := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_a"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"Bash"}}}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_b"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}}`,
	}
	lines := make([]dropcapCaptured, 0, len(raws))
	for i, raw := range raws {
		lines = append(lines, secapLine(t, i, raw))
	}

	got := secapWalk(t, lines, secapTestRedactor())
	if n := got.DeltasByBlockType[secapBlockToolUse][secapDeltaText]; n != 0 {
		t.Errorf("a text_delta in the SECOND message resolved to the FIRST message's tool_use block "+
			"(%d): the index map was not reset on message_start, so every multi-message turn would "+
			"be mislabelled", n)
	}
	if n := got.DeltasByBlockType["text"][secapDeltaText]; n != 1 {
		t.Errorf(`deltas_by_block_type["text"][%s] = %d, want 1`, secapDeltaText, n)
	}
	if got.BlockIndexReused != 0 {
		t.Errorf("block_index_reused = %d, want 0: an index re-opened AFTER a reset is a fresh "+
			"block, not a reuse", got.BlockIndexReused)
	}
	if len(got.MessageIDs) != 2 {
		t.Errorf("message_ids_observed = %v, want both ids", got.MessageIDs)
	}
}

// secapGoodRecord is a record that every gate arm passes, so each table row below
// isolates exactly one way a capture can look green from outside while proving
// nothing.
func secapGoodRecord() *secapRecord {
	return &secapRecord{
		Outcome:           secapFired,
		ClaudeVersion:     "2.1.259 (Claude Code)",
		PartialFlagInArgv: true,
		StreamEventLines:  18,
		DeltaTypeCensus:   map[string]int{secapDeltaText: 12, "input_json_delta": 3},
		BlockTypeCensus:   map[string]int{"text": 2, secapBlockToolUse: 1},
		DeltasByBlockType: map[string]map[string]int{
			"text":            {secapDeltaText: 12},
			secapBlockToolUse: {"input_json_delta": 3},
		},
		Lines: []secapEntry{
			{Index: 1, Type: secapTypeStreamEvent, PayloadEncoding: dropcapEncodingJSONString},
			{Index: 2, Type: secapTypeAssistant, PayloadEncoding: dropcapEncodingJSONString},
		},
	}
}

// TestSecapFixtureWorthyRefusesEveryBadCapture runs offline. fixtureWorthy is the
// only thing standing between a live run and a committed fixture.
func TestSecapFixtureWorthyRefusesEveryBadCapture(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*secapRecord)
		want   bool
	}{
		{"a good capture is promoted", func(*secapRecord) {}, true},
		{"bare version string, no suffix", func(r *secapRecord) { r.ClaudeVersion = "2.1.259" }, true},
		{"never fired", func(r *secapRecord) { r.Outcome = secapDidNotFire }, false},
		{"instrument broken", func(r *secapRecord) { r.Outcome = secapInstrumentBroken }, false},
		{
			// Without this arm the record cannot tell "claude emits none" from "the
			// flag never reached the child", and the second is not a finding.
			"the flag never reached the argv",
			func(r *secapRecord) { r.PartialFlagInArgv = false },
			false,
		},
		{"vacuous: zero stream_event lines", func(r *secapRecord) { r.StreamEventLines = 0 }, false},
		{
			"no text_delta anywhere",
			func(r *secapRecord) { r.DeltaTypeCensus = map[string]int{"input_json_delta": 3} },
			false,
		},
		{
			"a tool_use block that carried no deltas",
			func(r *secapRecord) {
				r.DeltasByBlockType = map[string]map[string]int{"text": {secapDeltaText: 12}}
			},
			false,
		},
		{
			// This probe's own arm, and not any sibling's. Every other capture in the
			// family filters to a handful of lines and can never reach the cap; this
			// one keeps every line of a turn under a flag that multiplies the count.
			// Past the cap whole lines are dropped and only counted, so the fixture
			// would carry a hole mid-delta-run while reading as complete.
			"a line dropped over the byte cap",
			func(r *secapRecord) { r.LinesDroppedOverCap = 1 },
			false,
		},
		{"a partial discarded at the accumulator cap", func(r *secapRecord) { r.PartialsDropped = 1 }, false},
		{"the stream ended mid-line", func(r *secapRecord) { r.UnterminatedPartialLen = 40 }, false},
		{"a different claude release still promotes", func(r *secapRecord) { r.ClaudeVersion = "2.1.260" }, true},
		{"version unreadable", func(r *secapRecord) { r.ClaudeVersion = "<unavailable: exec failed>" }, false},
		{"version absent", func(r *secapRecord) { r.ClaudeVersion = "" }, false},
		{
			// dropcapMakeEntry emits base64 with an EMPTY payload for a line that is
			// not valid UTF-8. Promoting one as a stream_event would hand #2270 a
			// fixture with no readable line to replay.
			"a base64 stream_event the consumer cannot read",
			func(r *secapRecord) { r.Lines[0].PayloadEncoding = dropcapEncodingBase64 },
			false,
		},
		{
			// A NON-quarry line in that state costs the reader nothing and must not
			// block an otherwise good record.
			"a base64 line of another type does not block promotion",
			func(r *secapRecord) { r.Lines[1].PayloadEncoding = dropcapEncodingBase64 },
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := secapGoodRecord()
			tc.mutate(rec)
			reason, ok := rec.fixtureWorthy()
			if ok != tc.want {
				t.Errorf("fixtureWorthy() ok = %v, want %v (reason %q)", ok, tc.want, reason)
			}
			if !ok && reason == "" {
				t.Error("fixtureWorthy() refused without naming a reason; the log line would say nothing")
			}
			if ok && reason != "" {
				t.Errorf("fixtureWorthy() promoted but named reason %q", reason)
			}
		})
	}
}

// TestSecapStagingVerdictSeparatesRigFailureFromFinding runs offline. The verdict
// is what a reader of a thin live gate acts on, and the cases that must never be
// confused are the ones where the rig failed versus the ones where claude's surface
// is the answer.
func TestSecapStagingVerdictSeparatesRigFailureFromFinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		mutate      func(*secapRecord)
		wantFinding bool
	}{
		{
			"the flag never reached the argv",
			func(r *secapRecord) { r.PartialFlagInArgv = false },
			false,
		},
		{
			"the flag was passed and nothing came back",
			func(r *secapRecord) { r.StreamEventLines = 0 },
			true,
		},
		{
			"no streamed assistant text",
			func(r *secapRecord) { r.DeltaTypeCensus = map[string]int{} },
			false,
		},
		{
			"the turn made no tool call at all",
			func(r *secapRecord) {
				r.BlockTypeCensus = map[string]int{"text": 2}
				r.DeltasByBlockType = map[string]map[string]int{"text": {secapDeltaText: 12}}
			},
			false,
		},
		{
			// The one #2270 must be told about: the block existed and its input never
			// streamed, which is a fact about the surface rather than about the rig.
			"a tool_use block opened and carried no deltas",
			func(r *secapRecord) {
				r.DeltasByBlockType = map[string]map[string]int{"text": {secapDeltaText: 12}}
			},
			true,
		},
		{"a complete capture", func(*secapRecord) {}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := secapGoodRecord()
			tc.mutate(rec)
			got := rec.stagingVerdict()
			if got == "" {
				t.Fatal("stagingVerdict() is empty; a thin capture would name no cause")
			}
			isFinding := strings.Contains(got, "finding about the surface")
			if isFinding != tc.wantFinding {
				t.Errorf("stagingVerdict() reads as a surface finding = %v, want %v\n  got: %s",
					isFinding, tc.wantFinding, got)
			}
			if !tc.wantFinding && !strings.Contains(got, "staging") {
				t.Errorf("a rig failure must say so plainly; got: %s", got)
			}
			// complete() is the same predicate as the verdict's default arm, and the
			// live test sets the record's OUTCOME from it. Left unchecked, the two
			// could drift into a record whose outcome says fired while its verdict
			// names a staging failure.
			wantComplete := tc.name == "a complete capture"
			if rec.complete() != wantComplete {
				t.Errorf("complete() = %v but the verdict took the %q arm; the outcome the live "+
					"test records and the verdict it publishes would disagree\n  got: %s",
					rec.complete(), tc.name, got)
			}
		})
	}
}

// TestSecapFixturePathRefusesAnUnusableVersion runs offline. The fixture NAME is
// composed from a SUBPROCESS's stdout, so this validator is the trust boundary
// between claude's output and a filesystem write.
func TestSecapFixturePathRefusesAnUnusableVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"a plain version", "2.1.259", secapFixturePrefix + "2.1.259.json"},
		{"the (Claude Code) suffix is cut", "2.1.259 (Claude Code)", secapFixturePrefix + "2.1.259.json"},
		{"a prerelease tag", "2.2.0-beta.1", secapFixturePrefix + "2.2.0-beta.1.json"},
		{"empty", "", ""},
		{"the unavailable sentinel", "<unavailable: exec failed>", ""},
		{"a traversal", "../../../etc/passwd", ""},
		{"an absolute path", "/etc/passwd", ""},
		{"a bare separator", "2.1/259", ""},
		{"a null byte", "2.1.259\x00.json", ""},
		{"over the length cap", strings.Repeat("9", secapMaxVersionToken+1), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := secapFixturePath(tc.version)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("secapFixturePath(%q) = %q with no error; a version read from a "+
						"subprocess must not compose a write path unchecked", tc.version, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("secapFixturePath(%q) returned %v; want %q", tc.version, err, tc.want)
			}
			if got != tc.want {
				t.Errorf("secapFixturePath(%q) = %q, want %q", tc.version, got, tc.want)
			}
			if matched, _ := filepath.Match(secapFixtureGlob, got); !matched {
				t.Errorf("%q does not match the arming glob %q, so a written fixture would never "+
					"disarm the probe", got, secapFixtureGlob)
			}
		})
	}
}

// TestSecapArgsCarryTheFlagAndProductionDoesNot runs offline. AC 4 is a claim about
// two argv shapes, and only one of them is this file's to state.
func TestSecapArgsCarryTheFlagAndProductionDoesNot(t *testing.T) {
	t.Parallel()
	if !secapContains(secapArgs, secapPartialFlag) {
		t.Fatalf("secapArgs = %v carries no %s: the probe would spawn claude without the flag it "+
			"exists to exercise, and every captured line would be of the ordinary surface",
			secapArgs, secapPartialFlag)
	}
	// The flag reaches claude ONLY through Config.Args, which buildArgs appends
	// after its fixed prefix. Production passes no Args of this shape, and this
	// ticket changes neither buildArgs nor any of its callers — the live half of
	// that claim is partial_messages_flag_in_argv, observed from the runner's own
	// spawn record rather than transcribed here.
	for _, a := range secapArgs {
		if strings.HasPrefix(a, "--input-format") || strings.HasPrefix(a, "--output-format") {
			t.Errorf("secapArgs restates %q, which buildArgs already supplies; a duplicated stream "+
				"flag would make the capture's spawn shape differ from production's in a second way", a)
		}
	}
}
