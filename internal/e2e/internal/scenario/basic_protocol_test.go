package scenario

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestBasicProtocolControls(t *testing.T) {
	for _, name := range []string{"announce-valid", "announce-wrong-model", "announce-alias", "announce-foreign", "announce-log-leak", "announce-missing", "announce-empty-delta", "announce-no-idle", "reject-valid", "reject-collision", "reject-empty-menu", "reject-dropped-menu", "reject-truncated-value", "reject-wrong-code", "reject-retryable", "reject-changed-model", "reject-wrong-correlation", "tool-valid", "tool-unrecognized", "tool-rate-limit", "tool-no-write", "tool-no-result", "tool-foreign-result", "tool-foreign-delta"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBasicProtocolHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_BASIC_PROTOCOL_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			valid := strings.HasSuffix(name, "-valid") || name == "reject-collision"
			if ctx.Err() != nil || (err == nil) != valid {
				t.Fatalf("protocol evidence acceptance err=%v output=%s", err, out)
			}
		})
	}
}

func TestBasicProtocolHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_BASIC_PROTOCOL_CONTROL")
	if name == "" {
		return
	}
	frame := func(kind string, value any) protocol.Envelope {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw}
	}
	announce := protocol.ModelAnnouncedPayload{ConversationID: "conv", Model: "resolved"}
	if name == "announce-wrong-model" || name == "reject-changed-model" {
		announce.Model = "different"
	}
	if name == "announce-alias" {
		announce.Model = "haiku"
	}
	if name == "announce-foreign" {
		announce.ConversationID = "other"
	}
	delta := protocol.AssistantDeltaPayload{ConversationID: "conv", Text: "reply"}
	if name == "announce-empty-delta" {
		delta.Text = " "
	}
	if name == "tool-foreign-delta" {
		delta.ConversationID = "other"
	}
	turn := func() []protocol.Envelope {
		rows := []protocol.Envelope{frame(protocol.TypeModelAnnounced, announce), frame(protocol.TypeAssistantDelta, delta), frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: "idle"})}
		if name == "announce-missing" {
			rows = rows[1:]
		}
		if name == "announce-no-idle" {
			rows = rows[:2]
		}
		return rows
	}
	var frames []protocol.Envelope
	sends := uint64(0)
	wire := Wire{Send: func(kind string, payload any) uint64 {
		sends++
		switch kind {
		case protocol.TypeRequestModelList:
			p, ok := payload.(protocol.RequestModelListPayload)
			if !ok || p.ConversationID != "conv" {
				t.Fatal("menu request lost conversation")
			}
			menu := protocol.ModelListPayload{Models: []protocol.ModelOption{{Value: "haiku"}}}
			if name == "reject-collision" {
				menu.Models = append(menu.Models, protocol.ModelOption{Value: "claude-no-such-model-2281-0"})
			}
			if name == "reject-empty-menu" {
				menu.Models = nil
			}
			if name == "reject-dropped-menu" {
				menu.DroppedModels = 1
			}
			if name == "reject-truncated-value" {
				menu.Models[0].TruncatedFields = []string{"value"}
			}
			env := frame(protocol.TypeModelList, menu)
			id := sends
			env.InReplyTo = &id
			frames = append(frames, env)
		case protocol.TypeSetSessionSettings:
			p, ok := payload.(protocol.SetSessionSettingsPayload)
			candidate := "claude-no-such-model-2281-0"
			if name == "reject-collision" {
				candidate = "claude-no-such-model-2281-1"
			}
			if !ok || p.SessionID != "session" || p.Model == nil || *p.Model != candidate || p.Effort == nil || *p.Effort != "high" || p.PermissionMode == nil || *p.PermissionMode != "plan" {
				t.Fatal("rejected settings did not retain the complete submitted request")
			}
			rejection := protocol.ErrorPayload{Code: protocol.CodeProtocolMalformed}
			if name == "reject-wrong-code" {
				rejection.Code = protocol.CodeModelListUnavailable
			}
			if name == "reject-retryable" {
				rejection.Retryable = true
			}
			env := frame(protocol.TypeError, rejection)
			id := sends
			if name == "reject-wrong-correlation" {
				id++
			}
			env.InReplyTo = &id
			frames = append(frames, env)
		case protocol.TypeSendMessage:
			p, ok := payload.(protocol.SendMessagePayload)
			if !ok || p.ConversationID != "conv" || p.Text != "prompt" {
				t.Fatal("trigger lost exact request")
			}
			if strings.HasPrefix(name, "tool-") {
				for _, tool := range []string{"Write", "Read"} {
					if name == "tool-no-write" && tool == "Write" {
						continue
					}
					frames = append(frames, frame(protocol.TypeToolUse, protocol.ToolUsePayload{ConversationID: "conv", Name: tool, ToolUseID: tool}))
					result := protocol.ToolResultPayload{ConversationID: "conv", ToolUseID: tool}
					if name == "tool-foreign-result" {
						result.ConversationID = "other"
					}
					if name != "tool-no-result" {
						frames = append(frames, frame(protocol.TypeToolResult, result))
					}
				}
				if name == "tool-unrecognized" {
					frames = append(frames, frame(protocol.TypeUnrecognizedMessage, protocol.UnrecognizedMessagePayload{}))
				}
				if name == "tool-rate-limit" {
					frames = append(frames, frame(protocol.TypeRateLimited, protocol.RateLimitedPayload{Status: "unknown"}))
				}
			}
			frames = append(frames, turn()...)
		default:
			t.Fatal("unexpected action")
		}
		return sends
	}, Next: func(deadline time.Time) protocol.Envelope {
		if len(frames) == 0 {
			time.Sleep(time.Millisecond)
			return protocol.Envelope{}
		}
		env := frames[0]
		frames = frames[1:]
		return env
	}}
	budget := 30 * time.Millisecond
	switch {
	case strings.HasPrefix(name, "announce-"):
		log := ""
		if name == "announce-log-leak" {
			log = "unexpected resolved value in debug record"
		}
		ModelAnnouncement(t, wire, "conv", "prompt", "haiku", "resolved", budget, func() string { return log })
	case strings.HasPrefix(name, "reject-"):
		ModelRejection(t, wire, "conv", "session", "prompt", "resolved", budget, budget)
	case strings.HasPrefix(name, "tool-"):
		ToolTurn(t, wire, "conv", "prompt", budget)
	}
	if len(frames) != 0 || sends != 1 && sends != 3 {
		t.Fatal("protocol milestones or actions were omitted")
	}
}
