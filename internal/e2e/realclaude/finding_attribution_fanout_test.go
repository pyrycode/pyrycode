//go:build e2e_realclaude

package realclaude

// The reap-log attribution, fanned across EVERY pinned process group and reduced
// to the single admissibility value #1271's run classifier accepts.
//
// This file reaches no verdict about pyry and takes no measurement. It is
// depended on as CODE, not as evidence. Everything here runs offline: no live
// claude, no credentials, no daemon, no subject process, no FIFO, no gather, no
// process-table read, no env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestFinAttribute' -v ./internal/e2e/realclaude/
//
// # Why a fan-out exists at all
//
// trailAdmitAttribution (trailer_admissibility_test.go:448) takes ONE
// tdnReapOutcome, which is the classification of pyry's reap log against ONE
// held group. The probe does not have one group: pinScanArgv returns Matches as
// a SLICE (process_pin_liveness_test.go:130-135), deliberately refusing to
// resolve "the" pid. Reducing that set to one value is new logic, and it is
// where a wrong rule silently costs the probe its only finding — exactly one of
// the fourteen run outcomes is one (trail_run_outcome_test.go:118), and it is
// returned only on Admit.Value == trailAdmitProof. finAttributeOrder states the
// ranking that reduction uses and argues for it.
//
// # Reused, not rebuilt
//
// tdnClassifyReapLog (teardown_liveness_test.go:144) is the reap-log reader;
// pyry's stderr is NOT re-parsed here. trailAdmitAttribution
// (trailer_admissibility_test.go:448) is the attribution predicate, and
// trailIsAdmitValue (:721) its membership predicate — called, never re-switched.
// trailReapLine (:753) renders one anchored line in reap.go:65's slog shape.
// trailClassifyRun / trailRunWellFormed (trail_run_outcome_test.go:518, :884)
// are the downstream consumer and its vary-one-thing base.
//
// trailDetail (trailer_admissibility_test.go:352) is reused rather than given a
// finDetail twin. Its own comment explains it is deliberately not tdnDetail
// because "the trail* family stays out of the tdn* teardown classifier's reach"
// — that argument does not transfer. trailDetail carries no decision (it is
// fmt.Sprintf plus reachCapCommand), and this file is BY DESIGN inside the trail
// family's reach: it embeds trailAdmitResult, calls trailAdmitAttribution and
// calls trailIsAdmitValue. Reusing it keeps the 512-byte cap single-sourced.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// --- the record-level conditions ---------------------------------------------

// The two ways a pinned group produces no attribution. Both are RECORD-LEVEL
// CONDITIONS and neither is ever a selectable value: trailRunReadings.Admit
// accepts only what trailIsAdmitValue accepts (checked by C3,
// trail_run_outcome_test.go:570), so an eighth locally-invented value there
// lands every such run on trailOutcomeOutOfContract.
const (
	// finAttributeGroupUnreportable: a pinned group the reaper can never report.
	// reap.go:52 skips pgid <= 1 before it kills anything, so no reap line can
	// carry one. SURFACED RATHER THAN CLASSIFIED: handing it to
	// tdnClassifyReapLog would trip that function's own :147 guard and return
	// tdnReapInstrumentFailed, which trailAdmitAttribution answers with
	// trailAdmitVoidInstrument — blaming the instrument for a consumer that
	// simply failed to capture a pgid.
	finAttributeGroupUnreportable = "fin-attribute-group-unreportable"
	// finAttributeNoGroups: no reportable distinct group remained — a nil input,
	// or every group hitting the condition above. A STAGING FAULT, and the reason
	// Selected is left zero rather than filled with either of the two falsehoods
	// documented on that field.
	finAttributeNoGroups = "fin-attribute-no-groups"
)

// --- the records ---------------------------------------------------------------

// finAttributeEntry is one distinct group's attribution: the pgid it was decided
// for, and the shipped predicate's result. NOTHING ELSE — no tdnReapOutcome (its
// Line is pyry's own stderr, teardown_liveness_test.go:126), no reachProc (its
// Command is verbatim argv read off the ambient process table,
// background_reach_probe_test.go:162-168), no command string. That is what makes
// the no-captured-bytes property true BY CONSTRUCTION rather than by an ordering
// discipline a later edit can break; trailAdmitResult is itself documented
// trap-free (trailer_admissibility_test.go:258-265), so carrying it whole is
// safe. TestFinAttributeRecordCarriesNoCapturedBytes is the enforcing test.
type finAttributeEntry struct {
	PGID  int              `json:"pgid"`
	Admit trailAdmitResult `json:"admit"`
}

