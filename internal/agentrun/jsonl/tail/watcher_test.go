package tail

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"

	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
)

const testSessionID = "6fc6d062-1972-4457-9bfd-6b47c7e77e11"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// eventRecorder collects OnEvent / OnEndOfTurn invocations from the Run
// goroutine. Safe for concurrent reads by the test goroutine.
type eventRecorder struct {
	mu       sync.Mutex
	events   []jsonl.Event
	endTurns int
}

func (r *eventRecorder) onEvent(ev jsonl.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *eventRecorder) onEndOfTurn() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endTurns++
}

func (r *eventRecorder) snapshot() (events []jsonl.Event, endTurns int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]jsonl.Event, len(r.events))
	copy(out, r.events)
	return out, r.endTurns
}

// startWatcher builds a Watcher and runs it in a goroutine. Returns the
// watcher, a cancel function, and a wait function that returns Run's error.
// Registers a t.Cleanup that cancels and asserts Run exits within 2s.
func startWatcher(t *testing.T, cfg Config) (*Watcher, context.CancelFunc, func() error) {
	t.Helper()
	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	var runErr error
	go func() {
		defer close(doneCh)
		runErr = w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
			t.Errorf("watcher did not stop within 2s")
		}
	})
	wait := func() error {
		select {
		case <-doneCh:
			return runErr
		case <-time.After(2 * time.Second):
			return errors.New("watcher did not stop within 2s")
		}
	}
	return w, cancel, wait
}

// expectedEncodedDir returns the encoded project-dir form for path, as the
// watcher would compute it.
func expectedEncodedDir(t *testing.T, home, workdir, sessionID string) string {
	t.Helper()
	return filepath.Dir(tuidriver.SessionJSONLPath(home, workdir, sessionID))
}

// waitForEndOfTurn polls the recorder until OnEndOfTurn has fired or the
// deadline elapses.
func waitForEndOfTurn(t *testing.T, rec *eventRecorder, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, end := rec.snapshot(); end >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("OnEndOfTurn did not fire within %s", d)
}

// writeLineByLine opens path for append+create, writes lines one at a time
// with a small inter-line delay, calling Sync after each.
func writeLineByLine(t *testing.T, path string, lines []string, delay time.Duration) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line); err != nil {
			t.Fatalf("write: %v", err)
		}
		if !strings.HasSuffix(line, "\n") {
			if _, err := f.WriteString("\n"); err != nil {
				t.Fatalf("write newline: %v", err)
			}
		}
		if err := f.Sync(); err != nil {
			t.Fatalf("sync: %v", err)
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
}

