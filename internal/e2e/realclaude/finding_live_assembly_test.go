//go:build e2e_realclaude

package realclaude

// The join: the one place the run's transcript reading and the rig's own facts
// meet, filling all eight finOutcomeStaging fields (finding_staging_gate_test.go:158)
// and returning finOutcomeStagingGate's decision (:262) as returned.
//
// This file reaches no verdict about pyry and takes no measurement. It ships the
// assembly and one offline trap on it: no live run, no pyry spawn, no real
// claude, no ps exec, no FIFO, no credentials, no env gate, no t.Skip, no
// goroutine. It DOES touch the filesystem — see § WithWorktree is the
// containment boundary, which is the one half of the blocker's prohibition list
// this file must not inherit.
//
//	go test -race -tags e2e_realclaude -run '^TestFinLiveAssemble' -v ./internal/e2e/realclaude/
//
// # Three from the transcript, five from the rig
//
// finTranscriptReading (finding_staging_fill_test.go:94) is the three
// transcript-side fields as a type, and its doc states why: the composition
// returns a value from which the other five are unreachable, so "the fill neither
// reads nor invents them" is structural rather than asserted.
// finLiveAssembleFacts below is its mirror image, and 3 + 5 = 8 keeps the
// partition visible from both sides.
//
// The caller's five arrive as a STRUCT rather than as five parameters. Flat, the
// assembly would take ten arguments including three adjacent strings, two
// adjacent bools and two adjacent ints; a call site transposing rendezvousDone
// and pinScanErrored compiles and reports stage-rendezvous-incomplete about a
// correctly staged run — a silent wrong verdict discovered on a burned turn,
// which is the failure class this whole file family exists to move offline.
//
// It is deliberately NOT a partial finOutcomeStaging (the shape
// finTranscriptStagedCaller returns, finding_staging_fill_test.go:355): that
// would hand the assembly a value on which BashIssued, IssuedCommand and
// TriggerFired are settable, and "the three transcript fields come from the
// transcript" would stop being structural and become a convention.
//
// # The drift hazard, closed structurally rather than by care
//
// The staged command has TWO consumers and they must be the same bytes:
//
//   - finOutcomeStagingGate's identity arm compares StagedCommand against
//     IssuedCommand (finding_staging_gate_test.go:299).
//   - finTranscriptFill only reads the trigger result when call.Command == staged
//     (finding_staging_fill_test.go:260).
//
// Arriving as two independent parameters they can drift, and drift there is
// doubly silent: the identity arm reports stage-command-not-staged AND
// TriggerFired is zeroed — one defect, two failure arms, both discovered on a
// burned live turn. The blocker records the same hazard at
// finding_live_staging_test.go:29-32. The assembly closes it by taking exactly
// ONE value for the staged string, so there is no second value to disagree with.
//
// # What this file does NOT prove, because it is already proven
//
// #1304's TestFinTranscriptFill (finding_staging_fill_test.go:541) already ships
// the transcript → fill → gate route and reaches the pass-through, with its
// three-field reading mapping, its decoy row and its caller-side-outcomes-absent
// sweep. #1284's finOutcomeGateCases (finding_staging_gate_test.go:411) already
// ships the count mapping — 2/2 → ready, 1/2 → unexpected, 0/0 → unexpected.
// Re-asserting either would be a second copy under a new prefix.
//
// So NO ASSERTION HERE GOES TO finOutcomeStagingGate DIRECTLY. Every row's
// expectation is read from the assembly's return value, and what is proven is
// that an assembly hands the caller's counts to the gate UNALTERED — a statement
// about the assembly, not about the gate.
//
// Building one staged transcript is not what "do not duplicate
// TestFinTranscriptFill" forbids; reproducing its assertions is. This test
// asserts gate values only.
//
// # Two mis-assemblies survive every row, and are held by review
//
// Stated rather than papered over, so a later reader does not "discover" the gap
// and add the duplicate rows AC2 forbids:
//
//   - A SWAP of the two counts inside the composite literal. The gate's decision
//     is (m != w || w < 1) ? unexpected : ready. When m == w a swap is the
//     identity; when m != w both orders take the same arm. No input distinguishes
//     a swap BY OUTCOME VALUE — only the Detail's two interpolated numbers differ,
//     and asserting the gate's prose here would be asserting another file's
//     Detail. Held by the name-for-name rule on the literal, and by review.
//   - HARDCODING the three transcript fields at their staged values. All four rows
//     use a correctly-staged transcript, so a hardcode is green on all of them.
//     Catching it needs a no-Bash-call or wrong-command row, which is exactly
//     TestFinTranscriptFill's rows 2 and 3 — the assertions AC2 forbids
//     reproducing. Held by the no-literal-on-any-right-hand-side rule and by
//     review; the fill's own route to those outcomes is proven at
//     finding_staging_fill_test.go:577-587.
//
// # This file execs nothing, and the check is the symbol list
//
// An `exec.` grep reads clean here by construction — the import set is testing
// and time — so it proves nothing. The rule is the symbols. FORBIDDEN in this
// file, each because it execs, spawns, blocks or skips INSIDE a helper where no
// grep of this file would see it:
//
//   - probeProcessSnapshot, pinScanArgv, tdnScan — each execs `ps` internally.
//     There is no ps flag to get wrong here because there is no ps: no -E, no
//     -Eww, no BSD `eww`. Those flags dump CLAUDE_CODE_OAUTH_TOKEN and
//     ANTHROPIC_API_KEY.
//   - spawnProbePyry, holdProbeFIFO — spawn pyry, create a real FIFO. #1340's
//     job, explicitly out of scope here.
//   - WithWorktreeAuthenticated (fixtures.go:96) — it t.Skipf's when neither
//     ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN is set (:100-107), AND A SKIP
//     EXITS 0. Reaching for it would silently convert an offline test into one
//     that never runs on a credential-free machine and still reports green.
//   - os.Getenv, os.Environ, os.Setenv — no DIRECT environment call is made here.
//     That is not the same as "no environment is read"; see the next section.
//   - finTranscriptStagedCaller (finding_staging_fill_test.go:355) — the fixture
//     to contrast against, never to call. Its PinMatchCount: 1, PinWantCount: 1
//     (:360-361) is exactly the poison the assembly must not inherit.
//   - finOutcomeStagedBase, finOutcomeGateCases (finding_staging_gate_test.go:398,
//     :411) — gate-side fixtures; calling either makes this a copy of the gate test.
//   - finLivePinReduce, finLivePinWantRows (finding_live_pin_test.go:202, :140) IN
//     THE ASSEMBLY'S BODY. Both are the driver's to call. finLivePinWantRows is
//     permitted below as a ROW VALUE — the rule is scoped by layer, not by file.
//
// # WithWorktree is the containment boundary — REQUIRED here, not forbidden
//
// The blocker's header forbids WithWorktree and t.TempDir()
// (finding_live_staging_test.go:46-50) because it declares constants and touches
// no filesystem. This file must write a transcript and read it back through the
// fill, and BOTH ends of that I/O resolve HOME:
//
//   - write: writeFixtureLines (fixtures_test.go:553) → os.UserHomeDir() →
//     tuidriver.SessionJSONLPath(home, workdir, sessionID)
//   - read: finTranscriptFill → ReadJSONL (fixtures.go:148) →
//     resolveAndOpenJSONL → os.UserHomeDir() (:397)
//
// WithWorktree's t.Setenv("HOME", t.TempDir()) (fixtures.go:59) is what makes
// both resolve inside this test's own temp dir. Omit it and writeFixtureLines
// writes a synthetic transcript into the OPERATOR'S REAL ~/.claude/projects/…
// tree at testSessionID — a write into live session storage, from a test still
// reporting green. It is called in the parent, before the first
// writeFixtureLines, and the transcript path is never constructed by hand.
//
// The t.Setenv inside it is also why NO SUBTEST HERE MAY CALL t.Parallel(). Go's
// runtime refuses that pairing, so the constraint is deterministic rather than
// advisory — the note exists so nobody adds the call and then removes
// WithWorktree to make it compile.
//
// # Captured bytes
//
// The staged and issued commands cross as captured strings because the shipped
// gate needs them as inputs and reduces them itself. This file writes none of
// them to an artifact, logs none of them, and formats NO Detail at all, so
// trailDetail's reachMaxCommandBytes cap (background_reach_probe_test.go:945)
// never applies here. No matched-row reachProc.Command value crosses this file:
// the pin reduction is #1338's and is not called. Should a later edit bring one
// in, it crosses as the CAPPED Command the shipped matcher already produces —
// do not re-read an uncapped argv to "repair" a truncation, the cap is the
// discipline and not a defect.
//
// Two sinks, two rules, deliberately not conflated. A published Detail may never
// carry either command IN ANY FORM, including a length or a prefix
// (finding_staging_gate_test.go:192-197); this file formats none, so that rule
// holds structurally. A test t.Errorf is not a published record, and its house
// form is LENGTHS ONLY (finding_staging_fill_test.go:556-562) — the two counts
// and the two outcome values are named freely below, and no command is printed
// in any form. If ever unsure which sink applies, print neither: the one
// resolution that is always wrong is printing the command itself.
//
// NO JSON TAGS on finLiveAssembleFacts, the rule finTranscriptReading
// (finding_staging_fill_test.go:92-93) and finOutcomeStaging
// (finding_staging_gate_test.go:141-157) both state at themselves: IssuedCommand
// is verbatim model output and StagedCommand embeds a t.TempDir()-derived path on
// a live run, so a tag is the first step toward publishing either into a public
// issue.
//
// The published-Detail obligation is discharged by
// TestFinOutcomeResultCarriesNoCapturedBytes (finding_staging_gate_test.go:705)
// rather than by a second sweep here: it plants a needle into BOTH command
// operands, sweeps every finOutcomeGateCases row, asserts the planted row still
// reaches its original arm, pins the per-row Detail headroom so a leak cannot be
// truncated into a false green, scans the marshalled result, and rejects any
// command/args/comm/argv-shaped key a future field might add. Because the
// assembly returns the gate's result unchanged, every (record → result) pair this
// file can produce is one that sweep already covers.
//
// # Not env-gated, and must not become so
//
// Nothing here needs a Claude login. TestMain (fixtures_test.go:348) branches
// only on GO_TEST_HELPER_PROCESS and otherwise runs m.Run(), so a regression here
// is red under `make e2e-realclaude` on a credential-free machine. Note that
// `go vet` and `staticcheck` in `make check` run WITHOUT -tags e2e_realclaude, so
// neither analyses this file; `make e2e-realclaude` is the gate.

