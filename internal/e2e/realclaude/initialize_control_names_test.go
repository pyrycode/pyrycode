//go:build e2e_realclaude

package realclaude

// #1696 — the name half of the `initialize` control-request capture: the namer
// that mints the fixture filename #1688's live run will write its bytes under,
// and the deterministic proof that nothing it mints can land in a committed
// fixture family.
//
// # What is being fenced off
//
// The daemon wants to publish claude's model list — identifiers, display names,
// reasoning-effort levels — to connected clients. Measured by hand on
// 2026-08-21 against claude 2.1.220, OUTSIDE this repo: a control_request with
// subtype "initialize", written on the child's already-held-open stdin, comes
// back with a models array. #1688 is the live run that spends tokens to put that
// round trip under test and commit the bytes; nothing in the tree records the
// request line or the response shape yet. This file mints the name those bytes
// land under, before the run exists that could trip it.
//
// testdata/ is swept by three globs owned by three different probes:
// fixtureGlob, dropcapFixtureGlob and setModeFamilyGlob. A capture whose name
// joined one of those families would be swept into a regression test asserting
// findings about a DIFFERENT argv, or overwritten by the next run of the probe
// that owns the family. Either way durable evidence is lost WITH NO RED
// ANYWHERE, which is why this gets a lock rather than a convention.
//
// The initialize_control_v prefix is the entire mechanism. filepath.Match
// anchors a pattern's literal head at position 0, so a name beginning
// initialize_control_v cannot match any of the three — their heads are
// permission_protocol_v, dropped_lines_v and set_permission_mode_v. The prefix
// is a literal inside initControlFixtureName that no input can reach, which is
// what makes the collision impossible rather than merely unobserved.
//
// # Two namers, one family (#1712)
//
// This file carries two. initControlFixtureName takes ONE input and mints the
// name of #1688's single committed capture; it keeps every current caller,
// writeInitControlFixture among them. initControlArmFixtureName takes TWO and
// mints one name per arm of #1715's three-arm measurement; nothing consumes it
// until #1713 migrates the writer onto it.
//
// #1696 collapsed the arm dimension deliberately and recorded the reason:
// poolRevokeFixtureName carries an arm because #1643 measures three postures,
// and this capture was one probe, so the name was initialize_control_v<slug>.json
// and the lock table was tokens × 1. That was correct for a one-probe family and
// it EXPIRED when the family grew arms. Through the one-input namer all three of
// #1715's arms mint the same path: the last arm wins, the other two vanish, and
// writeInitControlFixture — which mints its own path internally, on purpose —
// writes over testdata/initialize_control_v2.1.239.json. Nothing compares a
// written path against a committed one, so the loss is silent. Do not
// re-collapse the dimension: the reason it was collapsed is the reason it is
// back.
//
// That collapse also emptied the ARM column of the separator-bearing inputs
// #1661's hostileArms carried, and the token table below took them over. Both
// columns carry them now, because both inputs reach the name — see the hostile
// arms in the arm-carrying namer's lock, and § "every minted name stays directly
// inside its target directory" in each test for what those rows measure.
//
// # One-directional absence proves nothing, so every pattern carries a control
//
// The three patterns are NOT matched against the same string. fixtureGlob and
// dropcapFixtureGlob carry a testdata/ prefix, because that is how the tests
// owning them evaluate them; setModeFamilyGlob names base names and carries no
// prefix. Anchor either one against the other's shape and it is false for every
// input, the negative assertion passes unconditionally, and this file locks
// nothing. Both directions of that mistake are silent.
//
// So anchorFixtureName is the single place anchoring happens, and BOTH the
// negative assertions and the controls go through it: flip a row's underTestdata
// and that row's negative assertion goes vacuous AND its control reddens, in the
// same edit. Neither half can rot alone.
//
// # Offline, and further: no I/O in either direction
//
// This file reaches no live claude, no daemon, no subprocess, no credential and
// no directory. It must not reach resolveClaudeBin, probeClaudeVersion,
// WithWorktree, WithWorktreeAuthenticated or captureClaudeVersion; nor
// os.Getenv, os.Environ or os.LookupEnv; nor packageDir, any of its wrappers
// setModeFixturePath, writeSetModeFixture and writeFixture, filepath.Glob, or
// any os read or write. That last group is not tidiness: `go test` runs in the
// package source directory, so a RELATIVE os.WriteFile("testdata/…") reaches
// the very committed fixtures this file exists to protect, without naming
// packageDir at all. The controls are synthetic literals for the same reason —
// one globbed off the real testdata/ would be reading the directory.
// TestFinOfflineFilesReachNoExecHelper enforces all of it over this file's AST
// rather than over this paragraph.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestInitControlFixtureName_|TestInitControlArmFixtureName_|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// All three must report PASS — not SKIP, not "no tests to run" — on a machine with no
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

