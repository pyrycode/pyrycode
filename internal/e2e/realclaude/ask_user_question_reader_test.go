//go:build e2e_realclaude

package realclaude

// #1939 — the read half of the AskUserQuestion capture family: a deterministic,
// credential-free pass over whatever the family glob matches under testdata/,
// asserting that each match decodes, is shaped the way #1951's and #1950's checks
// say an AskUserQuestion call is shaped, and carries no value of a fixed deny
// class.
//
// # Why a second check, and why it is made of different fabric
//
// ask_user_question_capture_test.go drives a real claude and writes the artifact.
// A live run cannot prove on its own that the artifact reached the REPOSITORY: an
// agent run happens in a worktree that is discarded when the run ends, so a probe
// can write its file, pass, spend real tokens and land nothing. On 2026-08-25
// #1763's gate ran green and committed zero of the three artifacts its criteria
// asked for, blocking the ticket that reads them until the capture was re-run by
// hand and committed.
//
// A retry of the same stochastic, credentialed instrument cannot close that. This
// file is the other fabric: it needs no claude binary and no credential, it
// settles from committed bytes alone, and it reddens when the artifact is absent,
// unreadable, undecodable, reshaped by a claude release, or leaky. What it cannot
// see is an artifact that was never committed at all under a name this glob
// matches — that is a red here, which is the whole point, but it is a red the live
// run's own author has to act on.
//
// # It restates neither the shape contract nor the deny-scan
//
// The shape assertion is requireAskQuestionShape over askQuestionShapeFindings,
// and this file CALLS it. Two copies of a shape contract drift, which is why that
// assertion was built once against the tool's documented shape before any bytes
// arrived. Read its file header for the three limits binding all eight checks —
// in particular that the multi-select check is PRESENCE, NOT TRUTH, which is what
// makes a dropped key redden here while a well-formed false passes.
//
// The deny-scan is dropcapFixedNeedles, the version-independent half of
// dropped_line_capture_test.go's net, whose own doc states the property this file
// depends on: it needs no knowledge of the run that produced a record, which is
// what lets an offline validation re-scan a committed capture forever.
//
// # What it scans, and the reuse that looks obvious and is wrong
//
// It scans the BYTES os.ReadFile returned — not a re-marshal of the decoded
// record, and therefore NOT through scanAskQuestionFixture. That step marshals
// askQuestionFixtureRecord and scans the result, so any byte in the committed file
// the record does not carry — an unknown key, a key a claude release renamed —
// is silently absent from what it sees, and that class is precisely what this
// reader exists to catch. It also t.Fatalf's from inside itself, so it cannot be
// composed with the ordering below. The name is banned in this file's
// finOfflineExecBans entry so that the weaker scan cannot arrive as a
// plausible-looking reuse.
//
// The scanner is built inline as dropcapScanner{needles: dropcapFixedNeedles()},
// the spelling both offline callers in ask_user_question_writer_test.go use, and
// NEVER newDropcapScanner: that constructor reads os.Getenv twice and realHome, so
// it would make the verdict depend on whose machine ran the test and would put two
// live credentials in a struct. Both names are banned here for that reason.
//
// A clean scan over a clean capture is the expected result whether the scan is
// wired correctly or scanning nothing at all, so the verdict carries two vacuity
// controls of its own — see the subtest below. Neither exists elsewhere:
// TestAskQuestionFixture_ScanRefusesAPlantedValue proves the WRITER's call site
// fires, not this one.
//
// # Message discipline, which is the load-bearing rule of this file
//
// No message here prints the file's bytes, an excerpt of them, a byte offset, a
// needle, the dropcapScanner or the record. The scan's whole reason for existing
// is that a committed capture might carry a credential, so a message quoting the
// offending bytes would move that value into a run log this pipeline salvages and
// invert the control. Class names, counts, base names and the glob pattern are
// repo content and are safe. requireAskQuestionShape already holds this line and
// its doc says why; the call sites here match it.
//
// A file that is both leaky and misshapen reports the leak alone, because the scan
// fatals first. That is correct: it must be redacted and re-captured either way,
// and the leak is the finding with the more urgent remedy.
//
// # Offline, and it reads the committed fixtures on purpose
//
// This file reaches no live claude, no daemon, no subprocess, no credential and no
// environment. It is the second offline file in this package with a legitimate
// reason to READ testdata/, so its finOfflineExecBans entry — composed from
// initialize_control_compare_test.go's rather than copied from a sibling in this
// family — leaves filepath.Glob and os.ReadFile available and keeps every write
// name banned. TestFinOfflineFilesReachNoExecHelper enforces that over this file's
// AST rather than over this paragraph: it parses without parser.ParseComments, so
// the check cannot answer itself out of the sentence stating it.
//
//	env -u ANTHROPIC_API_KEY -u CLAUDE_CODE_OAUTH_TOKEN \
//	  go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestAskQuestionReader_|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// Both must report PASS — not SKIP, not "no tests to run" — on a machine with no
// claude and no credentials. Read the count of tests that executed, never the exit
// code: this package is behind the e2e_realclaude tag, `make check` never compiles
// it, and the suite exits 0 both on a build failure and on a full credentials
// skip. `make preship` is the gate that proves the package builds.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// --- discovery ------------------------------------------------------------------

