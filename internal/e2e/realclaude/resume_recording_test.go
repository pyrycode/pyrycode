//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func lifecycleRecordingBeforeIdle(t *testing.T, home string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	if c.Process.Scenario != "session-resume" || home != c.Process.Home {
		t.Fatal("idle checkpoint escaped original root")
	}
	prefix := filepath.Join(home, "recording-children", "bootstrap")
	job, err := readLifecycleJob(prefix + ".job")
	if err != nil || job.SessionID != evictResumeBootstrapUUID || job.ConversationID != evictResumeConvID || job.Config.OwnerPID <= 1 || syscall.Kill(job.Config.OwnerPID, 0) != nil {
		t.Fatal("idle checkpoint lacks its live original provider")
	}
	action := claudetape.LifecycleKill{PID: job.Config.OwnerPID, Signal: "idle-evict"}
	if claudetape.PublishLifecycleControl(prefix+".action", action) != nil {
		t.Fatal("idle checkpoint repeated")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var ack claudetape.LifecycleKill
		if claudetape.ReadLifecycleControl(prefix+".action.ready", 16384, &ack) == nil {
			if ack != action {
				t.Fatal("foreign idle checkpoint acknowledgement")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("idle checkpoint was not acknowledged")
}

func lifecycleRecordingResumeRole(c lifecycleRecordingConfig, session string, args []string) (claudetape.LifecycleRoleBinding, error) {
	fail := os.ErrInvalid
	binding := claudetape.LifecycleRoleBinding{Role: "bootstrap", SessionID: session, ConversationID: evictResumeConvID, WorkDir: c.Process.WorkDir}
	if session != evictResumeBootstrapUUID {
		return binding, fail
	}
	fresh, resume := 0, 0
	for _, arg := range args {
		if arg == "--session-id" {
			fresh++
		}
		if arg == "--resume" {
			resume++
		}
	}
	if fresh+resume != 1 {
		return binding, fail
	}
	var registry struct {
		Conversations []struct {
			ID, Cwd string
			Session string `json:"current_session_id"`
		} `json:"conversations"`
	}
	raw, err := os.ReadFile(filepath.Join(c.Process.Home, ".pyry", "test", "conversations.json"))
	if err != nil || json.Unmarshal(raw, &registry) != nil {
		return binding, fail
	}
	matches := 0
	for _, row := range registry.Conversations {
		if row.ID == evictResumeConvID {
			matches++
			if row.Session != session || row.Cwd != c.Process.WorkDir {
				return binding, fail
			}
		}
	}
	if matches != 1 {
		return binding, fail
	}
	if fresh == 1 {
		return binding, nil
	}
	binding.Role = "resumed"
	old, err := readLifecycleJob(filepath.Join(c.Process.Home, "recording-children", "bootstrap.job"))
	if err != nil || old.SessionID != session || old.ConversationID != binding.ConversationID || old.Config.OwnerPID <= 1 || syscall.Kill(old.Config.OwnerPID, 0) != syscall.ESRCH {
		return binding, fail
	}
	var receipt claudetape.LifecycleReceipt
	if claudetape.ReadLifecycleControl(filepath.Join(c.Process.Home, "recording-children", "bootstrap.receipt"), 16384, &receipt) != nil || !receipt.Success || receipt.OwnerPID != old.Config.OwnerPID {
		return binding, fail
	}
	if claudetape.PublishLifecycleControl(filepath.Join(c.Process.Home, "recording-roles", "resumed.json"), binding) != nil {
		return binding, fail
	}
	return binding, nil
}

// The original WARN gate already passed. Join the actual owned predecessor EOF
// before snapshotting its complete native output. Replay restores it before the
// unchanged resume send, so production's real existence probe must select resume.
func lifecycleRecordingResumeSnapshot(t *testing.T, home string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	if c.Process.Scenario != "session-resume" || c.Process.Home != home {
		t.Fatal("resume snapshot escaped original root")
	}
	prefix := filepath.Join(home, "recording-children", "bootstrap")
	job, err := readLifecycleJob(prefix + ".job")
	if err != nil || job.Config.OwnerPID <= 1 {
		t.Fatal("missing predecessor snapshot producer")
	}
	terminal := waitLifecycleReceipt(t, prefix+".receipt")
	if terminal.OwnerPID != job.Config.OwnerPID || syscall.Kill(job.Config.OwnerPID, 0) != syscall.ESRCH {
		t.Fatal("original eviction did not stop its owned predecessor")
	}
	var delivery claudetape.LifecycleDelivery
	if claudetape.ReadLifecycleControl(prefix+".user-000001", 16384, &delivery) != nil || delivery.Version != 1 || delivery.Kind != "user-input-forwarded" || delivery.OwnerPID != job.Config.OwnerPID || delivery.BrokerPID != terminal.BrokerPID || delivery.Sequence != 1 {
		t.Fatal("predecessor transcript lacks its original strict user request")
	}
	path := claudetape.LifecycleNativePath(home, evictResumeBootstrapUUID)
	var snapshot claudetape.SessionSnapshot
	if c.Process.Mode == "capture" {
		snapshot, err = claudetape.ReadSessionSnapshot(path, evictResumeBootstrapUUID, home)
		if err != nil {
			t.Fatal("evicted native provider left no complete private transcript")
		}
	} else {
		raw, e := os.ReadFile(filepath.Join(home, "recording-snapshot-inputs", "bootstrap.json"))
		if e != nil {
			t.Fatal("missing reviewed predecessor transcript")
		}
		snapshot, e = claudetape.DecodeSessionSnapshot(bytes.NewReader(raw))
		if e != nil {
			t.Fatal("invalid reviewed predecessor transcript")
		}
		if claudetape.PrepareSessionSnapshotDirectory(home, evictResumeBootstrapUUID) != nil || claudetape.InstallSessionSnapshot(snapshot, path, evictResumeBootstrapUUID, home) != nil {
			t.Fatal("cannot exclusively restore predecessor transcript after actual eviction")
		}
	}
	output := filepath.Join(home, "recording-snapshots", "bootstrap.json")
	if snapshot.Save(output, home) != nil {
		t.Fatal("predecessor transcript publication failed")
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal("missing published predecessor transcript")
	}
	sum := sha256.Sum256(raw)
	stage := claudetape.LifecycleSnapshotStage{Version: 1, OwnerPID: job.Config.OwnerPID, BrokerPID: terminal.BrokerPID, UserSequence: 1, Role: "bootstrap", Kind: "native-jsonl-after-owned-eviction", Revision: poolRecordedRevision, SourceBinarySHA256: c.SourceBinarySHA256, SnapshotSHA256: hex.EncodeToString(sum[:])}
	if claudetape.PublishLifecycleControl(filepath.Join(home, "recording-snapshots", "bootstrap.stage"), stage) != nil {
		t.Fatal("predecessor transcript causal stage repeated")
	}
}
