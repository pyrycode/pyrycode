package streamsup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

// #2251 — the committed capture of claude's system/init line taken with an effort
// ACTUALLY SET, readable from this package.
//
// systemInitLine declares one field, and its doc states the rule this family
// follows: the field mapping comes from a committed capture, never from a
// hand-built payload or an SDK type definition. Three more of the line's keys have
// an operator consumer — claude_code_version, permissionMode and effort — and #2252
// declares them from what this reader pins.
//
// It reads a file under internal/e2e/realclaude/testdata/ for the reason
// compactionCapturePath's doc gives: the bytes are only BYTES, the e2e_realclaude
// build tag belongs to that package's Go FILES rather than to its testdata, and
// reading it from here is what keeps the measurement inside `make check` instead of
// behind a gate that exits 0 with zero tests executed when there is no claude login.
//
// THIS IS A FOURTH SELF-CONTAINED READER, NOT A GENERALISATION of capturedLines,
// capturedToolProgressLines or capturedInitialize. Each of the four reads a
// DIFFERENT record shape, which is what makes reaching for the wrong one yield zero
// values rather than a failure; each keeps its own package constants and takes no
// path parameter, because the provenance assertions are what stop a hand-built file
// being swapped in behind them.
//
// # Why the pin is only evidence together with the witnesses
//
// The finding this capture may well carry is that the init line has NO effort key.
// That is a useful answer only if the same bytes prove an effort was set, or
// "claude does not publish it" cannot be told from "nobody asked". So the pin test
// asserts both witnesses the producing probe records — `--effort <A>` adjacent in
// the launch argv, and claude's own acknowledgement of the `/effort <B>` turn —
// before it compares a single key set.

const (
	// effortInitCaptureVersion is the ONE claude release these bytes record, spliced
	// into the path below rather than repeated so the filename cannot drift from the
	// version this reader enforces. The producing side refuses to write under a
	// mismatched name (effortInitPromotable), so a claude upgrade is a loud
	// instruction to re-capture and repin rather than a fixture that quietly measures
	// another release.
	effortInitCaptureVersion = "2.1.259"

	// effortInitCaptureArm is the producing probe's single arm name, and the second
	// half of the name-versus-content binding below.
	effortInitCaptureArm = "sonnet_effort"

	effortInitCapturePath = "../e2e/realclaude/testdata/effort_init_v" +
		effortInitCaptureVersion + "_" + effortInitCaptureArm + ".json"

	// The two levels the capture set, on the launch argv and in band. Restated here
	// rather than imported — that package is behind a build tag this one does not
	// carry — and asserted against the record's own account of itself, so a capture
	// taken at other levels cannot be read under this pin.
	effortInitCaptureLaunchLevel = "low"
	effortInitCaptureInBandLevel = "high"
)

// effortInitPin is ONE init line's pinned answer: the line's FULL sorted key set
// plus the three values with an operator consumer.
//
// EffortPresent is separate from Effort because the two absences are different
// findings. A line carrying `"effort": ""` says claude publishes the key and left it
// empty; a line carrying no such key says claude does not publish it at all, and
// #2252 must declare a field for the first and must not for the second.
type effortInitPin struct {
	Keys              []string
	ClaudeCodeVersion string
	PermissionMode    string
	Effort            string
	EffortPresent     bool
}

// effortInitPins IS THE MEASUREMENT THIS TICKET COMMITS, one entry per init line in
// the capture, in the order the lines appear.
//
// IT IS EMPTY ON PURPOSE UNTIL THE LIVE GATE HAS RUN. The fixture cannot exist
// before `make e2e-realclaude` produces it on an authenticated machine, which
// happens after verification, so a reader asserting against bytes any earlier would
// redden `make check` for every unrelated ticket. effortInitReaderGate turns that
// into a state machine with exactly one legal skip: filling this slice is the commit
// that lands the fixture, and a fixture landing WITHOUT it fatals rather than
// passing quietly.
//
// Transcribe it from the COMMITTED file, never from a run log. #2237 measured why:
// for the compaction capture three sources offered the same numbers — the ticket
// body, a recovered temp-directory record and the committed fixture — and they did
// not agree. Only the committed file is something a later reviewer can re-open.
var effortInitPins = []effortInitPin{}

// The four states of (fixture, pin). Only the first is a skip, and only on the leg
// before the live gate has ever run.
const (
	effortInitGateSkip  = "skip"
	effortInitGateRun   = "run"
	effortInitGateFatal = "fatal"
)