// finAttributeRecord is one fan-out: every distinct group's attribution plus the
// single value selected out of them.
type finAttributeRecord struct {
	// Conditions holds the finAttribute* names that fired. THE FIELD A CONSUMER
	// BRANCHES ON; the two below say which groups they fired for.
	Conditions []string `json:"conditions,omitempty"`
	// Unreportable is the distinct pgids finAttributeGroupUnreportable fired for,
	// ascending. Present in the record and absent from Entries.
	Unreportable []int `json:"unreportable_pgids,omitempty"`
	// Entries is one attribution per distinct REPORTABLE group, ascending by
	// pgid. The count of entries is the count of distinct groups, never the count
	// of pgids passed.
	Entries []finAttributeEntry `json:"entries,omitempty"`
	// Selected is the reduction under finAttributeOrder. THE ZERO
	// trailAdmitResult exactly when Entries is empty, which is exactly when
	// Conditions holds finAttributeNoGroups. A caller must branch on Conditions
	// and must NOT copy a zero Selected into trailRunReadings.Admit: under a
	// certifying gate that reaches trailOutcomeOutOfContract via C3, a caller bug
	// dressed as a reading. Filling it with trailAdmitOutOfContract instead is
	// the other falsehood — it passes C3 and falls to Step 8, publishing a clean
	// negative about a run whose attribution never happened.
	// TestFinAttributeEmptySetAlternativesArePublishedFalsehoods prices both.
	Selected trailAdmitResult `json:"selected"`
	// Detail is the record's one operator-visible sentence. Its content rule is
	// pinned rather than left to judgement, in trailRunOutcome.Detail's shape
	// (trail_run_outcome_test.go:380-390): it MAY name finAttribute* condition
	// names, admissibility values, pgids and the three counts; it MAY NEVER quote
	// the stderr, a tdnReapOutcome.Line, an entry's Admit.Detail, or the
	// certified string. Quoting Selected.Detail is the likeliest slip — it reads
	// as helpful context, and it both duplicates a string the record already
	// carries and spends the 512-byte cap on it.
	Detail string `json:"detail"`
}

// --- the selection order -------------------------------------------------------

// finAttributeOrder ranks the seven admissibility values STRONGEST EVIDENCE
// FIRST. Total over trailIsAdmitValue's space, so a lookup always lands.
//
// A FUNCTION rather than a package-level var, for trailRunWellFormed's stated
// reason (trail_run_outcome_test.go:882-883): a shared backing array is
// reachable from every test in this package, and this slice is read on every
// fan-out call. trailRunOutcomeValues (:1987) is the same shape for the same
// reason.
//
// # The argument for this order
//
// Only the first three can ever CO-OCCUR in one fan-out. For one (stderr,
// certified), trailAdmitOutOfContract (trailer_admissibility_test.go:509) and
// trailAdmitVoidBudgetFired (:521) fire before the verdict is read at all, so
// they are decided by certified alone and are identical for every group.
// trailAdmitVoidNoLine and trailAdmitVoidInstrument are decided by the stderr
// alone — the line count, and whether the pgids= list parsed — and are likewise
// uniform. The heldPGID <= 1 route to tdnReapInstrumentFailed is filtered out by
// the fan-out's step 2, so the ONLY group-dependent split is
// tdnReapHeldPGIDKilled vs tdnReapHeldPGIDAbsent. Ranks 4-7 are here for
// totality, not for a contested case.
//
// PROOF FIRST, and that is the load-bearing half. trailClassifyRun reads Admit.Value
// for a decision in exactly two places, C5 (trail_run_outcome_test.go:592) and Step 2
// (:791), both keyed on trailAdmitProof. A rule that let one group's void suppress
// another group's proof would cost the probe its only finding, while the order BELOW
// proof cannot change the run's outcome at all — it changes only what the published
// record says the reap log showed.
//
// BELOW PROOF, RANK BY PROXIMITY TO PROOF — never under-report the reap log.
// trailAdmitVoidNotOneReapLine says a pinned group WAS named and only line
// multiplicity defeated the ordering argument; trailAdmitVoidGroupUnnamed says
// no pinned group was named. Both are voids and neither can manufacture a
// finding, so the choice is purely about information: reporting GroupUnnamed for
// a stderr in which a pinned group WAS named discards the strongest thing
// observed and reads as though the reap log never mentioned the pinned set.
// Proof is simply the top of this same order, which is why step 5 is one ranked
// scan rather than a special case plus a tie-break.
func finAttributeOrder() []string {
	return []string{
		trailAdmitProof,
		trailAdmitVoidNotOneReapLine,
		trailAdmitVoidGroupUnnamed,
		trailAdmitVoidNoLine,
		trailAdmitVoidInstrument,
		trailAdmitVoidBudgetFired,
		trailAdmitOutOfContract,
	}
}

