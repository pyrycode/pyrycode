//go:build e2e_realclaude

package realclaude

// #1674 — the step between one arm's live drive and the record #1675 commits:
// a pure carrier of what that drive OBSERVED, and one pure function mapping it to
// #1662's eighteen-field poolRevokeFixtureRecord.
//
// Inside a live driver that step cannot be checked. The run costs 8–14 minutes, and
// SOME zeros in a committed artifact are honest measurements — a control arm's
// TakesSettingsUpdate, an untripped deadline, an empty not-delivered slice — so the
// only available check would be a human reading three JSON files and guessing which
// zeros were meant. Lifted out here it is an ordinary table running in milliseconds
// with no claude and no credentials.
//
// # Offline
//
// This file reaches no live claude, no daemon, no subprocess and no credential: it
// must not reach resolveClaudeBin, probeClaudeVersion, captureClaudeVersion,
// WithWorktree, WithWorktreeAuthenticated, os.Getenv, os.Environ or os.LookupEnv,
// and it must not reach packageDir or any of the wrappers that resolve the committed
// testdata/ through it. Unlike #1662's file it performs no I/O in EITHER direction:
// it builds values and asserts on a pure function's return.
// TestFinOfflineFilesReachNoExecHelper enforces the list over this file's AST rather
// than over this paragraph.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestPoolRevokeAssemble_|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// All must report PASS — not SKIP, not "no tests to run" — on a machine with no
// claude and no credentials. Read the count of tests that executed, never the exit
// code: this package is behind the e2e_realclaude tag, `make check` never compiles
// it, and the suite exits 0 both on a build failure and on a full credentials skip.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// --- the carrier ---------------------------------------------------------------

// poolRevokeObservation is everything ONE arm's drive observed, and nothing derived
// from it. #1675 fills one of these per arm and hands it to
// assemblePoolRevokeFixture; nothing else assembles that record.
//
// The field types are the OBSERVATION SOURCE's types, not the record's — stdoutLines
// is []json.RawMessage because that is what the tap's snapshotLines returns. Holding
// the record's []string here would move the conversion back into the live driver,
// where nothing can test it.
//
// EVERY SLICE ON THIS TYPE MUST BE A SNAPSHOT. The tap's snapshot accessors and the
// log recorder's notDelivered closure each return a fresh copy taken under their own
// mutex; a carrier filled from a recorder's live fields instead would be read while
// the child is still writing, and that is a race no test in this file can see.
//
// There is deliberately no env field and there must never be one, for the record
// type's own reason: the credential reaches the child through the environment while
// the argv carries none, so recording argv is safe and recording env would not be.
type poolRevokeObservation struct {
	// arm is the row the drive ran, and the ONLY source of the record's Arm,
	// LaunchYOLO and TakesSettingsUpdate. launchYOLO is the arm's STORED bootstrap
	// posture off its seeded registry entry, not a runtime flag and not a reading
	// of the composed argv — since #2065 every child launches with the escalation
	// flag on its argv, so the argv would answer a different question.
	arm poolRevokeArm

	// The version pair as captureClaudeVersion returned it: the whole line and its
	// leading token. Supplied by #1675; this file may not make that call.
	claudeVersionRaw string
	claudeVersion    string

	// argv is the composed spawn argv, recorded as an OBSERVATION. Nothing here
	// interprets it.
	argv []string

	// prompts are the probe's turns as sent.
	prompts []string

	// spawnCount is the log recorder's spawns() reading.
	spawnCount int

	// pidBefore and pidAfter are read at the two points at which the REVOKE arm
	// takes its settings update — on every arm, including the two controls that
	// take no update, so the arms stay identical. That requirement is what this
	// pair being a named field pair is for, and
	// TestPoolRevokeAssemble_ControlArmLeavesOnlyItsHonestZeros is what gives it
	// teeth: a control record whose pid pair is unfilled fails there.
	pidBefore int
	pidAfter  int

	// The tap's four snapshots.
	controlResponses []json.RawMessage
	initModes        []string
	stdoutLines      []json.RawMessage
	turnBoundaries   []int

	// probeOutcomes is the per-turn behavioural read, in turn order.
	probeOutcomes []probeOutcome

	// notDeliveredLogs is the log recorder's notDelivered() reading: the daemon's
	// own account of a failed in-band delivery, captured as emitted log records.
	notDeliveredLogs []string

	deadlineTripped bool

	// childOutputCapture is the FREE-TEXT child stream, and the only field of this
	// carrier that may hold one. The record's ChildOutputCapture is the field
	// designed to absorb it and the one writePoolRevokeFixture caps; free text
	// routed into stdoutLines instead would reach the committed artifact uncapped.
	childOutputCapture string
}

