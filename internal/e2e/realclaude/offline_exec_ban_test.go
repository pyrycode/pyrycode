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