// --- the fan-out ---------------------------------------------------------------

// finAttributeFanOut computes the reap-log attribution for every pinned process
// group and reduces the set to the one trailAdmitResult trailRunReadings.Admit
// accepts.
//
// Pure over its three arguments: no exec, no clock, no file read, no
// process-table read, no goroutine. That is what lets every arm be driven
// offline with no credentials and no turn, and it is the property #1281's live
// gather depends on.
//
// It takes no *testing.T and returns no error, the same contract as trailGate,
// trailAdmitAttribution, tdnClassifyReapLog, pinReadState and fifoLiveRead:
// every way of not producing an attribution has a NAME in the record, so nothing
// falls through to a default arm.
//
// The groups arrive as PGID INTEGERS and never as reachProc rows, and that is
// the signature closing the credential channel rather than a check inside the
// function. reachProc.Command is verbatim argv read off the ambient process
// table, and a ps column is how an operator's CLAUDE_CODE_OAUTH_TOKEN or
// ANTHROPIC_API_KEY reaches an artifact destined for a public issue. #1281's
// gather holds pinScan.Matches and must convert at the call site; a []reachProc
// signature that recorded only .PGID would pass every test in this file while
// reopening the channel.
//
// Cost: tdnClassifyReapLog walks the whole stderr once per distinct reportable
// group, so the work is O(len(stderr) x distinct groups) and the record is
// O(512 x entries), each string capped by reachCapCommand. No cap is specified
// on the group count: no run has produced a set large enough to matter, and a
// cap would cost another named condition.
func finAttributeFanOut(stderr []byte, pgids []int, certified string) finAttributeRecord {
	var out finAttributeRecord

	// Step 1: distinct, ascending. The set is collected out of the dedupe map and
	// SORTED — a record published in map range order would be a record built off
	// an order Go randomizes, which makes the order-independence assertion below
	// FLAKY rather than deterministically red. The sort is also what makes the
	// whole record a pure function of the SET: the input order is
	// pinScan.Matches' order, which is ps output order, an ambient fact about the
	// process table rather than a fact about the run. It resolves nothing —
	// nothing here collapses the set to "the" pid — it only fixes the order.
	seen := make(map[int]bool, len(pgids))
	distinct := make([]int, 0, len(pgids))
	for _, pgid := range pgids {
		if seen[pgid] {
			continue
		}
		seen[pgid] = true
		distinct = append(distinct, pgid)
	}
	sort.Ints(distinct)

	// Step 2: partition. Mirroring ONLY reap.go:52's first clause is deliberate.
	// That skip is `pgid <= 1 || pgid == self || pgid == rootPid`, but
	// tdnClassifyReapLog's guard (:147) is `heldPGID <= 1` and nothing more, so
	// <= 1 is precisely the set that would trip it. Extending the filter to self
	// / rootPid would need a process-table read this file forbids, and would be a
	// second opinion about groups the shipped classifier is willing to answer for.
	reportable := make([]int, 0, len(distinct))
	for _, pgid := range distinct {
		if pgid <= 1 {
			out.Unreportable = append(out.Unreportable, pgid)
			continue
		}
		reportable = append(reportable, pgid)
	}
	if len(out.Unreportable) > 0 {
		out.Conditions = append(out.Conditions, finAttributeGroupUnreportable)
	}

	// Step 3: the empty set, surfaced as a condition rather than as a reading.
	if len(reportable) == 0 {
		out.Conditions = append(out.Conditions, finAttributeNoGroups)
		out.Detail = trailDetail("%s: %d pgid(s) reduced to %d distinct group(s) and none is "+
			"reportable, so no attribution happened. Selected is left ZERO rather than filled with "+
			"%s, which reaches %s — a clean negative about a run that never pinned a group — or "+
			"left unset under a certifying gate, which reaches %s via C3. Branch on Conditions",
			finAttributeNoGroups, len(pgids), len(distinct), trailAdmitOutOfContract,
			trailOutcomeNoRowMatched, trailOutcomeOutOfContract)
		return out
	}

	// Step 4: classify. Every attribution the record carries is PRODUCED by the
	// two shipped predicates; nothing reaching Entries is built by struct literal,
	// because a hand-built classification is a manufactured reading.
	for _, pgid := range reportable {
		out.Entries = append(out.Entries, finAttributeEntry{
			PGID:  pgid,
			Admit: trailAdmitAttribution(tdnClassifyReapLog(stderr, pgid), certified),
		})
	}

	// Step 5: select the strongest-ranked entry. Strict < keeps the earlier entry
	// on a tie, and step 1 already sorted, so two groups carrying equal values
	// resolve to the lower pgid for free.
	order := finAttributeOrder()
	rank := func(value string) int {
		for i, v := range order {
			if v == value {
				return i
			}
		}
		// Unreachable from trailAdmitAttribution, whose seven values the order
		// ranks in full (TestFinAttributeOrderCoversTheAdmitSpace pins that). A
		// value the order does not name ranks LAST rather than first, so a future
		// eighth value can never win the selection by accident.
		return len(order)
	}
	best := 0
	for i := range out.Entries {
		if rank(out.Entries[i].Admit.Value) < rank(out.Entries[best].Admit.Value) {
			best = i
		}
	}
	out.Selected = out.Entries[best].Admit

	out.Detail = trailDetail("%d pgid(s) reduced to %d distinct group(s), %d of them unreportable; "+
		"the %d attribution(s) recorded selected %s for group %d under finAttributeOrder, which "+
		"ranks strongest evidence first so that no group's void suppresses another group's %s",
		len(pgids), len(distinct), len(out.Unreportable), len(out.Entries), out.Selected.Value,
		out.Entries[best].PGID, trailAdmitProof)
	return out
}

