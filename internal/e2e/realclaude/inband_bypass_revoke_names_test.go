//go:build e2e_realclaude

package realclaude

// #1661 — the name half of #1643's Pool-issued bypass-revocation substrate: the
// namer that mints a fixture filename for one arm, and the deterministic proof
// that nothing it mints can land in a committed fixture family.
//
// # What is being fenced off
//
// #1595 proved live that a set_permission_mode control request carrying mode
// "default" drops a running child's bypass posture. Its durable record is four
// committed fixtures under testdata/, named by setModeFixtureName over
// setModeArms. Three of those four arm names — revoke, control_default,
// control_bypass — are the SAME STRINGS poolRevokeArms carries, because the two
// tickets measure the same three postures by different means. So on one claude
// version, a live #1643 run that reached for setModeFixtureName would overwrite
// three of #1595's four committed fixtures WHILE EVERY TEST STAYED GREEN. That is
// a durable-evidence failure with no red anywhere, which is why it gets a lock
// rather than a convention — and why the lock lands before a live run exists that
// could trip it, so #1643's first failure mode costs zero tokens.
//
// The pool_revoke_ prefix is the entire mechanism. filepath.Match anchors a
// pattern's literal head at position 0, so a name beginning pool_revoke_v cannot
// match setModeFamilyGlob, fixtureGlob or dropcapFixtureGlob. The prefix is a
// literal inside poolRevokeFixtureName and no input can reach it — that is what
// makes the overwrite impossible rather than merely unobserved.
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
// same edit. Neither half can rot alone. The controls must not inline their own
// anchoring.
//
// # Offline, and further: no I/O in either direction
//
// This file reaches no live claude, no daemon, no subprocess, no credential and
// no directory. It must not reach resolveClaudeBin, probeClaudeVersion,
// WithWorktree, WithWorktreeAuthenticated, os.Getenv or os.Environ; and it must
// not reach packageDir, either of its wrappers setModeFixturePath and
// writeSetModeFixture, filepath.Glob, or any os read or write. That last group is
// not tidiness: `go test` runs in the package source directory, so a RELATIVE
// os.WriteFile("testdata/…") reaches the very committed fixtures this file exists
// to protect, without naming packageDir at all. The controls are synthetic
// literals for the same reason — one globbed off the real testdata/ would be
// reading the directory. TestFinOfflineFilesReachNoExecHelper enforces all of it
// over this file's AST rather than over this paragraph.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestPoolRevokeFixtureName_|TestFinOfflineFilesReachNoExecHelper' \
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

// poolRevokeFixtureName mints the fixture filename for one arm of #1643's
// Pool-issued bypass-revocation probe. It is a pure function of its two inputs:
// no directory parameter, no *testing.T, no I/O.
//
// CONTRACT for #1662's writer: the result is always a SINGLE CLEAN PATH
// COMPONENT — it carries no separator and is never "." or "..", for any pair of
// inputs. That is what makes filepath.Join(dir, name) land in dir at the call
// site. Nothing in the type says so; the containment subtest below is what proves
// it.
//
// BOTH inputs go through versionSlug, not only the version — the parameter is
// named for a version and a reader would otherwise wonder. versionSlug is a
// general [^a-z0-9._-]+ → _ normaliser with a 32-character cap, and the arm needs
// exactly the same treatment: poolRevokeArms is the human-edited growth point,
// and an arm typed as "a/b" interpolated raw mints a name landing one directory
// DOWN from the target. Applying the same normaliser to both is what makes "both
// dimensions get identical treatment" one line rather than a convention.
//
// The pool_revoke_ prefix is a literal here on purpose: it is the one part of the
// name no input can reach, and it is the whole reason the minted name cannot join
// a committed fixture family. Do not derive it from either argument.
func poolRevokeFixtureName(versionToken, arm string) string {
	return fmt.Sprintf("pool_revoke_v%s_%s.json", versionSlug(versionToken), versionSlug(arm))
}

