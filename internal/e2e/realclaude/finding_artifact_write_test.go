//go:build e2e_realclaude

package realclaude

// Rendering one probe run's record into a pasteable artifact, and proving the
// DIRECTORY it lands in leaks no captured bytes.
//
// This file reaches no verdict about pyry and takes no measurement. It is the
// last stage of the chain #1290 and #1291 built: the record is already clean,
// and what is new here is that the ACT OF PUBLISHING it is checked rather than
// careful. Everything here runs offline: no live claude, no credentials, no
// network, no daemon, no subject process, no process-table read, no env gate, no
// t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestFinWrite' -v ./internal/e2e/realclaude/
//
// # The gap this closes is a DIRECTORY, not a record
//
// writeReachArtifacts (background_reach_probe_test.go:823) marshals a clean
// record into reach.json and then writes rec.rawPS — the verbatim process table,
// full argv, every operator command line — into reach.ps.txt beside it. A clean
// JSON blob next to a raw-bytes sidecar publishes the raw bytes. THE RECORD'S
// SAFETY PROPERTY DOES NOT TRANSFER TO THE DIRECTORY, which is why the sweep
// below reads every file os.ReadDir returns rather than the two names it wrote.
//
// finWriteArtifacts' SIGNATURE is the design. It takes the built finRecordRun and
// nothing else — finRecordProc's doctrine (finding_run_record_test.go:92-112) one
// layer out: prefer the shape that cannot be got wrong over the discipline that
// must not be. writeReachArtifacts takes *reachRecord, whose unexported rawPS
// field is what produces the sidecar; finRecordRun has no unexported field and no
// field able to hold captured bytes, so there is nothing raw in this writer's
// reach to write out.
//
// # Nothing on this path may exec, and the check for that has to name symbols
//
// A grep for the os/exec package selector is NOT sufficient on its own: every
// route off this offline path runs through a SHIPPED HELPER that execs internally
// rather than through a visible call to that package. The version probe
// (background_trigger_probe_test.go:606) execs the binary, which is why the
// version here is a caller-supplied string; the claude-binary resolver
// (resilience_test.go:282) skips when claude is absent, and a skip that exits 0
// reads as a pass under `make e2e-realclaude`; the worktree credentials gate
// (fixtures.go:96) is the "no credentials" rule; the per-pid state read
// (process_pin_liveness_test.go:275) execs `ps` at :293, which is why the
// liveness fixture below is hand-built; the exit-1 borrow (:1088) execs `false`
// to obtain an *os.ProcessState Go cannot synthesize; and the argv scan (:191),
// the process snapshot (background_trigger_probe_test.go:867), the teardown scan
// (teardown_liveness_probe_test.go:482) and the FIFO hold
// (background_trigger_probe_test.go:663) each reach a process or the table.
//
// EVERY ONE OF THEM IS REFERENCED ABOVE BY FILE AND LINE RATHER THAN BY NAME, so
// that the forbidden-symbol grep reports on this file's CODE and cannot be
// defeated by this file's own prose. A check that cannot report clean is as
// useless as one that cannot fail — #1290's spec wrote a bare `t.Skip` grep that
// matched that file's own header sentence and so could never come back empty.
//
// Writing files under t.TempDir() is expected and is not an exec: os.ReadDir,
// os.ReadFile and os.WriteFile are pure filesystem calls on a directory the test
// itself created.
//
// # The two directions of failure are deliberately asymmetric
//
// THE WRITER NEVER FAILS A TEST. It is the instrument under measurement, and an
// instrument failure is a datum: a marshal error or a lost file is t.Errorf and
// the remaining write still happens, mirroring writeReachArtifacts:827-853 and
// the family's pure-builder contract (finding_run_record_test.go:320-325).
//
// THE SWEEP'S OWN READS FAIL LOUDLY. finWriteReadDir and every decode below are
// t.Fatalf, because a sweep that silently skipped a file it could not read would
// report clean on the one file that leaked.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// --- the artifact ------------------------------------------------------------------

// finWriteSafetyClaim is the standing sentence the note carries. A CONSTANT
// rather than two literals, so the writer and the test that asserts the artifact
// states it cannot drift apart.
//
// It is the claim #1290 made available and this ticket publishes: the record
// retains no trailer line in any form, capped or otherwise — finTrailerRecord
// (finding_trailer_evidence_test.go:142) is ten scalars with no Line, and
// finRecordInputs carries neither a trailObservation nor a trailScanResult
// (finding_run_record_test.go:214-221) — so there is no field left to mark for
// review.
const finWriteSafetyClaim = "This record carries no verbatim model output and no verbatim argv: " +
	"every field it holds is a count, an integer, an enumerated verdict, a decoded trailer scalar, " +
	"or a string this rig authored."

const (
	finWriteRecordFile = "run.json"
	finWriteNoteFile   = "run.md"
	// finWriteDetailKey is the json name every Detail in the family renders
	// under, named once so the headroom walk keys on the same string the tags do.
	finWriteDetailKey = "detail"
)

