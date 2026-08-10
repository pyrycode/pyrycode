//go:build e2e_realclaude

package realclaude

// The reduction: how one during-turn pin scan becomes the four things the
// staging record consumes — the count of rows carrying the run's FIFO needle,
// the raw process-group projection of those rows, the rows themselves, and
// claude's own argv.
//
// This file reaches no verdict about pyry and takes no measurement. It ships a
// PURE function over a pinScan value plus its offline trap: no live scan, no ps
// exec, no pyry spawn, no FIFO, no subject process, no credentials, no env gate,
// no t.Skip, no clock, no goroutine, no filesystem access of any kind.
//
//	go test -race -tags e2e_realclaude -run '^TestFinLivePin' -v ./internal/e2e/realclaude/
//
// # Why the reduction is separable from the scan
//
// pinScan is already a value: pinMatchArgvExcluding
// turns a ps table's BYTES into one, and the live wrapper pinScanArgv (:191) does
// the same over a real exec. So the reduction is separable from the exec, and a
// synthetic table drives exactly the matching the live path uses. That is the
// whole point: the later ticket that stages the live turn has ONE turn to spend,
// and everything it can be made to get wrong offline should be made to get wrong
// offline.
//
// # The count is the whole file
//
// finOutcomeStagingGate's count arm (finding_staging_gate_test.go:347) returns
// stage-pin-count-unexpected when PinMatchCount != PinWantCount. A mis-fill does
// not fail loudly in development — it fires on every CORRECTLY staged run,
// reporting a staging failure while the rig looks correct, and burns a live turn
// per attempt. One scan carries two needles, which is what makes both wrong fills
// available and plausible; both are pinned as VALUES in
// TestFinLivePinCountIsNeitherWrongCandidate rather than merely prohibited in
// prose at finLivePinWantRows.
//
// # This file execs nothing, and the check is the symbol list
//
// An `exec.` grep reads clean here by construction, so it proves nothing. The
// rule is the symbols: pinScanArgv, probeProcessSnapshot, tdnScan, holdProbeFIFO
// and WithWorktreeAuthenticated are FORBIDDEN in this file because each execs or
// blocks internally. pinMatchArgvExcluding, reachMatchArgvRows,
// reachMatchedNeedle, reachCapCommand and tdnClaudeCommand are pure over bytes
// and are what this file composes. There is no ps flag to get wrong because
// there is no ps: no -E, no -Eww, no BSD `eww`, and no environment read at all.
//
// # Failure messages name pids, counts and needle NAMES — never a Command value
//
// The only place captured-shaped bytes could be printed here is a t.Fatalf
// argument list. Today every command in this file is a synthetic fixture string,
// so a message quoting one is harmless — but the reduction's own inputs are
// verbatim argv off the ambient process table on a live run, and the habit has to
// survive contact with the first live caller. So no assertion below prints a
// Command; the truncation assertion in particular reports the pid and the rule
// that decides membership, not the string it examined.

import (
	"strings"
	"testing"
)

// --- the reading ----------------------------------------------------------------

// finLivePinReading is the during-turn pin scan reduced to what the staging
// record consumes, and nothing else.
//
// INPUT ONLY — NEVER PUBLISHED. Rows[i].Command and ClaudeCommand are verbatim
// argv read off the AMBIENT process table: any local process's command line, not
// just this run's. Like finOutcomeStaging (finding_staging_gate_test.go:142-150)
// this type deliberately carries NO json tags, so it cannot be embedded in,
// marshalled into or quoted by a published record by accident. Adding tags "for
// symmetry" is the first step toward publishing captured bytes into a public
// issue.
//
// Rows and RowCount mirror pinScan.Matches / pinScan.MatchCount — the same
// pairing, and RowCount is assigned exactly ONCE, as len(Rows), in the same place
// pinPartition assigns its own. One producer
// for the number; it is never computed a second way.
//
// PGIDs is a []int and never a []reachProc. Its consumer finAttributeFanOut
// (`finAttributeFanOut`) takes []int precisely because that
// signature closes the credential channel structurally rather than by a check
// inside the function (:195-202). The conversion happens HERE, in the reduction,
// so a downstream record has an []int to hand and never reaches into Rows for it
// — a reduction that returned only Rows would pass every test in this file while
// pushing the channel one file downstream.
//
// Rows ALIASES the scan's rows, including their Needles slices; nothing is
// deep-copied. That is correct rather than overlooked: the scan is a value the
// caller owns, and neither this file nor any caller mutates a reachProc after
// reachMatchArgvRows builds it. A defensive copy would be a defence for a failure
// mode nobody has observed.
type finLivePinReading struct {
	Rows          []reachProc
	RowCount      int
	PGIDs         []int
	ClaudeCommand string
}

