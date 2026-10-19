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

func TestModalControls(t *testing.T) {
	valid := map[string]bool{"allow": true, "allow-idle-first": true, "deny-without-text": true, "deny-retry": true, "refuse": true, "refuse-idle-first": true, "refuse-reask": true, "refuse-allow": true}
	cases := []string{"allow", "allow-idle-first", "deny-without-text", "deny-retry", "permission-no-dismissal", "permission-no-text", "permission-no-idle", "permission-foreign-idle", "permission-wrong-source", "permission-wrong-outcome", "permission-wrong-id", "permission-retry-unresolved", "permission-retry-cap", "refuse", "refuse-idle-first", "refuse-reask", "refuse-allow", "refuse-no-dismissal", "refuse-no-idle", "refuse-foreign-idle", "refuse-wrong-source", "refuse-wrong-outcome", "refuse-wrong-id", "refuse-reask-unresolved", "refuse-reask-cap", "refuse-allow-unresolved", "refuse-allow-wrong-outcome", "refuse-foreign-permission"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestModalHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_MODAL_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || (err == nil) != valid[name] {
				t.Fatalf("modal acceptance err=%v output=%s", err, out)
			}
			if valid[name] && !strings.Contains(string(out), "all modal milestones verified") {
				t.Fatal("helper did not execute the driver")
			}
		})
	}
}

func TestModalHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_MODAL_CONTROL")
	if name == "" {
		return
	}
	frame := func(kind string, payload any) protocol.Envelope {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw}
	}
	idle := frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: "idle"})
	foreignIdle := frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "foreign", State: "idle"})
	text := frame(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: "conv", Text: "done"})
	shown := protocol.ModalShownPayload{ModalID: "modal", ConversationID: "conv", Class: "permission"}
	option := "allow_once"
	if strings.HasPrefix(name, "deny") || strings.Contains(name, "retry") {
		option = "reject_once"
	}
	dis := protocol.ModalDismissedPayload{ModalID: "modal", Source: "remote", Outcome: option}
	if name == "permission-wrong-source" {
		dis.Source = "timeout"
	}
	if name == "permission-wrong-outcome" {
		dis.Outcome = "cancelled"
	}
	if name == "permission-wrong-id" {
		dis.ModalID = "foreign"
	}
	frames := []protocol.Envelope{frame(protocol.TypeModalDismissed, dis), text, idle}
	switch name {
	case "allow-idle-first":
		frames = []protocol.Envelope{text, idle, frame(protocol.TypeModalDismissed, dis)}
	case "deny-without-text":
		frames = []protocol.Envelope{frame(protocol.TypeModalDismissed, dis), idle}
	case "permission-no-dismissal":
		frames = []protocol.Envelope{text, idle}
	case "permission-no-text":
		frames = []protocol.Envelope{frame(protocol.TypeModalDismissed, dis), idle}
	case "permission-no-idle":
		frames = frames[:2]
	case "permission-foreign-idle":
		frames[2] = foreignIdle
	}
	if name == "deny-retry" || strings.HasPrefix(name, "permission-retry") {
		frames = frames[:1]
		count := 1
		if name == "permission-retry-cap" {
			count = 5
		}
		for i := 0; i < count; i++ {
			retry := shown
			retry.ModalID = string(rune('a' + i))
			frames = append(frames, frame(protocol.TypeModalShown, retry))
			if name != "permission-retry-unresolved" {
				frames = append(frames, frame(protocol.TypeModalDismissed, protocol.ModalDismissedPayload{ModalID: retry.ModalID, Source: "remote", Outcome: option}))
			}
		}
		frames = append(frames, idle)
	}
	refused := strings.HasPrefix(name, "refuse")
	if refused {
		dismissal := protocol.QuestionDismissedPayload{QuestionBatchID: "batch", Source: "remote", Outcome: "refused"}
		if name == "refuse-wrong-source" {
			dismissal.Source = "unanswered"
		}
		if name == "refuse-wrong-outcome" {
			dismissal.Outcome = "answered"
		}
		if name == "refuse-wrong-id" {
			dismissal.QuestionBatchID = "foreign"
		}
		d := frame(protocol.TypeQuestionDismissed, dismissal)
		frames = []protocol.Envelope{d, idle}
		switch name {
		case "refuse-idle-first":
			frames = []protocol.Envelope{idle, d}
		case "refuse-no-dismissal":
			frames = []protocol.Envelope{idle}
		case "refuse-no-idle":
			frames = []protocol.Envelope{d}
		case "refuse-foreign-idle":
			frames[1] = foreignIdle
		}
		if strings.HasPrefix(name, "refuse-reask") {
			frames = frames[:1]
			count := 1
			if name == "refuse-reask-cap" {
				count = 3
			}
			for i := 0; i < count; i++ {
				id := string(rune('a' + i))
				frames = append(frames, frame(protocol.TypeQuestionShown, protocol.QuestionShownPayload{QuestionBatchID: id, ConversationID: "conv"}))
				if name != "refuse-reask-unresolved" {
					frames = append(frames, frame(protocol.TypeQuestionDismissed, protocol.QuestionDismissedPayload{QuestionBatchID: id, Source: "remote", Outcome: "refused"}))
				}
			}
			frames = append(frames, idle)
		}
		if strings.HasPrefix(name, "refuse-allow") || name == "refuse-foreign-permission" {
			frames = frames[:1]
			if name == "refuse-foreign-permission" {
				shown.ConversationID = "foreign"
			}
			frames = append(frames, frame(protocol.TypeModalShown, shown))
			outcome := "allow_once"
			if name == "refuse-allow-wrong-outcome" {
				outcome = "reject_once"
			}
			if name != "refuse-allow-unresolved" {
				frames = append(frames, frame(protocol.TypeModalDismissed, protocol.ModalDismissedPayload{ModalID: "modal", Source: "remote", Outcome: outcome}))
			}
			frames = append(frames, idle)
		}
	}
	sends := 0
	wire := Wire{Send: func(kind string, payload any) uint64 {
		sends++
		if kind == protocol.TypeModalAnswer {
			p, ok := payload.(protocol.ModalAnswerPayload)
			if !ok || p.ModalID == "" || p.AnswerToken == "" || p.OptionID != option {
				t.Fatal("incorrect remote permission answer")
			}
		} else if kind == protocol.TypeQuestionRefused {
			p, ok := payload.(protocol.QuestionRefusedPayload)
			if !ok || p.QuestionBatchID == "" || p.AnswerToken == "" {
				t.Fatal("incorrect re-ask refusal")
			}
		} else {
			t.Fatal("unexpected action")
		}
		return uint64(sends)
	}, Next: func(deadline time.Time) protocol.Envelope {
		if len(frames) == 0 {
			time.Sleep(time.Until(deadline))
			return protocol.Envelope{}
		}
		env := frames[0]
		frames = frames[1:]
		return env
	}}
	if refused {
		obs := RefusedTurn(t, wire, "conv", "batch", 40*time.Millisecond)
		if name == "refuse-reask" && (obs.ExtraRefusals != 1 || sends != 1) {
			t.Fatal("re-ask was not refused")
		}
		if name == "refuse-allow" && (obs.ModalsAllowed != 1 || sends != 1) {
			t.Fatal("post-refusal permission was not allowed")
		}
	} else {
		got := ResolvePermission(t, wire, "conv", shown, option, "first", 40*time.Millisecond)
		if option == "allow_once" && got != "done" {
			t.Fatal("continuation was lost")
		}
		if name == "deny-retry" && sends != 2 {
			t.Fatal("retry was not denied")
		}
	}
	t.Log("all modal milestones verified")
}
