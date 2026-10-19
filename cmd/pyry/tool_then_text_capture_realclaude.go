//go:build e2e_realclaude

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type toolThenTextDaemonManifest struct {
	StartedAt      string   `json:"started_at"`
	FinishedAt     string   `json:"finished_at"`
	Source         string   `json:"source"`
	DaemonRevision string   `json:"daemon_revision"`
	DaemonDirty    bool     `json:"daemon_dirty"`
	ClaudeVersion  string   `json:"claude_version"`
	SpawnShape     []string `json:"spawn_shape"`
	Home           string   `json:"home"`
	Workdir        string   `json:"workdir"`
	BytesDropped   int      `json:"bytes_dropped"`
	Tail           int      `json:"unterminated_tail_bytes"`
	IOFailed       bool     `json:"io_failed"`
	CapturedBytes  int      `json:"captured_bytes"`
	StdoutSHA256   string   `json:"stdout_sha256"`
	Rejections     []string `json:"rejections"`
}

// The tagged recorder observes the same bytes and events as the daemon. Raw
// recovery files are private; only the deny-scanning reader may promote them.
type toolThenTextDaemonRecorder struct {
	mu       sync.Mutex
	dir      string
	maxBytes int
	written  int
	file     *os.File
	base     string
	seed     toolThenTextDaemonManifest
	manifest toolThenTextDaemonManifest
}

func newToolThenTextCapture(cfg *streamsup.Config, sink func(turnevent.Event)) (func(turnevent.Event), func(*streamsup.Parser), error) {
	dir := os.Getenv("PYRY_TOOL_THEN_TEXT_CAPTURE_DIR")
	if dir == "" {
		return sink, func(p *streamsup.Parser) { cfg.Stdout = p }, nil
	}
	stat, err := os.Lstat(dir)
	if err != nil || !stat.IsDir() || stat.Mode().Perm() != 0o700 {
		return nil, nil, errors.New("capture requires an existing private directory")
	}
	owner, ok := stat.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		return nil, nil, errors.New("capture directory must belong to the test user")
	}
	r := &toolThenTextDaemonRecorder{dir: dir, maxBytes: 8 << 20}
	r.seed = toolThenTextDaemonManifest{Source: "mobile-daemon", Home: os.Getenv("HOME"), Workdir: cfg.WorkDir}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				r.seed.DaemonRevision = setting.Value
			case "vcs.modified":
				r.seed.DaemonDirty = setting.Value != "false"
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	version, versionErr := exec.CommandContext(ctx, cfg.ClaudeBin, "--version").Output()
	cancel()
	if versionErr == nil {
		r.seed.ClaudeVersion = strings.TrimSpace(string(version))
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cfg.Logger = slog.New(toolThenTextCaptureHandler{Handler: logger.Handler(), recorder: r})
	return func(ev turnevent.Event) {
			if rejected, ok := ev.(turnevent.Unrecognized); ok {
				r.mu.Lock()
				r.manifest.Rejections = append(r.manifest.Rejections, fmt.Sprintf("%s/%s/%t", rejected.Site, rejected.Kind, rejected.Truncated))
				r.mu.Unlock()
			}
			sink(ev)
		}, func(p *streamsup.Parser) {
			cfg.Stdout = io.MultiWriter(p, r)
			cfg.ControlParser = p
			previousExit := cfg.OnChildExit
			cfg.OnChildExit = func() {
				if err := r.finish(); err != nil {
					logger.Error("tool-then-text capture finalization failed")
				}
				if previousExit != nil {
					previousExit()
				}
			}
		}, nil
}

func (r *toolThenTextDaemonRecorder) begin(args []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.manifest = r.seed
	r.manifest.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.manifest.SpawnShape = append([]string(nil), args...)
	r.manifest.Rejections = []string{}
	r.written = 0
	file, err := os.CreateTemp(r.dir, "daemon-*.stdout")
	if err != nil {
		r.manifest.IOFailed = true
		return
	}
	r.file, r.base = file, strings.TrimSuffix(file.Name(), ".stdout")
}

func (r *toolThenTextDaemonRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if last := bytes.LastIndexByte(b, '\n'); last >= 0 {
		r.manifest.Tail = len(b) - last - 1
	} else {
		r.manifest.Tail += len(b)
	}
	keep := min(len(b), max(0, r.maxBytes-r.written))
	r.manifest.BytesDropped += len(b) - keep
	if keep > 0 {
		if r.file == nil {
			r.manifest.IOFailed = true
		} else if n, err := r.file.Write(b[:keep]); err != nil || n != keep {
			r.manifest.IOFailed = true
		}
		r.written += keep
	}
	// Capture I/O never changes the daemon's parser or restart behavior. The
	// manifest makes any instrument failure fatal at evidence admission.
	return len(b), nil
}

func (r *toolThenTextDaemonRecorder) finish() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return errors.New("missing capture file")
	}
	if err := r.file.Close(); err != nil {
		r.manifest.IOFailed = true
	}
	r.file = nil
	r.manifest.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.manifest.CapturedBytes = r.written
	if raw, err := os.ReadFile(r.base + ".stdout"); err != nil {
		r.manifest.IOFailed = true
	} else {
		r.manifest.StdoutSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	blob, err := json.Marshal(r.manifest)
	if err != nil {
		return errors.New("encode capture manifest")
	}
	file, err := os.OpenFile(filepath.Clean(r.base)+".manifest.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("create capture manifest")
	}
	_, writeErr := file.Write(append(blob, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("write capture manifest")
	}
	return nil
}

type toolThenTextCaptureHandler struct {
	slog.Handler
	recorder *toolThenTextDaemonRecorder
}

func (h toolThenTextCaptureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level == slog.LevelInfo || h.Handler.Enabled(ctx, level)
}

func (h toolThenTextCaptureHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "spawning claude" {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "args" {
				if args, ok := attr.Value.Any().([]string); ok {
					h.recorder.begin(args)
				}
			}
			return true
		})
	}
	if h.Handler.Enabled(ctx, record.Level) {
		return h.Handler.Handle(ctx, record)
	}
	return nil
}

func (h toolThenTextCaptureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return toolThenTextCaptureHandler{Handler: h.Handler.WithAttrs(attrs), recorder: h.recorder}
}

func (h toolThenTextCaptureHandler) WithGroup(name string) slog.Handler {
	return toolThenTextCaptureHandler{Handler: h.Handler.WithGroup(name), recorder: h.recorder}
}