// --- the committed families this namer must stay out of -------------------------

// setModeFamilyGlob describes #1595's WHOLE committed fixture family — every name
// setModeFixtureName mints, for any version token and any of setModeArms' rows.
// It lives here rather than beside that namer because it is this file's subject:
// it is the load-bearing pattern, the one whose match would mean a live #1643 run
// writing over committed evidence.
//
// No testdata/ prefix, deliberately: it names base names, and filepath.Match's *
// does not cross a separator. See anchorFixtureName.
const setModeFamilyGlob = "set_permission_mode_v*_*.json"

// poolRevokeNamePattern is one committed-fixture family poolRevokeFixtureName's
// output must stay out of, paired with a control proving the pattern can still
// return true.
type poolRevokeNamePattern struct {
	glob string

	// underTestdata selects the string the glob is evaluated against, and it is
	// the field deciding whether this row asserts anything at all. See
	// anchorFixtureName.
	underTestdata bool

	// controls are BASE names of the shape this glob was written for;
	// anchorFixtureName adds the prefix where the row needs one.
	//
	// Synthetic literals, never committed filenames: a dropcap control written as
	// the one dropped-lines fixture that exists today reddens spuriously the day
	// that capture is retaken at a new version, and the cheap repair for a
	// spurious red is to weaken the control. A literal of the right SHAPE proves
	// the anchoring identically and does not rot with the fixture set.
	controls []string

	// hazard is what a match would cost, for the failure message. The message is
	// read once, by somebody about to spend tokens on a three-arm live run.
	hazard string
}

// anchorFixtureName returns the string p's glob is evaluated against.
//
// The single anchoring in this file: both the negative assertions and the
// controls call it, which is what keeps a mis-anchored row from going silently
// vacuous. fixtureGlob and dropcapFixtureGlob are evaluated by their owning tests
// against paths relative to the package directory, so they can only ever match a
// testdata/-prefixed subject; setModeFamilyGlob names base names and matches no
// prefixed subject at all.
func anchorFixtureName(p poolRevokeNamePattern, base string) string {
	if p.underTestdata {
		return filepath.Join("testdata", base)
	}
	return base
}

// --- the lock -------------------------------------------------------------------