// finWriteArtifacts renders one built record into dir as a pasteable artifact:
// the marshalled record, and a note holding the standing safety claim, the same
// bytes in a fenced block, and one summary line formatted from DERIVED SCALARS of
// rec alone.
//
// # The note does not carry the caveat it is the absence of
//
// It must not contain "operator-review-before-paste" or "OPERATOR-REVIEW"
// (result_trailer_observation_test.go:106 is where that obligation lives, on the
// field this record does not carry). A note explaining the absent caveat in the
// caveat's own words would train a reader to go looking for a field that is not
// there. The explanation belongs HERE and in the test — neither of which is
// written into the artifact. Scoping the rule by LAYER rather than "anywhere in
// the file" is #1280's lesson.
//
// # Errors are loud and non-fatal
//
// Following writeReachArtifacts:827-853: a marshal failure returns, a write
// failure names the path and CONTINUES to the next file. A lost artifact is loud
// but does not abort the remaining writes, and 0o600 matches the shipped writer's
// mode — the proof that the content is clean is what the tests assert, not
// something the mode may assume.
func finWriteArtifacts(t *testing.T, dir string, rec finRecordRun) {
	t.Helper()

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("marshal run record: %v", err)
		return
	}
	blob = append(blob, '\n')

	// Counts, an enumerated agreement verdict, an admissibility value and a
	// carried outcome — the same content rule finRecordRun.Detail states
	// (finding_run_record_test.go:153-173). Never an input, never a Detail, and
	// never a %v verb applied to a struct or a slice.
	note := fmt.Sprintf("# pyry agent-run background-reach probe: one run's record\n\n"+
		"%s\n\n```json\n%s```\n\n"+
		"Summary: pyry exited %d; %d matched row(s) and %d liveness read(s); the two runner "+
		"readings %s; attribution selected %s; trailer outcome %s.\n",
		finWriteSafetyClaim, blob, rec.ExitCode, len(rec.Rows), len(rec.Liveness),
		rec.RunnerAgreement, rec.Attribution.Selected.Value, rec.Trailer.Outcome)

	for _, f := range []struct {
		name    string
		content []byte
	}{
		{finWriteRecordFile, blob},
		{finWriteNoteFile, []byte(note)},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.content, 0o600); err != nil {
			t.Errorf("write run artifact %s: %v", f.name, err)
		}
	}
}

// --- the fixture -------------------------------------------------------------------

// finWriteTrailerPad is 0 DELIBERATELY, and the reason is the whole of AC2's
// trailer channel.
//
// trailNeedle's own comment says it is "placed PAST the cap"
// (result_trailer_observation_test.go:297-300). Against trailPaddedTrailer(0)
// that is not what happens: the line renders 385 bytes and the needle ends at
// byte 146, comfortably inside reachCapCommand's 512-byte cap, so trailScan
// records it into Line INTACT (:176-182).
//
// THAT IS THE PLANT THIS TICKET NEEDS. A needle past the cap never reaches Line
// in the first place, so its absence downstream proves nothing about whether the
// line is carried — a green assertion about nothing. #1290 AC4 established the
// same plant position for the same reason
// (finding_trailer_evidence_test.go:600-612). Measured at 4bc5f5b: pad 0 -> 385
// bytes, needle at 104-146; pad 200 -> 585 bytes, needle still in-cap but
// terminal_reason cut off the end; pad >= 367 -> needle past the cap and the
// test goes vacuous. The offset is a property of the PAD rather than an invariant
// of the needle, so AC2 asserts it rather than trusting this comment.
const finWriteTrailerPad = 0

// finWritePlantedTrailerScan is the shipped scan over the in-cap trailer plant.
// One construction site, so the fixture and AC2's precondition cannot disagree
// about which bytes were scanned.
func finWritePlantedTrailerScan() trailScanResult {
	return trailScan([]byte(trailPaddedTrailer(finWriteTrailerPad) + "\n"))
}

// finWritePlantedReapLog renders pyry's own captured stderr with the needle on an
// ANCHORED line, reusing #1280's plant verbatim
// (finding_attribution_fanout_test.go:730) for its stated reason: trailReapLine
// splices its pgids argument raw and tdnParsePGIDs stops at the first ]
// (teardown_liveness_test.go:246-254), so the list still parses and the needle
// lands in tdnReapOutcome.Line — the channel the fan-out drops. A needle on a
// NON-anchored line would be skipped before any field was filled
// (:161-163) and the sweep would go green over a record that kept the whole
// outcome.
func finWritePlantedReapLog() []byte {
	return []byte(trailReapLine(1, fmt.Sprintf("[%d] %s", finRecordSharedPGID, trailNeedle)) + "\n")
}

