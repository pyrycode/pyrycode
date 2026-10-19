package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDaemonRoleBindings(t *testing.T) {
	makeBinding := func(id string) (*DaemonBinding, map[string]json.RawMessage) {
		t.Helper()
		home := t.TempDir()
		work := filepath.Join(home, "work")
		if os.Mkdir(work, 0700) != nil {
			t.Fatal("mkdir")
		}
		daemon, socket := filepath.Join(home, "pyry"), filepath.Join(home, "socket")
		files := map[string]string{"--settings": `{"enableAllProjectMcpServers":true}`, "--append-system-prompt-file": "entire prompt", "--mcp-config": `{"mcpServers":{"test":{"command":"` + daemon + `","args":["mcp-files","-pyry-socket","` + socket + `"]}}}`}
		args := []string{"--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--session-id", id, "--model", "haiku", "--permission-prompt-tool", "mcp__pyry_approve__approve", "--strict-mcp-config"}
		for _, flag := range []string{"--settings", "--append-system-prompt-file", "--mcp-config"} {
			path := filepath.Join(home, flag[2:]+"-"+id)
			if os.WriteFile(path, []byte(files[flag]), 0600) != nil {
				t.Fatal("input")
			}
			args = append(args, flag, path)
		}
		b, err := NewDaemonBinding(args, home, work, daemon, socket)
		if err != nil {
			t.Fatal(err)
		}
		inventory, err := b.Files()
		if err != nil {
			t.Fatal(err)
		}
		return b, inventory
	}
	const first = "11111111-1111-4111-8111-111111111111"
	const second = "22222222-2222-4222-8222-222222222222"
	a, filesA := makeBinding(first)
	b, filesB := makeBinding(second)
	tape := &Tape{Files: filesA}
	if tape.CheckFiles(filesB) != nil {
		t.Fatal("typed path handles lost full input equivalence")
	}
	left, _ := json.Marshal(a.Args())
	right, _ := json.Marshal(b.Args())
	if !bytes.Equal(left, right) {
		t.Fatal("typed argv identities differ")
	}
	raw := []byte(`{"type":"system","subtype":"init","session_id":"` + first + `","cwd":"` + a.workdir + `","unknown":true}`)
	normalized, err := a.Frame(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := b.Frame(normalized, true)
	if err != nil || !bytes.Contains(restored, []byte(second)) || !bytes.Contains(restored, []byte(b.workdir)) || !bytes.Contains(restored, []byte(`"unknown":true`)) {
		t.Fatal("typed metadata restoration failed")
	}
	prose := []byte(`{"type":"user","message":{"content":"` + first + `"}}`)
	got, err := a.Frame(prose, false)
	if err != nil || !bytes.Contains(got, []byte(first)) {
		t.Fatal("session prose rebound")
	}
	if _, err := b.Frame(raw, false); err == nil {
		t.Fatal("foreign native session accepted")
	}
	for _, args := range [][]string{{"--session-id", first, "--resume", first}, {"--session-id", first, "--unknown-file", "/tmp/private"}, {"--session-id", first, "--session-id", second}} {
		if _, err := NewDaemonBinding(args, a.home, a.workdir, a.daemon, a.socket); err == nil {
			t.Fatal("unreviewed argv accepted")
		}
	}
	filesB["--settings"] = json.RawMessage(`{"content":{"enableAllProjectMcpServers":false},"mode":384}`)
	if tape.CheckFiles(filesB) == nil {
		t.Fatal("settings input change ignored")
	}
}
