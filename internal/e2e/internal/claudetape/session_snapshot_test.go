package claudetape

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionSnapshotPreservesUnboundBytesAndTypedIdentity(t *testing.T) {
	const a = "11111111-1111-4111-8111-111111111111"
	const b = "22222222-2222-4222-8222-222222222222"
	home := "/tmp/test-owned-snapshot"
	raw := []byte(" {\"type\":\"user\", \"sessionId\" : \"" + a + "\", \"cwd\":\"" + home + "/work/\\u00e4\",\"message\": {\"session_id\":\"" + a + "\",\"content\":\"literal " + a + " \\u00e4 ä\\n\"}}\r\n" +
		"{\"unknown\":true,\"session_id\":\"" + a + "\",\"number\":1.00e+02}\n")
	bound, e := bindSessionJSONL(raw, a, home, false)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(bound, []byte(`"session_id":"`+a+`","content"`)) || !bytes.Contains(bound, []byte(`literal `+a+` \u00e4 ä\n`)) || !bytes.Contains(bound, []byte(`/work/\u00e4`)) || !bytes.Contains(bound, []byte(`1.00e+02`)) {
		t.Fatal("unbound raw bytes rewritten")
	}
	restored, e := bindSessionJSONL(bound, a, home, true)
	if e != nil || !bytes.Equal(restored, raw) {
		t.Fatal("raw round trip changed bytes", e)
	}
	successor, e := bindSessionJSONL(bound, b, "/tmp/another-owned-home", true)
	if e != nil {
		t.Fatal(e)
	}
	expected := bytes.ReplaceAll(raw, []byte(`"sessionId" : "`+a+`"`), []byte(`"sessionId" : "`+b+`"`))
	expected = bytes.ReplaceAll(expected, []byte(`"session_id":"`+a+`","number"`), []byte(`"session_id":"`+b+`","number"`))
	expected = bytes.ReplaceAll(expected, []byte(home), []byte("/tmp/another-owned-home"))
	if !bytes.Equal(successor, expected) {
		t.Fatal("non-identity bytes changed during successor rebind")
	}
}
func TestSessionSnapshotRejectsAmbiguousOrForeignNativeData(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, raw := range []string{
		`{"sessionId":"foreign"}`,
		`{"sessionId":false,"session_id":"` + id + `"}`,
		`{"sessionId":{},"session_id":"` + id + `"}`,
		`{"sessionId":"` + id + `","sessionId":"` + id + `"}`,
		`{"sessionId":"` + id + `","message":{"x":1,"x":2}}`,
		`{"sessionId":"` + id + `","message":"${LIFECYCLE_SESSION}"}`,
		`{"nested":{"sessionId":"` + id + `"}}`,
		`{"sessionId":"` + id + `"} {}`,
		`{"sessionId":"` + id + `","secret":"\u0073k-ant-synthetic-credential-1234567890"}`,
		`{"sessionId":"` + id + `","cwd":"\/tmp\/snapshot\/work"}`,
	} {
		if _, e := bindSessionJSONL([]byte(raw), id, "/tmp/snapshot", false); e == nil {
			t.Fatal("invalid native JSONL accepted")
		}
	}
}
func TestSessionSnapshotStablePrivateFile(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	home := t.TempDir()
	path := filepath.Join(home, id+".jsonl")
	raw := []byte(`{"sessionId":"` + id + `","message":{"content":"native evidence"}}` + "\n")
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("write")
	}
	s, e := ReadSessionSnapshot(path, id, home)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Bytes(id, home)
	if e != nil || !bytes.Equal(got, raw) {
		t.Fatal("snapshot roundtrip", e)
	}
	s.Mode = 0644
	if _, e = s.Bytes(id, home); e == nil {
		t.Fatal("public snapshot accepted")
	}
	if os.Chmod(path, 0644) != nil {
		t.Fatal("chmod")
	}
	if _, e = ReadSessionSnapshot(path, id, home); e == nil {
		t.Fatal("public native file accepted")
	}
	if os.Chmod(path, 0600) != nil {
		t.Fatal("chmod")
	}
	link := filepath.Join(home, "alias.jsonl")
	if os.Symlink(path, link) != nil {
		t.Fatal("symlink")
	}
	if _, e = ReadSessionSnapshot(link, id, home); e == nil {
		t.Fatal("symlink native input accepted")
	}
	if _, e = ReadSessionSnapshot(path, id, filepath.Join(home, "foreign")); e == nil {
		t.Fatal("foreign HOME snapshot accepted")
	}
	if _, e = bindSessionJSONL([]byte(strings.Repeat("x", MaxBytes+1)), id, home, false); e == nil {
		t.Fatal("unbounded snapshot accepted")
	}
}

func TestSessionSnapshotExclusiveCausalInstall(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "owned")
	if os.MkdirAll(dir, 0700) != nil || os.Chmod(dir, 0700) != nil {
		t.Fatal("mkdir")
	}
	s := SessionSnapshot{Version: 1, Mode: 0600, Data: `{"sessionId":"${LIFECYCLE_SESSION}","cwd":"${LIFECYCLE_HOME}/work","message":{"content":"complete native bytes"}}` + "\n"}
	path := filepath.Join(dir, id+".jsonl")
	if err := InstallSessionSnapshot(s, path, id, home); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = InstallSessionSnapshot(s, path, id, home); err == nil {
		t.Fatal("existing transcript replaced")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("exclusive install changed old bytes")
	}
	if err = InstallSessionSnapshot(s, filepath.Join(dir, "foreign.jsonl"), id, home); err == nil {
		t.Fatal("foreign stem installed")
	}
	alias := filepath.Join(home, "alias")
	if os.Symlink(dir, alias) != nil {
		t.Fatal("symlink")
	}
	if _, err = ReadSessionSnapshot(filepath.Join(alias, id+".jsonl"), id, home); err == nil {
		t.Fatal("symlink directory input accepted")
	}
}
