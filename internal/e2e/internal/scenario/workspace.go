package scenario

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

type WorkspaceConfig struct {
	Home, ConversationID, Requested, Resolved, FirstPrompt, SecondPrompt string
	TurnBudget, VerbBudget                                               time.Duration
}

func WorkspaceChange(t *testing.T, w Wire, c WorkspaceConfig) {
	t.Helper()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "workspace-before", Text: c.FirstPrompt})
	CompletedTurn(t, w.Next, c.ConversationID, c.TurnBudget, false, w.Source)
	id := w.Send(protocol.TypeChangeWorkspace, protocol.ChangeWorkspacePayload{ConversationID: c.ConversationID, Cwd: c.Requested})
	env := Correlated(t, w.Next, id, c.VerbBudget)
	var changed protocol.ConversationUpdatedPayload
	if env.Type != protocol.TypeConversationUpdated || json.Unmarshal(env.Payload, &changed) != nil || changed.ID != c.ConversationID {
		t.Fatal("workspace update lost type, request or conversation correlation")
	}
	if changed.Cwd == c.Requested || changed.Cwd != c.Resolved {
		t.Fatal("workspace update did not return the confined realpath")
	}
	if ConversationCwd(t, c.Home, c.ConversationID) != c.Resolved {
		t.Fatal("workspace update was not persisted")
	}
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "workspace-after", Text: c.SecondPrompt})
	CompletedTurn(t, w.Next, c.ConversationID, c.TurnBudget, false, w.Source)
}
func ConversationCwd(t *testing.T, home, convID string) string {
	t.Helper()
	path := filepath.Join(home, ".pyry", "test", "conversations.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read conversations.json: %v", err)
	}
	var onDisk struct {
		Conversations []struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("decode on-disk registry: %v", err)
	}
	for _, c := range onDisk.Conversations {
		if c.ID == convID {
			return c.Cwd
		}
	}
	t.Fatalf("on-disk registry holds no row for %q (%d rows)", convID, len(onDisk.Conversations))
	return ""
}
