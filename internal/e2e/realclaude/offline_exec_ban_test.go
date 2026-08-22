//go:build e2e_realclaude

package realclaude

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// The offline files each carry a header saying they must reach no helper that
// execs, spawns or reads the operator's environment, and each header then names
// the helpers. Until this file existed, nothing ran that check.
//
// # Why the rule could not live in the headers
//
// Three of those headers describe an enforcement they do not have: a
// forbidden-symbol grep, which "has to read this file's CODE and skip its
// comments". They are right about the hazard and specific about it. Naming the
// banned helpers in the prose means the file answers for every name on the
// list, so a grep run over the whole file matches the paragraph rather than any
// call and can never come back empty. #1290 shipped exactly that shape: a bare
// t.Skip grep that matched its own file's header sentence.
//
// A rule that says "run this grep, but exclude the comments" is a rule someone
// has to remember, under exactly the budget pressure that makes people skip it.
// Parsing the file makes the exclusion STRUCTURAL rather than remembered. The
// call below does not pass parser.ParseComments, so the tree carries no comment
// text at all and the check CANNOT answer itself out of the header that states
// it. That is the property the prose asked for and could not hold.
//
// # Why it is worth a check at all
//
// One of the bans is called a credential guard by its own header, and it is
// not tidiness: the process environment here carries CLAUDE_CODE_OAUTH_TOKEN
// and ANTHROPIC_API_KEY, and the tests most tempted to dump it are the ones
// whose subject IS the ambient environment. Another ban keeps a skip out: a
// helper that skips when claude is absent exits 0, which reads as a pass under
// `make e2e-realclaude`.
//
// # One table, not four headers
//
// The list is here rather than restated per file, so a helper that learns to
// exec is added in one place. Each entry is the file's OWN header list and no
// wider: these files differ in what they legitimately do, and a shared ban
// would be red against shipped code.
//
// A dotted entry is matched as a selector, so `os.Setenv` is banned in a file
// that uses `t.Setenv` on purpose.
var finOfflineExecBans = map[string][]string{
	"finding_run_record_test.go": {
		"probeClaudeVersion", "resolveClaudeBin", "WithWorktreeAuthenticated",
		"pinReadState", "pinExit1", "pinScanArgv", "probeProcessSnapshot",
		"tdnScan", "holdProbeFIFO",
	},
	"finding_artifact_write_test.go": {
		"probeClaudeVersion", "resolveClaudeBin", "WithWorktreeAuthenticated",
		"pinReadState", "pinExit1", "pinScanArgv", "probeProcessSnapshot",
		"tdnScan", "holdProbeFIFO",
	},
	"finding_live_staging_test.go": {
		"spawnProbePyry", "holdProbeFIFO", "probeProcessSnapshot", "pinScanArgv",
		"tdnScan", "WithWorktree", "WithWorktreeAuthenticated",
		"t.TempDir", "os.Getenv", "os.Environ", "os.Setenv",
	},
	// #1651. t.TempDir is deliberately absent: this file's seed writes a
	// sessions.json, and a tempdir is where it must land. What the entry keeps out
	// is anything that would turn its PASS into a SKIP — the whole value of these
	// two tests is that they settle #1643's launch table with no claude binary and
	// no credentials at all.
	"inband_bypass_revoke_arms_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "os.Getenv", "os.Environ",
	},
	// #1661. Wider than its sibling above, because this file performs no I/O in
	// EITHER direction and the artifacts it fences off are committed. The first
	// six keep a SKIP out, as everywhere else here. The rest keep the file away
	// from the real testdata/: packageDir resolves it, setModeFixturePath and
	// writeSetModeFixture are the two wrappers that reach packageDir — so banning
	// it alone leaves the ban true and the property false — and filepath.Glob is
	// the read direction, which is what keeps this file's controls synthetic
	// literals rather than a directory listing.
	//
	// The os read/write entries are the shortest route and not the scenic one: `go
	// test` runs in the package source directory, so a RELATIVE
	// os.WriteFile("testdata/…") overwrites the committed fixtures without naming
	// packageDir or either wrapper. That is the exact hazard this file exists to
	// prevent, so the ban covers it. t.TempDir is absent for a different reason
	// than in #1651's entry: that file's seed must write somewhere, this one
	// writes nothing and needs no directory at all.
	"inband_bypass_revoke_names_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "os.Getenv", "os.Environ",
		"packageDir", "setModeFixturePath", "writeSetModeFixture",
		"filepath.Glob", "os.ReadFile", "os.WriteFile", "os.Create", "os.ReadDir",
	},
	// #1662. The first six are this family's standing set: the first four keep a
	// SKIP out — resolveClaudeBin and WithWorktreeAuthenticated both skip INSIDE
	// the test body, after `=== RUN` is printed, so the gate cannot tell the skip
	// from a pass — and the last two are the credential guard.
	//
	// The packageDir trio is copied from the entry above for the reason that entry
	// states: the check is an AST identifier match, so a file calling
	// writeSetModeFixture reaches packageDir transitively while never naming it,
	// and a packageDir-only entry leaves the ban true and the property false.
	// writeFixture is the fourth name for the same reason — it is the spike's own
	// packageDir wrapper, and #1661's code review flagged its absence as the one
	// residual left for this ticket.
	//
	// What is deliberately ABSENT, and where this entry must NOT copy the one
	// above: t.TempDir, os.WriteFile, os.Create, os.ReadFile, os.ReadDir and
	// filepath.Glob all stay available. This file's entire subject is a write, a
	// read-back and a directory listing, so banning them would be red against
	// shipped code. The relative-path hazard those bans close for #1661 — `go
	// test` runs in the package source directory, so a relative
	// os.WriteFile("testdata/…") reaches the committed fixtures — cannot be closed
	// by a ban here. It is closed instead by
	// TestPoolRevokeFixture_RoundTripsEveryFieldIntoOneNamedEntry: a writer that
	// sent its bytes to a relative testdata/ leaves the t.TempDir() holding ZERO
	// entries, and the exactly-one-entry assertion goes red.
	"inband_bypass_revoke_fixture_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "os.Getenv", "os.Environ",
		"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
	},
	// #1696. Shaped like inband_bypass_revoke_names_test.go's entry above — that
	// file performs no I/O in either direction either — with three additions and
	// the same 4+2 split #1662's entry states: the first four keep a SKIP out
	// (resolveClaudeBin and WithWorktreeAuthenticated skip INSIDE the test body,
	// after `=== RUN` is printed, and a skip exits 0, which reads as a pass under
	// `make e2e-realclaude`), the environment readers are the credential guard.
	//
	// captureClaudeVersion is in no sibling entry and is here because this file's
	// SUBJECT is version tokens: it is the package's own direct `claude --version`
	// exec and it returns precisely initControlFixtureName's input, so it is the
	// exec a developer is most likely to reach for thinking "use the real token".
	// It fails loudly rather than skipping, so it would not fake a pass; what it
	// would destroy is this file's defining property, that it settles with no
	// claude binary at all. Do not harmonise it away against the siblings.
	//
	// os.LookupEnv is the two-value form of os.Getenv reading the same
	// environment, so leaving it out is a hole in the credential guard as the
	// sibling entries have it. Added here as a deliberate superset; retrofitting
	// the siblings is not this ticket's. exec.Command and exec.CommandContext were
	// declined: this file imports no os/exec, and the package's own exec helpers —
	// the ones reachable without a new import — are already covered above.
	//
	// The packageDir group is the trio plus writeFixture, for the reason the two
	// entries above state: the check is an AST identifier match, so a file calling
	// a wrapper reaches packageDir transitively while never naming it. The os
	// read/write entries and filepath.Glob close the relative-path hazard — `go
	// test` runs in the package source directory, so a relative
	// os.WriteFile("testdata/…") reaches the committed fixtures without naming any
	// wrapper — and filepath.Glob is what keeps this file's controls synthetic
	// literals rather than a directory listing. t.TempDir is absent for #1661's
	// reason rather than #1651's: this file writes nothing and needs no directory.
	//
	// filepath.Match, filepath.Join and filepath.Dir are unaffected — a dotted
	// entry is matched as a selector, so filepath.Glob bans only filepath.Glob.
	"initialize_control_names_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
		"filepath.Glob", "os.ReadFile", "os.WriteFile", "os.Create", "os.ReadDir",
	},
	// #1701. The entry above, copied whole — same seventeen names, for the reason
	// that entry states: like #1696's file, this one performs no I/O in EITHER
	// direction. It builds a record and asserts on its values. Do NOT widen it
	// toward #1702's narrower twelve, and do not narrow it toward #1662's: #1702
	// is the writer half and legitimately needs os.WriteFile, os.Create,
	// os.ReadFile, os.ReadDir and filepath.Glob, which is why it gets its own
	// file. This map is keyed by file name and each file gets exactly one entry,
	// so folding both slices into one file would mean shipping the intersection
	// and losing this file's defining property.
	//
	// captureClaudeVersion matters MORE here than it did in #1696. It is the
	// package's own direct `claude --version` exec and it returns (raw, token) —
	// which is both of initControlFixtureRecord's version fields at once — so the
	// fully-populated fixture gives a developer two separate pulls toward the one
	// call that hands over real values for both, and
	// `versionRaw, versionToken := captureClaudeVersion(t)` is already the literal
	// line four sibling files in this package use. It t.Fatalf's rather than
	// skipping, so it would not fake a pass; what it would destroy is this file's
	// defining property, that it settles with no claude binary at all — and it
	// would take the slug guard down with it, because a real token is slug-clean
	// and TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken
	// would then redden against honest code, with the check below green the whole
	// time. Do not harmonise it away against the older siblings that omit it.
	//
	// os.LookupEnv is the two-value form of os.Getenv reading the same
	// environment, which here carries CLAUDE_CODE_OAUTH_TOKEN and
	// ANTHROPIC_API_KEY; #1662's entry omits it, #1696 added it as a deliberate
	// superset, and this follows #1696. exec.Command and exec.CommandContext were
	// considered and declined for #1696's reason: this file imports no os/exec,
	// and the package's own exec helpers are already covered above.
	"initialize_control_record_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
		"filepath.Glob", "os.ReadFile", "os.WriteFile", "os.Create", "os.ReadDir",
	},
	// #1702. The write half of the same family, and the entry the two above warn
	// against copying whole: twelve names rather than seventeen. The first four
	// keep a SKIP out, as everywhere in this family — resolveClaudeBin and
	// WithWorktreeAuthenticated skip INSIDE the test body, after `=== RUN` is
	// printed, and a skip exits 0, which reads as a pass under
	// `make e2e-realclaude`.
	//
	// captureClaudeVersion is carried from the two entries above and must not be
	// harmonised away against #1662's, which omits it. It is the package's own
	// direct `claude --version` exec and returns (raw, token) — both of the
	// record's version fields AND initControlFixtureName's input — so it is the
	// exec a developer minting a name here is most likely to reach for thinking
	// "use the real token". It t.Fatalf's rather than skipping, so it would not
	// fake a pass; what it would destroy is this file's defining property, that it
	// settles with no claude binary at all, with the check below green the whole
	// time. A real token is also slug-clean, which empties the "named exactly what
	// the namer mints" assertion — the same coupling #1701's slug guard protects.
	//
	// os.LookupEnv is the two-value form of os.Getenv reading the same environment,
	// which here carries CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY; #1662 omits
	// it, #1696 added it as a deliberate superset, and this follows #1696.
	// exec.Command and exec.CommandContext were considered and declined for #1696's
	// reason: this file imports no os/exec, and the package's own exec helpers are
	// already covered above.
	//
	// The packageDir group is the trio plus writeFixture, for the reason all three
	// entries above state: the check is an AST identifier match, so a file calling
	// a wrapper reaches packageDir transitively while never naming it, and a
	// packageDir-only entry leaves the ban true and the property false.
	//
	// What is deliberately ABSENT, and where this entry copies #1662's rather than
	// its two nearer siblings': t.TempDir, os.WriteFile, os.Create, os.ReadFile,
	// os.ReadDir and filepath.Glob all stay available. Those two files perform no
	// I/O in either direction; this file's entire subject is a write, a read-back
	// and a directory listing, so banning them would be red against shipped code.
	// The relative-path hazard those bans close for them — `go test` runs in the
	// package source directory, so a relative os.WriteFile("testdata/…") reaches
	// the committed fixtures — is closed here by
	// TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry instead: a
	// writer that sent its bytes to a relative testdata/ leaves the t.TempDir()
	// holding ZERO entries, and the exactly-one-entry assertion goes red.
	"initialize_control_writer_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
	},
}

