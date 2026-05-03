package rotation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// uuidStemPattern matches the canonical 36-char lowercase UUIDv4 stem claude
// uses for its <uuid>.jsonl filenames. Same shape as sessions.NewID's output.
var uuidStemPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// probeRetryDelays is the bounded retry schedule the watcher walks when a
// CREATE arrives before claude has the file open. Total worst-case wait is
// 250ms, well inside the AC's "within ~1 second".
var probeRetryDelays = []time.Duration{0, 50 * time.Millisecond, 200 * time.Millisecond}

// SessionRef is one (id, pid) pair the watcher should consider when matching
// a CREATE event. PID == 0 means no live child — skip and let the next event
// retry.
type SessionRef struct {
	ID  string
	PID int
}

// Config is the watcher's contract with its host. All callbacks are invoked
// from the watcher's single event-loop goroutine, but MUST be safe to call
// from a goroutine other than the one that constructed them.
type Config struct {
	// Dir is the claude sessions dir to watch (cfg.ClaudeSessionsDir).
	Dir string

	// Probe is the platform-specific FD probe. Required.
	Probe Probe

	// Snapshot returns the (id, pid) pairs to consider on each CREATE event.
	// The watcher snapshots once per event; ordering is not significant.
	Snapshot func() []SessionRef

	// IsAllocated reports whether id is in the freshly-allocated set, and
	// consumes the entry on a true return. Watcher calls this before
	// considering a CREATE for rotation.
	IsAllocated func(id string) bool

	// OnRotate is invoked when a CREATE for newID matches a session whose
	// probe reports it has newID's JSONL open. The watcher logs and continues
	// on error — rotation detection failures are not fatal.
	OnRotate func(oldID, newID string) error

	// Logger is required.
	Logger *slog.Logger
}

// Watcher detects /clear-style UUID rotations by combining fsnotify CREATE
// events on the claude session dir with a per-PID probe of each tracked
// process's currently-open JSONL.
type Watcher struct {
	cfg Config
	fsw *fsnotify.Watcher
	// resolvedDir is cfg.Dir with symlinks resolved, captured once at New so
	// the per-event match in handleCreate compares against the same canonical
	// form the platform probe returns. Falls back to cfg.Dir if EvalSymlinks
	// fails.
	resolvedDir string
}

// New constructs a Watcher. Returns an error only if fsnotify itself fails
// to initialize or the dir cannot be created and added. A missing Dir is
// created (MkdirAll, 0700) before the watch is added.
func New(cfg Config) (*Watcher, error) {
	if cfg.Dir == "" {
		return nil, errors.New("rotation: empty dir")
	}
	if cfg.Probe == nil {
		return nil, errors.New("rotation: nil Probe")
	}
	if cfg.Snapshot == nil {
		return nil, errors.New("rotation: nil Snapshot")
	}
	if cfg.OnRotate == nil {
		return nil, errors.New("rotation: nil OnRotate")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.IsAllocated == nil {
		cfg.IsAllocated = func(string) bool { return false }
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", cfg.Dir, err)
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}
	if err := fsw.Add(cfg.Dir); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("fsnotify add %s: %w", cfg.Dir, err)
	}
	resolved, err := filepath.EvalSymlinks(cfg.Dir)
	if err != nil {
		cfg.Logger.Warn("rotation: EvalSymlinks failed; using unresolved dir for path comparison",
			"dir", cfg.Dir, "err", err)
		resolved = cfg.Dir
	}
	return &Watcher{cfg: cfg, fsw: fsw, resolvedDir: resolved}, nil
}

// Run blocks until ctx is cancelled or the underlying fsnotify watcher's
// channels are closed. Returns ctx.Err() on clean shutdown.
func (w *Watcher) Run(ctx context.Context) error {
	defer w.fsw.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			if !ev.Op.Has(fsnotify.Create) {
				continue
			}
			w.handleCreate(ctx, ev.Name)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.cfg.Logger.Warn("rotation: fsnotify error", "err", err)
		}
	}
}

// handleCreate runs the rotation logic for a single CREATE event. Errors are
// logged and squashed so the event loop keeps running.
func (w *Watcher) handleCreate(ctx context.Context, fullPath string) {
	base := filepath.Base(fullPath)
	if !strings.HasSuffix(base, ".jsonl") {
		return
	}
	stem := base[:len(base)-len(".jsonl")]
	if !uuidStemPattern.MatchString(stem) {
		return
	}
	if w.cfg.IsAllocated(stem) {
		w.cfg.Logger.Debug("rotation: skipping fresh-allocated UUID", "id", stem)
		return
	}
	refs := w.cfg.Snapshot()
	if len(refs) == 0 {
		return
	}
	for _, ref := range refs {
		if ref.PID <= 0 {
			continue
		}
		if ref.ID == stem {
			// Already pointing at the new id (e.g. the bootstrap entry was
			// rotated by another path). Nothing to do.
			return
		}
		open, err := w.probeWithRetry(ctx, ref.PID)
		if err != nil {
			w.cfg.Logger.Debug("rotation: probe error", "pid", ref.PID, "err", err)
			continue
		}
		if open == "" {
			continue
		}
		expected := filepath.Join(w.resolvedDir, base)
		if filepath.Clean(open) != expected {
			continue
		}
		w.cfg.Logger.Info("rotation: detected /clear", "from", ref.ID, "to", stem, "pid", ref.PID)
		if err := w.cfg.OnRotate(ref.ID, stem); err != nil {
			w.cfg.Logger.Warn("rotation: OnRotate failed", "from", ref.ID, "to", stem, "err", err)
		}
		return
	}
}

// probeWithRetry walks probeRetryDelays, returning the first non-empty path
// the probe reports for pid. Returns ("", nil) if all attempts came up empty.
// Returns ctx.Err() if the context is cancelled mid-retry.
func (w *Watcher) probeWithRetry(ctx context.Context, pid int) (string, error) {
	var lastErr error
	for _, d := range probeRetryDelays {
		if d > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(d):
			}
		}
		open, err := w.cfg.Probe.OpenJSONL(pid)
		if err != nil {
			lastErr = err
			continue
		}
		if open != "" {
			return open, nil
		}
	}
	return "", lastErr
}