// --- the count ------------------------------------------------------------------

// finLivePinWantRows is how many ROWS a healthy during-turn pin matches on the
// run's FIFO needle. Rows — not pids, not process groups.
//
// THE REASON. #1230's live run of this exact shape — a hold command staged
// through claude's Bash tool, pinned by FIFO path in full argv — matched TWO rows
// on the FIFO needle: the `zsh -c` wrapper claude runs Bash through, whose argv
// carries the whole command string, and the `cat` itself. That measurement is
// recorded verbatim in shipped code at teardown_liveness_probe_test.go:511-514
// and is this constant's whole basis. #1268 CORROBORATES it and mutation-tested
// it — dropping the wrapper cut the count 2→1 (docs/knowledge/codebase/1268.md:151-157)
// — but its subject is a rig-staged `sh -c` rather than a claude-staged Bash
// call, so it is corroboration and not the primary basis.
//
// TestTrailRigCarriesMoreThanOneMatchedRow is NOT
// cited: it fails on MatchCount <= 1 (:563), i.e. it asserts MORE THAN ONE and
// never EXACTLY TWO. finding_staging_gate_test.go:395-397 already cites it for
// its own PinMatchCount: 2, and that citation is weaker than the number it
// justifies. This constant is therefore strictly stronger than any shipped
// assertion. A live run reporting a different count is a non-verdict outcome the
// gate already returns; it is never a silent first-match, and never a reason to
// loosen the expectation.
//
// PROHIBITION 1 — NEVER fill this from scan.MatchCount. It is 3 on a healthy
// run: one scan carries BOTH the FIFO needle and tdnClaudeNeedle, because
// reachMatchArgvRows matches a row on ANY needle
// (background_reach_probe_test.go:911-916), so claude's own row is in the match
// set alongside the wrapper and the `cat`.
//
// PROHIBITION 2 — NEVER fill this from the size of the process-group set. It is
// 1 on a healthy run (teardown_liveness_probe_test.go:516-520): claude isolates
// the whole Bash command into one detached group, and #1230's hand run measured
// count=1 on all three reps.
//
// EITHER wrong fill has the same consequence and the same cost: it fires
// finOutcomeStagingGate's count arm (finding_staging_gate_test.go:347) against a
// want of 2 on a CORRECTLY staged run, reporting stage-pin-count-unexpected with
// no other symptom, and spends one live claude turn finding out.
const finLivePinWantRows = 2

// --- the reduction --------------------------------------------------------------