// --- fixtures -------------------------------------------------------------------

// finAttributeCase is one fan-out input and what the record must carry.
type finAttributeCase struct {
	name      string
	stderr    []byte
	pgids     []int
	certified string
	// wantSelected is the value the reduction must select, or "" for the rows
	// whose distinct set holds no reportable group.
	wantSelected   string
	wantEntries    int
	wantConditions []string
	wantUnreported []int
	// wantValues pins individual groups' attributions on the rows whose whole
	// point is that two groups genuinely DIFFER — asserting only the winner would
	// pass over a fan-out that gave both groups the same value. Nil where the
	// selection alone is the claim.
	wantValues map[int]string
}

// finAttributeCases returns every fan-out input under test.
//
// EVERY ROW PASSES A NON-EMPTY certified. trailAdmitAttribution's contract block
// rejects an empty one out of hand (trailer_admissibility_test.go:509), so a row
// passing "" would measure that contract block instead of this selection rule.
//
// A function rather than a package-level var, matching trailGateCases() and
// trailRunCases(): the rows carry slices, and the runner below reverses one of
// them.
func finAttributeCases() []finAttributeCase {
	// One anchored line naming 7788 and no other group — the stderr every
	// group-dependent split is driven from.
	oneLine := []byte(trailReapLine(1, "[7788]") + "\n")
	// Two anchored lines, both naming 7788. A pinned 7788 is KILLED with
	// LineCount == 2 and a pinned 4242 is ABSENT: two groups, one stderr, two
	// genuinely different voids.
	twoLines := []byte(trailReapLine(1, "[7788]") + "\n" + trailReapLine(1, "[7788]") + "\n")
	// No line carrying tdnReapMessage at all, so LineCount == 0 for every group.
	noLine := []byte(`time=2026-08-03T09:00:00.000Z level=INFO msg="agentrun: nothing here"` + "\n")
	// An anchored line whose pgids= value does not open with a bracket, so
	// tdnParsePGIDs errors (teardown_liveness_test.go:242-245) and every group
	// reads instrument-failed.
	unparseable := []byte(trailReapLine(1, "not-a-bracketed-list") + "\n")

	return []finAttributeCase{
		{
			name:         "a group passed twice yields one attribution, so entries count distinct groups",
			stderr:       oneLine,
			pgids:        []int{7788, 4242, 7788, 4242},
			certified:    "completed",
			wantSelected: trailAdmitProof,
			wantEntries:  2,
		},
		{
			name:         "no void suppresses a proof: one line names X and not Y",
			stderr:       oneLine,
			pgids:        []int{7788, 4242},
			certified:    "completed",
			wantSelected: trailAdmitProof,
			wantEntries:  2,
			wantValues:   map[int]string{7788: trailAdmitProof, 4242: trailAdmitVoidGroupUnnamed},
		},
		{
			name:         "no composition of voids manufactures a proof: the line names neither group",
			stderr:       oneLine,
			pgids:        []int{1234, 4242},
			certified:    "completed",
			wantSelected: trailAdmitVoidGroupUnnamed,
			wantEntries:  2,
			wantValues: map[int]string{
				1234: trailAdmitVoidGroupUnnamed,
				4242: trailAdmitVoidGroupUnnamed,
			},
		},
		{
			name:         "two lines naming X but not Y select the value the stated order names",
			stderr:       twoLines,
			pgids:        []int{7788, 4242},
			certified:    "completed",
			wantSelected: trailAdmitVoidNotOneReapLine,
			wantEntries:  2,
			wantValues: map[int]string{
				7788: trailAdmitVoidNotOneReapLine,
				4242: trailAdmitVoidGroupUnnamed,
			},
		},
		{
			name:           "a pgid the reaper can never report is surfaced, not classified",
			stderr:         oneLine,
			pgids:          []int{0, 7788},
			certified:      "completed",
			wantSelected:   trailAdmitProof,
			wantEntries:    1,
			wantConditions: []string{finAttributeGroupUnreportable},
			wantUnreported: []int{0},
		},
		{
			// Named apart from the row above so the guard is not read as an == 0
			// check: reap.go:52 skips everything at or below 1.
			name:           "a negative pgid is unreportable for the same reason zero is",
			stderr:         oneLine,
			pgids:          []int{-5, 7788},
			certified:      "completed",
			wantSelected:   trailAdmitProof,
			wantEntries:    1,
			wantConditions: []string{finAttributeGroupUnreportable},
			wantUnreported: []int{-5},
		},
		{
			name:           "a nil group set is a staging fault and selects nothing",
			stderr:         oneLine,
			pgids:          nil,
			certified:      "completed",
			wantEntries:    0,
			wantConditions: []string{finAttributeNoGroups},
		},
		{
			name:           "a set in which every group is unreportable selects nothing either",
			stderr:         oneLine,
			pgids:          []int{0, 1},
			certified:      "completed",
			wantEntries:    0,
			wantConditions: []string{finAttributeGroupUnreportable, finAttributeNoGroups},
			wantUnreported: []int{0, 1},
		},
		{
			// The three rows below pin ranks 4-6 as REACHABLE rather than
			// decorative. Each value is decided by the stderr or by certified
			// alone, so it is uniform across the groups — which is the fan-out's
			// own argument for why only the first three ranks can co-occur. They
			// need no wantValues: a group reading anything stronger than the
			// uniform value would rank above it and change the selection.
			name:         "no anchored line at all is uniform across groups",
			stderr:       noLine,
			pgids:        []int{7788, 4242},
			certified:    "completed",
			wantSelected: trailAdmitVoidNoLine,
			wantEntries:  2,
		},
		{
			name:         "an unreadable pgids list is uniform across groups",
			stderr:       unparseable,
			pgids:        []int{7788, 4242},
			certified:    "completed",
			wantSelected: trailAdmitVoidInstrument,
			wantEntries:  2,
		},
		{
			name:         "a budget-fired run is uniform across groups whatever the reap log says",
			stderr:       oneLine,
			pgids:        []int{7788, 4242},
			certified:    trailBudgetTerminalReason,
			wantSelected: trailAdmitVoidBudgetFired,
			wantEntries:  2,
		},
	}
}

