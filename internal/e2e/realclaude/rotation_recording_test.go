//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func lifecycleRecordingBeforeRotation(t *testing.T, home string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	if c.Process.Scenario != "session-rotation" || home != c.Process.Home {
		t.Fatal("rotation checkpoint escaped original root")
	}
	prefix := filepath.Join(home, "recording-children", "bootstrap")
	job, err := readLifecycleJob(prefix + ".job")
	if err != nil || job.SessionID != streamNewSessionBootstrapUUID || job.ConversationID != streamNewSessionConvID || job.Config.OwnerPID <= 1 || syscall.Kill(job.Config.OwnerPID, 0) != nil {
		t.Fatal("rotation lacks its live original provider")
	}
	action := claudetape.LifecycleKill{PID: job.Config.OwnerPID, Signal: "rotate-session"}
	if claudetape.PublishLifecycleControl(prefix+".action", action) != nil {
		t.Fatal("rotation checkpoint repeated")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var got claudetape.LifecycleKill
		if claudetape.ReadLifecycleControl(prefix+".action.ready", 16384, &got) == nil {
			if got != action {
				t.Fatal("foreign rotation checkpoint acknowledgement")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("rotation checkpoint not acknowledged")
}

func lifecycleRecordingRotationRole(c lifecycleRecordingConfig, session string) (claudetape.LifecycleRoleBinding, error) {
	fail := os.ErrInvalid
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
	role := claudetape.LifecycleRoleBinding{Role: "rotated"}
	matches := 0
	for _, row := range registry.Conversations {
		if row.ID == streamNewSessionConvID {
			matches++
			role.ConversationID, role.SessionID, role.WorkDir = row.ID, row.Session, row.Cwd
		}
	}
	if matches != 1 || role.SessionID != session || session == streamNewSessionBootstrapUUID || role.WorkDir != c.Process.WorkDir {
		return role, fail
	}
	old, err := readLifecycleJob(filepath.Join(c.Process.Home, "recording-children", "bootstrap.job"))
	if err != nil || old.Config.OwnerPID <= 1 || syscall.Kill(old.Config.OwnerPID, 0) != syscall.ESRCH {
		return role, fail
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var terminal claudetape.LifecycleReceipt
		if claudetape.ReadLifecycleControl(filepath.Join(c.Process.Home, "recording-children", "bootstrap.receipt"), 16384, &terminal) == nil {
			if !terminal.Success || terminal.OwnerPID != old.Config.OwnerPID {
				return role, fail
			}
			break
		}
		if !time.Now().Before(deadline) {
			return role, fail
		}
		time.Sleep(5 * time.Millisecond)
	}
	if claudetape.PublishLifecycleControl(filepath.Join(c.Process.Home, "recording-roles", "rotated.json"), role) != nil {
		return role, fail
	}
	return role, nil
}

// The unchanged original file-presence assertions run after this causal hook.
// Replay may materialize native files only after the new provider boundary has
// accepted the original second user request, never merely after a phone ACK.
func lifecycleRecordingRotationSnapshots(t *testing.T, home, before, after string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	if c.Process.Scenario != "session-rotation" || home != c.Process.Home || before != streamNewSessionBootstrapUUID || after == before {
		t.Fatal("rotation snapshot escaped original identities")
	}
	for _, role := range claudetape.LifecycleProfiles()[c.Process.Scenario].Roles {
		binding, err := claudetape.ReadLifecycleRole(home, c.Process.Scenario, role)
		if err != nil || role == "rotated" && binding.SessionID != after {
			t.Fatal("rotation snapshot lost current role")
		}
		prefix := filepath.Join(home, "recording-children", role)
		job, err := readLifecycleJob(prefix + ".job")
		if err != nil {
			t.Fatal("missing snapshot producer")
		}
		deadline := time.Now().Add(rotateBudget)
		var delivery claudetape.LifecycleDelivery
		for {
			if claudetape.ReadLifecycleControl(prefix+".user-000001", 16384, &delivery) == nil {
				if delivery.Version != 1 || delivery.Kind != "user-input-forwarded" || delivery.OwnerPID != job.Config.OwnerPID || delivery.Sequence != 1 {
					t.Fatal("snapshot lacks its strict user-input witness")
				}
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatal("original user request never reached snapshot producer")
			}
			time.Sleep(5 * time.Millisecond)
		}
		path := claudetape.LifecycleNativePath(home, binding.SessionID)
		var snapshot claudetape.SessionSnapshot
		if c.Process.Mode == "capture" {
			for {
				snapshot, err = claudetape.ReadSessionSnapshot(path, binding.SessionID, home)
				if err == nil {
					break
				}
				if !time.Now().Before(deadline) {
					t.Fatal("native transcript did not reach a complete private JSONL snapshot")
				}
				time.Sleep(10 * time.Millisecond)
			}
		} else {
			raw, e := os.ReadFile(filepath.Join(home, "recording-snapshot-inputs", role+".json"))
			if e != nil {
				t.Fatal("missing reviewed causal native transcript")
			}
			snapshot, e = claudetape.DecodeSessionSnapshot(bytes.NewReader(raw))
			if e != nil {
				t.Fatal("invalid reviewed native transcript")
			}
			if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.Chmod(filepath.Dir(path), 0700) != nil {
				t.Fatal("cannot prepare native transcript directory")
			}
			if claudetape.InstallSessionSnapshot(snapshot, path, binding.SessionID, home) != nil {
				t.Fatal("cannot exclusively install native transcript after matching original input")
			}
		}
		output := filepath.Join(home, "recording-snapshots", role+".json")
		if snapshot.Save(output, home) != nil {
			t.Fatal("native transcript snapshot publication failed")
		}
		raw, err := os.ReadFile(output)
		if err != nil {
			t.Fatal("missing published native transcript")
		}
		sum := sha256.Sum256(raw)
		stage := claudetape.LifecycleSnapshotStage{Version: 1, OwnerPID: job.Config.OwnerPID, BrokerPID: delivery.BrokerPID, UserSequence: 1, Role: role, Kind: "native-jsonl-after-user", Revision: poolRecordedRevision, SourceBinarySHA256: c.SourceBinarySHA256, SnapshotSHA256: hex.EncodeToString(sum[:])}
		if claudetape.PublishLifecycleControl(filepath.Join(home, "recording-snapshots", role+".stage"), stage) != nil {
			t.Fatal("native transcript causal stage repeated")
		}
	}
}

func lifecycleRecordingInputWitness(job claudetape.LifecycleJob, home, scenario string) func([]byte) error {
	if len(claudetape.LifecycleSnapshotRoles(scenario)) == 0 {
		return nil
	}
	count := 0
	return func(raw []byte) error {
		var env map[string]json.RawMessage
		if json.Unmarshal(raw, &env) != nil {
			return os.ErrInvalid
		}
		var kind string
		if json.Unmarshal(env["type"], &kind) != nil || kind != "user" {
			return nil
		}
		count++
		delivery := claudetape.LifecycleDelivery{Version: 1, OwnerPID: job.Config.OwnerPID, BrokerPID: os.Getpid(), Sequence: count, Kind: "user-input-forwarded"}
		return claudetape.PublishLifecycleControl(filepath.Join(home, "recording-children", fmt.Sprintf("%s.user-%06d", job.Role, count)), delivery)
	}
}