// finLivePinReduce reduces one during-turn pin scan plus the run's FIFO path to
// the four values the staging record consumes.
//
// PURE: no exec, no *testing.T, no clock, no goroutine, no I/O, no error return.
// One pass over scan.Matches. It mutates nothing it does not allocate, so it is
// safe to call concurrently by construction.
//
// MEMBERSHIP IS reachMatchedNeedle, NEVER a
// re-scan of .Command. reachMatchArgvRows matches against the UNCAPPED command
// line (:913) and stores the CAPPED one (:924), so a row whose FIFO path sits
// past reachMaxCommandBytes is genuinely matched — its Needles list records the
// hit — while its retained Command no longer contains the path at all.
// strings.Contains(m.Command, fifoPath) looks equivalent and silently drops such
// a row, yielding 1 where the answer is 2. The trap's truncation pair is what
// makes this rule red rather than decorative.
//
// NO SECOND FULL-ARGV MATCHER IS GROWN. This is a pure post-filter over what
// reachMatchArgvRows already produced — the same relationship pinPartition has to
// it and for the same stated reason (process_pin_liveness_test.go:137-148:
// "reachMatchArgvRows is this package's one full-argv matcher and #1235 must not
// grow a second"). probeHasCommand is
// doubly wrong here besides: probeAnnotateCommands (:930) stores only
// filepath.Base(argv[0]), so the held command's Command is `cat` and it would
// match any unrelated `cat` on the machine, and it is scoped to descendants.
//
// NOTHING IS DEDUPED, anywhere — not Rows, not PGIDs. The projection handed on is
// EVERY matched row's .PGID, in scan order, duplicates intact. finAttributeFanOut
// dedupes and sorts internally (finding_attribution_fanout_test.go:220-229), and
// its own comment explains that the sort is what makes the record a pure function
// of the SET rather than of ps output order; pre-reducing here would duplicate
// that work and destroy the raw evidence. tdnPinHeld
// (`tdnPinHeld`) is the shipped FIFO filter but the WRONG
// SHAPE to copy: it dedupes pids and collapses to a single pgid, returning 0 when
// the rows do not resolve to exactly one group (:538-541).
//
// tdnClaudeCommand RUNS OVER scan, NOT OVER Rows. Claude's row carries the claude
// needle and NOT the FIFO needle — that disjointness is argued at
// teardown_liveness_probe_test.go:155-160 — so running it over the FIFO-filtered
// rows would find zero hits, take the n != 1 arm and return "" on every healthy
// run: a silent wrong answer with a plausible-looking cause.
//
// NO ERROR RETURN AND NO FAILURE ARM, deliberately. The ps exec's error is
// pinScanArgv's and already has a named home in the record —
// finOutcomeStaging.PinScanErrored → finOutcomePinScanErrored, ranked ABOVE the
// count arm (finding_staging_gate_test.go:332-341) precisely so a count of 0 the
// error produced is never reported as a count that was measured. A second error
// channel here would give that outcome two producers. A ZERO scan reduces to a
// ZERO reading: nil Rows, RowCount 0, nil PGIDs, empty ClaudeCommand, no panic.
// An EMPTY fifoPath matches nothing rather than everything, with no code:
// reachMatchArgvRows never records an empty needle (:913 requires needle != ""),
// so no row's Needles can contain "". Either way the gate's count arm fires
// against a want of finLivePinWantRows, which is the correct outcome.
//
// An empty ClaudeCommand is AMBIGUITY, not failure: tdnClaudeCommand returns ""
// when zero or several rows carry the claude needle
// (teardown_liveness_probe_test.go:571-573). It is provenance, it is not one of
// finOutcomeStaging's eight fields, and it must not be gated on here or
// downstream.
func finLivePinReduce(scan pinScan, fifoPath string) finLivePinReading {
	var out finLivePinReading
	for _, m := range scan.Matches {
		if !reachMatchedNeedle(m, fifoPath) {
			continue
		}
		out.Rows = append(out.Rows, m)
		out.PGIDs = append(out.PGIDs, m.PGID)
	}
	out.RowCount = len(out.Rows)
	out.ClaudeCommand = tdnClaudeCommand(scan)
	return out
}

// --- the fixture ----------------------------------------------------------------

// finLivePinFIFOPath is a SYNTHETIC needle and never a path. It is passed to
// strings.Contains inside the shipped matcher and to nothing else: it is never
// opened, stat'd, canonicalised, joined or executed, so path traversal, TOCTOU
// and symlink following are structurally inapplicable rather than merely
// unaddressed. It must not be built from t.TempDir() or os.Getenv — either would
// put an operator filesystem path into a test file for nothing, the precedent
// reason at finding_staging_gate_test.go:370-375.
const finLivePinFIFOPath = "/tmp/pyry-fin-live-pin/live-pin-hold"

// finLivePinClaudeCommand is the fixture's claude row, splicing tdnClaudeNeedle
// itself rather than repeating its text so the row tracks the constant. It stays
// well under reachMaxCommandBytes, which is what lets the trap assert
// ClaudeCommand equals it EXACTLY: a row over the cap would come back with
// reachTruncationMarker appended.
const finLivePinClaudeCommand = "/opt/node/bin/node /opt/claude/cli.js " +
	"--session-id 11111111-2222-3333-4444-555555555555 --settings /tmp/s.json " +
	tdnClaudeNeedle + " /tmp/pyry-fin-live-pin/system.txt"

// finLivePinPadToken pads the wrapper row past the cap. It must carry NEITHER
// needle and must not introduce a SECOND occurrence of the FIFO path. A filler
// carrying tdnClaudeNeedle would make tdnClaudeCommand ambiguous (n != 1 → ""),
// failing the trap's claude-argv assertion against a CORRECT implementation; a
// filler repeating the FIFO path would put it back inside the surviving prefix
// and quietly disarm the truncation pair.
const finLivePinPadToken = "--pad "