func finAttributeHasCondition(record finAttributeRecord, name string) bool {
	for _, c := range record.Conditions {
		if c == name {
			return true
		}
	}
	return false
}

// --- tests ----------------------------------------------------------------------

// TestFinAttributeFanOut drives the reduction over every input in
// finAttributeCases.
//
// EVERY ROW RUNS TWICE, once with pgids as given and once reversed, asserting
// the two records are identical. That discharges AC2's "identically under either
// input order" for every row rather than for the three the AC names.
//
// The runner also asserts the record's mutual-exclusion contract and AC4's third
// requirement — that whatever is selected is accepted by trailIsAdmitValue — on
// EVERY row instead of on one, so a locally-invented eighth value cannot reach
// the selected field from any input. That is not a dropped AC; it is the same
// obligation applied more widely.
func TestFinAttributeFanOut(t *testing.T) {
	for _, tc := range finAttributeCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := finAttributeFanOut(tc.stderr, tc.pgids, tc.certified)
			finAttributeAssert(t, tc, got)

			// The reversal builds a FRESH slice. Reversing tc.pgids in place would
			// mutate the row's own fixture and, on a shared value, leak that
			// mutation into every later test in the binary — trailRunWellFormed's
			// stated reason (trail_run_outcome_test.go:882-883) applied to the
			// runner.
			reversed := make([]int, len(tc.pgids))
			for i, pgid := range tc.pgids {
				reversed[len(tc.pgids)-1-i] = pgid
			}
			if back := finAttributeFanOut(tc.stderr, reversed, tc.certified); !reflect.DeepEqual(got, back) {
				t.Errorf("the record is not a function of the SET: %+v under %v, %+v under %v. Input "+
					"order is pinScan.Matches' order, which is ps output order — an ambient fact "+
					"about the process table and not a fact about the run", got, tc.pgids, back,
					reversed)
			}
		})
	}

	// AC3's pin, which no single row can make: dropping an unreportable group
	// from the input must leave the reportable group's attribution BYTE-IDENTICAL.
	// A fan-out that let the skipped group perturb the rest would satisfy every
	// row above and still misreport.
	stderr := []byte(trailReapLine(1, "[7788]") + "\n")
	withUnreportable := finAttributeFanOut(stderr, []int{0, 7788}, "completed")
	alone := finAttributeFanOut(stderr, []int{7788}, "completed")
	if !reflect.DeepEqual(withUnreportable.Entries, alone.Entries) {
		t.Errorf("the reportable group's attribution changed when an unreportable group was "+
			"present: %+v with it, %+v without", withUnreportable.Entries, alone.Entries)
	}
}

