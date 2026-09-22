package streamsup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"testing"
)

// rosterAfterFinishCapturePath is the committed real-claude capture of one live turn
// that backgrounds a Bash command, lets it FINISH, and then reads on through three
// further prompts — a quiet window, a mid-session `initialize`, and a whole further
// turn — watching for a system/background_tasks_changed line (#2525). It is produced
// by internal/e2e/realclaude's TestRealClaude_RosterAfterFinishCapture.
//
// It reads a file under internal/e2e/realclaude/testdata/ for the reason
// taskNotificationCapturePath gives: the bytes are only BYTES, the e2e_realclaude
// build tag belongs to that package's Go files rather than to its testdata, and
// reading it from here is what keeps the measurement inside `make check` instead of
// behind an opt-in gate that SKIPS (exit 0) with no claude login.
//
// THIS IS A FIFTH READER, NOT A GENERALISATION OF THE FOUR. capturedLines' docblock
// forbids by name growing any of the older readers a path parameter, because its
// is_capture assertion is what stops a hand-built payload file being swapped in
// behind the provenance checks. capturedToolProgressLines, the compaction reader and
// taskNotificationCapturePath each restated that discipline rather than importing it,
// and so does this one: its own package constants, no path parameter, every
// provenance check written out below. None of the five can decode another's record
// shape, and reaching for the wrong one yields zero values rather than a failure.
//
// Resist the tidying instinct here. Five near-identical readers look like an
// extraction waiting to happen, and the duplication IS the mechanism.
const rosterAfterFinishCapturePath = "../e2e/realclaude/testdata/roster_after_finish_v" +
	rosterAfterFinishCaptureVersion + ".json"

// rosterAfterFinishCaptureVersion is the claude release the capture was taken at,
// spliced into the path above rather than repeated so the filename cannot drift from
// the version this reader enforces. The producing side refuses to write under a
// mismatched name (rafcapRecord.fixtureWorthy), so a claude upgrade is a loud
// instruction to re-capture and repin rather than a fixture that quietly measures
// another release.
const rosterAfterFinishCaptureVersion = "2.1.272"

// The closed verdict set, spelled here as literals rather than imported from the
// probe — which is behind a build tag this package never compiles, and which is the
// thing being measured rather than a thing to agree with.
//
// The names say what each one licenses a client to do, because that is the whole
// point of the capture: desktop #1246 assumes the roster clears itself and desktop
// #1558 assumes it never does, and exactly one of these four settles that.
const (
	// The terminal-status line never arrived, so the capture makes NO CLAIM about the
	// roster. Never promotable as a fixture; listed so the closed set is complete and
	// a record carrying it is rejected by name rather than by falling off the end.
	rosterAfterFinishVerdictNoClaim = "task-did-not-complete"
	// Zero rosters after the terminal status, across all three prompts. The expected
	// real answer, and a legitimate measurement rather than a vacuous one.
	rosterAfterFinishVerdictNone = "none-after-terminal-status"
	// A roster arrived and STILL LISTED the finished task: the count does not come
	// down by itself and #1246's criterion is unsatisfiable as written.
	rosterAfterFinishVerdictStillLists = "roster-still-lists-the-finished-task"
	// A roster arrived that omits the finished task, or carries an empty array:
	// #1246's criterion stands, qualified by which prompt produced the line.
	rosterAfterFinishVerdictOmits = "roster-omits-the-finished-task"
)

// rosterAfterFinishPinnedVerdict IS THE MEASUREMENT THIS TICKET COMMITS: the verdict
// the committed record reached, transcribed here from the record's own `verdict`
// field AND NEVER FROM WHAT THE PROBE EXPECTED TO SEE.
//
// IT IS EMPTY ON PURPOSE UNTIL THE LIVE GATE HAS RUN, and that is what sequences this
// ticket. The fixture cannot exist before `make e2e-realclaude` produces it on an
// authenticated machine, which happens after verification, so a reader asserting
// against bytes any earlier would redden `make check` for every unrelated ticket.
// rosterAfterFinishReaderGate turns that into a state machine with exactly one legal
// skip: FILLING THIS STRING IS THE COMMIT THAT LANDS THE FIXTURE, and a fixture
// landing without it fatals rather than passing quietly.
//
// #2229 is why the gate is shaped this way rather than trusted to memory. Its probe
// fired green against claude 2.1.259 and wrote its record, but the producing run was
// the dispatcher's gate-only real-claude lap, which verifies from a detached worktree
// and never commits — so the in-repo write went out with the worktree and the bytes
// had to be landed by a follow-up ticket that also had to fill the pin its reader had
// shipped empty. The promotion log on the producing side prints this line ready to
// paste, so filling it needs no second reading of the record.
//
// The same commit that fills this owes two things the record cannot write for itself:
// one sentence in emitBackgroundTaskRoster's doc comment naming the claude version
// measured, and the same finding cross-posted on pyrycode/pyrycode-desktop#1246 and
// #1558 so the two client tickets stop assuming opposite things.
var rosterAfterFinishPinnedVerdict = ""

