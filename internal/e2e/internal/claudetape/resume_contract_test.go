package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resumeSuiteControl(note string) *LifecycleSuite {
	old := lifecycleSuiteControl()
	suite := &LifecycleSuite{Version: 1, Scenario: "session-resume", Producer: old.Producer, DaemonSHA256: old.DaemonSHA256, Roles: map[string]*LifecycleTape{}, Snapshots: map[string]SessionSnapshot{}}
	snapshot := SessionSnapshot{Version: 1, Mode: 0600, Data: `{"sessionId":"${LIFECYCLE_SESSION}","message":{"content":"` + note + `"}}` + "\n"}
	suite.Snapshots["bootstrap"] = snapshot
	for _, role := range LifecycleProfiles()[suite.Scenario].Roles {
		raw, _ := json.Marshal(old.Roles["bootstrap"])
		var tape LifecycleTape
		_ = json.Unmarshal(raw, &tape)
		tape.End = LifecycleEnd(suite.Scenario, role)
		tape.Tape.Scenario = suite.Scenario + "-" + role
		if role == "bootstrap" {
			tape.Tape.Version = 2
			tape.Tape.Frames = append(tape.Tape.Frames, Frame{Direction: ClientCheckpointDirection, Data: json.RawMessage(`{"type":"client_checkpoint","sequence":1,"action":"lifecycle_idle_eviction","conversation_id":"${LIFECYCLE_CONVERSATION}"}`)})
		} else {
			tape.Tape.Args[0] = "--resume"
			tape.Tape.Files["$resume-input"], _ = json.Marshal(snapshot)
		}
		suite.Roles[role] = &tape
	}
	return suite
}
func TestResumeContractRequiresCompleteOwnedNativeOutput(t *testing.T) {
	left, right := resumeSuiteControl("first complete native transcript"), resumeSuiteControl("different complete native transcript")
	roles := LifecycleProfiles()[left.Scenario].Roles
	raw, _ := json.MarshalIndent(left, "", "  ")
	if _, err := DecodeLifecycleSuite(bytes.NewReader(raw), roles); err != nil {
		t.Fatal("complete native resume source", err)
	}
	if SameContract(left.Roles["resumed"].Tape, right.Roles["resumed"].Tape) == nil {
		t.Fatal("literal different resume inputs match")
	}
	before, _ := json.Marshal(left)
	if err := SameLifecycleSuiteContract(left, right, roles); err != nil {
		t.Fatal("own complete causal source", err)
	}
	after, _ := json.Marshal(left)
	if !bytes.Equal(before, after) {
		t.Fatal("resume comparator changed fixture bytes")
	}
	for _, kind := range []string{"foreign-input", "missing-input", "missing-output", "extra-output", "public-mode", "fresh-argv", "wrong-session", "wrong-action", "changed-prompt", "changed-request"} {
		t.Run(kind, func(t *testing.T) {
			candidate := resumeSuiteControl("first complete native transcript")
			switch kind {
			case "foreign-input":
				candidate.Roles["resumed"].Tape.Files["$resume-input"] = right.Roles["resumed"].Tape.Files["$resume-input"]
			case "missing-input":
				delete(candidate.Roles["resumed"].Tape.Files, "$resume-input")
			case "missing-output":
				delete(candidate.Snapshots, "bootstrap")
			case "extra-output":
				candidate.Snapshots["foreign"] = candidate.Snapshots["bootstrap"]
			case "public-mode":
				s := candidate.Snapshots["bootstrap"]
				s.Mode = 0644
				candidate.Snapshots["bootstrap"] = s
				candidate.Roles["resumed"].Tape.Files["$resume-input"], _ = json.Marshal(s)
			case "fresh-argv":
				candidate.Roles["resumed"].Tape.Args[0] = "--session-id"
			case "wrong-session":
				candidate.Roles["resumed"].Tape.Args[1] = "11111111-1111-4111-8111-111111111111"
			case "wrong-action":
				candidate.Roles["bootstrap"].Tape.Frames[2].Data = json.RawMessage(`{"type":"client_checkpoint","sequence":1,"action":"lifecycle_rotate","conversation_id":"${LIFECYCLE_CONVERSATION}"}`)
			case "changed-prompt":
				candidate.Roles["resumed"].Tape.Files["--append-system-prompt-file"] = json.RawMessage(`{"content":"changed complete prompt","mode":384}`)
			case "changed-request":
				candidate.Roles["resumed"].Tape.Frames[0].Data = json.RawMessage(`{"type":"user","message":{"content":"different original user request"}}`)
			}
			if SameLifecycleSuiteContract(left, candidate, roles) == nil {
				t.Fatal("changed or unproved resume input accepted")
			}
		})
	}
}
func TestResumeBindingChecksExactNativeFileBeforeReadiness(t *testing.T) {
	home := t.TempDir()
	_ = os.Chmod(home, 0700)
	if os.Mkdir(filepath.Join(home, "recording-snapshots"), 0700) != nil {
		t.Fatal("mkdir")
	}
	snapshot := resumeSuiteControl("complete native bytes").Snapshots["bootstrap"]
	if err := snapshot.Save(filepath.Join(home, "recording-snapshots", "bootstrap.json"), home); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSessionSnapshotDirectory(home, LifecycleResumeSession); err != nil {
		t.Fatal(err)
	}
	path := LifecycleNativePath(home, LifecycleResumeSession)
	if err := InstallSessionSnapshot(snapshot, path, LifecycleResumeSession, home); err != nil {
		t.Fatal(err)
	}
	settings, prompt := filepath.Join(home, "settings.json"), filepath.Join(home, "prompt.txt")
	if os.WriteFile(settings, []byte(`{}`), 0600) != nil || os.WriteFile(prompt, []byte("complete literal prompt"), 0600) != nil {
		t.Fatal("inputs")
	}
	args := []string{"--resume", LifecycleResumeSession, "--settings", settings, "--append-system-prompt-file", prompt}
	daemon, socket, work := filepath.Join(home, "pyry"), filepath.Join(home, "socket"), filepath.Join(home, "work")
	if _, err := NewDaemonBinding(args, home, work, daemon, socket); err == nil {
		t.Fatal("fresh binding accepted resume")
	}
	if _, err := NewLifecycleDaemonBinding(args, home, work, daemon, socket, "session-resume", "bootstrap"); err == nil {
		t.Fatal("bootstrap accepted resume form")
	}
	binding, err := NewLifecycleDaemonBinding(args, home, work, daemon, socket, "session-resume", "resumed")
	if err != nil {
		t.Fatal(err)
	}
	files, err := binding.Files()
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := DecodeSessionSnapshot(bytes.NewReader(files["$resume-input"]))
	if err != nil || recorded != snapshot {
		t.Fatal("complete native input not recorded", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(path, append(raw, []byte(`{"sessionId":"`+LifecycleResumeSession+`","message":"unexpected input tail"}`+"\n")...), 0600) != nil {
		t.Fatal("mutate")
	}
	if _, err := binding.Files(); err == nil {
		t.Fatal("changed native transcript accepted before readiness")
	}
}
func TestSnapshotDirectoryPreparationNeverTraversesAncestorSymlinks(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if os.Symlink(outside, filepath.Join(home, ".claude")) != nil {
		t.Fatal("symlink")
	}
	if err := PrepareSessionSnapshotDirectory(home, LifecycleResumeSession); err == nil {
		t.Fatal("foreign ancestor traversed")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("created directory through foreign ancestor")
	}
	for _, session := range []string{"", "../outside", strings.Repeat("x", 4096)} {
		if PrepareSessionSnapshotDirectory(home, session) == nil {
			t.Fatal("foreign session path accepted")
		}
	}
}
