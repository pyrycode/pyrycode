//go:build e2e_realclaude

package realclaude

// Evidence capture for #2247 — the verbatim payload of claude's
// system/task_notification line, the fourth background-task subtype and the one
// with no captured payload anywhere in this repo.
//
// # Why a fourth probe rather than a parameter on #1260's
//
// One structural delta, and it is the whole ticket. #1260's staging holds the FIFO
// through holdProbeFIFO, whose release is pinned to t.Cleanup, so the `cat` it
// backgrounds never finishes and the background task never reaches a terminal
// state. task_notification is documented to fire when a background task COMPLETES,
// fails or is stopped, so that staging can never produce one — and the committed
// dropped_lines_v2.1.220.json records exactly that: a census carrying
// system/task_started, system/task_updated and system/background_tasks_changed,
// with expected_absent naming task_notification alone.
//
// This probe releases the FIFO MID-TEST. `cat` sees EOF, the background task
// completes, and the terminal-state line is what lands. Everything else about the
// staging is #1260's, unchanged: bgIdlePrompt, BASH_DEFAULT_TIMEOUT_MS=5000 (the
// short default is what pushes the blocked call out of the foreground and into a
// background task in the first place), the YOLO interactive spawn shape.
//
// That the lever works is not a prediction. tool_progress_v2.1.259.json was taken
// on this same surface at this same claude release with a FIFO held and then
// released, and its line_type_census counts "system/task_notification": 1. Its
// frames array kept only tool_progress lines, so the bytes were not retained —
// which is why this file exists rather than a reader over that record.
//
// # What is REUSED rather than forked, and why two of them carry another
// ticket's prefix
//
// tpcapHoldFIFO and tpcapCensus are called here directly. The tpcap prefix marks
// the file an identifier was minted in, per this package's branch-hygiene rule; it
// is not private scope, and re-deriving either would be exactly the fork the ticket
// forbids.
//
//   - tpcapHoldFIFO is holdProbeFIFO with the release EXPOSED instead of pinned to
//     cleanup, which is the one difference this probe needs. Its body also encodes
//     a measured BSD/XNU wakeup fix — a transient read-open does not unpark a
//     writer blocked in open(O_WRONLY) there, because that shape re-tests
//     readers==0 after the wakeup — and it cost a hung live gate to find. Its
//     failure messages name #2089 because the mechanism is that ticket's.
//   - tpcapCensus produces the content-free whole-turn census AC1 asks for, plus
//     the tool-name and tool-error diagnostics that let a did-not-fire record say
//     what claude did INSTEAD.
//
// dropcapRecorder, dropcapRedactor, dropcapScanner, dropcapMakeEntry, parseOne,
// dropcapWaitForChild, newDropcapArgvHandler and bgIdlePrompt are #1260's and are
// likewise called, not copied.
//
// # Redaction
//
// Inherited whole from #1260 (dropcapRedactionRationale), plus one mechanism of
// this ticket's own. See tncapRedactionRationale: task_notification is documented
// to carry output_file, a path on the OPERATOR'S HOST, and that is a field class no
// record in this family has carried before.
//
// # Running it
//
// `make e2e-realclaude` on an authenticated machine, and nothing else. Like #2089's
// and unlike the env-gated siblings, this probe is gated on the FIXTURE'S ABSENCE,
// so the live gate the ticket is labelled for is what produces the evidence; it
// disarms as soon as the fixture exists. TestRealClaude_TaskNotificationCapture's
// gate comment argues that break.
//
// To force a re-capture at a new claude version, over an existing fixture:
//
//	PYRY_PROBE_TASK_NOTIFICATION_CAPTURE=1 go test -tags e2e_realclaude -timeout 15m -v \
//	  -run '^TestRealClaude_TaskNotificationCapture$' ./internal/e2e/realclaude/
//
// A skip carries no signal about pyry's behaviour — but read WHICH skip: "fixture
// already exists" is the steady state, while a skip from WithWorktreeAuthenticated
// means the machine has no claude login and the evidence was not produced.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// tncapEnableEnv FORCES a re-capture when the fixture already exists. It is not the
// gate — see the gate comment in TestRealClaude_TaskNotificationCapture.
const tncapEnableEnv = "PYRY_PROBE_TASK_NOTIFICATION_CAPTURE"

// tncapFixturePath is where a good capture LANDS, in-repo, ready to commit. The
// streamsup-side reader names the same file through its own package constant and
// enforces the same version pin from the other end; the two are deliberately not
// shared, because that reader takes no path parameter by design.
const (
	tncapFixtureVersion = "2.1.259"
	tncapFixturePath    = "testdata/task_notification_v" + tncapFixtureVersion + ".json"
)

// Every file-local identifier takes the tncap prefix, for the reason #1260's header
// gives: siblings add files to this package concurrently and a branch-overlap check
// does not catch a same-package identifier collision.
const (
	tncapTicket         = "2247"
	tncapWorkdirName    = "tncap-work"
	tncapFIFOName       = "tncap-hold"
	tncapRecordName     = "tncap-record.json"
	tncapArtifactPrefix = "pyry-2247-capture-*"
	tncapModel          = "haiku"
	// #1260's value, deliberately. A five-second default is what makes the blocked
	// `cat` time out of the foreground and become a BACKGROUND TASK, which is the
	// thing that has to exist before it can terminate. #2089 raises this to 180000
	// for the opposite purpose — keeping a call in the foreground — and copying that
	// value here would stage a turn with no background task in it at all.
	tncapBashTimeoutMS = "5000"
	// A fixed literal in a per-test temp $HOME, not a secret. Distinct from #1260's
	// and #2089's so a record can never be mistaken for either probe's.
	tncapSessionID = "3e7a5d94-0c61-4f2b-9a83-5b1e6d0f47c2"
)

const (
	tncapTurnBudget  = 4 * time.Minute
	tncapRunExitWait = 30 * time.Second
	// How long to wait for claude's `cat` to open the FIFO. Generous: it covers
	// model latency and the tool round-trip, and overrunning it is recorded as "the
	// foreground call never started", which is a measurement rather than a flake.
	tncapRendezvousWait = 90 * time.Second
	// How long to wait, after the rendezvous, for claude to background the timed-out
	// call. BASH_DEFAULT_TIMEOUT_MS is 5 s, so this is twenty times the interval it
	// waits on; overrunning it means claude kept the call in the foreground, which
	// stagingVerdict reports as a rig failure rather than a surface finding.
	tncapBackgroundWait = 100 * time.Second
	// How long to wait, after the FIFO is released and `cat` can finally exit, for
	// the terminal-state line. This is the ONE wait whose overrun is evidence about
	// pyry's surface rather than about the staging.
	tncapNotificationWait = 90 * time.Second
	// dropcapRecorder exposes only a `result` signal, so the two per-subtype waits
	// above poll its snapshot. Teaching the recorder a second channel would be a
	// fork of a helper every probe in this package shares.
	tncapPoll = 250 * time.Millisecond
)

const (
	tncapFired            = "fired"
	tncapDidNotFire       = "did-not-fire"
	tncapInstrumentBroken = "instrument-broken"
)

const (
	tncapTerminatedResult = "result"
	tncapTerminatedBudget = "budget"
)

