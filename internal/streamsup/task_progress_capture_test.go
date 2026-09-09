package streamsup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
)

// taskProgressCapturePath is the committed real-claude capture this reader pins
// claude's system/task_progress line from (#2248).
//
// IT NAMES ANOTHER TICKET'S RECORD ON PURPOSE, and that is the whole shape of this
// ticket. #2248 was written to spend a live turn capturing this subtype, on a
// re-check that found the string nowhere in the repo. Between that check and the
// build, parent_tool_use_v2.1.259.json landed on main carrying TWO verbatim
// system/task_progress payloads: its probe runs sonnet with --allowed-tools omitted
// and asks for a delegation explicitly, which is the agent-shaped staging this
// subtype needs, and it keeps the WHOLE turn rather than only its own quarry. So the
// bytes were already committed, already redacted, already deny-scanned and already
// version-named — and nothing in this package read them. A second live turn would
// have re-measured what is measured.
//
// It reads a file under internal/e2e/realclaude/testdata/ for the reason
// taskNotificationCapturePath gives: the bytes are only BYTES, the e2e_realclaude
// build tag belongs to that package's Go files rather than to its testdata, and
// reading it from here is what keeps the measurement inside `make check` instead of
// behind an opt-in gate that SKIPS (exit 0) with no claude login.
//
// THIS IS A FIFTH READER, NOT A GENERALISATION OF ANY OF THE FOUR, and being the
// second reader over ONE file does not soften that. capturedLines' docblock forbids
// by name growing a capture reader a path parameter, because its is_capture
// assertion is what stops a hand-built payload file being swapped in behind the
// provenance checks; this reader takes no parameter either and writes every
// provenance check out below.
//
// The constants are minted here rather than borrowed from parentCapturePath, which
// names the same file today. Borrowing would couple this pin to another ticket's
// repin: a capture at a later claude release moves that constant to a new filename
// and would carry this reader silently along to bytes it never measured. Two
// independent constants make the same event a LOUD fatal instead — see the third
// quadrant of taskProgressReaderGate. Deliberately absent for the same reason: any
// assertion that the two constants agree. Two readers pinned at two claude releases
// over two committed records is a legitimate steady state, and a check for equality
// would forbid it.
const taskProgressCapturePath = "../e2e/realclaude/testdata/parent_tool_use_v" +
	taskProgressCaptureVersion + ".json"

// taskProgressCaptureVersion is the claude release the capture was taken at,
// spliced into the path above rather than repeated so the filename cannot drift
// from the version this reader enforces.
const taskProgressCaptureVersion = "2.1.259"

// taskProgressSubtype is the quarry, and taskProgressCensusKey is how the record's
// census spells it. Both are literals rather than imports from the parser's tables:
// this file measures what claude SENDS, and a comparison keyed on a production
// constant would agree with the matcher by construction instead of measuring it.
const (
	taskProgressSubtype   = "task_progress"
	taskProgressCensusKey = "system/" + taskProgressSubtype
)

// toolProgressType is THE THING THIS SUBTYPE IS NOT, named here because confusing
// the two is the documented easy mistake on this ticket and the only way to refuse a
// mistake is to spell it out.
//
// tool_progress is a TOP-LEVEL TYPE with no subtype, already measured in
// tool_progress_v2.1.259.json and already consumed by consumeToolProgress.
// task_progress is a SUBTYPE of the system type. The census keys system subtypes as
// "system/<subtype>" and top-level types by their bare name, so the two occupy
// distinct keys by construction — which is what
// TestRealClaudeTaskProgressCaptureIsNotToolProgress asserts rather than assumes.
const toolProgressType = "tool_progress"

