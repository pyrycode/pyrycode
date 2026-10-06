//go:build e2e_realclaude

package streamsup

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func spawnBinaryAlias(t *testing.T, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	if err := os.Symlink(os.Args[0], bin); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestSpawnBinarySelection(t *testing.T) {
	file := filepath.Join(t.TempDir(), "selection")
	a, b := spawnBinaryAlias(t, "child-a"), spawnBinaryAlias(t, "child-b")
	t.Setenv("PYRY_E2E_CLAUDE_BIN_FILE", file)
	writeSpawnBinarySelection(t, file, a)
	out := &safeBuffer{}
	cfg := helperRunCfg(t, "spawn_selection_witness", out, &safeBuffer{}, "SELECTION_SENTINEL=unchanged")
	cfg.Args = []string{"--model", "selection-model"}
	cfg.BackoffInitial = time.Second
	cfg.BackoffMax = time.Second
	exits := make(chan time.Time, 4)
	cfg.OnChildExit = func() {
		select {
		case exits <- time.Now():
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()
	first := waitSpawnBinaryWitness(t, out, 1)
	writeSpawnBinarySelection(t, file, b)
	if _, err := r.Stdin().Write([]byte("alive\n")); err != nil {
		t.Fatal(err)
	}
	waitForContains(t, out, "ALIVE", 10*time.Second)
	if r.State().RestartCount != 0 {
		t.Fatal("selection killed live child")
	}
	if _, err := r.Stdin().Write([]byte("crash\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for r.State().Phase != PhaseBackoff && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.State().Phase != PhaseBackoff {
		t.Fatal("no observed backoff")
	}
	var backoffStart time.Time
	select {
	case backoffStart = <-exits:
	case <-time.After(10 * time.Second):
		t.Fatal("missing exit notification")
	}
	writeSpawnBinarySelection(t, file, a)
	writeSpawnBinarySelection(t, file, b)
	second := waitSpawnBinaryWitness(t, out, 2)
	if time.Since(backoffStart) < cfg.BackoffInitial {
		t.Fatal("selection interrupted backoff")
	}
	if first.Args[0] != a || second.Args[0] != b {
		t.Fatalf("launched wrong executables: %v / %v", first.Args, second.Args)
	}
	if !reflect.DeepEqual(first.Args[1:], second.Args[1:]) {
		// Only the established session's create/resume flag may change.
		want := slices.Clone(first.Args[1:])
		for i, v := range want {
			if v == "--session-id" {
				want[i] = "--resume"
			}
		}
		if !reflect.DeepEqual(want, second.Args[1:]) {
			t.Fatalf("argv changed: %v / %v", first.Args, second.Args)
		}
	}
	if !reflect.DeepEqual(first.Env, second.Env) || first.Cwd != cfg.WorkDir || second.Cwd != cfg.WorkDir {
		t.Fatal("environment or workdir changed")
	}
	if !slices.Contains(second.Env, "SELECTION_SENTINEL=unchanged") || !slices.Contains(second.Args, testSessionID) {
		t.Fatal("spawn inputs lost")
	}
}

func TestSpawnBinarySelectionConcurrentLaunch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "selection")
	a, b := spawnBinaryAlias(t, "child-a"), spawnBinaryAlias(t, "child-b")
	t.Setenv("PYRY_E2E_CLAUDE_BIN_FILE", file)
	writeSpawnBinarySelection(t, file, a)
	out := &safeBuffer{}
	cfg := helperRunCfg(t, "spawn_selection_witness", out, &safeBuffer{})
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = time.Millisecond
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			bin := a
			if i%2 != 0 {
				bin = b
			}
			if err := os.WriteFile(file+".tmp", []byte(bin), 0o600); err != nil {
				done <- err
				return
			}
			if err := os.Rename(file+".tmp", file); err != nil {
				done <- err
				return
			}
		}
	}()
	defer func() {
		close(stop)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for i := 1; i <= 8; i++ {
		w := waitSpawnBinaryWitness(t, out, i)
		if w.Args[0] != a && w.Args[0] != b {
			t.Fatalf("torn or ignored selection: %v", w.Args)
		}
		if _, err := r.Stdin().Write([]byte("crash\n")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSpawnBinarySelectionInput(t *testing.T) {
	r := &Runner{cfg: Config{ClaudeBin: "configured"}}
	t.Setenv("PYRY_E2E_CLAUDE_BIN_FILE", "")
	if got, err := r.spawnClaudeBin(); got != "configured" || err != nil {
		t.Fatalf("unset: %q %v", got, err)
	}
	for _, tc := range []struct {
		name, body string
		missing    bool
		nonRegular string
	}{
		{name: "empty"}, {name: "relative", body: "claude"}, {name: "oversized", body: "/" + strings.Repeat("x", 4096)}, {name: "missing", missing: true},
		{name: "multiline", body: "/one\n/two"},
		{name: "nul", body: "/one\x00two"},
		{name: "directory", nonRegular: "directory"},
		{name: "fifo", nonRegular: "fifo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "selection")
			t.Setenv("PYRY_E2E_CLAUDE_BIN_FILE", file)
			switch tc.nonRegular {
			case "directory":
				if err := os.Mkdir(file, 0o700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(file, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.missing && tc.nonRegular == "" {
				if err := os.WriteFile(file, []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.spawnClaudeBin(); err == nil {
				t.Fatal("invalid activated input accepted")
			}
		})
	}
}
