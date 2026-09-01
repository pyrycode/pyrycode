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
	// #1943. initialize_control_record_test.go's entry, copied WHOLE — the same
	// seventeen names, for the reason that entry states: like #1696's and #1701's
	// files, this one performs no I/O in EITHER direction. It builds a record and
	// asserts on its own literals. Copying whole rather than hand-picking is the
	// point: the check is an AST identifier match, so banning packageDir while
	// leaving its wrappers setModeFixturePath, writeSetModeFixture and writeFixture
	// unlisted leaves the ban true and the property false.
	//
	// The first five keep a SKIP out — resolveClaudeBin and
	// WithWorktreeAuthenticated skip INSIDE the test body, after `=== RUN` is
	// printed, and a skip exits 0, which reads as a pass under `make
	// e2e-realclaude`, so reaching either would convert this file's "runs and
	// passes with no credentials" into a silent skip indistinguishable from a pass
	// at the gate.
	//
	// captureClaudeVersion matters here for #1701's reason, sharpened. It is the
	// package's own direct `claude --version` exec and it returns the raw line AND
	// its leading token — which is precisely what BOTH of askQuestionFixtureRecord's
	// version fields are minted from — so it is the single call a developer
	// populating this fixture is most likely to reach for, and
	// `versionRaw, versionToken := captureClaudeVersion(t)` is already the literal
	// line four sibling files in this package use. It t.Fatalf's rather than
	// skipping, so it would not fake a pass; what it would destroy is this file's
	// defining property, that it settles with no claude binary at all — and a real
	// version line is not what the slug-shape property is written against, so it
	// would take that guard down with it while this check stayed green.
	//
	// The three environment readers are the credential guard: this process
	// environment carries CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY.
	//
	// The packageDir group plus filepath.Glob and the four os read/write names
	// fence the file off from the committed testdata/, where the eight
	// permission_protocol_* captures live. That group is the one that matters and
	// it is not tidiness: `go test` runs in the package source directory, so a
	// RELATIVE os.WriteFile("testdata/…") reaches and overwrites those captures
	// while naming no wrapper at all.
	//
	// t.TempDir is absent for #1661's reason rather than #1651's: this file writes
	// nothing and needs no directory. versionSlug, compactRawMessages and
	// fixtureFieldNonZero are all pure — a regexp substitution, a json.Compact and
	// a reflect kind switch — and are banned nowhere; this file calls all three.
	// The limit of that, stated so nobody over-reads the ban: this check is per-file
	// SYNTAX, not a call graph, so a banned read stays reachable through a helper
	// the file calls while the ban stays green. A fourth helper added later
	// inherits no such check.
	"ask_user_question_record_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
		"filepath.Glob", "os.ReadFile", "os.WriteFile", "os.Create", "os.ReadDir",
	},
	// #1944. The entry above, copied WHOLE — the same seventeen names, for the
	// reason it states: like #1696's, #1701's and #1943's files, this one performs
	// no I/O in EITHER direction. It builds two tables of literals and asserts on a
	// pure namer's output. Copying whole rather than hand-picking is the point: the
	// check is an AST identifier match, so banning packageDir while leaving its
	// wrappers setModeFixturePath, writeSetModeFixture and writeFixture unlisted
	// leaves the ban true and the property false.
	//
	// The first five keep a SKIP out — resolveClaudeBin and
	// WithWorktreeAuthenticated skip INSIDE the test body, after `=== RUN` is
	// printed, and a skip exits 0, which reads as a pass under `make
	// e2e-realclaude`.
	//
	// captureClaudeVersion is the one a developer in THIS file is most likely to
	// reach for, more so than in #1943's: it is the package's own `claude --version`
	// exec and it returns exactly the token askQuestionFixtureName takes, so "use
	// the real token" is one line away. It t.Fatalf's rather than skipping, so it
	// would not fake a pass; what it would destroy is this file's defining property,
	// that it settles with no claude binary at all. The token arrives as a PARAMETER
	// here and #1938's live run is what supplies a real one.
	//
	// The three environment readers are the credential guard: this process
	// environment carries CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY, and no
	// table row or failure message in that file may be sourced from it.
	//
	// The packageDir group plus filepath.Glob and the four os read/write names are
	// what keep that file's pattern controls SYNTHETIC LITERALS rather than a
	// directory listing, which is AC 3's own requirement rather than tidiness — and
	// they close the shortest route besides: `go test` runs in the package source
	// directory, so a RELATIVE os.WriteFile("testdata/…") reaches the seventeen
	// committed captures while naming no wrapper at all.
	//
	// t.TempDir is absent for #1661's reason rather than #1651's: that file writes
	// nothing and needs no directory. versionSlug, filepath.Match, filepath.Join,
	// filepath.Dir, anchorFixtureName and strings.Repeat are all pure and banned
	// nowhere; it calls all six. A dotted entry is matched as a selector, so
	// filepath.Glob bans only filepath.Glob and leaves the other three alone.
	"ask_user_question_names_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
		"filepath.Glob", "os.ReadFile", "os.WriteFile", "os.Create", "os.ReadDir",
	},
	// #1941. The write half of the same family, and the entry the two above warn
	// against copying whole: FOURTEEN names rather than seventeen. It copies
	// initialize_control_writer_test.go's entry — this table's only other writer
	// entry — name for name.
	//
	// Do NOT copy the count sentence from that entry along with its names: its
	// comment says "twelve names rather than seventeen" and then lists fourteen,
	// because #1748 added newDropcapScanner and realHome without updating the
	// prose. Fourteen is this entry's number, counted against the literal below.
	//
	// The first five keep a SKIP out, as everywhere in this family —
	// resolveClaudeBin and WithWorktreeAuthenticated skip INSIDE the test body,
	// after `=== RUN` is printed, and a skip exits 0, which reads as a pass under
	// `make e2e-realclaude`, which is AC 5's real hazard. captureClaudeVersion
	// t.Fatalf's rather than skipping, so it would not fake a pass; what it would
	// destroy is this file's defining property, that it settles with no claude
	// binary at all. It returns (raw, token) — both of the record's version fields
	// AND the input askQuestionFixtureName takes — so it is the exec a developer
	// minting a name here is most likely to reach for thinking "use the real
	// token".
	//
	// The three environment readers are the credential guard: this process
	// environment carries CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY.
	//
	// The packageDir group is the trio plus writeFixture, for the reason every
	// entry above states: the check is a per-file AST identifier match and not a
	// call graph, so a file calling a WRAPPER reaches packageDir transitively while
	// never naming it, and a packageDir-only entry leaves the ban true and the
	// property false. writeFixture matters most of the four here — it is the
	// package's other fixture writer, it hardcodes permission_protocol_v%s.json,
	// resolves the real testdata/ through packageDir and performs NO scan at all,
	// so a single call would write an unscanned artifact over a committed capture
	// from the file whose whole subject is refusing to.
	//
	// What is deliberately ABSENT, and where this entry copies the sibling writer's
	// rather than its two nearer siblings': t.TempDir, os.WriteFile, os.Create,
	// os.ReadFile, os.ReadDir and filepath.Glob all stay available. Those two files
	// perform no I/O in either direction; this file's entire subject is a write, a
	// read-back and a directory listing, so banning them would be red against
	// shipped code. The relative-path hazard those bans close for them — `go test`
	// runs in the package source directory, so a relative os.WriteFile("testdata/…")
	// reaches the seventeen committed captures — is closed here differently, by
	// TestAskQuestionFixture_RoundTripsEveryFieldIntoOneNamedEntry: a writer that
	// sent its bytes to a relative testdata/ leaves the t.TempDir() holding ZERO
	// entries, and the exactly-one-entry assertion goes red.
	//
	// newDropcapScanner and realHome are the deterministic fabric behind the
	// scanner PARAMETER, and the ban is what makes that parameter mean something.
	// The writer runs a deny-scan over the marshalled record before its first
	// filesystem call and the scanner arrives from its caller; a file that built
	// its own would call a constructor reading os.Getenv twice and realHome,
	// satisfying this entry's os.Getenv ban to the letter while destroying the
	// offline property it protects — a table built through it is green or red
	// depending on whose machine ran it. The check matches a bare *ast.Ident as
	// well as a dotted selector, so both the wrapper and the plain realHome
	// reference are caught.
	//
	// os.TempDir is deliberately NOT added, matching the sibling writer's entry and
	// unlike #1732's: newDropcapScanner takes tempHome, artifactDir and workdir as
	// parameters and reads only realHome and the environment on its own, so
	// os.TempDir is not a route to anything here and would be a ban name with no
	// hazard behind it.
	"ask_user_question_writer_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
		"newDropcapScanner", "realHome",
	},
	// #1951. ask_user_question_record_test.go's entry, copied WHOLE — the same
	// seventeen names, for the reason that entry states: like #1943's and #1944's
	// files, this one performs no I/O in EITHER direction. It builds synthetic
	// literals and asserts on a pure check's returned findings. Do NOT copy the
	// entry immediately above instead: that is #1941's FOURTEEN, deliberately
	// narrower because that file writes a directory, and it is the entry its own
	// comment calls the one the two above it warn against copying whole. This file
	// is the same family and the wrong one to inherit from.
	//
	// The first five keep a SKIP out — resolveClaudeBin and
	// WithWorktreeAuthenticated skip INSIDE the test body, after `=== RUN` is
	// printed, and a skip exits 0, which reads as a pass under
	// `make e2e-realclaude`.
	//
	// captureClaudeVersion returns the raw line AND its leading token at once —
	// both of askQuestionFixtureRecord's version fields — so it is the exec a
	// developer populating a fixture here reaches for first. It t.Fatalf's rather
	// than skipping, so it would not fake a pass; what it would destroy is this
	// file's defining property, that it settles with no claude binary at all.
	//
	// The three environment readers are the credential guard: this process
	// environment carries CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY.
	//
	// The packageDir group plus filepath.Glob and the four os names matter MORE
	// here than in either entry this one copies, because a shape assertion is the
	// one place where "prove it against a real capture" is the natural next
	// thought. Two reasons it must not. First, AC 3: this file's whole claim is
	// that it settles offline. Second, it would be RED ON ARRIVAL — no committed
	// permission_protocol_* capture holds an AskUserQuestion tool_use block, only
	// the tool's name in the `system`/`init` tools array. And `go test` runs in the
	// package source directory, so a RELATIVE os.ReadFile("testdata/…") reaches
	// those captures while naming no wrapper at all, which is why the wrappers are
	// listed alongside packageDir itself.
	//
	// t.TempDir is absent for #1661's reason rather than #1651's: this file writes
	// nothing and needs no directory. The only in-package helper it calls is
	// askQuestionFullRecord, which is pure literals and is banned nowhere. The
	// limit of all of it, stated so nobody over-reads the ban: this check is
	// per-file SYNTAX, not a call graph, so a banned read stays reachable through a
	// helper the file calls while the ban stays green.
	"ask_user_question_shape_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
		"filepath.Glob", "os.ReadFile", "os.WriteFile", "os.Create", "os.ReadDir",
	},
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
	//
	// newDropcapScanner and realHome are #1748's two additions, and they are the
	// deterministic fabric behind that slice's scanner PARAMETER — #1732's entry
	// below added realHome/os.TempDir for exactly this reason. The writer runs a
	// deny-scan over the marshalled record before its first filesystem call, and the
	// scanner arrives from its caller; a file that built its own would call a
	// constructor reading os.Getenv twice and realHome, satisfying this entry's
	// os.Getenv ban to the letter while destroying the offline property it protects.
	// The check matches a bare *ast.Ident as well as a dotted selector, so both the
	// wrapper and the plain realHome reference are caught.
	//
	// os.TempDir is deliberately NOT added, unlike #1732's entry: newDropcapScanner
	// takes tempHome, artifactDir and workdir as parameters and reads only realHome
	// and the environment on its own, so os.TempDir is not a route to anything here
	// and would be a ban name with no hazard behind it.
	"initialize_control_writer_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
		"newDropcapScanner", "realHome",
	},
	// #1732. The same family's standing eight — the first four keep a SKIP out
	// (resolveClaudeBin and WithWorktreeAuthenticated skip INSIDE the test body,
	// after `=== RUN` is printed, and a skip exits 0, which reads as a pass under
	// `make e2e-realclaude`), captureClaudeVersion follows the three entries above
	// and must not be harmonised away against #1662's, and the three environment
	// readers are the credential guard for a process environment carrying
	// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY.
	//
	// realHome and os.TempDir are this entry's two additions and the reason it
	// exists. They are precisely the two ambient reads newDropcapRedactor performs
	// on its own, and newInitControlRedactor's whole subject is taking both as
	// parameters instead — so a rule exists if and only if a caller handed a value
	// for it. The check matches a bare *ast.Ident as well as a dotted selector, so
	// the plain realHome reference is caught.
	//
	// The ban and the file's own armed-values assertion are different fabric and
	// neither substitutes for the other: this check is per-file SYNTAX, not a call
	// graph, so realHome and os.TempDir() stay reachable through a helper the file
	// calls while the ban stays green. TestInitControlRedactorArmsOnlyItsCallersValues
	// reddens on such a rule however many hops away the read happened, and it
	// cannot see a direct reference that arms nothing.
	//
	// What is deliberately ABSENT, and where this entry must NOT copy its three
	// nearest siblings: the packageDir group and the os read/write group. Those
	// entries fence a file off from the committed testdata/ because their subject
	// IS a fixture, and `go test` runs in the package source directory so a
	// relative os.WriteFile("testdata/…") reaches the real artifacts. This file has
	// no writer, no reader and no fixture — it builds a table and substitutes into
	// byte slices — so the group would ban names the file has no route to anyway.
	//
	// t.TempDir is absent for #1651's reason rather than #1661's: initControlDivergentDir
	// has to create its directory somewhere, and that is where. filepath.EvalSymlinks,
	// os.Symlink and os.MkdirAll stay available for the same reason — they are that
	// helper's own mechanism, and the symlink it follows is one the test created
	// inside its own t.TempDir(), never derived from an argument or the environment.
	"initialize_control_redaction_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"realHome", "os.TempDir",
	},
	// #1762. initialize_control_record_test.go's entry, copied whole — the same
	// seventeen names, for the reason that entry states: like #1696's and #1701's
	// files, this one performs no I/O in EITHER direction. It builds line literals
	// and asserts on a returned struct. The first five keep a SKIP out
	// (resolveClaudeBin and WithWorktreeAuthenticated skip INSIDE the test body,
	// after `=== RUN` is printed, and a skip exits 0, which reads as a pass under
	// `make e2e-realclaude`), the three environment readers are the credential
	// guard, and the packageDir group is the trio plus writeFixture because the
	// check is an AST identifier match — a file calling a wrapper reaches
	// packageDir transitively while never naming it.
	//
	// The os read/write group and filepath.Glob are what this entry is most FOR.
	// The committed testdata/initialize_control_v2.1.239.json is where a window
	// read is checked BY HAND, and `go test` runs in the package source directory
	// — so a relative os.ReadFile("testdata/…") reaches that capture without
	// naming any wrapper, and that read is precisely the shortcut a developer
	// building this table is tempted by. It would turn a table of literals into a
	// test that reads the artifact it exists to justify, and the reader's whole
	// claim is that it touches no filesystem.
	//
	// t.TempDir is absent for #1661's reason rather than #1651's: this file writes
	// nothing and needs no directory. os.Open is deliberately declined — no
	// sibling entry carries it, the family's five os names are the established
	// set, and a ban name wants a hazard behind it, on the ground #1732's entry
	// declines os.TempDir.
	"initialize_control_window_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"packageDir", "setModeFixturePath", "writeSetModeFixture", "writeFixture",
		"filepath.Glob", "os.ReadFile", "os.WriteFile", "os.Create", "os.ReadDir",
	},
	// #1764. The entry above with three names DROPPED and one added, and the
	// dropped three are why this comment cannot be short: every sibling entry in
	// this family bans the route to the real testdata/, and a reader who skims will
	// take their absence here for an oversight. This is the FIRST offline file in
	// this package with a legitimate reason to read a committed fixture — its whole
	// subject is the three committed arm captures — so packageDir, filepath.Glob and
	// os.ReadFile are the file's own mechanism rather than a hazard. It resolves
	// packageDir through neither the name nor a wrapper: `go test` runs in the
	// package source directory, so initControlArmFixtureGlob's relative testdata/
	// prefix resolves with no wrapper at all, which is why packageDir goes with the
	// other two rather than staying banned.
	//
	// The relative-path hazard the sibling entries close is in the WRITE direction,
	// and this file reads and must never write — so os.WriteFile, os.Create,
	// writeFixture, writeSetModeFixture and setModeFixturePath all stay banned, and
	// writeInitControlFixture is added: it is this family's own writer and the one a
	// developer in this file is most likely to reach for. It mints its own path
	// internally from packageDir, so a single call would overwrite a committed
	// capture from a file whose entire claim is that it only reads.
	//
	// os.ReadDir stays banned although this file does not take it: filepath.Glob and
	// os.ReadDir are two ways to the same listing, and this package's own precedent
	// — #1732's entry declining os.TempDir, #1762's declining os.Open — is that a
	// ban name wants a hazard behind it, not that an unused one has to go. The
	// hazard here is a second, unanchored listing route past AC 4's `_` exclusion,
	// which is what keeps #1688's one-arm capture out of the read set.
	//
	// The first five keep a SKIP out, as everywhere in this family — resolveClaudeBin
	// and WithWorktreeAuthenticated skip INSIDE the test body, after `=== RUN` is
	// printed, and a skip exits 0, which reads as a pass under `make e2e-realclaude`.
	// captureClaudeVersion matters here specifically: it is the shortcut that would
	// replace this file's version grouping with a live `claude --version`, destroying
	// the offline property while every check stayed green. The three environment
	// readers are the credential guard for a process environment carrying
	// CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY.
	//
	// t.TempDir is absent for #1661's reason rather than #1651's: this file writes
	// nothing and needs no directory of its own.
	"initialize_control_compare_test.go": {
		"resolveClaudeBin", "WithWorktreeAuthenticated", "WithWorktree",
		"probeClaudeVersion", "captureClaudeVersion",
		"os.Getenv", "os.Environ", "os.LookupEnv",
		"setModeFixturePath", "writeSetModeFixture", "writeFixture", "writeInitControlFixture",
		"os.WriteFile", "os.Create", "os.ReadDir",
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
