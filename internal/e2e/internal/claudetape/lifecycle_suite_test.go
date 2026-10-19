package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func lifecycleSuiteControl() *LifecycleSuite {
	old := suiteControl()
	s := &LifecycleSuite{Version: 1, Scenario: "conversation-lifecycle", Producer: old.Producer, DaemonSHA256: old.DaemonSHA256, Roles: map[string]*LifecycleTape{}}
	for _, role := range []string{"bootstrap", "created-1"} {
		v := old.Roles[role]
		v.Scenario = s.Scenario + "-" + role
		v.Files["$cwd"] = json.RawMessage(`"work"`)
		v.Files["--append-system-prompt-file"] = json.RawMessage(`{"content":"complete controlled prompt","mode":384}`)
		v.Args = []string{"--session-id", daemonSession, "--settings", "${CHILD_FILE:--settings}", "--append-system-prompt-file", "${CHILD_FILE:--append-system-prompt-file}"}
		if role == "created-1" {
			v.Version = 2
			v.Frames = append(v.Frames, Frame{Direction: ClientCheckpointDirection, Data: json.RawMessage(`{"type":"client_checkpoint","sequence":1,"action":"lifecycle_delete","conversation_id":"${LIFECYCLE_CONVERSATION}"}`)})
		}
		s.Roles[role] = &LifecycleTape{Version: 1, Tape: v, End: LifecycleEnd(s.Scenario, role)}
	}
	return s
}
func TestLifecycleSuiteClosedRolesAndActions(t *testing.T) {
	s := lifecycleSuiteControl()
	roles := LifecycleProfiles()[s.Scenario].Roles
	raw, _ := json.Marshal(s)
	good, e := DecodeLifecycleSuite(bytes.NewReader(raw), roles)
	if e != nil || SameLifecycleSuiteContract(s, good, roles) != nil {
		t.Fatal("complete lifecycle suite rejected", e)
	}
	for _, kind := range []string{"missing-role", "extra-role", "wrong-action", "swapped-role", "wrong-cwd", "missing-file", "changed-input", "duplicate", "unknown", "trailing", "escaped-private-stderr"} {
		t.Run(kind, func(t *testing.T) {
			var x LifecycleSuite
			if json.Unmarshal(raw, &x) != nil {
				t.Fatal("control")
			}
			switch kind {
			case "missing-role":
				delete(x.Roles, "created-1")
			case "extra-role":
				x.Roles["extra"] = x.Roles["bootstrap"]
			case "wrong-action":
				x.Roles["created-1"].End = "seal"
			case "swapped-role":
				x.Roles["created-1"].Tape.Scenario = s.Scenario + "-bootstrap"
			case "wrong-cwd":
				x.Roles["created-1"].Tape.Files["$cwd"] = json.RawMessage(`"foreign"`)
			case "missing-file":
				delete(x.Roles["bootstrap"].Tape.Files, "--append-system-prompt-file")
			case "changed-input":
				x.Roles["created-1"].Tape.Frames[0].Data = json.RawMessage(`{"type":"user","message":{"content":"changed request"}}`)
			case "escaped-private-stderr":
				x.Roles["bootstrap"].Stderr = `{"hidden":"\u0073k-ant-synthetic-credential-1234567890"}`
			}
			candidate, _ := json.Marshal(x)
			switch kind {
			case "duplicate":
				candidate = bytes.Replace(candidate, []byte(`"roles":{`), []byte(`"roles":{"bootstrap":null,`), 1)
			case "unknown":
				candidate = append([]byte(`{"unknown":true,`), candidate[1:]...)
			case "trailing":
				candidate = append(candidate, []byte(`{}`)...)
			}
			decoded, e := DecodeLifecycleSuite(bytes.NewReader(candidate), roles)
			if e == nil && SameLifecycleSuiteContract(s, decoded, roles) == nil {
				t.Fatal("changed lifecycle contract accepted")
			}
		})
	}
}
func TestLifecycleArtifactsPinPrefixThroughOriginalCleanup(t *testing.T) {
	for _, mutation := range []string{"valid", "changed-tape", "changed-job", "foreign-stop", "changed-endpoint", "duplicate-claim", "extra-child", "extra-role", "failed-terminal", "missing-terminal", "public-control", "rebound-session", "foreign-stop-witness", "missing-stop-witness", "missing-action-ack"} {
		t.Run(mutation, func(t *testing.T) {
			home := t.TempDir()
			for _, dir := range []string{"recording-children", "recording-roles", "work"} {
				if os.Mkdir(filepath.Join(home, dir), 0700) != nil {
					t.Fatal("mkdir")
				}
			}
			write := func(path string, value any) {
				t.Helper()
				raw, e := json.Marshal(value)
				if e != nil || os.WriteFile(path, raw, 0600) != nil {
					t.Fatal("write")
				}
			}
			suite := lifecycleSuiteControl()
			daemon, socket := filepath.Join(home, "pyry"), filepath.Join(home, "socket")
			createdID := "11111111-1111-4111-8111-111111111111"
			conv := "22222222-2222-4222-8222-222222222222"
			write(filepath.Join(home, "finish"), struct{}{})
			write(filepath.Join(home, "recording-endpoint.json"), map[string]string{"Daemon": daemon, "Socket": socket})
			write(filepath.Join(home, "recording-roles", "created-1.json"), map[string]string{"Role": "created-1", "ConversationID": conv, "SessionID": createdID, "WorkDir": filepath.Join(home, "work")})
			jobs := map[string]LifecycleJob{}
			for index, role := range []string{"bootstrap", "created-1"} {
				session, conversation := LifecycleBootstrapSession, ""
				if role == "created-1" {
					session, conversation = createdID, conv
				}
				prefix := filepath.Join(home, "recording-children", role)
				args := []string{"--session-id", session, "--settings", filepath.Join(home, "settings.json"), "--append-system-prompt-file", filepath.Join(home, "prompt.txt")}
				j := LifecycleJob{Config: LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: "capture", Scenario: suite.Scenario + "-" + role, Path: prefix + ".json", Home: home, WorkDir: filepath.Join(home, "work"), Claim: prefix + ".claim", Finish: filepath.Join(home, "finish"), Producer: suite.Producer}, OwnerPID: 1001 + index, End: LifecycleEnd(suite.Scenario, role), Kill: prefix + ".action", Abort: prefix + ".abort"}, Args: args, Role: role, SessionID: session, ConversationID: conversation, WorkDir: filepath.Join(home, "work"), Daemon: daemon, Socket: socket, SourceBinarySHA256: suite.Producer.BinarySHA256}
				jobs[role] = j
				write(prefix+".job", j)
				write(prefix+".broker-pid", 2001+index)
				if os.WriteFile(prefix+".claim", nil, 0600) != nil {
					t.Fatal("claim")
				}
				if e := suite.Roles[role].Save(prefix + ".json"); e != nil {
					t.Fatal(e)
				}
				r := LifecycleReceipt{Success: true, Frames: len(suite.Roles[role].Tape.Frames), OwnerPID: 1001 + index, BrokerPID: 2001 + index}
				if role == "bootstrap" {
					write(prefix+".prefix", r)
				} else {
					write(prefix+".receipt", r)
					write(prefix+".action", LifecycleKill{PID: 1001 + index, Signal: "delete-conversation"})
					write(prefix+".action.ready", LifecycleKill{PID: 1001 + index, Signal: "delete-conversation"})
					write(prefix+".stopped", LifecycleStopObservation{PID: 1001 + index, Kind: "not-running"})
				}
			}
			before, _, e := AuditLifecycleArtifacts(home, suite.Scenario, suite.Producer.Revision, suite.Producer.BinarySHA256, suite.DaemonSHA256, false)
			if e != nil {
				t.Fatal("valid prefix rejected", e)
			}
			bootstrap := filepath.Join(home, "recording-children", "bootstrap")
			created := filepath.Join(home, "recording-children", "created-1")
			write(bootstrap+".receipt", LifecycleReceipt{Success: true, Frames: 2, OwnerPID: 1001, BrokerPID: 2001})
			switch mutation {
			case "changed-tape":
				suite.Roles["bootstrap"].Tape.Frames[1].Data = json.RawMessage(`{"type":"result","result":"changed after seal"}`)
				write(bootstrap+".json", suite.Roles["bootstrap"])
			case "changed-job":
				j := jobs["bootstrap"]
				j.Args = append(j.Args, "--verbose")
				write(bootstrap+".job", j)
			case "foreign-stop":
				write(created+".action", LifecycleKill{PID: 3333, Signal: "delete-conversation"})
			case "changed-endpoint":
				write(filepath.Join(home, "recording-endpoint.json"), map[string]string{"Daemon": daemon, "Socket": socket + "-rebound"})
			case "duplicate-claim":
				if os.WriteFile(created+".claim.duplicate", nil, 0600) != nil {
					t.Fatal("duplicate")
				}
			case "extra-child":
				write(filepath.Join(home, "recording-children", "unowned.job"), struct{}{})
			case "extra-role":
				write(filepath.Join(home, "recording-roles", "created-2.json"), struct{}{})
			case "failed-terminal":
				write(bootstrap+".receipt", LifecycleReceipt{Success: false, OwnerPID: 1001, BrokerPID: 2001})
			case "missing-terminal":
				if os.Remove(bootstrap+".receipt") != nil {
					t.Fatal("remove")
				}
			case "public-control":
				if os.Chmod(bootstrap+".job", 0644) != nil {
					t.Fatal("chmod")
				}
			case "foreign-stop-witness":
				write(created+".stopped", LifecycleStopObservation{PID: 3333, Kind: "not-running"})
			case "missing-stop-witness":
				if os.Remove(created+".stopped") != nil {
					t.Fatal("remove")
				}
			case "missing-action-ack":
				if os.Remove(created+".action.ready") != nil {
					t.Fatal("remove")
				}
			case "rebound-session":
				j := jobs["bootstrap"]
				j.SessionID = createdID
				j.Args[1] = createdID
				write(bootstrap+".job", j)
			}
			after, tapes, e := AuditLifecycleArtifacts(home, suite.Scenario, suite.Producer.Revision, suite.Producer.BinarySHA256, suite.DaemonSHA256, true)
			if mutation == "valid" {
				if e != nil || before != after || len(tapes) != 2 {
					t.Fatal("original cleanup changed immutable prefix", e)
				}
			} else if e == nil && before == after {
				t.Fatal("late lifecycle mutation accepted")
			}
		})
	}
}
