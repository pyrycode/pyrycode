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

// taskNotificationCapturePath is the committed real-claude capture of one live
// turn that backgrounds a Bash command and then lets it FINISH (#2247), produced
// by internal/e2e/realclaude's TestRealClaude_TaskNotificationCapture.
//
// It reads a file under internal/e2e/realclaude/testdata/ for the reason
// compactionCapturePath gives: the bytes are only BYTES, the e2e_realclaude build
// tag belongs to that package's Go files rather than to its testdata, and reading
// it from here is what keeps the measurement inside `make check` instead of behind
// an opt-in gate that SKIPS (exit 0) with no claude login.
//
// THIS IS A FOURTH READER, NOT A GENERALISATION OF ANY OF THE THREE. capturedLines'
// docblock forbids by name growing it a path parameter, because its is_capture
// assertion is what stops a hand-built payload file being swapped in behind the
// provenance checks. capturedToolProgressLines and the compaction reader each
// restated that discipline rather than importing it, and so does this one: its own
// package constants, no path parameter, every provenance check written out below.
// None of the four can decode another's record shape, and reaching for the wrong
// one yields zero values rather than a failure.
const taskNotificationCapturePath = "../e2e/realclaude/testdata/task_notification_v" +
	taskNotificationCaptureVersion + ".json"

// taskNotificationCaptureVersion is the claude release the capture was taken at,
// spliced into the path above rather than repeated so the filename cannot drift
// from the version this reader enforces. The producing side refuses to write under
// a mismatched name (tncapRecord.fixtureWorthy), so a claude upgrade is a loud
// instruction to re-capture and repin rather than a fixture that quietly measures
// another release.
const taskNotificationCaptureVersion = "2.1.259"

// taskNotificationSubtype is the quarry. Spelled as a literal rather than imported
// from the parser's tables: this file measures what claude SENDS, and a comparison
// keyed on a production constant would agree with the matcher by construction.
const taskNotificationSubtype = "task_notification"

// taskNotificationPinnedKeys IS THE MEASUREMENT THIS TICKET COMMITS: the top-level
// key set claude's system/task_notification line arrived with, sorted. That set is
// what #2245's mapping may declare from and nothing else — the family's rule, stated
// in systemTaskStartedLine's doc, is that the field set is exactly what the committed
// capture shows and nothing invented from a docs page.
//
// The Agent SDK's SDKTaskNotificationMessage describes task_id, tool_use_id, status,
// output_file, summary, usage, skip_transcript and ambient. That list is a thing to
// CHECK THE CAPTURE AGAINST, never the thing to fill this slice from. The record
// carries both the observed set and its two differences against the documented one,
// so a key the SDK describes and claude does not send is a recorded measurement here
// rather than a field somebody declared on the strength of documentation.
//
// IT IS EMPTY ON PURPOSE UNTIL THE LIVE GATE HAS RUN, and that is what sequences
// this ticket. The fixture cannot exist before `make e2e-realclaude` produces it on
// an authenticated machine, which happens after verification, so a reader asserting
// against bytes any earlier would redden `make check` for every unrelated ticket.
// taskNotificationReaderGate turns that into a state machine with exactly one legal
// skip: FILLING THIS SLICE IS THE COMMIT THAT LANDS THE FIXTURE, and a fixture
// landing without it fatals rather than passing quietly.
//
// #2229 is why the gate is shaped this way rather than trusted to memory. Its probe
// fired green against claude 2.1.259 and wrote its record, but the producing run was
// the dispatcher's gate-only real-claude lap, which verifies from a detached worktree
// and never commits — so the in-repo write went out with the worktree and the bytes
// had to be landed by a follow-up ticket that also had to fill the pin its reader had
// shipped empty. The promotion log on the producing side prints this slice's contents
// ready to paste, so filling it needs no second reading of the record.
var taskNotificationPinnedKeys = []string{"output_file", "session_id", "status", "subtype", "summary", "task_id", "tool_use_id", "type", "uuid"}

// The four states of (fixture, pin). Only the first is a skip, and only on the leg
// before the live gate has ever run.
const (
	taskNotificationGateSkip  = "skip"
	taskNotificationGateRun   = "run"
	taskNotificationGateFatal = "fatal"
)

// taskNotificationReaderGate is pure so all four quadrants are proved on every run,
// including the leg where the fixture is still absent and the reader itself cannot
// assert anything.
func taskNotificationReaderGate(fixtureExists, pinFilled bool) (action, reason string) {
	switch {
	case !fixtureExists && !pinFilled:
		return taskNotificationGateSkip, "the capture has not been taken yet: `make e2e-realclaude` on " +
			"an authenticated machine runs TestRealClaude_TaskNotificationCapture, which arms on this " +
			"fixture's absence and writes it in-repo. This is the ONLY state in which this reader may " +
			"skip, and it ends the moment the bytes and the pin land together"
	case !fixtureExists && pinFilled:
		return taskNotificationGateFatal, "taskNotificationPinnedKeys names the keys a capture observed " +
			"but " + taskNotificationCapturePath + " is gone. Restore the fixture, or if the capture was " +
			"deliberately dropped, empty the pin in the same commit — a pin with no bytes behind it is a " +
			"measurement nothing supports"
	case fixtureExists && !pinFilled:
		return taskNotificationGateFatal, "the capture at " + taskNotificationCapturePath + " has landed " +
			"but taskNotificationPinnedKeys is still empty, so nothing pins what it measured. Read the " +
			"record's observed_notification_keys and write them here IN THE COMMIT THAT ADDS THE FIXTURE. " +
			"Until then the bytes are committed and unpinned, which is the state this gate exists to make " +
			"impossible"
	default:
		return taskNotificationGateRun, ""
	}
}

