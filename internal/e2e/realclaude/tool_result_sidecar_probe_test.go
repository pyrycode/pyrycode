//go:build e2e_realclaude

package realclaude

// Evidence capture for #2023 — what a `user`/`tool_result` line carries on
// claude's STDOUT, the surface internal/streamsup actually parses.
//
// # The question
//
// Every `toolUseResult` sidecar the tree knows was read off a TRANSCRIPT
// (internal/agentrun/jsonl/testdata/*.jsonl), and the daemon does not read the
// transcript. The four live tests that decode the sidecar all reach it through
// the session JSONL on disk. Nothing records a stdout `user`/`tool_result` line
// carrying one at all. A shape-keyed decoder written against a sidecar that never
// arrives on stdout is dead code, and no hermetic test catches it — fixtures
// hand-copied from the transcript would pass against a decoder that never fires.
//
// So this probe asks one empirical question and commits the answer: is a top-level
// `toolUseResult` present on stdout, and with what key set? BOTH ANSWERS ARE A
// PASS. Absence is the result this run exists to get, not a failure.
//
// # Where it taps
//
// cmd/pyry's `newStreamRunnerFactory` installs streamsup.NewParser(...) as
// scfg.Stdout. dropcapRecorder takes that exact slot on an in-process
// streamsup.Runner, so "the production interactive stream-json shape, upstream of
// the parser" is a fact about the wiring rather than an argument.
//
// # What is reused, and the one thing that is not
//
// #1260's rig is reused wholesale: dropcapRecorder, dropcapRedactor,
// dropcapScanner, newDropcapArgvHandler, dropcapWaitForChild. That apparatus is
// what makes committing live stdout safe, and rebuilding it would be the whole
// cost of the ticket.
//
// THE RETENTION PREDICATE IS NOT REUSED, and it is not a one-line edit. #1260
// retains lines that emitted ZERO events, and its taxonomy (ignored-line-type,
// user-block-suppressed, empty-message, undecodable-line) is a REASON for that
// zero. A `user`/`tool_result` line is not in that taxonomy: `emitUser` in
// internal/streamsup/parser.go maps each tool_result block to a
// turnevent.ToolUpdate, so the line EMITS. Retention here is keyed on LINE TYPE
// and deliberately keeps a line that did emit. No parser is run and no drop
// reason is computed.
//
// # Redaction — the boundary is claude stdout -> record -> committed public file
//
// Two mechanisms of different fabric, both #1260's. dropcapRedactor is a declared
// substitution table applied to every string entering the record; dropcapScanner
// is a fail-closed deny-scan over the marshalled record AND every base64 payload's
// decoded bytes. On a hit nothing is written and the message names the CLASS only.
// os.Environ() is never read into the record; env_delta is a fixed literal.
//
// NO PAYLOAD AND NO RECORD EVER REACHES t.Logf. The deny-scan gates the FILE, not
// the run log, and this pipeline salvages run logs — so a %+v of the record would
// move every retained payload into a salvaged log having bypassed the fail-closed
// net entirely, including on the run where the scan refused to write the file.
// Counts, outcome names, the applied map and redactor-passed strings only.
//
// # Running it
//
// NOT env-gated, deliberately: `make e2e-realclaude` must run it. An env-gated
// probe skips under that gate, and a skip is indistinguishable from a pass by exit
// code — which is why that gate counts executed tests instead. The TestSidecap*
// tests below run offline, with no claude and no credentials.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// Every file-local identifier takes the sidecap prefix: siblings add files to this
// package concurrently and a branch-overlap check does NOT catch a same-package
// identifier collision — it surfaces only once both are on main.
const (
	sidecapTicket        = "2023"
	sidecapWorkdirName   = "sidecap-work"
	sidecapInputName     = "sidecap-input.txt"
	sidecapModel         = "haiku"
	sidecapFixtureStem   = "tool_result_sidecar_v"
	sidecapMarker        = "pyry-2023-shell-marker"
	sidecapNonArrayBlock = "<non-array-content>"
	// A content array element that is not a JSON object. Distinct from
	// sidecapNonArrayBlock: that one says the content was not an array at all.
	sidecapNonObjectBlock = "<non-object-block>"
)

// THE TWO SPELLINGS. The transcript spells the sidecar `toolUseResult`; the FIRST
// live run of this probe (claude 2.1.239, 2026-09-02) found stdout spelling it
// `tool_use_result`. Both are searched, and the spelling actually observed is
// recorded per line, because the difference is the entire practical finding: a
// decoder keyed on the transcript's camelCase name is dead code on stdout even
// though the sidecar is right there.
//
// Searching only the camelCase name — which this probe did until that run — yields
// a technically true "absent" that would send the downstream decoder ticket to
// exactly the wrong conclusion. Do not narrow this back to one spelling.
const (
	sidecapKeyCamel = "toolUseResult"
	sidecapKeySnake = "tool_use_result"
)

const (
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from
	// dropcapSessionID: both are redaction classes in the same package.
	sidecapSessionID = "3f9b1d42-7c05-4e18-a6d3-2b8e5f01c7a9"
)

// sidecapInputContent is the ENTIRE content of the file claude is asked to read.
// A declared constant, so the file's bytes are rig-authored and nothing
// operator-derived can reach a Read tool result through it.
const sidecapInputContent = "pyry #2023 probe input.\n" +
	"This file exists so one Read tool call has something to return.\n" +
	"Its bytes are a declared constant in tool_result_sidecar_probe_test.go.\n"

const (
	sidecapTurnBudget  = 3 * time.Minute
	sidecapSpawnWait   = 60 * time.Second
	sidecapRunExitWait = 30 * time.Second
)

// The outcomes. Only the two sidecar arms carry a valid observation; the other two
// say why there is none.
const (
	sidecapInstrumentBroken = "instrument-broken"
	sidecapNoUserLine       = "no-user-line"
	sidecapSidecarAbsent    = "sidecar-absent"
	sidecapSidecarPresent   = "sidecar-present"
)

const (
	sidecapTerminatedResult = "result"
	sidecapTerminatedBudget = "budget"
)