// initControlFixtureName mints the fixture filename for #1688's `initialize`
// control-request capture. It is a pure function of one input: no directory
// parameter, no *testing.T, no I/O.
//
// The initialize_control_v prefix is a literal here on purpose: it is the part
// of the name no input can reach, and it is the whole reason the minted name
// cannot join a committed fixture family. Do not derive it from the argument.
//
// CONTRACT for #1697's writer: the result is always a SINGLE CLEAN PATH
// COMPONENT — it carries no separator and is never "." or "..", for any input.
// That is what makes filepath.Join(dir, name) land in dir at the call site.
// Nothing in the signature says so; the containment subtest below is what proves
// it.
//
// That guarantee is LEXICAL, and it is about the name rather than about the
// filesystem. It says the minted string is one component, so Join cannot walk
// out of dir. It says nothing about dir itself: pass a directory that is or
// contains a symlink and the write still resolves wherever that symlink points.
// Choosing dir stays the caller's responsibility.
//
// Its TOTALITY rests on versionSlug's character class, not on the token table
// below. No separator survives [^a-z0-9._-]+ → _, which is why the property
// holds for every string rather than merely the thirteen sampled. The table's
// job is to catch that coupling breaking — widen the class to admit a separator
// and the containment subtest reddens on the /-bearing tokens. Both halves
// matter: the table is the tripwire, the character class is the guarantee.
func initControlFixtureName(versionToken string) string {
	return fmt.Sprintf("initialize_control_v%s.json", versionSlug(versionToken))
}

// --- the arm table --------------------------------------------------------------

// initControlArms names the three arms of #1715's `initialize` control-request
// measurement. Each identifier names the arm's SEND POINT, because the send point
// is the only dimension the arms vary on:
//
//   - before_first_turn — the control request is written before the first user turn
//   - after_completed_turn — it is written after a turn has completed
//   - control_no_request — no control request is sent at all
//
// READ-ONLY: never append to it, never reassign it. It is ranged over from
// t.Parallel() tests here and, once #1713 and #1715 land, from parallel tests in
// their files too; a mutation would race in a way -race catches only when the runs
// happen to overlap. Ranging is the only supported access — nothing hands the
// slice out, so no defensive copy is needed. poolRevokeArms carries the same
// contract for the same reason.
//
// GROW THIS DECLARATION rather than shadowing it. When #1713 or #1715 needs
// per-arm behaviour — the send point's semantics, the prompt, the drive sequence —
// those fields go here and this slice becomes a table of rows, exactly as
// poolRevokeArms is. A SECOND table keyed by these names is the duplication the
// distinctness subtest below exists to prevent: two sources of truth for the arm
// set is how a run reports more arms measured than there are fixtures on disk.
//
// A []string and not a one-field struct: this slice needs identifiers and nothing
// else today, and a one-field struct is a shape #1713 would have to change the
// moment it knows its fields.
//
// The hostile arms the lock below feeds through the namer are that test's OWN
// literals and must NOT be appended here, for the reason #1661 gives about
// poolRevokeArms: this declaration is what other files range.
var initControlArms = []string{
	"before_first_turn",
	"after_completed_turn",
	"control_no_request",
}

// --- the arm-carrying namer -----------------------------------------------------