import (
	"testing"
	"time"
)

// --- the caller's five ------------------------------------------------------------

// finLiveAssembleFacts is the five caller-side fields of finOutcomeStaging, and
// only those five: what the rig knows without reading the run's transcript. It is
// finTranscriptReading's mirror image, and the two together are the eight.
//
// The field names mirror finOutcomeStaging's EXACTLY, which is load-bearing
// rather than cosmetic: the assembly's composite literal is name-for-name, so a
// transposition there reads as a visible mistake rather than a plausible line —
// and a count swap is the one mis-assembly no value-only test can catch.
//
// NO JSON TAGS, the rule finTranscriptReading (finding_staging_fill_test.go:92-93)
// and finOutcomeStaging (finding_staging_gate_test.go:141-157) both state at
// themselves: it carries StagedCommand, which embeds a t.TempDir()-derived path
// on a live run.
type finLiveAssembleFacts struct {
	// StagedCommand is the hold command the rig staged. It is the ONLY source of
	// the staged string in this composition and reaches two consumers from here —
	// the gate's identity arm and finTranscriptFill's selection guard — so the
	// drift between them is closed structurally rather than by care.
	StagedCommand string
	// RendezvousDone comes from the FIFO; PinScanErrored, PinMatchCount and
	// PinWantCount from the during-turn ps scan. All four zero-value into a failure
	// arm or into the gate's PinWantCount < 1 guard — the safe direction.
	RendezvousDone bool
	PinScanErrored bool
	PinMatchCount  int
	PinWantCount   int
}