// finAttributeAssert checks one row's record, plus the invariants that hold on
// every row.
func finAttributeAssert(t *testing.T, tc finAttributeCase, got finAttributeRecord) {
	t.Helper()

	if got.Selected.Value != tc.wantSelected {
		t.Errorf("selected: got %q (%s), want %q", got.Selected.Value, got.Detail, tc.wantSelected)
	}
	if len(got.Entries) != tc.wantEntries {
		t.Errorf("entries: got %d %+v, want %d — the count is the count of DISTINCT groups, never "+
			"of the %d pgid(s) passed", len(got.Entries), got.Entries, tc.wantEntries, len(tc.pgids))
	}
	if !reflect.DeepEqual(got.Unreportable, tc.wantUnreported) {
		t.Errorf("unreportable: got %v, want %v", got.Unreportable, tc.wantUnreported)
	}
	if !reflect.DeepEqual(got.Conditions, tc.wantConditions) {
		t.Errorf("conditions: got %v, want %v — Conditions is the field a consumer branches on",
			got.Conditions, tc.wantConditions)
	}
	for pgid, want := range tc.wantValues {
		found := false
		for _, entry := range got.Entries {
			if entry.PGID != pgid {
				continue
			}
			found = true
			if entry.Admit.Value != want {
				t.Errorf("group %d attributed %q, want %q — this row's point is what the groups "+
					"individually read, not only which one won", pgid, entry.Admit.Value, want)
			}
		}
		if !found {
			t.Errorf("group %d has no entry in %+v", pgid, got.Entries)
		}
	}

	// The mutual-exclusion contract Selected's doc comment states.
	empty := len(got.Entries) == 0
	if empty != (got.Selected.Value == "") {
		t.Errorf("entries empty is %v but the zero Selected is %v: the two are the SAME condition, "+
			"and a consumer that branches on one holds the other", empty, got.Selected.Value == "")
	}
	if empty != finAttributeHasCondition(got, finAttributeNoGroups) {
		t.Errorf("entries empty is %v but %s is %v in %v", empty, finAttributeNoGroups,
			!empty, got.Conditions)
	}
	if (len(got.Unreportable) > 0) != finAttributeHasCondition(got, finAttributeGroupUnreportable) {
		t.Errorf("unreportable %v disagrees with condition %s in %v", got.Unreportable,
			finAttributeGroupUnreportable, got.Conditions)
	}

	// Nothing is manufactured: the selected value is some entry's own
	// attribution, byte for byte. Together with the fan-out's signature — which
	// accepts no trailAdmitResult at all — this is what makes "no composition of
	// voids produces a proof" structural rather than a property three rows hope
	// for.
	if !empty {
		found := false
		for _, entry := range got.Entries {
			if reflect.DeepEqual(entry.Admit, got.Selected) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("selected %+v is no entry's own attribution: %+v", got.Selected, got.Entries)
		}
	}

	// Only what trailIsAdmitValue accepts may reach the selected field: C3 calls
	// that predicate (trail_run_outcome_test.go:570), so an eighth value there
	// lands every such run on trailOutcomeOutOfContract.
	if !empty && !trailIsAdmitValue(got.Selected.Value) {
		t.Errorf("selected %q is not one of trailIsAdmitValue's seven", got.Selected.Value)
	}
	for _, entry := range got.Entries {
		if !trailIsAdmitValue(entry.Admit.Value) {
			t.Errorf("group %d carries %q, which is not one of trailIsAdmitValue's seven",
				entry.PGID, entry.Admit.Value)
		}
	}

	// Both slices ascend, which is what makes the record a function of the set.
	for i := 1; i < len(got.Entries); i++ {
		if got.Entries[i-1].PGID >= got.Entries[i].PGID {
			t.Errorf("entries are not strictly ascending by pgid: %+v", got.Entries)
		}
	}
	for i := 1; i < len(got.Unreportable); i++ {
		if got.Unreportable[i-1] >= got.Unreportable[i] {
			t.Errorf("unreportable pgids are not strictly ascending: %v", got.Unreportable)
		}
	}
}

