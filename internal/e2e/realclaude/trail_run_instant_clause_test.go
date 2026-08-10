//go:build e2e_realclaude

package realclaude

// Every run-classifier statement that forecloses a claim on CERTIFIES-NOTHING
// grounds names the instant it forecloses, so a reader can tell which claims it
// rules out and which it leaves open.
//
// Everything here runs offline: no live claude, no credentials, no daemon, no env
// gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestTrailRun' -v ./internal/e2e/realclaude/
//
// # Why this file exists at all, rather than two blocks inside the file it is about
//
// trail_run_outcome_test.go carries a hundred inbound line-number cites from
// sixteen other files in this package. An assertion added inside
// TestTrailClassifyRun's loop, and the "os" import a source sweep needs, would each
// displace every cite below them — the cascade #1417 met twice in this same file.
// Both tests below are the same coverage they would have been in place, and they
// cost zero displacement here.

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// trailRunCertifiesNothingArms is the four step-1 outcomes whose Detail argues
// from the gate having certified nothing, and which therefore carry
// trailDeclaredFinishInstantClause.
//
// #1446's finding is a member, and it is the only one that is not a void — which
// is precisely why it belongs. It is the one arm in the run space that publishes a
// POSITIVE finding from a gate that certified nothing, so it is the one a reader
// could mistake for an aliveness-at-declared-finish claim. Carrying the clause on
// three voids and omitting it from the single value the misreading is live for
// would invert the point of the clause. Its own Detail says the same thing twice
// over, naming the SIGHTING as the instant its verdict is about; the shared
// constant is the checked half of that, and hand-written prose is not.
//
// The other two arms reached by a gate that certifies nothing are deliberately NOT
// here, and the exclusion is a reading of what each Detail argues rather than an
// oversight:
//
//   - trailGateScanAborted's Detail argues the BYTES WERE UNREADABLE. It forecloses
//     nothing on certification grounds, so a clause about certification would be
//     answering a question that arm never asks.
//   - trailGateOutOfContract's already says "no instant is certified" outright — a
//     statement about CERTIFICATION rather than about a claim, correct as written
//     and one of the four this ticket left alone as the model.
func trailRunCertifiesNothingArms() []string {
	return []string{
		trailOutcomeVoidNoTrailer,
		trailOutcomeVoidPathOwesNoReason,
		trailOutcomeVoidReasonNotOwedByPath,
		trailOutcomeAliveAtSightingByOrdering,
	}
}

// TestTrailRunCertifiesNothingArmsNameTheInstant makes "exactly these four arms
// name the declared-finished instant" checkable rather than a convention the format
// strings happen to follow. Sharing one constant stops the three DRIFTING in how
// they name the instant, but an arm can still omit it from its own format string
// and nothing else in this package would notice.
//
// # Why the check is CONDITIONAL where #1440's is unconditional
//
// trail_sighting_liveness_test.go:579-588 requires trailSightingInstantClause on
// EVERY row, and is right to: every arm of that one predicate is about the sighting
// instant, so the clause is universal there. Here it is four outcomes out of
// fourteen, so an unconditional per-row check would redden the other ten.
//
// The absence half is not filler. It is what makes the claim "only these four"
// rather than "at least these four", and it is the executable half of the
// budget-fired carve-out: trailOutcomeVoidBudgetFired forecloses BOTH instants from
// a gate that CERTIFIED a terminal reason, so a certifies-nothing clause on it
// would be false. Its row goes red here if someone adds one.
func TestTrailRunCertifiesNothingArmsNameTheInstant(t *testing.T) {
	// The premise, first: a duplicate in the list below would silently shrink the
	// carrier set while len() of it did not change.
	carries := make(map[string]bool)
	for _, value := range trailRunCertifiesNothingArms() {
		if carries[value] {
			t.Fatalf("%q is listed twice as a clause-carrying arm, so the set is smaller than "+
				"it reads and one arm goes unchecked", value)
		}
		carries[value] = true
	}

	reached := make(map[string]int)
	for _, tc := range trailRunCases() {
		got := trailClassifyRun(tc.in)
		reached[got.Value]++

		t.Run(tc.name, func(t *testing.T) {
			if got.Value != tc.want {
				t.Fatalf("value: got %q (%s), want %q — every assertion below is about the arm "+
					"this row reaches, and a different arm makes them statements about "+
					"something else", got.Value, got.Detail, tc.want)
			}
			names := strings.Contains(got.Detail, trailDeclaredFinishInstantClause)
			switch {
			case carries[got.Value] && !names:
				t.Errorf("the %s Detail omits the clause, so it forecloses a claim without "+
					"naming the instant it forecloses — and a reader cannot tell whether it "+
					"also rules out an aliveness-at-SIGHTING finding, which it does not: %s",
					got.Value, got.Detail)
			case !carries[got.Value] && names:
				t.Errorf("the %s Detail carries the certifies-nothing clause, which is only "+
					"true of %v. On %s in particular the gate CERTIFIES a terminal reason and "+
					"the reap provably preceded the trailer write, so BOTH instants are "+
					"foreclosed there and this clause would misreport why: %s",
					got.Value, trailRunCertifiesNothingArms(), trailOutcomeVoidBudgetFired,
					got.Detail)
			}
		})
	}

	// The second premise: a table that stopped producing one of the three would
	// make the conditional above vacuously true for it.
	for _, value := range trailRunCertifiesNothingArms() {
		if reached[value] == 0 {
			t.Errorf("no row in trailRunCases() reaches %q, so its clause is unproven rather "+
				"than proven — the check above passed by never running on it", value)
		}
	}
}

// TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly pins the residual: after
// #1443, "aliveness-at-trailer" appears UNQUALIFIED in the run classifier at
// exactly the two budget-fired sites, and both carry the argument that earns the
// exemption.
//
// # The carve-out, in full, because this test is what reddens when someone "fixes"
// one of the two
//
// Every other statement in that file which forecloses a claim on certifies-nothing
// grounds was amended to name the DECLARED-FINISHED instant, because a
// sighting-instant claim survives there (#1440's trailEstablishSighting,
// trail_sighting_liveness_test.go:353). The budget-fired arm argues from the
// opposite premise: its gate CERTIFIES max_turns, and the Terminate hook reaped
// INSIDE the hook (runner.go:492-503) BEFORE the trailer was written. The reap
// provably precedes the trailer, so NEITHER instant can be proved from that path —
// the declared-finish one and the sighting one alike. The phrase is deliberately
// unqualified there, which is why qualifying it would be a regression rather than
// the improvement it looks like. Both sites say so in place, with ", at either
// instant".
//
// # Why this reads the source file rather than inspecting rendered Details
//
// One of the two sites is a COMMENT and the other is a format string. No runtime
// inspection of any record can see both, so reading the source is the only
// mechanism that can pin the set. The needle is ASSEMBLED from parts rather than
// written as one literal: were this test ever moved into the file it sweeps, a
// whole literal would match itself and inflate the count by exactly as many
// occurrences as the sweep spells.
//
// It deliberately asserts NO count of "aliveness-at-declared-finish". A number kept
// by hand beside a set drifts — trailer_admissibility_test.go:1172-1177 is this
// family's own record of a shipped comment saying twenty-nine while the map already
// held thirty-five.
func TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly(t *testing.T) {
	const subject = "trail_run_outcome_test.go"
	source, err := os.ReadFile(subject)
	if err != nil {
		t.Fatalf("reading %s: %v — a sweep that cannot read its subject has measured nothing, "+
			"and a clean report here would be exactly the shape of a check that cannot fail",
			subject, err)
	}

	needle := "aliveness-at-" + "trailer"
	// The budget-fired argument's shared prefix. Both exempt sites carry it on the
	// same line as the phrase, so this pins their IDENTITY where the count below
	// pins the set's SIZE: two occurrences that had drifted to two other statements
	// would satisfy a bare count and fail here.
	const argument = "no attribution on that path could prove"

	var sites []string
	for i, line := range strings.Split(string(source), "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		site := fmt.Sprintf("%s:%d: %s", subject, i+1, strings.TrimSpace(line))
		sites = append(sites, site)
		if !strings.Contains(line, argument) {
			t.Errorf("%s carries %q without the budget-fired argument %q on the same line, so "+
				"it forecloses a claim without naming which instant — the two-way ambiguity "+
				"#1443 removed everywhere except the budget-fired carve-out", site, needle,
				argument)
		}
	}

	if len(sites) != 2 {
		t.Errorf("%s carries %q at %d site(s), want exactly 2 — the budget-fired doc and its "+
			"arm's Detail, which forecloses BOTH instants from a CERTIFIED reason and is the "+
			"one place the phrase is correct unqualified. Found:\n\t%s", subject, needle,
			len(sites), strings.Join(sites, "\n\t"))
	}
}
