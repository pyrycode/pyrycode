package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	hostedtemplate "github.com/pyrycode/pyrycode/templates/hosted-app"
)

func TestCreateAndInspect(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	var changes []Change
	r.Subscribe(func(c Change) { changes = append(changes, c) })
	title := `A "title" </script> & café`
	first, err := r.Create(context.Background(), title)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Create(context.Background(), "Second")
	if err != nil {
		t.Fatal(err)
	}
	if first.AppID == second.AppID || !validID(first.AppID) || !validID(second.AppID) || len(changes) != 2 {
		t.Fatal(first, second, changes)
	}
	for i, source := range []Source{first, second} {
		if source.ServerID != testHost || source.Revision != uint64(i+1) || source.Record.Revision != source.Revision || source.Record.AppID != source.AppID || source.Record.Desired != "available" || source.Record.State != "stopped" || source.Record.ActiveRelease != "" || source.Record.PendingRelease != "" || source.Record.LastError != nil {
			t.Fatal(source)
		}
		if source.SourcePath != filepath.Join(r.root, source.AppID, "source") || source.ManifestPath != filepath.Join(source.SourcePath, "app.json") {
			t.Fatal(source)
		}
		manifest, err := ValidateManifest(testBytes(t, source.ManifestPath), testHost)
		if err != nil || manifest != source.Record.Manifest || manifest.ReleaseVersion() != "1.0.0" {
			t.Fatal(manifest, err)
		}
		var label struct{ Title string }
		if err := json.Unmarshal(testBytes(t, filepath.Join(source.SourcePath, "src/web/title.json")), &label); err != nil || label.Title != manifest.Title() {
			t.Fatal(label, err)
		}
		testCreatedInventory(t, source.SourcePath)
	}
	edited := filepath.Join(first.SourcePath, "src/web/App.tsx")
	if err := os.WriteFile(edited, []byte("editable retained bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(r.root, testHost)
	if err != nil {
		t.Fatal(err)
	}
	registry := testBytes(t, filepath.Join(r.root, "registry.json"))
	for range 2 {
		inspected, err := reopened.Inspect(context.Background(), first.ServerID, first.AppID)
		first.Revision = second.Revision
		if err != nil || !reflect.DeepEqual(inspected, first) || string(testBytes(t, edited)) != "editable retained bytes" || !bytes.Equal(registry, testBytes(t, filepath.Join(r.root, "registry.json"))) {
			t.Fatal(inspected, err)
		}
	}
}

func testCreatedInventory(t *testing.T, root string) {
	t.Helper()
	inventory, err := hostedtemplate.Assets.ReadFile("inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	if err := json.Unmarshal(inventory, &files); err != nil {
		t.Fatal(err)
	}
	var actual []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		actual = append(actual, filepath.ToSlash(rel))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := append(append([]string{}, files...), "app.json", "src/web/title.json")
	sort.Strings(expected)
	sort.Strings(actual)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal(actual, expected)
	}
	for _, name := range files {
		maintained := testBytes(t, filepath.Join("../../templates/hosted-app/source", name))
		bundled, err := hostedtemplate.Assets.ReadFile("source/" + name)
		if err != nil || !bytes.Equal(maintained, bundled) || !bytes.Equal(maintained, testBytes(t, filepath.Join(root, name))) {
			t.Fatalf("asset mismatch %s: %v", name, err)
		}
	}
}

func TestCreateFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("injected diagnostic with private path")
	cases := []struct {
		name                      string
		setup                     func(*testing.T, *Registry)
		title, id                 string
		want                      error
		mintFailure, writeFailure bool
	}{
		{name: "empty title", want: ErrInvalidManifest},
		{name: "surrounding whitespace", title: " bad", want: ErrInvalidManifest},
		{name: "controls", title: "bad\x7f", want: ErrInvalidManifest},
		{name: "oversize", title: strings.Repeat("é", 65), want: ErrInvalidManifest},
		{name: "invalid utf8", title: string([]byte{0xff}), want: ErrInvalidManifest},
		{name: "invalid id", title: "Valid", id: "bad", want: ErrInvalidIdentity},
		{name: "mint failure", title: "Valid", mintFailure: true, want: ErrMintIdentity},
		{name: "live", title: "Valid", id: testApp, want: ErrIdentityUnavailable, setup: func(t *testing.T, r *Registry) { testRegister(t, r, []byte(testManifest)) }},
		{name: "tombstone", title: "Valid", id: testApp, want: ErrIdentityUnavailable, setup: func(t *testing.T, r *Registry) {
			testRegister(t, r, []byte(testManifest))
			c, e := r.Remove(testApp)
			testChange(t, c, e)
		}},
		{name: "destination", title: "Valid", want: ErrIdentityUnavailable, setup: func(t *testing.T, r *Registry) { testRetainedTree(t, filepath.Join(r.root, testID(500))) }},
		{name: "app symlink", title: "Valid", want: ErrIdentityUnavailable, setup: func(t *testing.T, r *Registry) {
			target := testRoot(t)
			testRetainedTree(t, target)
			if e := os.Symlink(target, filepath.Join(r.root, testID(500))); e != nil {
				t.Fatal(e)
			}
		}},
		{name: "dangling symlink", title: "Valid", want: ErrIdentityUnavailable, setup: func(t *testing.T, r *Registry) {
			if e := os.Symlink(filepath.Join(r.root, "missing"), filepath.Join(r.root, testID(500))); e != nil {
				t.Fatal(e)
			}
		}},
		{name: "capacity", title: "Valid", want: ErrCapacity, setup: func(t *testing.T, r *Registry) {
			for n := 1; n < maxApps; n++ {
				testRegister(t, r, testInput(testID(n)))
			}
		}},
		{name: "revision exhausted", title: "Valid", want: ErrExhausted, setup: func(t *testing.T, r *Registry) {
			r.snapshot.Revision = maxRevision
			if e := writeSnapshot(r.root, r.snapshot); e != nil {
				t.Fatal(e)
			}
		}},
		{name: "source write", title: "Valid", want: ErrSourceWrite, writeFailure: true},
		{name: "persistence", title: "Valid", want: ErrCreatePersistence, setup: func(t *testing.T, r *Registry) { r.persist = func(string, snapshot) error { return failure } }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testOpen(t)
			testRegister(t, r, testInput(testID(900)))
			testRetainedTree(t, filepath.Join(r.root, testID(900)))
			if tc.setup != nil {
				tc.setup(t, r)
			}
			before := cloneSnapshot(r.snapshot)
			disk := testBytes(t, filepath.Join(r.root, "registry.json"))
			retained := testTreeBytes(t, r.root)
			notifications := 0
			r.Subscribe(func(Change) { notifications++ })
			id := tc.id
			if id == "" {
				id = testID(500)
			}
			mint := func() (string, error) {
				if tc.mintFailure {
					return "", failure
				}
				return id, nil
			}
			generate := writeSource
			if tc.writeFailure {
				generate = func(ctx context.Context, root, id string, files []sourceFile) error {
					if err := writeSource(ctx, root, id, files[:1]); err != nil {
						return err
					}
					return failure
				}
			}
			got, err := r.create(context.Background(), tc.title, mint, generate)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, Source{}) || notifications != 0 || !reflect.DeepEqual(before, r.snapshot) || !bytes.Equal(disk, testBytes(t, filepath.Join(r.root, "registry.json"))) {
				t.Fatal(got, err, notifications)
			}
			for path, data := range retained {
				if !bytes.Equal(data, testBytes(t, path)) {
					t.Fatalf("retained file changed: %s", path)
				}
			}
			if strings.Contains(err.Error(), failure.Error()) {
				t.Fatal("unsafe diagnostic exposed", err)
			}
			if tc.writeFailure || tc.name == "persistence" {
				if _, e := os.Stat(filepath.Join(r.root, id, "source")); e != nil {
					t.Fatal(e)
				}
				if _, e := r.Inspect(context.Background(), testHost, id); !errors.Is(e, ErrNotFound) {
					t.Fatal(e)
				}
				partial := testTreeBytes(t, r.root)
				_, e := r.create(context.Background(), "Retry", mint, writeSource)
				if !errors.Is(e, ErrIdentityUnavailable) {
					t.Fatal(e)
				}
				for path, data := range partial {
					if !bytes.Equal(data, testBytes(t, path)) {
						t.Fatal("retry overwrote", path)
					}
				}
			}
		})
	}
}