// finWriteInputs returns the maximal input set: every omitempty field non-zero,
// and trailNeedle planted in every input the pipeline REDUCES OR DROPS.
//
// A function rather than a package-level var, for trailRunWellFormed's stated
// reason (trail_run_outcome_test.go:608-610): a shared backing array is reachable
// from every test in this package, and `go test -race` runs them in parallel.
//
// # Plant only where the pipeline reduces
//
// This is the trap that would make an over-broad plant list red against a CORRECT
// build. The record carries several inputs verbatim BY DESIGN — Liveness whole
// including its Detail, StateColumn and ToolStderr; Attribution whole; Trailer
// whole including the four decoded scalars; RunnerFromEnv; ClaudeVersion (capped)
// — and finding_run_record_test.go:137-151 states it while
// TestFinRecordEmbedsTrailerRecordWhole pins it. A needle in any of those WILL
// appear in the artifact, correctly. The four plants are therefore exactly the
// inputs that are reduced or dropped:
//
//  1. each matched row's Command — verbatim argv, reduced to finRecordProc's
//     three integers (finding_run_record_test.go:372-374)
//  2. ClaudeCommand — reduced to one of tdnRunnerFromArgv's three constants (:379)
//  3. the trailer scan's Line — dropped by finTrailerBuild
//  4. the reap stderr — pyry's own captured bytes (teardown_liveness_test.go:126),
//     dropped by finAttributeFanOut
//
// # Why each omitempty field is filled
//
// The census (AC1) is only as strong as the fixture: matched_rows, liveness,
// conditions, unreportable_pgids, entries, ppid, state_column and tool_stderr all
// vanish from the artifact when zero, and each is one a lazier fixture would
// silently drop. #1291's liveness fixture is {Verdict, PID}, which renders four of
// pinStateOutcome's seven keys; that gap is precisely what this census closes.
// Passing pgid 1 alongside the real group is what fills Conditions and
// Unreportable in one call: finAttributeFanOut partitions pgid <= 1 as
// unreportable (finding_attribution_fanout_test.go:237-247) while 7788 still
// produces the entry AC2's premise reads.
//
// # The liveness outcome is hand-built, and it is not a shortcut
//
// pinReadState execs `ps` (process_pin_liveness_test.go:293), which this file
// forbids, so no shipped producer is available. Nor would one serve: NO SINGLE ARM
// OF pinClassifyState FILLS ALL SEVEN FIELDS — the running arm fills StateColumn
// and leaves ToolStderr empty, and the instrument-failed-with-stderr arm (:347)
// fills ToolStderr and leaves StateColumn empty. The maximal shape is therefore
// not a reading, and nothing here reads it as one; its job is that every key
// renders. ToolStderr is non-empty AND RIG-AUTHORED: the field is carried whole,
// so a needle there would be a plant in the wrong place.
//
// # The certified reason is "completed" and not the budget one
//
// trailAdmitAttribution returns trailAdmitVoidBudgetFired the moment certified ==
// trailBudgetTerminalReason (trailer_admissibility_test.go:425), ahead of every
// reap-side arm. AC2's premise is that the entry reads trailAdmitProof — reachable
// only from a needle-bearing line that was recognised, parsed and matched — so a
// budget reason here would defuse the non-vacuity check into a Fatalf about the
// wrong thing.
func finWriteInputs() finRecordInputs {
	return finRecordInputs{
		ExitCode: 0,
		Rows:     finRecordMatchedRows(" " + trailNeedle),
		Liveness: []pinStateOutcome{{
			Verdict:     pinStateInstrumentFailed,
			Detail:      "hand-built: the maximal shape, so every omitempty key renders",
			PID:         finRecordWrapperPID,
			PPID:        finRecordClaudePID,
			StateColumn: "Ss",
			ExitStatus:  pinExitStatusUnknown,
			ToolStderr:  "ps: no such process",
		}},
		Attribution: finAttributeFanOut(finWritePlantedReapLog(),
			[]int{1, finRecordSharedPGID}, "completed"),
		// #1286's Plant #3, SUPERSEDED BY #1320 AND RETIRED BY #1321 — not a live
		// guard. finTrailerBuild takes a finSighting now, which has no .Line, so
		// the needle in the scanned line reaches this record through no channel its
		// input has. The directory-wide sweep stays non-vacuous on the other three
		// plants (each row's Command, ClaudeCommand, the reap stderr); the plant
		// list at :228 is #1321's to revisit.
		Trailer: finTrailerBuild(trailOutcomeVoidBudgetFired,
			finTrailerSighting(finWritePlantedTrailerScan(), 250*time.Millisecond,
				trailBoundFromMiss)),
		RunnerFromEnv: reachRunnerPathFromEnv(finRecordEnvDelta()),
		ClaudeCommand: tdnFixturePtyArgv + " " + trailNeedle,
		ClaudeVersion: "2.1.220 (Claude Code)",
	}
}

// finWriteRender builds and writes the fixture into a fresh directory and returns
// every file that landed there. One build, one write; each test reads the
// directory back rather than the value it handed in, which is the difference
// between measuring the artifact and measuring the record.
func finWriteRender(t *testing.T) map[string][]byte {
	t.Helper()
	dir := t.TempDir()
	finWriteArtifacts(t, dir, finRecordBuild(finWriteInputs()))
	return finWriteReadDir(t, dir)
}

// --- the instrument's own reads ------------------------------------------------------

// finWriteReadDir reads EVERY regular file in dir, keyed by base name.
//
// os.ReadDir rather than the two names the writer wrote, and that is the whole
// point: a later edit that adds a third file inherits the sweep for free.
// reach.ps.txt is the proof that "a later edit adds a third file" is a thing that
// happens (background_reach_probe_test.go:834-844).
//
// Fatal on any read error: a sweep that silently skipped a file it could not read
// would report clean on the one file that leaked.
func finWriteReadDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the artifact directory %s: %v", dir, err)
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("reading artifact %s: %v", entry.Name(), err)
		}
		files[entry.Name()] = content
	}
	if len(files) == 0 {
		t.Fatalf("the artifact directory %s holds no file, so every sweep below would pass "+
			"against a writer that produced nothing", dir)
	}
	return files
}

