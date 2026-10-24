//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestVerifier3740ResetActiveOwnership(t *testing.T) {
	if mode := os.Getenv("PYRY_VERIFIER3740_RESET_PROBE"); mode != "" {
		now := time.Now().UTC()
		w := newResetWindow()
		w.sourceConv = "conv"
		w.transitions = []windowTransition{{payload: protocol.SessionTransitionPayload{ConversationID: "conv", PreviousSessionID: "old", NewSessionID: "new", OccurredAt: now}}}
		raw, err := json.Marshal(protocol.ResettingPayload{ConversationID: "conv", Active: true, Phase: protocol.ResetPhaseWrappingUp, Handoff: protocol.ResetHandoffPending})
		if err != nil {
			t.Fatal(err)
		}
		active := protocol.Envelope{Type: protocol.TypeResetting, TS: now.Add(-time.Second), Payload: raw, ConversationID: json.RawMessage(`"conv"`), SessionID: json.RawMessage(`"old"`)}
		switch mode {
		case "missing-session":
			active.SessionID = nil
		case "foreign-session":
			active.SessionID = json.RawMessage(`"foreign"`)
		case "missing-conversation":
			active.ConversationID = nil
		case "foreign-conversation":
			active.ConversationID = json.RawMessage(`"foreign"`)
		case "null-session":
			active.SessionID = json.RawMessage(`null`)
		case "invalid-session":
			active.SessionID = json.RawMessage(`{}`)
		case "null-conversation":
			active.ConversationID = json.RawMessage(`null`)
		case "invalid-conversation":
			active.ConversationID = json.RawMessage(`{}`)
		}
		w.receive = func(time.Time) protocol.Envelope { return active }
		if _, ok := w.next(t, nil, nil, now.Add(time.Second)); !ok {
			t.Fatal("no active observation")
		}
		w.clears = []protocol.Envelope{{Type: protocol.TypeResetting, SessionStateCleared: true, TS: now, ConversationID: json.RawMessage(`"conv"`), SessionID: json.RawMessage(`"new"`)}}
		w.assertProgress(t, "conv", protocol.ResetHandoffWritten)
		w.assertClear(t, "conv")
		t.Log("probe accepted active resetting frame and certified completed reset")
		return
	}
	for _, mode := range []string{"owned", "missing-session", "foreign-session", "missing-conversation", "foreign-conversation", "null-session", "invalid-session", "null-conversation", "invalid-conversation"} {
		t.Run(mode, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "-test.run=^TestVerifier3740ResetActiveOwnership$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_VERIFIER3740_RESET_PROBE="+mode)
			out, err := cmd.CombinedOutput()
			if mode == "owned" {
				if err != nil {
					t.Fatalf("owned progress rejected: %s", out)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s active metadata accepted: %s", mode, out)
			}
			if !strings.Contains(string(out), "FAIL") {
				t.Fatalf("probe failed outside assertion: %v %s", err, out)
			}
		})
	}
}