func testRetainedTree(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"source/app.json", "builds/retained/file", "data/app.sqlite"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("retained "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func testTreeBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	if err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type().IsRegular() {
			result[path] = testBytes(t, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestInspectRejectedIdentity(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	created, err := r.Create(context.Background(), "App")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host, id string
		want     error
	}{
		{testID(42), created.AppID, ErrForeignHost}, {testHost, testID(42), ErrNotFound}, {testHost, "bad", ErrInvalidIdentity}, {"bad", created.AppID, ErrInvalidIdentity},
	} {
		if got, e := r.Inspect(context.Background(), tc.host, tc.id); !errors.Is(e, tc.want) || !reflect.DeepEqual(got, Source{}) {
			t.Fatal(got, e)
		}
	}
	c, e := r.Remove(created.AppID)
	testChange(t, c, e)
	if _, e := r.Inspect(context.Background(), testHost, created.AppID); !errors.Is(e, ErrRemoved) {
		t.Fatal(e)
	}
}

func TestConcurrentCreate(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	var wg sync.WaitGroup
	results := make(chan Source, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, e := r.Create(context.Background(), "Concurrent")
			if e != nil {
				t.Error(e)
				return
			}
			results <- s
		}()
	}
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	revisions := map[uint64]bool{}
	for s := range results {
		if ids[s.AppID] || revisions[s.Revision] {
			t.Fatal("reused identity/revision")
		}
		ids[s.AppID] = true
		revisions[s.Revision] = true
	}
	if len(ids) != 8 {
		t.Fatal(len(ids))
	}
}

func TestCreateOutsideCheckout(t *testing.T) {
	if os.Getenv("PYRY_CREATE_HELPER") == "1" {
		r, err := Open(filepath.Join(os.Getenv("PYRY_CREATE_ROOT"), "apps"), testHost)
		if err != nil {
			t.Fatal(err)
		}
		created, err := r.Create(context.Background(), "Outside checkout")
		if err != nil {
			t.Fatal(err)
		}
		t.Log("BUNDLED_CREATE_OK", created.AppID)
		return
	}
	t.Parallel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	outside := testRoot(t)
	cmd := exec.Command(binary, "-test.run=^TestCreateOutsideCheckout$", "-test.v")
	cmd.Dir = outside
	cmd.Env = []string{"PYRY_CREATE_HELPER=1", "PYRY_CREATE_ROOT=" + outside, "PATH="}
	output, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("BUNDLED_CREATE_OK")) {
		t.Fatal(string(output), err)
	}
	paths, err := filepath.Glob(filepath.Join(outside, "apps", "*", "source"))
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	testCreatedInventory(t, paths[0])
}

func TestCreateCancelled(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Create(ctx, "App"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestSourceSymlinkRetained(t *testing.T) {
	t.Parallel()
	root, target := testRoot(t), testRoot(t)
	testRetainedTree(t, target)
	if err := os.Mkdir(filepath.Join(root, testApp), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, testApp, "source")); err != nil {
		t.Fatal(err)
	}
	if _, err := readSourceManifest(root, testApp); err == nil {
		t.Fatal("followed source symlink")
	}
	if string(testBytes(t, filepath.Join(target, "source/app.json"))) != "retained source/app.json" {
		t.Fatal("external contents changed")
	}
}
