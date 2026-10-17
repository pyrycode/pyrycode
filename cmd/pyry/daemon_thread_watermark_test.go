package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func testThreadWatermark(t *testing.T, empty bool) (relay.V2SessionConfig, *history.Store, *thread.Store, *threadShadow, string, []byte) {
	t.Helper()
	h := history.New(t.TempDir())
	reg, err := conversations.Load("")
	if err != nil {
		t.Fatal(err)
	}
	reg.Create(conversations.Conversation{ID: testConvID})
	s := thread.NewStore(h)
	t.Cleanup(func() { _ = s.Shutdown() })
	v := uint64(0)
	if !empty {
		v = testShadowAppend(t, h, testConvID)
	}
	if _, err := h.EnsureLogDir(testConvID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.LatestEntryID(testConvID); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(t.Context(), testConvID); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { got := s.Status(testConvID); return got.State == thread.StateUsable && got.Version == v })
	if err := s.Unload(testConvID); err != nil {
		t.Fatal(err)
	}
	shadow := newThreadShadow(h, reg, s, discardLogger())
	t.Cleanup(shadow.cancel)
	cfg := relay.V2SessionConfig{}
	installDaemonThreadRelay(&cfg, shadow)
	dir, err := h.LogDir(testConvID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "thread-certificate.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, h, s, shadow, path, raw
}

func TestDaemonThreadLastShownCertificates(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"valid", "zero", "cold", "cold-zero", "dirty", "schema", "rules", "epoch", "checksum", "watermark", "ahead", "behind", "missing", "malformed", "wrong-type", "trailing", "symlink", "directory", "history-failure", "unregistered", "removed", "zero-checksum", "absent-version", "null-version", "absent-shown", "null-shown", "absent-checksum", "null-checksum"} {
		t.Run(name, func(t *testing.T) {
			empty := name == "zero" || name == "cold-zero" || name == "zero-checksum" || strings.HasPrefix(name, "absent-") || strings.HasPrefix(name, "null-")
			cfg, h, store, shadow, path, original := testThreadWatermark(t, empty)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(original, &fields); err != nil {
				t.Fatal(err)
			}
			set := func(key, value string) { fields[key] = json.RawMessage(value) }
			want, present := uint64(0), false
			switch name {
			case "valid", "zero":
				present = true
				if !empty {
					want = 1
				}
			case "cold", "cold-zero":
				root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))
				h = history.New(root)
				store = thread.NewStore(h)
				t.Cleanup(func() { _ = store.Shutdown() })
				shadow.store = store
				installDaemonThreadRelay(&cfg, shadow)
			case "dirty":
				set("Clean", "false")
			case "schema":
				set("Schema", "999")
			case "rules":
				set("Rules", "999")
			case "epoch":
				set("Epoch", `"invalid"`)
			case "checksum":
				set("Checksum", `"invalid"`)
			case "zero-checksum":
				set("Checksum", `"`+strings.Repeat("a", 64)+`"`)
			case "wrong-type":
				set("Version", `"1"`)
			case "watermark":
				set("LastShownVersion", "2")
			case "ahead":
				set("Version", "2")
			case "behind":
				if _, err := h.Append(testConvID, "unsupported", []byte(`{}`), time.Now()); err != nil {
					t.Fatal(err)
				}
			case "history-failure":
				dir := filepath.Dir(path)
				if err := os.Rename(dir, dir+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			case "unregistered":
				shadow.registry, _ = conversations.Load("")
			case "removed":
				shadow.registry.Delete(testConvID)
			default:
				for key, suffix := range map[string]string{"Version": "version", "LastShownVersion": "shown", "Checksum": "checksum"} {
					if name == "absent-"+suffix {
						delete(fields, key)
					}
					if name == "null-"+suffix {
						set(key, "null")
					}
				}
			}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if name == "malformed" {
				raw = []byte(`{"Version":`)
			}
			if name == "trailing" {
				raw = append(raw, []byte(` {}`)...)
			}
			if name != "history-failure" {
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if name == "missing" || name == "symlink" || name == "directory" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if name == "symlink" {
						target := filepath.Join(t.TempDir(), "certificate.json")
						if err := os.WriteFile(target, original, 0600); err != nil {
							t.Fatal(err)
						}
						err = os.Symlink(target, path)
					}
					if name == "directory" {
						err = os.Mkdir(path, 0700)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			for range 3 {
				if got, ok := cfg.ThreadLastShownVersion(testConvID); got != want || ok != present {
					t.Fatalf("last shown = %d, %t; want %d, %t", got, ok, want, present)
				}
			}
			if store.Status(testConvID).State != thread.StateNotLoaded {
				t.Fatal("lookup loaded worker")
			}
			if name == "valid" || name == "zero" {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(after, raw) {
					t.Fatalf("lookup changed certificate: %v", err)
				}
			}
		})
	}
}

func TestDaemonThreadLastShownLoadedZero(t *testing.T) {
	cfg, _, store, _, _, _ := testThreadWatermark(t, true)
	if err := store.Load(t.Context(), testConvID); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(testConvID).State == thread.StateUsable })
	if got, present := cfg.ThreadLastShownVersion(testConvID); got != 0 || !present {
		t.Fatalf("loaded zero = %d, %t", got, present)
	}
	if allocs := testing.AllocsPerRun(100, func() { cfg.ThreadLastShownVersion(testConvID) }); allocs != 0 {
		t.Fatalf("loaded zero allocated %g times", allocs)
	}
}