// --- the assembler ---------------------------------------------------------------

// assemblePoolRevokeFixture maps one arm's observations onto the record #1675
// commits. It takes no *testing.T, performs no I/O, and mutates neither obs nor any
// package state.
//
// It applies NO CAP. writePoolRevokeFixture caps ChildOutputCapture through
// capFixtureCapture, and ControlResponses, StdoutLines and NotDeliveredLogs reach
// the artifact uncapped by #1662's explicit constraint — capping the probe's
// structured evidence would destroy the artifact. A second cap here would bound them
// silently.
//
// It clones no slice either. The carrier's slices are already snapshots, so the
// returned record shares their backing arrays; sharing is not mutation, and the
// no-mutation row of TestPoolRevokeAssemble_FillsEveryFieldFromItsOwnObservation
// pins that nothing here sorts or truncates in place.
//
// The return is a VALUE, so the call site reads
// `rec := assemblePoolRevokeFixture(obs); writePoolRevokeFixture(t, dir, &rec)`.
func assemblePoolRevokeFixture(obs poolRevokeObservation) poolRevokeFixtureRecord {
	return poolRevokeFixtureRecord{
		ClaudeVersionRaw:    obs.claudeVersionRaw,
		ClaudeVersion:       obs.claudeVersion,
		Arm:                 obs.arm.name,
		LaunchYOLO:          obs.arm.launchYOLO,
		TakesSettingsUpdate: obs.arm.takesSettingsUpdate,
		Argv:                obs.argv,
		Prompts:             obs.prompts,
		SpawnCount:          obs.spawnCount,
		PIDBefore:           obs.pidBefore,
		PIDAfter:            obs.pidAfter,
		ControlResponses:    obs.controlResponses,
		InitPermissionModes: obs.initModes,
		StdoutLines:         poolRevokeStdoutLines(obs.stdoutLines),
		TurnBoundaries:      obs.turnBoundaries,
		ProbeOutcomes:       obs.probeOutcomes,
		NotDeliveredLogs:    obs.notDeliveredLogs,
		DeadlineTripped:     obs.deadlineTripped,
		ChildOutputCapture:  obs.childOutputCapture,
	}
}

// poolRevokeStdoutLines converts the tap's retained lines to the record's []string,
// and is the one step of the mapping that is not an assignment.
//
// setModeRecorder's add retains a valid JSON line VERBATIM and retains a non-JSON run
// by json.Marshal-ing it as a JSON string, so the tap's slice mixes two encodings
// while the record wants the bytes claude actually emitted in both cases. A raw
// message that decodes as a JSON string is therefore unquoted, and everything else is
// taken verbatim — which is exactly the two-element shape poolRevokeFullRecord's own
// StdoutLines literal shows.
//
// The ambiguity, stated rather than hidden: a line claude emitted as a BARE JSON
// STRING is valid JSON, is retained verbatim by the tap, and is unquoted here. It
// cannot arise — claude's stream-json emits objects — and nothing could distinguish
// it if it did, because the tap keeps no per-line non-JSON flag, only a count. The
// alternative, carrying the tap's quoting into the committed artifact, damages every
// genuinely non-JSON line, which is the case that actually occurs.
func poolRevokeStdoutLines(lines []json.RawMessage) []string {
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		var quoted string
		if err := json.Unmarshal(raw, &quoted); err == nil {
			out = append(out, quoted)
			continue
		}
		out = append(out, string(raw))
	}
	return out
}