// taskProgressPinnedKeys IS THE MEASUREMENT THIS TICKET COMMITS: the top-level key
// set that EVERY captured system/task_progress line arrived with, sorted.
//
// Per-frame equality rather than a union across frames. The family's rule, stated in
// systemTaskStartedLine's doc, is that the field set is exactly what the committed
// capture shows and nothing invented from a docs page — and a union would let one
// line carrying a subset hide inside another line's keys, which is the same
// over-declaration by a quieter route.
//
// It is filled, not empty, and that is this ticket's whole difference from its
// siblings: they shipped their pins empty awaiting a live gate, whereas these bytes
// were already on main. taskProgressReaderGate therefore lands in its RUN quadrant
// from the first `make check`, and its other three quadrants are proven by the pure
// gate test rather than by this reader's own leg.
//
// DECLARED ALREADY SORTED, and TestTaskProgressPinsAreDeclaredSorted enforces it.
// The reason is a race rather than tidiness: a reader that sorted a package-level
// var in place would mutate state a t.Parallel() sibling could read, and with no
// parallel test touching this slice today the detector would have nothing to trip on
// and the hazard would ship latent. Sorting only ever happens on locally-built slices.
var taskProgressPinnedKeys = []string{
	"description", "last_tool_name", "session_id", "subagent_type", "subtype",
	"task_id", "tool_use_id", "type", "usage", "uuid",
}

// taskProgressPinnedUsageKeys is the sub-key set of the nested usage object, pinned
// because a top-level key set says nothing about a nested shape and the mapping
// downstream has to declare one.
var taskProgressPinnedUsageKeys = []string{"duration_ms", "tool_uses", "total_tokens"}

// taskProgressDocumentedKeys is what @anthropic-ai/claude-agent-sdk@0.3.263's
// sdk.d.ts describes for SDKTaskProgressMessage.
//
// IT IS A THING TO CHECK THE CAPTURE AGAINST, NEVER A FIELD SET TO DECLARE FROM. It
// reaches an assertion only as the two set DIFFERENCES below, so a key the SDK
// describes and claude does not send is a recorded measurement here rather than a
// field somebody declared on the strength of documentation.
var taskProgressDocumentedKeys = []string{
	"description", "last_tool_name", "subagent_type", "summary", "task_id",
	"tool_use_id", "usage",
}

// The two differences, pinned because each carries something the mapping needs.
//
// summary is documented and NOT observed, and that is a measurement about this
// STAGING rather than about the subtype: the SDK describes summary as present for a
// local agent only with the progress-summaries option, and always for an MCP task,
// and this capture is a local agent without that option. Pinning the absence is what
// stops the mapping declaring the field anyway; measuring what an MCP task or the
// progress-summaries option sends needs a live turn nobody has spent.
//
// The observed-and-undocumented four are the envelope keys claude puts on every line
// of this family. That is the expected shape rather than a finding, and it is pinned
// so that it stays a recorded expectation instead of a surprise re-derived each time.
var (
	taskProgressDocumentedNotObserved = []string{"summary"}
	taskProgressObservedNotDocumented = []string{"session_id", "subtype", "type", "uuid"}
)

// The four states of (fixture, pin). Only the first is a skip.
const (
	taskProgressGateSkip  = "skip"
	taskProgressGateRun   = "run"
	taskProgressGateFatal = "fatal"
)

// taskProgressReaderGate is pure so all four quadrants are proved on every run —
// including the three this repo cannot reach today, the bytes and the pin having
// landed together.
func taskProgressReaderGate(fixtureExists, pinFilled bool) (action, reason string) {
	switch {
	case !fixtureExists && !pinFilled:
		return taskProgressGateSkip, "no capture holding this subtype is committed and nothing is " +
			"pinned, so there is nothing to read and nothing claiming to have read anything. This is " +
			"the ONLY state in which this reader may skip. It is NOT the state this ticket shipped in: " +
			"the bytes were already on main in another probe's whole-turn record, so reaching this " +
			"quadrant means that record was removed and the pin emptied in the same commit"
	case !fixtureExists && pinFilled:
		return taskProgressGateFatal, "taskProgressPinnedKeys names the keys a capture observed but " +
			taskProgressCapturePath + " is gone. This is the quadrant that catches a repin of the " +
			"producing probe that forgot this reader: that record's own constants moved to a new " +
			"claude release and these did not. Restore the fixture, or repin this reader at the " +
			"release that replaced it, or — if the capture was deliberately dropped — empty the pin " +
			"in the same commit, because a pin with no bytes behind it is a measurement nothing supports"
	case fixtureExists && !pinFilled:
		return taskProgressGateFatal, "the capture at " + taskProgressCapturePath + " is committed but " +
			"taskProgressPinnedKeys is empty, so nothing pins what it measured. Read the " +
			"system/" + taskProgressSubtype + " frames' own key sets and write them here IN THE COMMIT " +
			"THAT LANDS THE BYTES. Until then the bytes are committed and unpinned, which is the state " +
			"this gate exists to make impossible"
	default:
		return taskProgressGateRun, ""
	}
}