// finWriteDeclaredPaths collects every json PATH reachable from typ: each field's
// tag name joined to its parent's with ".", and "[]" appended where the walk
// descends through a slice, array or pointer element.
//
// # Paths and not bare names, because four of these names are shared
//
// A census over bare NAMES cannot see a nested field vanish, because some OTHER
// type still renders the name: `detail` is declared by five of the seven types
// reached here, and `pid`, `ppid` and `pgid` by two each. Zeroing the liveness
// outcome's PPID drops liveness[].ppid from the published artifact, and a name
// census stays green because matched_rows[].ppid still renders it — the exact
// "an operator reads absence as not measured" failure AC1 exists to prevent, on
// the field-shape #1291's {Verdict, PID} fixture already exhibits. It is sharpest
// for matched_rows, one of the two slices AC1 names explicitly: ALL THREE of
// finRecordProc's keys are shared, so a name census covers that slice not at all.
//
// # Both walks are recursive, for the reason the deferral gave
//
// The TYPE walk to finWriteObservedPaths' VALUE walk. finRecordRun has four
// struct- or slice-valued fields, so a top-level scan inspects ten keys, misses
// every nested one, and reads as a structural guarantee it is not providing —
// "vacuous coverage is worse than none" (finding_run_record_test.go:934-944).
// Same shape as finRecordInputReaches (:731), which answers a different question
// and is called directly by AC4 rather than reimplemented.
//
// `stack` closes the walk against a self-referential type. It is a RECURSION
// STACK rather than a visited set: the same type reached at two different paths
// yields two different path sets and both must be collected — trailAdmitResult is
// reachable through attribution.entries[].admit AND attribution.selected, and a
// visited set would silently drop whichever it met second.
//
// # Which fields encoding/json actually renders
//
// An unexported field is skipped: encoding/json renders none of them, tagged or
// not, so recording one would fail AC1 over a field the artifact was never going
// to carry. The single exception is an ANONYMOUS field, whose tag json ignores
// and whose exported fields it promotes to the parent — so the walk descends at
// the PARENT's path and records no path of its own. No type reached from
// finRecordRun has either shape today; both arms are here so that a sibling
// adding one does not turn this census into a spurious failure that reads like a
// dropped field.
func finWriteDeclaredPaths(typ reflect.Type) map[string]bool {
	paths := make(map[string]bool)
	stack := make(map[reflect.Type]bool)

	var walk func(t reflect.Type, path string)
	walk = func(t reflect.Type, path string) {
		switch t.Kind() {
		case reflect.Pointer:
			walk(t.Elem(), path)
			return
		case reflect.Slice, reflect.Array:
			walk(t.Elem(), path+"[]")
			return
		case reflect.Struct:
		default:
			return
		}
		if stack[t] {
			return
		}
		stack[t] = true
		defer delete(stack, t)

		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if !field.IsExported() || (name == "" && field.Anonymous) {
				if field.Anonymous {
					walk(field.Type, path)
				}
				continue
			}
			if name == "" {
				name = field.Name
			}
			child := name
			if path != "" {
				child = path + "." + name
			}
			paths[child] = true
			walk(field.Type, child)
		}
	}
	walk(typ, "")
	return paths
}

// finWriteObservedPaths collects every key present in a decoded JSON value, keyed
// by its PATH, with array indices NORMALISED to "[]".
//
// Normalised because finWriteDeclaredPaths cannot know how many elements a slice
// held, and the two sets have to be comparable. The indexed form is what the
// headroom walk below needs instead, which is why that one is separate: it has to
// name WHICH detail overran.
func finWriteObservedPaths(raw json.RawMessage, into map[string]bool) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("decoding the artifact: %w", err)
	}

	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch typed := v.(type) {
		case map[string]any:
			for key, inner := range typed {
				child := key
				if path != "" {
					child = path + "." + key
				}
				into[child] = true
				walk(child, inner)
			}
		case []any:
			for _, inner := range typed {
				walk(path+"[]", inner)
			}
		}
	}
	walk("", value)
	return nil
}

// finWriteLeafKey is the bare json key at the end of a path: the segment after
// the last ".". AC3 asks about key SHAPES rather than positions, and matching the
// forbidden substrings against a whole path would let a parent segment decide a
// child's verdict.
func finWriteLeafKey(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

// finWriteObservedDetails collects every Detail the artifact carries, keyed by its
// JSON PATH.
//
// A third walk rather than a reuse of the key scan, because the headroom failure
// has to name WHICH Detail overran and the key scan deliberately discards the
// path. It names the path and the byte length and never the string itself:
// printing the content of a Detail that just failed a leak check would write the
// leak into CI logs, which is the shape finding_run_record_test.go:1036-1041
// already follows.
func finWriteObservedDetails(raw json.RawMessage, into map[string]string) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("decoding the artifact: %w", err)
	}

	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch typed := v.(type) {
		case map[string]any:
			for key, inner := range typed {
				child := key
				if path != "" {
					child = path + "." + key
				}
				if s, ok := inner.(string); ok && key == finWriteDetailKey {
					into[child] = s
				}
				walk(child, inner)
			}
		case []any:
			for i, inner := range typed {
				walk(fmt.Sprintf("%s[%d]", path, i), inner)
			}
		}
	}
	walk("", value)
	return nil
}

// finWriteSorted renders any of this file's string-keyed sets in a stable order,
// so a failure message names the same entries in the same order on every run
// rather than in Go's randomized map order.
//
// IT RETURNS THE KEYS AND NEVER THE VALUES, and that is load-bearing rather than
// incidental: it is called on the artifact directory (keys are file names, values
// are the file CONTENTS), on the walked details (keys are JSON paths, values are
// the published strings) and on the key sets. Printing what a file or a detail
// HOLDS after it just failed a leak check would write the leak into CI logs —
// security review item [7]. Generic over the value type so there is one such
// function to get right rather than one per map, which is this file's own
// finWriteArtifacts-signature doctrine: prefer the shape that cannot be got wrong
// over the discipline that must not be.
func finWriteSorted[V any](set map[string]V) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// --- AC1 -----------------------------------------------------------------------------