// The subtypes, spelled here as literals rather than imported from streamsup: the
// record is EVIDENCE about claude's wire shape, and a census keyed on the production
// constants would agree with the matcher by construction instead of measuring it.
//
// The quarry and the companions are separate names because they play different
// roles in the promotion rule — zero quarry frames refuses the fixture, zero
// companion frames is a recorded measurement under AC2 — and a single slice split
// positionally would make that distinction an index rather than a name.
const tncapQuarrySubtype = "task_notification"

var tncapCompanionSubtypes = []string{"task_started", "task_updated", "background_tasks_changed"}

// tncapAmbientKey and tncapSkipTranscriptKey are the two keys AC2 measures. A host
// is documented to use them to hide housekeeping tasks from activity indicators;
// dropped_lines_v2.1.220.json shows NEITHER on any of the three companion lines, and
// the shipped decode targets systemTaskStartedLine and systemBackgroundTaskEntry
// declare neither. Whether claude 2.1.259 sends them is what this record settles.
const (
	tncapAmbientKey        = "ambient"
	tncapSkipTranscriptKey = "skip_transcript"
)

// tncapDocumentedKeys is what @anthropic-ai/claude-agent-sdk@0.3.263's sdk.d.ts
// describes for SDKTaskNotificationMessage, measured 2026-09-07.
//
// IT IS A THING TO CHECK THE CAPTURE AGAINST, NEVER A FIELD SET TO DECLARE FROM.
// The family's rule, stated in systemTaskStartedLine's doc, is that the field set is
// exactly what the committed capture shows and nothing invented from a docs page, so
// this list reaches the record only as the two set DIFFERENCES against what claude
// actually sent. The envelope keys claude puts on every line of the family — type,
// subtype, uuid, session_id — are absent from it and will show up as observed-and-
// undocumented, which is the expected shape rather than a finding.
var tncapDocumentedKeys = []string{
	"ambient", "output_file", "skip_transcript", "status", "summary",
	"task_id", "tool_use_id", "usage",
}

var tncapArgs = []string{"--model", tncapModel, "--dangerously-skip-permissions"}

const tncapSpawnShapeDelta = "The YOLO interactive shape, identical to #1260's and #2089's — see " +
	"dropcapSpawnShapeDelta for what production's non-yolo spawn adds and what that implies for " +
	"system/init. Nothing about the background-task family is known to depend on the approval flags; " +
	"that is UNMEASURED, not ruled out."

const tncapLimitations = "One turn, one spawn shape, one claude version, one model (" + tncapModel + "), " +
	"one tool (Bash), one terminal state. Cross-version, cross-model and cross-tool stability are " +
	"UNMEASURED. The status this record observes is whatever a `cat` reaching EOF produces; the " +
	"documented `failed` and `stopped` states are NOT staged here and nothing in this record says what " +
	"they carry. A companion subtype absent from key_presence's fired set did not appear in THIS turn, " +
	"which is not the same as claude never emitting it — dropped_lines_v2.1.220.json fired all three " +
	"under a staging that never let the task finish, and tool_progress_v2.1.259.json fired task_started " +
	"alone, so per-turn variation in that family is measured rather than hypothetical."

const tncapRedactionRationale = "Inherited whole from #1260 (see dropcapRedactionRationale): a fresh " +
	"empty non-git workdir, a rig-authored prompt (bgIdlePrompt), a `cat <fifo>` that produces no " +
	"output, no os.Environ() read into the record, the declared dropcapRedactor substitution table over " +
	"every string, and dropcapScanner as a fail-closed deny-scan over the marshalled record. " +
	"WHAT THIS SUBTYPE SPECIFICALLY CAN CARRY, named so a person deciding whether to paste this record " +
	"into a public issue is told rather than left to infer: task_notification is documented to carry " +
	"output_file, A PATH ON THE OPERATOR'S HOST, and that is a field class no record in this family has " +
	"carried before. The two inherited mechanisms cover the classes they know — the temp $HOME, " +
	"os.TempDir(), the workdir, the operator's real home, and the deny-scan's fixed /Users/, /home/, " +
	"/var/folders/ and /private/var/folders/ prefixes, any of which fails the whole write closed. A path " +
	"under a prefix none of those know would pass both, so tncapUnredactedPathFields is a third " +
	"mechanism of its own: it refuses to PROMOTE a record whose quarry frames still carry a value " +
	"beginning with '/' after redaction, and names the field rather than the value. A refusal costs one " +
	"live turn; a promotion costs a public leak. " +
	"DELIBERATELY KEPT, because removing them would defeat the ticket: the subtype's own structural " +
	"fields, claude's task and tool identifiers, the task description (which is the rig's own `cat " +
	"$FIFO` command line), status tokens, token counts and durations."

// --- the record --------------------------------------------------------------

// tncapFrame is one captured background-task line. The payload half is built by
// dropcapMakeEntry so the base64 arm for invalid UTF-8 is shared rather than
// re-derived; the rest is this ticket's.
//
// keys is the line's own top-level key set, sorted, and it is what AC2's two
// booleans are DERIVED from rather than decoded alongside — a presence flag computed
// independently of the key list it claims to summarise is a flag that can disagree
// with its own evidence.
//
// events_emitted is the SHIPPED parser's verdict (parseOne), never a mirror of its
// tables. Every one of these subtypes is expected to read zero today; a non-zero
// count on the quarry would mean the drop set already moved under this ticket.
type tncapFrame struct {
	Index                   int    `json:"index"`
	Type                    string `json:"type"`
	Subtype                 string `json:"subtype"`
	PayloadLenBytesCaptured int    `json:"payload_len_bytes_captured"`
	PayloadLenBytes         int    `json:"payload_len_bytes"`
	PayloadEncoding         string `json:"payload_encoding"`
	Payload                 string `json:"payload,omitempty"`
	PayloadB64              string `json:"payload_b64,omitempty"`

	EventsEmitted        int      `json:"events_emitted"`
	Keys                 []string `json:"keys"`
	AmbientPresent       bool     `json:"ambient_present"`
	SkipTranscriptResent bool     `json:"skip_transcript_present"`
}

// tncapPresence is AC2's answer for ONE companion subtype. It is emitted for all
// three whether or not they fired, so a did-not-fire is a recorded measurement
// rather than an absence a later reader has to interpret.
type tncapPresence struct {
	Subtype                 string `json:"subtype"`
	Fired                   bool   `json:"fired"`
	LineCount               int    `json:"line_count"`
	LinesWithAmbient        int    `json:"lines_with_ambient"`
	LinesWithSkipTranscript int    `json:"lines_with_skip_transcript"`
	Note                    string `json:"note"`
}

