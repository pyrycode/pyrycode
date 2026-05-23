// Package tail watches ~/.claude/projects/<encoded-cwd>/<sid>.jsonl for
// claude's per-session JSONL output, opens the file as it appears, and feeds
// appended bytes into a jsonl.Reader until the deterministic end-of-turn
// signal fires or the caller cancels.
//
// MUST NOT log file contents. Like the jsonl package, this watcher logs only
// paths, offsets, and error messages — never line bytes.
package tail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
)

// Config configures a Watcher. OnEvent and OnEndOfTurn are invoked from the
// Run goroutine; callers that need to hand them to another goroutine should
// send on a channel from the callback.
type Config struct {
	// Workdir is the directory pyry's agent-run was invoked from. The watcher
	// resolves Workdir and encodes it the same way claude does to locate the
	// per-session JSONL under ~/.claude/projects/.
	Workdir string

	// SessionID is the claude session UUID — the stem of the .jsonl file we
	// are watching for.
	SessionID string

	// HomeDir is the home directory under which ~/.claude/projects lives.
	// Injectable for tests. If empty, os.UserHomeDir() is consulted at New.
	HomeDir string

	// StartOffset is a resume hint. When > 0, the watcher Seeks the file to
	// StartOffset on open and passes the same value through to jsonl.Config
	// so Offset() reports absolute file positions.
	StartOffset int64

	// OnEvent is invoked once per assistant event surfaced by jsonl.Reader.
	// Required. Invoked from the Run goroutine.
	OnEvent func(jsonl.Event)

	// OnEndOfTurn is invoked at most once, when an Event with EndOfTurn=true
	// is surfaced. After it returns, Run returns nil. Required.
	OnEndOfTurn func()

	// Logger is optional and defaults to slog.Default().
	Logger *slog.Logger
}

// Watcher wraps an fsnotify.Watcher with the claude session-JSONL lifecycle:
// wait for <sid>.jsonl to appear under ~/.claude/projects/<encoded>/, open
// it, and drive a jsonl.Reader until end-of-turn or cancellation.
type Watcher struct {
	cfg          Config
	fsw          *fsnotify.Watcher
	dir          string
	expectedPath string

	// reader is the jsonl.Reader bound to file; both are populated lazily,
	// either by the initial stat in Run or by the first CREATE event.
	file   *os.File
	reader *jsonl.Reader
}

// New constructs a Watcher. Creates the encoded project directory (mode 0700)
// if it does not exist — mirrors internal/sessions/rotation's pattern and
// covers the first-run-in-this-workdir case where claude has not yet created
// the dir.
//
// Caller MUST call Run; otherwise the underlying fsnotify watcher leaks
// (closed via defer in Run).
func New(cfg Config) (*Watcher, error) {
	if cfg.Workdir == "" {
		return nil, errors.New("tail: empty Workdir")
	}
	if cfg.SessionID == "" {
		return nil, errors.New("tail: empty SessionID")
	}
	if cfg.OnEvent == nil {
		return nil, errors.New("tail: nil OnEvent")
	}
	if cfg.OnEndOfTurn == nil {
		return nil, errors.New("tail: nil OnEndOfTurn")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	home := cfg.HomeDir
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("tail: home dir: %w", err)
		}
		home = h
	}

	// Claude resolves symlinks (macOS /var → /private/var) before encoding.
	// tuidriver.SessionJSONLPath is a pure join; the resolution step has to
	// happen here so the encoded directory matches what claude writes to.
	resolved, err := agentrun.ResolveWorkdir(cfg.Workdir)
	if err != nil {
		return nil, fmt.Errorf("tail: resolve workdir: %w", err)
	}
	expectedPath := tuidriver.SessionJSONLPath(home, resolved, cfg.SessionID)
	dir := filepath.Dir(expectedPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("tail: mkdir %s: %w", dir, err)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("tail: fsnotify: %w", err)
	}
	if err := fsw.Add(dir); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("tail: fsnotify add %s: %w", dir, err)
	}

	return &Watcher{
		cfg:          cfg,
		fsw:          fsw,
		dir:          dir,
		expectedPath: expectedPath,
	}, nil
}

// Run blocks until end-of-turn fires, ctx is cancelled, or an unrecoverable
// I/O error occurs. Returns nil after OnEndOfTurn has been invoked.
//
// Cleans up the fsnotify watcher (and any file opened by the watcher) via
// defer; safe to call exactly once per Watcher.
func (w *Watcher) Run(ctx context.Context) error {
	defer func() {
		_ = w.fsw.Close()
		if w.file != nil {
			_ = w.file.Close()
		}
	}()

	// fsnotify.Add was called in New, so any WRITE events that race the
	// wait below are queued in fsw.Events and replayed by the event loop
	// after the initial drain.
	if err := tuidriver.WaitForSessionJSONL(ctx, w.expectedPath); err != nil {
		return err
	}
	if done, err := w.openAndDrain(); err != nil {
		return err
	} else if done {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			if ev.Name != w.expectedPath {
				continue
			}
			if ev.Op.Has(fsnotify.Write) || ev.Op.Has(fsnotify.Create) {
				done, err := w.drain()
				if err != nil {
					return err
				}
				if done {
					return nil
				}
			}
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.cfg.Logger.Warn("tail: fsnotify error", "err", err)
		}
	}
}

// openAndDrain opens the expected JSONL file, constructs the jsonl.Reader,
// and drains whatever bytes are already available. Returns (done=true, nil)
// if end-of-turn fired; (false, nil) if more bytes are needed (i.e. drain hit
// io.EOF); a wrapped error otherwise. Assumes WaitForSessionJSONL has already
// returned nil — appearance is guaranteed, so no bounded retry.
func (w *Watcher) openAndDrain() (bool, error) {
	f, err := os.Open(w.expectedPath)
	if err != nil {
		return false, fmt.Errorf("tail: open %s: %w", w.expectedPath, err)
	}
	if w.cfg.StartOffset > 0 {
		if _, err := f.Seek(w.cfg.StartOffset, io.SeekStart); err != nil {
			_ = f.Close()
			return false, fmt.Errorf("tail: seek %s: %w", w.expectedPath, err)
		}
	}
	w.file = f
	w.reader = jsonl.NewReader(f, jsonl.Config{
		Logger:      w.cfg.Logger,
		StartOffset: w.cfg.StartOffset,
	})
	return w.drain()
}

// drain pulls events from the reader until io.EOF or end-of-turn. Returns
// (true, nil) when OnEndOfTurn has been invoked. Returns (false, nil) on EOF
// — the file is not yet at end-of-turn and the next fsnotify event will
// resume parsing.
func (w *Watcher) drain() (bool, error) {
	for {
		ev, err := w.reader.Next()
		if err == nil {
			w.cfg.OnEvent(ev)
			if ev.EndOfTurn {
				w.cfg.OnEndOfTurn()
				return true, nil
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, fmt.Errorf("tail: read %s: %w", w.expectedPath, err)
	}
}

// Offset returns the byte position of the next not-yet-consumed line. Equals
// Config.StartOffset before the file is opened. Call after Run returns; NOT
// safe to call concurrently with Run.
func (w *Watcher) Offset() int64 {
	if w.reader != nil {
		return w.reader.Offset()
	}
	return w.cfg.StartOffset
}
