//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func lifecycleRecordingBeforeReset(t *testing.T, home string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	_, initial, conversation, reset := claudetape.LifecycleResetSeeds(c.Process.Scenario)
	if !reset || home != c.Process.Home {
		t.Fatal("bounded reset checkpoint escaped original root")
	}
	prefix := filepath.Join(home, "recording-children", "bound")
	job, err := readLifecycleJob(prefix + ".job")
	if err != nil || job.SessionID != initial || job.ConversationID != conversation || job.Config.OwnerPID <= 1 || syscall.Kill(job.Config.OwnerPID, 0) != nil {
		t.Fatal("bounded reset lacks its live original bound provider")
	}
	action := claudetape.LifecycleKill{PID: job.Config.OwnerPID, Signal: "rotate-session"}
	if claudetape.PublishLifecycleControl(prefix+".action", action) != nil {
		t.Fatal("bounded reset checkpoint repeated")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var got claudetape.LifecycleKill
		if claudetape.ReadLifecycleControl(prefix+".action.ready", 16384, &got) == nil {
			if got != action {
				t.Fatal("foreign bounded reset checkpoint acknowledgement")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("bounded reset checkpoint was not acknowledged")
}

// Role membership is taken from the current persisted conversation, never from
// launch order. The fresh successor cannot claim the predecessor's native ID.
func lifecycleRecordingResetRole(c lifecycleRecordingConfig, session string) (claudetape.LifecycleRoleBinding, error) {
	fail := os.ErrInvalid
	bootstrapID, initial, conversation, ok := claudetape.LifecycleResetSeeds(c.Process.Scenario)
	if !ok {
		return claudetape.LifecycleRoleBinding{}, fail
	}
	bootstrap, err := claudetape.ReadLifecycleRole(c.Process.Home, c.Process.Scenario, "bootstrap")
	if err != nil {
		return bootstrap, fail
	}
	if session == bootstrap.SessionID {
		return bootstrap, nil
	}
	var registry struct {
		Conversations []struct {
			ID, Cwd string
			Session string `json:"current_session_id"`
		} `json:"conversations"`
	}
	raw, err := os.ReadFile(filepath.Join(c.Process.Home, ".pyry", "test", "conversations.json"))
	if err != nil || json.Unmarshal(raw, &registry) != nil {
		return bootstrap, fail
	}
	binding := claudetape.LifecycleRoleBinding{Role: "bound"}
	if session != initial {
		binding.Role = "rotated"
	}
	matches := 0
	for _, row := range registry.Conversations {
		if row.ID == conversation {
			matches++
			binding.ConversationID, binding.SessionID, binding.WorkDir = row.ID, row.Session, row.Cwd
		}
	}
	if matches != 1 || binding.SessionID != session || binding.SessionID == bootstrapID || binding.WorkDir != c.Process.WorkDir {
		return binding, fail
	}
	if binding.Role == "rotated" {
		old, err := readLifecycleJob(filepath.Join(c.Process.Home, "recording-children", "bound.job"))
		if err != nil || old.SessionID != initial || old.ConversationID != conversation || old.Config.OwnerPID <= 1 || syscall.Kill(old.Config.OwnerPID, 0) != syscall.ESRCH {
			return binding, fail
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			var terminal claudetape.LifecycleReceipt
			if claudetape.ReadLifecycleControl(filepath.Join(c.Process.Home, "recording-children", "bound.receipt"), 16384, &terminal) == nil {
				if !terminal.Success || terminal.OwnerPID != old.Config.OwnerPID {
					return binding, fail
				}
				break
			}
			if !time.Now().Before(deadline) {
				return binding, fail
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if claudetape.PublishLifecycleControl(filepath.Join(c.Process.Home, "recording-roles", binding.Role+".json"), binding) != nil {
		return binding, fail
	}
	return binding, nil
}
