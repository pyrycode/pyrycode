package claudetape

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rotationSuiteControl() *LifecycleSuite {
	old := lifecycleSuiteControl()
	s := &LifecycleSuite{Version: 1, Scenario: "session-rotation", Producer: old.Producer, DaemonSHA256: old.DaemonSHA256, Roles: map[string]*LifecycleTape{}, Snapshots: map[string]SessionSnapshot{}}
	for _, role := range LifecycleProfiles()[s.Scenario].Roles {
		raw, _ := json.Marshal(old.Roles["bootstrap"])
		var tape LifecycleTape
		_ = json.Unmarshal(raw, &tape)
		tape.End = LifecycleEnd(s.Scenario, role)
		tape.Tape.Scenario = s.Scenario + "-" + role
		if role == "bootstrap" {
			tape.Tape.Version = 2
			tape.Tape.Frames = append(tape.Tape.Frames, Frame{Direction: ClientCheckpointDirection, Data: json.RawMessage(`{"type":"client_checkpoint","sequence":1,"action":"lifecycle_rotate","conversation_id":"${LIFECYCLE_CONVERSATION}"}`)})
			tape.Tape.Frames = append(tape.Tape.Frames, Frame{Direction: "stdin", Data: json.RawMessage(`{"type":"user","message":{"content":"original wrapup request"}}`)})
		}
		s.Roles[role] = &tape
		s.Snapshots[role] = SessionSnapshot{Version: 1, Mode: 0600, Data: `{"sessionId":"${LIFECYCLE_SESSION}","message":{"content":"native transcript evidence"}}` + "\n"}
	}
	return s
}

func TestRotationSuiteRequiresClosedSnapshotsAndTypedAction(t *testing.T) {
	base := rotationSuiteControl()
	roles := LifecycleProfiles()[base.Scenario].Roles
	raw, _ := json.Marshal(base)
	if _, err := DecodeLifecycleSuite(bytes.NewReader(raw), roles); err != nil {
		t.Fatal("valid rotation suite", err)
	}
	for _, kind := range []string{"missing-snapshot", "extra-snapshot", "public-mode", "foreign-session", "wrong-action", "missing-checkpoint", "wrong-end", "extra-role", "private-data"} {
		t.Run(kind, func(t *testing.T) {
			var s LifecycleSuite
			_ = json.Unmarshal(raw, &s)
			switch kind {
			case "missing-snapshot":
				delete(s.Snapshots, "bootstrap")
			case "extra-snapshot":
				s.Snapshots["extra"] = s.Snapshots["bootstrap"]
			case "public-mode":
				v := s.Snapshots["bootstrap"]
				v.Mode = 0644
				s.Snapshots["bootstrap"] = v
			case "foreign-session":
				v := s.Snapshots["bootstrap"]
				v.Data = strings.ReplaceAll(v.Data, snapshotSession, "foreign")
				s.Snapshots["bootstrap"] = v
			case "wrong-action":
				s.Roles["bootstrap"].Tape.Frames[2].Data = json.RawMessage(`{"type":"client_checkpoint","sequence":1,"action":"lifecycle_delete","conversation_id":"${LIFECYCLE_CONVERSATION}"}`)
			case "missing-checkpoint":
				s.Roles["bootstrap"].Tape.Frames = s.Roles["bootstrap"].Tape.Frames[:2]
			case "wrong-end":
				s.Roles["bootstrap"].End = "seal"
			case "extra-role":
				s.Roles["extra"] = s.Roles["bootstrap"]
			case "private-data":
				v := s.Snapshots["bootstrap"]
				v.Data = strings.Replace(v.Data, "native transcript evidence", "sk-ant-synthetic-credential-1234567890", 1)
				s.Snapshots["bootstrap"] = v
			}
			candidate, _ := json.Marshal(s)
			if _, err := DecodeLifecycleSuite(bytes.NewReader(candidate), roles); err == nil {
				t.Fatal("changed rotation contract accepted")
			}
		})
	}
}