// askQuestionFixtureGlob sweeps the committed AskUserQuestion capture family, and
// it is the fifth family glob in this package — the first this family owns.
//
// THE testdata/ PREFIX IS INCLUDED, matching fixtureGlob, dropcapFixtureGlob and
// initControlArmFixtureGlob and unlike setModeFamilyGlob which names base names:
// `go test` runs in the package source directory, so a relative pattern resolves
// under it with no packageDir call. That is what keeps packageDir bannable in this
// file's finOfflineExecBans entry.
//
// THE HEAD IS askQuestionFixtureName's LITERAL PREFIX. That literal is the part of
// a minted name no input can reach, which is what makes every name that namer
// mints match this glob and makes this glob unable to sweep any of the four
// foreign families committed in the same directory.
//
// NO SECOND `_`, unlike fixtureGlob and initControlArmFixtureGlob. Those families
// carry a mode and an arm dimension respectively; askQuestionFixtureName mints
// exactly one component after the version slug, so a literal `_` here would match
// nothing this family ever writes.
//
// A GLOB RATHER THAN askQuestionFixtureName BY EXACT NAME, deliberately.
// Addressing by name needs a version token this file cannot learn from a live
// claude — captureClaudeVersion is banned for it — so it would hard-code one, and
// a re-capture at a new version would demand a code edit before the new artifact
// was validated at all. initControlArmFixtureGlob's doc argues this at length and
// the argument carries over unchanged.
//
// This constant must NOT be added to the family-glob table in
// TestAskQuestionFixtureName_AvoidsCommittedFamiliesAndStaysContained. That table
// holds globs FOREIGN to this family and asserts no minted name matches one; every
// name askQuestionFixtureName mints matches this glob by design, so adding it
// would be red against correct code. initControlArmFixtureGlob's doc states the
// same rule for its own family, and the "four committed families live there"
// claims beside that table stay true — they enumerate foreign globs, and this one
// is this family's own.
const askQuestionFixtureGlob = "testdata/ask_user_question_v*.json"

