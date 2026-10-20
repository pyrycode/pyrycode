package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	for _, mutation := range []string{"valid", "changed-tape", "changed-job", "foreign-stop", "changed-endpoint", "duplicate-claim", "extra-child", "extra-role", "failed-terminal", "missing-terminal", "public-control", "rebound-session", "early-created-terminal", "missing-created-terminal", "missing-action-ack"} {
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
				write(prefix+".prefix", r)
				if role == "created-1" {
					write(prefix+".action", LifecycleKill{PID: 1001 + index, Signal: "delete-conversation"})
					write(prefix+".action.ready", LifecycleKill{PID: 1001 + index, Signal: "delete-conversation"})
				}
			}
			before, _, e := AuditLifecycleArtifacts(home, suite.Scenario, suite.Producer.Revision, suite.Producer.BinarySHA256, suite.DaemonSHA256, 0, false)
			if e != nil {
				t.Fatal("valid prefix rejected", e)
			}
			bootstrap := filepath.Join(home, "recording-children", "bootstrap")
			created := filepath.Join(home, "recording-children", "created-1")
			write(bootstrap+".receipt", LifecycleReceipt{Success: true, Frames: 2, OwnerPID: 1001, BrokerPID: 2001})
			write(created+".receipt", LifecycleReceipt{Success: true, Frames: len(suite.Roles["created-1"].Tape.Frames), OwnerPID: 1002, BrokerPID: 2002})
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
			case "early-created-terminal":
				write(created+".prefix", LifecycleReceipt{Success: false, OwnerPID: 1002, BrokerPID: 2002})
			case "missing-created-terminal":
				if os.Remove(created+".receipt") != nil {
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
			after, tapes, e := AuditLifecycleArtifacts(home, suite.Scenario, suite.Producer.Revision, suite.Producer.BinarySHA256, suite.DaemonSHA256, 0, true)
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

func TestLifecycleLaunchRequiresKernelPeerAndClosedCurrentJob(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{"recording-children", "recording-roles", "work"} {
		if os.Mkdir(filepath.Join(home, dir), 0700) != nil {
			t.Fatal("mkdir")
		}
	}
	base := PoolProcessConfig{Mode: "capture", Scenario: "conversation-lifecycle", Home: home, WorkDir: filepath.Join(home, "work"), Claude: filepath.Join(home, "native"), Finish: filepath.Join(home, "finish"), Producer: suiteControl().Producer}
	daemon, socket := filepath.Join(home, "pyry"), filepath.Join(home, "socket")
	write := func(path string, v any) {
		raw, _ := json.Marshal(v)
		if os.WriteFile(path, raw, 0600) != nil {
			t.Fatal("write")
		}
	}
	write(filepath.Join(home, "recording-endpoint.json"), map[string]string{"Daemon": daemon, "Socket": socket})
	sourceSHA := base.Producer.BinarySHA256
	parent, peer := os.Getpid(), os.Getpid()+100
	prefix := filepath.Join(home, "recording-children", "bootstrap")
	process := base
	process.Scenario += "-bootstrap"
	process.Path = prefix + ".json"
	process.Claim = prefix + ".claim"
	initial := LifecycleJob{Config: LifecycleBrokerConfig{PoolProcessConfig: process, OwnerPID: peer, BrokerParentPID: parent, End: "seal", Kill: prefix + ".action", Abort: prefix + ".abort"}, Args: []string{"--session-id", LifecycleBootstrapSession, "--settings", filepath.Join(home, "settings.json"), "--append-system-prompt-file", filepath.Join(home, "prompt.txt")}, Role: "bootstrap", SessionID: LifecycleBootstrapSession, WorkDir: base.WorkDir, Daemon: daemon, Socket: socket, SourceBinarySHA256: sourceSHA}
	jobPath := prefix + ".job"
	if err := ValidateLifecycleLaunch(initial, jobPath, peer, parent, base, sourceSHA, daemon); err != nil {
		t.Fatal("valid current launch", err)
	}
	for _, kind := range []string{"kernel-peer", "parent", "source", "producer", "mode", "unknown-role", "foreign-output", "foreign-claim", "foreign-abort", "foreign-finish", "deadline", "foreign-endpoint", "foreign-argv-session", "foreign-role-session", "unknown-argument", "resume", "cwd", "native-executable"} {
		t.Run(kind, func(t *testing.T) {
			raw, _ := json.Marshal(initial)
			var j LifecycleJob
			if json.Unmarshal(raw, &j) != nil {
				t.Fatal("clone")
			}
			switch kind {
			case "kernel-peer":
				j.Config.OwnerPID++
			case "parent":
				j.Config.BrokerParentPID++
			case "source":
				j.SourceBinarySHA256 = "foreign"
			case "producer":
				j.Config.Producer.Revision = "foreign"
			case "mode":
				j.Config.Mode = "replay"
			case "unknown-role":
				j.Role = "extra"
			case "foreign-output":
				j.Config.Path = filepath.Join(home, "foreign.json")
			case "foreign-claim":
				j.Config.Claim += "-foreign"
			case "foreign-abort":
				j.Config.Abort += "-foreign"
			case "foreign-finish":
				j.Config.Finish += "-foreign"
			case "deadline":
				j.Config.Deadline = time.Now()
			case "foreign-endpoint":
				j.Socket += "-foreign"
			case "foreign-argv-session":
				j.Args[1] = "11111111-1111-4111-8111-111111111111"
			case "foreign-role-session":
				j.SessionID = "11111111-1111-4111-8111-111111111111"
			case "unknown-argument":
				j.Args = append(j.Args, "--foreign")
			case "resume":
				j.Args[0] = "--resume"
			case "cwd":
				j.WorkDir += "-foreign"
			case "native-executable":
				j.Config.Claude += "-foreign"
			}
			if err := ValidateLifecycleLaunch(j, jobPath, peer, parent, base, sourceSHA, daemon); err == nil {
				t.Fatal("unowned launch accepted")
			}
		})
	}
}

func TestSkippedResetRolesPreserveDistinctBootstrapAndBoundIdentity(t *testing.T) {
	home := t.TempDir()
	valid := LifecycleRoleBinding{Role: "bound", SessionID: LifecycleSkipSession, ConversationID: LifecycleSkipConversation, WorkDir: filepath.Join(home, "work")}
	if !validLifecycleRoleBinding(home, "clear-wrapup-skipped", "bound", valid) {
		t.Fatal("fixed original bound identity rejected")
	}
	for _, kind := range []string{"wrong-session", "wrong-conversation", "bootstrap-reuse", "successor-reuse", "foreign-workdir"} {
		t.Run(kind, func(t *testing.T) {
			b := valid
			role := "bound"
			switch kind {
			case "wrong-session":
				b.SessionID = "11111111-1111-4111-8111-111111111111"
			case "wrong-conversation":
				b.ConversationID = "11111111-1111-4111-8111-111111111111"
			case "bootstrap-reuse":
				b.SessionID = LifecycleSkipBootstrap
			case "successor-reuse":
				b.Role = "rotated"
				role = "rotated"
			case "foreign-workdir":
				b.WorkDir = home
			}
			if validLifecycleRoleBinding(home, "clear-wrapup-skipped", role, b) {
				t.Fatal("rebound original reset identity accepted")
			}
		})
	}
	original := lifecycleSuiteControl()
	suite := &LifecycleSuite{Version: 1, Scenario: "clear-wrapup-skipped", Producer: original.Producer, DaemonSHA256: original.DaemonSHA256, Roles: map[string]*LifecycleTape{}}
	for _, role := range LifecycleProfiles()[suite.Scenario].Roles {
		raw, _ := json.Marshal(original.Roles["bootstrap"])
		var tape LifecycleTape
		_ = json.Unmarshal(raw, &tape)
		tape.End = LifecycleEnd(suite.Scenario, role)
		tape.Tape.Scenario = suite.Scenario + "-" + role
		if role == "bound" {
			tape.Tape.Version = 2
			tape.Tape.Frames = append(tape.Tape.Frames, Frame{Direction: ClientCheckpointDirection, Data: json.RawMessage(`{"type":"client_checkpoint","sequence":1,"action":"lifecycle_rotate","conversation_id":"${LIFECYCLE_CONVERSATION}"}`)})
		}
		suite.Roles[role] = &tape
	}
	roles := LifecycleProfiles()[suite.Scenario].Roles
	if err := suite.Validate(roles); err != nil {
		t.Fatal("valid complete three-role reset", err)
	}
	delete(suite.Roles, "rotated")
	if err := suite.Validate(roles); err == nil {
		t.Fatal("reset without actual successor accepted")
	}
}