func TestRotationSnapshotAuditPinsCausalProducerAndNativePrefix(t *testing.T) {
	for _, mode := range []string{"capture", "replay"} {
		for _, mutation := range []string{"valid", "delivery-owner", "delivery-broker", "delivery-sequence", "stage-revision", "stage-source", "stage-digest", "stage-duplicate", "missing-native", "native-rewritten", "public-native", "extra-snapshot", "public-input-directory", "extra-input", "changed-replay-input"} {
			if mode == "capture" && mutation == "changed-replay-input" {
				continue
			}
			t.Run(mode+"/"+mutation, func(t *testing.T) {
				home := t.TempDir()
				_ = os.Chmod(home, 0700)
				for _, dir := range []string{"work", "recording-children", "recording-roles", "recording-snapshots", "recording-snapshot-inputs"} {
					if err := os.Mkdir(filepath.Join(home, dir), 0700); err != nil {
						t.Fatal(err)
					}
				}
				write := func(path string, v any) {
					t.Helper()
					raw, err := json.Marshal(v)
					if err != nil || os.WriteFile(path, raw, 0600) != nil {
						t.Fatal("write")
					}
				}
				producer := rotationSuiteControl().Producer
				stages := map[string]LifecycleSnapshotStage{}
				for i, role := range LifecycleProfiles()["session-rotation"].Roles {
					session := LifecycleRotationSession
					if role == "rotated" {
						session = "11111111-1111-4111-8111-111111111111"
						write(filepath.Join(home, "recording-roles", role+".json"), LifecycleRoleBinding{Role: role, SessionID: session, ConversationID: LifecycleRotationConversation, WorkDir: filepath.Join(home, "work")})
					}
					prefix := filepath.Join(home, "recording-children", role)
					job := LifecycleJob{Config: LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: mode}, OwnerPID: 1001 + i}}
					write(prefix+".job", job)
					write(prefix+".broker-pid", 2001+i)
					write(prefix+".user-000001", LifecycleDelivery{Version: 1, OwnerPID: 1001 + i, BrokerPID: 2001 + i, Sequence: 1, Kind: "user-input-forwarded"})
					snapshot := rotationSuiteControl().Snapshots[role]
					output := filepath.Join(home, "recording-snapshots", role+".json")
					if err := snapshot.Save(output, home); err != nil {
						t.Fatal(err)
					}
					raw, err := os.ReadFile(output)
					if err != nil {
						t.Fatal(err)
					}
					sum := sha256.Sum256(raw)
					stage := LifecycleSnapshotStage{Version: 1, OwnerPID: 1001 + i, BrokerPID: 2001 + i, UserSequence: 1, Role: role, Kind: "native-jsonl-after-user", Revision: producer.Revision, SourceBinarySHA256: producer.BinarySHA256, SnapshotSHA256: hex.EncodeToString(sum[:])}
					stages[role] = stage
					write(filepath.Join(home, "recording-snapshots", role+".stage"), stage)
					path := LifecycleNativePath(home, session)
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := InstallSessionSnapshot(snapshot, path, session, home); err != nil {
						t.Fatal(err)
					}
					// Native activity may append after the causal snapshot without rewriting it.
					f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = f.WriteString(`{"sessionId":"` + session + `","type":"result"}` + "\n")
					_ = f.Close()
					if mode == "replay" {
						if err := snapshot.Save(filepath.Join(home, "recording-snapshot-inputs", role+".json"), home); err != nil {
							t.Fatal(err)
						}
					}
				}
				prefix := filepath.Join(home, "recording-children", "bootstrap")
				stagePath := filepath.Join(home, "recording-snapshots", "bootstrap.stage")
				stage := stages["bootstrap"]
				path := LifecycleNativePath(home, LifecycleRotationSession)
				switch mutation {
				case "delivery-owner":
					write(prefix+".user-000001", LifecycleDelivery{Version: 1, OwnerPID: 3333, BrokerPID: 2001, Sequence: 1, Kind: "user-input-forwarded"})
				case "delivery-broker":
					write(prefix+".user-000001", LifecycleDelivery{Version: 1, OwnerPID: 1001, BrokerPID: 3333, Sequence: 1, Kind: "user-input-forwarded"})
				case "delivery-sequence":
					write(prefix+".user-000001", LifecycleDelivery{Version: 1, OwnerPID: 1001, BrokerPID: 2001, Sequence: 2, Kind: "user-input-forwarded"})
				case "stage-revision":
					stage.Revision = "foreign"
					write(stagePath, stage)
				case "stage-source":
					stage.SourceBinarySHA256 = "foreign"
					write(stagePath, stage)
				case "stage-digest":
					stage.SnapshotSHA256 = "foreign"
					write(stagePath, stage)
				case "stage-duplicate":
					raw, _ := os.ReadFile(stagePath)
					raw = append([]byte(`{"Role":"foreign",`), raw[1:]...)
					_ = os.WriteFile(stagePath, raw, 0600)
				case "missing-native":
					_ = os.Remove(path)
				case "native-rewritten":
					_ = os.WriteFile(path, []byte(`{"sessionId":"`+LifecycleRotationSession+`","message":"different bytes"}`+"\n"), 0600)
				case "public-native":
					_ = os.Chmod(path, 0644)
				case "extra-snapshot":
					write(filepath.Join(home, "recording-snapshots", "foreign.json"), struct{}{})
				case "public-input-directory":
					_ = os.Chmod(filepath.Join(home, "recording-snapshot-inputs"), 0755)
				case "extra-input":
					write(filepath.Join(home, "recording-snapshot-inputs", "foreign.json"), struct{}{})
				case "changed-replay-input":
					s := rotationSuiteControl().Snapshots["bootstrap"]
					s.Data = strings.Replace(s.Data, "native transcript evidence", "different native bytes", 1)
					write(filepath.Join(home, "recording-snapshot-inputs", "bootstrap.json"), s)
				}
				snapshots, hashes, err := ReadLifecycleSnapshots(home, "session-rotation", producer.Revision, producer.BinarySHA256)
				if mutation == "valid" {
					if err != nil || len(snapshots) != 2 || len(hashes) != map[string]int{"capture": 4, "replay": 6}[mode] {
						t.Fatal("valid causal snapshot rejected", err)
					}
				} else if err == nil {
					t.Fatal("mutated causal snapshot accepted")
				}
			})
		}
	}
}
