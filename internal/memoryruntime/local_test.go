package memoryruntime

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalPins(t *testing.T) {
	for _, target := range []struct{ goos, arch, torch string }{{"linux", "amd64", "2.8.0+cpu"}, {"darwin", "arm64", "2.8.0"}} {
		t.Run(target.goos, func(t *testing.T) {
			base, _ := selectPins(target.goos, target.arch, "openai")
			local, err := selectPins(target.goos, target.arch, "local")
			if err != nil {
				t.Fatal(err)
			}
			if base.Local || !local.Local || len(base.Wheels) != 32 || len(local.Wheels) != 60 || local.Python != base.Python || local.Runtime != base.Runtime {
				t.Fatal("local closure changed base runtime or lacks dependencies")
			}
			pins := map[string]artifact{}
			for _, a := range local.Wheels {
				if _, exists := pins[a.Name]; exists {
					t.Fatalf("duplicate distribution: %s", a.Name)
				}
				pins[a.Name] = a
				digest, err := hex.DecodeString(a.SHA256)
				if err != nil || len(digest) != 32 || !strings.HasPrefix(a.URL, "https://") || !strings.HasSuffix(a.File, ".whl") {
					t.Fatalf("untrusted executable artifact: %s", a.Name)
				}
				if target.goos == "darwin" && (strings.Contains(a.File, "linux") || strings.Contains(a.File, "x86_64")) {
					t.Fatalf("wrong platform: %s", a.File)
				}
			}
			for _, a := range base.Wheels {
				if pins[a.Name] != a {
					t.Fatalf("base artifact changed: %s", a.Name)
				}
			}
			for name, version := range map[string]string{"einops": "0.8.2", "sentence-transformers": "5.1.1", "transformers": "4.57.1", "torch": target.torch} {
				if pins[name].Version != version {
					t.Fatalf("wrong local pin: %s", name)
				}
			}
		})
	}
}

func TestLocalLifecycleContracts(t *testing.T) {
	t.Setenv("MEMORY_RUNTIME_TEST_MODE", "local")
	for _, tc := range []struct {
		name string
		test func(*testing.T)
	}{
		{"RepeatAndReplacement", TestRepeatAndReplacement},
		{"ConcurrentAndCancelledWaiter", TestConcurrentAndCancelledWaiter},
		{"CrossProcessExclusion", TestCrossProcessExclusion},
		{"InterruptedRetry", TestInterruptedRetry},
		{"UnsafeDestinations", TestUnsafeDestinations},
		{"GenerationSymlinkRejected", TestGenerationSymlinkRejected},
		{"DownloadIntegrity", TestDownloadIntegrity},
		{"DependencyIntegrityBeforeExecution", TestDependencyIntegrityBeforeExecution},
		{"CompatibilityProbeCancellation", TestCompatibilityProbeCancellation},
	} {
		t.Run(tc.name, tc.test)
	}
}

func TestLocalSwitchAndReplacement(t *testing.T) {
	e, o, hits := testEngine(t)
	openai := testInstall(t, e, o)
	local := e
	local.pins.Target = "local-fixture"
	baseProvision, baseProbe := e.provision, e.probe
	local.provision = func(ctx context.Context, dir string, p lock) error {
		if err := baseProvision(ctx, dir, p); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "local-dependencies"), []byte("compatible"), 0600)
	}
	compatible := true
	local.probe = func(ctx context.Context, dir string, p lock) (map[string]string, error) {
		data, err := os.ReadFile(filepath.Join(dir, "local-dependencies"))
		if !compatible || err != nil || string(data) != "compatible" {
			compatible = true
			return nil, ErrProbe
		}
		return baseProbe(ctx, dir, p)
	}
	o.Mode = "local"
	first := testInstall(t, local, o)
	if first.Launch == openai.Launch || testInstall(t, local, o).Launch != first.Launch {
		t.Fatal("mode compatibility/reuse failed")
	}
	compatible = false
	second := testInstall(t, local, o)
	if second.Launch == first.Launch {
		t.Fatal("fresh incompatible probe was reused")
	}
	dir := filepath.Dir(filepath.Dir(filepath.Dir(second.Python)))
	if err := os.Remove(filepath.Join(dir, "local-dependencies")); err != nil {
		t.Fatal(err)
	}
	third := testInstall(t, local, o)
	if third.Launch == second.Launch {
		t.Fatal("missing local dependency reused")
	}
	o.Mode = "openai"
	back := testInstall(t, e, o)
	if back.Launch == third.Launch || testInstall(t, e, o).Launch != back.Launch || hits.Load() != 1 {
		t.Fatal("switch back failed to verify/reuse cached artifacts")
	}
	for _, r := range []Runtime{openai, first, second, third, back} {
		if b, err := os.ReadFile(r.Launch); err != nil || string(b) != "verified" {
			t.Fatal("earlier launch changed")
		}
	}
}

func TestLocalFailurePreservesOpenAI(t *testing.T) {
	for _, stage := range []string{"install", "probe", "cancel", "deadline"} {
		t.Run(stage, func(t *testing.T) {
			e, o, hits := testEngine(t)
			old := testInstall(t, e, o)
			publication := filepath.Join(o.Home, managed, "current.json")
			before, err := os.ReadFile(publication)
			if err != nil {
				t.Fatal(err)
			}
			local := e
			local.pins.Target = "local-fixture"
			o.Mode = "local"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := ErrProbe
			switch stage {
			case "install":
				local.provision = func(context.Context, string, lock) error { return ErrProbe }
			case "probe":
				local.probe = func(context.Context, string, lock) (map[string]string, error) { return nil, ErrProbe }
			case "cancel":
				want = context.Canceled
				local.provision = func(ctx context.Context, _ string, _ lock) error { cancel(); return ctx.Err() }
			case "deadline":
				want = context.DeadlineExceeded
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			r, err := local.install(ctx, o)
			if !errors.Is(err, want) || r.Launch != "" || r.Python != "" {
				t.Fatalf("failed local setup returned runtime or lost cause: %+v %v", r, err)
			}
			after, err := os.ReadFile(publication)
			if err != nil || string(before) != string(after) {
				t.Fatal("failed local setup changed publication")
			}
			o.Mode = "openai"
			if testInstall(t, e, o).Launch != old.Launch {
				t.Fatal("failed local setup invalidated OpenAI reuse")
			}
			local.provision, local.probe = e.provision, e.probe
			o.Mode = "local"
			testInstall(t, local, o)
			if b, err := os.ReadFile(old.Launch); err != nil || string(b) != "verified" || hits.Load() != 1 {
				t.Fatal("retry lost earlier runtime or verified downloads")
			}
		})
	}
}