// --- the type-derived zero sweep ---------------------------------------------------

// poolRevokeZeroFields returns the JSON tags of rec's zero-valued fields, in
// declaration order, DERIVED FROM THE RECORD TYPE rather than from a hand list.
//
// That is the whole point of the helper: poolRevokeFixtureRecord is this family's
// growth point, and an eighteen-line hand list passes unchanged the day a nineteenth
// field lands — which is the precise failure #1662's doc warns about, since a field
// nothing fills reads back as a zero in a COMMITTED file.
//
// #1662's own round trip IS a hand-written eighteen-row list, and that is correct for
// what it asserts: a round trip needs both sides of each row named. The mapping table
// below is a hand list for the same reason. Only the zero judgement comes off the
// type.
//
// The verdict is #1662's fixtureFieldNonZero, so an empty slice counts as zero rather
// than as a filled field. Every field of the record is exported, so Interface() is
// safe on all of them.
func poolRevokeZeroFields(rec poolRevokeFixtureRecord) []string {
	rv := reflect.ValueOf(rec)
	rt := rv.Type()

	zero := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		if fixtureFieldNonZero(rv.Field(i).Interface()) {
			continue
		}
		zero = append(zero, poolRevokeFieldName(rt.Field(i)))
	}
	return zero
}

// poolRevokeFieldName names a record field the way a reader of the committed artifact
// sees it — by JSON tag, falling back to the Go name for an untagged or omitted
// field so a future field is never reported as an empty string.
func poolRevokeFieldName(f reflect.StructField) string {
	tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if tag == "" || tag == "-" {
		return f.Name
	}
	return tag
}

// --- the carriers the tests are run over -------------------------------------------

// poolRevokeArmByName returns the named row of the real poolRevokeArms table.
//
// The rows are taken from that table rather than written as literals here because two
// of this file's claims are claims about the REAL arms: that only revoke carries
// takesSettingsUpdate, and that control_bypass is the control whose stored posture is
// true. poolRevokeArms is read-only — this ranges over it and copies one row out.
func poolRevokeArmByName(t *testing.T, name string) poolRevokeArm {
	t.Helper()

	for _, arm := range poolRevokeArms {
		if arm.name == name {
			return arm
		}
	}
	t.Fatalf("#1674: poolRevokeArms carries no %q row; this file's rows are taken from the "+
		"real table so that its claims are claims about the real arms", name)
	return poolRevokeArm{}
}

