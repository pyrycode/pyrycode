package apps

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func testOpen(t *testing.T) *Registry {
	t.Helper()
	r, e := Open(filepath.Join(t.TempDir(), "apps"), testHost)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func testChange(t *testing.T, changed bool, e error) {
	t.Helper()
	if !changed || e != nil {
		t.Fatalf("changed=%v err=%v", changed, e)
	}
}
func testRegister(t *testing.T, r *Registry, data []byte) {
	t.Helper()
	c, e := r.Register(data)
	testChange(t, c, e)
}
func TestRegistryMutations(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	items, rev := r.List()
	if len(items) != 0 || rev != 0 {
		t.Fatal(items, rev)
	}
	testRegister(t, r, []byte(testManifest))
	items, rev = r.List()
	want := items[0]
	if rev != 1 || want.Revision != 1 || want.AppID != testApp || want.Title != "App" || want.Desired != "available" || want.State != "stopped" || want.ActiveRelease != "" || want.PendingRelease != "" || want.LastError != nil {
		t.Fatal(want, rev)
	}
	pretty, _ := json.MarshalIndent(testMap(t), "", "  ")
	c, e := r.Register(pretty)
	if c || e != nil {
		t.Fatal(c, e)
	}
	update := testMap(t)
	update["title"] = "New title"
	update["release_version"] = "2.0.0"
	data := testJSON(t, update)
	if c, e = r.Register(data); c || !errors.Is(e, ErrConflict) {
		t.Fatal(c, e)
	}
	c, e = r.UpdateManifest(testApp, data)
	testChange(t, c, e)
	items, rev = r.List()
	want.Manifest = items[0].Manifest
	want.Revision = 2
	if rev != 2 || !reflect.DeepEqual(items[0], want) || want.Manifest.Title() != "New title" {
		t.Fatal(items, rev)
	}
	c, e = r.UpdateManifest(testApp, data)
	if c || e != nil {
		t.Fatal(c, e)
	}
	if _, v := r.List(); v != 2 {
		t.Fatalf("update no-op advanced to %d", v)
	}
	if c, e = r.UpdateManifest(testID(9), data); c || e == nil {
		t.Fatal("mismatch", c, e)
	}
	if c, e = r.UpdateManifest(testID(9), testInput(testID(9))); c || !errors.Is(e, ErrNotFound) {
		t.Fatal(c, e)
	}
	// Preserve committed fields independently of a manifest candidate.
	r.mu.Lock()
	r.snapshot.Records[0].ActiveRelease = "1.0.0"
	r.snapshot.Records[0].PendingRelease = "2.0.0"
	r.snapshot.Records[0].State = "running"
	r.snapshot.Records[0].Desired = "stopped"
	r.snapshot.Records[0].LastError = &LastError{Code: "app.io_failed", Message: "Update failed"}
	r.mu.Unlock()
	c, e = r.UpdateManifest(testApp, []byte(testManifest))
	testChange(t, c, e)
	items, rev = r.List()
	items[0].LastError.Message = "caller mutation"
	again, _ := r.List()
	if again[0].LastError.Message != "Update failed" {
		t.Fatal("aliased error")
	}
	path := filepath.Join(r.root, "registry.json")
	before := testBytes(t, path)
	reopened, e := Open(r.root, testHost)
	if e != nil {
		t.Fatal(e)
	}
	got, v := reopened.List()
	again[0].State, again[0].Revision = "stopped", rev+1
	if v != rev+1 || !reflect.DeepEqual(got, again) || bytes.Equal(before, testBytes(t, path)) {
		t.Fatal("reopen did not commit stopped intent")
	}
	r = reopened
	c, e = r.Remove(testApp)
	testChange(t, c, e)
	path = filepath.Join(r.root, "registry.json")
	removedBytes := testBytes(t, path)
	c, e = r.Remove(testApp)
	if c || e != nil {
		t.Fatal(c, e)
	}
	reopened, e = Open(r.root, testHost)
	if e != nil {
		t.Fatal(e)
	}
	if rows, v := reopened.List(); len(rows) != 0 || v != 5 || len(reopened.snapshot.Tombstones) != 1 || reopened.snapshot.Tombstones[0].Revision != 5 || !bytes.Equal(removedBytes, testBytes(t, path)) {
		t.Fatal("removal/no-op lost revision or tombstone")
	}
	if c, e = reopened.Register([]byte(testManifest)); c || !errors.Is(e, ErrRemoved) {
		t.Fatal(c, e)
	}
	if c, e = reopened.UpdateManifest(testApp, []byte(testManifest)); c || !errors.Is(e, ErrRemoved) {
		t.Fatal(c, e)
	}
}

func TestCapacityAndRetention(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	for i := 256; i > 0; i-- {
		testRegister(t, r, testInput(testID(i)))
	}
	path := filepath.Join(r.root, "registry.json")
	before := testBytes(t, path)
	if c, e := r.Register(testInput(testID(257))); c || !errors.Is(e, ErrCapacity) {
		t.Fatal(c, e)
	}
	items, rev := r.List()
	if rev != 256 || !bytes.Equal(before, testBytes(t, path)) {
		t.Fatal("capacity changed state")
	}
	for i := 1; i < len(items); i++ {
		if items[i-1].AppID >= items[i].AppID {
			t.Fatal("unsorted")
		}
	}
	for _, sub := range []string{"source/app.json", "builds/version/dist/web/index.html", "data/app.sqlite"} {
		p := filepath.Join(r.root, testID(1), sub)
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte("retained bytes"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	c, e := r.Remove(testID(1))
	testChange(t, c, e)
	testRegister(t, r, testInput(testID(257)))
	for _, sub := range []string{"source/app.json", "builds/version/dist/web/index.html", "data/app.sqlite"} {
		if string(testBytes(t, filepath.Join(r.root, testID(1), sub))) != "retained bytes" {
			t.Fatal("data changed")
		}
	}
	if _, e := Open(r.root, testHost); e != nil {
		t.Fatal(e)
	}
	for p, mode := range map[string]os.FileMode{r.root: 0700, path: 0600} {
		info, e := os.Stat(p)
		if e != nil || info.Mode().Perm() != mode {
			t.Fatal(p, info, e)
		}
	}
	entries, e := os.ReadDir(r.root)
	if e != nil || len(entries) != 2 {
		t.Fatal("temporary file left", entries, e)
	}
}

func TestPersistenceAndVisibility(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	old := testBytes(t, filepath.Join(r.root, "registry.json"))
	held, rev := r.List()
	failure := errors.New("injected write failure")
	r.persist = func(string, snapshot) error { return failure }
	for _, op := range []func() (bool, error){func() (bool, error) { return r.Register(testInput(testID(3))) }, func() (bool, error) {
		m := testMap(t)
		m["title"] = "Changed"
		return r.UpdateManifest(testApp, testJSON(t, m))
	}, func() (bool, error) { return r.Remove(testApp) }} {
		c, e := op()
		got, v := r.List()
		if c || !errors.Is(e, failure) || v != rev || !reflect.DeepEqual(got, held) || !bytes.Equal(old, testBytes(t, filepath.Join(r.root, "registry.json"))) {
			t.Fatal("failed write leaked", c, e, got, v)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	r.persist = func(p string, s snapshot) error { close(entered); <-release; return writeSnapshot(p, s) }
	done := make(chan error, 1)
	go func() { _, e := r.Register(testInput(testID(3))); done <- e }()
	<-entered
	read := make(chan uint64, 1)
	go func() { _, v := r.List(); read <- v }()
	select {
	case <-read:
		t.Fatal("reader passed uncommitted write")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if v := <-read; v != 2 {
		t.Fatal(v)
	}
	r.persist = writeSnapshot
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			last := uint64(0)
			for i := 0; i < 8; i++ {
				if _, e := r.Register(testInput(testID(100 + n*8 + i))); e != nil {
					t.Error(e)
				}
				rows, v := r.List()
				if v < last || uint64(len(rows)) != v {
					t.Error("regressing or incoherent snapshot")
				}
				last = v
			}
		}(n)
	}
	wg.Wait()
	reopened, e := Open(r.root, testHost)
	if e != nil {
		t.Fatal(e)
	}
	got, v := reopened.List()
	if len(got) != 66 || v != 66 {
		t.Fatal(len(got), v)
	}
	// Actual rename failure leaves the obstructing directory untouched.
	blocked := testOpen(t)
	if e := os.MkdirAll(filepath.Join(blocked.root, "registry.json"), 0700); e != nil {
		t.Fatal(e)
	}
	if c, e := blocked.Register([]byte(testManifest)); c || e == nil {
		t.Fatal(c, e)
	}
	if rows, v := blocked.List(); len(rows) != 0 || v != 0 {
		t.Fatal(rows, v)
	}
}

func TestExhaustionAndCorruption(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	r.snapshot.Revision = maxRevision
	r.snapshot.Records[0].Revision = maxRevision
	if e := writeSnapshot(r.root, r.snapshot); e != nil {
		t.Fatal(e)
	}
	r, e := Open(r.root, testHost)
	if e != nil {
		t.Fatal(e)
	}
	c, e := r.Register([]byte(testManifest))
	if c || e != nil {
		t.Fatal(c, e)
	}
	c, e = r.UpdateManifest(testApp, []byte(testManifest))
	if c || e != nil {
		t.Fatal(c, e)
	}
	for _, op := range []func() (bool, error){func() (bool, error) { return r.Register(testInput(testID(3))) }, func() (bool, error) {
		m := testMap(t)
		m["title"] = "Changed"
		return r.UpdateManifest(testApp, testJSON(t, m))
	}, func() (bool, error) { return r.Remove(testApp) }} {
		if c, e := op(); c || !errors.Is(e, ErrExhausted) {
			t.Fatal(c, e)
		}
	}
	baseline := testBytes(t, filepath.Join(r.root, "registry.json"))
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { m["server_id"] = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa" }, func(m map[string]any) { m["revision"] = 0 }, func(m map[string]any) { m["revision"] = uint64(maxRevision + 1) },
		func(m map[string]any) { rows := m["records"].([]any); m["records"] = append(rows, rows[0]) }, func(m map[string]any) { row := m["records"].([]any)[0].(map[string]any); row["app_id"] = "bad" },
		func(m map[string]any) {
			row := m["records"].([]any)[0].(map[string]any)
			row["manifest"].(map[string]any)["title"] = ""
		}, func(m map[string]any) { m["tombstones"] = []any{map[string]any{"app_id": testApp, "revision": 1}} },
		func(m map[string]any) { m["tombstones"] = []any{map[string]any{"app_id": testID(1), "revision": 0}} }, func(m map[string]any) {
			row := m["records"].([]any)[0].(map[string]any)
			row["revision"] = uint64(maxRevision + 1)
		},
		func(m map[string]any) { row := m["records"].([]any)[0].(map[string]any); row["state"] = "invalid" }, func(m map[string]any) { row := m["records"].([]any)[0].(map[string]any); row["title"] = nil },
		func(m map[string]any) {
			m["tombstones"] = []any{map[string]any{"app_id": testID(1), "revision": uint64(maxRevision + 1)}}
		},
		func(m map[string]any) { m["tombstones"] = []any{map[string]any{"app_id": "bad", "revision": 1}} },
		func(m map[string]any) {
			dead := map[string]any{"app_id": testID(1), "revision": 1}
			m["tombstones"] = []any{dead, dead}
		},
		func(m map[string]any) { m["server_id"] = "bad" },
		func(m map[string]any) { m["records"].([]any)[0].(map[string]any)["revision"] = 0 },
		func(m map[string]any) {
			m["records"].([]any)[0].(map[string]any)["manifest"].(map[string]any)["server_id"] = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
		},
		func(m map[string]any) {
			m["records"].([]any)[0].(map[string]any)["manifest"].(map[string]any)["app_id"] = testID(9)
		},
		func(m map[string]any) { m["records"].([]any)[0].(map[string]any)["desired"] = "invalid" },
		func(m map[string]any) {
			m["records"].([]any)[0].(map[string]any)["last_error"] = map[string]any{"code": "arbitrary", "message": "Failure"}
		},
		func(m map[string]any) {
			rows := []any{}
			for i := 0; i <= maxApps; i++ {
				manifest := testMap(t)
				manifest["app_id"] = testID(i)
				rows = append(rows, map[string]any{"app_id": testID(i), "manifest": manifest, "title": "App", "desired": "available", "state": "stopped", "revision": 1})
			}
			m["records"] = rows
		},
	} {
		var m map[string]any
		d := json.NewDecoder(bytes.NewReader(baseline))
		d.UseNumber()
		if e := d.Decode(&m); e != nil {
			t.Fatal(e)
		}
		mutate(m)
		data := testJSON(t, m)
		path := filepath.Join(r.root, "registry.json")
		if e := os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := Open(r.root, testHost); e == nil {
			t.Fatalf("accepted %s", data)
		}
		if !bytes.Equal(data, testBytes(t, path)) {
			t.Fatal("repaired corruption")
		}
	}
	for _, data := range []string{"", "{}", "null", "{broken", string(baseline) + " {}"} {
		path := filepath.Join(r.root, "registry.json")
		if e := os.WriteFile(path, []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := Open(r.root, testHost); e == nil {
			t.Fatal("accepted", data)
		}
	}
}

func TestTwoHosts(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	other := "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	s, e := Open(filepath.Join(t.TempDir(), "apps"), other)
	if e != nil {
		t.Fatal(e)
	}
	testRegister(t, r, []byte(testManifest))
	testRegister(t, s, bytes.ReplaceAll([]byte(testManifest), []byte(testHost), []byte(other)))
	if c, e := s.Register([]byte(testManifest)); c || !errors.Is(e, ErrForeignHost) {
		t.Fatal(c, e)
	}
	if _, e := Open(r.root, other); !errors.Is(e, ErrForeignHost) {
		t.Fatal(e)
	}
	for _, root := range []string{"relative", ""} {
		if _, e := Open(root, testHost); e == nil {
			t.Fatal(root)
		}
	}
	if _, e := Open(t.TempDir(), "bad"); e == nil {
		t.Fatal("bad host")
	}
}
