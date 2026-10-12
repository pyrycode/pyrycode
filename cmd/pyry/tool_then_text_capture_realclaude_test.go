//go:build e2e_realclaude

package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestToolThenTextDaemonCapture(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PYRY_TOOL_THEN_TEXT_CAPTURE_DIR", dir)
	var events []turnevent.Event
	cfg := streamsup.Config{ClaudeBin: "missing-test-claude", WorkDir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RequestInitializeOnSpawn: true}
	oldExit := false
	cfg.OnChildExit = func() { oldExit = true }
	sink, bind, err := newToolThenTextCapture(&cfg, func(ev turnevent.Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	parser := streamsup.NewParser(sink, cfg.Logger)
	bind(parser)
	if cfg.ControlParser != parser || !cfg.RequestInitializeOnSpawn {
		t.Fatal("capture lost control correlation or initialize")
	}
	wire := "{\"type\":\"future_kind\"}\n{\"type\":\"result\"}\n"
	for i := 0; i < 2; i++ {
		cfg.Logger.Info("spawning claude", "args", []string{"--model", "observed"})
		for _, chunk := range []string{wire[:7], wire[7:]} {
			if _, err := cfg.Stdout.Write([]byte(chunk)); err != nil {
				t.Fatal(err)
			}
		}
		cfg.OnChildExit()
	}
	if !oldExit {
		t.Fatal("capture lost child-exit callback")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.manifest.json"))
	if len(paths) != 2 {
		t.Fatalf("child manifests=%d, want 2", len(paths))
	}
	for _, path := range paths {
		blob, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m toolThenTextDaemonManifest
		if err := json.Unmarshal(blob, &m); err != nil {
			t.Fatal(err)
		}
		if m.BytesDropped != 0 || m.Tail != 0 || m.IOFailed || len(m.Rejections) != 1 || m.SpawnShape[1] != "observed" {
			t.Fatalf("invalid manifest: %+v", m)
		}
		start, startErr := time.Parse(time.RFC3339Nano, m.StartedAt)
		finish, finishErr := time.Parse(time.RFC3339Nano, m.FinishedAt)
		if startErr != nil || finishErr != nil || finish.Before(start) {
			t.Fatal("child execution window missing")
		}
		raw, err := os.ReadFile(strings.TrimSuffix(path, ".manifest.json") + ".stdout")
		if err != nil || string(raw) != wire {
			t.Fatal("capture changed parser input", err)
		}
		stat, _ := os.Stat(path)
		if stat.Mode().Perm() != 0o600 {
			t.Fatal("manifest must be private")
		}
	}
	if len(events) != 4 {
		t.Fatalf("downstream events=%d, want 4", len(events))
	}
}

func TestToolThenTextDaemonCaptureLoss(t *testing.T) {
	dir := t.TempDir()
	r := &toolThenTextDaemonRecorder{dir: dir, maxBytes: 8}
	r.begin([]string{"test"})
	if _, err := r.Write([]byte("123\n456789")); err != nil {
		t.Fatal(err)
	}
	if err := r.finish(); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.manifest.json"))
	if len(paths) != 1 {
		t.Fatal("missing manifest")
	}
	blob, _ := os.ReadFile(paths[0])
	var m toolThenTextDaemonManifest
	if err := json.Unmarshal(blob, &m); err != nil {
		t.Fatal(err)
	}
	if m.BytesDropped != 2 || m.Tail != 6 {
		t.Fatalf("loss=%d tail=%d", m.BytesDropped, m.Tail)
	}
}