// poolRevokeFullObservation returns a carrier in which every observable is set to a
// distinct non-zero value, so that a record field left at its zero value means the
// assembler dropped it and a field carrying another field's value means the assembler
// crossed them.
//
// childOutputCapture is deliberately SHORT, well under stderrFixtureCap: the mapping
// table's failure message prints want and got, so an over-cap capture here would move
// 8 KiB out of a bounded field and into an unbounded run log on any unrelated red.
// The over-cap case belongs to
// TestPoolRevokeAssemble_RoutesFreeTextToChildOutputCaptureAndConvertsStdoutLines,
// whose messages report lengths only.
func poolRevokeFullObservation(t *testing.T) poolRevokeObservation {
	t.Helper()

	return poolRevokeObservation{
		arm:              poolRevokeArmByName(t, "revoke"),
		claudeVersionRaw: "2.1.220 (Claude Code)",
		claudeVersion:    "2.1.220",
		argv:             []string{"claude", "--dangerously-skip-permissions", "--print"},
		prompts:          []string{"probe turn one", "probe turn two"},
		spawnCount:       1,
		pidBefore:        4242,
		pidAfter:         4243,
		controlResponses: []json.RawMessage{
			json.RawMessage(`{"type":"control_response","response":{"subtype":"success","request_id":"req_1"}}`),
		},
		initModes: []string{"bypassPermissions", "default"},
		// The two encodings the tap produces, in one slice: a line claude emitted as
		// valid JSON, retained verbatim, and a non-JSON run the tap retained by
		// JSON-quoting it.
		stdoutLines: []json.RawMessage{
			json.RawMessage(`{"type":"system","subtype":"init"}`),
			json.RawMessage(`"not json at all"`),
		},
		turnBoundaries: []int{0, 7},
		probeOutcomes: []probeOutcome{{
			ToolUseNames:      []string{"Write"},
			ToolResultSeen:    true,
			ToolResultIsError: true,
			PermissionDenials: 1,
			ResultSubtype:     "success",
			ResultIsError:     true,
			ResultObserved:    true,
		}},
		notDeliveredLogs:   []string{"sessions: in-band settings command not delivered session=s1"},
		deadlineTripped:    true,
		childOutputCapture: "child stderr: the free-text stream this field absorbs",
	}
}

// poolRevokeFullStdoutLines is what poolRevokeFullObservation's stdoutLines must
// become in the record, written as a LITERAL rather than computed through
// poolRevokeStdoutLines: a want computed by the function under test compares that
// function with itself and pins nothing.
func poolRevokeFullStdoutLines() []string {
	return []string{`{"type":"system","subtype":"init"}`, "not json at all"}
}

// poolRevokeControlObservation returns a healthy CONTROL arm's carrier: the full
// carrier with control_bypass's row, no failed delivery, and the deadline reading the
// caller asks for.
//
// The arm is control_bypass and not control_default, and that is forced rather than
// chosen: control_default is seeded launchYOLO false, so its record would carry a
// FOURTH zero and "exactly these three are the honest zeros" would be false of it.
func poolRevokeControlObservation(t *testing.T, deadlineTripped bool) poolRevokeObservation {
	t.Helper()

	obs := poolRevokeFullObservation(t)
	obs.arm = poolRevokeArmByName(t, "control_bypass")
	obs.notDeliveredLogs = nil
	obs.deadlineTripped = deadlineTripped
	return obs
}

// --- every observable in the field that names it -------------------------------------