// initControlArmFixtureName mints the fixture filename for ONE ARM of #1715's
// three-arm `initialize` control-request measurement. It is a pure function of its
// two inputs: no directory parameter, no *testing.T, no I/O in either direction.
//
// It lands ALONGSIDE initControlFixtureName rather than replacing it: that namer
// keeps its one input and every current caller, and #1713 is what migrates
// writeInitControlFixture onto this one.
//
// The initialize_control_v prefix is a literal here on purpose, exactly as it is
// there: filepath.Match anchors a pattern's literal head at position 0, so a name
// beginning with a head no committed family shares cannot match any of the three
// family globs. Do not derive it from either argument — that is what makes the
// collision impossible rather than merely unobserved.
//
// BOTH inputs go through versionSlug, not only the version. poolRevokeFixtureName
// is the sibling to copy and setModeFixtureName is NOT: that one interpolates its
// arm RAW. initControlArms is the human-edited growth point, and an arm typed as
// "a/b" interpolated raw mints a name landing one directory DOWN from the target.
//
// The `_` between the two slugs is LOAD-BEARING. It is the single character
// keeping this name off initControlFixtureName's for an empty arm: drop it and the
// two namers agree on one string, which is #1688's committed capture overwritten
// by a live arm with nothing red anywhere.
//
// CONTRACT for #1713's writer: the result is always a SINGLE CLEAN PATH COMPONENT
// — it carries no separator and is never "." or "..", for any PAIR of inputs. That
// is what makes filepath.Join(dir, name) land in dir at the call site. Nothing in
// the signature says so; the containment subtest below is what proves it.
//
// That guarantee is LEXICAL, and its totality rests on versionSlug's character
// class rather than on the sampled tables: no separator survives
// [^a-z0-9._-]+ → _, in either column. It says nothing about dir itself — pass a
// directory that is or contains a symlink and the write still resolves wherever
// that symlink points. Choosing dir stays the caller's responsibility.
func initControlArmFixtureName(versionToken, arm string) string {
	return fmt.Sprintf("initialize_control_v%s_%s.json", versionSlug(versionToken), versionSlug(arm))
}

// --- the lock -------------------------------------------------------------------

// TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained is #1696
// whole: no name this namer mints joins a committed fixture family, every
// pattern making that claim can still match something, and every minted name
// stays a plain component directly inside whatever directory #1697's writer
// joins it under.
//
// It settles with no claude binary and no credentials, and it reads nothing off
// disk — the properties belong to the name, not to any call site.
func TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained(t *testing.T) {
	t.Parallel()

	// Adversarial version tokens: what `claude --version` might plausibly emit,
	// plus the shapes that could smuggle a name into another family or out of a
	// directory. The last three are the ones the containment subtest below
	// actually measures — see the note there before dropping any of them.
	tokens := []string{
		"2.1.220",
		"2.1.220 (Claude Code)",
		"2_1_220",
		"2.1.220-beta.1",
		"permission_protocol",
		"set_permission_mode",
		"dropped_lines",
		"..",
		"../..",
		"a/b",
		"/abs",
		"",
		strings.Repeat("9", 64),
	}

	// poolRevokeNamePattern is #1661's row type and is generic over the family
	// despite being named for #1643's: glob, underTestdata, controls, hazard say
	// nothing about which probe owns the family. Reused rather than duplicated —
	// a parallel type would mean a second anchoring path, and the single
	// anchorFixtureName is the property this file depends on.
	//
	// Function-local although every control here is a literal and it could be a
	// package-level var: it is used by exactly one test, and keeping it here
	// holds this file's package-scope surface at five identifiers —
	// initControlFixtureName, initControlArmFixtureName, initControlArms and the
	// two tests — which is what keeps concurrent siblings from colliding with it.
	patterns := []poolRevokeNamePattern{
		{
			glob:     setModeFamilyGlob,
			controls: []string{"set_permission_mode_v0.0.0_default.json"},
			hazard: "that is #1595's committed record of the in-band revocation wire format, and a " +
				"live #1688 run writing there overwrites it while every test stays green",
		},
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
			hazard: "the dropped-line capture's fixture sweep would read an initialize response as " +
				"one of its own captures",
		},
	}

	t.Run("no minted name joins a committed family", func(t *testing.T) {
		t.Parallel()

		for _, token := range tokens {
			base := initControlFixtureName(token)
			for _, p := range patterns {
				subject := anchorFixtureName(p, base)
				matched, err := filepath.Match(p.glob, subject)
				if err != nil {
					t.Fatalf("filepath.Match(%q, %q): %v; a malformed pattern constant makes "+
						"every comparison in this file meaningless", p.glob, subject, err)
				}
				if matched {
					t.Errorf("token %q mints %q, which matches %q: %s",
						token, base, p.glob, p.hazard)
				}
			}
		}
	})

	t.Run("each pattern's control can still match", func(t *testing.T) {
		t.Parallel()

		// A pattern anchored against a string it can never match is false for
		// every input, so the subtest above would pass over it having proven
		// nothing. Each row's control is the same pattern, anchored the same way,
		// against a synthetic literal of the shape it was written for.
		//
		// Never a committed filename and never a directory listing: a control
		// written as a real capture reddens spuriously the day that capture is
		// retaken at a new version, and the cheap repair for a spurious red is to
		// weaken the control. Worse than rot, and true today —
		// permission_protocol_v2.1.158.json is committed right now and does NOT
		// match fixtureGlob, having no second _, so a control lifted off the real
		// directory is red on arrival.
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

		// An arbitrary CLEAN absolute literal. filepath.Join cleans its result, so
		// a trailing slash here would make filepath.Dir return the cleaned form and
		// this comparison would fail on a perfectly healthy name. Nothing resolves
		// the package's real testdata/: the namer takes no directory, and the
		// directory needs to exist no more than the fixture does.
		const targetDir = "/initialize-control/fixtures"

		// This subtest is the ONLY thing in this file that a mutant can redden,
		// and only on the /-bearing tokens. Measured 2026-08-22 against a namer
		// interpolating its token raw — equally pure, still carrying the literal
		// prefix, conforming to every other line of #1696: the family globs above
		// stay green (the prefix keeps the name out of every family whether or not
		// the token was slugged) and only `../..`, `a/b` and `/abs` go red here.
		// That is why those three tokens are load-bearing and must not be dropped
		// from the table: without them this whole file is green under a namer that
		// never calls versionSlug.
		for _, token := range tokens {
			base := initControlFixtureName(token)
			if got := filepath.Dir(filepath.Join(targetDir, base)); got != targetDir {
				t.Errorf("token %q mints %q, which joined under %q lands in %q: #1697's writer "+
					"joins this name under a real directory, and a name carrying a separator "+
					"writes somewhere its caller never chose",
					token, base, targetDir, got)
			}

			// Strictly implied by the comparison above, and it CANNOT be the sole
			// red: base "." joins to targetDir whose Dir is /initialize-control,
			// and base ".." joins to /initialize-control whose Dir is /, so both
			// are already red there. Kept because it is one comparison and because
			// it pins the initialize_control_v prefix against a later edit that
			// drops it — a name that IS "." or ".." has no prefix left. Do not
			// credit it as the thing catching a mis-slugged token: measured, `..`
			// interpolated raw mints initialize_control_v...json, a perfectly clean
			// component, and this assertion stays green.
			if base == "." || base == ".." {
				t.Errorf("token %q mints %q, which is not a filename at all: #1697's writer would "+
					"join it under a real directory and resolve to that directory or its parent",
					token, base)
			}
		}
	})
}