// What `toolUseResult` WAS, when it was not an object. absent and null are
// different answers and telling them apart is the point of the ticket.
const (
	sidecapKindAbsent = "absent"
	sidecapKindObject = "object"
	sidecapKindArray  = "array"
	sidecapKindString = "string"
	sidecapKindNumber = "number"
	sidecapKindBool   = "bool"
	sidecapKindNull   = "null"
	sidecapKindBroken = "undecodable"
)

// payload_encoding is written on EVERY entry. A payload stored as a JSON string is
// re-encoded by the container and Go's encoder replaces invalid UTF-8 with U+FFFD,
// so invalid UTF-8 goes to base64 instead and says so.
const (
	sidecapEncodingJSONString = "json-string"
	sidecapEncodingBase64     = "base64"
)

// sidecapArgs is everything this probe adds; buildArgs supplies the fixed
// --input-format/--output-format/--verbose prefix and the id flag. Declared here
// rather than borrowing dropcapArgs so an edit there cannot silently change this
// probe's spawn shape.
var sidecapArgs = []string{"--model", sidecapModel, "--dangerously-skip-permissions"}

const sidecapSpawnShapeDelta = "This is the YOLO interactive shape. Production's stream path adds, via " +
	"cmd/pyry's withApprovalArgs and internal/sessions.claudeSettingsArgs, a --permission-prompt-tool / " +
	"--mcp-config pair on NON-yolo spawns plus a per-session --settings. This capture passes none of them: " +
	"it uses --dangerously-skip-permissions, a real production shape (the YOLO session bit) and precisely " +
	"the arm on which withApprovalArgs injects nothing. Whether the flag alters the user/tool_result line " +
	"is UNMEASURED — do not read parity into it."

const sidecapLimitations = "One turn, one spawn shape, one claude version, one model (" + sidecapModel + "), " +
	"and exactly two tool calls (one Read, one Bash). Cross-version, cross-model and cross-tool stability " +
	"are UNMEASURED. A key set absent from observed_key_sets did not appear on THIS turn, which is not the " +
	"same as claude never emitting it. In particular the transcript's 33 sidecars land in four key sets, " +
	"and this turn can at most reach the two that a file read and a shell call produce."

const sidecapRedactionRationale = "The primary defence is by construction: the workdir is a fresh directory " +
	"with NO git repo (no branch names), the prompt is rig-authored, the shell call is a fixed `echo` of a " +
	"declared marker, and os.Environ() is never read into the record. Retention is keyed on type==\"user\", " +
	"which is a NARROWING over #1260's capture: a system/init line — that file's largest operator-specific " +
	"class, carrying the local MCP server names, tool list, slash-command names, skill names and subagent " +
	"names — is never retained here at all. " +
	"THE ONE WIDENING, STATED RATHER THAN INHERITED: unlike #1260's workdir this one is NOT empty. It holds " +
	"exactly one file, because an AC needs a file read, and that file's entire content is a declared " +
	"constant in tool_result_sidecar_probe_test.go (carried in this record as input_file_content, so a " +
	"reader can check it rather than take this sentence on trust). Nothing operator-derived is introduced, " +
	"but the by-construction argument is weaker by exactly that much. " +
	"On top of that, dropcapRedactor substitutes a declared table of path/identifier classes into EVERY " +
	"string entering the record, and dropcapScanner is a fail-closed deny-scan over the whole marshalled " +
	"record plus every base64 payload's decoded bytes. " +
	"DELIBERATELY KEPT, because removing them would defeat the ticket: top-level types and subtypes, " +
	"claude's structural fields, tool names and tool-use ids, the sidecar's key sets and values, the " +
	"rig-authored file content and echo marker, token counts and timestamps."

// --- the shape ---------------------------------------------------------------

// sidecapShape is what one retained stdout line carries. Split from sidecapEntry
// so the offline table test can drive it on raw bytes with no redactor in hand.
type sidecapShape struct {
	// LineKeys is the ENVELOPE's own top-level key set. The one real stdout `user`
	// line the tree holds (dropped_lines[26] in dropped_lines_v2.1.220.json) carries
	// parent_tool_use_id/session_id/uuid/timestamp/isSynthetic — the transcript's
	// envelope style, which is what made the sidecar plausible here in the first
	// place. Recording the key set is how that stays checkable instead of recalled.
	LineKeys []string `json:"line_keys"`

	// Present is true when EITHER spelling is there. Key names the spelling the kind
	// and key set below were read from; Spellings lists every spelling observed, so a
	// line carrying both is visible rather than collapsed to whichever was checked
	// first.
	ToolUseResultPresent   bool     `json:"tool_use_result_present"`
	ToolUseResultKey       string   `json:"tool_use_result_key,omitempty"`
	ToolUseResultSpellings []string `json:"tool_use_result_spellings,omitempty"`
	ToolUseResultIsObject  bool     `json:"tool_use_result_is_object"`
	ToolUseResultKind      string   `json:"tool_use_result_kind"`
	ToolUseResultKeys      []string `json:"tool_use_result_keys,omitempty"`

	ToolResultBlockCount int      `json:"tool_result_block_count"`
	ContentBlockTypes    []string `json:"content_block_types"`
}

type sidecapEntry struct {
	Index int `json:"index"`
	sidecapShape
	PayloadLenBytes int    `json:"payload_len_bytes"`
	PayloadEncoding string `json:"payload_encoding"`
	Payload         string `json:"payload,omitempty"`
	PayloadB64      string `json:"payload_b64,omitempty"`
}

// sidecapKeySet is one distinct observed sidecar key set and how many retained
// lines carried it.
type sidecapKeySet struct {
	Keys  []string `json:"keys"`
	Count int      `json:"count"`
}