func TestDaemonThreadLastShownLifecycle(t *testing.T) {
	cfg, h, store, _, path, _ := testThreadWatermark(t, false)
	check := func(want uint64, present bool) {
		t.Helper()
		if got, ok := cfg.ThreadLastShownVersion(testConvID); got != want || ok != present {
			t.Fatalf("last shown = %d, %t; want %d, %t", got, ok, want, present)
		}
	}
	check(1, true)
	if _, err := h.Append(testConvID, "unsupported", []byte(`{}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	check(0, false)
	if err := store.Load(t.Context(), testConvID); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(testConvID).Version == 2 })
	check(1, true)
	if allocs := testing.AllocsPerRun(100, func() { cfg.ThreadLastShownVersion(testConvID) }); allocs != 0 {
		t.Fatalf("loaded lookup allocated %g times", allocs)
	}
	// A nonregular certificate cannot affect the loaded metadata path.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	check(1, true)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := store.Unload(testConvID); err != nil {
		t.Fatal(err)
	}
	check(1, true)
	// Poison every segment after joined retirement. Metadata must still succeed.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	poisoned := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		segment := filepath.Join(filepath.Dir(path), entry.Name())
		if err := os.Rename(segment, segment+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(segment, 0700); err != nil {
			t.Fatal(err)
		}
		poisoned++
	}
	if poisoned == 0 {
		t.Fatal("fixture has no history segments")
	}
	check(1, true)
}

func TestDaemonThreadLastShownUnavailable(t *testing.T) {
	t.Parallel()
	cfg, h, _, shadow, _, _ := testThreadWatermark(t, false)
	entered, release := make(chan struct{}), make(chan struct{})
	var folds atomic.Int32
	store := thread.NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
		if folds.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return errors.New("unavailable")
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	t.Cleanup(func() { _ = store.Shutdown() })
	shadow.store = store
	installDaemonThreadRelay(&cfg, shadow)
	if _, ok := cfg.ThreadLastShownVersion(testConvID); !ok || folds.Load() != 0 {
		t.Fatal("fallback started fold or lost certificate")
	}
	// Keep the unloaded certificate check above, then require suffix folding.
	if _, err := h.Append(testConvID, "unsupported", []byte(`{}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(t.Context(), testConvID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("fold not entered")
	}
	if _, ok := cfg.ThreadLastShownVersion(testConvID); ok {
		t.Fatal("rebuilding view used certificate")
	}
	close(release)
	testShadowWait(t, func() bool { return store.Status(testConvID).State == thread.StateUnavailable })
	if _, ok := cfg.ThreadLastShownVersion(testConvID); ok {
		t.Fatal("unavailable view used certificate")
	}
}
