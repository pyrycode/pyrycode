package claudetape

import (
	"bytes"
	"encoding/json"
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