// TestPoolRevokeAssemble_FillsEveryFieldFromItsOwnObservation is #1674's AC 1 and
// AC 2: over a carrier whose every observable is distinct and non-zero, the assembled
// record leaves no field at its zero value, each observable lands in the field that
// names it, and the carrier comes back unmutated.
func TestPoolRevokeAssemble_FillsEveryFieldFromItsOwnObservation(t *testing.T) {
	t.Parallel()

	obs := poolRevokeFullObservation(t)
	// Constructed a SECOND time rather than shallow-copied: a shallow copy shares
	// every backing array, so an assembler that sorted or truncated a slice in place
	// would mutate both sides and the no-mutation row below would pass.
	pristine := poolRevokeFullObservation(t)

	rec := assemblePoolRevokeFixture(obs)

	t.Run("no field is left at its zero value", func(t *testing.T) {
		if zero := poolRevokeZeroFields(rec); len(zero) != 0 {
			t.Errorf("#1674: the assembled record leaves %q at zero from a carrier in which "+
				"every observable is set; #1675 commits this record, and a field nothing "+
				"filled reads back in the artifact as exactly the zero an honest measurement "+
				"would have produced", zero)
		}
	})

	// The mapping rows. A hand list on purpose, and the one place in this file that
	// is: an identity claim needs both sides named, which is why the sweep above
	// comes off the type and this does not.
	rows := []struct {
		name       string
		want, have any
	}{
		{"claude_version_raw", obs.claudeVersionRaw, rec.ClaudeVersionRaw},
		{"claude_version", obs.claudeVersion, rec.ClaudeVersion},
		{"arm", obs.arm.name, rec.Arm},
		{"launch_yolo", obs.arm.launchYOLO, rec.LaunchYOLO},
		{"takes_settings_update", obs.arm.takesSettingsUpdate, rec.TakesSettingsUpdate},
		{"argv", obs.argv, rec.Argv},
		{"prompts", obs.prompts, rec.Prompts},
		{"spawn_count", obs.spawnCount, rec.SpawnCount},
		{"pid_before_revocation", obs.pidBefore, rec.PIDBefore},
		{"pid_after_revocation", obs.pidAfter, rec.PIDAfter},
		{"control_responses", obs.controlResponses, rec.ControlResponses},
		{"init_permission_modes", obs.initModes, rec.InitPermissionModes},
		{"stdout_lines", poolRevokeFullStdoutLines(), rec.StdoutLines},
		{"turn_boundaries", obs.turnBoundaries, rec.TurnBoundaries},
		{"probe_outcomes", obs.probeOutcomes, rec.ProbeOutcomes},
		{"not_delivered_logs", obs.notDeliveredLogs, rec.NotDeliveredLogs},
		{"deadline_tripped", obs.deadlineTripped, rec.DeadlineTripped},
		{"child_output_capture", obs.childOutputCapture, rec.ChildOutputCapture},
	}

	t.Run("each observable lands in the field that names it", func(t *testing.T) {
		for _, row := range rows {
			if !reflect.DeepEqual(row.want, row.have) {
				t.Errorf("#1674: %s carries %v, want the carrier's %v; this is the row that "+
					"catches a swapped pid pair, an InitPermissionModes fed from StdoutLines "+
					"and a crossed version pair — each of which reads back in the committed "+
					"artifact as a plausible measurement of something else",
					row.name, row.have, row.want)
			}
		}
	})

	t.Run("same-typed rows carry distinct values", func(t *testing.T) {
		for i := range rows {
			// Bools are skipped: all three are true on this carrier, so no pair of
			// them can discriminate here. That is what
			// TestPoolRevokeAssemble_ControlArmLeavesOnlyItsHonestZeros' two rows are
			// for — LaunchYOLO differs from the other two there, and its second row
			// is the only place TakesSettingsUpdate and DeadlineTripped differ.
			if reflect.ValueOf(rows[i].want).Kind() == reflect.Bool {
				continue
			}
			for j := i + 1; j < len(rows); j++ {
				if reflect.TypeOf(rows[i].want) != reflect.TypeOf(rows[j].want) {
					continue
				}
				if reflect.DeepEqual(rows[i].want, rows[j].want) {
					t.Errorf("#1674: %s and %s both carry %v; two same-typed observables that "+
						"can satisfy each other's row make the swap this test exists to "+
						"catch invisible", rows[i].name, rows[j].name, rows[i].want)
				}
			}
		}
	})

	t.Run("the carrier is not mutated", func(t *testing.T) {
		if !reflect.DeepEqual(obs, pristine) {
			t.Errorf("#1674: assembling changed the carrier: %+v, want the untouched %+v. "+
				"#1675 holds one carrier per arm and may read it again after assembling; a "+
				"function that sorts or truncates in place makes the second read a different "+
				"measurement", obs, pristine)
		}
	})
}

// --- free text out of the structured evidence ----------------------------------------

// poolRevokeFreeTextMarker appears ONLY in the free-text capture of the carrier below,
// never in any structured line, so "the free text did not leak into StdoutLines" is a
// containment check with a needle that can actually be absent.
const poolRevokeFreeTextMarker = "free-text-marker-child-stderr"