// taskProgressFrame is the slice of one captured frame this reader needs. payload is
// a JSON STRING holding the whole line (payload_encoding says so per frame), not a
// nested object, so the bytes can be re-decoded as claude sent them.
type taskProgressFrame struct {
	Index           int    `json:"index"`
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	PayloadEncoding string `json:"payload_encoding"`
	Payload         string `json:"payload"`
}

// taskProgressCaptureRecord decodes the record's frames and its census, and
// deliberately not any field the producing probe computed about its OWN quarry: a
// reader trusting those would pin the probe's decoding rather than claude's line.
type taskProgressCaptureRecord struct {
	IsCapture      bool                `json:"is_capture"`
	ClaudeVersion  string              `json:"claude_version"`
	LineTypeCensus map[string]int      `json:"line_type_census"`
	Frames         []taskProgressFrame `json:"frames"`
}

// taskProgressCapture reads the committed record, runs the gate, checks provenance
// and returns it with the system/task_progress frames already matched and their
// envelope labels verified against the bytes they describe.
//
// Existence is derived from a SINGLE os.ReadFile rather than an os.Stat followed by
// a read: there is then no gap between the check and the use for a swap to land in.
//
// Every failure past the gate is t.Fatalf, never a skip. The capture is committed, so
// a missing, undecodable, wrong-version or zero-frame record is a broken premise
// rather than an unavailable resource. Zero frames fatals HERE, one level below both
// callers, so neither can loop over an empty slice and pass vacuously.
//
// No message below prints a payload VALUE — only field names, frame indices, counts
// and the census. Today's bytes are public in the repo either way, so this is
// discipline rather than exposure; it is load-bearing forward, because description is
// one of the keys these frames carry and a background task's description can be a
// literal operator command line.
func taskProgressCapture(t *testing.T) (taskProgressCaptureRecord, []taskProgressFrame) {
	t.Helper()

	raw, readErr := os.ReadFile(taskProgressCapturePath)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("reading capture %s: %v", taskProgressCapturePath, readErr)
	}
	switch action, reason := taskProgressReaderGate(exists, len(taskProgressPinnedKeys) > 0); action {
	case taskProgressGateSkip:
		t.Skipf("#2248: %s", reason)
	case taskProgressGateFatal:
		t.Fatalf("#2248: %s", reason)
	}

	var capture taskProgressCaptureRecord
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", taskProgressCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — this file must be a genuine claude capture, never a "+
			"hand-written payload; a guessed line carries whatever field set its author expected, which "+
			"is precisely what this family refuses to declare from", taskProgressCapturePath)
	}
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on the
	// leading token; an "<unavailable: ...>" version fails it too, which is correct — a
	// capture that could not read the version it was taken at cannot vouch for the
	// release its filename claims.
	if got, _, _ := strings.Cut(capture.ClaudeVersion, " "); got != taskProgressCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures, so a record from a different claude must not be read under it. Re-capture at the "+
			"pinned version, or bump taskProgressCaptureVersion and every pin in this file together",
			taskProgressCapturePath, capture.ClaudeVersion, taskProgressCaptureVersion)
	}

	var frames []taskProgressFrame
	for _, f := range capture.Frames {
		if f.Type != "system" || f.Subtype != taskProgressSubtype {
			continue
		}
		if f.PayloadEncoding != "json-string" {
			t.Fatalf("%s: frame %d payload_encoding = %q, want %q — a line that was not valid UTF-8 "+
				"carries no readable payload, so its key set cannot be re-derived",
				taskProgressCapturePath, f.Index, f.PayloadEncoding, "json-string")
		}
		// The record's own envelope labels are checked against the bytes they describe
		// rather than taken on trust. This is the leg of AC3 that refuses a frame
		// LABELLED as the quarry while holding something else — a tool_progress line
		// recorded under this subtype's name would prove nothing and would feed an
		// invented mapping.
		var envelope struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal([]byte(f.Payload), &envelope); err != nil {
			t.Fatalf("%s: frame %d payload does not decode as a JSON object: %v",
				taskProgressCapturePath, f.Index, err)
		}
		if envelope.Type != "system" || envelope.Subtype != taskProgressSubtype {
			t.Fatalf("%s: frame %d is recorded as system/%s but its payload says type=%q subtype=%q — "+
				"the record's envelope labels disagree with the bytes they describe",
				taskProgressCapturePath, f.Index, taskProgressSubtype, envelope.Type, envelope.Subtype)
		}
		frames = append(frames, f)
	}
	if len(frames) == 0 {
		t.Fatalf("%s: the capture holds ZERO system/%s lines. A record with none is vacuous — every "+
			"assertion built on it would pass without reading a byte claude sent.\n"+
			"This record belongs to another probe, whose promotion rule requires ITS OWN quarry and not "+
			"this one, so the way to reach this state is a forced re-capture at the same claude release "+
			"whose turn did not delegate: claude inlining the reads produces a green run with no "+
			"subagent in it and therefore no progress lines. Read that as a re-capture that lost a "+
			"measurement, not as a broken test",
			taskProgressCapturePath, taskProgressSubtype)
	}
	return capture, frames
}