// TestFinAttributeOrderCoversTheAdmitSpace is the deterministic guard against an
// eighth admissibility value ranking silently last, in
// TestTrailRunOutcomeValuesAgreeWithThePredicate's shape
// (trail_run_outcome_test.go:2010).
func TestFinAttributeOrderCoversTheAdmitSpace(t *testing.T) {
	order := finAttributeOrder()
	if len(order) != 7 {
		t.Errorf("the order ranks %d value(s), want 7 — that is trailIsAdmitValue's whole space, "+
			"and a value missing from the order ranks LAST by the fan-out's backstop", len(order))
	}
	seen := make(map[string]bool, len(order))
	for _, v := range order {
		if !trailIsAdmitValue(v) {
			t.Errorf("%q is ranked but trailIsAdmitValue rejects it, so it is not a value the "+
				"selected field may carry", v)
		}
		if seen[v] {
			t.Errorf("%q is ranked twice, so the order is not a total order over distinct values", v)
		}
		seen[v] = true
	}
	// Yes, this restates the list. It is the only thing that catches a value
	// added to the predicate and not to the order, and #1271 accepted the same
	// shape for the same reason.
	for _, v := range []string{trailAdmitProof, trailAdmitVoidBudgetFired, trailAdmitVoidInstrument,
		trailAdmitVoidNoLine, trailAdmitVoidGroupUnnamed, trailAdmitVoidNotOneReapLine,
		trailAdmitOutOfContract} {
		if !seen[v] {
			t.Errorf("%q is one of trailIsAdmitValue's seven and the order does not rank it", v)
		}
	}
}

// TestFinAttributeEmptySetAlternativesArePublishedFalsehoods prices the two
// tempting answers the empty distinct-group set invites, by classifying each
// through the shipped trailClassifyRun.
//
// THESE TWO ROWS ARE THE OTHER LAYER. They stage an Admit on a trailRunReadings
// BY LITERAL and hand it to a DIFFERENT function — they are inputs to
// trailClassifyRun, not attributions this fan-out produced, exactly as
// trailRunProofReadings stages one (trail_run_outcome_test.go:896-904). The
// no-hand-built rule binds what the fan-out PRODUCES, and the shipped predicate
// is not a route to either value here anyway: trailAdmitOutOfContract comes out
// of it only on an empty certified, which no fan-out row may pass.
//
// Both rows start from trailRunWellFormed() and vary ONLY Admit. MatchCount
// stays 0 deliberately: at 1 the second row lands on Step 7 and asserts nothing
// about the empty set.
func TestFinAttributeEmptySetAlternativesArePublishedFalsehoods(t *testing.T) {
	t.Run("an unset attribution is a caller bug dressed as a reading", func(t *testing.T) {
		in := trailRunWellFormed()
		in.Admit = trailAdmitResult{}

		got := trailClassifyRun(in)
		if got.Value != trailOutcomeOutOfContract {
			t.Fatalf("value: got %q (%s), want %q via C3 — leaving Selected unset under a certifying "+
				"gate publishes the caller's own bug", got.Value, got.Detail,
				trailOutcomeOutOfContract)
		}
		if !strings.Contains(got.Detail, "no run condition under which a certifying gate arrives") {
			t.Errorf("detail: got %q, want C3's own sentence — the arm reached matters as much as "+
				"the value, because three other contract checks also answer %s", got.Detail,
				trailOutcomeOutOfContract)
		}
	})

	t.Run("an out-of-contract attribution publishes a clean negative", func(t *testing.T) {
		in := trailRunWellFormed()
		in.Admit = trailAdmitResult{Value: trailAdmitOutOfContract,
			Detail: "what a fan-out would have to invent for a set it never attributed"}

		got := trailClassifyRun(in)
		if got.Value != trailOutcomeNoRowMatched {
			t.Fatalf("value: got %q (%s), want %q — %s passes C3 and C5 does not fire on it, so the "+
				"decision falls to Step 8 and answers ABOUT THE SCAN for a run whose attribution "+
				"never happened", got.Value, got.Detail, trailOutcomeNoRowMatched,
				trailAdmitOutOfContract)
		}
	})
}