type sidecapRecord struct {
	Ticket           string   `json:"ticket"`
	ClaudeVersion    string   `json:"claude_version"`
	CapturedAt       string   `json:"captured_at"`
	IsCapture        bool     `json:"is_capture"`
	Model            string   `json:"model"`
	SpawnShape       []string `json:"spawn_shape"`
	SpawnShapeDelta  string   `json:"spawn_shape_delta"`
	EnvDelta         []string `json:"env_delta"`
	Workdir          string   `json:"workdir"`
	InputFile        string   `json:"input_file"`
	InputFileContent string   `json:"input_file_content"`
	Prompt           string   `json:"prompt"`

	Outcome                 string `json:"outcome"`
	OutcomeDetail           string `json:"outcome_detail"`
	SidecarObservationValid bool   `json:"sidecar_observation_valid"`
	TerminatedOn            string `json:"terminated_on"`

	StdoutLinesCaptured int            `json:"stdout_lines_captured"`
	UserLinesRetained   int            `json:"user_lines_retained"`
	UserLines           []sidecapEntry `json:"user_lines"`

	SidecarPresentCount int `json:"sidecar_present_count"`
	SidecarAbsentCount  int `json:"sidecar_absent_count"`
	// How many retained lines carried each spelling of the sidecar key. This is the
	// field that separates "stdout has no sidecar" from "stdout spells it
	// differently", and only the second of those turned out to be true.
	SidecarKeySpellingCensus      map[string]int  `json:"sidecar_key_spelling_census"`
	ObservedKeySets               []sidecapKeySet `json:"observed_key_sets"`
	ToolResultBlockCountHistogram map[string]int  `json:"tool_result_block_count_histogram"`
	ContentBlockTypeCensus        map[string]int  `json:"content_block_type_census"`

	Redaction             []dropcapSubstitution `json:"redaction"`
	RedactionRationale    string                `json:"redaction_rationale"`
	CredentialScanApplied map[string]bool       `json:"credential_scan_applied"`
	Limitations           string                `json:"limitations"`

	LinesDroppedOverCap    int `json:"lines_dropped_over_cap"`
	BytesDroppedOverCap    int `json:"bytes_dropped_over_cap"`
	PartialsDropped        int `json:"partials_dropped"`
	BlankLines             int `json:"blank_lines"`
	UnterminatedPartialLen int `json:"unterminated_partial_len"`
}

// set fills the outcome. Only the two sidecar arms license an observation claim.
// EVERY string reaching args must already have passed through dropcapRedactor:
// an error from streamsup.New or WriteTurn carries the workdir path.
func (rec *sidecapRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
	rec.SidecarObservationValid = outcome == sidecapSidecarAbsent || outcome == sidecapSidecarPresent
}

// --- inspection ----------------------------------------------------------------

// sidecapInspect reports what one raw stdout line carries.
//
// PRESENCE IS DECIDED BY KEY EXISTENCE on a map[string]json.RawMessage decode,
// never by a zero value. An absent `toolUseResult` and a `"toolUseResult": null`
// are DIFFERENT ANSWERS and telling them apart is the whole ticket: a decode into
// a struct with a `ToolUseResult map[string]any` field reports both as nil, which
// would let this probe publish "the sidecar is absent on stdout" against a claude
// that sends it explicitly empty.
func sidecapInspect(raw []byte) sidecapShape {
	shape := sidecapShape{
		LineKeys:          []string{},
		ToolUseResultKind: sidecapKindAbsent,
		ContentBlockTypes: []string{},
	}

	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		shape.ToolUseResultKind = sidecapKindBroken
		return shape
	}
	shape.LineKeys = sidecapKeysOf(top)

	// Both spellings, camelCase first so a line carrying both reports the
	// transcript's name as the one the kind and key set were read from.
	for _, spelling := range []string{sidecapKeyCamel, sidecapKeySnake} {
		sidecar, ok := top[spelling]
		if !ok {
			continue
		}
		shape.ToolUseResultSpellings = append(shape.ToolUseResultSpellings, spelling)
		if shape.ToolUseResultPresent {
			continue
		}
		shape.ToolUseResultPresent = true
		shape.ToolUseResultKey = spelling
		shape.ToolUseResultKind = sidecapValueKind(sidecar)
		if shape.ToolUseResultKind == sidecapKindObject {
			shape.ToolUseResultIsObject = true
			shape.ToolUseResultKeys = sidecapSortedKeys(sidecar)
		}
	}

	msg, ok := top["message"]
	if !ok {
		return shape
	}
	var msgObj struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msg, &msgObj) != nil || len(msgObj.Content) == 0 {
		return shape
	}
	// A user message's content is not always an array. A silent 0 would read as "no
	// tool_result blocks" rather than "not counted", so the shape says which.
	var blocks []json.RawMessage
	if json.Unmarshal(msgObj.Content, &blocks) != nil {
		shape.ContentBlockTypes = []string{sidecapNonArrayBlock}
		return shape
	}

	seen := map[string]bool{}
	for _, b := range blocks {
		var bt struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(b, &bt) != nil {
			seen[sidecapNonObjectBlock] = true
			continue
		}
		if bt.Type == "tool_result" {
			shape.ToolResultBlockCount++
		}
		seen[bt.Type] = true
	}
	for bt := range seen {
		shape.ContentBlockTypes = append(shape.ContentBlockTypes, bt)
	}
	sort.Strings(shape.ContentBlockTypes)
	return shape
}

// sidecapKeysOf returns a decoded object's keys, sorted.
func sidecapKeysOf(obj map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sidecapValueKind names what a JSON value is, without decoding it. Used only to
// say what a non-object `toolUseResult` was, so a reader is never left with
// is_object:false and no idea what arrived instead.
func sidecapValueKind(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return sidecapKindBroken
	}
	switch trimmed[0] {
	case '{':
		return sidecapKindObject
	case '[':
		return sidecapKindArray
	case '"':
		return sidecapKindString
	case 't', 'f':
		return sidecapKindBool
	case 'n':
		return sidecapKindNull
	}
	if (trimmed[0] >= '0' && trimmed[0] <= '9') || trimmed[0] == '-' {
		return sidecapKindNumber
	}
	return sidecapKindBroken
}

