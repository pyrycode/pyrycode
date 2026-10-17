package thread

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func testCacheJSON(t *testing.T, s *Store) (map[string]json.RawMessage, []byte, string) {
	t.Helper()
	dir, err := s.history.LogDir(testStoreA)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, cacheName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields, raw, path
}

func testCacheCompact(t *testing.T, s *Store, snap Snapshot) {
	t.Helper()
	fields, raw, _ := testCacheJSON(t, s)
	if len(raw) >= 1024 || fields["Items"] != nil || fields["Recovery"] != nil {
		t.Fatalf("cache retains content: %d bytes, items present %v", len(raw), fields["Items"] != nil)
	}
	for _, key := range []string{"Schema", "Rules", "Epoch", "Version", "LastShownVersion", "Checksum", "Clean"} {
		if fields[key] == nil {
			t.Fatalf("cache omits %s", key)
		}
	}
	var c struct {
		Schema, Rules             int
		Epoch, Checksum           string
		Version, LastShownVersion uint64
		Clean                     bool
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Schema != cacheSchema || c.Rules != foldingRules || !validToken(c.Epoch) || c.Version > snap.Version || c.LastShownVersion > c.Version || (c.Clean && (c.Epoch != snap.Epoch || c.Version != snap.Version || c.LastShownVersion != snap.LastShownVersion)) {
		t.Fatalf("incorrect complete evidence: %+v versus %+v", c, snap)
	}
	if c.Version == 0 {
		if c.Checksum != "" || c.LastShownVersion != 0 {
			t.Fatal("nonempty zero-version evidence", c)
		}
	} else if digest, err := hex.DecodeString(c.Checksum); err != nil || len(digest) != 32 || hex.EncodeToString(digest) != c.Checksum {
		t.Fatal("invalid checksum", c.Checksum)
	}
	if c.Version > 0 {
		entries, err := s.history.Lookup(testStoreA, []uint64{c.Version})
		if err != nil || len(entries) != 1 || entries[0].ID != c.Version || c.Checksum != entryChecksum(entries[0]) {
			t.Fatal("checksum does not identify the stored consumed entry", err)
		}
	}
}

func TestStoreCacheCompact(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 1, 128 * 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			s, h := testThreadStore(t)
			var entries []history.Entry
			if size > 0 {
				entries = testStoreAppend(t, h, testStoreA, testEntry(0, "message", `{"role":"user","text":"`+strings.Repeat("x", size)+`"}`))
			}
			first := testCheckpointLoad(t, s, testStoreA, uint64(len(entries)))
			testCacheCompact(t, s, first)
			if err := s.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			reloaded := testCheckpointLoad(t, s, testStoreA, first.Version)
			if reloaded.Epoch != first.Epoch {
				t.Fatal("reload rotated epoch")
			}
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
			next := NewStore(h)
			t.Cleanup(func() { _ = next.Shutdown() })
			got := testCheckpointLoad(t, next, testStoreA, first.Version)
			if got.Epoch != first.Epoch {
				t.Fatal("clean reopen rotated epoch")
			}
			testStoreEqual(t, got, testStoreA, entries)
			testCacheCompact(t, next, got)
		})
	}
}

