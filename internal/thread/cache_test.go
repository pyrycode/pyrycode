package thread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestStoreCacheEpochs(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"clean", "unclean", "unloaded-clean", "missing", "corrupt", "rules", "ahead", "items", "marker", "schema", "partial", "truncated-history"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			h := history.New(root)
			s := NewStore(h)
			entries := testStoreAppend(t, h, testStoreA, testMessage(1))
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			first := testStoreWait(t, s, testStoreA, StateUsable, 1)
			if len(first.Epoch) != 32 {
				t.Fatal("missing random epoch")
			}
			if err := s.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			if got := testStoreWait(t, s, testStoreA, StateUsable, 1); got.Epoch != first.Epoch {
				t.Fatal("reload changed epoch")
			}
			if err := s.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			if mode != "unclean" {
				if err := s.Shutdown(); err != nil {
					t.Fatal(err)
				}
			}
			dir, err := h.LogDir(testStoreA)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, cacheName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var c cacheRecord
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				err = os.Remove(path)
			case "corrupt":
				err = os.WriteFile(path, raw[:len(raw)/2], 0600)
			case "schema":
				c.Schema++
				raw, err = json.Marshal(c)
			case "partial":
				c.Complete = false
				raw, err = json.Marshal(c)
			case "truncated-history":
				files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
				log, readErr := os.ReadFile(files[0])
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = os.WriteFile(files[0], log[:bytes.IndexByte(log, '\n')+1], 0600)
				entries = nil
			case "rules":
				c.Rules++
				raw, err = json.Marshal(c)
			case "ahead":
				c.Version++
				raw, err = json.Marshal(c)
			case "items":
				c.Items[0].Summary = "forged"
				raw, err = json.Marshal(c)
			case "marker":
				err = os.Remove(filepath.Join(dir, recoveryName))
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "rules" || mode == "ahead" || mode == "items" || mode == "schema" || mode == "partial" {
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			next := NewStore(history.New(root))
			t.Cleanup(func() { _ = next.Shutdown() })
			if err := next.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			got := testStoreWait(t, next, testStoreA, StateUsable, uint64(len(entries)))
			testStoreEqual(t, got, testStoreA, entries)
			retain := mode == "clean" || mode == "unloaded-clean"
			if (got.Epoch == first.Epoch) != retain {
				t.Fatalf("epoch retention %s: %s -> %s", mode, first.Epoch, got.Epoch)
			}
			for _, name := range []string{cacheName, recoveryName, runName(next.runID)} {
				info, err := os.Stat(filepath.Join(dir, name))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("permissions %s: %v", name, err)
				}
			}
		})
	}
}

func TestStoreCacheShutdown(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "failed"}[fail], func(t *testing.T) {
			root := t.TempDir()
			h := history.New(root)
			s := NewStore(h)
			for _, id := range []string{string(testStoreA), string(testStoreB)} {
				conv := testStoreA
				if id == string(testStoreB) {
					conv = testStoreB
				}
				testStoreAppend(t, h, conv, testMessage(1))
				if err := s.Load(context.Background(), conv); err != nil {
					t.Fatal(err)
				}
				testStoreWait(t, s, conv, StateUsable, 1)
			}
			a, b := s.Snapshot(testStoreA).Epoch, s.Snapshot(testStoreB).Epoch
			if a == b {
				t.Fatal("shared epoch")
			}
			if err := s.Unload(testStoreB); err != nil {
				t.Fatal(err)
			}
			dir, _ := h.LogDir(testStoreB)
			path := filepath.Join(dir, cacheName)
			if fail {
				if err := os.Rename(path, path+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			results := make(chan error, 3)
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); results <- s.Shutdown() }()
			}
			wg.Wait()
			close(results)
			for err := range results {
				if fail != errors.Is(err, ErrPersistence) {
					t.Fatalf("shutdown result %v", err)
				}
			}
			if fail {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+"-saved", path); err != nil {
					t.Fatal(err)
				}
			}
			next := NewStore(history.New(root))
			t.Cleanup(func() { _ = next.Shutdown() })
			for _, conv := range []struct {
				id    string
				epoch string
			}{{string(testStoreA), a}, {string(testStoreB), b}} {
				id := testStoreA
				if conv.id == string(testStoreB) {
					id = testStoreB
				}
				if err := next.Load(context.Background(), id); err != nil {
					t.Fatal(err)
				}
				got := testStoreWait(t, next, id, StateUsable, 1)
				if (got.Epoch == conv.epoch) == fail {
					t.Fatal("partial shutdown certified clean")
				}
			}
		})
	}
}

