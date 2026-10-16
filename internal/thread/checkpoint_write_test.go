package thread

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testCheckpointWriter(t *testing.T, entries []history.Entry) (*checkpointWriter, *Fold, []history.Entry) {
	t.Helper()
	s := NewStore(history.New(t.TempDir()))
	entries = testStoreAppend(t, s.history, testStoreA, entries...)
	if _, err := s.history.EnsureLogDir(testStoreA); err != nil {
		t.Fatal(err)
	}
	f, source := testReferenceFold(entries)
	f.conversationID = string(testStoreA)
	return &checkpointWriter{s: s, id: testStoreA, source: source}, f, entries
}

func testCommitCheckpoint(t *testing.T, w *checkpointWriter, f *Fold, previous []CheckpointItem, clean bool) ([]CheckpointItem, error) {
	t.Helper()
	d, rows, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	var changed []CheckpointItem
	for i, row := range rows {
		if i >= len(previous) || !reflect.DeepEqual(row, previous[i]) {
			changed = append(changed, row)
		}
	}
	checksum := ""
	if d.Version > 0 {
		checksum = strings.Repeat("a", 64)
	}
	c := cacheRecord{Schema: cacheSchema, Rules: foldingRules, Epoch: strings.Repeat("c", 32), Version: d.Version, LastShownVersion: d.LastShown, Checksum: checksum, Clean: clean}
	return rows, w.commit(d, rows, changed, c)
}

func testCheckpointIndex(t *testing.T, w *checkpointWriter) (string, []byte) {
	t.Helper()
	dir, err := w.s.history.LogDir(w.id)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, w.ack.Generation)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, raw
}

func TestWriteCheckpointResume(t *testing.T) {
	t.Parallel()
	histories := [][]history.Entry{testFoldCorpus(151), testReferenceSendEntries(), {
		testChild(1, "assistant_delta", "missing", `,"text":"hidden"`),
		testMain(2, "tool_use", `,"tool_use_id":"missing","name":"Agent"`),
		testChild(3, "assistant_delta", "missing", `,"text":"late"`),
	}}
	comparisons := 0
	for family, entries := range histories {
		t.Run(fmt.Sprint(family), func(t *testing.T) {
			w, f, entries := testCheckpointWriter(t, entries)
			var previous []CheckpointItem
			for _, cut := range []int{0, 1, (len(entries) + 1) / 2, len(entries)} {
				testFeed(t, f, entries[f.Version():cut]...)
				var prefix []byte
				generation := w.ack.Generation
				if generation != "" {
					_, prefix = testCheckpointIndex(t, w)
				}
				rows, err := testCommitCheckpoint(t, w, f, previous, false)
				if err != nil {
					t.Fatal(err)
				}
				path, actual := testCheckpointIndex(t, w)
				want := bytes.Clone(prefix)
				for i, row := range rows {
					if generation == "" || i >= len(previous) || !reflect.DeepEqual(row, previous[i]) {
						want = append(want, testCheckpointFrame(t, row)...)
					}
				}
				if !bytes.Equal(actual, want) || generation != "" && generation != w.ack.Generation {
					t.Fatal("incremental bytes differ")
				}
				for _, leaf := range []string{path, filepath.Join(filepath.Dir(path), checkpointStateName)} {
					info, err := os.Stat(leaf)
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatal("nonprivate leaf", err)
					}
				}
				r, _, err := w.s.readCheckpoint(w.id, w.source)
				if err != nil {
					t.Fatal(err)
				}
				baseline, _ := testReferenceFold(entries)
				baseline.conversationID = string(w.id)
				testFeed(t, baseline, entries[:cut]...)
				p, q := testResumeProof{}, testResumeProof{}
				if p.update(t, baseline, true) != q.update(t, r, true) {
					t.Fatal("immediate mismatch")
				}
				comparisons++
				for _, e := range entries[cut:] {
					testFeed(t, baseline, e)
					testFeed(t, r, e)
					if p.update(t, baseline, false) != q.update(t, r, false) {
						t.Fatal("suffix mismatch", e.ID)
					}
					comparisons++
				}
				previous = rows
			}
		})
	}
	t.Logf("%d immediate/suffix comparisons", comparisons)
}

