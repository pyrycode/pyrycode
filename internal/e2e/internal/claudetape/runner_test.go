package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runnerControl(t *testing.T) (*RunnerSuite, map[string]*RunnerBinding) {
	t.Helper()
	p := suiteControl().Producer
	s := &RunnerSuite{Version: 1, Scenario: "runner-applied-settings", Producer: p, RunnerSHA256: p.BinarySHA256, Roles: map[string]*Tape{}}
	bindings := map[string]*RunnerBinding{}
	home := t.TempDir()
	for role, spec := range RunnerProfiles()[s.Scenario].Roles {
		work := filepath.Join(home, spec.Workdir)
		if os.Mkdir(work, 0700) != nil {
			t.Fatal("workdir")
		}
		args := []string{"--model", "opus", "--setting-sources", "", "--dangerously-skip-permissions", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--session-id", spec.SessionID}
		if role == "explicit" {
			args = append(args, "--effort", "low")
		}
		b, err := NewRunnerBinding(s.Scenario, role, args, home, work)
		if err != nil {
			t.Fatal(err)
		}
		bindings[role] = b
		files, err := b.Files()
		if err != nil {
			t.Fatal(err)
		}
		effort := "null"
		if role == "explicit" {
			effort = `"low"`
		}
		s.Roles[role] = &Tape{Version: 1, Scenario: s.Scenario + "-" + role, Producer: p, Args: b.Args(), Files: files, Frames: []Frame{
			{Direction: "stdin", Data: json.RawMessage(`{"type":"control_request","request_id":"settings","request":{"subtype":"get_settings"}}`)},
			{Direction: "stdout", Data: json.RawMessage(`{"type":"control_response","response":{"subtype":"success","request_id":"settings","response":{"applied":{"model":"resolved-opus","effort":` + effort + `}}}}`)},
		}}
	}
	return s, bindings
}
func TestRunnerSuiteRoleAndTypedSettingsContract(t *testing.T) {
	s, bindings := runnerControl(t)
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	decoded, err := DecodeRunnerSuite(bytes.NewReader(raw))
	if err != nil || SameRunnerSuiteContract(s, decoded) != nil {
		t.Fatal("valid runner suite rejected", err)
	}
	if !bytes.Contains(decoded.Roles["inherited"].Frames[1].Data, []byte(`"effort":null`)) {
		t.Fatal("nullable effort rewritten")
	}
	for _, kind := range []string{"missing-role", "extra-role", "binary-provenance", "unknown-field", "duplicate-role", "changed-query", "changed-effort", "unknown-argv", "extra-file"} {
		t.Run(kind, func(t *testing.T) {
			altered, err := DecodeRunnerSuite(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unknown-argv":
				altered.Roles["inherited"].Args = append(altered.Roles["inherited"].Args, "--unknown")
			case "extra-file":
				altered.Roles["inherited"].Files["extra"] = json.RawMessage(`{}`)
			case "missing-role":
				delete(altered.Roles, "explicit")
			case "extra-role":
				altered.Roles["unexpected"] = altered.Roles["inherited"]
			case "binary-provenance":
				altered.RunnerSHA256 = strings.Repeat("e", 64)
			case "changed-query":
				altered.Roles["inherited"].Frames[0].Data = json.RawMessage(`{"type":"control_request","request_id":"settings","request":{"subtype":"get_settings","extra":true}}`)
				if SameRunnerSuiteContract(s, altered) == nil {
					t.Fatal("changed query accepted")
				}
				return
			case "changed-effort":
				altered.Roles["explicit"].Args = append([]string(nil), altered.Roles["explicit"].Args...)
				altered.Roles["explicit"].Args[len(altered.Roles["explicit"].Args)-1] = "high"
				if SameRunnerSuiteContract(s, altered) == nil {
					t.Fatal("changed explicit effort accepted")
				}
				return
			case "unknown-field":
				if _, err := DecodeRunnerSuite(strings.NewReader(strings.TrimSuffix(string(raw), "}") + `,"unknown":true}`)); err == nil {
					t.Fatal("unknown field accepted")
				}
				return
			case "duplicate-role":
				if _, err := DecodeRunnerSuite(strings.NewReader(strings.Replace(string(raw), `"roles":{`, `"roles":{"inherited":null,`, 1))); err == nil {
					t.Fatal("duplicate role accepted")
				}
				return
			}
			if altered.Validate() == nil {
				t.Fatal("incomplete ownership accepted")
			}
		})
	}
	b := bindings["inherited"]
	id := b.session
	native := []byte(`{"type":"system","subtype":"init","session_id":"` + id + `","cwd":"` + b.workdir + `","nested":{"session_id":"` + id + `"}}`)
	normalized, err := b.Frame(native, false)
	if err != nil || !bytes.Contains(normalized, []byte(`"session_id":"${RUNNER_SESSION}"`)) || !bytes.Contains(normalized, []byte(`"nested":{"session_id":"`+id+`"}`)) {
		t.Fatal("native identity not typed", err)
	}
	restored, err := b.Frame(normalized, true)
	var a, c any
	_ = json.Unmarshal(native, &a)
	_ = json.Unmarshal(restored, &c)
	if err != nil || !equalJSON(a, c) {
		t.Fatal("native metadata round trip changed", err)
	}
	if os.WriteFile(filepath.Join(b.workdir, "unexpected-input"), []byte("must not be ignored"), 0600) != nil {
		t.Fatal("seed")
	}
	if _, err := b.Files(); err == nil {
		t.Fatal("nonempty workdir accepted")
	}
}
func equalJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func TestRunnerArtifactsRejectLateChanges(t *testing.T) {
	for _, kind := range []string{"complete", "unexpected", "duplicate-claim", "extra-role", "changed-tape", "failed-receipt", "public-claim", "missing-seal"} {
		t.Run(kind, func(t *testing.T) {
			suite, bindings := runnerControl(t)
			home := bindings["inherited"].home
			meta := filepath.Join(home, RunnerMetadataDirectory)
			if os.Mkdir(meta, 0700) != nil {
				t.Fatal("metadata")
			}
			for name, mode := range map[string]os.FileMode{"config.json": 0600, "claude": 0700} {
				if os.WriteFile(filepath.Join(meta, name), nil, mode) != nil {
					t.Fatal("control")
				}
			}
			for role, tape := range suite.Roles {
				prefix := filepath.Join(meta, role)
				if err := tape.Save(prefix + ".json"); err != nil {
					t.Fatal(err)
				}
				for _, suffix := range []string{".claim", ".finish"} {
					if os.WriteFile(prefix+suffix, nil, 0600) != nil {
						t.Fatal("ownership")
					}
				}
				raw, _ := json.Marshal(struct {
					Success bool `json:"success"`
					Frames  int  `json:"frames"`
				}{true, len(tape.Frames)})
				if os.WriteFile(prefix+".receipt", raw, 0600) != nil {
					t.Fatal("receipt")
				}
				digest, err := AuditRunnerRole(home, suite.Scenario, role)
				if err != nil {
					t.Fatal(err)
				}
				if os.WriteFile(prefix+".seal", []byte(digest), 0600) != nil {
					t.Fatal("seal")
				}
			}
			prefix := filepath.Join(meta, "inherited")
			switch kind {
			case "unexpected":
				_ = os.WriteFile(filepath.Join(meta, "unexpected"), nil, 0600)
			case "duplicate-claim":
				_ = os.WriteFile(prefix+".claim.duplicate", nil, 0600)
			case "extra-role":
				_ = os.WriteFile(filepath.Join(meta, "extra.json"), []byte(`{}`), 0600)
			case "changed-tape":
				raw, err := os.ReadFile(prefix + ".json")
				if err != nil {
					t.Fatal(err)
				}
				raw = bytes.Replace(raw, []byte("resolved-opus"), []byte("changed-opus"), 1)
				_ = os.WriteFile(prefix+".json", raw, 0600)
			case "failed-receipt":
				_ = os.WriteFile(prefix+".receipt", []byte(`{"success":false,"frames":2}`), 0600)
			case "public-claim":
				_ = os.Chmod(prefix+".claim", 0644)
			case "missing-seal":
				if os.Remove(prefix+".seal") != nil {
					t.Fatal("remove own synthetic seal")
				}
			}
			roles, err := AuditRunnerSuiteArtifacts(home, suite.Scenario)
			if kind == "complete" {
				if err != nil || len(roles) != 2 {
					t.Fatal("complete artifact proof rejected", err)
				}
			} else if err == nil {
				t.Fatal("late artifact mutation accepted")
			}
		})
	}
}