// effortInitReaderGate is pure, so all four quadrants are proved on every run —
// including the leg where the fixture is still absent and the reader itself can
// assert nothing.
func effortInitReaderGate(fixtureExists, pinFilled bool) (action, reason string) {
	switch {
	case !fixtureExists && !pinFilled:
		return effortInitGateSkip, "the capture has not been taken yet: `make e2e-realclaude` on an " +
			"authenticated machine runs TestRealClaude_EffortInitCapture, which arms on this fixture's " +
			"absence and writes it in-repo. This is the ONLY state in which this reader may skip, and " +
			"it ends the moment the bytes and the pin land together"
	case !fixtureExists && pinFilled:
		return effortInitGateFatal, "effortInitPins names what a capture observed but " +
			effortInitCapturePath + " is gone. Restore the fixture, or if the capture was deliberately " +
			"dropped, empty the pin in the same commit — a pin with no bytes behind it is a measurement " +
			"nothing supports"
	case fixtureExists && !pinFilled:
		return effortInitGateFatal, "the capture at " + effortInitCapturePath + " has landed but " +
			"effortInitPins is still empty, so nothing pins what it measured. Read the record's " +
			"effort_capture.init_lines and write them here IN THE COMMIT THAT ADDS THE FIXTURE. Until " +
			"then the bytes are committed and unpinned, which is the state this gate exists to make " +
			"impossible"
	default:
		return effortInitGateRun, ""
	}
}

// effortInitRecord is the slice of the capture this reader needs.
//
// A SECOND shape over setModeFixtureRecord, whose authoritative declaration sits
// behind the e2e_realclaude tag and can be read but not imported. capturedInitialize's
// doc names the risk this accepts — a parallel struct is how a field rename lands as
// a silent zero value — and the surface is minimised the same way: six fields, every
// one of them written on every arm of that record rather than on some.
//
// StdoutEvents is []json.RawMessage, which is what makes a replay a replay: each
// entry keeps claude's own line bytes, so every value below is re-derived from what
// claude wrote rather than from what a recorder made of it.
type effortInitRecord struct {
	ClaudeVersion string   `json:"claude_version"`
	Arm           string   `json:"arm"`
	Argv          []string `json:"argv"`
	Prompts       []string `json:"prompts"`

	StdoutEvents []json.RawMessage `json:"stdout_events"`

	EffortCapture *struct {
		LaunchLevel         string `json:"launch_level"`
		InBandLevel         string `json:"inband_level"`
		LaunchFlagSeen      bool   `json:"launch_flag_seen"`
		InBandPrompt        string `json:"inband_prompt"`
		Acknowledgement     string `json:"acknowledgement"`
		AcknowledgementSeen bool   `json:"acknowledgement_seen"`
	} `json:"effort_capture"`
}

// effortInitFlagAdjacent reports whether argv carries `--effort <level>` as an
// adjacent pair.
//
// Re-derived here rather than read off the record's launch_flag_seen, and the two
// are compared below. A reader that trusted the label would pin the probe's opinion
// of its own argv; a reader that only re-derived would lose the cross-check that the
// probe and the argv agree. Adjacency rather than presence, because an argv carrying
// the flag with some OTHER level, or with no value at all, must not read as the
// witness.
func effortInitFlagAdjacent(argv []string, level string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--effort" && argv[i+1] == level {
			return true
		}
	}
	return false
}

// effortInitReadPins re-derives one pin per system/init line from the line's OWN
// bytes: the full sorted key set, and the three values as claude spelled them.
//
// It decodes into map[string]json.RawMessage rather than a named struct because the
// key set IS the measurement and it drifts across releases — 19, 20, 22 and 24 keys
// are all committed in that directory — so a named-field decode would report absent
// for a spelling it merely did not anticipate.
func effortInitReadPins(t *testing.T, events []json.RawMessage) []effortInitPin {
	t.Helper()

	out := []effortInitPin{}
	for _, ev := range events {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(ev, &fields); err != nil {
			// A retained non-JSON line is a JSON string in the record; skipping it is
			// not a silent drop, because the init-line COUNT is asserted below.
			continue
		}
		if effortInitScalar(fields["type"]) != "system" || effortInitScalar(fields["subtype"]) != "init" {
			continue
		}
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		effort, present := fields["effort"]
		out = append(out, effortInitPin{
			Keys:              keys,
			ClaudeCodeVersion: effortInitScalar(fields["claude_code_version"]),
			PermissionMode:    effortInitScalar(fields["permissionMode"]),
			Effort:            effortInitScalar(effort),
			EffortPresent:     present,
		})
	}
	return out
}