type tncapRecord struct {
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

	Outcome       string `json:"outcome"`
	OutcomeDetail string `json:"outcome_detail"`
	TerminatedOn  string `json:"terminated_on"`

	// The staging measurement. Without these a did-not-fire record cannot separate
	// "claude never ran the command" from "it ran but was never backgrounded" from
	// "a background task existed, was allowed to finish, and no terminal-state line
	// came" — and only the third is a finding about pyry's surface. #2089's first
	// live run came back with zero frames and no way to tell which, which is what
	// put these fields in its record and in this one.
	ForegroundCallObserved bool    `json:"foreground_call_observed"`
	BackgroundTaskObserved bool    `json:"background_task_observed"`
	HeldSeconds            float64 `json:"fifo_held_seconds"`
	TurnSeconds            float64 `json:"turn_seconds"`

	// A content-free census of everything else on the wire, from tpcapCensus.
	LineTypeCensus   map[string]int `json:"line_type_census"`
	ToolCalls        []string       `json:"tool_calls"`
	ToolResultErrors int            `json:"tool_result_errors"`
	UndecodedLines   int            `json:"undecoded_lines"`

	LinesCaptured          int             `json:"lines_captured"`
	FrameCount             int             `json:"frame_count"`
	NotificationFrameCount int             `json:"notification_frame_count"`
	Frames                 []tncapFrame    `json:"frames"`
	KeyPresence            []tncapPresence `json:"key_presence"`

	ObservedNotificationKeys   []string `json:"observed_notification_keys"`
	DocumentedNotificationKeys []string `json:"documented_notification_keys"`
	KeysDocumentedNotObserved  []string `json:"keys_documented_not_observed"`
	KeysObservedNotDocumented  []string `json:"keys_observed_not_documented"`
	UnredactedPathFields       []string `json:"unredacted_path_fields"`

	FixtureStaged      bool   `json:"fixture_staged"`
	FixtureStageDetail string `json:"fixture_stage_detail"`

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

func (rec *tncapRecord) set(outcome, format string, args ...any) {
	rec.Outcome = outcome
	rec.OutcomeDetail = fmt.Sprintf(format, args...)
}

// fixtureWorthy answers whether this record may be promoted to tncapFixturePath,
// and names the reason when it may not.
//
// THE NON-VACUITY RULE IS KEYED ON THE QUARRY ALONE. A record holding zero
// task_notification frames is refused, because every assertion built on it would
// pass without reading a byte claude sent. A record whose COMPANION subtypes did not
// fire is promoted: that is a legitimate recorded measurement under AC2, and
// tool_progress_v2.1.259.json is the precedent — a real turn on this surface that
// fired task_started and neither of the other two.
//
// The version arm is the producing half of the pin the streamsup reader enforces
// (taskNotificationCaptureVersion). `claude --version` prints "<version> (Claude
// Code)", so the comparison is on the leading token. Refusing to write under the
// wrong name turns a claude upgrade into an instruction to repin, instead of a file
// whose key set silently describes a different release.
func (rec *tncapRecord) fixtureWorthy() (string, bool) {
	if rec.Outcome != tncapFired {
		return fmt.Sprintf("outcome=%s", rec.Outcome), false
	}
	if rec.NotificationFrameCount == 0 {
		return fmt.Sprintf("zero system/%s frames — a vacuous fixture proves nothing",
			tncapQuarrySubtype), false
	}
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != tncapFixtureVersion {
		return fmt.Sprintf("claude_version %q is not the %s pinned in the fixture name — repin "+
			"tncapFixtureVersion and taskNotificationCaptureVersion together, then re-run",
			got, tncapFixtureVersion), false
	}
	// The consumer refuses any encoding but json-string, and dropcapMakeEntry emits
	// base64 with an EMPTY payload for a frame that is not valid UTF-8. Promoting one
	// would land a fixture that reddens `make check` for every unrelated ticket.
	for _, f := range rec.Frames {
		if f.PayloadEncoding != dropcapEncodingJSONString {
			return fmt.Sprintf("frame %d is encoded %q, and the streamsup reader reads only %q — a "+
				"non-UTF-8 frame carries no readable payload, so a fixture holding one would fail the "+
				"assertion it exists to feed", f.Index, f.PayloadEncoding, dropcapEncodingJSONString), false
		}
	}
	// The third redaction mechanism, and the only one this ticket adds. See
	// tncapRedactionRationale: output_file is a documented path on the operator's
	// host and a field class no record in this family has carried, so a value the
	// declared table did not know and the deny-scan's fixed prefixes did not match
	// stops the promotion here rather than reaching a public artefact.
	if len(rec.UnredactedPathFields) > 0 {
		return fmt.Sprintf("the %s payload carries an absolute host path in %v after redaction. Extend "+
			"dropcapRedactor's table with the class that value belongs to and re-run; the FIELD is named "+
			"and the value deliberately is not, because printing it is the exposure this refusal exists "+
			"to prevent", tncapQuarrySubtype, rec.UnredactedPathFields), false
	}
	return "", true
}

// stagingVerdict names WHICH of the three ways a zero-notification capture can
// happen actually happened.
//
// Only the third case is evidence about pyry's surface. The first two are the rig
// failing to stage the thing it meant to measure, and saying so plainly is what
// stops the next reader from loosening the promotion rule to fix a staging bug —
// which is the failure #2089's first live run walked right up to.
func (rec *tncapRecord) stagingVerdict() string {
	switch {
	case !rec.ForegroundCallObserved:
		return fmt.Sprintf("claude never opened the FIFO within %s, so no Bash call ever started. The "+
			"staging failed BEFORE any background task could exist — read tool_calls and "+
			"line_type_census to see what claude did instead", tncapRendezvousWait)
	case !rec.BackgroundTaskObserved:
		return fmt.Sprintf("a Bash call started but no system/task_started line arrived within %s, so "+
			"the call was never backgrounded and there was no background task to terminate. "+
			"BASH_DEFAULT_TIMEOUT_MS is %s ms: claude kept the call in the foreground or requested a "+
			"timeout of its own. The staging failed, not the surface",
			tncapBackgroundWait, tncapBashTimeoutMS)
	default:
		return fmt.Sprintf("a background task was started, the FIFO was held %.1fs and then RELEASED so "+
			"`cat` could reach EOF and the task could complete, and no system/%s line arrived in the %s "+
			"that followed. The staging WORKED, so this is a finding about the surface — the terminal "+
			"state does not reach --output-format stream-json stdout here — and it is to be routed back, "+
			"not fixed by loosening the promotion rule",
			rec.HeldSeconds, tncapQuarrySubtype, tncapNotificationWait)
	}
}

// --- decoding one line -------------------------------------------------------

// tncapKeys returns one captured line's own top-level key set, sorted, plus
// membership of the two keys AC2 measures.
//
// json.RawMessage rather than a typed target so the key set is CLAUDE'S rather than
// this file's: a struct would silently drop every key it does not declare, which is
// the exact failure the whole family's declare-only-what-you-captured rule exists to
// prevent. The two booleans are read out of the same map the key list comes from, so
// neither can disagree with the other.
//
// Presence means the key EXISTS on the object. A key present with a null value
// counts as present and its value is visible in the payload the record keeps, which
// is the right split: whether claude sends the key at all is this ticket's question,
// and what it puts in it is #2245's.
func tncapKeys(raw []byte) (keys []string, ambient, skipTranscript bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false, false
	}
	keys = make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	_, ambient = obj[tncapAmbientKey]
	_, skipTranscript = obj[tncapSkipTranscriptKey]
	return keys, ambient, skipTranscript
}

