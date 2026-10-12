//go:build memory_runtime_smoke

package memoryruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSmoke is explicitly opted in with the memory_runtime_smoke build tag.
// PYRY_MEMORY_SMOKE_EVIDENCE selects where the executed JSON evidence is written.
func TestSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]smokeMode{}
	installedModes := map[string]Runtime{}
	for _, mode := range []string{"openai", "local"} {
		t.Run(mode, func(t *testing.T) {
			installed, err := Install(ctx, Options{Home: home, Mode: mode, Progress: func(stage string) { t.Log("stage:", stage) }})
			if err != nil {
				if f, ok := err.(*Failure); ok {
					t.Fatalf("%s: %v", f.Stage, f.Err)
				}
				t.Fatal(err)
			}
			repeat, err := Install(ctx, Options{Home: home, Mode: mode})
			if err != nil || repeat.Launch != installed.Launch || repeat.Python != installed.Python || !reflect.DeepEqual(repeat.Versions, installed.Versions) {
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
			if err != nil || strings.TrimSpace(string(out)) != "temporary_database_open_close: PASS" {
				t.Fatal("Milvus Lite open/close:", err)
			}
			installedModes[mode] = installed
			checks := map[string]string{}
			for _, check := range smokeChecks(mode) {
				if check != "retained_launch_verified" {
					checks[check] = "PASS"
					t.Log(check + ": PASS")
				}
			}
			modes[mode] = smokeMode{Versions: installed.Versions, Checks: checks}
		})
	}
	if t.Failed() {
		return
	}
	for mode, installed := range installedModes {
		pins, err := selectPins(runtime.GOOS, runtime.GOARCH, mode)
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(filepath.Dir(filepath.Dir(installed.Python)))
		versions, err := probe(ctx, dir, pins)
		if err != nil || !reflect.DeepEqual(versions, installed.Versions) {
			t.Fatalf("retained %s runtime: %v", mode, err)
		}
		result := modes[mode]
		result.Checks["retained_launch_verified"] = "PASS"
		result.Executed, result.Passed = len(result.Checks), len(result.Checks)
		modes[mode] = result
		t.Log(mode + " retained_launch_verified: PASS")
	}
	dir := filepath.Dir(filepath.Dir(filepath.Dir(installedModes["local"].Python)))
	var release, libc, macVersion, macBuild []byte
	target := "Ubuntu 24.04 amd64; minimum glibc 2.39"
	if runtime.GOOS == "darwin" {
		target = "macOS arm64; minimum macOS 26.6.2"
		macVersion, err = run(ctx, dir, "/usr/bin/sw_vers", "-productVersion")
		if err == nil {
			macBuild, err = run(ctx, dir, "/usr/bin/sw_vers", "-buildVersion")
		}
	} else {
		release, err = os.ReadFile("/etc/os-release")
		if err == nil {
			libc, err = run(ctx, dir, "/usr/bin/getconf", "GNU_LIBC_VERSION")
		}
	}
	if err != nil {
		t.Fatal("host evidence:", err)
	}
	record := smokeEvidence{
		CapturedAt: time.Now().UTC().Format(time.RFC3339), OS: runtime.GOOS,
		Invocation: "PYRY_MEMORY_SMOKE_EVIDENCE=" + os.Getenv("PYRY_MEMORY_SMOKE_EVIDENCE") + " go test -tags memory_runtime_smoke -run '^TestSmoke$' -count=1 -v ./internal/memoryruntime",
		Target:     target, OSRelease: string(release), Architecture: runtime.GOARCH, Libc: string(libc),
		MacOSVersion: strings.TrimSpace(string(macVersion)), MacOSBuild: strings.TrimSpace(string(macBuild)), Modes: modes,
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