// effortInitScalar renders one raw value: the decoded string when it is one, the
// raw JSON otherwise. A non-string effort — a number, an object — is exactly the
// shape a guessed decode target gets wrong, so it is pinned as what it is rather
// than flattened to "".
func effortInitScalar(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// TestEffortInitReaderGate_HasExactlyOneLegalSkip proves all four quadrants,
// including on the leg where the fixture is still absent and the pin test below can
// assert nothing. The reason strings are checked for non-emptiness because each one
// is the only instruction an operator gets at that state.
func TestEffortInitReaderGate_HasExactlyOneLegalSkip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		fixture, pin bool
		want         string
	}{
		{false, false, effortInitGateSkip},
		{false, true, effortInitGateFatal},
		{true, false, effortInitGateFatal},
		{true, true, effortInitGateRun},
	}
	for _, tc := range cases {
		action, reason := effortInitReaderGate(tc.fixture, tc.pin)
		if action != tc.want {
			t.Errorf("effortInitReaderGate(%v, %v) = %q, want %q", tc.fixture, tc.pin, action, tc.want)
		}
		if action != effortInitGateRun && reason == "" {
			t.Errorf("effortInitReaderGate(%v, %v) gives no reason; an operator at this state has "+
				"nothing to act on", tc.fixture, tc.pin)
		}
		if action == effortInitGateRun && reason != "" {
			t.Errorf("effortInitReaderGate(%v, %v) is the running state and must carry no reason, "+
				"got %q", tc.fixture, tc.pin, reason)
		}
	}
}

// TestEffortInitFlagAdjacent_RequiresTheLevelAfterTheFlag pins the witness rule
// this reader enforces. It runs whether or not the fixture has landed, so the rule
// the pin test rests on is proved on every leg.
func TestEffortInitFlagAdjacent_RequiresTheLevelAfterTheFlag(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"adjacent", []string{"--model", "claude-sonnet-5", "--effort", "low"}, true},
		{"flag with no value", []string{"--effort"}, false},
		{"a different level", []string{"--effort", "high"}, false},
		{"level present but not after the flag", []string{"low", "--effort", "high"}, false},
		{"no flag", []string{"--model", "claude-sonnet-5"}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := effortInitFlagAdjacent(tc.argv, "low"); got != tc.want {
				t.Errorf("effortInitFlagAdjacent(%q, %q) = %v, want %v", tc.argv, "low", got, tc.want)
			}
		})
	}
}