// tncapUnredactedPathFields returns the FIELD PATHS, never the values, at which a
// quarry frame still carries a string beginning with '/' after redaction.
//
// This is the third redaction mechanism and the only one this ticket adds; see
// tncapRedactionRationale for the gap it closes. Field names are claude's vocabulary
// and appear in nobody's data, so naming them is safe in a way naming a value never
// is — the same distinction the deny-scan draws when it reports a CLASS.
//
// It walks to any depth and through arrays, because output_file is documented at the
// top level but usage is a nested object and nothing guarantees a host path stays
// where the docs put it.
func tncapUnredactedPathFields(frames []tncapFrame) []string {
	found := map[string]bool{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch n := v.(type) {
		case string:
			if strings.HasPrefix(n, "/") {
				found[prefix] = true
			}
		case []any:
			for _, e := range n {
				walk(prefix+"[]", e)
			}
		case map[string]any:
			for k, e := range n {
				child := k
				if prefix != "" {
					child = prefix + "." + k
				}
				walk(child, e)
			}
		}
	}
	for _, f := range frames {
		if f.Subtype != tncapQuarrySubtype || f.Payload == "" {
			continue
		}
		var doc any
		if err := json.Unmarshal([]byte(f.Payload), &doc); err != nil {
			// An undecodable quarry payload cannot be swept, and a promotion that
			// silently skipped it would be the hole this function exists to close.
			found[fmt.Sprintf("frame %d: payload does not decode, so it could not be swept", f.Index)] = true
			continue
		}
		walk("", doc)
	}
	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tncapPresenceTable is AC2: for each of the three companion subtypes, whether it
// fired in this turn and, if it did, how many of its lines carried each of the two
// keys.
//
// EVERY companion gets an entry whether or not it fired. An omitted subtype and one
// that fired carrying neither key look identical to a later reader, and only one of
// those is a measurement.
func tncapPresenceTable(frames []tncapFrame) []tncapPresence {
	out := make([]tncapPresence, 0, len(tncapCompanionSubtypes))
	for _, subtype := range tncapCompanionSubtypes {
		p := tncapPresence{Subtype: subtype}
		for _, f := range frames {
			if f.Subtype != subtype {
				continue
			}
			p.Fired = true
			p.LineCount++
			if f.AmbientPresent {
				p.LinesWithAmbient++
			}
			if f.SkipTranscriptResent {
				p.LinesWithSkipTranscript++
			}
		}
		switch {
		case !p.Fired:
			p.Note = "did not fire in this turn; nothing here says whether claude sends these keys on " +
				"this subtype, only that this turn carried no line to read them from"
		case p.LinesWithAmbient == 0 && p.LinesWithSkipTranscript == 0:
			p.Note = "fired carrying NEITHER key, matching dropped_lines_v2.1.220.json"
		default:
			p.Note = "fired carrying at least one of the two keys — read the frames' own key lists"
		}
		out = append(out, p)
	}
	return out
}

// tncapKeyDiff returns the entries of a that are absent from b, sorted.
func tncapKeyDiff(a, b []string) []string {
	inB := map[string]bool{}
	for _, s := range b {
		inB[s] = true
	}
	out := []string{}
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// tncapCollect keeps every captured line of the four background-task subtypes,
// whatever the parser did with it. The filter is deliberately the TYPE and SUBTYPE
// rather than the parser's verdict: filtering on "dropped" — dropcapClassifyAll's
// rule — would discard a subtype the parser had started mapping, and the verdict
// rides along per frame as data instead.
func tncapCollect(t *testing.T, lines []dropcapCaptured, red *dropcapRedactor) []tncapFrame {
	t.Helper()
	wanted := map[string]bool{tncapQuarrySubtype: true}
	for _, s := range tncapCompanionSubtypes {
		wanted[s] = true
	}
	out := []tncapFrame{}
	for _, c := range lines {
		if !c.Decoded || c.Type != "system" || !wanted[c.Subtype] {
			continue
		}
		// The payload half, shared with #1260 so the base64 arm for invalid UTF-8 is
		// not re-derived here. The reason field it wants is spent below on the key
		// set, so it is passed empty and dropped.
		entry := dropcapMakeEntry(c, "", red)
		// Read out of the REDACTED payload rather than out of c.Raw, so the key set
		// and the path sweep both describe the bytes the record actually carries. A
		// frame that was not valid UTF-8 has an EMPTY Payload and its base64 twin
		// instead, so its key set reads empty here; fixtureWorthy refuses to promote
		// a record holding one, and payload_encoding on the frame says which it is.
		keys, ambient, skipTranscript := tncapKeys([]byte(entry.Payload))
		out = append(out, tncapFrame{
			Index:                   entry.Index,
			Type:                    entry.Type,
			Subtype:                 entry.Subtype,
			PayloadLenBytesCaptured: entry.PayloadLenBytesCaptured,
			PayloadLenBytes:         entry.PayloadLenBytes,
			PayloadEncoding:         entry.PayloadEncoding,
			Payload:                 entry.Payload,
			PayloadB64:              entry.PayloadB64,
			EventsEmitted:           len(parseOne(t, string(c.Raw))),
			Keys:                    keys,
			AmbientPresent:          ambient,
			SkipTranscriptResent:    skipTranscript,
		})
	}
	return out
}

// tncapAwaitSubtype polls the recorder's snapshot until a system line of the given
// subtype has been captured, the turn ends, or the budget runs out.
//
// A poll rather than a signal because dropcapRecorder exposes only `result`, and
// teaching it a second channel would fork a helper every probe in this package
// shares — for a wait whose granularity does not matter: the two callers are bounded
// in tens of seconds and one tick is a quarter of a second.
func tncapAwaitSubtype(recorder *dropcapRecorder, subtype string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		lines, _ := recorder.snapshot()
		for _, c := range lines {
			if c.Decoded && c.Type == "system" && c.Subtype == subtype {
				return true
			}
		}
		select {
		case <-recorder.resultSeen:
			// The turn is over, so nothing more will arrive. One last look, because
			// the line may have landed in the same batch as the result.
			lines, _ = recorder.snapshot()
			for _, c := range lines {
				if c.Decoded && c.Type == "system" && c.Subtype == subtype {
					return true
				}
			}
			return false
		case <-time.After(tncapPoll):
		}
		if !time.Now().Before(deadline) {
			return false
		}
	}
}

// --- the live capture --------------------------------------------------------

// TestRealClaude_TaskNotificationCapture drives the turn and writes the record.
//
// Ordering below is load-bearing in three places, and each wrong order fails
// SILENTLY rather than loudly:
//
//   - newDropcapScanner reads CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY via
//     os.Getenv AS DENY NEEDLES, and WithWorktreeAuthenticated is what re-pins them
//     into this process's environment. Building the scanner first yields an EMPTY
//     needle, which dropcapScanner.scan reports as notApplied — skipped, not fatal.
//     The credential net would be off while every message read green.
//   - The record-writing cleanup is registered FIRST in this body, so t.Cleanup's
//     LIFO runs it LAST and a structural t.Fatalf still leaves the evidence on disk.
//   - The FIFO hold is registered BEFORE the runner, so its release cleanup runs
//     AFTER the runner's cancel — the descendant reap kills the backgrounded `cat`,
//     and closing the last write end is the backstop if the reap missed.
func TestRealClaude_TaskNotificationCapture(t *testing.T) {
	// THE GATE IS THE FIXTURE'S ABSENCE, AND THAT IS A DELIBERATE BREAK FROM THE
	// ENV-GATED PROBES IN THIS PACKAGE, copied from #2089's.
	//
	// Under the env-gated shape `make e2e-realclaude` never sets the variable, so the
	// probe skips on the ENV check before it ever reaches the credential check, the
	// live gate passes vacuously, and the fixture never lands. That is CLAUDE.md
	// § Testing's #1763 failure exactly: a green gate and a spent budget look
	// identical whether the bytes landed or not. Three of this ticket's five
	// acceptance criteria turn on the fixture existing.
	//
	// So the probe ARMS ITSELF while the fixture is absent and DISARMS once it
	// exists. The env var survives as a FORCE, for re-capturing at a new claude
	// version.
	force := os.Getenv(tncapEnableEnv) == "1"
	if _, err := os.Stat(tncapFixturePath); err == nil && !force {
		t.Skipf("#2247 task_notification capture: the fixture %s already exists, so there is nothing "+
			"to capture and this costs no claude turn.\n"+
			"Force a re-capture (a new claude version, or a suspected shape change) with:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 15m -v \\\n"+
			"    -run '^TestRealClaude_TaskNotificationCapture$' ./internal/e2e/realclaude/",
			tncapFixturePath, tncapEnableEnv)
	}

	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t) // t.Skip when no credentials; MUST precede the scanner

	// Deliberately NOT t.TempDir(): the operator needs the record after the test ends
	// in order to commit it as the fixture, and this directory is also the ONLY copy
	// that survives a run in a worktree the dispatcher discards.
	artifactDir, err := os.MkdirTemp("", tncapArtifactPrefix)
	if err != nil {
		t.Fatalf("#2247: create artifact dir: %v", err)
	}

	// A fresh EMPTY directory, deliberately not a git repo: no branch names and no
	// file contents can reach a payload.
	workdir := filepath.Join(home, tncapWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2247: create workdir: %v", err)
	}
	nonce := time.Now().UnixNano()
	fifoPath := filepath.Join(workdir, tncapFIFOName)

	red := newDropcapRedactor(home, artifactDir, workdir, fifoPath, tncapSessionID, nonce)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	t.Logf("#2247 capture artifacts: %s", red.str(artifactDir))

	rec := &tncapRecord{
		Ticket:        tncapTicket,
		ClaudeVersion: probeClaudeVersion(claudeBin),
		CapturedAt:    time.Now().Format(time.RFC3339),
		IsCapture:     true,
		Model:         tncapModel,
		// A FIXED LITERAL, never harvested from os.Environ().
		EnvDelta:                   []string{dropcapBashTimeoutEnv + "=" + tncapBashTimeoutMS},
		SpawnShapeDelta:            tncapSpawnShapeDelta,
		Workdir:                    red.str(workdir),
		Frames:                     []tncapFrame{},
		KeyPresence:                []tncapPresence{},
		ObservedNotificationKeys:   []string{},
		DocumentedNotificationKeys: tncapDocumentedKeys,
		UnredactedPathFields:       []string{},
		RedactionRationale:         tncapRedactionRationale,
		CredentialScanApplied:      scanner.applied(),
		CredentialScanSkipped:      []string{},
		Limitations:                tncapLimitations,
	}
	rec.set(tncapInstrumentBroken, "did not reach a classification point")

	t.Cleanup(func() { tncapWriteRecord(t, artifactDir, red, scanner, rec) })

	// MUST precede the runner: Config.Env stays nil so the child inherits this
	// process's environment verbatim.
	t.Setenv(dropcapBashTimeoutEnv, tncapBashTimeoutMS)

	rendezvous, releaseFIFO := tpcapHoldFIFO(t, fifoPath)

	recorder := newDropcapRecorder()
	argvHandler, argv := newDropcapArgvHandler()
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin: claudeBin,
		WorkDir:   workdir,
		SessionID: tncapSessionID,
		Args:      tncapArgs,
		Stdout:    recorder,
		Logger:    slog.New(argvHandler),
	})
	if err != nil {
		rec.set(tncapInstrumentBroken, "streamsup.New failed, so no claude was ever spawned: %v",
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
		case <-time.After(tncapRunExitWait):
			t.Errorf("#2247: streamsup.Run did not return within %s of cancel", tncapRunExitWait)
		}
	})

	stdin := dropcapWaitForChild(runner)
	if stdin == nil {
		rec.set(tncapInstrumentBroken, "no live child within %s: claude never spawned, so nothing was "+
			"on the wire to capture", dropcapSpawnWait)
		return
	}

	prompt := bgIdlePrompt(fifoPath, nonce)
	rec.Prompt = red.str(prompt)
	turnStart := time.Now()
	turnBudget := time.NewTimer(tncapTurnBudget)
	defer turnBudget.Stop()
	if err := streamsup.WriteTurn(ctx, stdin, []byte(prompt)); err != nil {
		rec.set(tncapInstrumentBroken, "writing the turn envelope failed, so no turn was ever driven: %v",
			red.str(err.Error()))
		return
	}

	// Phase 1 — wait for claude's `cat` to open the FIFO. Nothing else in this rig
	// opens it, so that open is unambiguous evidence a Bash call started.
	var heldFrom time.Time
	select {
	case <-rendezvous:
		rec.ForegroundCallObserved = true
		heldFrom = time.Now()
	case <-recorder.resultSeen:
	case <-time.After(tncapRendezvousWait):
	}

	// Phase 2 — wait for claude to background the timed-out call. This is the task
	// that has to EXIST before it can terminate, and its absence is a staging failure
	// rather than a finding.
	if rec.ForegroundCallObserved {
		rec.BackgroundTaskObserved = tncapAwaitSubtype(recorder, "task_started", tncapBackgroundWait)
	}

	// Phase 3 — RELEASE. This is the whole delta from #1260's staging: `cat` sees
	// EOF, the background task completes, and the terminal-state line is what this
	// probe exists to catch.
	releaseFIFO()
	// Left at zero when the rendezvous never fired: nothing was ever held open, and a
	// duration measured from before the wait would read as a hold that happened.
	if rec.ForegroundCallObserved {
		rec.HeldSeconds = time.Since(heldFrom).Seconds()
	}

	// Phase 4 — wait for the terminal-state line, then for the turn to end.
	tncapAwaitSubtype(recorder, tncapQuarrySubtype, tncapNotificationWait)
	select {
	case <-recorder.resultSeen:
		rec.TerminatedOn = tncapTerminatedResult
	case <-turnBudget.C:
		rec.TerminatedOn = tncapTerminatedBudget
	}
	rec.TurnSeconds = time.Since(turnStart).Seconds()

	rec.SpawnShape = red.strs(argv())
	lines, caps := recorder.snapshot()
	rec.LinesCaptured = len(lines)
	rec.LinesDroppedOverCap = caps.LinesOverCap
	rec.BytesDroppedOverCap = caps.BytesOverCap
	rec.PartialsDropped = caps.PartialsDropped
	rec.BlankLines = caps.BlankLines
	rec.UnterminatedPartialLen = caps.UnterminatedPartial

	// tpcapCensus is #2089's and is CALLED rather than re-derived; the tpcap prefix
	// marks the file it was minted in, not private scope. See this file's header.
	rec.LineTypeCensus, rec.ToolCalls, rec.ToolResultErrors, rec.UndecodedLines = tpcapCensus(lines, red)
	rec.Frames = tncapCollect(t, lines, red)
	rec.FrameCount = len(rec.Frames)
	rec.KeyPresence = tncapPresenceTable(rec.Frames)
	rec.UnredactedPathFields = tncapUnredactedPathFields(rec.Frames)

	observed := map[string]bool{}
	for _, f := range rec.Frames {
		if f.Subtype != tncapQuarrySubtype {
			continue
		}
		rec.NotificationFrameCount++
		for _, k := range f.Keys {
			observed[k] = true
		}
	}
	for k := range observed {
		rec.ObservedNotificationKeys = append(rec.ObservedNotificationKeys, k)
	}
	sort.Strings(rec.ObservedNotificationKeys)
	rec.KeysDocumentedNotObserved = tncapKeyDiff(tncapDocumentedKeys, rec.ObservedNotificationKeys)
	rec.KeysObservedNotDocumented = tncapKeyDiff(rec.ObservedNotificationKeys, tncapDocumentedKeys)

	if rec.NotificationFrameCount == 0 {
		rec.set(tncapDidNotFire, "the turn produced no system/%s line at all; %s",
			tncapQuarrySubtype, rec.stagingVerdict())
	} else {
		rec.set(tncapFired, "%d system/%s frame(s), keys %v", rec.NotificationFrameCount,
			tncapQuarrySubtype, rec.ObservedNotificationKeys)
	}

	// --- AC4 ------------------------------------------------------------------
	// Counts, indices, censuses and key NAMES only. The frames themselves are in the
	// record the cleanup has already written; putting claude's bytes in CI output is
	// precisely the exposure the deny-scan exists to prevent.
	if rec.NotificationFrameCount == 0 {
		t.Fatalf("#2247: the turn recorded ZERO system/%s frames out of %d captured lines "+
			"(terminated_on=%s, turn=%.1fs). A capture recording none is vacuous — every assertion built "+
			"on it would pass without reading a byte claude sent, and #2245 would have nothing to declare "+
			"a decode target from.\n"+
			"  staging: %s\n"+
			"  background-task frames: %d; key_presence: %v\n"+
			"  line types: %v; tools called: %v; tool_result errors: %d; undecoded: %d\n"+
			"Read the staging line FIRST: only the last of its three cases is a finding about pyry's "+
			"surface, and the other two are the rig failing to background a command and let it finish",
			tncapQuarrySubtype, rec.LinesCaptured, rec.TerminatedOn, rec.TurnSeconds, rec.stagingVerdict(),
			rec.FrameCount, rec.KeyPresence, rec.LineTypeCensus, rec.ToolCalls, rec.ToolResultErrors,
			rec.UndecodedLines)
	}
}