// sidecapSortedKeys returns an object's top-level keys, sorted, or nil for
// anything that is not a decodable object.
func sidecapSortedKeys(raw json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- the arming census -----------------------------------------------------------

// sidecapScanPathClasses names the classes newDropcapScanner arms through
// addDynamicPath. It is a SECOND, INDEPENDENT COPY of that constructor's
// addDynamicPath calls rather than a read of them, for the reason
// initControlScanPathClasses states: a class added there and not here VANISHES
// from the recorded map whenever its value is empty, while a name here that no
// scanner arms writes a `false` key for a class that does not exist.
//
// The two credential classes and the five fixed literals are DELIBERATELY ABSENT.
// They cannot vanish: addDynamic appends its needle unconditionally, so an unset
// CLAUDE_CODE_OAUTH_TOKEN lands as `false` rather than as no key at all, and the
// fixed needles are authoring-time literals that are never empty.
var sidecapScanPathClasses = []string{
	dropcapClassTempHome,
	dropcapClassOperatorHome,
	dropcapClassArtifactDir,
	dropcapClassWorkdir,
}

// sidecapScanApplied returns s.applied() with every path class PRESENT.
//
// The two arming paths behave differently for an absent value, and only one of
// them behaves the way "an unset value arms nothing" suggests. addDynamic appends
// unconditionally, so a class handed "" lands as `false`. addDynamicPath goes
// through dropcapPathSpellings, which returns nil for "" — so NO needle is
// appended, and applied, which builds its map by ranging the needles, carries no
// key for that class AT ALL. An omitted class reads exactly like a class nobody
// ever thought about, which is the outcome credential_scan_applied exists to
// prevent.
//
// This probe writes straight into testdata/ and mints no artifact directory, so it
// hands newDropcapScanner an empty artifactDir and artifact_dir is precisely the
// class that would vanish. operator_home has the identical hole whenever realHome
// is empty, so all four are closed here rather than only the one that is certain.
//
// The completion belongs HERE at the fill site, not inside dropcapScanner, whose
// record shape is already committed and shared with two other families — #1747
// made the same call for #1688.
//
// Only a MISSING key is added. An existing entry — true or false — is left alone: a
// completion that assigned false unconditionally would report every armed class as
// armed-nothing while still satisfying the artifact_dir check, and the workdir
// control in TestSidecapScanApplied_RecordsAnArmedNothingClassForAnAbsentPath is
// its sole red.
//
// applied returns a FRESH map per call, so this mutates a map it owns and no
// caller's value is shared. Do not take a defensive second copy.
func sidecapScanApplied(s dropcapScanner) map[string]bool {
	out := s.applied()
	for _, class := range sidecapScanPathClasses {
		if _, ok := out[class]; !ok {
			out[class] = false
		}
	}
	return out
}

// --- the live capture ------------------------------------------------------------

// TestRealClaude_ToolResultSidecarCapture drives ONE live turn on the production
// interactive stream-json shape, retains every `user` line off stdout verbatim,
// and commits what those lines carry.
//
// IT ASSERTS NOTHING ABOUT CLAUDE. A run in which the sidecar is absent is a
// PASSING outcome and is the answer this probe most expects to get. The run fails
// only when the instrument broke.
//
// "ZERO STDOUT LINES RETAINED" IS READ AS ZERO LINES CAPTURED OFF STDOUT, not as
// zero `user` lines retained. The alternative reading fails the run on claude's
// behaviour — a turn that took no tool call — which contradicts this family's
// standing rule that only a broken instrument or a fail-closed refusal is fatal
// (dropcapClassifyOutcome, runInitControlChild). A turn that provoked no tool call
// is information: it lands in `outcome` as no-user-line with
// sidecar_observation_valid false, where a reader sees it.
func TestRealClaude_ToolResultSidecarCapture(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials
	versionRaw, versionToken := captureClaudeVersion(t)

	// A fresh directory, deliberately not a git repo. It is NOT empty — see
	// sidecapRedactionRationale for why that widening is stated rather than hidden.
	workdir := filepath.Join(home, sidecapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2023: create workdir: %v", err)
	}
	inputPath := filepath.Join(workdir, sidecapInputName)
	if err := os.WriteFile(inputPath, []byte(sidecapInputContent), 0o600); err != nil {
		t.Fatalf("#2023: seed the input file: %v", err)
	}

	nonce := time.Now().UnixNano()

	// THE EMPTY SLOTS ARE artifactDir AND fifoPath, and the emptiness is a fact this
	// probe records rather than an omission to tidy up: it writes into testdata/ and
	// holds no FIFO. Both constructors guard "" (dropcapRedactor.add returns early;
	// strings.ReplaceAll(s, "", x) would otherwise insert x between every character).
	red := newDropcapRedactor(home, "", workdir, "", sidecapSessionID, nonce)

	// newDropcapScanner(tempHome, artifactDir, workdir) — passing workdir into the
	// middle slot would arm artifact_dir with the workdir path and leave workdir
	// unarmed, and minting a directory to fill it would report an arming that never
	// happened. sidecapScanApplied is what keeps the empty class PRESENT as
	// armed-nothing instead of missing.
	//
	// THIS VALUE IS CREDENTIAL-BEARING: NEVER FORMAT IT. newDropcapScanner reads
	// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY into its needles, so a
	// dropcapScanner in scope is two live credentials in a struct, and a %v, %+v,
	// %#v or %q on it prints sk-ant-… into a run log this pipeline salvages. Its
	// applied() MAP is safe to print and is the diagnostic worth printing: keys are
	// declared vocabulary, values are bools, and applied keys by class never by
	// value.
	scanner := newDropcapScanner(home, "", workdir)

	rec := &sidecapRecord{
		Ticket:           sidecapTicket,
		ClaudeVersion:    versionRaw,
		CapturedAt:       time.Now().Format(time.RFC3339),
		IsCapture:        true,
		Model:            sidecapModel,
		SpawnShapeDelta:  sidecapSpawnShapeDelta,
		EnvDelta:         []string{}, // A FIXED LITERAL. This probe sets no variable.
		Workdir:          red.str(workdir),
		InputFile:        red.str(inputPath),
		InputFileContent: sidecapInputContent,
		UserLines:        []sidecapEntry{},
		ObservedKeySets:  []sidecapKeySet{},

		SidecarKeySpellingCensus:      map[string]int{},
		ToolResultBlockCountHistogram: map[string]int{},
		ContentBlockTypeCensus:        map[string]int{},
		RedactionRationale:            sidecapRedactionRationale,
		CredentialScanApplied:         sidecapScanApplied(scanner),
		Limitations:                   sidecapLimitations,
	}
	rec.set(sidecapInstrumentBroken, "did not reach a classification point")

	// Registered FIRST in this body so t.Cleanup's LIFO runs it LAST of ours: the
	// order is runner ctx cancelled -> fixture written, and a t.Fatalf below still
	// lands the evidence.
	t.Cleanup(func() {
		sidecapWriteFixture(t, filepath.Join(packageDir(t), "testdata"), red, scanner, rec, versionToken)
	})

	recorder := newDropcapRecorder()
	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: sidecapSessionID,
		Args:      sidecapArgs,
		Stdout:    recorder,
		// Stderr stays nil, so the exec.Cmd sends the child's stderr to /dev/null.
		// That is STRONGER than scrubbing it and is why initControlScrubbed is not
		// reused: that guard exists because #1688's fixture records stderr in a
		// StderrCapture field, and this record has none. Capturing stderr in order to
		// scrub it would mean a mutex-guarded buffer — os/exec drives Config.Stderr
		// from its own copier goroutine — holding bytes nothing consumes.
		Logger: slog.New(argvHandler),
	})
	if err != nil {
		rec.set(sidecapInstrumentBroken, "streamsup.New failed, so no claude was ever spawned: %v",
			red.str(err.Error()))
		t.Fatalf("#2023: %s", rec.OutcomeDetail)
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
		case <-time.After(sidecapRunExitWait):
			t.Errorf("#2023: streamsup.Run did not return within %s of cancel", sidecapRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(sidecapInstrumentBroken, "no live child within %s: claude never spawned, so nothing "+
			"was on the wire to capture", sidecapSpawnWait)
		t.Fatalf("#2023: %s", rec.OutcomeDetail)
	}

	prompt := sidecapPrompt(inputPath, nonce)
	rec.Prompt = red.str(prompt)
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		rec.set(sidecapInstrumentBroken, "writing the turn envelope failed, so no turn was ever "+
			"driven: %v", red.str(err.Error()))
		t.Fatalf("#2023: %s", rec.OutcomeDetail)
	}

	select {
	case <-recorder.resultSeen:
		rec.TerminatedOn = sidecapTerminatedResult
	case <-time.After(sidecapTurnBudget):
		rec.TerminatedOn = sidecapTerminatedBudget
	}

	rec.SpawnShape = red.strs(argv())
	lines, caps := recorder.snapshot()
	rec.StdoutLinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	// The retention predicate: LINE TYPE, not a drop reason. These lines DID emit.
	for _, line := range lines {
		if line.Type != "user" {
			continue
		}
		rec.UserLines = append(rec.UserLines, sidecapMakeEntry(line, red))
	}
	rec.UserLinesRetained = len(rec.UserLines)
	sidecapSummarize(rec)

	if rec.StdoutLinesCaptured == 0 {
		rec.set(sidecapInstrumentBroken, "zero lines were captured from claude's stdout, so the "+
			"recorder observed nothing and there is no surface to report on")
		t.Fatalf("#2023: %s", rec.OutcomeDetail)
	}
}

