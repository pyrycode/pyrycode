package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recoveryControl(t *testing.T) *RecoverySuite {
	t.Helper()
	producer := suiteControl().Producer
	suite := &RecoverySuite{Version: 1, Scenario: RecoveryScenario, Producer: producer, DaemonSHA256: strings.Repeat("c", 64), Roles: map[string]RecoveryRole{}}
	for _, role := range RecoveryRoles() {
		tape := suiteControl().Roles["bootstrap"]
		raw, _ := json.Marshal(tape)
		var copy Tape
		_ = json.Unmarshal(raw, &copy)
		copy.Scenario = RecoveryScenario + "-" + role
		copy.Producer = producer
		copy.Files["$cwd"] = json.RawMessage(`"work"`)
		suite.Roles[role] = RecoveryRole{Tape: &copy, Transcript: RecoveryTranscript{SessionID: "77777777-7777-4777-8777-777777777777", Snapshot: CLISnapshot{Exists: true, Mode: 0600, Data: `{"type":"user","sessionId":"77777777-7777-4777-8777-777777777777","message":{"content":"test-owned ` + role + ` request ä literal-escape:\u00e4"}}` + "\n"}}}
	}
	return suite
}
func TestRecoverySuiteWholeRoleAndRawTranscriptContract(t *testing.T) {
	suite := recoveryControl(t)
	if err := suite.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(suite)
	decoded, err := DecodeRecoverySuite(bytes.NewReader(raw))
	if err != nil || SameRecoveryContract(suite, decoded) != nil {
		t.Fatal("valid composite recording rejected", err)
	}
	if decoded.Roles["retained"].Transcript.Snapshot.Data != suite.Roles["retained"].Transcript.Snapshot.Data {
		t.Fatal("transcript Unicode or encoding rewritten")
	}
	for _, kind := range []string{"missing-role", "extra-role", "wrong-session", "missing-session", "invalid-transcript", "private-transcript", "invalid-mode", "changed-request", "unknown-field", "duplicate-role"} {
		t.Run(kind, func(t *testing.T) {
			changed, err := DecodeRecoverySuite(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			value := changed.Roles["retained"]
			switch kind {
			case "missing-role":
				delete(changed.Roles, "dropped")
			case "extra-role":
				changed.Roles["extra"] = value
			case "wrong-session":
				value.Transcript.SessionID = "11111111-1111-4111-8111-111111111111"
				changed.Roles["retained"] = value
			case "missing-session":
				value.Transcript.Snapshot.Data = `{"type":"user"}`
				changed.Roles["retained"] = value
			case "invalid-transcript":
				value.Transcript.Snapshot.Data = `"unrelated JSON scalar"`
				changed.Roles["retained"] = value
			case "private-transcript":
				value.Transcript.Snapshot.Data = strings.ReplaceAll(value.Transcript.Snapshot.Data, "test-owned", strings.Repeat("sk-ant-", 1)+"credential-material-1234567890")
				changed.Roles["retained"] = value
			case "invalid-mode":
				value.Transcript.Snapshot.Mode = 01000
				changed.Roles["retained"] = value
			case "changed-request":
				value.Tape.Frames[0].Data = json.RawMessage(`{"type":"user","message":{"content":"changed"}}`)
				changed.Roles["retained"] = value
				if SameRecoveryContract(suite, changed) == nil {
					t.Fatal("changed native input accepted")
				}
				return
			case "unknown-field":
				if _, err := DecodeRecoverySuite(strings.NewReader(strings.TrimSuffix(string(raw), "}") + `,"unknown":true}`)); err == nil {
					t.Fatal("unknown envelope field accepted")
				}
				return
			case "duplicate-role":
				if _, err := DecodeRecoverySuite(strings.NewReader(strings.Replace(string(raw), `"roles":{`, `"roles":{"retained":null,`, 1))); err == nil {
					t.Fatal("duplicate role accepted")
				}
				return
			}
			if changed.Validate() == nil {
				t.Fatal("incomplete or private composite recording accepted")
			}
		})
	}
}
func TestRecoveryArtifactsRejectLateChanges(t *testing.T) {
	for _, kind := range []string{"complete", "unexpected", "duplicate-claim", "changed-transcript", "failed-receipt", "public-endpoint", "missing-seal"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			meta := filepath.Join(root, RecoveryMetadataDirectory)
			if os.Mkdir(meta, 0700) != nil {
				t.Fatal("metadata")
			}
			for name, mode := range map[string]os.FileMode{"config.json": 0600, "claude": 0700} {
				if os.WriteFile(filepath.Join(meta, name), nil, mode) != nil {
					t.Fatal("control")
				}
			}
			suite := recoveryControl(t)
			for role, value := range suite.Roles {
				prefix := filepath.Join(meta, role)
				if value.Tape.Save(prefix+".json") != nil {
					t.Fatal("tape")
				}
				for _, suffix := range []string{".claim", ".finish"} {
					if os.WriteFile(prefix+suffix, nil, 0600) != nil {
						t.Fatal("claim")
					}
				}
				receipt, _ := json.Marshal(struct {
					Success bool `json:"success"`
					Frames  int  `json:"frames"`
				}{true, len(value.Tape.Frames)})
				transcript, _ := json.Marshal(value.Transcript)
				endpoint, _ := json.Marshal(RecoveryEndpoint{Daemon: "/tmp/owned-daemon", Socket: "/tmp/owned-socket"})
				for suffix, raw := range map[string][]byte{".receipt": receipt, ".transcript": transcript, ".endpoint": endpoint} {
					if os.WriteFile(prefix+suffix, raw, 0600) != nil {
						t.Fatal("artifact")
					}
				}
				digest, err := AuditRecoveryRole(root, role)
				if err != nil {
					t.Fatal(err)
				}
				if os.WriteFile(prefix+".seal", []byte(digest), 0600) != nil {
					t.Fatal("seal")
				}
			}
			prefix := filepath.Join(meta, "retained")
			switch kind {
			case "unexpected":
				_ = os.WriteFile(filepath.Join(meta, "unexpected"), nil, 0600)
			case "duplicate-claim":
				_ = os.WriteFile(prefix+".claim.duplicate", nil, 0600)
			case "changed-transcript":
				_ = os.WriteFile(prefix+".transcript", []byte(`{}`), 0600)
			case "failed-receipt":
				_ = os.WriteFile(prefix+".receipt", []byte(`{"success":false,"frames":1}`), 0600)
			case "public-endpoint":
				_ = os.Chmod(prefix+".endpoint", 0644)
			case "missing-seal":
				if os.Remove(prefix+".seal") != nil {
					t.Fatal("remove own synthetic seal")
				}
			}
			roles, err := AuditRecoverySuiteArtifacts(root)
			if kind == "complete" {
				if err != nil || len(roles) != 2 {
					t.Fatal("valid closed artifacts rejected", err)
				}
			} else if err == nil {
				t.Fatal("late artifact mutation accepted")
			}
		})
	}
}