// --- writing the record ------------------------------------------------------

// tncapWriteRecord marshals, deny-scans, writes, promotes and stages. #1260's
// fail-closed rule verbatim: on a hit NOTHING is written and the message names the
// CLASS only, never the matched value.
func tncapWriteRecord(t *testing.T, dir string, red *dropcapRedactor, scanner dropcapScanner, rec *tncapRecord) {
	t.Helper()
	rec.Redaction = red.substitutions()

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2247: marshal record: %v", err)
		return
	}
	hits, notApplied := scanner.scan(blob)
	// A base64 payload hides its bytes from a scan of the marshalled record, so the
	// decoded bytes are scanned too.
	for _, f := range rec.Frames {
		if f.PayloadB64 == "" {
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(f.PayloadB64)
		if derr != nil {
			t.Errorf("#2247: frame %d: decode base64 payload for the scan: %v", f.Index, derr)
			return
		}
		if h, _ := scanner.scan(decoded); len(h) > 0 {
			hits = append(hits, h...)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("#2247: deny-scan found %d denied class(es) still present in the record: %v\n"+
			"NOTHING was written — not the record, not the fixture. Extend dropcapRedactor's table with "+
			"the named class and re-run the capture. The offending value is deliberately not printed: "+
			"putting it in CI output is exactly the exposure this scan exists to prevent", len(hits), hits)
	}
	rec.CredentialScanSkipped = notApplied

	// Promotion is decided BEFORE the final marshal so fixture_staged and
	// fixture_stage_detail ship inside the bytes that land, in the record and in the
	// fixture alike. A record that cannot say whether its own promotion happened is
	// the #2229 shape: green from outside, with no way to tell afterwards.
	reason, worthy := rec.fixtureWorthy()
	if worthy {
		rec.FixtureStaged, rec.FixtureStageDetail = tncapStageFixture(red)
	} else {
		rec.FixtureStageDetail = "not promoted: " + reason
	}

	// Re-marshal so credential_scan_skipped and the promotion fields ship in the
	// written bytes. credential_scan_skipped is a list of CLASS NAMES the scan could
	// not apply, which is the one thing that makes a silently-off credential net
	// visible after the fact.
	blob, err = json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("#2247: re-marshal record: %v", err)
		return
	}

	path := filepath.Join(dir, tncapRecordName)
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2247: write record %s: %v", red.str(path), err)
		return
	}
	t.Logf("#2247 outcome=%s terminated_on=%s turn=%.1fs fg_call=%v bg_task=%v held=%.1fs captured=%d "+
		"frames=%d notification_frames=%d keys=%v presence=%v line_types=%v tools=%v tool_errors=%d "+
		"scan_not_applied=%v\n  record: %s\n  %s",
		rec.Outcome, rec.TerminatedOn, rec.TurnSeconds, rec.ForegroundCallObserved,
		rec.BackgroundTaskObserved, rec.HeldSeconds, rec.LinesCaptured, rec.FrameCount,
		rec.NotificationFrameCount, rec.ObservedNotificationKeys, rec.KeyPresence, rec.LineTypeCensus,
		rec.ToolCalls, rec.ToolResultErrors, notApplied, red.str(path), red.str(rec.OutcomeDetail))

	if !worthy {
		t.Logf("#2247: NOT promoted to %s — %s. The record above is the evidence; read it, then re-run "+
			"or route the finding back", tncapFixturePath, reason)
		return
	}
	// The same deny-scanned bytes, promoted in-repo so the run that produced them is
	// the run that lands them. Writing the fixture here rather than leaving it in the
	// tempdir is the point of this probe's self-arming gate: a capture that still
	// needs a human to copy a file is a capture #1763 says will not land.
	if err := os.WriteFile(tncapFixturePath, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("#2247: write fixture %s: %v", tncapFixturePath, red.str(err.Error()))
		return
	}
	t.Logf("#2247: FIXTURE WRITTEN to %s (%d %s frame(s)). git add: staged=%v — %s\n"+
		"  COMMIT IT, and in the SAME COMMIT fill taskNotificationPinnedKeys in "+
		"internal/streamsup/task_notification_capture_test.go with exactly:\n"+
		"    %s\n"+
		"  Bytes that land with an empty pin FATAL that reader rather than passing quietly, which is "+
		"this ticket's AC5 and the deterministic net behind #2229's lost capture. If the staging above "+
		"ran in a worktree that gets discarded, the record at the artifact path logged earlier is the "+
		"copy that survives.\n"+
		"  A run reaching this line at all means the fixture was absent or %s=1 forced a re-capture, so "+
		"this is a NEW claude release or a suspected shape change: re-read the key set before trusting "+
		"the old one",
		tncapFixturePath, rec.NotificationFrameCount, tncapQuarrySubtype, rec.FixtureStaged,
		rec.FixtureStageDetail, tncapPinLiteral(rec.ObservedNotificationKeys), tncapEnableEnv)
}

