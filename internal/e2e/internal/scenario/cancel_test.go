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

func TestCancelPermissionControls(t *testing.T) {
	valid := map[string]bool{"dismiss-first": true, "turn-first": true, "separate-budgets": true}
	for _, name := range []string{"dismiss-first", "turn-first", "separate-budgets", "missing-dismissal", "missing-completion", "foreign-completion", "idle-without-turn-end", "wrong-modal", "wrong-source", "wrong-outcome", "new-permission", "new-question", "foreign-surface", "late-dismissal", "late-completion"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCancelPermissionHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_CANCEL_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || (err == nil) != valid[name] {
				t.Fatalf("cancel evidence acceptance err=%v output=%s", err, out)
			}
			if valid[name] && !strings.Contains(string(out), "all cancellation milestones verified") {
				t.Fatal("helper did not execute the driver")
			}
		})
	}
}

func TestCancelPermissionHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_CANCEL_CONTROL")
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
	shown := protocol.ModalShownPayload{ModalID: "modal", ConversationID: "conv", Class: "permission"}
	dis := protocol.ModalDismissedPayload{ModalID: "modal", Source: "remote", Outcome: "cancelled"}
	end := protocol.TurnEndPayload{ConversationID: "conv"}
	switch name {
	case "wrong-modal":
		dis.ModalID = "foreign"
	case "wrong-source":
		dis.Source = "timeout"
	case "wrong-outcome":
		dis.Outcome = "reject_once"
	case "foreign-completion":
		end.ConversationID = "foreign"
	case "foreign-surface":
		shown.ConversationID = "foreign"
	}
	d, e := frame(protocol.TypeModalDismissed, dis), frame(protocol.TypeTurnEnd, end)
	frames := []protocol.Envelope{d, e}
	switch name {
	case "turn-first":
		frames = []protocol.Envelope{e, d}
	case "missing-dismissal":
		frames = []protocol.Envelope{e}
	case "missing-completion":
		frames = []protocol.Envelope{d}
	case "idle-without-turn-end":
		frames = []protocol.Envelope{d, frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: "idle"})}
	case "new-permission":
		frames = []protocol.Envelope{frame(protocol.TypeModalShown, shown), d, e}
	case "new-question":
		frames = []protocol.Envelope{frame(protocol.TypeQuestionShown, protocol.QuestionShownPayload{ConversationID: "conv", QuestionBatchID: "batch"}), d, e}
	}
	sends, reads := 0, 0
	var initialDeadline time.Time
	wire := Wire{Send: func(kind string, payload any) uint64 {
		sends++
		p, ok := payload.(protocol.ModalCancelPayload)
		if kind != protocol.TypeModalCancel || !ok || p.ModalID != "modal" {
			t.Fatal("driver did not send the exact cancellation")
		}
		return uint64(sends)
	}, Next: func(deadline time.Time) protocol.Envelope {
		reads++
		if reads == 1 {
			initialDeadline = deadline
		}
		if name == "separate-budgets" && reads == 2 && deadline.Sub(initialDeadline) < 50*time.Millisecond {
			t.Fatal("warm completion budget was not started at dismissal")
		}
		if (name == "late-dismissal" && reads == 1) || (name == "late-completion" && reads == 2) {
			time.Sleep(time.Until(deadline) + 5*time.Millisecond)
		}
		if len(frames) == 0 {
			time.Sleep(time.Until(deadline))
			return protocol.Envelope{}
		}
		env := frames[0]
		frames = frames[1:]
		return env
	}}
	turnBudget := 40 * time.Millisecond
	if name == "separate-budgets" {
		turnBudget = 100 * time.Millisecond
	}
	CancelPermission(t, wire, "conv", shown, 40*time.Millisecond, turnBudget)
	if sends != 1 || len(frames) != 0 {
		t.Fatal("cancellation was not sent once or milestones were not consumed")
	}
	t.Log("all cancellation milestones verified")
}