// TestRealClaudeEffortInitCaptureIsPinned reads the committed capture and pins, per
// system/init line, the full sorted key set plus claude_code_version, permissionMode
// and effort — recording an ABSENT effort as the pinned answer when it is absent.
//
// Every failure once the gate says run is t.Fatalf, never a skip: the capture is
// committed, so a missing or contradictory record is a broken premise rather than an
// unavailable resource.
func TestRealClaudeEffortInitCaptureIsPinned(t *testing.T) {
	raw, readErr := os.ReadFile(effortInitCapturePath)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("reading capture %s: %v", effortInitCapturePath, readErr)
	}
	switch action, reason := effortInitReaderGate(exists, len(effortInitPins) > 0); action {
	case effortInitGateSkip:
		t.Skipf("#2251: %s", reason)
	case effortInitGateFatal:
		t.Fatalf("#2251: %s", reason)
	}

	var rec effortInitRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decoding capture %s: %v", effortInitCapturePath, err)
	}

	// Binds the file's NAME to its CONTENT: a swapped file, a re-capture at a later
	// claude, and one arm's bytes committed under another arm's name all land here.
	// `claude --version` prints "<version> (Claude Code)", so the comparison is on the
	// leading token; an "<unavailable: ...>" version fails it too, which is correct —
	// a record that could not read its own release cannot vouch for one.
	if got, _, _ := strings.Cut(rec.ClaudeVersion, " "); got != effortInitCaptureVersion {
		t.Fatalf("%s: claude_version is %q, want %q — the filename pins the release this capture "+
			"measures. Re-capture at the pinned version, or bump effortInitFixtureVersion, "+
			"effortInitCaptureVersion and effortInitPins together", effortInitCapturePath,
			rec.ClaudeVersion, effortInitCaptureVersion)
	}
	if rec.Arm != effortInitCaptureArm {
		t.Fatalf("%s: arm is %q but the filename says %q; the name and the content disagree, so "+
			"neither can be trusted to say which capture these bytes are", effortInitCapturePath,
			rec.Arm, effortInitCaptureArm)
	}

	// THE TWO WITNESSES, asserted before any key set is compared. Without them a
	// pinned "effort absent" cannot be told from "no effort was ever set", which is
	// the only way this capture can fail to answer the question it was run for.
	if rec.EffortCapture == nil {
		t.Fatalf("%s: no effort_capture block, so the record makes no claim that an effort was ever "+
			"set and its init lines cannot answer the question", effortInitCapturePath)
	}
	if rec.EffortCapture.LaunchLevel != effortInitCaptureLaunchLevel ||
		rec.EffortCapture.InBandLevel != effortInitCaptureInBandLevel {
		t.Fatalf("%s: the record was taken at --effort %q then /effort %q, but this pin was written "+
			"for %q then %q", effortInitCapturePath, rec.EffortCapture.LaunchLevel,
			rec.EffortCapture.InBandLevel, effortInitCaptureLaunchLevel, effortInitCaptureInBandLevel)
	}
	adjacent := effortInitFlagAdjacent(rec.Argv, effortInitCaptureLaunchLevel)
	if !adjacent {
		t.Fatalf("%s: the recorded argv carries no `--effort %s` pair, so the child was never "+
			"launched with an effort and an absent effort key proves nothing:\n  argv: %q",
			effortInitCapturePath, effortInitCaptureLaunchLevel, rec.Argv)
	}
	if adjacent != rec.EffortCapture.LaunchFlagSeen {
		t.Errorf("%s: launch_flag_seen is %v but the argv re-derives as %v; the record contradicts "+
			"itself about its own launch", effortInitCapturePath, rec.EffortCapture.LaunchFlagSeen,
			adjacent)
	}
	want := "/effort " + effortInitCaptureInBandLevel
	if !slices.Contains(rec.Prompts, want) {
		t.Fatalf("%s: no %q turn among the recorded prompts %q, so the in-band path was never "+
			"exercised", effortInitCapturePath, want, rec.Prompts)
	}
	// The prompts list is what was WRITTEN to the child; inband_prompt is the record's
	// own account of what it meant to write. Checking both against the same literal is
	// the second half of the re-derive-and-cross-check rule the argv row above applies.
	if rec.EffortCapture.InBandPrompt != want {
		t.Errorf("%s: inband_prompt is %q but the in-band turn this pin was written for is %q",
			effortInitCapturePath, rec.EffortCapture.InBandPrompt, want)
	}
	if !rec.EffortCapture.AcknowledgementSeen || rec.EffortCapture.Acknowledgement == "" {
		t.Fatalf("%s: the /effort turn produced no acknowledgement from claude (seen=%v), so nothing "+
			"says the command was received", effortInitCapturePath,
			rec.EffortCapture.AcknowledgementSeen)
	}

	got := effortInitReadPins(t, rec.StdoutEvents)
	if len(got) < 2 {
		t.Fatalf("%s: %d system/init line(s) among %d recorded events; the measurement needs at "+
			"least two — one from the child launched with --effort %s and a later one after the "+
			"in-band /effort %s", effortInitCapturePath, len(got), len(rec.StdoutEvents),
			effortInitCaptureLaunchLevel, effortInitCaptureInBandLevel)
	}
	if len(got) != len(effortInitPins) {
		t.Fatalf("%s: the capture carries %d system/init line(s) but effortInitPins names %d. A "+
			"re-capture must repin every line, not the ones that happen to line up",
			effortInitCapturePath, len(got), len(effortInitPins))
	}

	for i, want := range effortInitPins {
		if !slices.Equal(got[i].Keys, want.Keys) {
			t.Errorf("init line %d: key set\n   on disk: %q\n  pinned:  %q", i, got[i].Keys, want.Keys)
		}
		if got[i].ClaudeCodeVersion != want.ClaudeCodeVersion {
			t.Errorf("init line %d: claude_code_version = %q, pinned %q", i,
				got[i].ClaudeCodeVersion, want.ClaudeCodeVersion)
		}
		if got[i].PermissionMode != want.PermissionMode {
			t.Errorf("init line %d: permissionMode = %q, pinned %q", i,
				got[i].PermissionMode, want.PermissionMode)
		}
		if got[i].EffortPresent != want.EffortPresent {
			t.Errorf("init line %d: an effort key is present=%v, pinned present=%v. A key appearing "+
				"or disappearing is the whole subject of this capture", i, got[i].EffortPresent,
				want.EffortPresent)
		}
		if got[i].Effort != want.Effort {
			t.Errorf("init line %d: effort = %q, pinned %q", i, got[i].Effort, want.Effort)
		}
		// The key set and the three values are pinned independently on purpose: a
		// value read out of a line whose key set does not contain its key would be a
		// silent zero, and the row above is what makes that a failure.
		if want.EffortPresent && !slices.Contains(got[i].Keys, "effort") {
			t.Errorf("init line %d: an effort value was pinned but the line's key set does not "+
				"carry the key; the pin and the bytes disagree", i)
		}
	}
}