// TestFinWriteArtifactRendersEveryDeclaredField is AC1: every field the record
// carries — including the fields nested inside the two embedded sub-records and
// the two slices — reaches the rendered output, so a field cannot be silently
// dropped and read by an operator as absent.
//
// Set EQUALITY rather than containment, reported in both directions, because the
// converse failure is worth naming too: an artifact rendering a key the record's
// type does not declare is an artifact carrying something the census never
// reviewed. Equality costs nothing here and is strictly stronger.
//
// This census is also what stops AC2 from passing against a writer that renders
// nothing: a needle sweep over an empty artifact is green.
//
// # Compared by PATH, not by key name
//
// Bare names cannot carry AC1's guarantee, because four of the names this record
// renders are declared by more than one of the seven types it reaches. See
// finWriteDeclaredPaths: a dropped liveness[].ppid is invisible to a name census
// because matched_rows[].ppid still renders `ppid`, and matched_rows — one of the
// two slices AC1 names explicitly — has no unshared key at all.
//
// WHAT THIS CENSUS IS ABOUT IS ABSENCE, which is what an operator misreads as
// "not measured". A field without omitempty cannot go absent: it renders its zero
// value, and `"detail": ""` is a visible empty reading rather than a missing one.
// So the fields this can actually catch are the eight omitempty ones — every one
// of which finWriteInputs fills for exactly that reason — plus any whole subtree
// a writer stopped descending into. A zero value that should not have been zero
// is a different claim and belongs to the builder's own tests, not here.
func TestFinWriteArtifactRendersEveryDeclaredField(t *testing.T) {
	files := finWriteRender(t)
	record, ok := files[finWriteRecordFile]
	if !ok {
		t.Fatalf("no %s in the artifact directory, which holds %v", finWriteRecordFile,
			finWriteSorted(files))
	}

	declared := finWriteDeclaredPaths(reflect.TypeOf(finRecordRun{}))

	// THE NON-VACUITY PRECONDITION. A walk that collected implausibly few paths
	// would make the equality below trivially satisfiable. The floor rather than
	// the exact count (41 at 4bc5f5b: finRecordRun's 10 fields, plus 3 under
	// matched_rows[], 7 under liveness[], 8 under attribution — 2 of them the
	// twice-reached trailAdmitResult's, at both entries[].admit and selected — and
	// 10 under trailer), so that a sibling adding a field to its own record is not
	// a failure here. Well above the 10 a walk that stopped at the top level
	// collects, which is the failure this floor is for.
	if len(declared) < 35 {
		t.Fatalf("the type walk collected %d declared path(s): %v. The record reaches seven struct "+
			"types, so a count this low means the walk stopped short and the equality below would "+
			"be trivially satisfiable", len(declared), finWriteSorted(declared))
	}

	observed := make(map[string]bool)
	if err := finWriteObservedPaths(record, observed); err != nil {
		t.Fatalf("walking %s: %v", finWriteRecordFile, err)
	}

	var missing, extra []string
	for _, path := range finWriteSorted(declared) {
		if !observed[path] {
			missing = append(missing, path)
		}
	}
	for _, path := range finWriteSorted(observed) {
		if !declared[path] {
			extra = append(extra, path)
		}
	}

	if len(missing) > 0 {
		t.Errorf("the record declares %d path(s) the artifact does not render: %v. An operator "+
			"reads an absent field as \"not measured\" rather than as \"dropped on the way to the "+
			"file\", so a field that vanishes here publishes a false absence. Every omitempty field "+
			"is non-zero in finWriteInputs precisely so this cannot pass by rendering less",
			len(missing), missing)
	}
	if len(extra) > 0 {
		t.Errorf("the artifact renders %d path(s) the record's type does not declare: %v — content "+
			"reaching the file from somewhere other than finRecordRun is content no census here "+
			"reviewed", len(extra), extra)
	}
}

// --- AC2 -----------------------------------------------------------------------------

