//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	"os"
	"path/filepath"
	"testing"
)

func lifecycleRecordingPromptSeeds(t *testing.T, home string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	if c.Process.Scenario != "conversation-system-prompt" || home != c.Process.Home {
		t.Fatal("prompt seed escaped original root")
	}
	var registry struct {
		Conversations []struct {
			ID, Cwd string
			Session string `json:"current_session_id"`
		} `json:"conversations"`
	}
	raw, err := os.ReadFile(filepath.Join(home, ".pyry", "test", "conversations.json"))
	if err != nil || json.Unmarshal(raw, &registry) != nil || len(registry.Conversations) != 2 {
		t.Fatal("invalid original prompt registry")
	}
	for _, row := range registry.Conversations {
		role := ""
		if row.ID == livePromptedConvID {
			role = "prompted"
		}
		if row.ID == liveBareConvID {
			role = "bare"
		}
		b := claudetape.LifecycleRoleBinding{Role: role, ConversationID: row.ID, SessionID: row.Session, WorkDir: row.Cwd}
		if role == "" || claudetape.PublishLifecycleControl(filepath.Join(home, "recording-roles", role+".json"), b) != nil {
			t.Fatal("prompt role repeated or foreign")
		}
		if _, err := claudetape.ReadLifecycleRole(home, c.Process.Scenario, role); err != nil {
			t.Fatal("prompt role differs from original persisted seed")
		}
	}
}
func lifecycleRecordingPromptRole(c lifecycleRecordingConfig, session string) (claudetape.LifecycleRoleBinding, error) {
	for _, role := range claudetape.LifecycleProfiles()[c.Process.Scenario].Roles {
		b, err := claudetape.ReadLifecycleRole(c.Process.Home, c.Process.Scenario, role)
		if err != nil {
			return b, err
		}
		if b.SessionID == session {
			return b, nil
		}
	}
	return claudetape.LifecycleRoleBinding{}, os.ErrInvalid
}
