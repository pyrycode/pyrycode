//go:build e2e_realclaude

package realclaude

// #1764 — the cross-arm read of the `initialize` control-request measurement:
// the two arms that send the request, compared against the arm that sends
// nothing, at the same turn index, over the fixtures #1763 committed.
//
// # Why a control arm exists at all
//
// The subtype is named `initialize`, which is exactly the kind of name that might
// reset something. It does not: every arm re-emits `system`/`init` once per turn,
// the control included, and every trailer reads `num_turns: 1`. A re-emitted init
// is therefore the baseline SHAPE rather than a perturbation signal, and that is
// the whole reason a delta only means something measured against an arm that sent
// no request at all. Computed here rather than inherited — the orientation above
// is what the numbers said on 7813fced's bytes, and this file's output is the
// finding.
//
// # What the fixtures actually discriminate
//
// Every field probeOutcome carries is CONSTANT across all three arms at both turn
// indices — the drive sequence is tool-free — so porting that field set literally
// would give a comparison no mutation of this file could redden.
// initControlTurnRead's doc states the divergence and what ControlResponses buys.
//
// # The verdict is reported; the instrument is asserted
//
// A test whose only output is a t.Logf cannot fail, so what is asserted here is
// that the reported agreement is worth reading. Three ways this file could
// otherwise compute agreement out of absence — an out-of-range read zero-filling
// into equality, an arm dropping out silently, and a set of arms whose turns never
// reached the model agreeing with each other by construction — and each is closed
// at the assertion that names it below.
//
// # Offline, and it reads the committed fixtures on purpose
//
// This file reaches no live claude, no daemon, no subprocess, no credential and no
// environment. It is the first offline file in this family with a legitimate
// reason to READ testdata/, so its finOfflineExecBans entry drops the three names
// that keep its siblings away from the real directory and keeps every write name
// banned — the relative-path hazard those entries close is in the write direction,
// and this file reads and must never write. A locally regenerated capture that
// went the way 400db2d1's did makes this test red; that is the liveness clause
// earning its keep, not a reason to prefer the committed bytes over what is on
// disk.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestInitControlArms_CompareMeasurementArmsAgainstTheControl|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// Both must report PASS — not SKIP, not "no tests to run" — on a machine with no
// claude and no credentials. Read the count of tests that executed, never the exit
// code: this package is behind the e2e_realclaude tag, `make check` never compiles
// it, and the suite exits 0 both on a build failure and on a full credentials skip.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// --- discovery ------------------------------------------------------------------

// initControlArmFixtureGlob sweeps the ARM-CARRYING members of the
// initialize_control_v family, and it is the fourth family glob in this package —
// the first this family owns. The testdata/ prefix is included, matching
// fixtureGlob and dropcapFixtureGlob and unlike setModeFamilyGlob which names base
// names: `go test` runs in the package source directory, so a relative glob
// resolves under it with no packageDir call.
//
// THE `_` IS THE EXCLUSION, AND IT IS STRUCTURAL RATHER THAN A FILTER.
// filepath.Match needs a literal `_` after the version segment, and #1688's
// one-arm testdata/initialize_control_v2.1.239.json has none — it carries no arm,
// no send_point_index and none of the after_send_point_* keys, so it would decode
// to a row of zeros under a truthful-looking name. It shares the 2.1.239 token
// with the three arms, so the version grouping below does not separate it; this
// does. Nothing mints an unarmed name any more, so that one legacy file is the
// whole exclusion.
//
// A GLOB RATHER THAN initControlArmFixtureName BY EXACT NAME, deliberately.
// Addressing by name needs a version token this file cannot learn from a live
// claude — captureClaudeVersion is banned for it — so it would hard-code one, and
// a re-capture at a new version would demand a code edit. Worse, a file addressed
// by exact name for a declared arm can never HAVE an undeclared one, which makes
// the undeclared-arm check structurally unreachable. The glob discovers the
// version and gives that check something to reject.
//
// This constant must NOT be added to the family-glob tables in
// TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained or
// TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained.
// Those assert that no minted name matches a FOREIGN family's glob; every arm name
// matches this one by design, and the one-arm namer's `2_1_220` row mints a name
// that matches it too. Both would go red against correct code.
const initControlArmFixtureGlob = "testdata/initialize_control_v*_*.json"

