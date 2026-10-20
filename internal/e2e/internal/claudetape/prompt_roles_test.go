package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func promptSuiteControl() *LifecycleSuite {
	old := lifecycleSuiteControl()
	s := &LifecycleSuite{Version: 1, Scenario: "conversation-system-prompt", Producer: old.Producer, DaemonSHA256: old.DaemonSHA256, Roles: map[string]*LifecycleTape{}}
	for _, role := range LifecycleProfiles()[s.Scenario].Roles {
		raw, _ := json.Marshal(old.Roles["bootstrap"])
		var tape LifecycleTape
		_ = json.Unmarshal(raw, &tape)
		tape.Tape.Scenario = s.Scenario + "-" + role
		tape.End = "seal"
		tape.Timeline = &LifecycleTimeline{Kind: lifecycleTimelineKind, OutputAfterNS: []int64{}}
		for _, frame := range tape.Tape.Frames {
			if frame.Direction == "stdout" {
				tape.Timeline.OutputAfterNS = append(tape.Timeline.OutputAfterNS, 0)
			}
		}
		s.Roles[role] = &tape
	}
	return s
}
func TestPromptSuiteRetainsSeparatePromptFilesAndSchedules(t *testing.T) {
	s := promptSuiteControl()
	roles := LifecycleProfiles()[s.Scenario].Roles
	if s.Validate(roles) != nil {
		t.Fatal("valid prompt suite")
	}
	raw, _ := json.Marshal(s)
	for _, mutation := range []string{"missing-timeline", "foreign-role", "changed-prompt", "changed-mode"} {
		t.Run(mutation, func(t *testing.T) {
			var x LifecycleSuite
			_ = json.Unmarshal(raw, &x)
			switch mutation {
			case "missing-timeline":
				x.Roles["bare"].Timeline = nil
			case "foreign-role":
				x.Roles["bare"].Tape.Scenario = s.Scenario + "-prompted"
			case "changed-prompt":
				x.Roles["prompted"].Tape.Files["--append-system-prompt-file"] = json.RawMessage(`{"content":"foreign prompt","mode":384}`)
			case "changed-mode":
				x.Roles["prompted"].Tape.Files["--append-system-prompt-file"] = json.RawMessage(`{"content":"complete controlled prompt","mode":420}`)
			}
			candidate, _ := json.Marshal(x)
			decoded, err := DecodeLifecycleSuite(bytes.NewReader(candidate), roles)
			if err == nil && SameLifecycleSuiteContract(s, decoded, roles) == nil {
				t.Fatal("changed role, prompt or schedule accepted")
			}
		})
	}
}
func TestPromptRolesRejectSwappedPersistedIdentity(t *testing.T) {
	home := t.TempDir()
	b := LifecycleRoleBinding{Role: "prompted", SessionID: "aaaaaaaa-2222-4222-8222-aaaaaaaaaaaa", ConversationID: "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa", WorkDir: filepath.Join(home, "work")}
	if !validLifecycleRoleBinding(home, "conversation-system-prompt", "prompted", b) {
		t.Fatal("original identity rejected")
	}
	b.Role = "bare"
	if validLifecycleRoleBinding(home, "conversation-system-prompt", "bare", b) {
		t.Fatal("cross-conversation identity accepted")
	}
}

func TestPromptArtifactAuditJoinsAllThreeRoles(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{"recording-children", "recording-roles", "work"} {
		if os.Mkdir(filepath.Join(home, dir), 0700) != nil {
			t.Fatal("mkdir")
		}
	}
	write := func(path string, v any) {
		t.Helper()
		raw, e := json.Marshal(v)
		if e != nil || os.WriteFile(path, raw, 0600) != nil {
			t.Fatal("write")
		}
	}
	suite := promptSuiteControl()
	daemon, socket := filepath.Join(home, "pyry"), filepath.Join(home, "socket")
	write(filepath.Join(home, "finish"), struct{}{})
	write(filepath.Join(home, "recording-endpoint.json"), map[string]string{"Daemon": daemon, "Socket": socket})
	bindings := map[string]LifecycleRoleBinding{
		"bare":     {Role: "bare", SessionID: "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb", ConversationID: "bbbbbbbb-1111-4111-8111-bbbbbbbbbbbb", WorkDir: filepath.Join(home, "work")},
		"prompted": {Role: "prompted", SessionID: "aaaaaaaa-2222-4222-8222-aaaaaaaaaaaa", ConversationID: "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa", WorkDir: filepath.Join(home, "work")},
	}
	for role, b := range bindings {
		write(filepath.Join(home, "recording-roles", role+".json"), b)
	}
	for i, role := range LifecycleProfiles()[suite.Scenario].Roles {
		b, err := ReadLifecycleRole(home, suite.Scenario, role)
		if err != nil {
			t.Fatal(err)
		}
		prefix := filepath.Join(home, "recording-children", role)
		args := []string{"--session-id", b.SessionID, "--settings", filepath.Join(home, "settings.json"), "--append-system-prompt-file", filepath.Join(home, "prompt.txt")}
		j := LifecycleJob{Config: LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: "capture", Scenario: suite.Scenario + "-" + role, Path: prefix + ".json", Home: home, WorkDir: b.WorkDir, Claim: prefix + ".claim", Finish: filepath.Join(home, "finish"), Producer: suite.Producer}, OwnerPID: 1001 + i, End: "seal", Kill: prefix + ".action", Abort: prefix + ".abort"}, Args: args, Role: role, SessionID: b.SessionID, ConversationID: b.ConversationID, WorkDir: b.WorkDir, Daemon: daemon, Socket: socket, SourceBinarySHA256: suite.Producer.BinarySHA256}
		write(prefix+".job", j)
		write(prefix+".broker-pid", 2001+i)
		if os.WriteFile(prefix+".claim", nil, 0600) != nil || suite.Roles[role].Save(prefix+".json") != nil {
			t.Fatal("private artifact")
		}
		receipt := LifecycleReceipt{Success: true, Frames: len(suite.Roles[role].Tape.Frames), OwnerPID: 1001 + i, BrokerPID: 2001 + i}
		write(prefix+".prefix", receipt)
	}
	before, tapes, err := AuditLifecycleArtifacts(home, suite.Scenario, suite.Producer.Revision, suite.Producer.BinarySHA256, suite.DaemonSHA256, 0, false)
	if err != nil || len(tapes) != 3 {
		t.Fatal("three-role prefix rejected", err)
	}
	for i, role := range LifecycleProfiles()[suite.Scenario].Roles {
		write(filepath.Join(home, "recording-children", role)+".receipt", LifecycleReceipt{Success: true, Frames: len(suite.Roles[role].Tape.Frames), OwnerPID: 1001 + i, BrokerPID: 2001 + i})
	}
	after, _, err := AuditLifecycleArtifacts(home, suite.Scenario, suite.Producer.Revision, suite.Producer.BinarySHA256, suite.DaemonSHA256, 0, true)
	if err != nil || before != after {
		t.Fatal("three-role cleanup rejected", err)
	}
	foreign := bindings["bare"]
	foreign.SessionID = bindings["prompted"].SessionID
	write(filepath.Join(home, "recording-roles", "bare.json"), foreign)
	if _, _, err := AuditLifecycleArtifacts(home, suite.Scenario, suite.Producer.Revision, suite.Producer.BinarySHA256, suite.DaemonSHA256, 0, true); err == nil {
		t.Fatal("foreign persisted role accepted")
	}
}