// The fixture's four rows, by pid. Row 200's group is DISTINCT from the
// wrapper's, and that is load-bearing: sharing it would make "claude's group is
// absent from the projection" red against a correct implementation, since the
// projection carries that group for the FIFO rows.
const (
	finLivePinClaudePID  = 200
	finLivePinWrapperPID = 300
	finLivePinCatPID     = 400
	finLivePinClaudePGID = 200
	finLivePinHeldPGID   = 300
)

// finLivePinTable builds the synthetic four-column `ps -axww -o
// pid=,ppid=,pgid=,command=` table, following reachArgvFixture's shape
// (background_reach_probe_test.go:1163-1173) rather than inventing a format. The
// table is built as BYTES and turned into a pinScan by the real matcher: a
// hand-built pinScan would skip reachMatchArgvRows, and the match-uncapped /
// store-capped asymmetry the truncation pair rests on exists only inside that
// function.
//
// Exactly four well-formed rows and no malformed ones, which fixes RowsScanned at
// 4 against a MatchCount of 3:
//
//  1. 200 — claude, carrying tdnClaudeNeedle, NO FIFO path, under the cap, in its
//     own process group.
//  2. 300 — the `zsh -c` wrapper, argv LONGER than reachMaxCommandBytes with the
//     FIFO path occurring only PAST the cap and nowhere in the surviving prefix.
//  3. 400 — the `cat`, in the SAME group as the wrapper, FIFO path within the cap.
//  4. 500 — an unrelated daemon carrying neither needle. It never enters Matches
//     and constrains nothing downstream; it exists only to keep RowsScanned and
//     MatchCount apart.
//
// The wrapper's padding repeat count is DERIVED from reachMaxCommandBytes and
// never from a literal 512, so the fixture tracks the constant if it ever moves.
func finLivePinTable() []byte {
	const head = "/bin/zsh -c "
	tail := " cat " + finLivePinFIFOPath

	var pad strings.Builder
	for len(head)+pad.Len() < reachMaxCommandBytes {
		pad.WriteString(finLivePinPadToken)
	}
	// head+pad already fills the cap, so the FIFO path's first occurrence starts
	// past it and command[:reachMaxCommandBytes] cannot contain it.
	wrapper := head + pad.String() + tail

	rows := []string{
		"  200   100   200 " + finLivePinClaudeCommand,
		"  300   200   300 " + wrapper,
		"  400   300   300 cat " + finLivePinFIFOPath,
		"  500     1   500 /usr/sbin/notifyd",
	}
	return []byte("\n" + strings.Join(rows, "\n") + "\n")
}

// finLivePinScan turns the fixture into a pinScan through the real byte-pure
// surface, with BOTH needles and an EMPTY exclusion set — exclusions are the
// driver ticket's, and a nil map here is what fixes the totals at 4 / 3 / 2 / 1.
func finLivePinScan(t *testing.T) pinScan {
	t.Helper()
	scan := pinMatchArgvExcluding(finLivePinTable(),
		[]string{finLivePinFIFOPath, tdnClaudeNeedle}, nil)
	if len(scan.Matches) == 0 {
		t.Fatalf("the synthetic table produced no matched row at all across %d scanned "+
			"row(s), so the fixture itself is broken and nothing below is an assertion "+
			"about the reduction", scan.RowsScanned)
	}
	return scan
}

// --- the traps ------------------------------------------------------------------