// initControlControlArmID returns the id of the one arm that sends no request.
//
// It comes FROM THE TABLE, never from a literal: a second spelling of
// "control_no_request" in this file is exactly the duplication initControlArms'
// own doc forbids, and it is what would let this file compare against an arm the
// table no longer calls the control.
func initControlControlArmID(t *testing.T) string {
	t.Helper()

	var ids []string
	for _, arm := range initControlArms {
		if !arm.sendsRequest {
			ids = append(ids, arm.id)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("#1764: initControlArms declares %d arm(s) with sendsRequest false %v, want exactly "+
			"one; with none this comparison has no control and every delta below is measured "+
			"against nothing, and with two it has no single baseline", len(ids), ids)
	}
	return ids[0]
}

// initControlDiscoverArms reads the arm-carrying fixtures, binds each one's name
// to its own record, and returns the single claude version they agree on together
// with its arm→record map.
//
// It accumulates every problem it can and ends with ONE t.Fatalf listing all of
// them — assertRegressionFixture's idiom, so one run names every broken fixture
// rather than the first.
//
// WHICH VERSION TO COMPARE WHEN SEVERAL ARE PRESENT: none. The run fails. The
// alternative — compare the newest — was declined because these tokens have no
// total order without a semver parser, and a wrong order silently compares the
// stale set, which is the one outcome worse than a red. The writer derives the
// filename from the version, so a re-capture at the same version overwrites in
// place and never reaches that branch; one at a new version leaves a stale set
// whose deletion is the fix, and the message says so.
func initControlDiscoverArms(t *testing.T) (string, map[string]*initControlFixtureRecord) {
	t.Helper()

	matches, err := filepath.Glob(initControlArmFixtureGlob)
	if err != nil {
		t.Fatalf("#1764: glob %s: %v; a malformed pattern constant makes every claim in this "+
			"file meaningless", initControlArmFixtureGlob, err)
	}
	if len(matches) == 0 {
		t.Fatalf("#1764: no fixtures matched %s; a deleted fixture set must be loud rather than "+
			"reported as three arms that agree", initControlArmFixtureGlob)
	}

	declared := make(map[string]bool, len(initControlArms))
	for _, arm := range initControlArms {
		declared[arm.id] = true
	}

	var problems []string
	byVersion := map[string]map[string]*initControlFixtureRecord{}
	for _, path := range matches {
		base := filepath.Base(path)

		raw, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: read: %v", base, err))
			continue
		}

		// Through initControlFixtureRecord and never through a second struct: a
		// parallel shape is how a field rename lands as a silent zero value, and
		// everything this comparison needs is already on that record.
		rec := &initControlFixtureRecord{}
		if err := json.Unmarshal(raw, rec); err != nil {
			problems = append(problems, fmt.Sprintf("%s: decode through initControlFixtureRecord: %v", base, err))
			continue
		}

		// AC 4's second half. An empty arm is #1688's one-arm shape, which the glob
		// already excludes; an arm the table never declared is a name a later writer
		// minted, and either way the run fails rather than growing a fourth row of
		// zeros under a truthful-looking name.
		if !declared[rec.Arm] {
			problems = append(problems, fmt.Sprintf("%s: arm %q is not one of initControlArms' "+
				"identifiers — an empty or undeclared arm decodes to a row of zeros and must fail "+
				"the run rather than become a fourth row", base, rec.Arm))
			continue
		}

		// Binds the name to the CONTENT in one comparison, so the version grouping
		// below cannot be computed off a name whose record says something else.
		// String equality only: no value read out of a fixture is ever joined into a
		// path here.
		if want := initControlArmFixtureName(rec.ClaudeVersion, rec.Arm); base != want {
			problems = append(problems, fmt.Sprintf("%s: its record says claude_version %q arm %q, "+
				"which initControlArmFixtureName mints as %q; the file's name and its content "+
				"disagree, so neither can be trusted to say which version this row belongs to",
				base, rec.ClaudeVersion, rec.Arm, want))
			continue
		}

		byArm := byVersion[rec.ClaudeVersion]
		if byArm == nil {
			byArm = make(map[string]*initControlFixtureRecord, len(initControlArms))
			byVersion[rec.ClaudeVersion] = byArm
		}
		byArm[rec.Arm] = rec
	}

	versions := make([]string, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Strings(versions)

	if len(versions) > 1 {
		problems = append(problems, fmt.Sprintf("the glob matched captures of %d claude versions %v; "+
			"comparing an arm from one version against a control from another is not a comparison, "+
			"and picking one needs an order these tokens do not have — delete the stale set, or "+
			"teach this test which to compare and say why", len(versions), versions))
	}

	// "Exactly once" needs no second branch: every key here is a declared arm, and
	// two files with the same (version, arm) would have to share the base name the
	// check above pins them to, which one directory listing cannot produce. Presence
	// is therefore the whole property.
	if len(problems) == 0 {
		for _, arm := range initControlArms {
			if _, ok := byVersion[versions[0]][arm.id]; !ok {
				problems = append(problems, fmt.Sprintf("claude %s has no capture of arm %q; an arm "+
					"the run cannot compare must be named rather than dropped out of the comparison "+
					"silently", versions[0], arm.id))
			}
		}
	}

	if len(problems) > 0 {
		t.Fatalf("#1764: discovery over %s found %d problem(s):\n  %s",
			initControlArmFixtureGlob, len(problems), strings.Join(problems, "\n  "))
	}
	return versions[0], byVersion[versions[0]]
}

// --- the per-turn read ------------------------------------------------------------

// initControlTurnRead is the behavioural read of ONE turn of one arm. EVERY FIELD
// IS COMPARED, which is probeOutcome's discipline applied to a different field set
// — see this file's header for why porting that set literally would leave a
// comparison no mutation could redden.
//
// ControlResponses is the one field that differs between arms at a matching index.
// It is fixed by each arm's own send point, so it survives re-capture, and it is
// what makes a wrong-index, wrong-arm or wrong-window slice observable.
//
// COST AND thinking_tokens ARE DELIBERATELY NOT FIELDS HERE. Cost differs at the
// fourth decimal between arms for reasons that are not perturbation, so a compared
// cost field would report "differs" on every row and turn the verdict into noise;
// it is reported by the side-by-side window row instead, where it is labelled
// rather than subtracted. thinking_tokens counts differ run to run as model
// variance, so comparing on one flips the verdict between captures. Do not
// "complete" this struct with either.
type initControlTurnRead struct {
	ControlResponses     int    // `control_response` lines inside THIS turn's window
	SystemInitCount      int    // `system` lines whose subtype is `init`, keyed on BOTH
	ResultObserved       bool   // false ⇒ the window never closed on a `result`
	ResultSubtype        string // reported, never the liveness key — see the instrument below
	ResultIsError        bool
	ResultTerminalReason string
	ResultNumTurns       int
}

// initControlReadTurn reduces one turn window to its behavioural read. closed
// reports whether the window ended on a `result` line.
//
// ITS INPUT IS UNTRUSTED CHILD OUTPUT committed to disk. It is total over it,
// exactly as initControlReadWindow is: a line that is not a JSON object is skipped
// and the read continues, and a field that will not decode leaves its destination
// at the zero value with the line still landing. Every field crosses through
// initControlWindowField, which absorbs a decode failure at the FIELD — never at
// the line, never at the window. Nothing here returns an error and nothing here
// may grow one.
func initControlReadTurn(window []json.RawMessage, closed bool) initControlTurnRead {
	out := initControlTurnRead{ResultObserved: closed}
	for _, raw := range window {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			continue
		}
		var typ string
		initControlWindowField(obj, "type", &typ)
		switch typ {
		case "system":
			// The subtype half is load-bearing, for initControlReadWindow's stated
			// reason: these captures carry `thinking_tokens`, `post_turn_summary` and
			// `background_tasks_changed` lines, and a count keyed on `type` alone
			// reads seven or more where one is right.
			var subtype string
			initControlWindowField(obj, "subtype", &subtype)
			if subtype == "init" {
				out.SystemInitCount++
			}
		case "control_response":
			out.ControlResponses++
		case "result":
			initControlWindowField(obj, "subtype", &out.ResultSubtype)
			initControlWindowField(obj, "is_error", &out.ResultIsError)
			initControlWindowField(obj, "terminal_reason", &out.ResultTerminalReason)
			initControlWindowField(obj, "num_turns", &out.ResultNumTurns)
		}
	}
	return out
}

