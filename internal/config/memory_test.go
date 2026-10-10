package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func testMemorySettings() MemorySettings {
	return MemorySettings{
		Vault:           MemoryVault{Mode: "separate", Path: "/unresolved/vault"},
		AdditionalRoots: []string{"/unresolved/one", "/unresolved/two"},
		Embedding:       MemoryEmbedding{Provider: "openai", Model: "embed-model", CredentialReference: "opaque-reference"},
		Capture:         MemoryCapture{Agent: "codex", Model: "capture-model"},
	}
}

func TestLoadMemory(t *testing.T) {
	t.Parallel()
	configured := testMemorySettings()
	for _, tc := range []struct {
		name, body string
		want       *MemorySettings
	}{
		{"legacy", `{}`, nil},
		{"null", `{"memory":null}`, nil},
		{"default vault", `{"memory":{"vault":{"mode":"default"},"embedding":{"provider":"local","model":"embed"},"capture":{"agent":"claude","model":"capture"}}}`, &MemorySettings{Vault: MemoryVault{Mode: "default"}, Embedding: MemoryEmbedding{Provider: "local", Model: "embed"}, Capture: MemoryCapture{Agent: "claude", Model: "capture"}}},
		{"separate vault", `{"memory":{"vault":{"mode":"separate","path":"/unresolved/vault"},"additional_roots":["/unresolved/one","/unresolved/two"],"embedding":{"provider":"openai","model":"embed-model","credential_reference":"opaque-reference"},"capture":{"agent":"codex","model":"capture-model"}}}`, &configured},
		{"openai with claude", `{"memory":{"embedding":{"provider":"openai","model":"embed"},"capture":{"agent":"claude","model":"capture"}}}`, &MemorySettings{Embedding: MemoryEmbedding{Provider: "openai", Model: "embed"}, Capture: MemoryCapture{Agent: "claude", Model: "capture"}}},
		{"local with codex", `{"memory":{"embedding":{"provider":"local","model":"embed"},"capture":{"agent":"codex","model":"capture"}}}`, &MemorySettings{Embedding: MemoryEmbedding{Provider: "local", Model: "embed"}, Capture: MemoryCapture{Agent: "codex", Model: "capture"}}},
		{"parse only", `{"memory":{"vault":{"mode":"future","path":"relative"},"embedding":{"provider":"future","model":"","credential_reference":"unverified"},"capture":{"agent":"future","model":""}}}`, &MemorySettings{Vault: MemoryVault{Mode: "future", Path: "relative"}, Embedding: MemoryEmbedding{Provider: "future", CredentialReference: "unverified"}, Capture: MemoryCapture{Agent: "future"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.json")
			testWriteConfig(t, path, tc.body)
			got, err := Load(path)
			if err != nil || !reflect.DeepEqual(got.Memory, tc.want) || got.RelayURL != DefaultConfig().RelayURL {
				t.Fatalf("Load = %#v, %v; want memory %#v and defaults", got, err, tc.want)
			}
		})
	}
	if DefaultConfig().Memory != nil {
		t.Fatal("defaults enable memory")
	}
}

func TestUpdateMemoryReplacement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	providers := `[{"id":"search","agent":"claude","enabled":false}]`
	body := `{"relay_url":"custom","debug_capture":true,"memory_search_providers":` + providers + `,"unknown":{"n":900719925474099312345,"fraction":1.234567890123456789,"exponent":1e400,"other":[null,true,"x"]},"memory":{"future":[1,{"n":9007199254740993}],"vault":{"future":false,"mode":"old","path":"old"},"embedding":{"future":null,"provider":"old","model":"old","credential_reference":"old"},"capture":{"future":{"n":1e400},"agent":"old","model":"old"},"additional_roots":["old"]}}`
	testWriteConfig(t, path, body)
	original := testConfigObject(t, []byte(body))
	first := testMemorySettings()
	second := MemorySettings{Vault: MemoryVault{Mode: "default"}, Embedding: MemoryEmbedding{Provider: "local", Model: "local-model"}, Capture: MemoryCapture{Agent: "claude", Model: "different"}}
	third := MemorySettings{Vault: MemoryVault{Mode: "unsupported", Path: "relative"}, AdditionalRoots: []string{"replacement"}, Embedding: MemoryEmbedding{Provider: "unknown", CredentialReference: "unverified"}, Capture: MemoryCapture{Agent: "unknown"}}
	for _, settings := range []MemorySettings{first, second, third, {AdditionalRoots: []string{}}, {}} {
		if err := UpdateMemory(path, settings); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil || !reflect.DeepEqual(cfg.Memory, &settings) || len(cfg.MemorySearchProviders) != 1 {
			t.Fatalf("Load = %#v, %v; want %#v and independent provider", cfg, err, settings)
		}
		got := testConfigObject(t, testReadConfig(t, path))
		for _, key := range []string{"relay_url", "debug_capture", "memory_search_providers", "unknown"} {
			testJSONEqual(t, got[key], original[key])
		}
		memory := testConfigObject(t, got["memory"])
		oldMemory := testConfigObject(t, original["memory"])
		testJSONEqual(t, memory["future"], oldMemory["future"])
		for _, key := range []string{"vault", "embedding", "capture"} {
			object := testConfigObject(t, memory[key])
			oldObject := testConfigObject(t, oldMemory[key])
			testJSONEqual(t, object["future"], oldObject["future"])
		}
		vault := testConfigObject(t, memory["vault"])
		embedding := testConfigObject(t, memory["embedding"])
		for _, optional := range []struct {
			object  map[string]json.RawMessage
			key     string
			present bool
		}{
			{vault, "path", settings.Vault.Path != ""},
			{embedding, "credential_reference", settings.Embedding.CredentialReference != ""},
			{memory, "additional_roots", settings.AdditionalRoots != nil},
		} {
			if _, ok := optional.object[optional.key]; ok != optional.present {
				t.Fatalf("%s presence = %v, want %v", optional.key, ok, optional.present)
			}
		}
	}
}