func TestStoreCacheLegacy(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(1))
	first := testCheckpointLoad(t, s, testStoreA, 1)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	fields, _, path := testCacheJSON(t, s)
	testScalarMigration(t, s)
	fields["Schema"] = json.RawMessage(`0`)
	fields["Items"], _ = json.Marshal(first.Items)
	delete(fields, "Checksum")
	delete(fields, "LastShownVersion")
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	epoch := first.Epoch
	for attempt := 0; attempt < 2; attempt++ {
		next := NewStore(h)
		t.Cleanup(func() { _ = next.Shutdown() })
		got := testCheckpointLoad(t, next, testStoreA, 1)
		if (got.Epoch == epoch) != (attempt == 1) {
			t.Fatal("legacy migration must rotate exactly once")
		}
		testCacheCompact(t, next, got)
		epoch = got.Epoch
		if err := next.Shutdown(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreCacheReplacedTail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	h := history.New(root)
	s := NewStore(h)
	entries := testStoreAppend(t, h, testStoreA, testMessage(1), testEntry(2, "unsupported", `{"lost":true}`))
	first := testCheckpointLoad(t, s, testStoreA, 2)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	dir, err := h.LogDir(testStoreA)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(raw, []byte("\n"))
	if err := os.WriteFile(files[0], bytes.Join(lines[:2], nil), 0600); err != nil {
		t.Fatal(err)
	}
	replacement := testStoreAppend(t, history.New(root), testStoreA, testEntry(0, "unsupported", `{"replacement":true}`))
	if replacement[0].ID != 2 {
		t.Fatal("fixture did not reuse the lost ID")
	}
	if bound, err := h.LatestEntryID(testStoreA); err != nil || bound != 2 {
		t.Fatal("fixture lost warm cursor", bound, err)
	}
	next := NewStore(h)
	t.Cleanup(func() { _ = next.Shutdown() })
	got := testCheckpointLoad(t, next, testStoreA, 2)
	if got.Epoch == first.Epoch || !sameItems(got.Items, first.Items) {
		t.Fatal("equal-version replacement must rotate even with identical items")
	}
	entries = append(entries[:1], replacement...)
	testStoreEqual(t, got, testStoreA, entries)
	entries = append(entries, testStoreAppend(t, h, testStoreA, testMessage(0))...)
	testStoreEqual(t, testStoreWait(t, next, testStoreA, StateUsable, 3), testStoreA, entries)
}

func TestStoreCacheCompactRejection(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(map[string]json.RawMessage){
		"missing-checksum": func(c map[string]json.RawMessage) { delete(c, "Checksum") },
		"short-checksum":   func(c map[string]json.RawMessage) { c["Checksum"] = json.RawMessage(`"00"`) },
		"uppercase-checksum": func(c map[string]json.RawMessage) {
			c["Checksum"] = json.RawMessage(`"` + strings.Repeat("AB", 32) + `"`)
		},
		"invalid-checksum": func(c map[string]json.RawMessage) {
			c["Checksum"] = json.RawMessage(`"` + strings.Repeat("zz", 32) + `"`)
		},
		"wrong-checksum": func(c map[string]json.RawMessage) {
			c["Checksum"] = json.RawMessage(`"` + strings.Repeat("00", 32) + `"`)
		},
		"ahead-watermark": func(c map[string]json.RawMessage) { c["LastShownVersion"] = json.RawMessage(`2`) },
		"wrong-watermark": func(c map[string]json.RawMessage) { c["LastShownVersion"] = json.RawMessage(`0`) },
		"zero-version":    func(c map[string]json.RawMessage) { c["Version"] = json.RawMessage(`0`) },
		"invalid-epoch":   func(c map[string]json.RawMessage) { c["Epoch"] = json.RawMessage(`"invalid"`) },
	} {
		t.Run(name, func(t *testing.T) {
			s, h := testThreadStore(t)
			testStoreAppend(t, h, testStoreA, testMessage(1))
			first := testCheckpointLoad(t, s, testStoreA, 1)
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
			fields, _, path := testCacheJSON(t, s)
			testScalarMigration(t, s)
			change(fields)
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			next := NewStore(h)
			t.Cleanup(func() { _ = next.Shutdown() })
			got := testCheckpointLoad(t, next, testStoreA, 1)
			if got.Epoch == first.Epoch {
				t.Fatal("invalid evidence retained epoch")
			}
		})
	}
}

func TestStoreCacheMissingEmptyEvidence(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"Version", "LastShownVersion", "Checksum"} {
		for _, null := range []bool{false, true} {
			t.Run(key+strconv.FormatBool(null), func(t *testing.T) {
				s, h := testThreadStore(t)
				first := testCheckpointLoad(t, s, testStoreA, 0)
				if err := s.Unload(testStoreA); err != nil {
					t.Fatal(err)
				}
				fields, _, path := testCacheJSON(t, s)
				testScalarMigration(t, s)
				if null {
					fields[key] = json.RawMessage(`null`)
				} else {
					delete(fields, key)
				}
				raw, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := s.Shutdown(); err != nil {
					t.Fatal("shutdown touched unloaded evidence", err)
				}
				next := NewStore(h)
				t.Cleanup(func() { _ = next.Shutdown() })
				got := testCheckpointLoad(t, next, testStoreA, 0)
				if got.Epoch == first.Epoch {
					t.Fatal("missing empty evidence retained epoch")
				}
			})
		}
	}
}