// initControlTurnReads slices the recorded lines at the recorded `result` indices
// and reduces each window, mirroring setModeTurnWindows including its trailing
// unclosed window, which is emitted with ResultObserved false — a turn that never
// closed is a legitimate outcome, not an omission, and the instrument below is
// what refuses to compare it.
//
// THE RANGE GUARD IS LOAD-BEARING, NOT STYLISTIC. turn_boundaries is a number read
// off disk; an out-of-range or non-monotonic entry without it is a slice-out-of-
// range panic that takes this package's whole test binary down.
func initControlTurnReads(lines []json.RawMessage, boundaries []int) []initControlTurnRead {
	var out []initControlTurnRead
	start := 0
	for _, b := range boundaries {
		if b < start || b >= len(lines) {
			continue
		}
		out = append(out, initControlReadTurn(lines[start:b+1], true))
		start = b + 1
	}
	if start < len(lines) {
		out = append(out, initControlReadTurn(lines[start:], false))
	}
	return out
}

// initControlTurnReadAt returns the i-th turn read and REPORTS WHETHER IT EXISTS.
//
// This is the one place this file deliberately diverges from setModeOutcomeAt,
// which returns the zero value for an out-of-range index. One zero value compares
// equal to another, so two arms that both ran short report as "the arms do not
// differ" — agreement computed out of absence. The bool is what closes it, and the
// instrument below is what consumes it.
func initControlTurnReadAt(reads []initControlTurnRead, i int) (initControlTurnRead, bool) {
	if i >= 0 && i < len(reads) {
		return reads[i], true
	}
	return initControlTurnRead{}, false
}