// The four states of (fixture, pin). Only the first is a skip, and only on the leg
// before the live gate has ever run.
const (
	rosterAfterFinishGateSkip  = "skip"
	rosterAfterFinishGateRun   = "run"
	rosterAfterFinishGateFatal = "fatal"
)

// rosterAfterFinishReaderGate is pure so all four quadrants are proved on every run,
// including the leg where the fixture is still absent and the reader itself cannot
// assert anything.
func rosterAfterFinishReaderGate(fixtureExists, verdictPinned bool) (action, reason string) {
	switch {
	case !fixtureExists && !verdictPinned:
		return rosterAfterFinishGateSkip, "the capture has not been taken yet: `make e2e-realclaude` on " +
			"an authenticated machine runs TestRealClaude_RosterAfterFinishCapture, which arms on this " +
			"fixture's absence and writes it in-repo. This is the ONLY state in which this reader may " +
			"skip, and it ends the moment the bytes and the verdict land together"
	case !fixtureExists && verdictPinned:
		return rosterAfterFinishGateFatal, "rosterAfterFinishPinnedVerdict names a verdict some capture " +
			"reached but " + rosterAfterFinishCapturePath + " is gone. Restore the fixture, or if the " +
			"capture was deliberately dropped, empty the pin in the same commit — a verdict with no bytes " +
			"behind it is a measurement nothing supports, and this one is quoted to two client tickets"
	case fixtureExists && !verdictPinned:
		return rosterAfterFinishGateFatal, "the capture at " + rosterAfterFinishCapturePath + " has " +
			"landed but rosterAfterFinishPinnedVerdict is still empty, so nothing transcribes what it " +
			"measured. Read the record's `verdict` and write it here IN THE COMMIT THAT ADDS THE FIXTURE, " +
			"together with the doc sentence and the two cross-posts that verdict owes. Until then the " +
			"bytes are committed and untranscribed, which is the state this gate exists to make impossible"
	default:
		return rosterAfterFinishGateRun, ""
	}
}

// rosterAfterFinishIsVerdict answers whether s is one of the four. A verdict outside
// the closed set is a record this reader cannot interpret, and guessing at it is how
// a client ends up told something the capture never said.
func rosterAfterFinishIsVerdict(s string) bool {
	switch s {
	case rosterAfterFinishVerdictNoClaim, rosterAfterFinishVerdictNone,
		rosterAfterFinishVerdictStillLists, rosterAfterFinishVerdictOmits:
		return true
	}
	return false
}

// rosterAfterFinishCaptureRecord is the slice of the record this reader needs.
//
// It decodes the record's verdict AND the roster observations the verdict was read
// from, because the two are checked against each other: a verdict is a one-word
// summary, and a summary that disagrees with its own evidence is worse than none.
// Nothing else of the record is decoded here — the probe's own censuses and staging
// booleans are for a human reading the file, not for this gate.
type rosterAfterFinishCaptureRecord struct {
	IsCapture     bool   `json:"is_capture"`
	ClaudeVersion string `json:"claude_version"`
	Verdict       string `json:"verdict"`
	VerdictPrompt string `json:"verdict_prompt"`
	Rosters       []struct {
		Index       int    `json:"index"`
		Phase       string `json:"phase"`
		OffsetLines int    `json:"offset_lines"`
	} `json:"rosters"`
}

// rosterAfterFinishPostTerminal counts the roster observations that arrived AFTER the
// terminal-status line — the only ones the verdict may be read from. offset_lines is
// the roster's index minus the terminal-status line's, so a strictly positive offset
// is exactly "after it"; a pre-terminal roster (the 2.1.220 shape, a roster at the
// task's START) carries a negative one and says nothing about a completion.
func rosterAfterFinishPostTerminal(rec rosterAfterFinishCaptureRecord) int {
	n := 0
	for _, r := range rec.Rosters {
		if r.OffsetLines > 0 {
			n++
		}
	}
	return n
}

