package claudetape

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestConversationWorkerCanonicalProductionArgv(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "..", "..", "..", "cmd", "pyry", "reply_fallback.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	constants := map[string]ast.Expr{}
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
			for _, spec := range gen.Specs {
				if v, ok := spec.(*ast.ValueSpec); ok && len(v.Names) == 1 && len(v.Values) == 1 {
					constants[v.Names[0].Name] = v.Values[0]
				}
			}
		}
	}
	var value func(ast.Expr) string
	value = func(e ast.Expr) string {
		switch n := e.(type) {
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				s, err := strconv.Unquote(n.Value)
				if err == nil {
					return s
				}
			}
		case *ast.BinaryExpr:
			if n.Op == token.ADD {
				return value(n.X) + value(n.Y)
			}
		case *ast.Ident:
			if e, ok := constants[n.Name]; ok {
				return value(e)
			}
		}
		t.Fatal("production fallback argv is no longer a closed literal declaration")
		return ""
	}
	var actual []string
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "replyFallbackArgs" {
			if fn.Body == nil || len(fn.Body.List) != 1 {
				t.Fatal("fallback argv requires a new source review")
			}
			ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				t.Fatal("fallback argv return changed")
			}
			list, ok := ret.Results[0].(*ast.CompositeLit)
			if !ok {
				t.Fatal("fallback argv not literal")
			}
			for _, e := range list.Elts {
				actual = append(actual, value(e))
			}
		}
	}
	if !reflect.DeepEqual(actual, conversationWorkerArgs()) {
		t.Fatal("complete production fallback argv, including prompt bytes, changed")
	}
}

func conversationWorkerPolicyControl() *LifecycleSuite {
	return promptSuiteControl()
}
func cloneConversationControl(s *LifecycleSuite) *LifecycleSuite {
	raw, _ := json.Marshal(s)
	var x LifecycleSuite
	_ = json.Unmarshal(raw, &x)
	return &x
}
func TestConversationWorkerPolicyReportsIndependentLifecycles(t *testing.T) {
	a := conversationWorkerPolicyControl()
	b := cloneConversationControl(a)
	b.Workers[0], b.Workers[1] = b.Workers[1], b.Workers[0]
	expected := LifecycleProfiles()[a.Scenario].Roles
	report, err := CompareLifecycleSuite(a, b, expected, ConversationWorkerPolicy)
	if err != nil || report.BaselineWorkers == nil || report.FreshWorkers == nil || len(*report.FreshWorkers) != 2 || !reflect.DeepEqual(report.BaselineWorkers, report.FreshWorkers) {
		t.Fatal("settled source facts changed", err)
	}
	if SameLifecycleSuiteContract(a, b, expected) == nil {
		t.Fatal("strict ordered replay ledger weakened")
	}

	for _, scenario := range []string{"fallback-suggestion", "session-resume", "conversation-lifecycle"} {
		x := cloneConversationControl(a)
		x.Scenario = scenario
		if _, err := CompareLifecycleSuite(x, x, expected, ConversationWorkerPolicy); err == nil {
			t.Fatal("policy escaped its original prompt/routing oracles")
		}
	}
}
func TestConversationWorkerPolicyRejectsUnmatchedForeignEvidence(t *testing.T) {
	a := conversationWorkerPolicyControl()
	zero := cloneConversationControl(a)

	for _, kind := range []string{"prompt", "argv", "source", "sequence", "duplicate", "third", "failed", "signal", "open-eof", "cancel-unproved", "incomplete-source", "changed-user", "changed-main-prompt", "absent", "partial", "native-cancel", "wrapper-cancel", "missing-settlement", "wrong-settlement", "native-suggestion"} {
		t.Run(kind, func(t *testing.T) {
			x := cloneConversationControl(a)
			switch kind {
			case "absent":
				x.Workers = nil
			case "partial":
				x.Workers = x.Workers[:1]
			case "native-cancel":
				x.Workers[0].Tape.Status = FallbackWorkerStatus{ExitCode: -1, Signal: 9}
			case "wrapper-cancel":
				x.Workers[0].ProductionWait = &FallbackProductionWait{Status: FallbackWorkerStatus{ExitCode: -1, Signal: 9}, GroupCancel: true}
			case "missing-settlement":
				x.Settlement = nil
			case "wrong-settlement":
				x.Settlement.Sources = []string{"bare/1", "prompted/2"}
			case "native-suggestion":
				x.Roles["bare"].Tape.Frames = append(x.Roles["bare"].Tape.Frames, Frame{Direction: "stdout", Data: json.RawMessage(`{"type":"prompt_suggestion","suggestion":"A competing native suggestion"}`)})
			case "prompt":
				x.Workers[0].Tape.Args[len(x.Workers[0].Tape.Args)-1] = "unmatched changed system prompt"
			case "argv":
				x.Workers[0].Tape.Args[2] = "changed-model"
			case "source":
				x.Workers[0].SourceRole = "bootstrap"
			case "sequence":
				x.Workers[0].UserSequence = 2
			case "duplicate":
				x.Workers = append(x.Workers, x.Workers[0])
			case "third":
				x.Workers = append(x.Workers, x.Workers[0], x.Workers[0])
			case "failed":
				x.Workers[0].Tape.Status.ExitCode = 7
			case "signal":
				x.Workers[0].Tape.Status = FallbackWorkerStatus{ExitCode: -1, Signal: 15}
			case "open-eof":
				x.Workers[0].Tape.StdoutEOF = false
			case "cancel-unproved":
				x.Workers[0].ProductionWait = &FallbackProductionWait{Status: FallbackWorkerStatus{ExitCode: -1, Signal: 9}}
			case "incomplete-source":
				x.Roles["prompted"].Tape.Frames = nil
			case "changed-user":
				x.Workers[0].Tape.Input = `{"user":"foreign user","assistant":"Would you like a Go test?"}`
			case "changed-main-prompt":
				x.Roles["prompted"].Tape.Files["--append-system-prompt-file"] = json.RawMessage(`{"content":"foreign prompt","mode":384}`)
			}
			if _, err := CompareLifecycleSuite(zero, x, LifecycleProfiles()[a.Scenario].Roles, ConversationWorkerPolicy); err == nil {
				t.Fatal("unmatched foreign/partial edge accepted")
			}
		})
	}
}
