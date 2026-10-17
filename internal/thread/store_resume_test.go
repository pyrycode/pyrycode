package thread

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func testStoreResumeSeed(t *testing.T, entries []history.Entry, cut int, clean bool) (*Store, *Fold, []history.Entry, checkpointFileState) {
	t.Helper()
	w, f, entries := testCheckpointWriter(t, entries)
	testFeed(t, f, entries[:cut]...)
	checksum := ""
	if cut > 0 {
		stored, err := w.s.history.Lookup(testStoreA, []uint64{entries[cut-1].ID})
		if err != nil {
			t.Fatal(err)
		}
		checksum = entryChecksum(stored[0])
	}
	c := workerCheckpoint{writer: *w, fold: f, pending: make(map[uint64]bool)}
	if err := c.commit(Snapshot{Version: f.Version(), LastShownVersion: f.lastShown, Epoch: strings.Repeat("c", 32)}, checksum, clean); err != nil {
		t.Fatal(err)
	}
	if !clean {
		stale := c.writer.ack.Certificate
		stale.Clean = true
		if err := w.s.writeCacheFile(testStoreA, cacheName, stale); err != nil {
			t.Fatal(err)
		}
	}
	f.source = newRecentSource(func(ids []uint64) ([]history.Entry, error) { return w.s.history.Lookup(testStoreA, ids) })
	t.Cleanup(func() { _ = w.s.Shutdown() })
	return w.s, f, entries, c.writer.ack
}

func TestStoreResumeCuts(t *testing.T) {
	t.Parallel()
	fixture := []history.Entry{
		testMain(1, "assistant_delta", `,"text":"unfinished "`),
		testChild(2, "tool_use", "parent", `,"tool_use_id":"nested","name":"Read"`),
		testTask(3, "background_task_updated", `,"status":"completed","summary":"early"`),
		testTask(4, "background_task_started", `,"tool_call_id":"parent"`),
		testMain(5, "tool_use", `,"tool_use_id":"parent","name":"Agent"`),
		testMain(6, "assistant_delta", `,"text":"continued"`),
		testChild(7, "tool_result", "parent", `,"tool_use_id":"nested","is_error":false`),
	}
	for family, fixture := range [][]history.Entry{fixture, testReferenceSendEntries(), testFoldCorpus(151)} {
		for _, clean := range []bool{false, true} {
			for _, cut := range []int{0, 1, 2, 3, 4, len(fixture)} {
				if cut > len(fixture) {
					continue
				}
				t.Run(fmt.Sprintf("%d/%t/%d", family, clean, cut), func(t *testing.T) {
					s, baseline, entries, saved := testStoreResumeSeed(t, fixture, cut, clean)
					var feeds atomic.Int32
					s.beforeFold = func(context.Context, conversations.ConversationID) error { feeds.Add(1); return nil }
					version := uint64(len(entries))
					got := testCheckpointLoad(t, s, testStoreA, version)
					if (got.Epoch == saved.Certificate.Epoch) != clean {
						t.Fatal("epoch policy", got.Epoch)
					}
					if cut == len(entries) && feeds.Load() != 0 {
						t.Fatal("replayed prefix without a suffix")
					}
					testFeed(t, baseline, entries[cut:]...)
					check := func(got Snapshot) {
						t.Helper()
						want := baseline.Observe()
						active := false
						for _, item := range baseline.items {
							active = active || item.Active
						}
						if !reflect.DeepEqual(got.Items, want.Items) || got.Version != want.Version || got.LastShownVersion != want.LastShownVersion || got.Active != active {
							t.Fatalf("publication differs: got=%+v want=%+v active=%v", got, want, active)
						}
					}
					check(got)
					if cut == len(entries) {
						if err := s.Unload(testStoreA); err != nil {
							t.Fatal(err)
						}
						_, unchanged, err := s.readCheckpoint(testStoreA, baseline.source)
						if err != nil || unchanged.ValidLength != saved.ValidLength {
							t.Fatal("unchanged resumed inventory rewritten", err)
						}
						if again := testCheckpointLoad(t, s, testStoreA, version); again.Epoch != got.Epoch {
							t.Fatal("clean resumed retirement rotated epoch")
						}
					}
					for _, e := range []history.Entry{testMain(version+1, "assistant_delta", `,"text":" tail"`), testEntry(version+2, "unsupported", `{}`)} {
						appended := testStoreAppend(t, s.history, testStoreA, e)
						testFeed(t, baseline, appended...)
						check(testStoreWait(t, s, testStoreA, StateUsable, e.ID))
					}
					if err := s.Unload(testStoreA); err != nil {
						t.Fatal(err)
					}
					r, state, err := s.readCheckpoint(testStoreA, baseline.source)
					if err != nil || state.Generation != saved.Generation {
						t.Fatal("resume changed generation", err)
					}
					p, q := testResumeProof{}, testResumeProof{}
					if p.update(t, baseline, true) != q.update(t, r, true) {
						t.Fatal("private continuation differs")
					}
					again := testCheckpointLoad(t, s, testStoreA, version+2)
					if again.Epoch != got.Epoch {
						t.Fatal("clean retirement rotated epoch")
					}
				})
			}
		}
	}
}