// initControlTurnRows reports one row per field of initControlTurnRead, and names
// the fields that differ.
//
// "The arms do not differ" is an OUTCOME here, never a missing result: every row
// reads either `agrees` or `differs`, and the caller's verdict line says which of
// the two it was. setModeFieldMatches' three-way "both controls agree — not a
// discriminator" lean is deliberately not ported: that construct exists because
// that probe has two controls, and this one has a single control against which
// every field either agrees or differs.
func initControlTurnRows(got, control initControlTurnRead) (rows []string, differing []string) {
	cells := []struct{ name, gotV, controlV string }{
		{"control_responses", fmt.Sprint(got.ControlResponses), fmt.Sprint(control.ControlResponses)},
		{"system_init_count", fmt.Sprint(got.SystemInitCount), fmt.Sprint(control.SystemInitCount)},
		{"result_observed", fmt.Sprint(got.ResultObserved), fmt.Sprint(control.ResultObserved)},
		{"result_subtype", got.ResultSubtype, control.ResultSubtype},
		{"result_is_error", fmt.Sprint(got.ResultIsError), fmt.Sprint(control.ResultIsError)},
		{"result_terminal_reason", got.ResultTerminalReason, control.ResultTerminalReason},
		{"result_num_turns", fmt.Sprint(got.ResultNumTurns), fmt.Sprint(control.ResultNumTurns)},
	}
	rows = make([]string, 0, len(cells))
	for _, c := range cells {
		verdict := "agrees"
		if c.gotV != c.controlV {
			verdict = "differs"
			differing = append(differing, c.name)
		}
		rows = append(rows, fmt.Sprintf("%s=%s vs control=%s → %s", c.name, c.gotV, c.controlV, verdict))
	}
	return rows, differing
}

// initControlSendPointTurn returns the turn index an arm's send point falls in:
// the count of recorded boundaries strictly below it. Pure counting — it slices
// nothing, so it carries no bounds hazard and stays total over a send point past
// the last boundary, which yields an index past the last turn.
func initControlSendPointTurn(boundaries []int, sendPoint int) int {
	n := 0
	for _, b := range boundaries {
		if b < sendPoint {
			n++
		}
	}
	return n
}

// --- the comparison ---------------------------------------------------------------