// TestAskQuestionReader_CommittedCapturesAreWellShapedAndScanClean is #1939 whole:
// every file the family glob matches reads, scans clean, decodes and satisfies the
// shape assertion, and a glob matching nothing fails the run.
//
// ONE SUBTEST PER MATCH rather than initControlDiscoverArms' accumulate-then-one-
// t.Fatalf shape. The shape assertion this ticket reuses is already a fatal
// wrapper, so an accumulator would force a second call path around it; per-file
// subtests get the same "one run names every broken fixture" property for free,
// since a t.Fatalf inside one subtest fails that file alone.
//
// THE ORDER OF THE PER-FILE STEPS IS LOAD-BEARING IN ONE PLACE: the decode fails
// the file on its own and the shape assertion runs only after it. A zero-valued
// record reaching askQuestionShapeFindings reports tool_name and NOTHING ELSE,
// because the decode guard inside that function returns early on the empty
// ToolInput — so "one finding, nothing else missing" would read as all-clear on
// garbage. That guard is askQuestionCheckToolInputDecodes and belongs to
// rec.ToolInput one level down; it is not this file's decode branch, and the two
// must not be folded.
//
// json.Unmarshal PLAIN, never with DisallowUnknownFields. A claude release ADDING
// a field is not a defect this reader owns, and the added bytes are scanned anyway
// because the scan reads the file rather than the record.
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO, each considered and declined because no
// criterion asks for it and each is what turned the sibling reader into a 562-line
// file: no version agreement across matches (a second committed capture is a
// healthy event, and every match is validated independently), no name↔record
// binding (#1944's lock already pins the namer), no cross-capture comparison, and
// no widening of the scan beyond this family — five captures outside it carry a
// /Users/ occurrence today, so a widened scan would redden on files this ticket
// does not own.
func TestAskQuestionReader_CommittedCapturesAreWellShapedAndScanClean(t *testing.T) {
	t.Parallel()

	matches, err := filepath.Glob(askQuestionFixtureGlob)
	if err != nil {
		t.Fatalf("#1939: glob %s: %v; a malformed pattern constant makes every claim in this "+
			"file meaningless", askQuestionFixtureGlob, err)
	}
	if len(matches) == 0 {
		t.Fatalf("#1939: no fixtures matched %s; a deleted fixture set — or one a live run wrote "+
			"into a worktree and never committed — must be loud rather than pass vacuously over "+
			"an empty match set", askQuestionFixtureGlob)
	}

	// Built ONCE in the parent and shared by every subtest, which is safe for the
	// reason ask_user_question_writer_test.go's own sharing states: a dropcapScanner
	// is append-only during construction and read-only afterwards, and scan is a
	// value receiver that allocates its own results. Written inline rather than
	// behind a local helper, the spelling both offline callers in that file use, so
	// that "this reads no environment" stays visible at the site this file's
	// finOfflineExecBans entry protects.
	scanner := dropcapScanner{needles: dropcapFixedNeedles()}

	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			base := filepath.Base(path)

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("#1939: read %s: %v; a file this glob matched and the process cannot "+
					"read must fail the run rather than be skipped past, which would leave the "+
					"capture unvalidated under a green gate", base, err)
			}

			// THE SCAN RUNS BEFORE THE DECODE, over raw and never over a re-marshal:
			// see this file's header. Its two vacuity controls run first, because a
			// verdict of "no hits" from an instrument that searched for nothing is the
			// failure mode this whole file would otherwise have.
			hits, notApplied := scanner.scan(raw)

			// Every fixed needle is exempt from dropcapMinNeedle BY CONSTRUCTION, so a
			// reported class means one was marked dynamic — its class is then skipped
			// silently and the verdict below goes green-and-vacuous.
			if len(notApplied) != 0 {
				t.Fatalf("#1939: the offline scanner reports %d class(es) NOT APPLIED %v while "+
					"scanning %s; every fixed needle is exempt from the dropcapMinNeedle minimum "+
					"by construction, so a skipped one means a fixed needle was marked dynamic "+
					"and the verdict below proves nothing", len(notApplied), notApplied, base)
			}

			// The positive control, over the SAME buffer and the same scanner: the
			// committed capture is clean, so "no hits" is expected whether this scan
			// is wired to raw or to nothing at all, and a required-negative paired
			// with a required-positive over one buffer is what a subtest scanning the
			// wrong variable cannot satisfy.
			//
			// A FRESH []byte, NEVER append(raw, …). os.ReadFile can return a slice
			// with spare capacity, and appending into it would mutate the very bytes
			// the clean scan already read and the decode is about to read.
			//
			// askQuestionPlantedPath is reused rather than re-minted: it is already
			// constrained to exactly one armed class, is JSON-string-safe, and carries
			// none of '<', '>' or '&'.
			if planted, _ := scanner.scan([]byte(string(raw) + askQuestionPlantedPath)); !dropcapContains(planted, dropcapDenyUsers) {
				t.Fatalf("#1939: a planted value of class %q appended to %s was NOT reported by "+
					"the scan, so the clean verdict on that file is green-and-vacuous rather "+
					"than evidence: the scanner is searching for nothing, or this subtest is "+
					"scanning something other than the bytes it read",
					dropcapDenyUsers, base)
			}

			if len(hits) != 0 {
				t.Fatalf("#1939: the deny-scan found %d denied class(es) %v in the committed "+
					"capture %s. Redact the named class at the CAPTURE site and re-capture; a "+
					"committed capture is permanent, and #1688's needed three follow-up tickets "+
					"to redact what it had already swallowed. The offending value is deliberately "+
					"not printed, and neither is its offset: putting either in a run log this "+
					"pipeline salvages is exactly the exposure this scan exists to prevent",
					len(hits), hits, base)
			}

			var rec askQuestionFixtureRecord
			if err := json.Unmarshal(raw, &rec); err != nil {
				t.Fatalf("#1939: decode %s through askQuestionFixtureRecord: %v; a file that "+
					"does not decode fails HERE rather than reaching the shape assertion as a "+
					"zero-valued record, where one finding and nothing else missing would read "+
					"as all-clear on garbage", base, err)
			}

			// Called, never restated. It is a t.Fatalf wrapper and needs the test
			// goroutine, which this is.
			requireAskQuestionShape(t, &rec)
		})
	}
}