func TestStoreCacheChecksumAtVersion(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	entries := testStoreAppend(t, h, testStoreA, testMessage(1))
	first := testCheckpointLoad(t, s, testStoreA, 1)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	entries = append(entries, testStoreAppend(t, h, testStoreA, testMessage(2))...)
	next := NewStore(h)
	t.Cleanup(func() { _ = next.Shutdown() })
	got := testCheckpointLoad(t, next, testStoreA, 2)
	if got.Epoch != first.Epoch {
		t.Fatal("checksum was not checked at the cached version")
	}
	testStoreEqual(t, got, testStoreA, entries)
	testCacheCompact(t, next, got)
}

func TestEntryChecksum(t *testing.T) {
	t.Parallel()
	e := history.Entry{ID: 7, Type: "kind", TS: time.Unix(1700000000, 123456789).UTC(), Payload: json.RawMessage(`{"x":1}`)}
	const golden = "b0c1f41566550ecbb502b417f8fc0a5a4bd840789b96d051a3080335f5c521f5"
	if got := entryChecksum(e); got != golden {
		t.Fatal("checksum encoding changed", got)
	}
	for _, field := range []string{"id", "type", "seconds", "nanoseconds", "payload"} {
		t.Run(field, func(t *testing.T) {
			changed := e
			switch field {
			case "id":
				changed.ID++
			case "type":
				changed.Type += "x"
			case "seconds":
				changed.TS = e.TS.Add(time.Second)
			case "nanoseconds":
				changed.TS = e.TS.Add(time.Nanosecond)
			case "payload":
				changed.Payload = json.RawMessage(`{"x":2}`)
			}
			if entryChecksum(changed) == golden {
				t.Fatal("changed entry retained checksum")
			}
		})
	}
	e.TS = e.TS.In(time.FixedZone("offset", 3600))
	if entryChecksum(e) != golden {
		t.Fatal("timestamp zone changed checksum")
	}
	left, right := e, e
	left.Type, left.Payload = "ab", json.RawMessage("c")
	right.Type, right.Payload = "a", json.RawMessage("bc")
	if entryChecksum(left) == entryChecksum(right) {
		t.Fatal("type/payload boundaries are ambiguous")
	}
}

func TestStoreCachePartialEvidence(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	entries := testStoreAppend(t, h, testStoreA, testMessage(1))
	base := s.forward
	s.forward = func(id conversations.ConversationID) (storeReader, error) {
		r, err := base(id)
		return testStoreReader{storeReader: r, tail: func(ctx context.Context, feed func([]history.Entry) error) error {
			return feed([]history.Entry{testMessage(2), testMessage(2)})
		}}, err
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUnavailable, 0)
	if err := s.Unload(testStoreA); err != ErrUnavailable {
		t.Fatal("partial feed did not fail", err)
	}
	s.mu.Lock()
	p := s.records[testStoreA]
	s.mu.Unlock()
	f := New(string(testStoreA))
	testFeed(t, f, entries...)
	testCacheCompact(t, s, Snapshot{Epoch: p.Epoch, Version: f.Version(), LastShownVersion: f.lastShown})
	if p.Version != 1 || p.LastShownVersion != f.lastShown || p.Checksum != entryChecksum(entries[0]) || s.Shutdown() != ErrPersistence {
		t.Fatal("partial progress certified")
	}
}

func TestStoreCacheFinalEvidence(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"Checksum", "LastShownVersion"} {
		t.Run(field, func(t *testing.T) {
			s, h := testThreadStore(t)
			testStoreAppend(t, h, testStoreA, testMessage(1))
			testCheckpointLoad(t, s, testStoreA, 1)
			if err := s.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			fields, _, path := testCacheJSON(t, s)
			fields[field] = json.RawMessage(`0`)
			testScalarMigration(t, s)
			if field == "Checksum" {
				fields[field] = json.RawMessage(`"` + strings.Repeat("00", 32) + `"`)
			}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.Shutdown(); err != nil {
				t.Fatal("shutdown touched unloaded evidence", err)
			}
			next := NewStore(h)
			t.Cleanup(func() { _ = next.Shutdown() })
			if got := testCheckpointLoad(t, next, testStoreA, 1); string(fields["Epoch"]) == `"`+got.Epoch+`"` {
				t.Fatal("modified evidence retained epoch")
			}
		})
	}
}

// Scalar-only evidence requires migration even when its scalar values are valid.
func testScalarMigration(t *testing.T, s *Store) {
	t.Helper()
	if err := os.Remove(filepath.Join(mustCheckpointDir(t, s), checkpointStateName)); err != nil {
		t.Fatal(err)
	}
}
