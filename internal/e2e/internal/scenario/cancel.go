package scenario

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const CancelPermissionWitness = "pyrycode-cancel-denied-witness.txt"

// CancelPermissionPrompt keeps the original live trigger identical in capture.
// The denial instruction prevents a second tool request from parking the turn.
const CancelPermissionPrompt = "Use the Write tool to create a file named " + CancelPermissionWitness + " containing the single word hello. " +
	"If the tool call is denied, do not retry it, do not use any other tool, and reply with one short word."

// CancelPermission requires the original cancellation milestones: the exact
// remote dismissal within dismissBudget, then owned turn_end within turnBudget
// of that dismissal. A turn_end already observed while waiting for dismissal is
// retained because those broadcasts travel independently. Replay checks ordering
// and attribution; only a live run proves elapsed time and the file's absence.
func CancelPermission(t *testing.T, w Wire, convID string, shown protocol.ModalShownPayload, dismissBudget, turnBudget time.Duration) {
	t.Helper()
	if convID == "" || shown.Class != "permission" || shown.ModalID == "" || shown.ConversationID != convID {
		t.Fatal("cannot cancel an unowned permission")
	}
	w.Send(protocol.TypeModalCancel, protocol.ModalCancelPayload{ModalID: shown.ModalID})
	deadline := time.Now().Add(dismissBudget)
	dismissed, ended := false, false
	for time.Now().Before(deadline) {
		env := w.Next(deadline)
		if !time.Now().Before(deadline) {
			break
		}
		alarm(t, env)
		switch env.Type {
		case protocol.TypeModalShown, protocol.TypeQuestionShown:
			t.Fatal("cancelled permission unexpectedly raised another prompt")
		case protocol.TypeModalDismissed:
			var dis protocol.ModalDismissedPayload
			if json.Unmarshal(env.Payload, &dis) != nil || dis.ModalID != shown.ModalID || dis.Source != "remote" || dis.Outcome != "cancelled" {
				t.Fatal("cancel dismissal lost identity, outcome or remote attribution")
			}
			if dismissed {
				t.Fatal("cancelled permission was dismissed more than once")
			}
			dismissed = true
			deadline = time.Now().Add(turnBudget)
		case protocol.TypeTurnEnd:
			var end protocol.TurnEndPayload
			if json.Unmarshal(env.Payload, &end) != nil {
				t.Fatal("invalid cancellation turn_end")
			}
			if end.ConversationID == convID {
				ended = true
			}
		}
		if dismissed && ended {
			return
		}
	}
	if !dismissed {
		t.Fatal("cancelled permission did not receive its matching dismissal within the dismissal budget")
	}
	t.Fatal("cancelled permission did not reach owned turn_end within the warm completion budget")
}