func TestWriteCheckpointFaults(t *testing.T) {
	t.Parallel()
	stages := []string{"history-sync", "reconcile", "index-truncate", "index-append", "index-sync", "index-close", "index-directory", "state-write", "state-sync", "state-close", "state-rename", "state-directory", "partial-append", "short-index", "short-state"}
	for _, clean := range []bool{false, true} {
		for _, stage := range stages {
			t.Run(fmt.Sprintf("%t/%s", clean, stage), func(t *testing.T) {
				w, f, entries := testCheckpointWriter(t, []history.Entry{testMessage(1), testMessage(2), testMessage(3)})
				testFeed(t, f, entries[0])
				previous, err := testCommitCheckpoint(t, w, f, nil, false)
				if err != nil {
					t.Fatal(err)
				}
				ack := w.ack
				path, prefix := testCheckpointIndex(t, w)
				if err := os.WriteFile(path, append(bytes.Clone(prefix), 1, 2, 3), 0600); err != nil {
					t.Fatal(err)
				}
				testFeed(t, f, entries[1])
				injected := errors.New("injected")
				fired := false
				w.step = func(name string, action func() error) error {
					if name == stage {
						fired = true
						if name == "index-close" || name == "state-close" {
							if err := action(); err != nil {
								t.Fatal(err)
							}
						}
						return injected
					}
					return action()
				}
				w.write = func(file *os.File, raw []byte) (int, error) {
					index := file.Name() == path
					if stage == "partial-append" && index || stage == "short-index" && index || stage == "short-state" && !index {
						fired = true
						n, err := file.Write(raw[:len(raw)/2])
						if stage == "partial-append" {
							err = injected
						}
						return n, err
					}
					return file.Write(raw)
				}
				if _, err := testCommitCheckpoint(t, w, f, previous, clean); !errors.Is(err, ErrPersistence) || !fired || !reflect.DeepEqual(w.ack, ack) {
					t.Fatal("failure acknowledged or not exercised", err, fired)
				}
				r, saved, readErr := w.s.readCheckpoint(w.id, w.source)
				if clean {
					if r != nil && saved.Certificate.Clean {
						t.Fatal("failed clean claim survived")
					}
				} else {
					want := uint64(1)
					if stage == "state-directory" {
						want = 2
					}
					if readErr != nil || r.Version() != want {
						t.Fatal("prior/new checkpoint lost", readErr)
					}
					baseline, _ := testReferenceFold(entries)
					baseline.conversationID = string(w.id)
					testFeed(t, baseline, entries[:want]...)
					p, q := testResumeProof{}, testResumeProof{}
					if p.update(t, baseline, true) != q.update(t, r, true) {
						t.Fatal("mixed checkpoint")
					}
				}
				w.step, w.write = nil, nil
				// When B survived, retry C without resupplying B's indispensable row.
				_, atB, _ := f.ExportCheckpoint()
				if stage == "state-directory" && !clean {
					previous = atB
				}
				testFeed(t, f, entries[2])
				if _, err := testCommitCheckpoint(t, w, f, previous, false); err != nil {
					t.Fatal("retry", err)
				}
				r, _, err = w.s.readCheckpoint(w.id, w.source)
				if err != nil || r.Version() != 3 {
					t.Fatal("retry boundary", err)
				}
				p, q := testResumeProof{}, testResumeProof{}
				if p.update(t, f, true) != q.update(t, r, true) {
					t.Fatal("retry lost records")
				}
			})
		}
	}
}