// sidecapPrompt drives exactly one Read and exactly one Bash call.
//
// It takes bgIdlePrompt's posture — exactly once, verbatim, nothing else — because
// "which two tool calls happen" is setup here, not the measured axis; the measured
// axis is what the resulting stdout lines CARRY. The two calls are chosen because
// the transcript's two dominant sidecar key sets are the shell set and the file
// set, so a turn that provokes both can reach either.
//
// Only two values are interpolated: inputPath, a temp-$HOME-derived absolute path
// plus a fixed const basename carrying no shell metacharacters, and nonce. NONCE IS
// A CACHE-BUSTER, not security randomness — it must stay wall-clock, must not be
// "upgraded" to crypto/rand, and must not be read as though it were a token.
func sidecapPrompt(inputPath string, nonce int64) string {
	return fmt.Sprintf("Do exactly two things, in this order, and nothing else. "+
		"First, use the Read tool exactly once on this file: %s. "+
		"Second, use the Bash tool exactly once to run this command verbatim: echo %s. "+
		"Do not chain commands with && or ;, do not add any flags or redirections, do not read "+
		"or list any other file, and do not comment on the results. run=%d",
		inputPath, sidecapMarker, nonce)
}

// sidecapMakeEntry redacts one retained line into a record entry.
//
// The payload is REDACTED BEFORE it is base64-encoded, so the fixture never carries
// raw paths on the base64 arm and the writer's scan of the decoded bytes sees the
// same content the JSON-string arm shows.
func sidecapMakeEntry(c dropcapCaptured, red *dropcapRedactor) sidecapEntry {
	e := sidecapEntry{
		Index:           c.Index,
		sidecapShape:    sidecapInspect(c.Raw),
		PayloadLenBytes: len(c.Raw),
	}
	redacted := red.redact(append([]byte(nil), c.Raw...))
	if utf8.Valid(redacted) {
		e.PayloadEncoding = sidecapEncodingJSONString
		e.Payload = string(redacted)
	} else {
		e.PayloadEncoding = sidecapEncodingBase64
		e.PayloadB64 = base64.StdEncoding.EncodeToString(redacted)
	}
	return e
}