// TestRealClaudeTaskProgressCaptureKeysArePinned reads the committed capture and pins
// the key set every system/task_progress line in it arrived with, top level and
// inside usage — the evidence the progress mapping declares its decode target from.
func TestRealClaudeTaskProgressCaptureKeysArePinned(t *testing.T) {
	_, frames := taskProgressCapture(t)

	for _, f := range frames {
		// Re-derived from the line's OWN bytes. json.RawMessage rather than a typed
		// target so the key set is claude's rather than this file's: a struct would
		// silently drop every key it does not declare, which is the exact failure this
		// measurement exists to prevent downstream.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(f.Payload), &obj); err != nil {
			t.Fatalf("%s: frame %d payload does not decode as a JSON object: %v",
				taskProgressCapturePath, f.Index, err)
		}
		if got := taskProgressSortedKeys(obj); !taskProgressSameSet(got, taskProgressPinnedKeys) {
			t.Fatalf("%s: frame %d carries the key set %v, but taskProgressPinnedKeys says %v.\n"+
				"The pin is per-frame on purpose: a union across frames would let one line carrying a "+
				"subset hide inside another's keys. If claude's shape genuinely changed, re-capture at "+
				"the new version and repin both ends together; a pin edited to match a fixture nobody "+
				"re-read turns this measurement into an agreement with itself",
				taskProgressCapturePath, f.Index, got, taskProgressPinnedKeys)
		}

		var usage map[string]json.RawMessage
		if err := json.Unmarshal(obj["usage"], &usage); err != nil {
			t.Fatalf("%s: frame %d usage does not decode as a JSON object: %v — the top-level pin says "+
				"the key is there, so a usage that is not an object is a shape change the pin missed",
				taskProgressCapturePath, f.Index, err)
		}
		if got := taskProgressSortedKeys(usage); !taskProgressSameSet(got, taskProgressPinnedUsageKeys) {
			t.Fatalf("%s: frame %d usage carries the sub-key set %v, but taskProgressPinnedUsageKeys "+
				"says %v. A top-level pin says nothing about a nested shape, and the mapping downstream "+
				"has to declare one", taskProgressCapturePath, f.Index, got, taskProgressPinnedUsageKeys)
		}
	}
}

