package claudetape

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResumeSnapshotAuditRequiresOwnedEvictionAndExactInput(t *testing.T) {
	for _, mode := range []string{"capture", "replay"} {
		for _, mutation := range []string{"valid", "missing-terminal", "failed-terminal", "foreign-terminal-owner", "foreign-terminal-broker", "wrong-stage-kind", "foreign-stage-source", "rewritten-native-prefix", "foreign-resume-identity", "extra-snapshot", "foreign-replay-input"} {
			if mode == "capture" && mutation == "foreign-replay-input" {
				continue
			}
			t.Run(mode+"/"+mutation, func(t *testing.T) {
				home := t.TempDir()
				_ = os.Chmod(home, 0700)
				for _, dir := range []string{"work", "recording-roles", "recording-children", "recording-snapshots", "recording-snapshot-inputs"} {
					if os.Mkdir(filepath.Join(home, dir), 0700) != nil {
						t.Fatal("mkdir")
					}
				}
				write := func(path string, value any) {
					t.Helper()
					raw, err := json.Marshal(value)
					if err != nil || os.WriteFile(path, raw, 0600) != nil {
						t.Fatal("write")
					}
				}
				suite := resumeSuiteControl("complete original native transcript")
				write(filepath.Join(home, "recording-roles", "resumed.json"), LifecycleRoleBinding{Role: "resumed", SessionID: LifecycleResumeSession, ConversationID: LifecycleResumeConversation, WorkDir: filepath.Join(home, "work")})
				for i, role := range LifecycleProfiles()[suite.Scenario].Roles {
					prefix := filepath.Join(home, "recording-children", role)
					write(prefix+".job", LifecycleJob{Config: LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: mode}, OwnerPID: 1001 + i}})
					write(prefix+".broker-pid", 2001+i)
				}
				prefix := filepath.Join(home, "recording-children", "bootstrap")
				write(prefix+".user-000001", LifecycleDelivery{Version: 1, OwnerPID: 1001, BrokerPID: 2001, Sequence: 1, Kind: "user-input-forwarded"})
				receipt := LifecycleReceipt{Success: true, OwnerPID: 1001, BrokerPID: 2001, Frames: 3}
				write(prefix+".receipt", receipt)
				snapshot := suite.Snapshots["bootstrap"]
				output := filepath.Join(home, "recording-snapshots", "bootstrap.json")
				if snapshot.Save(output, home) != nil {
					t.Fatal("save")
				}
				raw, _ := os.ReadFile(output)
				sum := sha256.Sum256(raw)
				stage := LifecycleSnapshotStage{Version: 1, OwnerPID: 1001, BrokerPID: 2001, UserSequence: 1, Role: "bootstrap", Kind: "native-jsonl-after-owned-eviction", Revision: suite.Producer.Revision, SourceBinarySHA256: suite.Producer.BinarySHA256, SnapshotSHA256: hex.EncodeToString(sum[:])}
				stagePath := filepath.Join(home, "recording-snapshots", "bootstrap.stage")
				write(stagePath, stage)
				if PrepareSessionSnapshotDirectory(home, LifecycleResumeSession) != nil || InstallSessionSnapshot(snapshot, LifecycleNativePath(home, LifecycleResumeSession), LifecycleResumeSession, home) != nil {
					t.Fatal("native output")
				}
				if mode == "replay" {
					if snapshot.Save(filepath.Join(home, "recording-snapshot-inputs", "bootstrap.json"), home) != nil {
						t.Fatal("saved input")
					}
				}
				switch mutation {
				case "missing-terminal":
					_ = os.Remove(prefix + ".receipt")
				case "failed-terminal":
					receipt.Success = false
					write(prefix+".receipt", receipt)
				case "foreign-terminal-owner":
					receipt.OwnerPID = 3333
					write(prefix+".receipt", receipt)
				case "foreign-terminal-broker":
					receipt.BrokerPID = 3333
					write(prefix+".receipt", receipt)
				case "wrong-stage-kind":
					stage.Kind = "native-jsonl-after-user"
					write(stagePath, stage)
				case "foreign-stage-source":
					stage.SourceBinarySHA256 = "foreign"
					write(stagePath, stage)
				case "rewritten-native-prefix":
					_ = os.WriteFile(LifecycleNativePath(home, LifecycleResumeSession), []byte(`{"sessionId":"`+LifecycleResumeSession+`","message":"foreign prefix"}`+"\n"), 0600)
				case "foreign-resume-identity":
					write(filepath.Join(home, "recording-roles", "resumed.json"), LifecycleRoleBinding{Role: "resumed", SessionID: "11111111-1111-4111-8111-111111111111", ConversationID: LifecycleResumeConversation, WorkDir: filepath.Join(home, "work")})
				case "extra-snapshot":
					write(filepath.Join(home, "recording-snapshots", "resumed.json"), snapshot)
				case "foreign-replay-input":
					write(filepath.Join(home, "recording-snapshot-inputs", "bootstrap.json"), resumeSuiteControl("foreign native transcript").Snapshots["bootstrap"])
				}
				snapshots, hashes, err := ReadLifecycleSnapshots(home, suite.Scenario, suite.Producer.Revision, suite.Producer.BinarySHA256)
				if mutation == "valid" {
					want := 2
					if mode == "replay" {
						want = 3
					}
					if err != nil || len(snapshots) != 1 || len(hashes) != want {
						t.Fatal("valid owned eviction snapshot rejected", err)
					}
				} else if err == nil {
					t.Fatal("mutated predecessor dependency accepted")
				}
			})
		}
	}
}