// TestInitControlArms_CompareMeasurementArmsAgainstTheControlAtTheSameTurnIndex is
// #1764 whole: it reports each measurement arm against the no-request control at
// the same turn index, field by field, and reports the three arms' send-point
// window reads side by side.
//
// THE VERDICT IS REPORTED, NEVER ASSERTED. What is asserted is this file's own
// instrument — see the header. It settles with no claude binary and no
// credentials, reaches no child and no environment, and reports PASS rather than
// SKIP on a machine that has neither.
func TestInitControlArms_CompareMeasurementArmsAgainstTheControlAtTheSameTurnIndex(t *testing.T) {
	t.Parallel()

	controlArm := initControlControlArmID(t)
	version, byArm := initControlDiscoverArms(t)

	reads := make(map[string][]initControlTurnRead, len(byArm))
	compared := 0
	for _, arm := range initControlArms {
		rec := byArm[arm.id]
		r := initControlTurnReads(rec.StdoutEvents, rec.TurnBoundaries)
		reads[arm.id] = r
		if len(r) > compared {
			compared = len(r)
		}
	}
	if compared < 1 {
		t.Fatalf("#1764: claude %s: no arm produced a single turn read from its recorded "+
			"turn_boundaries, so there is no index to compare at; that is an unreadable capture "+
			"set and must be named rather than reported as three arms in agreement", version)
	}
	t.Logf("#1764: claude %s — %d arms, %d turn index(es) compared, control arm %q",
		version, len(initControlArms), compared, controlArm)

	// AC 3: the send-point window reads, side by side, in the table's declared
	// order. Read off the record, never recomputed — these are the numbers the
	// capturing run committed.
	controlSendTurn := initControlSendPointTurn(byArm[controlArm].TurnBoundaries, byArm[controlArm].SendPointIndex)
	for _, arm := range initControlArms {
		rec := byArm[arm.id]
		sendTurn := initControlSendPointTurn(rec.TurnBoundaries, rec.SendPointIndex)

		trailers := make([]string, 0, len(rec.AfterSendPointResultTrailers))
		for _, tr := range rec.AfterSendPointResultTrailers {
			cost := "absent"
			if tr.TotalCostUSDPresent {
				cost = fmt.Sprintf("%g", tr.TotalCostUSD)
			}
			trailers = append(trailers, fmt.Sprintf("num_turns=%d cost=%s", tr.NumTurns, cost))
		}

		// An arm whose window opens in a different turn from the control's spans a
		// different stretch of the session, so its row says so rather than presenting
		// the span difference as a delta. before_first_turn opens at index 0, so its
		// window is the whole session where the other two open after a completed first
		// turn — structural, and it survives any re-capture.
		span := fmt.Sprintf("opens in the same turn as the control's (turn %d), so these numbers read as a delta against the control's row", controlSendTurn)
		switch {
		case arm.id == controlArm:
			span = "the control — every other row is read against this one"
		case sendTurn != controlSendTurn:
			span = fmt.Sprintf("opens in turn %d where the control's opens in turn %d, so it spans a "+
				"different stretch of the session and these numbers are NOT a delta", sendTurn, controlSendTurn)
		}
		t.Logf("#1764: window %s — send_point_index=%d (turn %d) system_init=%d result_trailers=%d [%s] — %s",
			arm.id, rec.SendPointIndex, sendTurn, rec.AfterSendPointSystemInitCount,
			len(rec.AfterSendPointResultTrailers), strings.Join(trailers, "; "), span)
	}

	// AC 1 and AC 2: each measurement arm against the control at the same index.
	for i := 0; i < compared; i++ {
		control, controlOK := initControlTurnReadAt(reads[controlArm], i)

		for _, arm := range initControlArms {
			got, ok := initControlTurnReadAt(reads[arm.id], i)
			if !ok {
				t.Errorf("#1764: arm %q produced %d turn read(s) and has none at index %d, which "+
					"another arm reached; an arm that ran short is named here rather than zero-filled "+
					"into agreement with every other short arm", arm.id, len(reads[arm.id]), i)
				continue
			}

			// The liveness clause, keyed on is_error and terminal_reason and NEVER on
			// subtype. 400db2d1's 401 capture recorded subtype "success" with num_turns
			// 1 on every turn that never reached the model, so a subtype-keyed assertion
			// is vacuous against the exact capture this clause exists to reject — and a
			// set of arms whose turns all died before the model agrees with itself by
			// construction. The subtype is reported inside the message instead.
			if !got.ResultObserved || got.ResultIsError || got.ResultTerminalReason != "completed" {
				t.Errorf("#1764: arm %q turn %d did not complete: result_observed=%v is_error=%v "+
					"terminal_reason=%q (subtype %q, reported not asserted — a 401 capture reads "+
					"subtype \"success\" on a turn that never reached the model); arms whose turns "+
					"died before the model agree with each other by construction, so no verdict is "+
					"reported for this index", arm.id, i, got.ResultObserved, got.ResultIsError,
					got.ResultTerminalReason, got.ResultSubtype)
				continue
			}

			if arm.id == controlArm || !controlOK {
				continue
			}

			rows, differing := initControlTurnRows(got, control)
			// Row totality, mechanically: a field added to initControlTurnRead and
			// forgotten in the row builder would otherwise go silently uncompared while
			// every verdict below still read "agrees".
			if want := reflect.TypeOf(initControlTurnRead{}).NumField(); len(rows) != want {
				t.Errorf("#1764: the comparison reports %d row(s) for %d field(s) of "+
					"initControlTurnRead; a field with no row is compared by nothing and its "+
					"disagreement is reported as agreement", len(rows), want)
			}

			verdict := fmt.Sprintf("agrees with %s on every field", controlArm)
			if len(differing) > 0 {
				verdict = fmt.Sprintf("differs from %s on %v", controlArm, differing)
			}
			t.Logf("#1764: %s turn %d %s\n    %s", arm.id, i, verdict, strings.Join(rows, "\n    "))
		}
	}
}