func TestStoreResumeReject(t *testing.T) {
	t.Parallel()
	for _, damage := range []string{"missing", "corrupt", "fold", "short-log", "changed-V", "owner", "rules", "file-schema", "fold-schema", "scalar-only"} {
		t.Run(damage, func(t *testing.T) {
			s, baseline, _, saved := testStoreResumeSeed(t, []history.Entry{testMessage(1), testEntry(2, "unsupported", `{}`)}, 2, true)
			dir := mustCheckpointDir(t, s)
			var err error
			switch damage {
			case "missing", "scalar-only":
				err = os.Remove(filepath.Join(dir, checkpointStateName))
			case "corrupt":
				err = os.WriteFile(filepath.Join(dir, saved.Generation), []byte("bad"), 0600)
			case "fold":
				saved.Fold.Openings = map[uint64]int{1: 999}
			case "owner":
				saved.Fold.ConversationID = string(testStoreB)
			case "rules":
				saved.Certificate.Rules++
			case "file-schema":
				saved.Schema++
			case "fold-schema":
				saved.Fold.Schema++
			case "short-log", "changed-V":
				paths, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
				raw, readErr := os.ReadFile(paths[0])
				if readErr != nil {
					t.Fatal(readErr)
				}
				if damage == "short-log" {
					first := bytes.IndexByte(raw, '\n') + 1
					raw = raw[:first+bytes.IndexByte(raw[first:], '\n')+1]
				} else {
					raw = bytes.ReplaceAll(raw, []byte("unsupported"), []byte("unrecognized"))
				}
				err = os.WriteFile(paths[0], raw, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if damage == "fold" || damage == "owner" || damage == "rules" || strings.HasSuffix(damage, "schema") {
				testWriteCheckpointState(t, dir, saved)
			}
			// A fresh history owner cannot hide changed bytes behind its read cache.
			h := history.New(filepath.Dir(filepath.Dir(filepath.Dir(dir))))
			next := NewStore(h)
			t.Cleanup(func() { _ = next.Shutdown() })
			bound := uint64(2)
			if damage == "short-log" {
				bound = 1
			}
			got := testCheckpointLoad(t, next, testStoreA, bound)
			if got.Epoch == saved.Certificate.Epoch {
				t.Fatal("rejected state retained epoch")
			}
			if err := next.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			r, state, err := next.readCheckpoint(testStoreA, baseline.source)
			if err != nil || r.Version() != bound || state.Generation == saved.Generation {
				t.Fatal("fallback did not write fresh complete generation", err)
			}
		})
	}
}

func TestStoreResumeReconcile(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"trailing", "resume-truncate", "resume-sync", "resume-close", "unsafe-leaf", "unsafe-directory"} {
		t.Run(fault, func(t *testing.T) {
			s, _, _, saved := testStoreResumeSeed(t, []history.Entry{testMessage(1)}, 1, true)
			dir := mustCheckpointDir(t, s)
			index := filepath.Join(dir, saved.Generation)
			raw, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(index, append(append(bytes.Clone(raw), raw...), 1, 2, 3), 0600); err != nil {
				t.Fatal(err)
			}
			obsolete := filepath.Join(dir, "thread-index-"+strings.Repeat("a", 32)+".bin")
			marker := filepath.Join(t.TempDir(), "marker")
			if err := os.WriteFile(marker, []byte("safe"), 0600); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "unsafe-leaf":
				err = os.Symlink(marker, obsolete)
			case "unsafe-directory":
				err = os.Mkdir(obsolete, 0700)
			default:
				err = os.WriteFile(obsolete, []byte("obsolete"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			s.checkpointStep = func(stage string, action func() error) error {
				if stage == fault {
					return errors.New("injected")
				}
				return action()
			}
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			if fault == "trailing" {
				testStoreWait(t, s, testStoreA, StateUsable, 1)
			} else {
				testStoreWait(t, s, testStoreA, StateUnavailable, 0)
				// Join before repairing hooks or paths.
				s.mu.Lock()
				done := s.workers[testStoreA].done
				s.mu.Unlock()
				<-done
				s.checkpointStep = nil
				if strings.HasPrefix(fault, "unsafe") {
					if err := os.Remove(obsolete); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.Retry(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				testStoreWait(t, s, testStoreA, StateUsable, 1)
			}
			got, err := os.ReadFile(index)
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatal("generation not reconciled", err)
			}
			if _, err := os.Lstat(obsolete); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("obsolete generation survived", err)
			}
			if got, err := os.ReadFile(marker); err != nil || string(got) != "safe" {
				t.Fatal("redirected target changed", err)
			}
		})
	}
}

func TestStoreResumeLoadEvidence(t *testing.T) {
	entries := testFoldCorpus(30000)
	s, _, _, saved := testStoreResumeSeed(t, entries, 29990, true)
	var feeds atomic.Int32
	s.beforeFold = func(context.Context, conversations.ConversationID) error { feeds.Add(1); return nil }
	load := func() {
		if err := s.Load(context.Background(), testStoreA); err != nil {
			t.Fatal(err)
		}
		for s.Status(testStoreA).State == StateRebuilding {
			time.Sleep(time.Millisecond)
		}
		if got := s.Status(testStoreA); got.State != StateUsable || got.Version != 30000 {
			t.Fatal("load evidence unavailable", got.State, got.Version)
		}
	}
	start := time.Now()
	load()
	resume := time.Since(start)
	if feeds.Load() != 1 {
		t.Fatal("resume folded prefix chunks", feeds.Load())
	}
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(mustCheckpointDir(t, s), checkpointStateName)); err != nil {
		t.Fatal(err)
	}
	feeds.Store(0)
	start = time.Now()
	load()
	t.Logf("Linux warm Store.Load through first usable Status (requested rendering excluded): entries=30000 V=%d suffix=10 resume=%s full-refold=%s full-feed-chunks=%d", saved.Fold.Version, resume, time.Since(start), feeds.Load())
}
