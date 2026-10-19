package scenario

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// WaitPermission proves the tool parked before any client decision is sent.
func WaitPermission(t *testing.T, next Next, convID string, budget time.Duration) protocol.ModalShownPayload {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		env := next(deadline)
		alarm(t, env)
		if env.Type != protocol.TypeModalShown {
			continue
		}
		var shown protocol.ModalShownPayload
		if json.Unmarshal(env.Payload, &shown) != nil || shown.Class != "permission" || shown.ModalID == "" || shown.ConversationID != convID {
			t.Fatal("permission surface has wrong class, identity or conversation")
		}
		return shown
	}
	t.Fatal("no permission surfaced")
	return protocol.ModalShownPayload{}
}

// ResolvePermission requires every sent decision to be remotely attributed,
// completion of the owned turn, and a nonempty continuation for allow. Denial
// may complete without text and rejects up to four retry modals, as the live gate
// has always done. Dismissal and idle are independent broadcasts.
func ResolvePermission(t *testing.T, w Wire, convID string, shown protocol.ModalShownPayload, option, token string, budget time.Duration) string {
	t.Helper()
	if shown.Class != "permission" || shown.ModalID == "" || shown.ConversationID != convID {
		t.Fatal("cannot resolve an unowned permission")
	}
	if option != "allow_once" && option != "reject_once" {
		t.Fatal("unsupported permission decision")
	}
	w.Send(protocol.TypeModalAnswer, protocol.ModalAnswerPayload{ModalID: shown.ModalID, OptionID: option, AnswerToken: token})
	return permissionTurn(t, w, convID, map[string]bool{shown.ModalID: true}, option, 4, budget)
}

// DenyRetries retains callers whose initial dismissal was already checked.
func DenyRetries(t *testing.T, w Wire, convID string, maxRetries int, budget time.Duration) {
	t.Helper()
	permissionTurn(t, w, convID, map[string]bool{}, "reject_once", maxRetries, budget)
}

func permissionTurn(t *testing.T, w Wire, convID string, pending map[string]bool, option string, maxRetries int, budget time.Duration) string {
	t.Helper()
	seen := map[string]bool{}
	for id := range pending {
		seen[id] = true
	}
	retries := 0
	idle := false
	var text strings.Builder
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		env := w.Next(deadline)
		alarm(t, env)
		switch env.Type {
		case protocol.TypeQuestionShown:
			t.Fatal("permission turn unexpectedly raised a question")
		case protocol.TypeModalShown:
			var shown protocol.ModalShownPayload
			if json.Unmarshal(env.Payload, &shown) != nil || shown.Class != "permission" || shown.ModalID == "" || shown.ConversationID != convID {
				t.Fatal("invalid retry permission ownership")
			}
			if seen[shown.ModalID] {
				t.Fatal("permission was surfaced more than once")
			}
			if option != "reject_once" || retries >= maxRetries {
				t.Fatal("permission retries exceeded the bounded denial contract")
			}
			retries++
			seen[shown.ModalID], pending[shown.ModalID] = true, true
			idle = false
			w.Send(protocol.TypeModalAnswer, protocol.ModalAnswerPayload{ModalID: shown.ModalID, OptionID: option, AnswerToken: fmt.Sprintf("e2e-deny-retry-%d", retries)})
		case protocol.TypeModalDismissed:
			var dis protocol.ModalDismissedPayload
			if json.Unmarshal(env.Payload, &dis) != nil || !pending[dis.ModalID] || dis.Source != "remote" || dis.Outcome != option {
				t.Fatal("permission dismissal lost decision, correlation or remote attribution")
			}
			delete(pending, dis.ModalID)
		case protocol.TypeAssistantDelta:
			var delta protocol.AssistantDeltaPayload
			if json.Unmarshal(env.Payload, &delta) != nil {
				t.Fatal("invalid permission continuation")
			}
			if delta.ConversationID == convID && strings.TrimSpace(delta.Text) != "" {
				text.WriteString(delta.Text)
			}
		case protocol.TypeTurnState:
			var state protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &state) != nil {
				t.Fatal("invalid permission turn state")
			}
			if state.ConversationID == convID && state.State == "idle" && (option == "reject_once" || text.Len() > 0) {
				idle = true
			}
		}
		if idle && len(pending) == 0 {
			return text.String()
		}
	}
	t.Fatalf("permission turn did not complete: pending decisions=%d retries=%d text bytes=%d idle=%t", len(pending), retries, text.Len(), idle)
	return ""
}