// tncapPinLiteral renders the observed key set as the Go literal the streamsup
// reader's pin takes, so filling it is a paste rather than a second reading of the
// record. #2229's follow-up had to do exactly that reading, having shipped its pin
// empty and lost the bytes that would have filled it.
func tncapPinLiteral(keys []string) string {
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = strconv.Quote(k)
	}
	return "var taskNotificationPinnedKeys = []string{" + strings.Join(quoted, ", ") + "}"
}

// tncapStageFixture runs `git add` on the fixture the caller has just written — AC3,
// so the run that produces the artefact is the run that stages it rather than
// leaving a human to carry a file out of a tempdir.
//
// Fixed argv, no shell, and the one argument is a compile-time constant with `--`
// ahead of it so it can never be read as a flag; nothing claude emitted reaches it.
// The combined output goes through the redactor because git prints repository paths
// on failure.
//
// BEST-EFFORT AND NEVER FATAL. git being absent, or the working tree being somewhere
// staging means nothing, must not throw away a capture that cost a live turn — the
// bytes are already in the working tree and in the artifact directory by this point.
// The outcome is recorded either way, because "did the staging happen" is exactly the
// question #2229 could not answer about itself afterwards.
func tncapStageFixture(red *dropcapRedactor) (bool, string) {
	out, err := exec.Command("git", "add", "--", tncapFixturePath).CombinedOutput()
	if err != nil {
		return false, red.str(fmt.Sprintf("git add failed (%v): %s. The fixture is written in-repo "+
			"regardless; stage and commit it by hand", err, strings.TrimSpace(string(out))))
	}
	return true, "staged with `git add`. A run in a worktree the dispatcher discards stages into an " +
		"index that goes with it, so the artifact-dir record remains the copy that survives"
}