// --- the assembly -------------------------------------------------------------------

// finLiveAssembleStaging fills all eight finOutcomeStaging fields — three read
// from the run's own transcript, five supplied by the rig — and returns
// finOutcomeStagingGate's decision AS RETURNED: not re-derived, not renamed, not
// cross-checked into a new verdict.
//
// IT IS NOT PURE, and it does not claim to be. finTranscriptFill takes a
// *testing.T, reads the transcript off the filesystem and polls to a deadline, so
// this inherits all three. finOutcomeStagingGate itself IS pure, and that
// contract is the gate's rather than this function's. What is promised instead is
// the prohibition list in this file's header: nothing here spawns, execs, reads
// the environment directly or writes an artifact.
//
// The deadlines are parameters for finTranscriptFill's own stated reason
// (finding_staging_fill_test.go:238-241): both waiters poll to expiry before
// returning empty, so a live caller passes probeToolUseDeadline /
// probeToolResultDeadline and an offline row passes milliseconds for a result
// already on disk.
//
// THREE RULES ON THE COMPOSITE LITERAL, each preventing a named failure:
//
//   - ALL EIGHT KEYS PRESENT. This is "no field is left at its zero value by
//     accident" made auditable: a missing key is a field silently at its zero, and
//     four of the eight zero into failure arms while PinWantCount: 0 zeroes into
//     the gate's guard (finding_staging_gate_test.go:347).
//   - NO LITERAL ON ANY RIGHT-HAND SIDE. Every one is r.X or facts.X and nothing
//     else. This single rule is what forbids finTranscriptStagedCaller's poison
//     PinMatchCount: 1, forbids PinWantCount: finLivePinWantRows (the driver's job
//     — the counts are FORWARDED, neither re-derived nor fixed internally), and
//     forbids BashIssued: true.
//   - NAME-FOR-NAME. PinMatchCount: facts.PinMatchCount, never facts.PinWantCount.
//
// It returns finOutcomeResult and NOT the eight-field record alongside it.
// finOutcomeResult is the one publishable type in this family; handing a caller
// the record too would hand it a value carrying two captured strings for no
// stated need, undoing the asymmetry finOutcomeStaging's own doc establishes
// ("INPUT ONLY — NEVER PUBLISHED", finding_staging_gate_test.go:142). If #1340
// needs more, that is #1340's argument to make.
//
// NO ERROR RETURN AND NO FAILURE ARM OF ITS OWN, matching every gate and
// reduction in this family: an instrument reading is a datum, not a reason to
// abort a turn, and every wrong or missing reading already has a named home among
// the gate's seven outcomes. It adds no t.Fatal and no t.Error. It inherits
// exactly one abort path — ReadJSONL t.Fatalf's on a transcript it cannot open or
// parse (fixtures.go:152, :163) — which is #1304's shipped behaviour, named here
// so a live caller knows this call can abort a turn on an unreadable transcript.
// A MISSING file is not fatal: probeWaitForBashToolUse guards with os.Stat first
// (background_trigger_probe_test.go:767), so it times out to "no Bash call
// issued", which is the safe direction.
func finLiveAssembleStaging(t *testing.T, workdir, sessionID string, facts finLiveAssembleFacts,
	toolUseTimeout, resultTimeout time.Duration) finOutcomeResult {
	t.Helper()
	r := finTranscriptFill(t, workdir, sessionID, facts.StagedCommand, toolUseTimeout, resultTimeout)
	return finOutcomeStagingGate(finOutcomeStaging{
		BashIssued:     r.BashIssued,
		IssuedCommand:  r.IssuedCommand,
		StagedCommand:  facts.StagedCommand,
		TriggerFired:   r.TriggerFired,
		RendezvousDone: facts.RendezvousDone,
		PinScanErrored: facts.PinScanErrored,
		PinMatchCount:  facts.PinMatchCount,
		PinWantCount:   facts.PinWantCount,
	})
}