// TestRealClaudeRosterAfterFinishVerdictIsPinned reads the committed capture and pins
// the verdict it reached — the fact desktop #1246 and #1558 currently assume opposite
// answers to.
//
// Every failure once the gate says run is t.Fatalf, never a skip: the capture is
// committed, so a missing or unreadable record is a broken premise rather than an
// unavailable resource.
func TestRealClaudeRosterAfterFinishVerdictIsPinned(t *testing.T) {
	raw, readErr := os.ReadFile(rosterAfterFinishCapturePath)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("reading capture %s: %v", rosterAfterFinishCapturePath, readErr)
	}
	switch action, reason := rosterAfterFinishReaderGate(exists, rosterAfterFinishPinnedVerdict != ""); action {
	case rosterAfterFinishGateSkip:
		t.Skipf("#2525: %s", reason)
	case rosterAfterFinishGateFatal:
		t.Fatalf("#2525: %s", reason)
	}

	var capture rosterAfterFinishCaptureRecord
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", rosterAfterFinishCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false — this file must be a genuine claude capture, never a "+
			"hand-written record; a guessed one carries whatever answer its author expected, which is "+
			"precisely the thing two client tickets are already doing without it",
			rosterAfterFinishCapturePath)
	}
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on the
	// leading token; an "<unavailable: ...>" version fails it too, which is correct — a
	// capture that could not read the version it was taken at cannot vouch for the
	// release its filename claims.
	if got := rosterAfterFinishLeadingToken(capture.ClaudeVersion); got != rosterAfterFinishCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures, so a record from a different claude must not be read under it. Re-capture at the "+
			"pinned version, or bump rafcapFixtureVersion, rosterAfterFinishCaptureVersion and "+
			"rosterAfterFinishPinnedVerdict together", rosterAfterFinishCapturePath, capture.ClaudeVersion,
			rosterAfterFinishCaptureVersion)
	}
	if !rosterAfterFinishIsVerdict(capture.Verdict) {
		t.Fatalf("%s: verdict is %q, which is outside the closed set {%s, %s, %s, %s}. A verdict this "+
			"reader cannot interpret is one no client can be told", rosterAfterFinishCapturePath,
			capture.Verdict, rosterAfterFinishVerdictNoClaim, rosterAfterFinishVerdictNone,
			rosterAfterFinishVerdictStillLists, rosterAfterFinishVerdictOmits)
	}
	if capture.Verdict == rosterAfterFinishVerdictNoClaim {
		t.Fatalf("%s: verdict is %q, so the staged task never completed and the record makes no claim "+
			"about the roster. The producing side refuses to promote such a run (rafcapRecord."+
			"fixtureWorthy), so these bytes were not written by a passing capture — re-run the live gate "+
			"and read its staging verdict", rosterAfterFinishCapturePath, capture.Verdict)
	}

	// The record is checked against ITSELF before it is checked against the pin. A
	// verdict is one word and the rosters array is the evidence it summarises; a
	// summary that contradicts its own evidence would otherwise be transcribed into
	// emitBackgroundTaskRoster's doc and quoted to two client tickets.
	postTerminal := rosterAfterFinishPostTerminal(capture)
	if capture.Verdict == rosterAfterFinishVerdictNone && postTerminal > 0 {
		t.Fatalf("%s: verdict is %q but the record lists %d roster line(s) after the terminal-status "+
			"line. The record disagrees with its own evidence and neither half can be trusted",
			rosterAfterFinishCapturePath, capture.Verdict, postTerminal)
	}
	if capture.Verdict != rosterAfterFinishVerdictNone && postTerminal == 0 {
		t.Fatalf("%s: verdict is %q, which can only be read off a roster line, but the record lists "+
			"none after the terminal-status line", rosterAfterFinishCapturePath, capture.Verdict)
	}
	// Where a roster was seen the verdict must name WHICH of the three prompts
	// preceded it: "the roster answers a mid-session control request only" is a
	// different instruction to a client than "claude sends it unprompted".
	if capture.Verdict != rosterAfterFinishVerdictNone && capture.VerdictPrompt == "" {
		t.Fatalf("%s: verdict is %q but verdict_prompt is empty, so the record does not say which of the "+
			"quiet window, the mid-session initialize or the follow-on turn produced the line — which is "+
			"the distinction this capture exists to make", rosterAfterFinishCapturePath, capture.Verdict)
	}

	if capture.Verdict != rosterAfterFinishPinnedVerdict {
		t.Fatalf("%s: the record's verdict is %q but rosterAfterFinishPinnedVerdict says %q.\nIf claude's "+
			"behaviour genuinely changed, re-capture at the new version and repin both ends together — "+
			"and re-post the finding on pyrycode/pyrycode-desktop#1246 and #1558, which were told the old "+
			"one. A pin edited to match a fixture nobody re-read turns this measurement into an agreement "+
			"with itself", rosterAfterFinishCapturePath, capture.Verdict, rosterAfterFinishPinnedVerdict)
	}
}

// rosterAfterFinishLeadingToken returns everything before the first space, which is
// the version out of `claude --version`'s "<version> (Claude Code)".
//
// Spelled out here rather than reached for from a sibling reader for this file's
// stated reason: the five readers share no helper, so none of them can be repointed
// at another's record by an edit to one place.
func rosterAfterFinishLeadingToken(version string) string {
	for i := 0; i < len(version); i++ {
		if version[i] == ' ' {
			return version[:i]
		}
	}
	return version
}

