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
// # One input, not two
//
// poolRevokeFixtureName carries an arm because #1643 measures three postures;
// this capture is one probe, so the name is initialize_control_v<slug>.json and
// the lock table is tokens × 1. Collapsing that dimension is what makes this
// file smaller than #1661's — and it is also what removes most of the
// separator-bearing inputs, since #1661's hostileArms carried them. They are
// back in the token table below, and § "every minted name stays directly inside
// its target directory" says why they are the only rows that measure anything.
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
//	  -run 'TestInitControlFixtureName_|TestFinOfflineFilesReachNoExecHelper' \
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
	// holds this file's package-scope surface at two identifiers, which is what
	// keeps concurrent siblings from colliding with it.
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
