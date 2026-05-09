package conversations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// syncBuffer is a goroutine-safe sink for slog text output. Tests that read
// while the loop goroutine writes need this; bytes.Buffer is not safe for
// concurrent use.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func captureLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// seedArchivables appends n archive-eligible conversations starting at idOffset
// (so multiple calls don't collide on ID). All entries are 31 days idle and
// unpromoted relative to time.Now() — i.e. archivable on the next sweep.
func seedArchivables(reg *Registry, n, idOffset int) {
	for i := 0; i < n; i++ {
		reg.Create(Conversation{
			ID:         ConversationID(fmt.Sprintf("%08d-2222-4333-8444-555555555555", idOffset+i)),
			Cwd:        fmt.Sprintf("/seed-%d", idOffset+i),
			IsPromoted: false,
			LastUsedAt: time.Now().Add(-31 * 24 * time.Hour),
		})
	}
}

// brokenSavePath returns a path under tmpDir for which reg.Save will always
// fail: the parent directory location exists as a regular file, so the
// MkdirAll inside Save returns ENOTDIR.
func brokenSavePath(t *testing.T, tmpDir string) string {
	t.Helper()
	notADir := filepath.Join(tmpDir, "notadir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file-as-dir: %v", err)
	}
	return filepath.Join(notADir, "conversations.json")
}

func TestSweepOnce(t *testing.T) {
	t.Parallel()

	t.Run("happy-path", func(t *testing.T) {
		t.Parallel()
		reg := &Registry{}
		seedArchivables(reg, 2, 0)
		// One fresh (non-archivable) entry.
		reg.Create(Conversation{
			ID:         ConversationID("99999999-2222-4333-8444-555555555555"),
			Cwd:        "/fresh",
			IsPromoted: false,
			LastUsedAt: time.Now(),
		})

		path := filepath.Join(t.TempDir(), "conversations.json")
		log, buf := captureLogger()

		sweepOnce(reg, path, log)

		if got := len(reg.List()); got != 1 {
			t.Errorf("len(List) after sweep = %d, want 1", got)
		}
		loaded, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%s): %v", path, err)
		}
		if got := len(loaded.List()); got != 1 {
			t.Errorf("loaded len(List) = %d, want 1", got)
		}
		out := buf.String()
		if !strings.Contains(out, "level=INFO") {
			t.Errorf("log missing INFO record: %q", out)
		}
		if !strings.Contains(out, "count=2") {
			t.Errorf("log missing count=2: %q", out)
		}
	})

	t.Run("no-op-tick", func(t *testing.T) {
		t.Parallel()
		reg := &Registry{}
		reg.Create(Conversation{
			ID:         ConversationID("00000001-2222-4333-8444-555555555555"),
			Cwd:        "/fresh",
			IsPromoted: false,
			LastUsedAt: time.Now(),
		})

		path := filepath.Join(t.TempDir(), "conversations.json")
		log, buf := captureLogger()

		sweepOnce(reg, path, log)

		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expected file not to exist, stat err = %v", err)
		}
		if out := buf.String(); out != "" {
			t.Errorf("expected empty log buffer, got %q", out)
		}
	})

	t.Run("save-error-non-fatal", func(t *testing.T) {
		t.Parallel()
		reg := &Registry{}
		seedArchivables(reg, 1, 0)

		path := brokenSavePath(t, t.TempDir())
		log, buf := captureLogger()

		sweepOnce(reg, path, log)

		if got := len(reg.List()); got != 0 {
			t.Errorf("len(List) after sweep = %d, want 0 (in-memory delete still happens)", got)
		}
		out := buf.String()
		if !strings.Contains(out, "level=ERROR") {
			t.Errorf("log missing ERROR record: %q", out)
		}
		if !strings.Contains(out, "archived=1") {
			t.Errorf("log missing archived=1: %q", out)
		}
	})
}

// waitFor polls cond every ms until it returns true or deadline expires.
// Returns true if cond eventually held.
func waitFor(deadline time.Duration, cond func() bool) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

func TestRunSweepLoop_TicksAndCancels(t *testing.T) {
	t.Parallel()
	reg := &Registry{}
	seedArchivables(reg, 2, 0)
	// One fresh entry that should survive.
	reg.Create(Conversation{
		ID:         ConversationID("99999999-2222-4333-8444-555555555555"),
		Cwd:        "/fresh",
		IsPromoted: false,
		LastUsedAt: time.Now(),
	})

	path := filepath.Join(t.TempDir(), "conversations.json")
	log, _ := captureLogger()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunSweepLoop(ctx, reg, path, 5*time.Millisecond, log)
	}()

	if !waitFor(2*time.Second, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}) {
		cancel()
		<-done
		t.Fatalf("file %s did not appear within deadline", path)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunSweepLoop returned %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunSweepLoop did not return within 1s of cancel")
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	survivors := loaded.List()
	if len(survivors) != 1 {
		t.Fatalf("loaded len(List) = %d, want 1", len(survivors))
	}
	if survivors[0].Cwd != "/fresh" {
		t.Errorf("survivor Cwd = %q, want /fresh", survivors[0].Cwd)
	}
}

func TestRunSweepLoop_NoOpDoesNotSave(t *testing.T) {
	t.Parallel()
	reg := &Registry{}
	reg.Create(Conversation{
		ID:         ConversationID("00000001-2222-4333-8444-555555555555"),
		Cwd:        "/fresh",
		IsPromoted: false,
		LastUsedAt: time.Now(),
	})

	path := filepath.Join(t.TempDir(), "conversations.json")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunSweepLoop(ctx, reg, path, time.Millisecond, discardLogger())
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunSweepLoop returned %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunSweepLoop did not return within 1s of cancel")
	}

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected file not to exist, stat err = %v", err)
	}
}

// countSubstring counts non-overlapping occurrences of sub in s.
func countSubstring(s, sub string) int {
	if sub == "" {
		return 0
	}
	n := 0
	for i := 0; ; {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return n
		}
		n++
		i += j + len(sub)
	}
}

func TestRunSweepLoop_SaveErrorContinues(t *testing.T) {
	t.Parallel()
	reg := &Registry{}
	seedArchivables(reg, 1, 0)

	path := brokenSavePath(t, t.TempDir())
	log, buf := captureLogger()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunSweepLoop(ctx, reg, path, time.Millisecond, log)
	}()

	if !waitFor(2*time.Second, func() bool {
		return countSubstring(buf.String(), "level=ERROR") >= 1
	}) {
		cancel()
		<-done
		t.Fatalf("did not observe first ERROR within deadline; buf=%q", buf.String())
	}

	// Re-seed: a new archivable while the loop is mid-run forces another tick
	// to attempt Save and fail again. Proves the loop kept ticking after the
	// first error rather than crashing or terminating.
	seedArchivables(reg, 1, 100)

	if !waitFor(2*time.Second, func() bool {
		return countSubstring(buf.String(), "level=ERROR") >= 2
	}) {
		cancel()
		<-done
		t.Fatalf("did not observe second ERROR within deadline; buf=%q", buf.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunSweepLoop returned %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunSweepLoop did not return within 1s of cancel")
	}

	out := buf.String()
	if got := countSubstring(out, "level=ERROR"); got < 2 {
		t.Errorf("ERROR count = %d, want >= 2; buf=%q", got, out)
	}
	if strings.Contains(out, "level=INFO") {
		t.Errorf("unexpected INFO record (Save always failed); buf=%q", out)
	}
}
