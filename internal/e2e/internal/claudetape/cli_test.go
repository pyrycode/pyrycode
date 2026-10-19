package claudetape

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func cliControl() *CLITape {
	const session = "11111111-1111-4111-8111-111111111111"
	return &CLITape{Version: 1, Scenario: "cli-prompt-unicode", Producer: suiteControl().Producer, DaemonSHA256: strings.Repeat("c", 64), Args: []string{"--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--permission-mode", "dontAsk", "--settings", "${CLI_FILE:--settings}", "--append-system-prompt-file", "${CLI_FILE:--append-system-prompt-file}", "--model", "claude-haiku-4-5", "--effort", "low", "--max-turns", "1", "--allowed-tools", "Read"}, Files: map[string]json.RawMessage{"--settings": json.RawMessage(`{"content":"{}","mode":384}`), "--append-system-prompt-file": json.RawMessage(`{"content":"original system","mode":384}`), "$cwd": json.RawMessage(`"."`)}, Input: `{"type":"user","message":{"role":"user","content":"ä ö å — 🐍"}}` + "\n", InputEOF: true, Frames: []CLIFrame{{Stream: "stdout", Data: `{"type":"system","subtype":"init","session_id":"` + session + `","apiKeySource":"ANTHROPIC_API_KEY"}` + "\n"}, {Stream: "stdout", Data: `{"type":"result","result":"ok"}` + "\n"}}, ExitCode: 0, SessionID: session, Snapshot: CLISnapshot{Exists: true, Mode: 0600, Data: `{"type":"user","message":{"content":"ä ö å — 🐍 literal-escape:\u00e4"}}` + "\n"}}
}
func TestCLIByteFidelityAndContract(t *testing.T) {
	a := cliControl()
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(a)
	b, err := DecodeCLI(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b.Input != a.Input || b.Snapshot.Data != a.Snapshot.Data || !strings.Contains(b.Snapshot.Data, `\u00e4`) {
		t.Fatal("CLI round trip rewrote transcript or Unicode encoding")
	}
	if SameCLIContract(a, b) != nil {
		t.Fatal("valid CLI contract rejected")
	}
	b.Frames[1].Data = `{"type":"result","result":"different provider wording"}` + "\n"
	b.Snapshot.Data = `{"type":"assistant","message":{"content":"different wording"}}` + "\n"
	if SameCLIContract(a, b) != nil {
		t.Fatal("provider wording became outgoing contract")
	}
	b.Input = `{"type":"user","message":{"role":"user","content":"changed"},"unknown":true}`
	if SameCLIContract(a, b) == nil {
		t.Fatal("changed prompt or unknown input field accepted")
	}
	path := t.TempDir() + "/cli.json"
	if a.Save(path) != nil {
		t.Fatal("save failed")
	}
	if a.Save(path) == nil {
		t.Fatal("overwrote CLI candidate")
	}
	for _, kind := range []string{"eof", "argv", "exit", "snapshot", "unicode", "unknown-field", "duplicate-field"} {
		t.Run(kind, func(t *testing.T) {
			var changed CLITape
			_ = json.Unmarshal(raw, &changed)
			switch kind {
			case "eof":
				changed.InputEOF = false
			case "argv":
				changed.Args = append(changed.Args, "--unreviewed")
			case "exit":
				changed.ExitCode = -1
			case "snapshot":
				changed.Snapshot.Exists = false
			case "unicode":
				changed.Snapshot.Data = string([]byte{0xff})
			}
			candidate, _ := json.Marshal(&changed)
			if kind == "unicode" {
				if changed.Validate() == nil {
					t.Fatal("invalid UTF8 accepted")
				}
				return
			}
			if kind == "unknown-field" {
				candidate = append([]byte(`{"unknown":true,`), candidate[1:]...)
			}
			if kind == "duplicate-field" {
				candidate = append([]byte(`{"version":1,`), candidate[1:]...)
			}
			if _, err := DecodeCLI(bytes.NewReader(candidate)); err == nil {
				t.Fatal("invalid CLI proof accepted")
			}
		})
	}
}
func TestCLIPrivacyCarriers(t *testing.T) {
	const opaque = "opaque-cli-credential-control"
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", opaque)
	for _, kind := range []string{"metadata", "args", "input", "stdout", "stderr", "snapshot", "files", "escaped-secret"} {
		t.Run(kind, func(t *testing.T) {
			c := cliControl()
			switch kind {
			case "metadata":
				c.Producer.ClaudeVersion = opaque
			case "args":
				c.Args[12] = opaque
			case "input":
				c.Input = `{"type":"user","message":{"content":"` + opaque + `"}}`
			case "stdout":
				c.Frames[1].Data = `{"type":"result","result":"` + opaque + `"}`
			case "stderr":
				c.Frames = append(c.Frames, CLIFrame{Stream: "stderr", Data: opaque})
			case "snapshot":
				c.Snapshot.Data = `{"type":"user","message":{"content":"` + opaque + `"}}` + "\n"
			case "files":
				c.Files["unknown"] = json.RawMessage(`{"value":"` + opaque + `"}`)
			case "escaped-secret":
				c.Frames[1].Data = `{"type":"result","result":"\u0073k-ant-hidden"}`
			}
			if c.Validate() == nil {
				t.Fatal("private data carrier accepted")
			}
		})
	}
}

// Repeated keys must not hide an earlier escaped credential from a map-based
// audit while the byte-preserving recording still stores it. All material below
// is a synthetic credential-shaped marker, never an account credential.
func TestCLIDuplicateJSONPrivacyCarriers(t *testing.T) {
	hidden := `{"shadow":"\u0073k-ant-synthetic-credential-1234567890","shadow":"clean"}`
	add := func(raw string) string {
		return strings.TrimSuffix(strings.TrimSpace(raw), "}") + `,"shadow":"\u0073k-ant-synthetic-credential-1234567890","shadow":"clean"}` + "\n"
	}
	for _, carrier := range []string{"stdout", "nested-stdout", "snapshot", "input", "file-content", "metadata"} {
		t.Run(carrier, func(t *testing.T) {
			tape := cliControl()
			switch carrier {
			case "stdout":
				tape.Frames[0].Data = add(tape.Frames[0].Data)
			case "nested-stdout":
				encoded, _ := json.Marshal(hidden)
				tape.Frames[0].Data = strings.TrimSuffix(strings.TrimSpace(tape.Frames[0].Data), "}") + `,"nested":` + string(encoded) + "}\n"
			case "snapshot":
				tape.Snapshot.Data = add(tape.Snapshot.Data)
			case "input":
				tape.Input = add(tape.Input)
			case "file-content":
				raw, _ := json.Marshal(struct {
					Content string `json:"content"`
					Mode    uint32 `json:"mode"`
				}{hidden, 0600})
				tape.Files["--settings"] = raw
			case "metadata":
				tape.Producer.ClaudeVersion = hidden
			}
			if tape.Validate() == nil {
				t.Fatal("duplicate-key privacy carrier accepted")
			}
		})
	}
}