// TestFinLivePinReduce drives the reduction end-to-end over the synthetic table:
// the four outputs, one property each.
func TestFinLivePinReduce(t *testing.T) {
	t.Parallel()

	scan := finLivePinScan(t)
	got := finLivePinReduce(scan, finLivePinFIFOPath)

	t.Run("both FIFO rows are counted, in scan order", func(t *testing.T) {
		if got.RowCount != finLivePinWantRows {
			t.Fatalf("reduced row count: got %d, want %d — the wrapper and its forked cat "+
				"both carry the FIFO needle", got.RowCount, finLivePinWantRows)
		}
		wantPIDs := []int{finLivePinWrapperPID, finLivePinCatPID}
		if len(got.Rows) != len(wantPIDs) {
			t.Fatalf("matched rows: got %d, want %d", len(got.Rows), len(wantPIDs))
		}
		for i, want := range wantPIDs {
			if got.Rows[i].PID != want {
				t.Errorf("matched row %d: got pid %d, want pid %d — rows are handed on in "+
					"scan order with no dedup and no sort", i, got.Rows[i].PID, want)
			}
		}
		if got.RowCount != len(got.Rows) {
			t.Errorf("row count %d disagrees with len(Rows) %d — RowCount has exactly one "+
				"producer and it is len(Rows)", got.RowCount, len(got.Rows))
		}
	})

	// The pair that makes AC1's membership rule red rather than decorative. An
	// implementation that re-scans .Command counts 1 and fails the first half;
	// without the second half the property goes vacuous the day the fixture row
	// drops under the cap. Neither message prints a Command value.
	t.Run("an over-cap row is counted although its retained command lost the path", func(t *testing.T) {
		var row reachProc
		found := false
		for _, r := range got.Rows {
			if r.PID == finLivePinWrapperPID {
				row, found = r, true
				break
			}
		}
		if !found {
			t.Fatalf("pid %d is absent from the matched rows although its argv carries the "+
				"FIFO path past byte %d; membership is decided by reachMatchedNeedle over "+
				"the recorded needle list, never by re-scanning the capped Command",
				finLivePinWrapperPID, reachMaxCommandBytes)
		}
		if strings.Contains(row.Command, finLivePinFIFOPath) {
			t.Fatalf("pid %d's retained command still contains the FIFO needle, so the "+
				"fixture row no longer exceeds the %d-byte cap and the truncation property "+
				"above is vacuous; lengthen the wrapper row's padding",
				finLivePinWrapperPID, reachMaxCommandBytes)
		}
	})

	t.Run("the process-group projection is raw, duplicates intact", func(t *testing.T) {
		if len(got.PGIDs) != finLivePinWantRows {
			t.Fatalf("projected pgids: got %d entries, want %d — one per matched row, "+
				"unsorted and undeduped; a deduping reduction yields 1 here and duplicates "+
				"finAttributeFanOut's own dedupe while destroying the raw evidence",
				len(got.PGIDs), finLivePinWantRows)
		}
		// Compared POSITIONALLY, never sorted-then-compared: the point is that both
		// entries survived, which a sort would hide.
		if got.PGIDs[0] != finLivePinHeldPGID || got.PGIDs[1] != finLivePinHeldPGID {
			t.Errorf("projected pgids: got [%d %d], want [%d %d] — the wrapper and its cat "+
				"share one detached group", got.PGIDs[0], got.PGIDs[1],
				finLivePinHeldPGID, finLivePinHeldPGID)
		}
	})

	t.Run("claude is in neither the rows nor the projection", func(t *testing.T) {
		for _, pgid := range got.PGIDs {
			if pgid == finLivePinClaudePGID {
				t.Errorf("claude's process group %d appears in the projection; only rows "+
					"carrying the FIFO needle belong there, and claude's row carries only "+
					"the claude needle", finLivePinClaudePGID)
			}
		}
		for _, row := range got.Rows {
			if row.PID == finLivePinClaudePID {
				t.Errorf("claude's pid %d is counted as a FIFO-matched row; that is the "+
					"scan.MatchCount fill wearing the reduction's name",
					finLivePinClaudePID)
			}
		}
	})

	// Catches tdnClaudeCommand being run over the FIFO-filtered rows instead of
	// over the scan: claude's row carries only the claude needle, so the filtered
	// set has zero hits and the n != 1 arm returns "" on every healthy run.
	t.Run("claude's argv comes back from the same scan", func(t *testing.T) {
		if got.ClaudeCommand == "" {
			t.Fatalf("claude's argv came back empty although exactly one fixture row "+
				"carries %s; tdnClaudeCommand must run over the scan, not over the "+
				"FIFO-filtered rows", tdnClaudeNeedle)
		}
		if got.ClaudeCommand != finLivePinClaudeCommand {
			t.Error("claude's argv is not the fixture's claude row (compared as opaque " +
				"bytes; neither operand is printed)")
		}
	})

	t.Run("the zero scan reduces to the zero reading", func(t *testing.T) {
		zero := finLivePinReduce(pinScan{}, finLivePinFIFOPath)
		if zero.Rows != nil || zero.PGIDs != nil || zero.RowCount != 0 || zero.ClaudeCommand != "" {
			t.Errorf("the zero scan reduced to rows=%d pgids=%d count=%d claude-argv-empty=%t; "+
				"want an inert zero reading — the scan's own error has its own named arm "+
				"above the count arm and this must invent nothing", len(zero.Rows),
				len(zero.PGIDs), zero.RowCount, zero.ClaudeCommand == "")
		}
	})

	t.Run("an empty fifo path matches nothing rather than everything", func(t *testing.T) {
		if empty := finLivePinReduce(scan, ""); empty.RowCount != 0 {
			t.Errorf("an empty FIFO path matched %d row(s); reachMatchArgvRows never "+
				"records an empty needle, so no row's needle list can contain one",
				empty.RowCount)
		}
	})
}