// --- offline self-checks -----------------------------------------------------

// TestTncapFixtureWorthyRefusesEveryBadCapture runs offline. fixtureWorthy is the
// only thing standing between a live run and a committed fixture, and each refusing
// arm below is a capture that would look green from outside — the record is written,
// the deny-scan passed, the log is cheerful — while proving nothing, proving
// something about the wrong claude, or carrying a host path into a public artefact.
//
// The two PROMOTING arms past the first are the ones worth having. A record whose
// companion subtypes never fired is promoted, because that is AC2 data rather than a
// defect: tool_progress_v2.1.259.json is a real turn on this surface that fired
// task_started and neither of the other two, so refusing on it would refuse a
// legitimate measurement. And a companion frame carrying an absolute path does not
// block promotion, because the sweep is scoped to the quarry — the companions'
// `description` is the rig's own command line and their payloads are already
// committed unredacted in dropped_lines_v2.1.220.json.
func TestTncapFixtureWorthyRefusesEveryBadCapture(t *testing.T) {
	t.Parallel()
	good := func() *tncapRecord {
		frames := make([]tncapFrame, 2)
		for i := range frames {
			frames[i] = tncapFrame{
				Index:           i,
				Type:            "system",
				Subtype:         tncapQuarrySubtype,
				PayloadEncoding: dropcapEncodingJSONString,
			}
		}
		return &tncapRecord{
			Outcome:                tncapFired,
			ClaudeVersion:          tncapFixtureVersion + " (Claude Code)",
			NotificationFrameCount: len(frames),
			Frames:                 frames,
			KeyPresence:            tncapPresenceTable(frames),
		}
	}
	tests := []struct {
		name   string
		mutate func(*tncapRecord)
		want   bool
	}{
		{"a good capture is promoted", func(*tncapRecord) {}, true},
		{"bare version string, no suffix", func(r *tncapRecord) { r.ClaudeVersion = tncapFixtureVersion }, true},
		{
			// The arm this ticket's promotion rule turns on.
			"companion subtypes that never fired are still promoted",
			func(r *tncapRecord) { r.KeyPresence = tncapPresenceTable(nil) },
			true,
		},
		{"never fired", func(r *tncapRecord) { r.Outcome = tncapDidNotFire }, false},
		{"instrument broken", func(r *tncapRecord) { r.Outcome = tncapInstrumentBroken }, false},
		{"vacuous: zero quarry frames", func(r *tncapRecord) { r.NotificationFrameCount = 0 }, false},
		{"a different claude release", func(r *tncapRecord) { r.ClaudeVersion = "2.1.260 (Claude Code)" }, false},
		{"version unreadable", func(r *tncapRecord) { r.ClaudeVersion = "<unavailable: exec failed>" }, false},
		{"version absent", func(r *tncapRecord) { r.ClaudeVersion = "" }, false},
		{
			// The one shape this side would otherwise promote and the reading side
			// refuses: a frame that was not valid UTF-8 is recorded base64 with an
			// empty payload, and the streamsup reader fatals on the encoding.
			"a base64 frame the consumer cannot read",
			func(r *tncapRecord) { r.Frames[1].PayloadEncoding = dropcapEncodingBase64 },
			false,
		},
		{
			"an unredacted host path in the quarry",
			func(r *tncapRecord) { r.UnredactedPathFields = []string{"output_file"} },
			false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := good()
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

// TestTncapStagingVerdictSeparatesRigFailureFromFinding runs offline. The verdict
// string is what a reader of a failed live gate acts on, and the one case that must
// never be confused with the others is the third: a background task that was started
// and then ALLOWED TO FINISH with no terminal-state line is evidence about claude's
// surface, while the first two are the rig failing to stage anything to measure.
func TestTncapStagingVerdictSeparatesRigFailureFromFinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rec         tncapRecord
		wantFinding bool
	}{
		{
			name: "the command never ran",
			rec:  tncapRecord{ForegroundCallObserved: false},
		},
		{
			name: "it ran but was never backgrounded",
			rec:  tncapRecord{ForegroundCallObserved: true, BackgroundTaskObserved: false},
		},
		{
			name: "a background task existed, finished, and nothing came",
			rec: tncapRecord{
				ForegroundCallObserved: true,
				BackgroundTaskObserved: true,
				HeldSeconds:            12,
			},
			wantFinding: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.rec.stagingVerdict()
			if got == "" {
				t.Fatal("stagingVerdict() is empty; the zero-frame fatal would name no cause")
			}
			isFinding := strings.Contains(got, "finding about the surface")
			if isFinding != tc.wantFinding {
				t.Errorf("stagingVerdict() reads as a surface finding = %v, want %v\n  got: %s",
					isFinding, tc.wantFinding, got)
			}
			if !tc.wantFinding && !strings.Contains(got, "staging failed") {
				t.Errorf("a rig failure must say so; got: %s", got)
			}
		})
	}
}