// sidecapSummarize fills the census and decides the outcome. The key-set census and
// the block-count histogram are what let AC3's one-sidecar-per-block question rest
// on a stdout observation rather than on the transcript's 33-for-33.
func sidecapSummarize(rec *sidecapRecord) {
	bySet := map[string]int{}
	for _, e := range rec.UserLines {
		if e.ToolUseResultPresent {
			rec.SidecarPresentCount++
		} else {
			rec.SidecarAbsentCount++
		}
		for _, spelling := range e.ToolUseResultSpellings {
			rec.SidecarKeySpellingCensus[spelling]++
		}
		if e.ToolUseResultIsObject {
			bySet[strings.Join(e.ToolUseResultKeys, ",")]++
		}
		rec.ToolResultBlockCountHistogram[strconv.Itoa(e.ToolResultBlockCount)]++
		for _, bt := range e.ContentBlockTypes {
			rec.ContentBlockTypeCensus[bt]++
		}
	}
	for joined, count := range bySet {
		keys := []string{}
		if joined != "" {
			keys = strings.Split(joined, ",")
		}
		rec.ObservedKeySets = append(rec.ObservedKeySets, sidecapKeySet{Keys: keys, Count: count})
	}
	sort.Slice(rec.ObservedKeySets, func(i, j int) bool {
		return strings.Join(rec.ObservedKeySets[i].Keys, ",") < strings.Join(rec.ObservedKeySets[j].Keys, ",")
	})

	switch {
	case rec.UserLinesRetained == 0:
		rec.set(sidecapNoUserLine, "claude's stdout carried %d line(s) but none of type \"user\", so the "+
			"turn provoked no tool result and this run supports no claim about the sidecar",
			rec.StdoutLinesCaptured)
	case rec.SidecarPresentCount == 0:
		rec.set(sidecapSidecarAbsent, "%d retained user line(s), NONE carrying a top-level sidecar under "+
			"either spelling (%q or %q). This is a recorded result, not a failure: it is evidence that a "+
			"shape-keyed decoder would never fire on stdout", rec.UserLinesRetained,
			sidecapKeyCamel, sidecapKeySnake)
	default:
		rec.set(sidecapSidecarPresent, "%d of %d retained user line(s) carry a top-level sidecar; "+
			"spelling census %v. NOTE the spelling: the transcript spells it %q and a decoder keyed on "+
			"that name is dead code on stdout if the census below says %q",
			rec.SidecarPresentCount, rec.UserLinesRetained, rec.SidecarKeySpellingCensus,
			sidecapKeyCamel, sidecapKeySnake)
	}
}

// --- the write path ---------------------------------------------------------------

