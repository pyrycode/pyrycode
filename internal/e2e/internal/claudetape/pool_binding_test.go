package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPoolBindingIdentityIsOnlyTypedNativeMetadata(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "12340000-0000-4000-8000-000000000001"
	p, err := NewPoolBinding([]string{"--session-id", id, "--model", "haiku"}, home, work)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"type": "system", "subtype": "init", "session_id": id, "cwd": work, "message": map[string]any{"session_id": id, "text": id}})
	normalized, err := p.Frame(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(normalized, []byte(`"session_id":"${POOL_SESSION}"`)) || !bytes.Contains(normalized, []byte(`"text":"`+id+`"`)) {
		t.Fatal("typed session binding was lost or rewrote prose")
	}
	restored, err := p.Frame(normalized, true)
	if err != nil || !bytes.Equal(raw, restored) {
		t.Fatal("exact native metadata restoration failed")
	}
	request := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + id + `"}],"session_id":"` + id + `"}}`)
	got, err := p.Frame(request, false)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	json.Unmarshal(request, &a)
	json.Unmarshal(got, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("ordinary input changed")
	}
	changed, _ := object(raw)
	changed["session_id"] = "foreign"
	wrongID, _ := json.Marshal(changed)
	changed, _ = object(raw)
	changed["cwd"] = nil
	wrongCWD, _ := json.Marshal(changed)
	for _, change := range [][]byte{wrongID, wrongCWD} {
		if _, err := p.Frame(change, false); err == nil {
			t.Fatal("wrong native metadata accepted")
		}
	}

	args := p.Args()
	args[0] = "changed"
	if p.Args()[0] != "--session-id" {
		t.Fatal("returned args alias binding state")
	}
	for _, args := range [][]string{{"--session-id", id, "--session-id", id}, {"--session-id", "not-uuid"}, {"--session-id", id, "--resume", id}, {"--session-id", id, "--continue"}, {"--session-id", id, "--settings", "relative"}, {"--session-id", id, "--unknown-file", "/tmp/unaccounted"}, {"--session-id", id, "--settings"}} {
		if _, err := NewPoolBinding(args, home, work); err == nil {
			t.Fatal("ambiguous child identity/files accepted")
		}
	}
	if _, err := NewPoolBinding([]string{"--session-id", id}, home, filepath.Dir(home)); err == nil {
		t.Fatal("uncontrolled cwd accepted")
	}
}
func TestPoolBindingCompleteFileInventory(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	settings, prompt := filepath.Join(home, "settings.json"), filepath.Join(home, "system.txt")
	if err := os.WriteFile(settings, []byte(`{"unknown":{"exact":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prompt, []byte("literal\nUnicode: ä"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := NewPoolBinding([]string{"--session-id", "12340000-0000-4000-8000-000000000001", "--settings", settings, "--append-system-prompt-file", prompt}, home, work)
	if err != nil {
		t.Fatal(err)
	}
	files, err := p.Files()
	if err != nil || len(files) != 3 {
		t.Fatal("input inventory missing")
	}
	tape := &Tape{Files: files}
	if err := tape.CheckFiles(files); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings, 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := p.Files()
	if err != nil || tape.CheckFiles(changed) == nil {
		t.Fatal("input mode drift accepted")
	}
	if err := os.WriteFile(settings, []byte(`{} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Files(); err == nil {
		t.Fatal("trailing input JSON accepted")
	}
}
