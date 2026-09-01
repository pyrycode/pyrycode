//go:build e2e_realclaude

package realclaude

// #1944 — the name half of the AskUserQuestion capture: the namer that mints the
// fixture filename #1941's writer and #1938's live run will put their bytes
// under, and the deterministic proof that nothing it mints can land in a
// committed fixture family or outside the directory its caller chose.
//
// # What is being fenced off
//
// Nothing in this repo has ever recorded a CALL to AskUserQuestion. All eight
// committed permission_protocol_* captures list the tool in their `system`/`init`
// tools array, unbroken from claude 2.1.143 through 2.1.199, and not one holds a
// tool_use block for it. #1943 built the record those bytes decode into; this
// file mints the name they land under, before the run exists that could trip it.
//
// Two hazards make a name worth a lock rather than an fmt.Sprintf at a call site,
// and both are silent:
//
//  1. testdata/ IS SWEPT BY FOREIGN GLOBS. Four committed families live there,
//     described by fixtureGlob, dropcapFixtureGlob, setModeFamilyGlob and
//     initControlArmFixtureGlob. A capture whose name joined one of them would be
//     swept into a regression test asserting about a run it never made, or
//     overwritten by the next run of the probe that owns the family — either way
//     durable evidence is lost WITH NO RED ANYWHERE.
//
//  2. THE NAME IS JOINED UNDER A DIRECTORY THE NAMER NEVER SEES. The version
//     token comes out of `claude --version`, so a token carrying a separator
//     mints a name that writes somewhere its caller never chose. versionSlug's
//     character class is what closes that, and only a table of
//     separator-bearing tokens pins the namer to it.
//
// The ask_user_question_v prefix closes the first hazard whole. filepath.Match
// anchors a pattern's literal head at position 0, so a name beginning with a head
// no committed family shares cannot match any of the four — their heads are
// permission_protocol_v, dropped_lines_v, set_permission_mode_v and
// initialize_control_v. The prefix is a literal inside askQuestionFixtureName
// that no input can reach, which is what makes the collision impossible rather
// than merely unobserved.
//
// initControlArmFixtureGlob is a required row here and its own doc comment does
// not contradict that: it forbids being added to the two locks in
// initialize_control_names_test.go, where it is the family's OWN glob and every
// arm-carrying name matches it on purpose. Here it is foreign, exactly as the
// other three are, so AC 3's "a capture family already committed under
// testdata/" covers it. #1696's table omits it only because #1764 had not added
// it yet.
//
// # One-directional absence proves nothing, so every pattern carries a control
//
// The four patterns are NOT matched against the same string, which is why
// anchorFixtureName is the single place anchoring happens and why BOTH the
// negative assertions and the controls go through it. That argument is written
// out once, at anchorFixtureName and in inband_bypass_revoke_names_test.go's
// header; it is not restated here. What matters at this call site: flip a row's
// underTestdata and that row's negative assertion goes vacuous AND its control
// reddens, in the same edit.
//
// # Why the token table carries a golden `want` column
//
// The three subtests this file inherits from
// TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained cannot see
// a namer that stops calling versionSlug on a NON-SEPARATOR token: such a namer
// mints ask_user_question_v2.1.239 (Claude Code).json, a perfectly legal single
// component that joins no family. So the token table carries the minted name it
// is contracted to produce, and a fourth subtest reads it. Keeping it as a column
// rather than a second table is deliberate — a row added later cannot reach one
// subtest and miss another.
//
// # Offline, and further: no I/O in either direction
//
// This file reaches no live claude, no daemon, no subprocess, no credential and
// no directory. It must not reach resolveClaudeBin, probeClaudeVersion,
// WithWorktree, WithWorktreeAuthenticated or captureClaudeVersion; nor os.Getenv,
// os.Environ or os.LookupEnv; nor packageDir, any of its wrappers
// setModeFixturePath, writeSetModeFixture and writeFixture, filepath.Glob, or any
// os read or write. That last group is not tidiness: `go test` runs in the
// package source directory, so a RELATIVE os.WriteFile("testdata/…") reaches the
// very committed captures this file exists to protect, without naming packageDir
// at all. The controls below are synthetic literals for the same reason — one
// globbed off the real testdata/ would be reading the directory.
//
// TestFinOfflineFilesReachNoExecHelper enforces all of it over this file's AST
// rather than over this paragraph: it parses without parser.ParseComments, so the
// check CANNOT answer itself out of the header that states it.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestAskQuestionFixtureName_|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// Both must report PASS — not SKIP, not "no tests to run" — on a machine with no
// claude and no credentials. Read the count of tests that executed, never the
// exit code: this package is behind the e2e_realclaude tag, `make check` never
// compiles it, and the suite exits 0 both on a build failure and on a full
// credentials skip.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// --- the namer ----------------------------------------------------------------