// sidecapWriteFixture runs the deny-scan and, only if it passes, writes the capture
// into testdata/ where this ticket's PR commits it.
//
// NOTHING IS WRITTEN ON AN INSTRUMENT-BROKEN RUN. #1260 writes its record on every
// path because it writes to a temp artifact directory an operator inspects by hand;
// this writes into testdata/, which the run `git add`s, and a record with no
// captured lines would commit a worthless artifact under a name that claims to be a
// capture. The outcome still reaches the run log.
//
// THE SCAN RUNS BEFORE THE FIRST FILESYSTEM CALL, so a value the redaction table did
// not predict aborts with no file written — and no partial. A reviewer who sees that
// failure has found a gap in the table, not a bug in the writer.
func sidecapWriteFixture(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner,
	rec *sidecapRecord, versionToken string) {
	t.Helper()
	rec.Redaction = red.substitutions()

	if rec.StdoutLinesCaptured == 0 {
		t.Logf("#2023: outcome=%s — nothing was captured off stdout, so no fixture was written: %s",
			rec.Outcome, rec.OutcomeDetail)
		return
	}

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("#2023: marshal record: %v", err)
	}
	hits, notApplied := scanner.scan(blob)
	// A base64 payload hides its bytes from a scan of the marshalled record, so the
	// decoded bytes are scanned too.
	for _, e := range rec.UserLines {
		if e.PayloadB64 == "" {
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(e.PayloadB64)
		if derr != nil {
			t.Fatalf("#2023: a base64 payload did not decode, so it could not be scanned; refusing to "+
				"write: entry %d", e.Index)
		}
		if h, _ := scanner.scan(decoded); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("#2023: deny-scan found %d denied class(es) still present in the record: %v\n"+
			"  located at: %v\n"+
			"NOTHING was written. Extend dropcapRedactor's table with the named class and re-run the "+
			"capture. The offending value is deliberately not printed: putting it in CI output is exactly "+
			"the exposure this scan exists to prevent",
			len(hits), hits, sidecapLocateHits(scanner, rec))
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("#2023: create testdata dir: %v", err)
	}
	path := filepath.Join(dir, sidecapFixtureStem+versionSlug(versionToken)+".json")
	// Temp-plus-rename, at 0o600: a non-atomic write can leave a partial JSON in
	// testdata/ that the run then `git add`s. The temp lives in the destination
	// directory so the rename is same-filesystem, and is removed on every failure
	// arm — a leftover .tmp is itself commit bait.
	tmp, err := os.CreateTemp(dir, sidecapFixtureStem+"*.tmp")
	if err != nil {
		t.Fatalf("#2023: create temp fixture: %v", err)
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		t.Fatalf("#2023: chmod temp fixture: %v", err)
	}
	if _, err := tmp.Write(append(blob, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		t.Fatalf("#2023: write temp fixture: %v", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		t.Fatalf("#2023: close temp fixture: %v", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		t.Fatalf("#2023: rename temp fixture into place: %v", err)
	}

	// Counts, outcome names, the applied map and redactor-passed strings ONLY. Never
	// the record and never a payload: the scan above gates the FILE, not this log,
	// which the pipeline salvages. And never the scanner — two live credentials.
	t.Logf("#2023 outcome=%s observation_valid=%t terminated_on=%s stdout_lines=%d user_lines=%d "+
		"sidecar_present=%d sidecar_absent=%d spellings=%v key_sets=%d block_hist=%v block_types=%v "+
		"scan_not_applied=%v applied=%v\n  fixture: %s\n  %s",
		rec.Outcome, rec.SidecarObservationValid, rec.TerminatedOn, rec.StdoutLinesCaptured,
		rec.UserLinesRetained, rec.SidecarPresentCount, rec.SidecarAbsentCount,
		rec.SidecarKeySpellingCensus, len(rec.ObservedKeySets), rec.ToolResultBlockCountHistogram, rec.ContentBlockTypeCensus,
		notApplied, rec.CredentialScanApplied, red.str(path), rec.OutcomeDetail)
	for _, ks := range rec.ObservedKeySets {
		t.Logf("#2023: observed sidecar key set (x%d): %v", ks.Count, ks.Keys)
	}
}

// sidecapLocateHits narrows a hit to a CLASS plus an entry index — the two things
// the failure message may carry. It re-scans each entry alone and the record frame
// alone, so an operator learns where to extend the table without any value being
// printed. The frame arm is not decorative: #1260's first live capture's only hit
// was in the record's OWN metadata, not in a payload at all.
func sidecapLocateHits(scanner dropcapScanner, rec *sidecapRecord) []string {
	located := []string{}
	for _, e := range rec.UserLines {
		if blob, err := json.Marshal(e); err == nil {
			if h, _ := scanner.scan(blob); len(h) > 0 {
				located = append(located, fmt.Sprintf("user line entry %d %v", e.Index, h))
			}
		}
	}
	frame := *rec
	frame.UserLines = nil
	if blob, err := json.Marshal(frame); err == nil {
		if h, _ := scanner.scan(blob); len(h) > 0 {
			located = append(located, fmt.Sprintf("record frame (provenance/census/redaction fields, "+
				"no payload involved) %v", h))
		}
	}
	if len(located) == 0 {
		located = append(located, "not localised")
	}
	return located
}

// --- offline self-checks -----------------------------------------------------------

// TestSidecapInspect_ReadsTheSidecarOffAStdoutUserLine drives sidecapInspect over
// hand-written stdout `user` lines. It needs no claude and no credentials.
//
// The load-bearing rows are the ones a naive implementation gets wrong:
//
//   - "present but null" — a decode into a struct with a map-typed ToolUseResult
//     field reports null and absent IDENTICALLY as nil, which would let the live
//     probe publish "the sidecar is absent on stdout" against a claude that sends it
//     explicitly empty. Presence must come from key existence.
//   - "two tool_result blocks" — the one-sidecar-per-block assumption downstream is
//     exactly what the block count exists to test, so a count hard-wired to 1 (or to
//     "did the line have any block at all") must be red here.
//   - "string content" — a user message's content is not always an array, and a
//     silent 0 would read as "no tool_result blocks" rather than "not counted".
func TestSidecapInspect_ReadsTheSidecarOffAStdoutUserLine(t *testing.T) {
	t.Parallel()

	shellSet := []string{"interrupted", "isImage", "noOutputExpected", "stderr", "stdout"}

	tests := []struct {
		name string
		line string
		want sidecapShape
	}{
		{
			name: "sidecar absent, one tool_result block",
			line: `{"type":"user","session_id":"s","message":{"role":"user","content":[` +
				`{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
			want: sidecapShape{
				LineKeys:          []string{"message", "session_id", "type"},
				ToolUseResultKind: sidecapKindAbsent,

				ToolResultBlockCount: 1,
				ContentBlockTypes:    []string{"tool_result"},
			},
		},
		{
			name: "sidecar present as the shell key set",
			line: `{"type":"user","toolUseResult":{"stdout":"hi","stderr":"","interrupted":false,` +
				`"isImage":false,"noOutputExpected":false},"message":{"content":[` +
				`{"type":"tool_result","tool_use_id":"t1"}]}}`,
			want: sidecapShape{
				LineKeys:               []string{"message", "toolUseResult", "type"},
				ToolUseResultPresent:   true,
				ToolUseResultKey:       sidecapKeyCamel,
				ToolUseResultSpellings: []string{sidecapKeyCamel},
				ToolUseResultIsObject:  true,
				ToolUseResultKind:      sidecapKindObject,
				ToolUseResultKeys:      shellSet,
				ToolResultBlockCount:   1,
				ContentBlockTypes:      []string{"tool_result"},
			},
		},
		{
			name: "sidecar present as the file key set",
			line: `{"type":"user","toolUseResult":{"type":"text","file":{"filePath":"/w/f.txt",` +
				`"content":"x","numLines":1,"startLine":1,"totalLines":1}},"message":{"content":[` +
				`{"type":"tool_result","tool_use_id":"t2"}]}}`,
			want: sidecapShape{
				LineKeys:               []string{"message", "toolUseResult", "type"},
				ToolUseResultPresent:   true,
				ToolUseResultKey:       sidecapKeyCamel,
				ToolUseResultSpellings: []string{sidecapKeyCamel},
				ToolUseResultIsObject:  true,
				ToolUseResultKind:      sidecapKindObject,
				ToolUseResultKeys:      []string{"file", "type"},
				ToolResultBlockCount:   1,
				ContentBlockTypes:      []string{"tool_result"},
			},
		},
		{
			// THE SHAPE THE FIRST LIVE RUN ACTUALLY FOUND (claude 2.1.239). A probe
			// searching only the camelCase name reports this line as absent, which is
			// true of that key and false of the sidecar — the exact wrong conclusion to
			// hand the downstream decoder.
			name: "sidecar present under the snake_case spelling stdout uses",
			line: `{"type":"user","tool_use_result":{"type":"text","file":{"filePath":"/w/f.txt",` +
				`"content":"x","numLines":1,"startLine":1,"totalLines":1}},"message":{"content":[` +
				`{"type":"tool_result","tool_use_id":"t7"}]}}`,
			want: sidecapShape{
				LineKeys:               []string{"message", "tool_use_result", "type"},
				ToolUseResultPresent:   true,
				ToolUseResultKey:       sidecapKeySnake,
				ToolUseResultSpellings: []string{sidecapKeySnake},
				ToolUseResultIsObject:  true,
				ToolUseResultKind:      sidecapKindObject,
				ToolUseResultKeys:      []string{"file", "type"},
				ToolResultBlockCount:   1,
				ContentBlockTypes:      []string{"tool_result"},
			},
		},
		{
			// Both spellings on one line: the camelCase one supplies the kind and key
			// set, and BOTH appear in the spelling list rather than the second being
			// collapsed away.
			name: "both spellings on one line are both reported",
			line: `{"type":"user","toolUseResult":{"a":1},"tool_use_result":{"b":2},"message":{` +
				`"content":[{"type":"tool_result","tool_use_id":"t8"}]}}`,
			want: sidecapShape{
				LineKeys:               []string{"message", "toolUseResult", "tool_use_result", "type"},
				ToolUseResultPresent:   true,
				ToolUseResultKey:       sidecapKeyCamel,
				ToolUseResultSpellings: []string{sidecapKeyCamel, sidecapKeySnake},
				ToolUseResultIsObject:  true,
				ToolUseResultKind:      sidecapKindObject,
				ToolUseResultKeys:      []string{"a"},
				ToolResultBlockCount:   1,
				ContentBlockTypes:      []string{"tool_result"},
			},
		},
		{
			name: "sidecar present but null is NOT absent",
			line: `{"type":"user","toolUseResult":null,"message":{"content":[` +
				`{"type":"tool_result","tool_use_id":"t3"}]}}`,
			want: sidecapShape{
				LineKeys:               []string{"message", "toolUseResult", "type"},
				ToolUseResultPresent:   true,
				ToolUseResultKey:       sidecapKeyCamel,
				ToolUseResultSpellings: []string{sidecapKeyCamel},
				ToolUseResultKind:      sidecapKindNull,
				ToolResultBlockCount:   1,
				ContentBlockTypes:      []string{"tool_result"},
			},
		},
		{
			name: "sidecar present but a string",
			line: `{"type":"user","toolUseResult":"raw output","message":{"content":[` +
				`{"type":"tool_result","tool_use_id":"t4"}]}}`,
			want: sidecapShape{
				LineKeys:               []string{"message", "toolUseResult", "type"},
				ToolUseResultPresent:   true,
				ToolUseResultKey:       sidecapKeyCamel,
				ToolUseResultSpellings: []string{sidecapKeyCamel},
				ToolUseResultKind:      sidecapKindString,
				ToolResultBlockCount:   1,
				ContentBlockTypes:      []string{"tool_result"},
			},
		},
		{
			name: "two tool_result blocks on one line",
			line: `{"type":"user","toolUseResult":{"stdout":"a","stderr":"","interrupted":false,` +
				`"isImage":false,"noOutputExpected":false},"message":{"content":[` +
				`{"type":"tool_result","tool_use_id":"t5"},{"type":"tool_result","tool_use_id":"t6"}]}}`,
			want: sidecapShape{
				LineKeys:               []string{"message", "toolUseResult", "type"},
				ToolUseResultPresent:   true,
				ToolUseResultKey:       sidecapKeyCamel,
				ToolUseResultSpellings: []string{sidecapKeyCamel},
				ToolUseResultIsObject:  true,
				ToolUseResultKind:      sidecapKindObject,
				ToolUseResultKeys:      shellSet,
				ToolResultBlockCount:   2,
				ContentBlockTypes:      []string{"tool_result"},
			},
		},
		{
			name: "text-only user line carries no tool_result block",
			line: `{"type":"user","isSynthetic":true,"message":{"content":[` +
				`{"type":"text","text":"No output."}]}}`,
			want: sidecapShape{
				LineKeys:          []string{"isSynthetic", "message", "type"},
				ToolUseResultKind: sidecapKindAbsent,

				ToolResultBlockCount: 0,
				ContentBlockTypes:    []string{"text"},
			},
		},
		{
			name: "string content is marked, never silently zero",
			line: `{"type":"user","message":{"content":"hello"}}`,
			want: sidecapShape{
				LineKeys:          []string{"message", "type"},
				ToolUseResultKind: sidecapKindAbsent,

				ToolResultBlockCount: 0,
				ContentBlockTypes:    []string{sidecapNonArrayBlock},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sidecapInspect([]byte(tc.line))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("sidecapInspect mismatch\n got: %+v\nwant: %+v", got, tc.want)
			}
		})
	}
}

// TestSidecapScanApplied_RecordsAnArmedNothingClassForAnAbsentPath pins AC4's
// completion: a deny-scan class whose needle was empty must read as ARMED-NOTHING
// rather than vanish from credential_scan_applied, because an omitted class reads
// exactly like a class nobody ever thought about.
//
// artifact_dir is the class that would vanish — this probe mints no artifact
// directory, and addDynamicPath appends no needle for "" because
// dropcapPathSpellings returns nil, so applied() carries no key for it at all.
//
// THE workdir ROW IS THE STALENESS CONTROL and is the sole red for a completion
// that assigns false unconditionally: such a completion would satisfy the
// artifact_dir assertion while reporting every genuinely armed class as
// armed-nothing. operator_home is deliberately NOT asserted: realHome is read from
// the launching environment, so its arming is environment-dependent and an
// assertion either way would be flaky rather than binding.
func TestSidecapScanApplied_RecordsAnArmedNothingClassForAnAbsentPath(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	workdir := filepath.Join(t.TempDir(), sidecapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}

	// The middle slot is artifactDir, and it is empty for the same reason the live
	// probe leaves it empty: this family writes into testdata/, not into a minted
	// artifact directory. NEVER FORMAT THIS VALUE — it holds two live credentials.
	scanner := newDropcapScanner(tempHome, "", workdir)

	raw := scanner.applied()
	if _, ok := raw[dropcapClassArtifactDir]; ok {
		t.Fatalf("premise gone: dropcapScanner.applied() now carries a %q key for an empty path, so "+
			"sidecapScanApplied no longer closes a real hole. Re-derive AC4 against the new behaviour "+
			"rather than deleting the completion", dropcapClassArtifactDir)
	}

	applied := sidecapScanApplied(scanner)

	got, ok := applied[dropcapClassArtifactDir]
	if !ok {
		t.Fatalf("%q is MISSING from the completed map; an omitted class reads exactly like a class "+
			"nobody thought about, which is what this completion exists to prevent",
			dropcapClassArtifactDir)
	}
	if got {
		t.Fatalf("%q reads as armed, but no needle was ever appended for it", dropcapClassArtifactDir)
	}

	// The staleness control.
	if armed, ok := applied[dropcapClassWorkdir]; !ok || !armed {
		t.Fatalf("%q must read as ARMED (present=%t, armed=%t): a needle was appended for it, and a "+
			"completion that assigns false unconditionally would report every armed class as "+
			"armed-nothing while still satisfying the %q assertion above",
			dropcapClassWorkdir, ok, armed, dropcapClassArtifactDir)
	}
	if armed, ok := applied[dropcapClassTempHome]; !ok || !armed {
		t.Fatalf("%q must read as ARMED (present=%t, armed=%t)", dropcapClassTempHome, ok, armed)
	}
}