// TestRealClaudeTaskProgressCaptureIsNotToolProgress is AC3, and it refuses the
// documented easy mistake on this ticket from the census side. The other side of it —
// a frame LABELLED as this subtype while holding something else — is refused in
// taskProgressCapture, at the reader both tests share.
func TestRealClaudeTaskProgressCaptureIsNotToolProgress(t *testing.T) {
	capture, frames := taskProgressCapture(t)

	counted, ok := capture.LineTypeCensus[taskProgressCensusKey]
	if !ok {
		t.Fatalf("%s: the census carries no %q key although %d frame(s) of that subtype are in the "+
			"record. The census is what tells a later reader the two progress shapes were counted "+
			"apart, and one that omits the subtype cannot",
			taskProgressCapturePath, taskProgressCensusKey, len(frames))
	}
	// A bare key would mean a system SUBTYPE was counted as a TOP-LEVEL TYPE, which is
	// exactly how the two shapes get conflated: tool_progress is counted bare because it
	// genuinely is a top-level type, and a bare task_progress beside it would be
	// indistinguishable from one.
	if n, bare := capture.LineTypeCensus[taskProgressSubtype]; bare {
		t.Fatalf("%s: the census carries a BARE %q key counting %d line(s). This subtype must be "+
			"counted as %q; %q is counted bare because it is a top-level type, and a bare key here "+
			"would make the two indistinguishable in the one place that keeps them apart",
			taskProgressCapturePath, taskProgressSubtype, n, taskProgressCensusKey, toolProgressType)
	}
	if counted != len(frames) {
		t.Fatalf("%s: the census counts %d %s line(s) but the record holds %d frame(s) of that "+
			"subtype. The census and the payloads disagree about how many there were, so one of them "+
			"is describing a different turn",
			taskProgressCapturePath, counted, taskProgressCensusKey, len(frames))
	}
}

// TestTaskProgressDocumentedKeysAreOnlyEverAComparison is the net under the family's
// declare-only-what-you-captured rule, and it runs offline.
//
// Without it taskProgressDocumentedKeys is decoration: nothing would notice a future
// editor "fixing" a failing pin by pasting the SDK's field list into it, which is the
// precise move the rule forbids and the easiest one to make when the pin reddens. The
// two differences must stay non-empty in BOTH directions — claude sends envelope keys
// the SDK does not describe, and the SDK describes a summary this staging does not
// produce — so a pin that had become a copy of the documented set reddens here.
func TestTaskProgressDocumentedKeysAreOnlyEverAComparison(t *testing.T) {
	t.Parallel()

	documentedNotObserved := taskProgressDiff(taskProgressDocumentedKeys, taskProgressPinnedKeys)
	observedNotDocumented := taskProgressDiff(taskProgressPinnedKeys, taskProgressDocumentedKeys)

	if len(documentedNotObserved) == 0 || len(observedNotDocumented) == 0 {
		t.Fatalf("the pinned key set and the documented one differ in %d and %d places — a difference "+
			"empty in either direction means the pin has become a COPY of the docs page rather than a "+
			"reading of claude's bytes, which is the one thing this family's rule forbids",
			len(documentedNotObserved), len(observedNotDocumented))
	}
	if !taskProgressSameSet(documentedNotObserved, taskProgressDocumentedNotObserved) {
		t.Errorf("documented-not-observed is %v, want %v — this is the set the mapping must NOT "+
			"declare from, so a change to it is a change to what claude was measured not to send",
			documentedNotObserved, taskProgressDocumentedNotObserved)
	}
	if !taskProgressSameSet(observedNotDocumented, taskProgressObservedNotDocumented) {
		t.Errorf("observed-not-documented is %v, want %v — these are the envelope keys claude puts on "+
			"every line of this family, and they are pinned so they stay a recorded expectation rather "+
			"than a surprise re-derived each time", observedNotDocumented, taskProgressObservedNotDocumented)
	}
}

// TestTaskProgressPinsAreDeclaredSorted is deterministic code standing behind an
// advisory rule, which is the only reason it is worth a test of its own.
//
// The rule is that a pin is DECLARED sorted so no reader ever sorts one in place: a
// sort of a package-level var mutates state a t.Parallel() sibling could read. No
// parallel test touches these slices today, so -race has nothing to trip on and the
// hazard would ship latent — which is exactly the class a test rather than a comment
// has to hold.
func TestTaskProgressPinsAreDeclaredSorted(t *testing.T) {
	t.Parallel()
	for _, pin := range []struct {
		name string
		keys []string
	}{
		{"taskProgressPinnedKeys", taskProgressPinnedKeys},
		{"taskProgressPinnedUsageKeys", taskProgressPinnedUsageKeys},
		{"taskProgressDocumentedKeys", taskProgressDocumentedKeys},
		{"taskProgressDocumentedNotObserved", taskProgressDocumentedNotObserved},
		{"taskProgressObservedNotDocumented", taskProgressObservedNotDocumented},
	} {
		if !sort.StringsAreSorted(pin.keys) {
			t.Errorf("%s is declared unsorted (%v). Declare it sorted rather than sorting it at run "+
				"time: sorting a package-level var in place mutates state a parallel test could read",
				pin.name, pin.keys)
		}
	}
}