func TestWriteCheckpointRecovery(t *testing.T) {
	t.Parallel()
	for _, damage := range []string{"missing", "json", "graph", "index"} {
		t.Run(damage, func(t *testing.T) {
			w, f, entries := testCheckpointWriter(t, []history.Entry{testMessage(1), testMessage(2)})
			testFeed(t, f, entries[0])
			previous, err := testCommitCheckpoint(t, w, f, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			path, _ := testCheckpointIndex(t, w)
			statePath := filepath.Join(filepath.Dir(path), checkpointStateName)
			switch damage {
			case "missing":
				err = os.Remove(statePath)
			case "json":
				err = os.WriteFile(statePath, []byte("{"), 0600)
			case "graph":
				bad := w.ack
				bad.Fold.Openings = map[uint64]int{1: 999}
				testWriteCheckpointState(t, filepath.Dir(path), bad)
			case "index":
				err = os.WriteFile(path, []byte("torn"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if r, _, err := w.s.readCheckpoint(w.id, w.source); err == nil || r != nil {
				t.Fatal("invalid state restored")
			}
			oldGeneration := w.ack.Generation
			testFeed(t, f, entries[1])
			if _, err := testCommitCheckpoint(t, w, f, previous, false); err != nil || oldGeneration == w.ack.Generation {
				t.Fatal("not a fresh rebuild", err)
			}
			r, _, err := w.s.readCheckpoint(w.id, w.source)
			if err != nil {
				t.Fatal(err)
			}
			p, q := testResumeProof{}, testResumeProof{}
			if p.update(t, f, true) != q.update(t, r, true) {
				t.Fatal("delta-only rebuild")
			}
		})
	}
}

func TestWriteCheckpointOrderAndRecipe(t *testing.T) {
	t.Parallel()
	w, f, entries := testCheckpointWriter(t, []history.Entry{testMessage(1), testMessage(2), testMessage(3)})
	testFeed(t, f, entries[:2]...)
	_, rows, _ := f.ExportCheckpoint()
	var stages []string
	w.step = func(stage string, action func() error) error {
		stages = append(stages, stage)
		return action()
	}
	// Supply no deltas on the first checkpoint; it must write the full export.
	previous, err := testCommitCheckpoint(t, w, f, rows, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "history-sync,reconcile,index-truncate,index-append,index-append,index-sync,index-close,index-directory,state-write,state-sync,state-close,state-rename,state-directory"
	if strings.Join(stages, ",") != want {
		t.Fatal("durability order", stages)
	}
	w.step = nil
	path, prefix := testCheckpointIndex(t, w)
	recipe := f.recipes[1]
	recipe.fields = map[string]recipeField{"task_id": {value: []byte(`"recipe-only"`)}}
	f.recipes[1] = recipe
	_, changed, _ := f.ExportCheckpoint()
	if !reflect.DeepEqual(previous[0].Metadata, changed[0].Metadata) {
		t.Fatal("fixture changed public revision")
	}
	// Omitting a recipe-only change must fail before corrupting the saved fold.
	if _, err := testCommitCheckpoint(t, w, f, changed, false); err == nil {
		t.Fatal("incomplete delta admitted")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, prefix) {
		t.Fatal("invalid delta mutated index")
	}
	if _, err := testCommitCheckpoint(t, w, f, previous, false); err != nil {
		t.Fatal(err)
	}
	_, got := testCheckpointIndex(t, w)
	if !bytes.Equal(got, append(prefix, testCheckpointFrame(t, changed[0])...)) {
		t.Fatal("unchanged row rewritten")
	}
	r, _, err := w.s.readCheckpoint(w.id, w.source)
	if err != nil {
		t.Fatal(err)
	}
	for _, fold := range []*Fold{f, r} {
		testFeed(t, fold, entries[2])
	}
	p, q := testResumeProof{}, testResumeProof{}
	if p.update(t, f, true) != q.update(t, r, true) {
		t.Fatal("recipe-only continuation differs")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), cacheName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scalar certificate changed", err)
	}
}

func TestWriteCheckpointFreshFaults(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"history-sync", "index-append", "index-directory", "state-write", "state-rename", "state-directory"} {
		t.Run(stage, func(t *testing.T) {
			w, f, entries := testCheckpointWriter(t, []history.Entry{testMessage(1)})
			testFeed(t, f, entries...)
			fired := false
			w.step = func(name string, action func() error) error {
				if name == stage {
					fired = true
					return errors.New("injected")
				}
				return action()
			}
			if _, err := testCommitCheckpoint(t, w, f, nil, false); err == nil || !fired || w.ack.Generation != "" {
				t.Fatal("fresh failure acknowledged", err)
			}
			r, _, err := w.s.readCheckpoint(w.id, w.source)
			if stage == "state-directory" {
				if err != nil || r.Version() != 1 {
					t.Fatal("new complete state missing", err)
				}
			} else if err == nil || r != nil {
				t.Fatal("absent state restored")
			}
			w.step = nil
			if _, err := testCommitCheckpoint(t, w, f, nil, false); err != nil {
				t.Fatal("fresh retry", err)
			}
		})
	}
}

func TestWriteCheckpointSecurity(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"state-symlink", "state-directory", "state-fifo", "index-symlink", "index-directory", "index-fifo", "outside", "sibling"} {
		t.Run(fault, func(t *testing.T) {
			w, f, entries := testCheckpointWriter(t, []history.Entry{testMessage(1), testMessage(2)})
			testFeed(t, f, entries[0])
			previous, err := testCommitCheckpoint(t, w, f, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			path, _ := testCheckpointIndex(t, w)
			dir := filepath.Dir(path)
			segments, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
			before, err := os.ReadFile(segments[0])
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			marker := filepath.Join(outside, "unchanged")
			if err := os.WriteFile(marker, before, 0600); err != nil {
				t.Fatal(err)
			}
			if fault == "outside" || fault == "sibling" {
				target := outside
				if fault == "sibling" {
					target, err = w.s.history.EnsureLogDir(testStoreB)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(dir, dir+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
				dir += "-saved"
			} else {
				if strings.HasPrefix(fault, "state-") {
					path = filepath.Join(dir, checkpointStateName)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				switch {
				case strings.HasSuffix(fault, "symlink"):
					err = os.Symlink(marker, path)
				case strings.HasSuffix(fault, "directory"):
					err = os.Mkdir(path, 0700)
				default:
					err = syscall.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			testFeed(t, f, entries[1])
			if _, err := testCommitCheckpoint(t, w, f, previous, false); !errors.Is(err, ErrPersistence) {
				t.Fatal("unsafe path admitted", err)
			}
			for _, path := range []string{marker, filepath.Join(dir, filepath.Base(segments[0]))} {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, before) {
					t.Fatal("outside/history modified", err)
				}
			}
		})
	}
}