// --- the contract row's want ----------------------------------------------------------

// finLiveAssembleContractWant is a want NO LIVE DRIVER EMITS — the driver always
// passes finLivePinWantRows. It exists for the one row that catches an assembly
// forwarding the match count while fixing the want internally, which every other
// row lets through.
//
// DERIVED as +1 rather than written as a literal so it can never coincide with
// the driver's want: the derivation IS the guard, making != finLivePinWantRows
// structural and >= 1 free, so no runtime precondition asserting either is added
// on top of a tautology. A later edit that "fixed" the row by pinning it to
// finLivePinWantRows would delete the only check that the want travels at all.
//
// Non-producible by design and stating its reason at itself, following the house
// form at finding_staging_gate_test.go:433-436.
const finLiveAssembleContractWant = finLivePinWantRows + 1

// --- the trap -------------------------------------------------------------------------

// finLiveAssembleCountCase is one pair of caller-side counts and the gate value
// the assembly must return for it. The counts are the ONLY thing that varies:
// every other field of finLiveAssembleFacts is held at its staged value, and all
// four rows read the same correctly-staged transcript.
type finLiveAssembleCountCase struct {
	name                  string
	matchCount, wantCount int
	// The value read from the ASSEMBLY's return, never from a direct gate call.
	want string
}