// TestFinWriteArtifactsCarryNoCapturedBytes is AC2: the shipped trailNeedle is
// planted in every input the pipeline reduces or drops, and appears ZERO times
// across EVERY FILE the writer wrote.
//
// # Why zero, and not "once, in a field marked for review"
//
// The record carries no trailer line in any form, capped or otherwise:
// finTrailerRecord (finding_trailer_evidence_test.go:142) is ten scalars with no
// Line, and finRecordInputs carries neither a trailObservation nor a
// trailScanResult (finding_run_record_test.go:214-221), so trailScanResult.Line
// is unreachable from this record at any depth. A SINGLE OCCURRENCE WOULD
// THEREFORE MEAN A REDUCTION HAD BEEN WIDENED BACK INTO A RETENTION — not that a
// permitted field needed review.
//
// # Why the trailer plant lands INSIDE the cap
//
// Past the cap the needle never reaches trailScanResult.Line in the first place,
// so its absence downstream would prove nothing about whether the line is carried
// — see finWriteTrailerPad. Both halves are asserted below rather than trusted.
//
// # The mandated mutations, applied and observed
//
// A redaction assertion that stays green when the redaction is removed is a
// failure mode this family has shipped once (#1284). Both were applied, observed
// RED and reverted:
//
//   - M1 — the pipeline sweep is live. In finRecordBuild
//     (finding_run_record_test.go:386-390) the Detail format was changed to
//     interpolate in.Rows[0].Command — the %v-on-an-input slip finRecordRun's own
//     comment names at :168-173 — INSIDE the format string rather than appended
//     after trailDetail returns. Appended, reachCapCommand has already run and the
//     needle survives regardless, which proves nothing about #1284's defect;
//     injected inside, the cap applies and the headroom step below is what keeps
//     the needle visible. Observed: RED, naming BOTH run.json and run.md — red on
//     only one file would mean the directory walk was not reading every file —
//     and red a second time on AC4's local pairing check.
//   - M2 — the directory walk is live, and does not rest on key names.
//     finWriteArtifacts was temporarily widened to take finRecordInputs alongside
//     the record and to write a third file run.notes.txt holding
//     fmt.Sprintf("evidence: %s", in.ClaudeCommand) — reach.ps.txt in miniature.
//     Observed: RED, naming run.notes.txt, a file no test was told about. The key
//     is "evidence" deliberately: it matches none of AC3's shape list, so a green
//     result could not have been resting on key names. The signature widening was
//     reverted; the writer ships taking the record alone.
func TestFinWriteArtifactsCarryNoCapturedBytes(t *testing.T) {
	in := finWriteInputs()

	// THE NON-VACUITY PRECONDITIONS, one per plant channel and each naming its own
	// channel, so a fixture that lost a plant fails HERE rather than passing the
	// sweep three steps later. A needle the pipeline never carried as far as the
	// writer's input is a green assertion about nothing.
	for i, row := range in.Rows {
		if !strings.Contains(row.Command, trailNeedle) {
			t.Fatalf("channel 1 (matched row argv): input row %d carries no needle in its %d-byte "+
				"command, so the sweep below would pass against a writer that leaked every argv it "+
				"was handed", i, len(row.Command))
		}
	}
	if !strings.Contains(in.ClaudeCommand, trailNeedle) {
		t.Fatalf("channel 2 (claude argv): the planted claude command carries no needle, so the " +
			"runner-path reduction is not under test")
	}

	line := trailPaddedTrailer(finWriteTrailerPad)
	scan := finWritePlantedTrailerScan()
	if scan.State != trailSeen {
		t.Fatalf("channel 3 (trailer line): fixture state is %q (%s), want %q", scan.State,
			scan.Detail, trailSeen)
	}
	if end := strings.Index(line, trailNeedle) + len(trailNeedle); end > reachMaxCommandBytes {
		t.Fatalf("channel 3 (trailer line): the needle ends at byte %d of the %d-byte fixture line, "+
			"PAST the %d-byte cap — so it never reaches trailScanResult.Line and its absence from "+
			"the artifact would prove nothing about whether the line is carried", end, len(line),
			reachMaxCommandBytes)
	}
	if !strings.Contains(scan.Line, trailNeedle) {
		t.Fatalf("channel 3 (trailer line): the capped line does not carry the needle, so the sweep "+
			"below would pass against a writer that kept it verbatim. Line is %d bytes",
			len(scan.Line))
	}

	if !bytes.Contains(finWritePlantedReapLog(), []byte(trailNeedle)) {
		t.Fatalf("channel 4 (reap stderr): the synthetic reap log carries no needle")
	}
	// The premise doubles as the non-vacuity proof, following
	// finding_attribution_fanout_test.go:737-741: trailAdmitProof is reachable only
	// if the needle-bearing line was recognised as anchored, parsed and found to
	// name the held group. A plant that stopped being anchored lands here.
	//
	// The COUNT and the closed-set Value, never the entries themselves: a %+v on
	// them renders every Admit.Detail, and the rule is to name the count, the path
	// and the length rather than the string (security review item [7]). The Values
	// are safe to print because trailAdmitAttribution returns one of a closed set
	// (trailer_admissibility_test.go:190-197); the Details beside them are not.
	first := "<no entries>"
	if len(in.Attribution.Entries) > 0 {
		first = in.Attribution.Entries[0].Admit.Value
	}
	if len(in.Attribution.Entries) != 1 || first != trailAdmitProof {
		t.Fatalf("channel 4 (reap stderr): the fan-out produced %d entr(ies), the first reading %q; "+
			"want exactly one reading %s — the premise is that the needle rides an ANCHORED line "+
			"the classifier read in full", len(in.Attribution.Entries), first, trailAdmitProof)
	}

	// The two embedded sub-records asserted CLEAN before the build, per
	// finding_run_record_test.go:996-1014, so a red sweep below names THIS ticket's
	// writer rather than a sibling's builder. Both are built from planted inputs;
	// what is asserted is that the plant did not survive the sub-builder.
	for _, sub := range []struct {
		name string
		val  any
	}{
		{"attribution", in.Attribution},
		{"trailer", in.Trailer},
	} {
		encoded, err := json.Marshal(sub.val)
		if err != nil {
			t.Fatalf("marshalling the %s sub-record: %v", sub.name, err)
		}
		if bytes.Contains(encoded, []byte(trailNeedle)) {
			t.Fatalf("the %s sub-record already carries the needle before this ticket's writer "+
				"runs, so a red sweep below would name the wrong channel", sub.name)
		}
	}

	dir := t.TempDir()
	finWriteArtifacts(t, dir, finRecordBuild(in))
	files := finWriteReadDir(t, dir)

	// THE HEADROOM, ASSERTED ON WHAT WAS ACTUALLY WRITTEN rather than on the built
	// record (#1291 asserts that at :1036). trailDetail caps at
	// reachMaxCommandBytes, so a Detail that had wrongly interpolated an argv would
	// be truncated before the needle if the surrounding prose left no room — and
	// the sweep below would then pass against a leaking writer. That is the defect
	// #1284 shipped and had to fix.
	details := make(map[string]string)
	if err := finWriteObservedDetails(files[finWriteRecordFile], details); err != nil {
		t.Fatalf("walking %s for details: %v", finWriteRecordFile, err)
	}
	// Six Details ride this fixture: the record's, the attribution's, the one
	// entry's Admit, the selected Admit, the trailer's and the one liveness
	// outcome's. A walk that found fewer has stopped descending, and the headroom
	// claim would then hold over a subset.
	if len(details) < 6 {
		t.Fatalf("the artifact walk found %d detail(s) at %v, want at least 6 — the headroom claim "+
			"below would otherwise hold over a subset of the strings the artifact publishes",
			len(details), finWriteSorted(details))
	}
	for _, path := range finWriteSorted(details) {
		if room := reachMaxCommandBytes - len(details[path]); room < len(trailNeedle) {
			t.Errorf("the detail at %s is %d bytes, leaving %d of the %d-byte cap against a "+
				"%d-byte needle: a detail that leaked an argv would be truncated before the needle "+
				"and the sweep below would pass against it. Shorten it — the long-form argument "+
				"belongs in a comment, which no cap applies to", path, len(details[path]), room,
				reachMaxCommandBytes, len(trailNeedle))
		}
	}

	// THE SWEEP, over the map rather than over the two names the writer wrote. The
	// failure names the FILE and never its contents: printing the bytes of a file
	// that just failed a leak sweep would write the leak into CI logs.
	for _, name := range finWriteSorted(files) {
		if bytes.Contains(files[name], []byte(trailNeedle)) {
			t.Errorf("%s carries the needle. Every plant sits in an input the pipeline REDUCES OR "+
				"DROPS — a row's verbatim argv, the claude argv, the trailer scan's Line, pyry's "+
				"captured stderr — and the record retains no trailer line in any form, capped or "+
				"otherwise, so one occurrence means a reduction was widened back into a retention. "+
				"The artifact directory holds %v", name, finWriteSorted(files))
		}
	}
}