// TestRosterAfterFinishReaderGateHasExactlyOneLegalSkip proves the four quadrants of
// the sequencing gate, and it runs on every leg including the one where the fixture
// does not exist yet — which is the leg where the reader above can assert nothing and
// this is the only non-vacuous coverage in the file.
//
// The third row is the one the whole design turns on. #2229's live gate ran green and
// landed none of the bytes its acceptance criteria asked for; here the equivalent miss
// is bytes that land with nothing transcribing what they measured, and a gate that
// passed in that state would hide it exactly as well. The handoff this ticket cannot
// perform — the doc sentence and the two desktop cross-posts — hangs off that same
// commit, so a silent pass would lose all three at once.
func TestRosterAfterFinishReaderGateHasExactlyOneLegalSkip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		fixtureExists bool
		verdictPinned bool
		want          string
	}{
		{"before the live gate has run: the one legal skip", false, false, rosterAfterFinishGateSkip},
		{"a verdict whose bytes are gone", false, true, rosterAfterFinishGateFatal},
		{"bytes committed with nothing transcribing them", true, false, rosterAfterFinishGateFatal},
		{"the steady state", true, true, rosterAfterFinishGateRun},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, reason := rosterAfterFinishReaderGate(tc.fixtureExists, tc.verdictPinned)
			if action != tc.want {
				t.Errorf("rosterAfterFinishReaderGate(%v, %v) = %q, want %q", tc.fixtureExists,
					tc.verdictPinned, action, tc.want)
			}
			if action == rosterAfterFinishGateRun && reason != "" {
				t.Errorf("the run state named a reason %q; nothing reads it", reason)
			}
			if action != rosterAfterFinishGateRun && reason == "" {
				t.Errorf("%s named no reason; the skip or fatal message would say nothing", action)
			}
		})
	}
}

// TestRosterAfterFinishVerdictSetIsClosed pins that the reader accepts exactly the
// four verdicts and nothing else — including the near-misses a hand-edited record
// would plausibly carry.
//
// Its value is at the boundary: "none" and "unknown" are what somebody summarising
// the capture by hand would write, and either would sail past a reader that only
// checked for a non-empty string.
func TestRosterAfterFinishVerdictSetIsClosed(t *testing.T) {
	t.Parallel()
	for _, v := range []string{
		rosterAfterFinishVerdictNoClaim, rosterAfterFinishVerdictNone,
		rosterAfterFinishVerdictStillLists, rosterAfterFinishVerdictOmits,
	} {
		if !rosterAfterFinishIsVerdict(v) {
			t.Errorf("rosterAfterFinishIsVerdict(%q) = false for a member of the set", v)
		}
	}
	for _, v := range []string{"", "none", "unknown", "completed", "NONE-AFTER-TERMINAL-STATUS"} {
		if rosterAfterFinishIsVerdict(v) {
			t.Errorf("rosterAfterFinishIsVerdict(%q) = true; the set is closed and this is not in it", v)
		}
	}
}

// TestRosterAfterFinishPostTerminalCountsOnlyWhatFollowedTheStatus pins the one
// arithmetic the self-consistency check rests on.
//
// The pre-terminal case is real rather than hypothetical: dropped_lines_v2.1.220.json
// records a roster at the task's START, in the turn that backgrounded the command.
// Counting that one would let a "roster-omits" verdict be read off a line that
// arrived before anything had finished.
func TestRosterAfterFinishPostTerminalCountsOnlyWhatFollowedTheStatus(t *testing.T) {
	t.Parallel()
	rec := func(offsets ...int) rosterAfterFinishCaptureRecord {
		var out rosterAfterFinishCaptureRecord
		for i, o := range offsets {
			out.Rosters = append(out.Rosters, struct {
				Index       int    `json:"index"`
				Phase       string `json:"phase"`
				OffsetLines int    `json:"offset_lines"`
			}{Index: i, OffsetLines: o})
		}
		return out
	}
	tests := []struct {
		name string
		in   rosterAfterFinishCaptureRecord
		want int
	}{
		{"no rosters at all", rec(), 0},
		{"the 2.1.220 shape: one roster BEFORE the terminal status", rec(-9), 0},
		{"a roster on the terminal-status line itself is not after it", rec(0), 0},
		{"one after", rec(4), 1},
		{"one before and two after", rec(-9, 4, 61), 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := rosterAfterFinishPostTerminal(tc.in); got != tc.want {
				t.Errorf("rosterAfterFinishPostTerminal() = %d, want %d", got, tc.want)
			}
		})
	}
}