// TestTncapKeysReadsPresenceOutOfTheLineItself runs offline. AC2's whole value is
// that "claude sends ambient" is read off claude's own line rather than off a struct
// this repo declared, so the load-bearing property is that the two booleans and the
// key list come from the same decode and cannot disagree.
func TestTncapKeysReadsPresenceOutOfTheLineItself(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		payload            string
		wantKeys           []string
		wantAmbient        bool
		wantSkipTranscript bool
	}{
		{
			// dropped_lines_v2.1.220.json's shape: neither key present.
			name:     "the 2.1.220 shape carries neither key",
			payload:  `{"type":"system","subtype":"task_started","task_id":"x","uuid":"u"}`,
			wantKeys: []string{"subtype", "task_id", "type", "uuid"},
		},
		{
			name:               "both keys present",
			payload:            `{"type":"system","ambient":true,"skip_transcript":false}`,
			wantKeys:           []string{"ambient", "skip_transcript", "type"},
			wantAmbient:        true,
			wantSkipTranscript: true,
		},
		{
			name:        "one key present is not both",
			payload:     `{"ambient":false}`,
			wantKeys:    []string{"ambient"},
			wantAmbient: true,
		},
		{
			// Present-with-null is PRESENT: whether claude sends the key is this
			// ticket's question, and what it puts in it is #2245's.
			name:               "a null value is still a key claude sent",
			payload:            `{"ambient":null,"skip_transcript":null}`,
			wantKeys:           []string{"ambient", "skip_transcript"},
			wantAmbient:        true,
			wantSkipTranscript: true,
		},
		{
			name:    "a line that does not decode reports nothing rather than guessing",
			payload: `not json at all`,
		},
		{
			name:    "a JSON array is not an object and reports nothing",
			payload: `["ambient"]`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			keys, ambient, skipTranscript := tncapKeys([]byte(tc.payload))
			if strings.Join(keys, ",") != strings.Join(tc.wantKeys, ",") {
				t.Errorf("tncapKeys() keys = %v, want %v", keys, tc.wantKeys)
			}
			if ambient != tc.wantAmbient {
				t.Errorf("tncapKeys() ambient = %v, want %v", ambient, tc.wantAmbient)
			}
			if skipTranscript != tc.wantSkipTranscript {
				t.Errorf("tncapKeys() skipTranscript = %v, want %v", skipTranscript, tc.wantSkipTranscript)
			}
			// The two booleans must be derivable from the key list they ship beside:
			// a flag that can disagree with its own evidence is worse than no flag.
			if ambient != tncapContains(keys, tncapAmbientKey) ||
				skipTranscript != tncapContains(keys, tncapSkipTranscriptKey) {
				t.Errorf("the presence flags (%v, %v) disagree with the key list %v they were read "+
					"from", ambient, skipTranscript, keys)
			}
		})
	}
}

// TestTncapPresenceTableRecordsADidNotFire runs offline and is AC2's other half: a
// companion subtype that did not fire must be RECORDED as not having fired, not
// omitted. An omitted subtype and one that fired carrying neither key look identical
// to a later reader, and only one of those is a measurement.
func TestTncapPresenceTableRecordsADidNotFire(t *testing.T) {
	t.Parallel()

	t.Run("an empty turn still answers for all three", func(t *testing.T) {
		t.Parallel()
		got := tncapPresenceTable(nil)
		if len(got) != len(tncapCompanionSubtypes) {
			t.Fatalf("tncapPresenceTable(nil) returned %d entries, want %d — a subtype with no entry "+
				"is a question AC2 asked and this record did not answer", len(got), len(tncapCompanionSubtypes))
		}
		for _, p := range got {
			if p.Fired || p.LineCount != 0 {
				t.Errorf("%s: fired=%v line_count=%d, want a recorded absence", p.Subtype, p.Fired, p.LineCount)
			}
			if !strings.Contains(p.Note, "did not fire") {
				t.Errorf("%s: note %q does not say the line did not fire, so a reader cannot tell an "+
					"absence from a measurement", p.Subtype, p.Note)
			}
		}
	})

	t.Run("a fired subtype counts the lines carrying each key", func(t *testing.T) {
		t.Parallel()
		frames := []tncapFrame{
			{Subtype: "task_started", AmbientPresent: true},
			{Subtype: "task_started"},
			{Subtype: "task_updated", SkipTranscriptResent: true},
			// The quarry is not a companion and must not appear in this table.
			{Subtype: tncapQuarrySubtype, AmbientPresent: true, SkipTranscriptResent: true},
		}
		byName := map[string]tncapPresence{}
		for _, p := range tncapPresenceTable(frames) {
			byName[p.Subtype] = p
		}
		if len(byName) != len(tncapCompanionSubtypes) {
			t.Fatalf("got %d entries, want %d", len(byName), len(tncapCompanionSubtypes))
		}
		if got := byName["task_started"]; !got.Fired || got.LineCount != 2 || got.LinesWithAmbient != 1 {
			t.Errorf("task_started = %+v, want fired with 2 lines, 1 carrying ambient", got)
		}
		if got := byName["task_updated"]; !got.Fired || got.LinesWithSkipTranscript != 1 {
			t.Errorf("task_updated = %+v, want fired with 1 line carrying skip_transcript", got)
		}
		if got := byName["background_tasks_changed"]; got.Fired {
			t.Errorf("background_tasks_changed = %+v, want a recorded absence", got)
		}
	})
}

// TestTncapUnredactedPathFieldsNamesTheFieldNotTheValue runs offline. This is the
// third redaction mechanism, added by this ticket's security review because
// task_notification is documented to carry output_file — a path on the operator's
// host, and a field class no record in this family has carried before.
//
// The last case is the one that makes the whole thing worth having rather than
// decorative: the returned strings must never contain the path itself, because a
// refusal message printing the value would be the exposure the refusal exists to
// prevent.
func TestTncapUnredactedPathFieldsNamesTheFieldNotTheValue(t *testing.T) {
	t.Parallel()
	quarry := func(payload string) []tncapFrame {
		return []tncapFrame{{Index: 3, Subtype: tncapQuarrySubtype, Payload: payload}}
	}
	tests := []struct {
		name  string
		items []tncapFrame
		want  []string
	}{
		{
			name:  "a redacted path is not a finding",
			items: quarry(`{"output_file":"$TEMP_HOME/tncap-work/out.txt","status":"completed"}`),
			want:  []string{},
		},
		{
			name:  "a bare host path names its field",
			items: quarry(`{"output_file":"/var/x/out.txt","status":"completed"}`),
			want:  []string{"output_file"},
		},
		{
			name:  "a path nested inside an object is caught at depth",
			items: quarry(`{"usage":{"log":"/tmp/501/z"},"status":"completed"}`),
			want:  []string{"usage.log"},
		},
		{
			name:  "a path inside an array is caught too",
			items: quarry(`{"paths":["/a/b","ok"]}`),
			want:  []string{"paths[]"},
		},
		{
			// Scoped to the quarry: the companions' description IS the rig's own
			// command line and their payloads already ship unredacted in
			// dropped_lines_v2.1.220.json, so sweeping them would refuse every
			// capture for a class that is deliberately kept.
			name:  "a companion subtype is not swept",
			items: []tncapFrame{{Subtype: "task_started", Payload: `{"description":"/bin/cat /x/y"}`}},
			want:  []string{},
		},
		{
			name:  "a quarry payload that cannot be decoded cannot be swept, and says so",
			items: quarry(`not json`),
			want:  []string{"frame 3: payload does not decode, so it could not be swept"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tncapUnredactedPathFields(tc.items)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("tncapUnredactedPathFields() = %v, want %v", got, tc.want)
			}
			// The property the whole mechanism rests on, checked on every row rather
			// than in one case of its own: what comes back is claude's vocabulary,
			// never the operator's data.
			for _, f := range tc.items {
				for _, v := range []string{"/var/x/out.txt", "/tmp/501/z", "/a/b", "/bin/cat /x/y"} {
					if strings.Contains(f.Payload, v) && strings.Contains(strings.Join(got, "|"), v) {
						t.Errorf("the result %v carries the VALUE %q; it must name the field only", got, v)
					}
				}
			}
		})
	}
}

// tncapContains is a local spelling of "is s in xs", used by the presence-flag
// consistency check above. It takes the file prefix like every other identifier
// here: siblings add files to this package concurrently and a branch-overlap check
// does not catch a same-package identifier collision, which is exactly the shape a
// generically-named helper would produce.
func tncapContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
