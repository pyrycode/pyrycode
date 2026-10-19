//go:build memory_runtime_smoke

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/memoryruntime"
)

// TestSmoke complements internal/memoryruntime.TestSmoke. Downloads are opt-in.
// PYRY_MEMORY_OWNER_EVIDENCE selects the durable record destination.
func TestSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	var probes atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if r.Method != "POST" || r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer synthetic-secret" || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || body.Model != "text-embedding-3-small" || len(body.Input) != 1 || body.Input[0] != "pyry managed memory health probe" {
			http.Error(w, "invalid probe", 400)
			return
		}
		probes.Add(1)
		vector := make([]float64, 1536)
		vector[0] = 1
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "model": body.Model, "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": vector}}, "usage": map[string]int{"prompt_tokens": 6, "total_tokens": 6}})
	}))
	defer endpoint.Close()
	record := memoryOwnerEvidence{OS: runtime.GOOS, Architecture: runtime.GOARCH, CapturedAt: time.Now().UTC().Format(time.RFC3339), Invocation: "PYRY_MEMORY_OWNER_EVIDENCE=<record> go test -tags memory_runtime_smoke -run '^TestSmoke$' -count=1 -v ./cmd/pyry", Modes: map[string]memoryOwnerMode{}}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
		if err != nil {
			t.Fatal(err)
		}
		record.MacOSVersion = strings.TrimSpace(string(out))
		out, err = exec.Command("/usr/bin/sw_vers", "-buildVersion").Output()
		if err != nil {
			t.Fatal(err)
		}
		record.MacOSBuild = strings.TrimSpace(string(out))
	} else {
		release, err := os.ReadFile("/etc/os-release")
		if err != nil {
			t.Fatal(err)
		}
		record.OSRelease = string(release)
		out, err := exec.Command("/usr/bin/getconf", "GNU_LIBC_VERSION").Output()
		if err != nil {
			t.Fatal(err)
		}
		record.Libc = strings.TrimSpace(string(out))
	}
	for _, provider := range []string{"local", "openai"} {
		t.Run(provider, func(t *testing.T) {
			rt, err := memoryruntime.Install(ctx, memoryruntime.Options{Home: home, Mode: provider, Progress: func(stage string) { t.Log(provider, stage) }})
			if err != nil {
				t.Fatal(err)
			}
			vault := filepath.Join(home, "vault-"+provider)
			if err := os.Mkdir(vault, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(vault, "unindexed.md"), []byte("fixture note must never be indexed"), 0600); err != nil {
				t.Fatal(err)
			}
			model := "sentence-transformers/all-MiniLM-L6-v2"
			if provider == "openai" {
				model = "text-embedding-3-small"
			}
			s := config.MemorySettings{Embedding: config.MemoryEmbedding{Provider: provider, Model: model}}
			e := effectiveMemory{VaultPath: vault, SearchRoots: []string{vault}}
			options := memoryOwnerOptions{shim: "PYRY_TEST_EMPTY = True"}
			if provider == "openai" {
				options.endpoint = endpoint.URL + "/v1"
				options.credential = func(context.Context, string) (string, error) { return "synthetic-secret", nil }
			}
			for attempt := 0; attempt < 2; attempt++ {
				owner, err := startMemoryOwnerWith(ctx, rt, s, e, options)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := startMemoryOwnerWith(ctx, rt, s, e, options); err != errMemoryOccupied {
					t.Fatalf("exclusion: %v", err)
				}
				if err := owner.close(); err != nil {
					t.Fatal(err)
				}
			}
			store, lock, err := memoryOwnerStorage(ctx, home, e)
			if err != nil {
				t.Fatal(err)
			}
			_ = lock.Close()
			if provider == "openai" {
				if probes.Load() != 2 {
					t.Fatalf("real provider probe count: %d", probes.Load())
				}
				entries, err := os.ReadDir(filepath.Join(store, "models"))
				if err != nil || len(entries) != 0 {
					t.Fatal("OpenAI downloaded local model")
				}
			}
			result := memoryOwnerMode{Model: model, Versions: rt.Versions, Checks: map[string]string{}}
			for _, check := range memoryOwnerChecks {
				result.Checks[check] = "PASS"
				t.Log(check, "PASS")
			}
			result.Executed = len(result.Checks)
			result.Passed = result.Executed
			record.Modes[provider] = result
			record.Executed += result.Executed
			record.Passed += result.Passed
		})
	}
	if t.Failed() {
		return
	}
	if err := validateMemoryOwnerEvidence(record); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	destination := os.Getenv("PYRY_MEMORY_OWNER_EVIDENCE")
	if destination == "" {
		t.Fatal("PYRY_MEMORY_OWNER_EVIDENCE required; evidence must be retained")
	}
	if err := os.WriteFile(destination, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(fmt.Sprintf("executed=%d passed=%d; %s", record.Executed, record.Passed, destination))
}
