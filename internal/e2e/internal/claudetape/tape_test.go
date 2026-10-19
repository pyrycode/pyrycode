package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rigTape() *Tape {
	return &Tape{Version: 1, Scenario: "rig", Producer: Producer{Revision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64), ClaudeVersion: "synthetic unit rig", CapturedAt: "2026-10-19T00:00:00Z"}, Args: []string{"--stream"}, Frames: []Frame{
		{"stdin", json.RawMessage(`{"type":"control_request","request_id":"old","request":{"subtype":"initialize"}}`)},
		{"stdout", json.RawMessage(`{"type":"control_response","response":{"request_id":"old","subtype":"success"},"text":"old","large":9007199254740993}`)},
		{"stdin", json.RawMessage(`{"type":"user","message":{"role":"user","content":"hello"}}`)},
		{"stdout", json.RawMessage(`{"type":"control_request","request_id":"permission","request":{"subtype":"can_use_tool","tool_name":"Write"}}`)},
		{"stdin", json.RawMessage(`{"type":"control_response","response":{"request_id":"permission","subtype":"success","response":{"behavior":"deny"}}}`)},
		{"stdout", json.RawMessage(`{"type":"result","subtype":"success"}`)},
	}}
}

func TestCausalReplay(t *testing.T) {
	p, err := New(rigTape(), []string{"--stream"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Prefix()) != 0 {
		t.Fatal("released output before daemon request")
	}
	out, err := p.Input([]byte(`{"request":{"subtype":"initialize"},"type":"control_request","request_id":"fresh"}`))
	if err != nil || len(out) != 1 {
		t.Fatalf("initialize: %v", err)
	}
	if !bytes.Contains(out[0], []byte(`"request_id":"fresh"`)) || !bytes.Contains(out[0], []byte(`"text":"old"`)) || !bytes.Contains(out[0], []byte(`9007199254740993`)) {
		t.Fatal("binding changed prose or numeric precision")
	}
	if p.Complete() == nil {
		t.Fatal("accepted missing turn and answer")
	}
	out, err = p.Input([]byte(`{"type":"user","message":{"role":"user","content":"hello"}}`))
	if err != nil || len(out) != 1 || bytes.Contains(out[0], []byte(`"result"`)) {
		t.Fatal("permission did not hold terminal reply")
	}
	out, err = p.Input([]byte(`{"type":"control_response","response":{"request_id":"permission","subtype":"success","response":{"behavior":"deny"}}}`))
	if err != nil || len(out) != 1 || p.Complete() != nil {
		t.Fatalf("completed exchange: %v", err)
	}
	if _, err = p.Input([]byte(`{"type":"user"}`)); err == nil || p.Complete() == nil {
		t.Fatal("accepted extra request after completion")
	}
}

func TestContractRejections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame int
		data  string
	}{
		{"changed initialize", 0, `{"type":"control_request","request_id":"new","request":{"subtype":"other"}}`},
		{"missing field", 0, `{"type":"control_request","request_id":"new"}`},
		{"extra field", 0, `{"type":"control_request","request_id":"new","request":{"subtype":"initialize"},"extra":false}`},
		{"changed user text", 2, `{"type":"user","message":{"role":"user","content":"bye"}}`},
		{"uncorrelated permission", 4, `{"type":"control_response","response":{"request_id":"wrong","subtype":"success","response":{"behavior":"deny"}}}`},
		{"changed choice", 4, `{"type":"control_response","response":{"request_id":"permission","subtype":"success","response":{"behavior":"allow"}}}`},
		{"trailing bytes", 0, `{"type":"control_request"} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tape := rigTape()
			p, err := New(tape, tape.Args)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tc.frame; i++ {
				if tape.Frames[i].Direction == "stdin" {
					if _, err = p.Input(tape.Frames[i].Data); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err = p.Input([]byte(tc.data)); err == nil {
				t.Fatal("accepted changed contract")
			}
			if len(p.Prefix()) != 0 || p.Complete() == nil {
				t.Fatal("failed player released output or certified completion")
			}
			if strings.Contains(err.Error(), tc.data) {
				t.Fatal("diagnostic exposed request bytes")
			}
		})
	}
	if _, err := New(rigTape(), []string{"--changed"}); err == nil {
		t.Fatal("accepted changed process arguments")
	}
}

func TestCandidateSafety(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.json")
	tape := rigTape()
	if err := tape.Save(path); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = tape.Save(path); err == nil {
		t.Fatal("overwrote reviewed candidate")
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(current, original) {
		t.Fatal("changed existing fixture")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("candidate permissions are not private")
	}
	decoded, err := Decode(bytes.NewReader(current))
	if err != nil || len(decoded.Frames) != 6 {
		t.Fatalf("round trip: %v", err)
	}
	for _, raw := range []string{string(current) + "{}", strings.Replace(string(current), `"version": 1`, `"version": 2`, 1), `{"version":1,"unknown":true}`, strings.Repeat("x", MaxBytes+1)} {
		if _, err = Decode(strings.NewReader(raw)); err == nil {
			t.Fatal("accepted malformed or oversized capture")
		}
	}
	for _, private := range []string{"sk-ant-private", "Bearer private", "/Users/operator/private", "/home/operator/private", `"access_token":"private"`} {
		if err = Safe([]byte(private)); err == nil || strings.Contains(err.Error(), private) {
			t.Fatal("private data accepted or logged")
		}
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "private-credential-sentinel")
	if Safe([]byte("private-credential-sentinel")) == nil {
		t.Fatal("configured credential accepted")
	}
}

func TestFreshContract(t *testing.T) {
	a, b := rigTape(), rigTape()
	for i := range b.Frames {
		b.Frames[i].Data = bytes.ReplaceAll(b.Frames[i].Data, []byte(`"old"`), []byte(`"fresh-daemon-id"`))
		b.Frames[i].Data = bytes.ReplaceAll(b.Frames[i].Data, []byte(`"permission"`), []byte(`"fresh-permission-id"`))
	}
	if err := SameContract(a, b); err != nil {
		t.Fatal(err)
	}
	b.Frames[4].Data = bytes.ReplaceAll(b.Frames[4].Data, []byte(`"deny"`), []byte(`"allow"`))
	if SameContract(a, b) == nil {
		t.Fatal("compatibility accepted a changed user choice")
	}
}

func TestCredentialSourceLabels(t *testing.T) {
	for _, raw := range []string{`{"tokenSource":"CLAUDE_CODE_OAUTH_TOKEN"}`, `{"apiKeySource": "ANTHROPIC_API_KEY"}`} {
		if err := Safe([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{"text":"CLAUDE_CODE_OAUTH_TOKEN"}`, `{"oauth_token":"secret"}`, `{"tokenSource":"sk-ant-secret"}`} {
		if Safe([]byte(raw)) == nil {
			t.Fatal("source label exception hid private data")
		}
	}
}

