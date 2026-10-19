package claudetape

import (
	"bytes"
	"io"
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

func TestSessionSnapshotRejectsDecodedReservedMarkersAndExpansion(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, tail := range []string{
		`"cwd":"\u0024{LIFECYCLE_HOME}/work"`,
		`"message":"\u0024{LIFECYCLE_SESSION}"`,
		`"nested":{"\u0024{LIFECYCLE_HOME}":true}`,
	} {
		raw := []byte(`{"sessionId":"` + id + `",` + tail + `}`)
		if _, err := bindSessionJSONL(raw, id, "/h", false); err == nil {
			t.Fatal("decoded native marker accepted")
		}
	}
	raw := []byte(`{"sessionId":"` + id + `","message":"` + strings.Repeat("/h", MaxBytes/4) + `"}`)
	if _, err := bindSessionJSONL(raw, id, "/h", false); err == nil {
		t.Fatal("expanded capture exceeded bound")
	}
	raw = []byte(`{"sessionId":"` + snapshotSession + `","message":"` + strings.Repeat(snapshotHome, MaxBytes/(2*len(snapshotHome))) + `"}`)
	if _, err := bindSessionJSONL(raw, id, "/"+strings.Repeat("x", 512), true); err == nil {
		t.Fatal("expanded restore exceeded bound")
	}
	raw = []byte(`{"sessionId":"` + id + `","a":"` + strings.Repeat("/h", MaxBytes/32) + `","b":"` + strings.Repeat("/h", MaxBytes/32) + `"}`)
	if _, err := bindSessionJSONL(raw, id, "/h", false); err == nil {
		t.Fatal("aggregate expanded JSONL exceeded bound")
	}
}

func TestSessionSnapshotReadRechecksPrivateMode(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "native.jsonl")
	if os.WriteFile(path, []byte("private native bytes"), 0600) != nil {
		t.Fatal("write")
	}
	_, err := readStableSessionFile(path, home, func(r io.Reader) ([]byte, error) {
		raw, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		if err = os.Chmod(path, 0644); err != nil {
			return nil, err
		}
		return raw, nil
	})
	if err == nil {
		t.Fatal("file made public during read was accepted")
	}
}

func TestSessionSnapshotDirectorySwapCannotReadOrPublishForeignFile(t *testing.T) {
	for _, operation := range []string{"anchored-leaf", "read", "install"} {
		t.Run(operation, func(t *testing.T) {
			home, foreign := t.TempDir(), t.TempDir()
			parent := filepath.Join(home, ".claude", "projects", "owned")
			if os.MkdirAll(parent, 0700) != nil || os.Chmod(parent, 0700) != nil {
				t.Fatal("mkdir")
			}
			leaf := "11111111-1111-4111-8111-111111111111.jsonl"
			path := filepath.Join(parent, leaf)
			ownRaw, foreignRaw := []byte("owned native evidence"), []byte("foreign private evidence")
			if operation != "install" {
				if os.WriteFile(path, ownRaw, 0600) != nil {
					t.Fatal("write own")
				}
				if os.WriteFile(filepath.Join(foreign, leaf), foreignRaw, 0600) != nil {
					t.Fatal("write foreign")
				}
			}
			old := parent + "-moved"
			swap := func() {
				t.Helper()
				if os.Rename(parent, old) != nil || os.Symlink(foreign, parent) != nil {
					t.Fatal("swap")
				}
			}
			switch operation {
			case "anchored-leaf":
				dir, name, err := openSnapshotDirectory(path, home)
				if err != nil {
					t.Fatal(err)
				}
				defer dir.Close()
				swap()
				f, err := openSnapshotLeaf(dir, name)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				got, err := io.ReadAll(f)
				if err != nil || !bytes.Equal(got, ownRaw) {
					t.Fatal("anchored read followed foreign parent")
				}
				if snapshotDirectoryStillCurrent(dir, path, home) {
					t.Fatal("rebound directory accepted")
				}
			case "read":
				_, err := readStableSessionFile(path, home, func(r io.Reader) ([]byte, error) { swap(); return io.ReadAll(r) })
				if err == nil {
					t.Fatal("parent rebound during read accepted")
				}
			case "install":
				err := publishSnapshotAt(path, home, ownRaw, func(w io.Writer, p []byte) (int, error) { n, err := w.Write(p); swap(); return n, err })
				if err == nil {
					t.Fatal("parent rebound during publication accepted")
				}
				if _, err := os.Lstat(filepath.Join(foreign, leaf)); !os.IsNotExist(err) {
					t.Fatal("snapshot published into foreign directory")
				}
				if _, err := os.Lstat(filepath.Join(foreign, leaf+".pending")); !os.IsNotExist(err) {
					t.Fatal("pending snapshot published into foreign directory")
				}
			}
		})
	}
}