// taskNotificationCapture is the slice of the record this reader needs. payload is a
// JSON STRING holding the whole line (payload_encoding says so per frame), not a
// nested object, so the bytes can be re-decoded as claude sent them.
//
// It decodes the FRAMES and not the record's own observed_notification_keys, which
// is the whole point: that field was written by the probe, and a reader trusting it
// would pin the probe's decoding rather than claude's line.
type taskNotificationCaptureRecord struct {
	IsCapture     bool   `json:"is_capture"`
	ClaudeVersion string `json:"claude_version"`
	Frames        []struct {
		Index           int    `json:"index"`
		Type            string `json:"type"`
		Subtype         string `json:"subtype"`
		PayloadEncoding string `json:"payload_encoding"`
		Payload         string `json:"payload"`
	} `json:"frames"`
}

// TestRealClaudeTaskNotificationCaptureKeysArePinned reads the committed capture and
// pins the top-level key set every system/task_notification line in it arrived with
// — the evidence #2245 declares its decode target from.
//
// Every failure once the gate says run is t.Fatalf, never a skip: the capture is
// committed, so a missing or empty record is a broken premise rather than an
// unavailable resource. Zero task_notification frames fatals here, one level below
// any future test that loops over them, so none of them can pass vacuously.
func TestRealClaudeTaskNotificationCaptureKeysArePinned(t *testing.T) {
	raw, readErr := os.ReadFile(taskNotificationCapturePath)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("reading capture %s: %v", taskNotificationCapturePath, readErr)
	}
	switch action, reason := taskNotificationReaderGate(exists, len(taskNotificationPinnedKeys) > 0); action {
	case taskNotificationGateSkip:
		t.Skipf("#2247: %s", reason)
	case taskNotificationGateFatal:
		t.Fatalf("#2247: %s", reason)
	}

	var capture taskNotificationCaptureRecord
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", taskNotificationCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — this file must be a genuine claude capture, never a "+
			"hand-written payload; a guessed line carries whatever field set its author expected, which "+
			"is precisely what this family refuses to declare from", taskNotificationCapturePath)
	}
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on
	// the leading token; an "<unavailable: ...>" version fails it too, which is
	// correct — a capture that could not read the version it was taken at cannot
	// vouch for the release its filename claims.
	if got, _, _ := strings.Cut(capture.ClaudeVersion, " "); got != taskNotificationCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures, so a record from a different claude must not be read under it. Re-capture at the "+
			"pinned version, or bump tncapFixtureVersion, taskNotificationCaptureVersion and "+
			"taskNotificationPinnedKeys together", taskNotificationCapturePath, capture.ClaudeVersion,
			taskNotificationCaptureVersion)
	}

	seen := map[string]bool{}
	frames := 0
	for _, f := range capture.Frames {
		if f.Type != "system" || f.Subtype != taskNotificationSubtype {
			continue
		}
		if f.PayloadEncoding != "json-string" {
			t.Fatalf("%s: frame %d payload_encoding = %q, want %q — a line that was not valid UTF-8 "+
				"carries no readable payload, so its key set cannot be re-derived",
				taskNotificationCapturePath, f.Index, f.PayloadEncoding, "json-string")
		}
		// Re-derived from the line's OWN bytes. json.RawMessage rather than a typed
		// target so the key set is claude's rather than this file's: a struct would
		// silently drop every key it does not declare, which is the exact failure
		// this measurement exists to prevent downstream.
		var keys map[string]json.RawMessage
		if err := json.Unmarshal([]byte(f.Payload), &keys); err != nil {
			t.Fatalf("%s: frame %d payload does not decode as a JSON object: %v",
				taskNotificationCapturePath, f.Index, err)
		}
		// The record's own envelope labels are checked against the bytes they
		// describe rather than taken on trust, for the reason the frame loop exists.
		if string(keys["type"]) != `"system"` || string(keys["subtype"]) != `"`+taskNotificationSubtype+`"` {
			t.Fatalf("%s: frame %d is recorded as system/%s but its payload says type=%s subtype=%s — "+
				"the record's envelope labels disagree with the bytes they describe",
				taskNotificationCapturePath, f.Index, taskNotificationSubtype,
				string(keys["type"]), string(keys["subtype"]))
		}
		frames++
		for k := range keys {
			seen[k] = true
		}
	}
	if frames == 0 {
		t.Fatalf("%s: the capture holds ZERO system/%s lines. A record with none is vacuous — every "+
			"assertion built on it would pass without reading a byte claude sent, and this subtype's "+
			"whole reason for being captured is that nothing in this repo held its payload",
			taskNotificationCapturePath, taskNotificationSubtype)
	}

	got := make([]string, 0, len(seen))
	for k := range seen {
		got = append(got, k)
	}
	sort.Strings(got)
	want := append([]string(nil), taskNotificationPinnedKeys...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s: the %d captured %s line(s) carry the key set %v, but taskNotificationPinnedKeys "+
			"says %v.\nIf claude's shape genuinely changed, re-capture at the new version and repin both "+
			"ends together; a pin edited to match a fixture nobody re-read turns this measurement into an "+
			"agreement with itself", taskNotificationCapturePath, frames, taskNotificationSubtype, got, want)
	}
}