// TestFinOfflineFilesReachNoExecHelper runs the check those headers describe.
//
// It reports every offending call site rather than the first, because a file
// that acquired two is not one edit away from clean and a reader deserves to
// know that before starting.
func TestFinOfflineFilesReachNoExecHelper(t *testing.T) {
	t.Parallel()

	files := make([]string, 0, len(finOfflineExecBans))
	for f := range finOfflineExecBans {
		files = append(files, f)
	}
	sort.Strings(files)

	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			t.Parallel()

			banned := map[string]bool{}
			for _, name := range finOfflineExecBans[file] {
				banned[name] = true
			}

			fset := token.NewFileSet()
			// No parser.ParseComments: see the header above. The absence of that
			// flag is what makes this check unable to satisfy itself out of the
			// prose that names every banned symbol.
			parsed, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}

			ast.Inspect(parsed, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.SelectorExpr:
					recv, ok := node.X.(*ast.Ident)
					if !ok {
						return true
					}
					if dotted := recv.Name + "." + node.Sel.Name; banned[dotted] {
						t.Errorf("%s reaches %s at %s: that helper execs, spawns or reads the "+
							"operator's environment, which this file's header forbids",
							file, dotted, fset.Position(node.Pos()))
					}
					// The selector's own parts are not reported separately: a
					// banned bare name and a banned dotted name are different
					// rules, and t.Setenv must not answer for os.Setenv.
					return false
				case *ast.Ident:
					if banned[node.Name] {
						t.Errorf("%s reaches %s at %s: that helper execs, spawns or reads the "+
							"operator's environment, which this file's header forbids",
							file, node.Name, fset.Position(node.Pos()))
					}
				}
				return true
			})
		})
	}
}