// TestFinAttributeRecordCarriesNoCapturedBytes makes the
// operator-review-before-paste obligation checkable rather than advisory, in
// TestTrailAdmissibilityRecordsCarryNoCapturedBytes's shape
// (trailer_admissibility_test.go:1264) and reusing the shipped trailNeedle.
//
// # The plant position is the whole test
//
// The needle sits ON AN ANCHORED LINE, after the pgids= list. trailReapLine
// splices its pgids argument raw, and tdnParsePGIDs stops at the first ] and
// checks nothing after it on an unquoted value (teardown_liveness_test.go:246-254),
// so the list still parses. A NEEDLE ON A NON-ANCHORED LINE WOULD MAKE THIS TEST
// VACUOUS: tdnClassifyReapLog skips every line not carrying tdnReapMessage
// (:161-163) BEFORE it fills any field, so such a needle enters no field at all
// and the test goes green over a record that recorded the whole outcome. The
// anchored line is what Line captures (:165-167), which is the channel this
// record is exposed to.
//
// # The needle goes into the stderr ONLY
//
// #1271's own test plants it in every string-bearing input, and following that
// here would put it into certified — which CROSSES VERBATIM BY DESIGN:
// trailAdmitAttribution splices certified with %q into two of its Details
// (trailer_admissibility_test.go:524, :587). It is trailGate's certified Reason,
// i.e. the trailer's terminal_reason, which shipped code already treats as publishable
// (trailClassifyRun puts it into its own Details at trail_run_outcome_test.go:700
// and :803, and TestTrailRunOutcomeCarriesNoCapturedBytes deliberately leaves Reason
// alone). Planting it there would go red, and the only fix would be to stop carrying
// trailAdmitResult whole — which the record's shape requires. The
// exposure is pre-existing and unchanged in content, but this fan-out
// MULTIPLIES it by the distinct-group count; each copy is capped at 512 bytes by
// reachCapCommand, so the growth is bounded per entry and unbounded only in the
// entry count.
func TestFinAttributeRecordCarriesNoCapturedBytes(t *testing.T) {
	stderr := []byte(trailReapLine(1, "[7788] "+trailNeedle) + "\n")
	got := finAttributeFanOut(stderr, []int{7788}, "completed")

	// The premise first, and it doubles as the non-vacuity proof: this value is
	// reachable only if the needle-bearing line was recognised as anchored,
	// parsed, and found to name 7788. A plant that stopped being anchored turns
	// this into a Fatalf rather than a silent pass.
	if len(got.Entries) != 1 || got.Entries[0].Admit.Value != trailAdmitProof {
		t.Fatalf("entries: got %+v (%s), want one entry reading %s — the premise is that the "+
			"needle rides an ANCHORED line the classifier read in full", got.Entries, got.Detail,
			trailAdmitProof)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the attribution record: %v", err)
	}
	if bytes.Contains(encoded, []byte(trailNeedle)) {
		t.Errorf("the marshalled record carries pyry's captured stderr: %s", encoded)
	}

	// The structural half, following trail_run_outcome_test.go:1885-1907. line
	// and stderr join #1271's list because the channel THIS record is exposed to
	// is tdnReapOutcome.Line, not a ps column.
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keyed); err != nil {
		t.Fatalf("decoding the marshalled record: %v", err)
	}
	for _, forbidden := range []string{"command", "args", "comm", "argv", "line", "stderr"} {
		for key := range keyed {
			if strings.Contains(key, forbidden) {
				t.Errorf("the record carries key %q, which is %q-shaped: this record's whole value "+
					"is that it can be published unreviewed, and such a field would inherit the "+
					"operator-review-before-paste obligation onto it", key, forbidden)
			}
		}
	}
}