func TestInputFileRoundTrip(t *testing.T) {
	tape := rigTape()
	tape.Files = map[string]json.RawMessage{"settings": json.RawMessage(`{"effort":"low","extra":false}`), "system": json.RawMessage(`"Exact system prose."`)}
	raw, err := json.MarshalIndent(tape, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]json.RawMessage{"settings": json.RawMessage(`{ "extra": false, "effort": "low" }`), "system": json.RawMessage(`"Exact system prose."`)}
	if err = saved.CheckFiles(actual); err != nil {
		t.Fatal("serialized input-file formatting changed the contract")
	}
	actual["settings"] = json.RawMessage(`{"effort":"high","extra":false}`)
	if saved.CheckFiles(actual) == nil {
		t.Fatal("changed setting passed")
	}
	actual["settings"] = tape.Files["settings"]
	actual["system"] = json.RawMessage(`"Changed system prose."`)
	if saved.CheckFiles(actual) == nil {
		t.Fatal("changed prompt passed")
	}
}

func TestControlBindingPreservesPayloadIDs(t *testing.T) {
	tape := rigTape()
	tape.Frames[0].Data = json.RawMessage(`{"type":"control_request","request_id":"old","request":{"subtype":"initialize","data":{"request_id":"old"}}}`)
	tape.Frames[1].Data = json.RawMessage(`{"type":"control_response","response":{"request_id":"old","subtype":"success","response":{"request_id":"old"}}}`)
	p, err := New(tape, tape.Args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Input([]byte(`{"type":"control_request","request_id":"fresh","request":{"subtype":"initialize","data":{"request_id":"old"}}}`))
	if err != nil {
		t.Fatal("control binding changed application payload:", err)
	}
	if len(out) != 1 || !bytes.Contains(out[0], []byte(`"response":{"request_id":"old"}`)) {
		t.Fatal("control binding changed output payload")
	}
	fresh := rigTape()
	fresh.Frames[0].Data = json.RawMessage(`{"type":"control_request","request_id":"new","request":{"subtype":"initialize","data":{"request_id":"new"}}}`)
	if SameContract(tape, fresh) == nil {
		t.Fatal("compatibility ignored changed payload request_id")
	}
}