func TestNew_ValidationErrors(t *testing.T) {
	t.Parallel()
	base := Config{
		Workdir:     t.TempDir(),
		SessionID:   testSessionID,
		HomeDir:     t.TempDir(),
		OnEvent:     func(jsonl.Event) {},
		OnEndOfTurn: func() {},
		Logger:      discardLogger(),
	}
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"empty workdir", func(c *Config) { c.Workdir = "" }, "empty Workdir"},
		{"empty session", func(c *Config) { c.SessionID = "" }, "empty SessionID"},
		{"nil OnEvent", func(c *Config) { c.OnEvent = nil }, "nil OnEvent"},
		{"nil OnEndOfTurn", func(c *Config) { c.OnEndOfTurn = nil }, "nil OnEndOfTurn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mut(&cfg)
			_, err := New(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// TestNew_RealpathEncoding verifies that on macOS, a workdir under /var
// resolves through /private/var, and the watched directory is the
// -private-var-... form rather than -var-....
func TestNew_RealpathEncoding(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("realpath /var -> /private/var symlink is macOS-specific")
	}
	t.Parallel()
	// t.TempDir() on darwin lands under /var/folders/..., which resolves to
	// /private/var/folders/...
	workdir := t.TempDir()
	home := t.TempDir()
	rec := &eventRecorder{}

	startWatcher(t, Config{
		Workdir:     workdir,
		SessionID:   testSessionID,
		HomeDir:     home,
		OnEvent:     rec.onEvent,
		OnEndOfTurn: rec.onEndOfTurn,
		Logger:      discardLogger(),
	})

	wantDir := expectedEncodedDir(t, home, workdir, testSessionID)
	if base := filepath.Base(wantDir); !strings.HasPrefix(base, "-private-var-folders-") {
		t.Fatalf("encoded dir base = %q, want prefix -private-var-folders-", base)
	}
	if info, err := os.Stat(wantDir); err != nil || !info.IsDir() {
		t.Fatalf("expected encoded dir %s to exist as dir; stat err=%v info=%v", wantDir, err, info)
	}
}

// TestWatcher_LateCreate covers the "watcher started before JSONL exists"
// path: CREATE fires, the file is opened with retry, then WRITE events drive
// further parsing until end-of-turn.
func TestWatcher_LateCreate(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	home := t.TempDir()
	rec := &eventRecorder{}

	startWatcher(t, Config{
		Workdir:     workdir,
		SessionID:   testSessionID,
		HomeDir:     home,
		OnEvent:     rec.onEvent,
		OnEndOfTurn: rec.onEndOfTurn,
		Logger:      discardLogger(),
	})

	dir := expectedEncodedDir(t, home, workdir, testSessionID)
	path := filepath.Join(dir, testSessionID+".jsonl")

	// Give the watcher a moment to install its fsnotify subscription before
	// the file appears.
	time.Sleep(50 * time.Millisecond)

	lines := []string{
		`{"type":"assistant","message":{"stop_reason":"tool_use","content":[{"type":"text","text":"thinking..."}]}}`,
		`{"type":"user","message":{"role":"user","content":"more"}}`,
		`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`,
	}
	writeLineByLine(t, path, lines, 20*time.Millisecond)

	waitForEndOfTurn(t, rec, 3*time.Second)

	events, endTurns := rec.snapshot()
	if endTurns != 1 {
		t.Fatalf("endTurns = %d, want 1", endTurns)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (assistant + user + assistant)", len(events))
	}
	if !events[len(events)-1].EndOfTurn {
		t.Fatalf("last event EndOfTurn = false, want true")
	}
}

// TestWatcher_ExistingFile covers the initial-stat path: the JSONL exists
// before Run starts; no fsnotify event is needed to pick it up.
func TestWatcher_ExistingFile(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	home := t.TempDir()
	rec := &eventRecorder{}

	dir := expectedEncodedDir(t, home, workdir, testSessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, testSessionID+".jsonl")
	body := strings.Join([]string{
		`{"type":"assistant","message":{"stop_reason":"tool_use","content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"last"}]}}`,
		``,
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, _, wait := startWatcher(t, Config{
		Workdir:     workdir,
		SessionID:   testSessionID,
		HomeDir:     home,
		OnEvent:     rec.onEvent,
		OnEndOfTurn: rec.onEndOfTurn,
		Logger:      discardLogger(),
	})

	waitForEndOfTurn(t, rec, 2*time.Second)
	if err := wait(); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}

	events, endTurns := rec.snapshot()
	if endTurns != 1 {
		t.Fatalf("endTurns = %d, want 1", endTurns)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
}

// TestWatcher_FixtureIntegration replays testdata/clean.jsonl line by line
// into a tempdir-hosted fake home and asserts the watcher sees every
// assistant entry exactly once, with end-of-turn firing once on the last.
func TestWatcher_FixtureIntegration(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	home := t.TempDir()
	rec := &eventRecorder{}

	startWatcher(t, Config{
		Workdir:     workdir,
		SessionID:   testSessionID,
		HomeDir:     home,
		OnEvent:     rec.onEvent,
		OnEndOfTurn: rec.onEndOfTurn,
		Logger:      discardLogger(),
	})

	dir := expectedEncodedDir(t, home, workdir, testSessionID)
	path := filepath.Join(dir, testSessionID+".jsonl")

	src, err := os.Open("../testdata/clean.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer dst.Close()

	// Replay line by line with a tiny delay between writes to interleave
	// fsnotify events with reads.
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	// Wait briefly to ensure fsnotify is armed before the first write.
	time.Sleep(50 * time.Millisecond)
	for sc.Scan() {
		if _, err := dst.Write(append(sc.Bytes(), '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = dst.Sync()
		time.Sleep(3 * time.Millisecond)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}

	waitForEndOfTurn(t, rec, 5*time.Second)

	events, endTurns := rec.snapshot()
	if endTurns != 1 {
		t.Fatalf("endTurns = %d, want 1", endTurns)
	}
	// clean.jsonl has 64 lines; the sole end_turn assistant entry sits at
	// line 62, so the watcher stops draining there. With every line kind
	// now surfaced, that is 62 Events total.
	if got, want := len(events), 62; got != want {
		t.Fatalf("events = %d, want %d", got, want)
	}
	if !events[len(events)-1].EndOfTurn {
		t.Fatalf("last event EndOfTurn = false, want true")
	}
}

// TestWatcher_ContextCancellation verifies Run exits with ctx.Err() when the
// context is cancelled while waiting for the JSONL to appear.
func TestWatcher_ContextCancellation(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	home := t.TempDir()
	rec := &eventRecorder{}

	_, cancel, wait := startWatcher(t, Config{
		Workdir:     workdir,
		SessionID:   testSessionID,
		HomeDir:     home,
		OnEvent:     rec.onEvent,
		OnEndOfTurn: rec.onEndOfTurn,
		Logger:      discardLogger(),
	})

	// Confirm the watcher is in its event loop.
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// TestWatcher_WaitTimeout verifies that when the JSONL never appears, Run
// returns a wrapped context.DeadlineExceeded from tuidriver.WaitForSessionJSONL.
func TestWatcher_WaitTimeout(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	home := t.TempDir()
	rec := &eventRecorder{}

	w, err := New(Config{
		Workdir:     workdir,
		SessionID:   testSessionID,
		HomeDir:     home,
		OnEvent:     rec.onEvent,
		OnEndOfTurn: rec.onEndOfTurn,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- w.Run(ctx) }()

	select {
	case runErr := <-doneCh:
		if !errors.Is(runErr, context.DeadlineExceeded) {
			t.Fatalf("Run returned %v, want context.DeadlineExceeded", runErr)
		}
	case <-time.After(650 * time.Millisecond):
		t.Fatalf("Run did not return within 500ms of the deadline")
	}
}

// TestWatcher_SpecialCharWorkdir spot-checks that the path the watcher
// computes goes through tuidriver's encoder for special characters
// (underscore, space, dot — all of which the old EncodeProjectDir mapped
// wrong before #501). Confirms the seam is wired up; tuidriver's own
// cwd_test.go owns the exhaustive byte-rule coverage.
func TestWatcher_SpecialCharWorkdir(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	workdir := filepath.Join(parent, "a_b c.d")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("MkdirAll workdir: %v", err)
	}
	home := t.TempDir()

	w, err := New(Config{
		Workdir:     workdir,
		SessionID:   testSessionID,
		HomeDir:     home,
		OnEvent:     func(jsonl.Event) {},
		OnEndOfTurn: func() {},
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.fsw.Close() })

	if !strings.HasSuffix(filepath.Base(w.dir), "-a-b-c-d") {
		t.Fatalf("encoded dir base = %q, want suffix -a-b-c-d", filepath.Base(w.dir))
	}
	info, err := os.Stat(w.dir)
	if err != nil {
		t.Fatalf("stat encoded dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("encoded path %s is not a directory", w.dir)
	}
}
