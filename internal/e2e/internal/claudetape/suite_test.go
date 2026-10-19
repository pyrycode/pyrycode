package claudetape

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func suiteControl() *Suite {
	p := Producer{Revision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64), ClaudeVersion: "suite-control", CapturedAt: "2026-10-20T00:00:00Z"}
	s := &Suite{Version: 1, Scenario: "conversation-control", DaemonSHA256: strings.Repeat("c", 64), Producer: p, Roles: map[string]*Tape{}}
	for _, role := range []string{"bootstrap", "created-1"} {
		s.Roles[role] = &Tape{Version: 1, Scenario: s.Scenario + "-" + role, Producer: p, Args: []string{"--session-id", "${CHILD_SESSION}"}, Files: map[string]json.RawMessage{"--settings": json.RawMessage(`{}`)}, Frames: []Frame{{Direction: "stdin", Data: json.RawMessage(`{"type":"user","message":{"content":"` + role + `"}}`)}, {Direction: "stdout", Data: json.RawMessage(`{"type":"result","result":"ok"}`)}}}
	}
	return s
}
func TestSuiteClosedRoles(t *testing.T) {
	roles := []string{"bootstrap", "created-1"}
	s := suiteControl()
	raw, _ := json.Marshal(s)
	got, err := DecodeSuite(bytes.NewReader(raw), roles)
	if err != nil {
		t.Fatal(err)
	}
	if SameSuiteContract(s, got, roles) != nil {
		t.Fatal("valid role-preserving exchange rejected")
	}
	for _, kind := range []string{"missing", "extra", "renamed", "producer", "swapped-input", "unknown-field", "duplicate-field", "duplicate-role", "trailing", "aggregate-size"} {
		t.Run(kind, func(t *testing.T) {
			var changed Suite
			_ = json.Unmarshal(raw, &changed)
			switch kind {
			case "missing":
				delete(changed.Roles, "created-1")
			case "extra":
				changed.Roles["extra"] = changed.Roles["bootstrap"]
			case "renamed":
				changed.Roles["created-1"].Scenario = "conversation-control-bootstrap"
			case "producer":
				changed.Roles["created-1"].Producer.Revision = strings.Repeat("c", 40)
			case "swapped-input":
				changed.Roles["created-1"].Frames[0].Data = changed.Roles["bootstrap"].Frames[0].Data
			case "aggregate-size":
				changed.Roles["created-1"].Frames[1].Data = json.RawMessage(`{"type":"result","result":"` + strings.Repeat("x", MaxBytes) + `"}`)
			}
			candidate, _ := json.Marshal(&changed)
			if kind == "unknown-field" {
				candidate = append([]byte(`{"unknown":true,`), candidate[1:]...)
			}
			if kind == "duplicate-field" {
				candidate = append([]byte(`{"version":1,`), candidate[1:]...)
			}
			if kind == "duplicate-role" {
				candidate = bytes.Replace(candidate, []byte(`"roles":{`), []byte(`"roles":{"bootstrap":null,`), 1)
			}
			if kind == "trailing" {
				candidate = append(candidate, []byte(`{}`)...)
			}
			decoded, err := DecodeSuite(bytes.NewReader(candidate), roles)
			if err == nil && SameSuiteContract(s, decoded, roles) == nil {
				t.Fatal("invalid role contract accepted")
			}
		})
	}
	if s.Validate([]string{"bootstrap", "bootstrap"}) == nil {
		t.Fatal("duplicate declared role accepted")
	}
	path := t.TempDir() + "/suite.json"
	if s.Save(path, roles) != nil {
		t.Fatal("save failed")
	}
	if s.Save(path, roles) == nil {
		t.Fatal("overwrote reviewed suite")
	}
}
