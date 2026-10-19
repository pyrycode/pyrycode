package claudetape

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDaemonSuiteLateArtifacts(t *testing.T) {
	prepare := func(t *testing.T) string {
		t.Helper()
		home := t.TempDir()
		for _, dir := range []string{"work", "recording-roles", "recording-children"} {
			if os.Mkdir(filepath.Join(home, dir), 0700) != nil {
				t.Fatal("mkdir")
			}
		}
		write := func(path string, value any) {
			raw, _ := json.Marshal(value)
			if os.WriteFile(filepath.Join(home, path), raw, 0600) != nil {
				t.Fatal("write")
			}
		}
		write("recording-endpoint.json", map[string]string{"Daemon": filepath.Join(home, "pyry"), "Socket": filepath.Join(home, "socket")})
		write("recording-roles/created-1.json", map[string]string{"Role": "created-1", "ConversationID": "created-conv", "SessionID": "11111111-1111-4111-8111-111111111111", "WorkDir": filepath.Join(home, "work")})
		control := suiteControl()
		for _, role := range []string{"bootstrap", "created-1"} {
			tape := control.Roles[role]
			tape.Scenario = "conversation-default-" + role
			tape.Files["$cwd"] = json.RawMessage(`"work"`)
			prefix := filepath.Join(home, "recording-children", role)
			if tape.Save(prefix+".json") != nil {
				t.Fatal("tape save")
			}
			if os.WriteFile(prefix+".claim", nil, 0600) != nil {
				t.Fatal("claim")
			}
			write("recording-children/"+role+".receipt", map[string]any{"success": true, "frames": len(tape.Frames)})
		}
		return home
	}
	for _, kind := range []string{"unexpected", "duplicate-claim", "extra-child", "extra-role", "changed-role", "changed-tape", "failed-receipt", "public-claim"} {
		t.Run(kind, func(t *testing.T) {
			home := prepare(t)
			before, err := AuditDaemonSuiteArtifacts(home, "conversation-default")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "recording-unexpected")
			raw := []byte{}
			switch kind {
			case "duplicate-claim":
				path = filepath.Join(home, "recording-children/bootstrap.claim.duplicate")
			case "extra-child":
				path = filepath.Join(home, "recording-children/unowned.receipt")
			case "extra-role":
				path = filepath.Join(home, "recording-roles/created-2.json")
				raw = []byte(`{}`)
			case "changed-role":
				path = filepath.Join(home, "recording-roles/created-1.json")
				raw = []byte(`{"Role":"created-1","ConversationID":"rebound","SessionID":"22222222-2222-4222-8222-222222222222","WorkDir":"` + filepath.Join(home, "work") + `"}`)
			case "changed-tape":
				path = filepath.Join(home, "recording-children/bootstrap.json")
				raw, _ = os.ReadFile(path)
				var tape Tape
				_ = json.Unmarshal(raw, &tape)
				tape.Frames[1].Data = json.RawMessage(`{"type":"result","result":"changed after seal"}`)
				raw, _ = json.Marshal(tape)
			case "failed-receipt":
				path = filepath.Join(home, "recording-children/bootstrap.receipt")
				raw = []byte(`{"success":false,"frames":2}`)
			case "public-claim":
				path = filepath.Join(home, "recording-children/bootstrap.claim")
				if os.Chmod(path, 0644) != nil {
					t.Fatal("chmod")
				}
			}
			if kind != "public-claim" {
				if os.WriteFile(path, raw, 0600) != nil {
					t.Fatal("late mutation")
				}
			}
			after, err := AuditDaemonSuiteArtifacts(home, "conversation-default")
			if err == nil && after == before {
				t.Fatal("post-cleanup audit accepted late artifact mutation")
			}
		})
	}
}