// TestTaskProgressReaderGateHasExactlyOneLegalSkip proves the four quadrants of the
// sequencing gate. Three of them are unreachable in this repo — the bytes and the pin
// landed together, so the reader itself only ever exercises the run state — which
// makes this the only coverage they have.
func TestTaskProgressReaderGateHasExactlyOneLegalSkip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		fixtureExists bool
		pinFilled     bool
		want          string
	}{
		{"no bytes and no pin: the one legal skip", false, false, taskProgressGateSkip},
		{"a pin whose bytes are gone", false, true, taskProgressGateFatal},
		{"bytes committed with nothing pinning them", true, false, taskProgressGateFatal},
		{"the steady state this ticket ships in", true, true, taskProgressGateRun},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, reason := taskProgressReaderGate(tc.fixtureExists, tc.pinFilled)
			if action != tc.want {
				t.Errorf("taskProgressReaderGate(%v, %v) = %q, want %q", tc.fixtureExists,
					tc.pinFilled, action, tc.want)
			}
			if action == taskProgressGateRun && reason != "" {
				t.Errorf("the run state named a reason %q; nothing reads it", reason)
			}
			if action != taskProgressGateRun && reason == "" {
				t.Errorf("%s named no reason; the skip or fatal message would say nothing", action)
			}
		})
	}
}

// capturedTaskProgressLines returns the bytes of the committed system/task_progress
// lines, in record order, as claude sent them — what #2246's mapping assertions
// replay through the shipped parser.
//
// It goes through taskProgressCapture rather than re-reading the file, unlike
// capturedTaskNotificationLine's independent read of ITS record. The difference is
// which failure each guards. That helper duplicates its sibling's provenance checks
// because the two readers are separate functions over separate constants, and one
// trusting the other to have run would hand a mapping assertion whatever bytes were
// on disk. Here the gate, the version check and the envelope check all live in the
// ONE reader this file already has, so calling it is how those checks run — reading
// the file a second time is what would skip them.
//
// The task-count assertion is this helper's own and belongs at this level: the rate
// bound is keyed PER TASK, so a record whose frames spanned two tasks would make
// every count assertion built on these lines mean something different, and it would
// do so silently. Both frames carrying one id is the staging #2246's bound is
// argued from, not an incidental property.
func capturedTaskProgressLines(t *testing.T) [][]byte {
	t.Helper()
	_, frames := taskProgressCapture(t)

	ids := map[string]bool{}
	lines := make([][]byte, 0, len(frames))
	for _, f := range frames {
		var tl struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal([]byte(f.Payload), &tl); err != nil {
			t.Fatalf("%s: frame %d payload does not decode: %v", taskProgressCapturePath, f.Index, err)
		}
		ids[tl.TaskID] = true
		lines = append(lines, []byte(f.Payload))
	}
	if len(ids) != 1 {
		t.Fatalf("%s: the %d captured %s frames span %d task ids, not 1. #2246's rate bound is keyed "+
			"per task, so a multi-task record silently changes what every count assertion built on "+
			"these lines measures — re-read the record before trusting a count taken from it",
			taskProgressCapturePath, len(frames), taskProgressCensusKey, len(ids))
	}
	return lines
}

// taskProgressSortedKeys returns a decoded object's own top-level key set, sorted.
// The slice is built here and sorted here, so nothing package-level is mutated.
func taskProgressSortedKeys(obj map[string]json.RawMessage) []string {
	out := make([]string, 0, len(obj))
	for k := range obj {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// taskProgressSameSet compares two key lists that are both already sorted — the pins
// by declaration, the observed sets by taskProgressSortedKeys — so neither operand is
// sorted in place here.
func taskProgressSameSet(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}

// taskProgressDiff returns the entries of a that are absent from b, in a's order,
// which is sorted for every caller.
func taskProgressDiff(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, s := range b {
		inB[s] = true
	}
	out := []string{}
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	return out
}