// TestTaskNotificationReaderGateHasExactlyOneLegalSkip proves the four quadrants of
// the sequencing gate, and it runs on every leg including the one where the fixture
// does not exist yet — which is the leg where the reader above can assert nothing
// and this is the only non-vacuous coverage in the file.
//
// The third row is the one the whole design turns on. #2229's live gate ran green
// and landed none of the bytes its acceptance criteria asked for; here the equivalent
// miss is bytes that land with nothing pinning what they measured, and a gate that
// passed in that state would hide it exactly as well.
func TestTaskNotificationReaderGateHasExactlyOneLegalSkip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		fixtureExists bool
		pinFilled     bool
		want          string
	}{
		{"before the live gate has run: the one legal skip", false, false, taskNotificationGateSkip},
		{"a pin whose bytes are gone", false, true, taskNotificationGateFatal},
		{"bytes committed with nothing pinning them", true, false, taskNotificationGateFatal},
		{"the steady state", true, true, taskNotificationGateRun},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, reason := taskNotificationReaderGate(tc.fixtureExists, tc.pinFilled)
			if action != tc.want {
				t.Errorf("taskNotificationReaderGate(%v, %v) = %q, want %q", tc.fixtureExists,
					tc.pinFilled, action, tc.want)
			}
			if action == taskNotificationGateRun && reason != "" {
				t.Errorf("the run state named a reason %q; nothing reads it", reason)
			}
			if action != taskNotificationGateRun && reason == "" {
				t.Errorf("%s named no reason; the skip or fatal message would say nothing", action)
			}
		})
	}
}

// capturedTaskNotificationLine returns the bytes of the ONE system/task_notification
// line the committed capture holds, as claude sent them.
//
// It lives here rather than in capture_test.go because this file is the fourth
// reader and owns the constants that address this record: capturedLines' docblock
// forbids by name growing any of the three older readers a path parameter, and a
// reader reaching for the wrong record would decode zero values rather than fail.
//
// Every provenance check the pin test makes is made again here rather than assumed
// from it, because the two run independently and a helper that trusted a sibling
// test to have run would hand a mapping assertion whatever bytes were on disk.
//
// Every failure is t.Fatalf and none is a skip: the capture is committed, so
// absence is a broken premise rather than an unavailable resource. In particular a
// count other than exactly one fatals HERE, one level below every assertion built
// on the line, so none of them can pass vacuously.
func capturedTaskNotificationLine(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile(taskNotificationCapturePath)
	if err != nil {
		t.Fatalf("reading capture %s: %v — the fixture is committed, so its absence is a broken "+
			"premise; taskNotificationReaderGate states the one leg on which it may be missing and "+
			"that leg is spent", taskNotificationCapturePath, err)
	}
	var capture taskNotificationCaptureRecord
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", taskNotificationCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — a mapping proven against a hand-written payload proves "+
			"only that the author and the mapper expected the same fields", taskNotificationCapturePath)
	}
	if got, _, _ := strings.Cut(capture.ClaudeVersion, " "); got != taskNotificationCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q", taskNotificationCapturePath,
			capture.ClaudeVersion, taskNotificationCaptureVersion)
	}

	var lines [][]byte
	for _, f := range capture.Frames {
		if f.Type != "system" || f.Subtype != taskNotificationSubtype {
			continue
		}
		if f.PayloadEncoding != "json-string" {
			t.Fatalf("%s: frame %d payload_encoding = %q, want %q — a line that was not valid UTF-8 "+
				"carries no replayable payload", taskNotificationCapturePath, f.Index,
				f.PayloadEncoding, "json-string")
		}
		lines = append(lines, []byte(f.Payload))
	}
	if len(lines) != 1 {
		t.Fatalf("%s: holds %d system/%s lines, want exactly 1. Zero makes every assertion built on "+
			"this line vacuous; more than one makes \"the captured line\" ambiguous and this helper "+
			"would be silently picking one", taskNotificationCapturePath, len(lines),
			taskNotificationSubtype)
	}
	return lines[0]
}
