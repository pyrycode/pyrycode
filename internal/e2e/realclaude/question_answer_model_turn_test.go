//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestQuestionAnswerModelTurnWiring(t *testing.T) {
	t.Parallel()
	file, err := parser.ParseFile(token.NewFileSet(), "interactive_stream_question_answer_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "driveSharedQuestionAnswer" {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "drainQuestionModelTurn" {
					found = true
				}
			}
			return true
		})
		if !found {
			t.Fatal("shared live question driver must use the controlled model-turn drain")
		}
		return
	}
	t.Fatal("shared live question driver not found")
}

func TestQuestionAnswerModelTurnControls(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, failure string }{
		{"valid", ""}, {"foreign_then_valid", ""}, {"warning", ""}, {"queued_idle_then_valid", ""},
		{"foreign_only", "no model_announced"}, {"missing_model", "no model_announced"},
		{"wrong_model", "post-question model"}, {"first_owned_wrong", "post-question model"},
		{"malformed_model", "model"},
		{"unrecognized", "unrecognized provider message"}, {"error", "question error envelope"},
		{"unknown_rate", "rate-limit status"}, {"missing_rate", "rate-limit status"},
		{"null_rate", "rate-limit status"}, {"wrong_rate_type", "rate-limit status"},
		{"malformed_rate", "rate-limit status"}, {"modal", "unexpected modal_shown"},
		{"missing_reply", "M1:"}, {"foreign_reply", "M1:"}, {"missing_idle", "M2:"},
		{"foreign_idle", "M2:"}, {"queued_idle_only", "M2:"},
		{"omitted_session", "M2:"}, {"foreign_session", "M2:"}, {"null_session", "M2:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQuestionAnswerModelTurnHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_QUESTION_MODEL_TURN_CASE="+tc.name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("control exceeded deadline: %v; output=%q", ctx.Err(), out)
			}
			if tc.failure == "" {
				if err != nil || !strings.Contains(string(out), "model turn completed") {
					t.Fatalf("valid turn failed: %v; output=%q", err, out)
				}
			} else if err == nil || !strings.Contains(string(out), tc.failure) {
				t.Fatalf("invalid turn must fail with %q: %v; output=%q", tc.failure, err, out)
			}
			if tc.name == "unrecognized" || tc.name == "modal" {
				if len(out) > questionTextLogCap+1500 || !strings.Contains(string(out), `\n`) {
					t.Fatalf("diagnostics must be capped and quoted: output=%q", out)
				}
			}
		})
	}
}

func TestQuestionAnswerModelTurnHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_QUESTION_MODEL_TURN_CASE")
	if name == "" {
		return
	}
	model := func(conv, value string) protocol.Envelope {
		return protocol.Envelope{Type: protocol.TypeModelAnnounced, Payload: mustJSON(t, protocol.ModelAnnouncedPayload{ConversationID: conv, Model: value})}
	}
	delta := func(conv string) protocol.Envelope {
		return protocol.Envelope{Type: protocol.TypeAssistantDelta, Payload: mustJSON(t, protocol.AssistantDeltaPayload{ConversationID: conv, TurnID: "turn", Text: "ok"})}
	}
	idle := func(conv string) protocol.Envelope {
		return protocol.Envelope{Type: protocol.TypeTurnState, SessionID: json.RawMessage(`"fixture"`), Payload: mustJSON(t, protocol.TurnStatePayload{ConversationID: conv, State: "idle"})}
	}
	m, d, end := model("conv", "target"), delta("conv"), idle("conv")
	frames := []protocol.Envelope{m, d, end}
	alarm := protocol.Envelope{Type: protocol.TypeRateLimited}
	switch name {
	case "valid":
	case "foreign_only":
		frames[0] = model("other", "target")
	case "foreign_then_valid":
		frames = append([]protocol.Envelope{model("other", "wrong")}, frames...)
	case "missing_model":
		frames = frames[1:]
	case "wrong_model":
		frames[0] = model("conv", "wrong")
	case "first_owned_wrong":
		frames = append([]protocol.Envelope{model("conv", "wrong")}, frames...)
	case "malformed_model":
		frames[0].Payload = json.RawMessage(`{"model":7}`)
	case "warning", "unknown_rate", "missing_rate", "null_rate", "wrong_rate_type", "malformed_rate":
		alarm.Payload = json.RawMessage(map[string]string{
			"warning": `{"status":"allowed_warning","conversation_id":"other"}`, "unknown_rate": `{"status":"unknown","conversation_id":"other"}`,
			"missing_rate": `{}`, "null_rate": `{"status":null}`, "wrong_rate_type": `{"status":7}`, "malformed_rate": `[]`,
		}[name])
		frames = append([]protocol.Envelope{alarm}, frames...)
	case "unrecognized", "modal", "error":
		alarm.Type = map[string]string{"unrecognized": protocol.TypeUnrecognizedMessage, "modal": protocol.TypeModalShown, "error": protocol.TypeError}[name]
		alarm.Payload = mustJSON(t, map[string]string{"conversation_id": "other", "raw": "\n" + strings.Repeat("x", questionTextLogCap*2)})
		frames = append([]protocol.Envelope{alarm}, frames...)
	case "missing_reply":
		frames = []protocol.Envelope{m, end}
	case "foreign_reply":
		frames[1] = delta("other")
	case "missing_idle":
		frames = frames[:2]
	case "omitted_session", "foreign_session", "null_session":
		frames[2].SessionID = map[string]json.RawMessage{"omitted_session": nil, "foreign_session": json.RawMessage(`"foreign"`), "null_session": json.RawMessage(`null`)}[name]
	case "queued_idle_only":
		frames = []protocol.Envelope{m, end, d}
	case "foreign_idle":
		frames[2] = idle("other")
	case "queued_idle_then_valid":
		frames = append([]protocol.Envelope{end}, frames...)
	default:
		t.Fatalf("unknown control %q", name)
	}
	send, recv := completedTurnCipherPair(t)
	phone := newCompletedSourceFrames(t, send, frames)
	drainQuestionModelTurn(t, phone, recv, "conv", "target", 50*time.Millisecond, scenario.SourceFacts(func(string) []history.Entry { return phone.source }))
	if len(phone.frames) != 0 {
		t.Fatal("drain left frames unread")
	}
	t.Log("model turn completed")
}