// askQuestionFixtureName mints the fixture filename for the AskUserQuestion
// capture. It is a pure function of one input: no directory parameter, no
// *testing.T, no I/O in either direction.
//
// ONE PARAMETER, and the tool name is not the second. askQuestionFullRecord's doc
// comment forward-references "#1944's namer assertion … on the tool-name column",
// which reads as a namer over (versionToken, toolName). That prediction was
// written before this ticket was refined and it is STALE: this capture has no arm
// dimension and the tool name is a constant for the whole family, so
// interpolating it would put a fixed string in the name twice over. What that
// comment was reaching for lands with #1941 — the writer names its file exactly
// what this namer mints.
//
// The ask_user_question_v prefix is a literal here on purpose: it is the part of
// the name no input can reach, and it is the whole reason the minted name cannot
// join a committed fixture family. Do not derive it from the argument. Nor
// shorten it to ask_question_ to match the Go identifier prefix — the file name a
// human reads under testdata/ carries the tool's real name, while the Go
// identifiers stay askQuestion* to match askQuestionFixtureRecord and its
// siblings.
//
// CONTRACT for #1941's writer: the result is always a SINGLE CLEAN PATH
// COMPONENT — it carries no separator and is never "." or "..", for any input.
// That is what makes filepath.Join(dir, name) land in dir at the call site.
// Nothing in the signature says so — it is string in and string out — and the
// containment subtest below is what proves it.
//
// That guarantee is LEXICAL, and it is about the name rather than about the
// filesystem. It says the minted string is one component, so Join cannot walk out
// of dir. It says NOTHING about dir itself: pass a directory that is or contains
// a symlink and the write still resolves wherever that symlink points, and
// nothing here opens with O_NOFOLLOW or an equivalent. Choosing dir stays the
// caller's responsibility, and #1941 must not inherit a guarantee it was never
// given.
//
// Its TOTALITY rests on versionSlug's character class, not on the token table
// below. No separator survives [^a-z0-9._-]+ → _, which is why the property holds
// for every string rather than merely the fifteen sampled. The table's job is to
// catch that coupling breaking. Both halves matter: the table is the tripwire,
// the character class is the guarantee.
func askQuestionFixtureName(versionToken string) string {
	return fmt.Sprintf("ask_user_question_v%s.json", versionSlug(versionToken))
}

// --- the lock -------------------------------------------------------------------

// askQuestionNameRow is one adversarial version token paired with the name
// askQuestionFixtureName is contracted to mint from it.
//
// One table, four subtests: `want` is a column rather than a second table so the
// load-bearing-row argument is written once and a row added later cannot reach
// one subtest and miss another.
//
// Function-local for the reason the sibling locks' pattern tables are: it is used
// by exactly one test, and keeping it here holds this file's package-scope
// surface at two identifiers — askQuestionFixtureName and the test — which is
// what keeps concurrent siblings from colliding with it.
type askQuestionNameRow struct {
	token string
	want  string
}