// TestPoolRevokeAssemble_RoutesFreeTextToChildOutputCaptureAndConvertsStdoutLines is
// #1674's AC 3: free text reaches ChildOutputCapture and never StdoutLines, the
// stdout conversion carries the bytes claude emitted rather than the tap's quoting,
// and the assembler caps nothing.
//
// Failure messages report LENGTHS and at most a short prefix. Printing the capture
// would move up to 8 KiB out of a bounded field and into an unbounded run log, which
// is the entire thing writePoolRevokeFixture's cap exists to prevent.
func TestPoolRevokeAssemble_RoutesFreeTextToChildOutputCaptureAndConvertsStdoutLines(t *testing.T) {
	t.Parallel()

	// Over the cap in every direction, because "the assembler applies no cap of its
	// own" is unfalsifiable against short values.
	capture := poolRevokeFreeTextMarker + strings.Repeat("A", stderrFixtureCap)
	longLog := "sessions: in-band settings command not delivered " + strings.Repeat("N", stderrFixtureCap)
	longResponse := json.RawMessage(`{"type":"control_response","pad":"` +
		strings.Repeat("p", stderrFixtureCap) + `"}`)
	longLine := json.RawMessage(`{"type":"assistant","pad":"` +
		strings.Repeat("s", stderrFixtureCap) + `"}`)

	obs := poolRevokeFullObservation(t)
	obs.childOutputCapture = capture
	obs.notDeliveredLogs = append(obs.notDeliveredLogs, longLog)
	obs.controlResponses = append(obs.controlResponses, longResponse)
	obs.stdoutLines = append(obs.stdoutLines, longLine)

	rec := assemblePoolRevokeFixture(obs)

	t.Run("the free text reaches the capture field whole", func(t *testing.T) {
		if rec.ChildOutputCapture != capture {
			t.Errorf("#1674: ChildOutputCapture reads back at %d bytes, want the carrier's %d "+
				"unaltered; the cap belongs to writePoolRevokeFixture, and an assembler that "+
				"applies a second one bounds the field twice while the writer's stated bound "+
				"is the only one anybody reads", len(rec.ChildOutputCapture), len(capture))
		}
	})

	t.Run("no free-text byte reaches StdoutLines", func(t *testing.T) {
		for i, line := range rec.StdoutLines {
			if strings.Contains(line, poolRevokeFreeTextMarker) {
				t.Errorf("#1674: StdoutLines[%d] (%d bytes) carries the free-text marker; "+
					"StdoutLines is UNCAPPED by #1662's constraint, so a free-text failure "+
					"stream routed here reaches the committed artifact unbounded — and an "+
					"auth failure's message is exactly the unbounded, credential-bearing "+
					"case ChildOutputCapture exists to absorb", i, len(line))
			}
		}
	})

	t.Run("both tap encodings arrive as the bytes claude emitted", func(t *testing.T) {
		want := append(poolRevokeFullStdoutLines(), string(longLine))
		if len(rec.StdoutLines) != len(want) {
			t.Fatalf("#1674: StdoutLines holds %d entries, want %d; the conversion dropped or "+
				"invented a line", len(rec.StdoutLines), len(want))
		}
		// Only the two short rows are compared by value: the third is 8 KiB and is
		// checked by length below.
		for i := range poolRevokeFullStdoutLines() {
			if rec.StdoutLines[i] != want[i] {
				t.Errorf("#1674: StdoutLines[%d] is %q, want %q. A valid-JSON line is retained "+
					"verbatim by the tap and must arrive verbatim; a non-JSON run is retained "+
					"JSON-QUOTED by setModeRecorder's add, and carrying that quoting into the "+
					"committed artifact reports bytes claude never emitted",
					i, rec.StdoutLines[i], want[i])
			}
		}
	})

	// The three fields #1662's constraint declares uncapped, checked by element count
	// and by longest element so a silent truncation of the structured evidence cannot
	// pass as a correct mapping.
	// Each field is compared by element count FIRST and indexed only after that count
	// matched. An assembler that dropped a field would otherwise index an empty slice
	// and panic, and a panic takes the whole test binary down with it — every other
	// row in this package included, so the run reports one crash instead of one
	// finding.
	t.Run("the structured evidence is uncapped", func(t *testing.T) {
		if got, want := len(rec.ControlResponses), len(obs.controlResponses); got != want {
			t.Errorf("#1674: ControlResponses holds %d entries, want %d", got, want)
		} else if got, want := len(rec.ControlResponses[got-1]), len(longResponse); got != want {
			t.Errorf("#1674: the longest control response reads back at %d bytes, want %d; "+
				"capping the probe's structured evidence destroys the artifact #1675 commits",
				got, want)
		}
		if got, want := len(rec.NotDeliveredLogs), len(obs.notDeliveredLogs); got != want {
			t.Errorf("#1674: NotDeliveredLogs holds %d entries, want %d", got, want)
		} else if got, want := len(rec.NotDeliveredLogs[got-1]), len(longLog); got != want {
			t.Errorf("#1674: the longest not-delivered record reads back at %d bytes, want %d; "+
				"it is the daemon's own account of a failed delivery and is uncapped by "+
				"#1662's constraint", got, want)
		}
		if got, want := len(rec.StdoutLines), len(obs.stdoutLines); got != want {
			t.Errorf("#1674: StdoutLines holds %d entries, want %d", got, want)
		} else if got, want := len(rec.StdoutLines[got-1]), len(longLine); got != want {
			t.Errorf("#1674: the longest stdout line reads back at %d bytes, want %d", got, want)
		}
	})
}