func TestUpdateMemoryObjects(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"memory":null}`, `{"memory":{}}`, `{"memory":{"vault":null,"embedding":null,"capture":null}}`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.json")
			testWriteConfig(t, path, body)
			want := testMemorySettings()
			if err := UpdateMemory(path, want); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil || !reflect.DeepEqual(cfg.Memory, &want) {
				t.Fatalf("Load = %#v, %v", cfg, err)
			}
		})
	}
}

func TestUpdateMemoryRejectsStructure(t *testing.T) {
	t.Parallel()
	bodies := []string{"", " ", "{", `{} {}`, `null`, `[]`, `true`, `"text"`, `42`}
	for _, location := range []string{"memory", "vault", "embedding", "capture"} {
		for _, value := range []string{`[]`, `false`, `42`, `"text"`} {
			body := `{"memory":{` + `"` + location + `":` + value + `}}`
			if location == "memory" {
				body = `{"memory":` + value + `}`
			}
			bodies = append(bodies, body)
		}
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.json")
			testWriteConfig(t, path, body)
			if err := UpdateMemory(path, testMemorySettings()); err == nil {
				t.Fatal("accepted invalid structure")
			}
			if got := testReadConfig(t, path); string(got) != body {
				t.Fatalf("modified config: %s", got)
			}
			testNoMemoryStaging(t, filepath.Dir(path))
		})
	}
}

func TestUpdateMemoryFreshProcess(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	path := filepath.Join(base, "private", "nested", "config.json")
	if err := UpdateMemory(path, testMemorySettings()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{
		{path, 0o600}, {filepath.Dir(path), 0o700}, {filepath.Join(base, "private"), 0o700},
	} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != tc.mode {
			t.Fatalf("mode %s = %o, want %o", tc.path, info.Mode().Perm(), tc.mode)
		}
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMemory(path, testMemorySettings()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("replacement mode = %v, %v", info, err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryLoadHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "GO_TEST_MEMORY_CONFIG="+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh-process Load: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("--- PASS: TestMemoryLoadHelperProcess")) {
		t.Fatalf("fresh-process test did not execute: %s", out)
	}
	testNoMemoryStaging(t, filepath.Dir(path))
}

func TestMemoryLoadHelperProcess(t *testing.T) {
	path := os.Getenv("GO_TEST_MEMORY_CONFIG")
	if path == "" {
		return
	}
	want := DefaultConfig()
	settings := testMemorySettings()
	want.Memory = &settings
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %#v, %v; want %#v", got, err, want)
	}
}

type testMemoryTempFile struct {
	*os.File
	failure string
	err     error
}

func (f *testMemoryTempFile) Write(data []byte) (int, error) {
	if f.failure == "write" || f.failure == "short write" {
		n, err := f.File.Write(data[:len(data)/2])
		if err != nil {
			return n, err
		}
		if f.failure == "write" {
			return n, f.err
		}
		return n, nil
	}
	return f.File.Write(data)
}

func (f *testMemoryTempFile) Sync() error {
	if f.failure == "sync" {
		return f.err
	}
	return f.File.Sync()
}

func (f *testMemoryTempFile) Close() error {
	err := f.File.Close()
	if f.failure == "close" && err == nil {
		return f.err
	}
	return err
}

func (f *testMemoryTempFile) Chmod(mode os.FileMode) error {
	if f.failure == "chmod" {
		return f.err
	}
	return f.File.Chmod(mode)
}

func TestUpdateMemoryFailures(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"create", "chmod", "write", "short write", "sync", "close", "rename"} {
		for _, exists := range []bool{false, true} {
			t.Run(failure+"/"+map[bool]string{false: "missing", true: "existing"}[exists], func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				path := filepath.Join(dir, "config.json")
				before := `{"relay_url":"existing","memory":null}`
				if exists {
					testWriteConfig(t, path, before)
				}
				sentinel := errors.New("injected " + failure)
				ops := memoryWriteOps{
					createTemp: func(dir, pattern string) (memoryTempFile, error) {
						if failure == "create" {
							return nil, sentinel
						}
						f, err := os.CreateTemp(dir, pattern)
						if err != nil {
							return nil, err
						}
						if dir != filepath.Dir(path) {
							t.Fatalf("staged in %s", dir)
						}
						return &testMemoryTempFile{File: f, failure: failure, err: sentinel}, nil
					},
					rename: func(from, to string) error {
						if failure == "rename" {
							return sentinel
						}
						return os.Rename(from, to)
					},
				}
				err := updateMemory(path, testMemorySettings(), ops)
				wantErr := sentinel
				if failure == "short write" {
					wantErr = io.ErrShortWrite
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("error = %v, want %v", err, wantErr)
				}
				if exists {
					if got := testReadConfig(t, path); string(got) != before {
						t.Fatalf("changed previous bytes: %s", got)
					}
				} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing config created: %v", err)
				}
				testNoMemoryStaging(t, dir)
				if err := UpdateMemory(path, testMemorySettings()); err != nil {
					t.Fatalf("retry: %v", err)
				}
			})
		}
	}
}

func TestUpdateMemoryReadAndDirectoryErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := UpdateMemory(dir, testMemorySettings()); err == nil {
		t.Fatal("accepted directory as config")
	}
	path := filepath.Join(dir, "file")
	testWriteConfig(t, path, "unchanged")
	if err := UpdateMemory(filepath.Join(path, "config.json"), testMemorySettings()); err == nil {
		t.Fatal("accepted file as parent")
	}
	if got := testReadConfig(t, path); string(got) != "unchanged" {
		t.Fatalf("changed parent: %s", got)
	}
	testNoMemoryStaging(t, dir)
}

func testWriteConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testReadConfig(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testConfigObject(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		t.Fatalf("object %s: %v", data, err)
	}
	return object
}

func testJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var compactGot, compactWant bytes.Buffer
	if err := json.Compact(&compactGot, got); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&compactWant, want); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compactGot.Bytes(), compactWant.Bytes()) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

func testNoMemoryStaging(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".memory-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("staging files = %v, %v", matches, err)
	}
}
