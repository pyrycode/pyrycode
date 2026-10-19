package scenario

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestSourceProtocolControls(t *testing.T) {
	for _, name := range []string{"context-absent-valid", "context-outside-valid", "context-source-id", "context-wrong-tool", "context-outside-reason", "context-presence", "context-reconnect", "context-dismissal", "context-denial", "context-created-before", "context-created-after", "native-valid", "native-no-owned-delta", "native-empty", "native-foreign", "native-zero-revision", "native-no-source", "native-null-omitted", "native-clear-string", "native-old-revision", "native-number"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSourceProtocolHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_SOURCE_PROTOCOL_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || (err == nil) != strings.HasSuffix(name, "-valid") {
				t.Fatalf("source protocol acceptance err=%v output=%s", err, out)
			}
		})
	}
}
func TestSourceProtocolHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_SOURCE_PROTOCOL_CONTROL")
	if name == "" {
		return
	}
	budget := 40 * time.Millisecond
	frame := func(kind string, p any) protocol.Envelope {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw}
	}
	var rows []protocol.Envelope
	sequence := uint64(0)
	turns := 0
	next := func(deadline time.Time) protocol.Envelope {
		if len(rows) > 0 {
			env := rows[0]
			rows = rows[1:]
			return env
		}
		time.Sleep(time.Until(deadline))
		return protocol.Envelope{}
	}
	if strings.HasPrefix(name, "context-") {
		target := filepath.Join(t.TempDir(), "permission-context-witness.txt")
		outside := name == "context-outside-valid" || name == "context-outside-reason"
		source := PermissionContextSource{Type: "control_request", RequestID: "request"}
		source.Request.ToolUseID = "use"
		source.Request.ToolName = "Bash"
		source.Request.Input.Command = "printf hello > permission-context-witness.txt"
		if outside {
			source.Request.ToolName = "Write"
			source.Request.Input.FilePath = target
			source.Request.ReasonType = json.RawMessage(`"workingDir"`)
			source.Request.Reason = json.RawMessage(`"outside directory"`)
		}
		if name == "context-source-id" {
			source.RequestID = ""
		}
		if name == "context-wrong-tool" {
			source.Request.ToolName = "Other"
		}
		if name == "context-outside-reason" {
			source.Request.ReasonType = json.RawMessage(`"unknown"`)
		}
		shown := protocol.ModalShownPayload{ConversationID: "conv", ModalID: "modal", Class: "permission", Reason: source.Request.Reason}
		if outside {
			shown.ReasonType = "workingDir"
		}
		if name == "context-presence" {
			shown.Reason = json.RawMessage(`null`)
		}
		if name == "context-created-before" {
			if err := os.WriteFile(target, []byte("unapproved"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		w := Wire{Next: next, Send: func(kind string, p any) uint64 {
			sequence++
			if kind == protocol.TypeSendMessage {
				rows = append(rows, frame(protocol.TypeModalShown, shown))
			}
			if kind == protocol.TypeModalAnswer {
				answer := p.(protocol.ModalAnswerPayload)
				if answer.ModalID != "modal" || answer.OptionID != "reject_once" || answer.AnswerToken != "permission-context-deny" {
					t.Fatal("permission context answer changed")
				}
				owner := "remote"
				if name == "context-dismissal" {
					owner = "local"
				}
				rows = append(rows, frame(protocol.TypeModalDismissed, protocol.ModalDismissedPayload{ModalID: "modal", Source: owner, Outcome: "reject_once"}), frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: "idle"}))
				if name == "context-created-after" {
					if err := os.WriteFile(target, []byte("executed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			return sequence
		}}
		absent := 0
		PermissionContext(t, w, PermissionContextConfig{ConversationID: "conv", Target: target, Prompt: "authored permission prompt", Outside: outside, SurfaceBudget: budget, TurnBudget: budget, Reconnect: func() {
			again := shown
			if name == "context-reconnect" {
				again.ModalID = "different"
			}
			rows = append(rows, frame(protocol.TypeModalShown, again))
		}, RequireAbsent: func() {
			absent++
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("denied target exists or absence not proved")
			}
		}, Source: func(kind string) PermissionContextSource {
			if kind == "control_request" {
				return source
			}
			id := "use"
			if name == "context-denial" {
				id = "other"
			}
			raw, _ := json.Marshal(map[string]any{"type": "result", "permission_denials": []map[string]string{{"tool_use_id": id}}})
			var result PermissionContextSource
			if json.Unmarshal(raw, &result) != nil {
				t.Fatal("invalid test result")
			}
			return result
		}})
		if absent != 2 {
			t.Fatal("permission absence witnesses missing")
		}
		return
	}
	nativeChecked := 0
	w := Wire{Next: next, Send: func(kind string, p any) uint64 {
		sequence++
		if kind != protocol.TypeSendMessage {
			t.Fatal("native suggestion sent an unexpected client action")
		}
		turns++
		if turns == 1 {
			conv := "conv"
			if name == "native-no-owned-delta" {
				conv = "foreign"
			}
			rows = append(rows, frame(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: conv, Text: "reply"}), frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: "idle"}))
			text := "Add a test"
			rev := uint64(5)
			conv = "conv"
			if name == "native-empty" {
				text = " "
			}
			if name == "native-foreign" {
				conv = "foreign"
			}
			if name == "native-zero-revision" {
				rev = 0
			}
			rows = append(rows, frame(protocol.TypeReplySuggestion, map[string]any{"conversation_id": conv, "revision": rev, "suggested_reply": text}))
		} else {
			if p.(protocol.SendMessagePayload).Text != NativeSuggestionClearPrompt {
				t.Fatal("native clear turn changed")
			}
			value := any(nil)
			rev := 6
			if name == "native-clear-string" {
				value = ""
			}
			if name == "native-old-revision" {
				rev = 5
			}
			if name == "native-number" {
				value = 42
			}
			body := map[string]any{"conversation_id": "conv", "revision": rev, "suggested_reply": value}
			if name == "native-null-omitted" {
				delete(body, "suggested_reply")
			}
			rows = append(rows, frame(protocol.TypeReplySuggestion, body))
		}
		return sequence
	}}
	NativeSuggestions(t, w, NativeSuggestionConfig{ConversationID: "conv", Prompts: []string{"Write code"}, TurnBudget: budget, Window: budget, CheckNative: func() {
		nativeChecked++
		if name == "native-no-source" {
			t.Fatal("no native source")
		}
	}})
	if nativeChecked != 1 || turns != 2 {
		t.Fatal("native source or clear turn was not checked")
	}
}