// --- the honest zeros ------------------------------------------------------------------

// TestPoolRevokeAssemble_ControlArmLeavesOnlyItsHonestZeros is #1674's AC 4: a
// control arm's record comes back with EXACTLY the zeros that are measurements, and
// every other field filled.
//
// This is what keeps the sweep above from being read as "every field must be non-zero
// in a real run". It is also the sweep's own vacuity control: it is the only place
// poolRevokeZeroFields is asserted to REPORT a zero, so a reflection walk that
// returned nothing at all could not leave AC 2 green.
//
// The second row is not redundant with the first and must not be deleted as such. The
// record's three bools are all true on the full carrier, so that test discriminates no
// pair of them; row one separates LaunchYOLO from the other two, and row two is the
// ONLY place TakesSettingsUpdate and DeadlineTripped differ. Without it an assembler
// that crossed those two sources is green everywhere.
func TestPoolRevokeAssemble_ControlArmLeavesOnlyItsHonestZeros(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		deadlineTripped bool
		wantZero        []string
		why             string
	}{
		{
			name:     "a healthy control arm",
			wantZero: []string{"takes_settings_update", "not_delivered_logs", "deadline_tripped"},
			why: "these three are the complete set of honest zeros for a healthy control: only " +
				"the revoke arm carries takesSettingsUpdate, nothing failed to deliver, and the " +
				"deadline did not trip. Every other field has a value on every arm — including " +
				"the pid pair, which the controls read at the revoke arm's two points precisely " +
				"so they can be compared",
		},
		{
			name:            "a control arm whose deadline tripped",
			deadlineTripped: true,
			wantZero:        []string{"takes_settings_update", "not_delivered_logs"},
			why: "a tripped deadline is a legitimate observation — the turn never closed — and " +
				"it is the only row on which TakesSettingsUpdate and DeadlineTripped differ, so " +
				"it is the only thing that can tell those two sources apart",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			obs := poolRevokeControlObservation(t, tc.deadlineTripped)
			rec := assemblePoolRevokeFixture(obs)

			if got := poolRevokeZeroFields(rec); !reflect.DeepEqual(got, tc.wantZero) {
				t.Errorf("#1674: the control record's zero fields are %q, want exactly %q: %s. "+
					"An extra name means a field nothing filled, which reads back in the "+
					"committed artifact as a measurement; a missing one means a zero the "+
					"assembler invented a value for", got, tc.wantZero, tc.why)
			}
		})
	}
}
