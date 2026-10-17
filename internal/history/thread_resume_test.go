package history_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestThreadResumeReadAccounting(t *testing.T) {
	root := t.TempDir()
	id := conversations.ConversationID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	h := history.New(root)
	for i := 1; i <= 6010; i++ {
		if i == 6001 {
			s := thread.NewStore(h)
			t.Cleanup(func() { _ = s.Shutdown() })
			if err := s.Load(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			testThreadStatus(t, s, id, 6000)
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
		}
		kind, payload := "future", `{"padding":"`+strings.Repeat("x", 2048)+`"}`
		if i == 1 {
			kind, payload = "message", `{"role":"user","text":"visible"}`
		}
		if _, err := h.Append(id, kind, []byte(payload), time.Unix(int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := h.LogDir(id)
	if err != nil {
		t.Fatal(err)
	}
	segments, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var total int64
	for _, path := range segments {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	if len(segments) < 5 {
		t.Fatal("fixture did not span segments")
	}
	probe := history.New(root)
	var coldBytes int64
	var coldOpens int
	for _, cold := range []bool{true, false} {
		history.ResumeReadStats(probe, true)
		r, err := probe.Forward(id, 6000)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		if err := r.Walk(context.Background(), 6010, func(es []history.Entry) error { count += len(es); return nil }); err != nil || count != 10 {
			t.Fatal(count, err)
		}
		b, n := history.ResumeReadStats(probe, true)
		if cold {
			coldBytes, coldOpens = b, n
		} else {
			t.Logf("Forward cold-discovery=%d bytes/%d opens for %d segments; suffix-only=%d bytes/%d opens on both cold and warm walks", coldBytes-b, coldOpens-n, len(segments), b, n)
		}
	}
	for _, warm := range []bool{false, true} {
		if !warm {
			h = history.New(root)
		}
		history.ResumeReadStats(h, true)
		if _, err := h.Page(id, "", 1); err != nil {
			t.Fatal(err)
		}
		pageBytes, pageOpens := history.ResumeReadStats(h, true)
		if _, err := h.Lookup(id, []uint64{6000}); err != nil {
			t.Fatal(err)
		}
		lookupBytes, lookupOpens := history.ResumeReadStats(h, true)
		if !warm {
			h = history.New(root)
		}
		var feeds atomic.Int32
		s := thread.NewStore(h, func(context.Context, conversations.ConversationID) error { feeds.Add(1); return nil })
		t.Cleanup(func() { _ = s.Shutdown() })
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := s.Load(ctx, id); err != nil {
			t.Fatal(err)
		}
		testThreadStatus(t, s, id, 6010)
		loadBytes, loadOpens := history.ResumeReadStats(h, true)
		if feeds.Load() != 1 || loadBytes >= total {
			t.Fatalf("prefix replay: feeds=%d load=%d total=%d", feeds.Load(), loadBytes, total)
		}
		if got := s.Snapshot(id); got.State != thread.StateUsable || len(got.Items) != 1 {
			t.Fatal("rendered snapshot", got)
		}
		renderBytes, renderOpens := history.ResumeReadStats(h, true)
		t.Logf("warm=%v Page=%d/%d checksum-at-V=%d/%d Store-load=%d/%d requested-render-interval=%d/%d bytes/opens; history=%d bytes (load/render intervals may include the tail's initial recheck)", warm, pageBytes, pageOpens, lookupBytes, lookupOpens, loadBytes, loadOpens, renderBytes, renderOpens, total)
		cancel()
		_ = s.Shutdown() // cancellation deliberately leaves the saved boundary dirty
	}
}

func testThreadStatus(t *testing.T, s *thread.Store, id conversations.ConversationID, version uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		got := s.Status(id)
		if got.State == thread.StateUsable && got.Version == version {
			return
		}
		if got.State == thread.StateUnavailable {
			t.Fatal("unavailable")
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Store did not reach %d", version)
}