// --- the arm-carrying namer's lock ----------------------------------------------

// TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained is
// #1712 whole: no name the arm-carrying namer mints joins a committed fixture
// family, every pattern making that claim can still match something, no minted
// name collides with #1688's committed one-arm capture, distinct arms mint
// distinct names, and every minted name stays a plain component directly inside
// whatever directory #1713's writer joins it under.
//
// Five properties and not one restated five ways: each subtest is the SOLE red
// for a distinct mutant — an arm interpolated raw reddens only containment, a
// prefix derived from an input reddens only the family globs, an arm folded to a
// constant reddens only distinctness, and an arm appended with no separator
// reddens only the equality check. Measured 2026-08-24; the rows each mutant needs
// are called out where they live.
//
// It settles with no claude binary and no credentials, and it reads nothing off
// disk — the properties belong to the name, not to any call site.
func TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained(t *testing.T) {
	t.Parallel()

	// Adversarial version tokens: what `claude --version` might plausibly emit,
	// plus the shapes that could smuggle a name into another family or out of a
	// directory.
	//
	// 2.1.239 is REQUIRED and is not a plausible-version row: it is the
	// claude_version inside testdata/initialize_control_v2.1.239.json, the capture
	// committed by #1688, which is what makes the equality subtest cover the file
	// on disk today rather than only the general property.
	//
	// The three family-smuggling tokens carry the family's `v`, and the bare heads
	// #1696's table carries CANNOT stand in for them. Measured with filepath.Match
	// semantics on 2026-08-24: under a namer deriving its fixed prefix from an
	// input, token "set_permission_mode" mints set_permission_mode_<arm>.json,
	// which matches NOTHING — set_permission_mode_v*_*.json needs a literal `v`
	// after the head and the head alone never supplies it. Same for the other two.
	// The _v1 forms hit all three instantly. Do not "harmonise" these back to the
	// bare heads; that silently empties the family-glob subtest of its only mutant.
	tokens := []string{
		"2.1.239",
		"2.1.220",
		"2.1.220 (Claude Code)",
		"2_1_220",
		"2.1.220-beta.1",
		"permission_protocol_v1",
		"set_permission_mode_v1",
		"dropped_lines_v1",
		"..",
		"../..",
		"a/b",
		"/abs",
		"",
		strings.Repeat("9", 64),
	}

	// The other input dimension. These are this test's OWN literals and must NOT be
	// added to initControlArms: that table is read-only and is ranged from
	// t.Parallel() tests, here and later in #1713's and #1715's files. #1661's
	// hostileArms is the precedent and carries the same shapes, for the same reason
	// — the arm table is where a fourth arm gets added by somebody typing a string.
	//
	// The rows carrying a literal `/` — "a/b", "../..", "/abs" — are the ones the
	// containment subtest actually measures, and ".." is not among them: measured,
	// an arm ".." interpolated raw still mints a perfectly clean component. It is
	// kept for the "." / ".." clause, not for containment.
	//
	// The "" row is not filler either: it is the ONLY row that reddens the equality
	// subtest's mutant, because an empty arm is what makes a separator-less
	// concatenation agree with initControlFixtureName's output exactly.
	hostileArms := []string{
		"a/b",
		"..",
		"../..",
		"/abs",
		"",
		"set_permission_mode_v1",
		strings.Repeat("z", 64),
	}

	// The declared arms plus the hostile ones. The distinctness subtest below
	// deliberately does NOT use this slice — see the note there.
	arms := make([]string, 0, len(initControlArms)+len(hostileArms))
	arms = append(arms, initControlArms...)
	arms = append(arms, hostileArms...)

	// #1661's row type and the single anchorFixtureName, reused across the family
	// boundary exactly as the one-input lock above reuses them. A parallel type
	// would mean a second anchoring path, and a second anchoring site is how a row
	// goes silently vacuous.
	patterns := []poolRevokeNamePattern{
		{
			glob:     setModeFamilyGlob,
			controls: []string{"set_permission_mode_v0.0.0_default.json"},
			hazard: "that is #1595's committed record of the in-band revocation wire format, and " +
				"one arm of a live #1715 run writing there overwrites it while every test stays green",
		},
		{
			glob:          fixtureGlob,
			underTestdata: true,
			controls:      []string{"permission_protocol_v0.0.0_default.json"},
			hazard: "TestRealClaude_PermissionProtocol_RegressionFixtures sweeps that glob and reads " +
				"the trailing token as the expected init permission mode, so the arm's capture would " +
				"be swept in and asserted about a different argv — and with three arms it would be " +
				"swept in three times",
		},
		{
			glob:          dropcapFixtureGlob,
			underTestdata: true,
			controls:      []string{"dropped_lines_v0.0.0.json"},
			hazard: "the dropped-line capture's fixture sweep would read an initialize response as " +
				"one of its own captures",
		},
	}

	t.Run("no minted name joins a committed family", func(t *testing.T) {
		t.Parallel()

		for _, token := range tokens {
			for _, arm := range arms {
				base := initControlArmFixtureName(token, arm)
				for _, p := range patterns {
					subject := anchorFixtureName(p, base)
					matched, err := filepath.Match(p.glob, subject)
					if err != nil {
						t.Fatalf("filepath.Match(%q, %q): %v; a malformed pattern constant makes "+
							"every comparison in this file meaningless", p.glob, subject, err)
					}
					if matched {
						t.Errorf("token %q arm %q mints %q, which matches %q: %s",
							token, arm, base, p.glob, p.hazard)
					}
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
		//
		// Never a committed filename and never a directory listing, for the reason
		// the one-input lock's control subtest states in full: a control lifted off
		// the real testdata/ rots the day a capture is retaken, and the cheap repair
		// for a spurious red is to weaken the control.
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

	t.Run("no minted name collides with the committed one-arm capture", func(t *testing.T) {
		t.Parallel()

		// String equality, and a pattern check CANNOT stand in for it: the
		// initialize_control_v family is matched by no glob in this package — it is
		// addressed only by exact name — so the family-glob subtest above sweeps
		// straight past the one collision that is actually reachable today.
		//
		// The `_` between the two slugs is the whole mechanism, and the empty arm is
		// where it earns its place: concatenate the slugs without it and an empty arm
		// mints exactly what initControlFixtureName mints.
		for _, token := range tokens {
			oneArm := initControlFixtureName(token)
			for _, arm := range arms {
				if got := initControlArmFixtureName(token, arm); got == oneArm {
					t.Errorf("token %q arm %q mints %q, which is also what initControlFixtureName "+
						"mints from that token: a live #1715 arm writes straight over #1688's "+
						"committed capture — testdata/initialize_control_v2.1.239.json is the file "+
						"on disk today — and nothing compares a written path against a committed "+
						"one, so the loss is silent", token, arm, got)
				}
			}
		}
	})

	t.Run("distinct arms mint distinct names", func(t *testing.T) {
		t.Parallel()

		// initControlArms ONLY, never the hostile arms: two hostile shapes that slug
		// to one string are a property of this test's own literals rather than a
		// defect in the namer, and folding them in would redden this subtest against
		// honest code. The declared identifiers are the subject — they are the
		// human-edited growth point, and the one place a fourth arm arrives.
		//
		// versionSlug is what makes a collision reachable at all: it lowercases,
		// folds [^a-z0-9._-]+ to _, and truncates at 32 characters, so arms differing
		// only in case, only in a folded character, or only past character 32 mint one
		// name. The seen-map shape is
		// TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs's, whose
		// comment gives the reason in one line: a collision would silently overwrite
		// one direction with the other. Here it is worse by a dimension — two arms
		// reducing to one name overwrite each other, and the run then reports more
		// arms measured than there are fixtures on disk.
		for _, token := range tokens {
			seen := make(map[string]string, len(initControlArms))
			for _, arm := range initControlArms {
				name := initControlArmFixtureName(token, arm)
				if prev, dup := seen[name]; dup {
					t.Errorf("token %q: arms %q and %q both mint %q, so one arm's capture "+
						"overwrites the other's and #1715 reports three arms measured with two "+
						"fixtures on disk", token, prev, arm, name)
				}
				seen[name] = arm
			}
		}
	})

	t.Run("every minted name stays directly inside its target directory", func(t *testing.T) {
		t.Parallel()

		// An arbitrary CLEAN absolute literal. filepath.Join cleans its result, so a
		// trailing slash here would make filepath.Dir return the cleaned form and this
		// comparison would fail on a perfectly healthy name. Nothing resolves the
		// package's real testdata/: the namer takes no directory, and the directory
		// needs to exist no more than the fixture does.
		const targetDir = "/initialize-control/fixtures"

		// This subtest is the sole red for a namer interpolating its ARM raw — which
		// is what setModeFixtureName does, so it is not a hypothetical mutant — and it
		// reddens only on the /-bearing rows. Both columns carry them now that both
		// inputs reach the name: tokens "a/b", "/abs" and "../..", and the same three
		// shapes in hostileArms. Dropping them from either column leaves this whole
		// file green against a namer that never slugs that column.
		for _, token := range tokens {
			for _, arm := range arms {
				base := initControlArmFixtureName(token, arm)
				if got := filepath.Dir(filepath.Join(targetDir, base)); got != targetDir {
					t.Errorf("token %q arm %q mints %q, which joined under %q lands in %q: #1713's "+
						"writer joins this name under a real directory, and a name carrying a "+
						"separator writes somewhere its caller never chose",
						token, arm, base, targetDir, got)
				}

				// Strictly implied by the comparison above and it can never be the
				// sole red — base "." joins to targetDir whose Dir is
				// /initialize-control, and ".." joins to /initialize-control whose Dir
				// is / — so both are already red there. Kept as a prefix pin: a name
				// that IS "." or ".." has no initialize_control_v prefix left. Do not
				// credit it as the thing catching a mis-slugged input; measured, ".."
				// interpolated raw in either column still mints a perfectly clean
				// component and this assertion stays green.
				if base == "." || base == ".." {
					t.Errorf("token %q arm %q mints %q, which is not a filename at all: #1713's "+
						"writer would join it under a real directory and resolve to that directory "+
						"or its parent", token, arm, base)
				}
			}
		}
	})
}
