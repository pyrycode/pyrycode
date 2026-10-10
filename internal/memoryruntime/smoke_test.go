//go:build memory_runtime_smoke

package memoryruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestSmoke is explicitly opted in with the memory_runtime_smoke build tag.
// PYRY_MEMORY_SMOKE_EVIDENCE selects where the executed JSON evidence is written.
func TestSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	home := t.TempDir()
	installed, err := Install(ctx, Options{Home: home, Mode: "openai", Progress: func(stage string) { t.Log("stage:", stage) }})
	if err != nil {
		if f, ok := err.(*Failure); ok {
			t.Fatalf("%s: %v", f.Stage, f.Err)
		}
		t.Fatal(err)
	}
	repeat, err := Install(ctx, Options{Home: home, Mode: "openai"})
	if err != nil || repeat.Launch != installed.Launch {
		t.Fatalf("repeat setup: %v", err)
	}
	dir := filepath.Dir(filepath.Dir(filepath.Dir(installed.Python)))
	database := filepath.Join(t.TempDir(), "smoke.db")
	script := `from pymilvus import MilvusClient
import sys
client=MilvusClient(sys.argv[1])
assert client.list_collections()==[]
client.close()
print('temporary_database_open_close: PASS')`
	out, err := run(ctx, dir, installed.Python, "-I", "-B", "-c", script, database)
	if err != nil {
		t.Fatal("Milvus Lite open/close:", err)
	}
	t.Log(string(out))
	release, err := os.ReadFile("/etc/os-release")
	if err != nil {
		t.Fatal(err)
	}
	libc, err := run(ctx, dir, "/usr/bin/getconf", "GNU_LIBC_VERSION")
	if err != nil {
		t.Fatal(err)
	}
	record := struct {
		CapturedAt   string            `json:"captured_at_utc"`
		OS           string            `json:"os"`
		Invocation   string            `json:"invocation"`
		Target       string            `json:"target"`
		OSRelease    string            `json:"os_release"`
		Architecture string            `json:"architecture"`
		Libc         string            `json:"libc"`
		Versions     map[string]string `json:"versions"`
		Checks       map[string]string `json:"checks"`
		Executed     int               `json:"executed"`
		Passed       int               `json:"passed"`
	}{
		CapturedAt: time.Now().UTC().Format(time.RFC3339), OS: runtime.GOOS,
		Invocation: "PYRY_MEMORY_SMOKE_EVIDENCE=" + os.Getenv("PYRY_MEMORY_SMOKE_EVIDENCE") + " go test -tags memory_runtime_smoke -run '^TestSmoke$' -count=1 -v ./internal/memoryruntime",
		Target:     "Ubuntu 24.04 amd64; minimum glibc 2.39",
		OSRelease:  string(release), Architecture: runtime.GOARCH, Libc: string(libc), Versions: installed.Versions,
		Checks: map[string]string{"installer": "PASS", "repeat_verified_setup": "PASS", "python_and_dependency_versions": "PASS", "memsearch_pymilvus_milvus_lite_openai_imports": "PASS", "memsearch_cli_version": "PASS", "pip_dependency_compatibility": "PASS", "temporary_database_open_close": "PASS"}, Executed: 7, Passed: 7,
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if destination := os.Getenv("PYRY_MEMORY_SMOKE_EVIDENCE"); destination != "" {
		if err := os.WriteFile(destination, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(data))
}