// TestFinLiveAssembleStagingForwardsTheCounts is AC2: the caller's two counts
// reach the gate unaltered, over one transcript and four rows.
//
// The transcript is written ONCE, IN THE PARENT, from the shipped builders. All
// four rows read it, which makes "the counts are the only thing that varies"
// literal rather than asserted, and means there is no write-during-read to make
// atomic. The staged literal is the blocker's finLiveStageCommand over its
// synthetic fixture path — NOT finOutcomeHoldCommand
// (finding_staging_gate_test.go:375), which is a gate fixture deliberately of the
// wrong shape: its `sh -c … ; exit 0` carries a `;` into a string the model is
// asked to reproduce byte-for-byte against a system prompt that forbids chaining
// (finding_live_staging_test.go:136-139).
//
// One `staged` local feeds both the transcript block and every row's
// facts.StagedCommand, so a mismatch is impossible by construction.
//
// The mis-assemblies each row kills, writing w for finLivePinWantRows and c for
// finLiveAssembleContractWant:
//
//	                                     (w,w)  (w-1,w)  (0,0)  (c,c)
//	inherits the fixture's 1, 1           green   RED     RED    green
//	ignores its inputs, counts at zero    RED     green   green  RED
//	copies the want into the match count  green   RED     green  green
//	forwards the match, fixes the want    green   green   green  RED
//	substitutes its own match == want     green   green   RED    green
//
// Every row is some mutant's sole RED or the live driver's own input; none is
// decorative. Note in particular that (0,0) is NOT the row that catches a
// forgotten fill — (w,w) is.
//
// NO SUBTEST HERE MAY CALL t.Parallel(): WithWorktree calls t.Setenv and Go's
// runtime refuses that pairing.
func TestFinLiveAssembleStagingForwardsTheCounts(t *testing.T) {
	// Before the first write, in the parent: it is what makes both ends of this
	// file's I/O resolve inside this test's own temp dir rather than into the
	// operator's real ~/.claude/projects tree.
	workdir := WithWorktree(t)
	staged := finLiveStageCommand(finLiveStageFixtureFIFOPath)
	writeFixtureLines(t, workdir, testSessionID,
		finTranscriptAssistantLine(t,
			finTranscriptBashBlock(finTranscriptStagedID, staged, false)),
		finTranscriptResultLine(t, finTranscriptStagedID, "bg_staged", "5000"),
	)

	for _, tc := range []finLiveAssembleCountCase{
		{
			// The rig-realistic input — the only pair a live driver actually produces —
			// and the one row that catches an assembly which drops the counts. It is
			// also the only row proving the eight-field composition reaches the
			// pass-through at all.
			name:       "the counts agree at the rig's expectation",
			matchCount: finLivePinWantRows,
			wantCount:  finLivePinWantRows,
			want:       finOutcomeReadyToClassify,
		},
		{
			// w-1 is 1 today, exactly finTranscriptStagedCaller's hardcode, so this is
			// the most direct kill for an assembly that inherited the fixture. The
			// coincidence is deliberate; the row stays a disagreeing pair under any
			// change to finLivePinWantRows either way.
			name:       "the counts disagree",
			matchCount: finLivePinWantRows - 1,
			wantCount:  finLivePinWantRows,
			want:       finOutcomePinCountUnexpected,
		},
		{
			// This row pins the gate's `|| PinWantCount < 1` guard
			// (finding_staging_gate_test.go:347) SURVIVING THE COMPOSITION, which kills
			// an assembly substituting its own count == want test for the gate's
			// decision. It is not the row that catches a forgotten fill.
			name:       "both counts at zero",
			matchCount: 0,
			wantCount:  0,
			want:       finOutcomePinCountUnexpected,
		},
		{
			// The only row that catches an assembly forwarding the match count while
			// fixing the want internally. A deliberate contract row over a want no live
			// driver emits; the constant says so at itself.
			name:       "the counts agree at a want no live driver emits",
			matchCount: finLiveAssembleContractWant,
			wantCount:  finLiveAssembleContractWant,
			want:       finOutcomeReadyToClassify,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := finLiveAssembleStaging(t, workdir, testSessionID, finLiveAssembleFacts{
				StagedCommand:  staged,
				RendezvousDone: true,
				PinScanErrored: false,
				PinMatchCount:  tc.matchCount,
				PinWantCount:   tc.wantCount,
			}, finTranscriptTestDeadline, finTranscriptTestDeadline)

			// The counts and the outcome values are named; the staged command is not
			// printed in any form. The assertion reads the ASSEMBLY's return — a row
			// re-asserting this pair against finOutcomeStagingGate would be a second
			// copy of finOutcomeGateCases.
			if got.Value != tc.want {
				t.Errorf("got %q, want %q for a match count of %d against a want of %d: the "+
					"assembly must forward both counts to the gate unaltered, neither "+
					"re-deriving them nor fixing either one internally",
					got.Value, tc.want, tc.matchCount, tc.wantCount)
			}
		})
	}
}