func TestStoreCacheContinuation(t *testing.T) {
	t.Parallel()
	fixture := []history.Entry{
		testMain(1, "assistant_delta", `,"text":"unfinished "`),
		testAcceptance(2, "phone"),
		testMain(3, "assistant_delta", `,"text":"continued"`),
		testMain(4, "tool_result", `,"tool_use_id":"read","is_error":false`),
		testChild(5, "tool_use", "parent", `,"tool_use_id":"nested","name":"Read"`),
		testTask(6, "background_task_updated", `,"status":"completed","summary":"early"`),
		testTask(7, "background_task_started", `,"tool_call_id":"parent"`),
		testMain(8, "tool_use", `,"tool_use_id":"parent","name":"Agent"`),
		testMain(9, "tool_use", `,"tool_use_id":"read","name":"Read"`),
		testChild(10, "tool_result", "parent", `,"tool_use_id":"nested","is_error":false`),
		testMessage(11),
		testSendOutcome(12, "send_delivered", `,"accepted_entry_id":2,"delivery_entry_id":11`, "delivered"),
	}
	for cut := 0; cut <= len(fixture); cut++ {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			root := t.TempDir()
			h := history.New(root)
			s := NewStore(h)
			entries := testStoreAppend(t, h, testStoreA, fixture[:cut]...)
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			first := testStoreWait(t, s, testStoreA, StateUsable, uint64(cut))
			testStoreEqual(t, first, testStoreA, entries)
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
			next := NewStore(history.New(root))
			t.Cleanup(func() { _ = next.Shutdown() })
			if err := next.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			reopened := testStoreWait(t, next, testStoreA, StateUsable, uint64(cut))
			if reopened.Epoch != first.Epoch {
				t.Fatal("clean continuation rotated epoch")
			}
			testStoreEqual(t, reopened, testStoreA, entries)
			for _, e := range fixture[cut:] {
				entries = append(entries, testStoreAppend(t, next.history, testStoreA, e)...)
				got := testStoreWait(t, next, testStoreA, StateUsable, uint64(len(entries)))
				testStoreEqual(t, got, testStoreA, entries)
				if got.Epoch != first.Epoch {
					t.Fatal("tail rotated epoch")
				}
			}
		})
	}
}

func TestStoreCacheSecurity(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"cache-symlink", "cache-directory", "cache-fifo", "marker-symlink", "certificate-symlink", "sibling", "outside", "replace", "marker-replace", "run-replace", "history"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			h := history.New(root)
			s := NewStore(h)
			entries := testStoreAppend(t, h, testStoreA, testMessage(1))
			testStoreAppend(t, h, testStoreB, testMessage(1))
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			first := testStoreWait(t, s, testStoreA, StateUsable, 1)
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
			dir, _ := h.LogDir(testStoreA)
			initialCache, err := os.ReadFile(filepath.Join(dir, cacheName))
			if err != nil {
				t.Fatal(err)
			}
			segments, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
			original, err := os.ReadFile(segments[0])
			if err != nil {
				t.Fatal(err)
			}
			next := NewStore(history.New(root))
			t.Cleanup(func() { _ = next.Shutdown() })
			repair := func() {}
			switch fault {
			case "cache-symlink", "cache-directory", "cache-fifo", "marker-symlink", "certificate-symlink":
				name := cacheName
				if fault == "marker-symlink" {
					name = recoveryName
				}
				if fault == "certificate-symlink" {
					name = runName(s.runID)
				}
				path := filepath.Join(dir, name)
				if err := os.Rename(path, path+"-saved"); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "private-content")
				if err := os.WriteFile(target, []byte("private-payload /host/path"), 0600); err != nil {
					t.Fatal(err)
				}
				if fault == "cache-directory" {
					err = os.Mkdir(path, 0700)
				} else if fault == "cache-fifo" {
					err = syscall.Mkfifo(path, 0600)
				} else {
					err = os.Symlink(target, path)
				}
				if err != nil {
					t.Fatal(err)
				}
				repair = func() {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(path+"-saved", path); err != nil {
						t.Fatal(err)
					}
					raw, err := os.ReadFile(target)
					if err != nil || string(raw) != "private-payload /host/path" {
						t.Fatal("outside leaf changed", err)
					}
				}
			case "sibling", "outside":
				other, _ := h.LogDir(testStoreB)
				if fault == "outside" {
					other = t.TempDir()
				}
				if err := os.Rename(dir, dir+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, dir); err != nil {
					t.Fatal(err)
				}
				repair = func() {
					if err := os.Remove(dir); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(dir+"-saved", dir); err != nil {
						t.Fatal(err)
					}
				}
			case "replace", "marker-replace", "run-replace":
				base := next.replace
				next.replace = func(from, to string) error {
					name := filepath.Base(to)
					fail := fault == "replace" && name == cacheName || fault == "marker-replace" && name == recoveryName || fault == "run-replace" && strings.HasPrefix(name, "thread-run-")
					if fail {
						return errors.New("private-payload /host/path")
					}
					return base(from, to)
				}
				repair = func() { next.replace = base }
			case "history":
				if err := os.WriteFile(segments[0], []byte("private-payload /host/path\n"), 0600); err != nil {
					t.Fatal(err)
				}
				repair = func() {
					if err := os.WriteFile(segments[0], original, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := next.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			snap := testStoreWait(t, next, testStoreA, StateUnavailable, 0)
			if snap.Err != ErrUnavailable || snap.Epoch != "" || snap.Items != nil {
				t.Fatal("unsafe failure", snap)
			}
			if fault == "replace" {
				raw, err := os.ReadFile(filepath.Join(dir, cacheName))
				if err != nil || !bytes.Equal(raw, initialCache) {
					t.Fatal("interrupted replacement changed prior checkpoint", err)
				}
				leftovers, err := filepath.Glob(filepath.Join(dir, ".thread-*"))
				if err != nil || len(leftovers) != 0 {
					t.Fatal("unpublished temporary files retained", err)
				}
			}
			repair()
			raw, err := os.ReadFile(segments[0])
			if err != nil || !bytes.Equal(raw, original) {
				t.Fatal("history changed", err)
			}
			if err := next.Retry(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			got := testStoreWait(t, next, testStoreA, StateUsable, 1)
			testStoreEqual(t, got, testStoreA, entries)
			if fault == "replace" && got.Epoch == first.Epoch {
				t.Fatal("interrupted checkpoint retained prior clean epoch")
			}
			if !errors.Is(next.Unload("../../private"), history.ErrInvalidID) {
				t.Fatal("invalid unload accepted")
			}
		})
	}
}

