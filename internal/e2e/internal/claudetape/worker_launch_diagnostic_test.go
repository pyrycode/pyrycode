package claudetape

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFallbackLaunchDiagnosticRegistration(t *testing.T) {
	for _, kind := range []string{"absent", "registered", "partial", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			if os.Chmod(home, 0700) != nil || os.Mkdir(filepath.Join(home, "recording-workers"), 0700) != nil {
				t.Fatal("worker directory")
			}
			path := FallbackWorkerJobPath(home, 1)
			switch kind {
			case "registered":
				if PublishLifecycleControl(path, FallbackWorkerJob{OwnerPID: 42, Args: []string{"private-input-must-not-leak"}}) != nil {
					t.Fatal("job")
				}
			case "partial":
				if os.WriteFile(path, []byte("{"), 0600) != nil {
					t.Fatal("partial job")
				}
			case "symlink":
				target := filepath.Join(home, "foreign")
				if PublishLifecycleControl(target, FallbackWorkerJob{OwnerPID: 42}) != nil || os.Symlink(target, path) != nil {
					t.Fatal("symlink job")
				}
			}
			done := make(chan struct{})
			close(done)
			audit := &FallbackLaunchAudit{home: home, scenario: "composer-stop", stop: make(chan struct{}), done: done}
			evidence := t.TempDir()
			if os.Chmod(evidence, 0700) != nil {
				t.Fatal("private evidence directory")
			}
			destination := filepath.Join(evidence, "audit.json")
			if err := audit.Save(destination); err != nil {
				t.Fatal(err)
			}
			var saved struct {
				RegistrationsKnown bool
				RegisteredOwners   []int
				WaitsKnown         bool
			}
			raw, err := runnerPrivateRead(destination, 16384)
			if err != nil || json.Unmarshal(raw, &saved) != nil {
				t.Fatal("diagnostic")
			}
			want := []int{}
			if kind == "registered" {
				want = []int{42}
			}
			if saved.RegistrationsKnown != (kind == "absent" || kind == "registered") || !reflect.DeepEqual(saved.RegisteredOwners, want) || saved.WaitsKnown {
				t.Fatalf("unknown and absent conflated: %+v", saved)
			}
			info, statErr := os.Stat(destination)
			if err != nil || statErr != nil || info.Mode().Perm() != 0600 || strings.Contains(string(raw), "private-input-must-not-leak") {
				t.Fatal("diagnostic exposed job content or permissions")
			}
			if audit.Save(destination) == nil {
				t.Fatal("overwrote retained evidence")
			}
		})
	}
}