// --- AC3 -----------------------------------------------------------------------------

// TestFinWriteArtifactCarriesNoCapturedByteShapedKey is AC3: no key in the
// rendered artifact is argv-, line- or stderr-shaped, walked RECURSIVELY.
//
// The walk must be recursive because a top-level scan on finRecordRun inspects
// ten keys and misses every nested one — the reason
// finding_run_record_test.go:934-944 gives for deferring this scan to this
// ticket: "vacuous coverage is worse than none". The forbidden list is the union
// of the two flat scans this family already ships
// (finding_trailer_evidence_test.go:688, finding_attribution_fanout_test.go:758),
// less the redundant trailer_line, which "line" already covers.
func TestFinWriteArtifactCarriesNoCapturedByteShapedKey(t *testing.T) {
	files := finWriteRender(t)
	observed := make(map[string]bool)
	if err := finWriteObservedPaths(files[finWriteRecordFile], observed); err != nil {
		t.Fatalf("walking %s: %v", finWriteRecordFile, err)
	}
	// The paths carry where each key sits, which is what the failure has to name;
	// the SHAPE question is about the key itself, so both the exemptions and the
	// forbidden list are matched against the leaf.
	keys := make(map[string]bool, len(observed))
	for path := range observed {
		keys[finWriteLeafKey(path)] = true
	}

	// TWO EXEMPTIONS, EACH BY EXACT KEY AND EACH WITH ITS OWN REASON. Never a
	// prefix or substring rule: #1281 settled that a forbidden-key sweep meeting a
	// shipped-and-permitted key defuses by exact key, so that a later field
	// genuinely named for a captured column still trips this scan.
	exempt := map[string]string{
		// A shipped and permitted key on the carried pinStateOutcome
		// (process_pin_liveness_test.go:252). finding_run_record_test.go:148-151
		// names this sweep as the place to exempt it rather than as a reason to
		// strip the field: the value is the ps tool's own stderr about a lookup,
		// not a process's argv or a model's output.
		"tool_stderr": "a permitted field on the carried pinStateOutcome",
		// Matches the substring "argv" and its reason is a DIFFERENT one: the key
		// names the READING, not the argv. Every arm of tdnRunnerFromArgv returns a
		// constant (teardown_liveness_probe_test.go:773-789), so no input byte
		// reaches its value and the field's space is the closed set {ptyrunner ...,
		// streamrunner ..., indeterminate ...}. AC2's second plant is the enforcing
		// proof. Without this exemption the family's shipped list fails on a field
		// that carries no bytes.
		"runner_from_argv": "a closed-set reading of the argv, never the argv",
	}

	// THE EXEMPTION MECHANISM'S OWN NON-VACUITY, iterated over `exempt` ITSELF
	// rather than over a restatement of it. An exemption for a key the artifact
	// does not render could be quietly hiding a stricter rule's failure — if
	// tool_stderr stops rendering under omitempty, that is a loud failure here
	// rather than a silent widening. Restating the set as a second literal would
	// mean a third exemption shipped with no such coverage, which is this file's
	// own finWriteArtifacts-signature doctrine violated in miniature.
	for _, key := range finWriteSorted(exempt) {
		if !keys[key] {
			t.Fatalf("the artifact does not render the exempted key %q, so its exemption covers "+
				"nothing and could be hiding a stricter rule's failure. The artifact's keys are %v",
				key, finWriteSorted(keys))
		}
	}

	for _, path := range finWriteSorted(observed) {
		key := finWriteLeafKey(path)
		if _, ok := exempt[key]; ok {
			continue
		}
		for _, forbidden := range []string{
			"command", "args", "comm", "argv", "line", "stderr", "result", "raw",
		} {
			if strings.Contains(key, forbidden) {
				t.Errorf("the artifact carries key %q at %s, which is %q-shaped: this artifact's "+
					"whole value is that it can be pasted unreviewed, and such a field would "+
					"inherit the operator-review obligation onto the whole directory. If the key is "+
					"genuinely safe, exempt it BY EXACT KEY with its own reason, as %v are",
					key, path, forbidden, finWriteSorted(exempt))
			}
		}
	}
}