func TestStoreCacheInterrupted(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"unload", "shutdown", "context"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			h := history.New(root)
			s := NewStore(h)
			testStoreAppend(t, h, testStoreA, testMessage(1))
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			epoch := testStoreWait(t, s, testStoreA, StateUsable, 1).Epoch
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
			next := NewStore(history.New(root))
			reached := make(chan struct{})
			base := next.forward
			next.forward = func(id conversations.ConversationID) (storeReader, error) {
				r, err := base(id)
				return testStoreReader{storeReader: r, walk: func(ctx context.Context, _ uint64, _ func([]history.Entry) error) error {
					close(reached)
					<-ctx.Done()
					return ctx.Err()
				}}, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := next.Load(ctx, testStoreA); err != nil {
				t.Fatal(err)
			}
			testStoreSignal(t, reached)
			switch action {
			case "unload":
				if err := next.Unload(testStoreA); err == nil {
					t.Fatal("incomplete unload succeeded")
				}
			case "shutdown":
				if err := next.Shutdown(); err == nil {
					t.Fatal("incomplete shutdown succeeded")
				}
			case "context":
				cancel()
				testStoreWait(t, next, testStoreA, StateUnavailable, 0)
			}
			if next.Shutdown() == nil {
				t.Fatal("interrupted recovery certified clean")
			}
			reopened := NewStore(history.New(root))
			t.Cleanup(func() { _ = reopened.Shutdown() })
			if err := reopened.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			if got := testStoreWait(t, reopened, testStoreA, StateUsable, 1); got.Epoch == epoch {
				t.Fatal("interrupted recovery retained epoch")
			}
		})
	}
}

func TestStoreCacheFinalCertificate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	h := history.New(root)
	s := NewStore(h)
	testStoreAppend(t, h, testStoreA, testMessage(1))
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	first := testStoreWait(t, s, testStoreA, StateUsable, 1)
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	base := s.replace
	s.replace = func(from, to string) error {
		if filepath.Base(to) == runName(s.runID) {
			return errors.New("private-payload /host/path")
		}
		return base(from, to)
	}
	if err := s.Shutdown(); err != ErrPersistence {
		t.Fatal("final marker failure unreported", err)
	}
	next := NewStore(history.New(root))
	t.Cleanup(func() { _ = next.Shutdown() })
	if err := next.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	if got := testStoreWait(t, next, testStoreA, StateUsable, 1); got.Epoch == first.Epoch {
		t.Fatal("interrupted final certificate retained epoch")
	}
}

func TestStoreCacheZeroHistoryValidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	h := history.New(root)
	testStoreAppend(t, h, testStoreA, testMessage(1))
	dir, _ := h.LogDir(testStoreA)
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	header := raw[:bytes.IndexByte(raw, '\n')+1]
	if err := os.WriteFile(files[0], header, 0600); err != nil {
		t.Fatal(err)
	}
	h = history.New(root)
	s := NewStore(h)
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 0)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	// Prime the history cursor at zero, then make the actual empty log unreadable.
	if _, err := h.LatestEntryID(testStoreA); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files[0], []byte("private-payload /host/path\n"), 0600); err != nil {
		t.Fatal(err)
	}
	next := NewStore(h)
	t.Cleanup(func() { _ = next.Shutdown() })
	base := next.forward
	next.forward = func(id conversations.ConversationID) (storeReader, error) {
		r, err := base(id)
		return testStoreReader{storeReader: r, tail: func(ctx context.Context, _ func([]history.Entry) error) error { <-ctx.Done(); return ctx.Err() }}, err
	}
	if err := next.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, next, testStoreA, StateUnavailable, 0)
}
