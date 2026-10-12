package hostedtemplate

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestBundleInventory(t *testing.T) {
	t.Parallel()
	want := testBundleFiles(t)
	var got []string
	if err := fs.WalkDir(Assets, ".", func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			got = append(got, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("compiled assets = %v, want only maintained inventory %v", got, want)
	}
}

func testBundleFiles(t *testing.T) []string {
	t.Helper()
	data, err := Assets.ReadFile("inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		t.Fatal(err)
	}
	files := []string{"inventory.json", "manifest.json"}
	for _, name := range names {
		files = append(files, "source/"+name)
	}
	slices.Sort(files)
	return files
}

func TestBundleExcludesWorkingFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range testBundleFiles(t) {
		data, err := Assets.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		write(name, data)
	}
	for _, name := range []string{"bundle.go", "bundle_test.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		write(name, data)
	}
	write("go.mod", []byte("module bundleprobe\n\ngo 1.26.2\n"))
	for _, name := range []string{
		"source/.env", "source/.env.local", "source/data/app.sqlite",
		"source/data/app.sqlite-wal", "source/data/app.sqlite-shm",
		"source/node_modules/example/index.js", "source/dist/service/main.js",
		"source/scripts/private.mjs", "source/src/service/private.mjs",
		"source/src/web/private.tsx", "source/sql/migrations/private.sql",
		"source/test/private.test.mjs", "source/.private",
	} {
		write(name, []byte("SYNTHETIC_PRIVATE_WORKING_FILE"))
	}
	// Recompile the production declaration with private files present, then inspect
	// the embedded filesystem itself rather than filtering generated output.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-run", "^TestBundleInventory$", ".")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bundle compiled from a populated working tree: %v\n%s", err, output)
	}
}