// --- AC4 -----------------------------------------------------------------------------

// TestFinWriteArtifactPublishesNoVerbatimModelOutput is AC4: the four decoded
// trailer fields the artifact publishes are not among the fields carrying
// captured bytes, and the artifact says so.
func TestFinWriteArtifactPublishesNoVerbatimModelOutput(t *testing.T) {
	files := finWriteRender(t)

	t.Run("resultTrailer structurally cannot carry the assistant payload", func(t *testing.T) {
		// The claim three shipped comments state in prose
		// (result_trailer_observation_test.go:114,
		// finding_trailer_evidence_test.go:38 and :616) and none of them checks.
		// The decode is what feeds the four published scalars, so its SHAPE is what
		// makes them safe.
		typ := reflect.TypeOf(resultTrailer{})
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "result" {
				t.Errorf("resultTrailer declares a %q member (field %s): that would put the last "+
					"assistant message — roughly 415 of the trailer's retained bytes — into the "+
					"decode, and from there into the four scalars this artifact publishes, in a "+
					"record whose whole value is that it can be pasted unreviewed", name, field.Name)
			}
		}
	})

	t.Run("the four decoded scalars crossed and the payload beside them did not", func(t *testing.T) {
		// Decoded off the artifact's own KEYS rather than into finRecordRun, so this
		// reads what an operator pasting the file would read.
		var artifact struct {
			Trailer struct {
				Subtype        string `json:"subtype"`
				TerminalReason string `json:"terminal_reason"`
				IsError        bool   `json:"is_error"`
				StopReason     string `json:"stop_reason"`
			} `json:"trailer"`
		}
		if err := json.Unmarshal(files[finWriteRecordFile], &artifact); err != nil {
			t.Fatalf("decoding %s: %v", finWriteRecordFile, err)
		}

		// The wire values of trailPaddedTrailer(finWriteTrailerPad), whose `result`
		// field held the needle. THE PAIR IN ONE ARTIFACT IS THE CLAIM: the four
		// fields cross by design, and the payload they sat beside does not. The
		// full-directory sweep is AC2's; the needle check here is what makes the
		// pairing local rather than a cross-reference.
		for _, f := range []struct{ name, got, want string }{
			{"subtype", artifact.Trailer.Subtype, "error_max_turns"},
			{"terminal_reason", artifact.Trailer.TerminalReason, "max_turns"},
			{"stop_reason", artifact.Trailer.StopReason, "end_turn"},
		} {
			if f.got != f.want {
				t.Errorf("trailer.%s: got %q, want %q", f.name, f.got, f.want)
			}
		}
		if !artifact.Trailer.IsError {
			t.Error("trailer.is_error: got false, want true — the budget-fired reading crosses too")
		}
		if bytes.Contains(files[finWriteRecordFile], []byte(trailNeedle)) {
			t.Errorf("%s carries the payload the four scalars sat beside on the wire",
				finWriteRecordFile)
		}

		// NOT RESTATED HERE: that the decode ran against the FULL line rather than
		// the capped Line. TestFinTrailerRecordReadsTheDecodedTrailer already pins
		// it (finding_trailer_evidence_test.go:430-470, at pad 200 so
		// terminal_reason is cut from Line), and re-deriving a merged sibling's test
		// is out of scope.
	})

	t.Run("the artifact states the claim and not the caveat", func(t *testing.T) {
		note := string(files[finWriteNoteFile])
		if !strings.Contains(note, finWriteSafetyClaim) {
			t.Errorf("%s does not carry the standing claim: an artifact that does not say what it "+
				"is safe to do with reaches an operator as an unlabelled blob", finWriteNoteFile)
		}
		// THE NEGATIVE HALF, and it is not red-by-construction: the note MAY NOT
		// explain the absent caveat using the caveat's own words. A reader meeting
		// "operator-review-before-paste" in the note would go looking for the field
		// that obligation attaches to (trailScanResult.Line,
		// result_trailer_observation_test.go:106) and find no such field, which is
		// the opposite of what the artifact is for. The explanation lives in
		// finWriteArtifacts' doc comment and here — neither of which is written into
		// the artifact.
		for _, caveat := range []string{"operator-review-before-paste", "OPERATOR-REVIEW"} {
			if strings.Contains(note, caveat) {
				t.Errorf("%s carries %q. The record has no field that obligation attaches to, so "+
					"the note would train a reader to look for one that is not there",
					finWriteNoteFile, caveat)
			}
		}
	})
}