// TestAskQuestionFixtureName_AvoidsCommittedFamiliesAndStaysContained is #1944
// whole: every token mints exactly the pinned name, no name this namer mints
// joins a committed fixture family, every pattern making that claim can still
// match something, and every minted name stays a plain component directly inside
// whatever directory #1941's writer joins it under.
//
// It settles with no claude binary and no credentials, and it reads nothing off
// disk — the properties belong to the name, not to any call site.
func TestAskQuestionFixtureName_AvoidsCommittedFamiliesAndStaysContained(t *testing.T) {
	t.Parallel()

	// Adversarial version tokens with the name each one is contracted to mint,
	// computed against versionSlug on 2026-09-01. THE `want` VALUES ARE THE
	// CONTRACT: do not re-derive them by running the namer and pasting what it
	// printed, which turns the golden subtest into a tautology.
	//
	// Which rows are load-bearing, measured 2026-09-01 over all fifteen rows:
	//
	//   - Under a namer interpolating its token RAW — equally pure, still carrying
	//     the literal prefix — the golden subtest reddens on `2.1.239 (Claude
	//     Code)`, `../..`, `a/b`, `/abs` and the 64-nine row, and containment
	//     reddens on the three separator rows. The two rows where the golden
	//     subtest is the SOLE red are `2.1.239 (Claude Code)` and the 64-nine row:
	//     without them a namer that never slugs a separator-free token is green
	//     across this whole file. The family globs stay green on all fifteen — the
	//     prefix keeps the name out of every family whether or not the token was
	//     slugged.
	//
	//   - Under a namer deriving its prefix from the argument, the four family
	//     rows redden the family subtest ONE GLOB EACH — permission_protocol_v1_x
	//     hits fixtureGlob, set_permission_mode_v1_x hits setModeFamilyGlob,
	//     dropped_lines_v1 hits dropcapFixtureGlob, initialize_control_v1_x hits
	//     initControlArmFixtureGlob — and no other row reddens any. The mapping
	//     row→family is 1:1, so dropping one row silently drops one family.
	//
	// The family rows carry the family's `v` and a trailing token, and the bare
	// heads #1696's table carries CANNOT stand in for them: #1712 measured that
	// `set_permission_mode` mints a name matching NOTHING, because
	// set_permission_mode_v*_*.json needs a literal `v` after the head and the
	// bare head never supplies it. Three of the four carry a trailing `_x` because
	// their globs need a second `_`; dropped_lines_v1 does not because its does
	// not. Do not "harmonise" these back to bare heads — that silently empties the
	// family subtest of its only mutant.
	rows := []askQuestionNameRow{
		// The plausible current version, already slug-clean: the baseline row. It
		// gives the golden subtest nothing, and is here so the table's first row
		// reads as the ordinary case.
		{token: "2.1.239", want: "ask_user_question_v2.1.239.json"},
		// The raw `claude --version` LINE shape — space, parens, uppercase. Load
		// bearing, and one of the two rows where the golden subtest is the sole red.
		{token: "2.1.239 (Claude Code)", want: "ask_user_question_v2.1.239_claude_code_.json"},
		// The shape #1941 passes if it mints from askQuestionFixtureRecord's
		// ClaudeVersionSlug: slug-clean by construction, so re-slugging it is a
		// fixed point. It documents that fixed point and is 0-red on the golden
		// subtest — do not credit it with catching a mis-slugged token.
		{token: "2.1.239-fixture", want: "ask_user_question_v2.1.239-fixture.json"},
		// Underscores, dots and hyphens all survive the character class.
		{token: "2_1_220", want: "ask_user_question_v2_1_220.json"},
		{token: "2.1.220-beta.1", want: "ask_user_question_v2.1.220-beta.1.json"},
		// The four family-smuggling rows. See the mapping above.
		{token: "permission_protocol_v1_x", want: "ask_user_question_vpermission_protocol_v1_x.json"},
		{token: "set_permission_mode_v1_x", want: "ask_user_question_vset_permission_mode_v1_x.json"},
		{token: "dropped_lines_v1", want: "ask_user_question_vdropped_lines_v1.json"},
		{token: "initialize_control_v1_x", want: "ask_user_question_vinitialize_control_v1_x.json"},
		// The "." / ".." clause. Note what it mints is a perfectly CLEAN component,
		// so this row does not catch a mis-slugged token either.
		{token: "..", want: "ask_user_question_v...json"},
		// The three separator-bearing rows the containment subtest measures. These
		// are the traversal tripwire: drop all three and a namer interpolating its
		// token raw escapes its target directory with nothing red.
		{token: "../..", want: "ask_user_question_v.._...json"},
		{token: "a/b", want: "ask_user_question_va_b.json"},
		{token: "/abs", want: "ask_user_question_v_abs.json"},
		// The empty input.
		{token: "", want: "ask_user_question_v.json"},
		// versionSlug's 32-byte truncation path, and the second row where the golden
		// subtest is the sole red. The want is written as an expression rather than
		// a literal run of digits: a hand-typed run of 32 identical characters is
		// unreviewable.
		{token: strings.Repeat("9", 64), want: "ask_user_question_v" + strings.Repeat("9", 32) + ".json"},
	}

	// poolRevokeNamePattern is #1661's row type and is generic over the family
	// despite being named for #1643's: glob, underTestdata, controls and hazard say
	// nothing about which probe owns the family. Reused rather than duplicated — a
	// parallel type would mean a second anchoring path, and the single
	// anchorFixtureName is the property this file depends on.
	//
	// underTestdata is per-row and is what decides whether a row asserts anything
	// at all. Three of these globs carry a testdata/ prefix and setModeFamilyGlob
	// does not, because that is how their owning tests evaluate them.
	//
	// The controls are SYNTHETIC LITERALS, never a committed filename and never a
	// directory listing. A control lifted off the real directory is red on arrival
	// — permission_protocol_v2.1.158.json is committed right now and does NOT match
	// fixtureGlob, having no second `_` — and one written as a real capture reddens
	// spuriously the day that capture is retaken at a new version, where the cheap
	// repair for a spurious red is to weaken the control. No committed capture
	// carries v0.0.0.
	patterns := []poolRevokeNamePattern{
		{
			glob:          fixtureGlob,
			underTestdata: true,
			controls:      []string{"permission_protocol_v0.0.0_default.json"},
			hazard: "TestRealClaude_PermissionProtocol_RegressionFixtures sweeps that glob and reads " +
				"the trailing token as the expected init permission mode, so the capture would be " +
				"swept in and asserted about a different argv",
		},
		{
			glob:          dropcapFixtureGlob,
			underTestdata: true,
			controls:      []string{"dropped_lines_v0.0.0.json"},
			hazard: "the dropped-line capture's fixture sweep would read an AskUserQuestion call as " +
				"one of its own captures",
		},
		{
			glob:     setModeFamilyGlob,
			controls: []string{"set_permission_mode_v0.0.0_default.json"},
			hazard: "that is #1595's committed record of the in-band revocation wire format, and a " +
				"live #1938 run writing there overwrites it while every test stays green",
		},
		{
			glob:          initControlArmFixtureGlob,
			underTestdata: true,
			controls:      []string{"initialize_control_v0.0.0_before_first_turn.json"},
			hazard: "#1764's cross-arm comparison globs that pattern and expects exactly three arms " +
				"that agree, so the capture would be read as a fourth arm of a measurement it took " +
				"no part in",
		},
	}

	t.Run("each token mints exactly the pinned name", func(t *testing.T) {
		t.Parallel()

		// The subtest that catches a namer which stops calling versionSlug on a
		// token carrying no separator. The three subtests below cannot: such a name
		// is a legal single component that joins no family.
		for _, row := range rows {
			if got := askQuestionFixtureName(row.token); got != row.want {
				t.Errorf("token %q mints %q, want %q: #1941's writer and #1938's live run both "+
					"derive their path from this function, so a name that is not the pinned one is "+
					"a capture landing somewhere nobody looks for it",
					row.token, got, row.want)
			}
		}
	})

	t.Run("no minted name joins a committed family", func(t *testing.T) {
		t.Parallel()

		for _, row := range rows {
			base := askQuestionFixtureName(row.token)
			for _, p := range patterns {
				subject := anchorFixtureName(p, base)
				matched, err := filepath.Match(p.glob, subject)
				if err != nil {
					t.Fatalf("filepath.Match(%q, %q): %v; a malformed pattern constant makes "+
						"every comparison in this file meaningless", p.glob, subject, err)
				}
				if matched {
					t.Errorf("token %q mints %q, which matches %q: %s",
						row.token, base, p.glob, p.hazard)
				}
			}
		}
	})

	t.Run("each pattern's control can still match", func(t *testing.T) {
		t.Parallel()

		// A pattern anchored against a string it can never match is false for every
		// input, so the subtest above would pass over it having proven nothing. Each
		// row's control is the same pattern, anchored the same way through the same
		// anchorFixtureName, against a synthetic literal of the shape it was written
		// for.
		for _, p := range patterns {
			if len(p.controls) == 0 {
				t.Errorf("pattern %q carries no control, so nothing shows it can return true and "+
					"its negative assertion proves nothing", p.glob)
				continue
			}
			for _, control := range p.controls {
				subject := anchorFixtureName(p, control)
				matched, err := filepath.Match(p.glob, subject)
				if err != nil {
					t.Fatalf("filepath.Match(%q, %q): %v; a malformed pattern constant makes "+
						"every comparison in this file meaningless", p.glob, subject, err)
				}
				if !matched {
					t.Errorf("pattern %q does not match its own control %q: it is anchored against a "+
						"string shape it can never match, so \"no minted name matches it\" passed "+
						"unconditionally and this file locks nothing", p.glob, subject)
				}
			}
		}
	})

	t.Run("every minted name stays directly inside its target directory", func(t *testing.T) {
		t.Parallel()

		// An arbitrary CLEAN absolute literal. filepath.Join cleans its result, so a
		// trailing slash here would make filepath.Dir return the cleaned form and
		// this comparison would fail on a perfectly healthy name. Nothing resolves
		// the package's real testdata/: the namer takes no directory, and the
		// directory needs to exist no more than the fixture does.
		const targetDir = "/ask-user-question/fixtures"

		for _, row := range rows {
			base := askQuestionFixtureName(row.token)
			if got := filepath.Dir(filepath.Join(targetDir, base)); got != targetDir {
				t.Errorf("token %q mints %q, which joined under %q lands in %q: #1941's writer "+
					"joins this name under a real directory, and a name carrying a separator "+
					"writes somewhere its caller never chose",
					row.token, base, targetDir, got)
			}

			// Strictly implied by the comparison above, and it CANNOT be the sole
			// red: base "." joins to targetDir whose Dir is /ask-user-question, and
			// base ".." joins to /ask-user-question whose Dir is /, so both are
			// already red there. Kept because it is one comparison and because it
			// pins the ask_user_question_v prefix against a later edit that drops it
			// — a name that IS "." or ".." has no prefix left. Do not credit it as
			// the thing catching a mis-slugged token: measured, `..` interpolated raw
			// mints ask_user_question_v...json, a perfectly clean component, and this
			// assertion stays green.
			if base == "." || base == ".." {
				t.Errorf("token %q mints %q, which is not a filename at all: #1941's writer would "+
					"join it under a real directory and resolve to that directory or its parent",
					row.token, base)
			}
		}
	})
}
