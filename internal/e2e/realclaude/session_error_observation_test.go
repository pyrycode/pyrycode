//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestSessionErrorObservationControls(t *testing.T) {
	for _, name := range []string{"clear", "foreign-clear", "nonempty-clear", "delivery-once", "duplicate-delivery", "actual-error"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionErrorObservationHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_RECOVERY_OBSERVATION_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			valid := name == "clear" || name == "delivery-once" || name == "actual-error"
			if ctx.Err() != nil || (err == nil) != valid {
				t.Fatalf("recovery observation acceptance err=%v output=%s", err, out)
			}
		})
	}
}

func TestSessionErrorObservationHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_RECOVERY_OBSERVATION_CONTROL")
	if name == "" {
		return
	}
	state := &sessionErrorObservation{}
	conv, _ := json.Marshal(liveConvID)
	clear := protocol.Envelope{Type: protocol.TypeSessionError, Payload: json.RawMessage(`{}`), ConversationID: conv, SessionStateCleared: true}
	switch name {
	case "clear", "foreign-clear", "nonempty-clear":
		if name == "foreign-clear" {
			clear.ConversationID = json.RawMessage(`"other"`)
		}
		if name == "nonempty-clear" {
			clear.Payload, _ = json.Marshal(protocol.SessionErrorPayload{ConversationID: liveConvID, Code: protocol.CodeSessionChildCrashing})
		}
		state.observe(t, clear)
		if state.crashing || state.blocked || state.delivered != "" {
			t.Fatal("cleared reading became recovery evidence")
		}
	case "delivery-once", "duplicate-delivery":
		raw, _ := json.Marshal(protocol.MessagePayload{ConversationID: liveConvID, Role: "user", MessageID: "held-2859"})
		state.observe(t, protocol.Envelope{Type: protocol.TypeMessage, Payload: raw})
		f := scenario.QueuePlacementFrame{Kind: "user_message", MessageID: "held-2859", Status: "delivered"}
		state.observePlacement(t, f)
		if name == "duplicate-delivery" {
			state.observePlacement(t, f)
		}
		if state.delivered != "held-2859" || !state.empty {
			t.Fatal("canonical delivery was not recorded")
		}
	case "actual-error":
		raw, _ := json.Marshal(protocol.SessionErrorPayload{ConversationID: liveConvID, Code: protocol.CodeSessionChildCrashing})
		state.observe(t, protocol.Envelope{Type: protocol.TypeSessionError, Payload: raw})
		if !state.crashing || state.blocked {
			t.Fatal("actual child-crashing reading was lost")
		}
	}
}