// TestPoolRevokeFixtureName_AvoidsCommittedFamiliesAndStaysContained is #1661
// whole: no name this namer mints joins a committed fixture family, every pattern
// making that claim can still match something, and every minted name stays a
// plain component directly inside whatever directory #1662's writer joins it
// under.
//
// It settles with no claude binary and no credentials, and it reads nothing off
// disk — the properties belong to the name, not to any call site.
func TestPoolRevokeFixtureName_AvoidsCommittedFamiliesAndStaysContained(t *testing.T) {
	t.Parallel()

	// Adversarial version tokens: what `claude --version` might plausibly emit,
	// plus the shapes that could smuggle a name into another family or out of a
	// directory. TestRealClaude_SetPermissionMode_FixtureNamesAvoidRegressionGlobs
	// carries nine of these, one of them (permission_protocol) shaped to smuggle a
	// name into another family. This namer must stay clear of THREE families, so
	// it carries one such token per family.
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
		"",
		strings.Repeat("9", 64),
	}

	// The other input dimension. These are this test's OWN literals and must NOT
	// be added to poolRevokeArms: that table is read-only, is ranged from
	// t.Parallel() tests in several files, and
	// TestPoolRevokeArms_PinLaunchPostureAndUpdateByName fails loudly on any row
	// it does not pin. The dimension matters because the arm table is where a
	// fourth arm gets added by somebody typing a string.
	hostileArms := []string{
		"a/b",
		"..",
		"../..",
		"/abs",
		"",
		"set_permission_mode",
		strings.Repeat("z", 64),
	}

	arms := make([]string, 0, len(poolRevokeArms)+len(hostileArms))
	for _, arm := range poolRevokeArms {
		arms = append(arms, arm.name)
	}
	arms = append(arms, hostileArms...)

	// The family row's controls are #1595's OWN output over the same tokens, which
	// is why the pattern table cannot be a package-level literal. This is the one
	// place this file ranges setModeArms, and it ranges it read-only.
	familyControls := make([]string, 0, len(tokens)*len(setModeArms))
	for _, token := range tokens {
		for _, arm := range setModeArms {
			familyControls = append(familyControls, setModeFixtureName(token, arm.name))
		}
	}

	patterns := []poolRevokeNamePattern{
		{
			glob:     setModeFamilyGlob,
			controls: familyControls,
			hazard: "that is #1595's committed record of the in-band revocation wire format, and a " +
				"live #1643 run writing there overwrites it while every test stays green",
		},
		{
			glob:          fixtureGlob,
			underTestdata: true,
			controls:      []string{"permission_protocol_v0.0.0_default.json"},
			hazard: "TestRealClaude_PermissionProtocol_RegressionFixtures sweeps that glob and reads " +
				"the trailing token as the expected init permission mode, so the fixture would be " +
				"swept in and asserted about a different argv",
		},
		{
			glob:          dropcapFixtureGlob,
			underTestdata: true,
			controls:      []string{"dropped_lines_v0.0.0.json"},
			hazard: "the dropped-line capture's fixture sweep would read a revocation record as one " +
				"of its own captures",
		},
	}

	t.Run("no minted name joins a committed family", func(t *testing.T) {
		t.Parallel()

		for _, token := range tokens {
			for _, arm := range arms {
				base := poolRevokeFixtureName(token, arm)
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

		// The readable restatement of the family-glob row above, not an
		// independent guard: measured, every name setModeFixtureName mints matches
		// setModeFamilyGlob, so that row strictly implies this and this can never
		// be the sole red. Kept because it names the hazard concretely in a
		// failure message, and because it keeps holding if #1595's family ever
		// grows a name the glob does not describe. Do not credit it as the thing
		// closing the overwrite hazard — the glob closes it.
		for _, token := range tokens {
			for _, arm := range poolRevokeArms {
				got := poolRevokeFixtureName(token, arm.name)
				if got == setModeFixtureName(token, arm.name) {
					t.Errorf("token %q arm %q: this namer and setModeFixtureName both mint %q, so a "+
						"live #1643 run writes straight over #1595's committed fixture for that arm",
						token, arm.name, got)
				}
			}
		}
	})

	t.Run("each pattern's control can still match", func(t *testing.T) {
		t.Parallel()

		// A pattern anchored against a string it can never match is false for
		// every input, so the subtest above would pass over it having proven
		// nothing. Each row's control is the same pattern, anchored the same way,
		// against a subject of the shape it was written for.
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
		const targetDir = "/pool-revoke/fixtures"

		// This assertion is NOT green by construction, which is why it keeps the
		// version dimension as well as the arm one. The namer above is what makes
		// it green; a namer interpolating both inputs raw is equally pure and
		// equally AC-conforming, and escapes on a large fraction of these pairs —
		// `../..` mints pool_revoke_v../.._revoke.json, whose directory under any
		// target is that target's own subdirectory. Containment is the assertion
		// forcing both inputs through the slug.
		//
		// It also covers the family glob's blind spot: a separator-bearing name
		// escapes containment AND evades setModeFamilyGlob, because filepath.Match
		// will not cross a separator. Neither assertion alone settles such a name.
		for _, token := range tokens {
			for _, arm := range arms {
				base := poolRevokeFixtureName(token, arm)
				if got := filepath.Dir(filepath.Join(targetDir, base)); got != targetDir {
					t.Errorf("token %q arm %q mints %q, which joined under %q lands in %q: #1662's "+
						"writer joins this name under a real directory, and a name carrying a "+
						"separator writes somewhere its caller never chose",
						token, arm, base, targetDir, got)
				}
			}
		}
	})
}