// TestFinLivePinCountIsNeitherWrongCandidate pins all three candidate values off
// the SAME scan and asserts they are pairwise distinct.
//
// Both wrong fills are integers of the right type in the right frame, and neither
// is caught by anything shipped. Pinning them as values rather than prohibiting
// them in prose is what makes a regression to either one red — and what makes a
// later edit that COLLAPSES them fail here instead of silently disarming the
// trap: dropping the fixture's claude row would take MatchCount to 2 and this
// test would say so.
func TestFinLivePinCountIsNeitherWrongCandidate(t *testing.T) {
	t.Parallel()

	// The fixture's three totals, each named for what it would cost.
	const (
		// wantRowsScanned keeps the scanned total distinct from the match count;
		// the unrelated daemon is the whole reason they differ.
		wantRowsScanned = 4
		// wantScanMatchCount is WRONG FILL 1: one scan carries both needles, so
		// claude's own row is in it.
		wantScanMatchCount = 3
		// wantDistinctGroups is WRONG FILL 2: claude isolates the whole Bash
		// command into one detached group.
		wantDistinctGroups = 1
	)
	const cost = "stage-pin-count-unexpected on a correctly staged run, with no other " +
		"symptom, at one live claude turn per attempt"

	scan := finLivePinScan(t)
	got := finLivePinReduce(scan, finLivePinFIFOPath)

	// The dedupe is computed HERE and deliberately not in the reduction: the
	// projection is handed on raw so finAttributeFanOut can be the one place that
	// reduces it to a set.
	seen := make(map[int]bool, len(got.PGIDs))
	for _, pgid := range got.PGIDs {
		seen[pgid] = true
	}
	gotDistinctGroups := len(seen)

	if scan.RowsScanned != wantRowsScanned {
		t.Errorf("rows scanned: got %d, want %d — the unrelated daemon is what keeps this "+
			"total apart from the match count", scan.RowsScanned, wantRowsScanned)
	}
	if scan.MatchCount != wantScanMatchCount {
		t.Errorf("scan.MatchCount: got %d, want %d. This is WRONG FILL 1 pinned as a "+
			"value: filling PinMatchCount from it costs %s",
			scan.MatchCount, wantScanMatchCount, cost)
	}
	if gotDistinctGroups != wantDistinctGroups {
		t.Errorf("distinct process groups over the FIFO rows: got %d, want %d. This is "+
			"WRONG FILL 2 pinned as a value: filling PinMatchCount from it costs %s",
			gotDistinctGroups, wantDistinctGroups, cost)
	}
	if got.RowCount != finLivePinWantRows {
		t.Errorf("reduced row count: got %d, want %d — the one population "+
			"finOutcomeStagingGate's count arm expects", got.RowCount, finLivePinWantRows)
	}

	// Pairwise distinctness over the OBSERVED values. Without this, a fixture edit
	// that made two candidates coincide would leave the two assertions above
	// passing while the trap no longer discriminates between the fills at all.
	if got.RowCount == scan.MatchCount {
		t.Errorf("the correct count and scan.MatchCount are both %d, so this trap no "+
			"longer discriminates between them; the fixture must keep a claude row that "+
			"carries the claude needle and NOT the FIFO needle", got.RowCount)
	}
	if got.RowCount == gotDistinctGroups {
		t.Errorf("the correct count and the distinct-group count are both %d, so this "+
			"trap no longer discriminates between them; the fixture's two FIFO rows must "+
			"share one process group", got.RowCount)
	}
	if scan.MatchCount == gotDistinctGroups {
		t.Errorf("both wrong candidates are %d, so a fill from either is indistinguishable "+
			"here", scan.MatchCount)
	}
}
